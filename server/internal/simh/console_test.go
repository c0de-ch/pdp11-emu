package simh

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"pdp11emu/server/internal/api"
)

// consolePeer is the simulator end of a console connection.
type consolePeer struct {
	l     net.Listener
	mu    sync.Mutex
	conns []net.Conn
	got   bytes.Buffer
	times []time.Time // arrival time of every byte
}

func newConsolePeer(t *testing.T) *consolePeer {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &consolePeer{l: l}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			p.conns = append(p.conns, c)
			p.mu.Unlock()
			go func() {
				buf := make([]byte, 1024)
				for {
					n, err := c.Read(buf)
					p.mu.Lock()
					p.got.Write(buf[:n])
					for i := 0; i < n; i++ {
						p.times = append(p.times, time.Now())
					}
					p.mu.Unlock()
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	t.Cleanup(func() {
		l.Close()
		p.mu.Lock()
		for _, c := range p.conns {
			c.Close()
		}
		p.mu.Unlock()
	})
	return p
}

func (p *consolePeer) dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp", p.l.Addr().String())
}

func (p *consolePeer) received() (string, []time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.got.String(), append([]time.Time(nil), p.times...)
}

func (p *consolePeer) send(t *testing.T, s string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		n := len(p.conns)
		var c net.Conn
		if n > 0 {
			c = p.conns[n-1]
		}
		p.mu.Unlock()
		if c != nil {
			if _, err := io.WriteString(c, s); err == nil {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no connection to send on")
}

func attachedConsole(t *testing.T, cps int) (*console, *consolePeer) {
	t.Helper()
	p := newConsolePeer(t)
	c := newConsole(testLogger(t), cps)
	conn, err := p.dial(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	c.attach(conn, p.dial)
	t.Cleanup(c.detach)
	return c, p
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestConsoleInputPacing(t *testing.T) {
	const cps = 200
	c, p := attachedConsole(t, cps)
	c.setHold(false)
	input := strings.Repeat("abcdefghij", 10) // 100 bytes
	start := time.Now()
	if n, err := c.Write([]byte(input)); n != 100 || err != nil {
		t.Fatalf("Write = %d, %v", n, err)
	}
	waitUntil(t, "all input", func() bool { s, _ := p.received(); return len(s) == 100 })
	got, times := p.received()
	if got != input {
		t.Fatalf("received %q", got)
	}
	// The first 32 bytes go out at once, the rest at about cps.
	if d := times[inputBurst-1].Sub(start); d > 100*time.Millisecond {
		t.Fatalf("burst took %s", d)
	}
	paced := times[99].Sub(times[inputBurst-1])
	want := time.Duration(float64(100-inputBurst) / cps * float64(time.Second))
	if paced < want*8/10 || paced > want*2 {
		t.Fatalf("paced part took %s, want about %s", paced, want)
	}
	if c.Queued() != 0 {
		t.Fatalf("queued %d", c.Queued())
	}
	// After a pause the bucket is full again: typing is never delayed.
	time.Sleep(200 * time.Millisecond)
	start = time.Now()
	c.Write([]byte("0123456789"))
	waitUntil(t, "typed input", func() bool { s, _ := p.received(); return len(s) == 110 })
	if _, times = p.received(); times[109].Sub(start) > 50*time.Millisecond {
		t.Fatalf("typing delayed by %s", times[109].Sub(start))
	}
}

func TestConsoleHoldQueueCancel(t *testing.T) {
	c, p := attachedConsole(t, 1000)
	// Held (CPU not running): input waits in the queue.
	c.Write([]byte("hello"))
	time.Sleep(50 * time.Millisecond)
	if s, _ := p.received(); s != "" || c.Queued() != 5 {
		t.Fatalf("sent %q while held, queued %d", s, c.Queued())
	}
	c.CancelInput()
	if c.Queued() != 0 {
		t.Fatal("CancelInput kept input")
	}
	big := make([]byte, inputQueueMax)
	if n, err := c.Write(big); n != len(big) || err != nil {
		t.Fatalf("Write(64 KiB) = %d, %v", n, err)
	}
	if n, err := c.Write([]byte("x")); n != 0 || !errors.Is(err, api.ErrInputFull) {
		t.Fatalf("Write beyond the limit = %d, %v", n, err)
	}
	c.CancelInput()
	c.Write([]byte("world"))
	c.setHold(false)
	waitUntil(t, "released input", func() bool { s, _ := p.received(); return s == "world" })
}

func TestConsoleOutputHistory(t *testing.T) {
	c, p := attachedConsole(t, 1000)
	if !c.Connected() {
		t.Fatal("not connected")
	}
	// Subscribers registered at any moment see history + live output that
	// together form the exact stream (SubscribeHistory is atomic).
	var stream bytes.Buffer
	for i := 0; i < 200; i++ {
		stream.WriteString(strings.Repeat(string(rune('a'+i%26)), i%37+1))
	}
	type view struct {
		mu   sync.Mutex
		buf  bytes.Buffer
		hist []byte
	}
	var views []*view
	var vmu sync.Mutex
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			v := &view{}
			h, _ := c.SubscribeHistory(func(b []byte) { v.mu.Lock(); v.buf.Write(b); v.mu.Unlock() })
			v.hist = h
			vmu.Lock()
			views = append(views, v)
			vmu.Unlock()
			time.Sleep(time.Millisecond)
		}
	}()
	for _, chunk := range strings.SplitAfter(stream.String(), "q") {
		p.send(t, chunk)
		time.Sleep(200 * time.Microsecond)
	}
	<-done
	all := stream.String()
	waitUntil(t, "output", func() bool {
		h, cancel := c.SubscribeHistory(func([]byte) {})
		cancel()
		return string(h) == all
	})
	for i, v := range views {
		v.mu.Lock()
		got := string(v.hist) + v.buf.String()
		v.mu.Unlock()
		if got != all {
			t.Fatalf("view %d: history %d + live %d bytes != stream (%d bytes)", i, len(v.hist), v.buf.Len(), len(all))
		}
	}
}

func TestConsoleHistoryWraps(t *testing.T) {
	c := newConsole(testLogger(t), 0)
	var all bytes.Buffer
	for i := 0; all.Len() < historySize+50000; i++ {
		chunk := bytes.Repeat([]byte{byte('A' + i%26)}, 1000+i%977)
		all.Write(chunk)
		c.output(chunk)
	}
	h, cancel := c.SubscribeHistory(func([]byte) {})
	cancel()
	want := all.Bytes()[all.Len()-historySize:]
	if !bytes.Equal(h, want) {
		t.Fatalf("history of %d bytes is not the newest %d", len(h), historySize)
	}
}

func TestReadyMatcher(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		chunks  []string
		fires   bool
	}{
		{"plain", "login: ", []string{"2.11 BSD UNIX (pidp) (console)\r\n\r\nlogin: "}, true},
		{"split", "login: ", []string{"log", "in", ": "}, true},
		{"byte by byte", "login: ", strings.Split("xxlogin: ", ""), true},
		{"fill characters", "login: ", []string{"lo\x00\x7fgi", "\x00n: "}, true},
		{"absent", "login: ", []string{"logon: ", "login:"}, false},
		{"one byte", ".", []string{"RT-11", "SJ  V05.03\r\n."}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			n := 0
			var r readyMatcher
			r.arm(tc.pattern, func() { n++ })
			for _, c := range tc.chunks {
				r.feed([]byte(c))
			}
			if want := map[bool]int{true: 1, false: 0}[tc.fires]; n != want {
				t.Fatalf("fired %d times, want %d", n, want)
			}
			r.feed([]byte(tc.pattern)) // fires once at most: then it is disarmed
			if n != 1 {
				t.Fatalf("fired %d times in total", n)
			}
		})
	}
	var r readyMatcher
	r.feed([]byte("login: ")) // not armed
	r.arm("", func() { t.Fatal("empty pattern fired") })
	r.feed([]byte("anything"))
}

func TestConsoleReconnect(t *testing.T) {
	c, p := attachedConsole(t, 1000)
	c.setHold(false)
	p.send(t, "before")
	p.mu.Lock()
	first := p.conns[0]
	p.mu.Unlock()
	first.Close() // the simulator dropped the connection
	waitUntil(t, "reconnect", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return len(p.conns) == 2 && c.Connected()
	})
	p.send(t, "after")
	waitUntil(t, "output after reconnect", func() bool {
		h, cancel := c.SubscribeHistory(func([]byte) {})
		cancel()
		return string(h) == "beforeafter"
	})
	c.Write([]byte("typed"))
	waitUntil(t, "input after reconnect", func() bool { s, _ := p.received(); return s == "typed" })
	c.detach()
	if c.Connected() {
		t.Fatal("connected after detach")
	}
	if _, err := c.Write([]byte("x")); !errors.Is(err, api.ErrNotPowered) {
		t.Fatalf("Write after detach: %v", err)
	}
}
