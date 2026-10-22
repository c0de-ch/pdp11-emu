package web

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"pdp11emu/server/internal/api"
	"pdp11emu/server/internal/panel/paneltest"
)

func TestConnectOrder(t *testing.T) {
	e := newWebEnv(t, webCfg{})
	e.m.Stream().EmitString("2.11 BSD UNIX\r\nlogin: ")
	e.lg.Add("PDP-11 simulator Open SIMH V4.1-0")
	e.lg.Add("sim> boot rq0")
	// wait until the panel published a frame
	paneltest.WaitFor(5*time.Second, func() bool { e.s.hub.mu.Lock(); defer e.s.hub.mu.Unlock(); return e.s.hub.lastFrame != nil })

	w := e.dial("sub=panel,console,log", nil)
	hello := w.next().json()
	if hello["type"] != "hello" || hello["protocol"] != 1.0 || hello["server"] != testVersion() || hello["lamps"] != 64.0 ||
		hello["frameHz"] != 100.0 || hello["auth"] != false || hello["insecure"] != true || hello["clientId"].(float64) < 1 {
		t.Fatalf("hello = %v", hello)
	}
	st := w.next().json()
	if st["type"] != "state" || st["run"] != "running" || st["power"] != "on" || st["system"] != "bsd211" ||
		st["running"] != "bsd211" || st["speed"] != "fast" || st["viewers"] != 1.0 || st["needsInstall"] != nil {
		t.Fatalf("state = %v", st)
	}
	if get(st, "controls", "key") != "power" || get(st, "controls", "pressed", "EXAM") != false {
		t.Fatalf("controls = %v", st["controls"])
	}
	sys := w.next().json()
	if sys["type"] != "systems" || sys["selected"] != "bsd211" || len(sys["list"].([]any)) != 2 {
		t.Fatalf("systems = %v", sys)
	}
	bsd, rt := sys["list"].([]any)[0].(map[string]any), sys["list"].([]any)[1].(map[string]any)
	if c := bsd["credentials"]; c == nil {
		t.Fatal("credentials encoded as null")
	}
	// Wire names the UI relies on (R2): noLogin and bootTimeoutSec.
	if bsd["noLogin"] != false || bsd["bootTimeoutSec"] != 90.0 || rt["noLogin"] != true || rt["bootTimeoutSec"] != 60.0 {
		t.Fatalf("noLogin/bootTimeoutSec: bsd211 %v/%v, rt11 %v/%v", bsd["noLogin"], bsd["bootTimeoutSec"], rt["noLogin"], rt["bootTimeoutSec"])
	}
	frame := w.next()
	if frame.msgType() != "#frame" || len(frame.data) != 68 || frame.data[1] != 1 {
		t.Fatalf("panel history = %x", frame.data)
	}
	con := w.next()
	if con.msgType() != "#console" || string(con.data[1:]) != "2.11 BSD UNIX\r\nlogin: " {
		t.Fatalf("console history = %q", con.data)
	}
	lg := w.next().json()
	if lg["type"] != "log" || fmt.Sprint(lg["lines"]) != "[PDP-11 simulator Open SIMH V4.1-0 sim> boot rq0]" {
		t.Fatalf("log history = %v", lg)
	}
	// Then live data.
	e.m.Stream().EmitString("root\r\n")
	if got := w.await("#console", nil); string(got.data[1:]) != "root\r\n" {
		t.Fatalf("live console = %q", got.data)
	}
	e.lg.Add("live line")
	if got := w.await("log", nil).json(); fmt.Sprint(got["lines"]) != "[live line]" {
		t.Fatalf("live log = %v", got)
	}
}

func TestDefaultSubscriptionAndEmptyHistory(t *testing.T) {
	e := newWebEnv(t, webCfg{})
	w := e.dial("", nil) // default: panel,console
	for _, want := range []string{"hello", "state", "systems", "#frame", "#console"} {
		if got := w.next(); got.msgType() != want {
			t.Fatalf("got %s, want %s", got.msgType(), want)
		}
	}
	// An empty console history is still sent (the client resets its terminal).
	w2 := e.dial("sub=console", nil)
	w2.next()
	w2.next()
	w2.next()
	if h := w2.next(); h.msgType() != "#console" || len(h.data) != 1 {
		t.Fatalf("empty history = %x", h.data)
	}
	// Unknown channels are reported and ignored.
	w3 := e.dial("sub=panel,tty0", nil)
	w3.skipInit(0)
	w3.toast("tty0")
}

// TestHistoryThenLive connects clients while the guest prints: every
// client must see one contiguous run of the output (history, then live),
// without loss or duplication.
func TestHistoryThenLive(t *testing.T) {
	e := newWebEnv(t, webCfg{})
	stream := e.m.Stream()
	var (
		mu  sync.Mutex
		all bytes.Buffer
	)
	stop := make(chan struct{})
	printer := make(chan struct{})
	go func() {
		defer close(printer)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			line := fmt.Sprintf("%07d\r\n", i)
			mu.Lock()
			all.WriteString(line)
			stream.EmitString(line) // under mu: the order of all is the order of the stream
			mu.Unlock()
			if i%50 == 0 {
				time.Sleep(time.Millisecond)
			}
		}
	}()
	var clients []*wsClient
	for i := 0; i < 8; i++ {
		time.Sleep(time.Duration(5+rand.IntN(20)) * time.Millisecond)
		clients = append(clients, e.dial("sub=console", nil))
	}
	time.Sleep(100 * time.Millisecond)
	close(stop)
	<-printer
	full := all.Bytes()
	for i, w := range clients {
		w.skipInit(0)
		var got []byte
		first := true
		for _, m := range w.drain(500 * time.Millisecond) {
			if m.msgType() != "#console" {
				continue
			}
			if first && len(m.data) == 1 {
				t.Fatalf("client %d: empty history although output existed", i)
			}
			first = false
			got = append(got, m.data[1:]...)
		}
		if len(got) == 0 || !bytes.HasSuffix(full, got) {
			t.Fatalf("client %d: received %d bytes that are not the end of the output (%d bytes); starts %q",
				i, len(got), len(full), got[:min(len(got), 40)])
		}
	}
}

func TestLogHistoryThenLive(t *testing.T) {
	e := newWebEnv(t, webCfg{})
	for i := 0; i < 50; i++ {
		e.lg.Add(fmt.Sprintf("line %d", i))
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	n := 50
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			e.lg.Add(fmt.Sprintf("line %d", n))
			n++
			time.Sleep(50 * time.Microsecond)
		}
	}()
	time.Sleep(10 * time.Millisecond)
	w := e.dial("sub=log", nil)
	time.Sleep(50 * time.Millisecond)
	close(stop)
	<-done
	w.skipInit(0)
	var lines []string
	for _, m := range w.drain(300 * time.Millisecond) {
		if m.msgType() == "log" {
			for _, l := range m.json()["lines"].([]any) {
				lines = append(lines, l.(string))
			}
		}
	}
	// History is the last logHistoryLines lines at subscription time; the
	// run must be contiguous up to the last line.
	if len(lines) < 50 {
		t.Fatalf("only %d lines", len(lines))
	}
	var first int
	fmt.Sscanf(lines[0], "line %d", &first)
	for i, l := range lines {
		if want := fmt.Sprintf("line %d", first+i); l != want {
			t.Fatalf("line %d = %q, want %q (lost or duplicated)", i, l, want)
		}
	}
	if last := lines[len(lines)-1]; last != fmt.Sprintf("line %d", n-1) {
		t.Fatalf("last line %q, want line %d", last, n-1)
	}
}

// countingLog counts the lines its SubscribeHistory subscribers got.
type countingLog struct {
	api.LogBuffer
	mu      sync.Mutex
	history int // lines returned as history
	live    int // lines passed to the subscriber
}

func (l *countingLog) SubscribeHistory(n int, fn func([]string)) ([]string, func()) {
	hist, cancel := l.LogBuffer.SubscribeHistory(n, func(lines []string) {
		l.mu.Lock()
		l.live += len(lines)
		l.mu.Unlock()
		fn(lines)
	})
	l.mu.Lock()
	l.history += len(hist)
	l.mu.Unlock()
	return hist, cancel
}

// TestLogHistoryThenLiveIdenticalLines: identical lines at the boundary
// between history and live data are neither dropped nor duplicated (the
// history and the subscription are one atomic step).
func TestLogHistoryThenLiveIdenticalLines(t *testing.T) {
	var cl *countingLog
	e := newWebEnv(t, webCfg{logWrap: func(l api.LogBuffer) api.LogBuffer { cl = &countingLog{LogBuffer: l}; return cl }})
	for i := 0; i < 20; i++ {
		e.lg.Add("sim> examine PC")
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			e.lg.Add("sim> examine PC")
			time.Sleep(20 * time.Microsecond)
		}
	}()
	time.Sleep(5 * time.Millisecond)
	w := e.dial("sub=log", nil)
	time.Sleep(50 * time.Millisecond)
	close(stop)
	<-done
	w.skipInit(0)
	got := 0
	for _, m := range w.drain(300 * time.Millisecond) {
		if m.msgType() == "log" {
			got += len(m.json()["lines"].([]any))
		}
	}
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if cl.live == 0 {
		t.Fatal("no live lines during the subscription: the test did not overlap")
	}
	if want := cl.history + cl.live; got != want {
		t.Fatalf("client got %d lines, want %d (%d history + %d live)", got, want, cl.history, cl.live)
	}
}

func TestPanelFrames(t *testing.T) {
	e := newWebEnv(t, webCfg{})
	w := e.dial("sub=panel", nil)
	w.skipInit(1)
	e.src.Push(api.Sample{Addr: 0o1234, Weight: 10})
	var seq uint16
	f := w.await("#frame", func(m rx) bool { return m.data[4+2] == 255 && m.data[4+3] == 255 })
	seq = binary.LittleEndian.Uint16(f.data[2:])
	e.src.Push(api.Sample{Addr: 0o7, Weight: 10})
	f2 := w.await("#frame", func(m rx) bool { return m.data[4+0] == 255 && m.data[4+3] == 0 })
	if s2 := binary.LittleEndian.Uint16(f2.data[2:]); s2 <= seq {
		t.Fatalf("sequence %d after %d", s2, seq)
	}
	// Unsubscribe: no more frames; resubscribe: the latest frame first.
	w.send(map[string]any{"type": "subscribe", "sub": []string{}})
	time.Sleep(50 * time.Millisecond)
	w.drain(50 * time.Millisecond)
	e.src.Push(api.Sample{Addr: 0o70, Weight: 10})
	for _, m := range w.drain(200 * time.Millisecond) {
		if m.msgType() == "#frame" {
			t.Fatal("frame after unsubscribe")
		}
	}
	w.send(map[string]any{"type": "subscribe", "sub": []string{"panel"}})
	if f := w.next(); f.msgType() != "#frame" || f.data[4+3] != 255 {
		t.Fatalf("resubscribe: %s %v", f.msgType(), f.data)
	}
}

func TestResubscribeConsoleSendsHistoryAgain(t *testing.T) {
	e := newWebEnv(t, webCfg{})
	e.m.Stream().EmitString("abc")
	w := e.dial("sub=console", nil)
	w.skipInit(1)
	w.send(map[string]any{"type": "subscribe", "sub": []string{"console", "panel"}}) // console already on: no history
	for {
		m := w.next()
		if m.msgType() == "#console" {
			t.Fatal("console history sent again for a channel that stayed subscribed")
		}
		if m.msgType() == "#frame" {
			break
		}
	}
	w.send(map[string]any{"type": "subscribe", "sub": []string{"panel"}})
	w.send(map[string]any{"type": "subscribe", "sub": []string{"panel", "console"}})
	h := w.await("#console", nil)
	if string(h.data[1:]) != "abc" {
		t.Fatalf("history again = %q", h.data)
	}
}

func TestClientMessages(t *testing.T) {
	e := newWebEnv(t, webCfg{})
	w := e.dial("sub=console", nil)
	w.skipInit(1)
	other := e.dial("sub=console", nil)
	other.skipInit(1)

	w.send(map[string]any{"type": "sr", "value": 0o1234567})
	w.state(func(s map[string]any) bool { return get(s, "controls", "sr") == float64(0o1234567) })
	if sr, _ := e.m.SR(); sr != 0o34567 {
		t.Fatalf("machine SR = %o", sr)
	}

	w.send(map[string]any{"type": "knob", "name": "addr", "pos": 3})
	w.state(func(s map[string]any) bool { return get(s, "controls", "addrKnob") == 3.0 })
	w.send(map[string]any{"type": "knob", "name": "data", "pos": 7})
	w.state(func(s map[string]any) bool { return get(s, "controls", "dataKnob") == 7.0 })

	// A console switch while running: the hint goes to the sender only, with req.
	w.send(map[string]any{"type": "momentary", "name": "EXAM", "active": true, "req": "r-exam"})
	tm := w.toast("halted")
	if tm["req"] != "r-exam" || tm["level"] != "warn" {
		t.Fatalf("toast %v", tm)
	}
	w.send(map[string]any{"type": "momentary", "name": "EXAM", "active": false})
	w.send(map[string]any{"type": "momentary", "name": "LAMP_TEST", "active": true})
	w.state(func(s map[string]any) bool { return get(s, "controls", "lampTest") == true })
	w.send(map[string]any{"type": "momentary", "name": "LAMP_TEST", "active": false})

	w.send(map[string]any{"type": "latch", "name": "ENABLE_HALT", "value": "halt"})
	w.state(func(s map[string]any) bool {
		return s["run"] == "halted" && get(s, "console", "lastStop") == "Front panel HALT"
	})
	w.send(map[string]any{"type": "latch", "name": "SINST_SBUS", "value": "bus"})
	w.state(func(s map[string]any) bool { return get(s, "controls", "sinstSbus") == "bus" })
	w.send(map[string]any{"type": "latch", "name": "SINST_SBUS", "value": "inst"})
	w.send(map[string]any{"type": "knob", "name": "addr", "pos": 0})
	w.send(map[string]any{"type": "momentary", "name": "EXAM", "active": true})
	if !paneltest.WaitFor(5*time.Second, func() bool { return e.m.Count("Examine") == 1 }) {
		t.Fatal("EXAM did not reach the machine")
	}
	w.send(map[string]any{"type": "momentary", "name": "EXAM", "active": false})
	w.send(map[string]any{"type": "latch", "name": "ENABLE_HALT", "value": "enable"})

	w.send(map[string]any{"type": "boot"})
	if !paneltest.WaitFor(5*time.Second, func() bool { return e.m.Count("Boot") == 1 }) {
		t.Fatal("boot did not reach the machine")
	}
	w.state(func(s map[string]any) bool { return s["run"] == "running" })

	w.send(map[string]any{"type": "speed", "value": "authentic"})
	w.state(func(s map[string]any) bool { return s["speed"] == "authentic" })
	if !paneltest.WaitFor(5*time.Second, func() bool { return e.m.Speed() == api.SpeedAuthentic }) {
		t.Fatal("speed not applied to the running machine")
	}
	if e.sys.SpeedFor("bsd211") != api.SpeedAuthentic {
		t.Fatal("speed not persisted for the selected system")
	}

	// Console input and the answerer.
	w.sendRaw(websocket.MessageBinary, []byte("\x10ls\r"))
	if !paneltest.WaitFor(5*time.Second, func() bool { return string(e.m.Stream().Input()) == "ls\r" }) {
		t.Fatalf("console input = %q", e.m.Stream().Input())
	}
	e.m.Stream().SetQueued(42)
	st := w.state(func(s map[string]any) bool { return get(s, "console", "inputQueued") == 42.0 })
	if get(st, "console", "answerer") == 0.0 {
		t.Fatalf("no answerer: %v", st["console"])
	}
	w.send(map[string]any{"type": "inputCancel"})
	if !paneltest.WaitFor(5*time.Second, func() bool { return e.m.Stream().Cancels() == 1 }) {
		t.Fatal("inputCancel not forwarded")
	}

	w.send(map[string]any{"type": "ping"})
	w.await("pong", nil)

	w.send(map[string]any{"type": "cancel", "task": "install", "id": "rt11"})
	if !paneltest.WaitFor(5*time.Second, func() bool { return strings.Contains(fmt.Sprint(e.sys.Calls()), "Cancel rt11") }) {
		t.Fatal("cancel not forwarded")
	}

	// Selecting needs the machine off.
	w.send(map[string]any{"type": "selectSystem", "id": "rt11", "req": "sel"})
	if tm := w.toast("Power the machine off"); tm["req"] != "sel" {
		t.Fatalf("toast %v", tm)
	}
	w.send(map[string]any{"type": "key", "pos": "off"})
	w.state(func(s map[string]any) bool { return s["run"] == "off" && s["power"] == "off" })
	w.send(map[string]any{"type": "selectSystem", "id": "rt11"})
	w.await("systems", func(m rx) bool { return m.json()["selected"] == "rt11" })
	w.state(func(s map[string]any) bool { return s["system"] == "rt11" })
	// The other client sees selection changes too, but none of w's hints.
	other.await("systems", func(m rx) bool { return m.json()["selected"] == "rt11" })
	for _, m := range other.drain(100 * time.Millisecond) {
		if m.msgType() == "toast" {
			t.Fatalf("other client got a toast meant for the sender: %s", m.data)
		}
	}
	// Machine errors go to everyone.
	e.m.EmitError("disk image missing")
	w.toast("disk image missing")
	other.toast("disk image missing")
}

func TestInvalidMessages(t *testing.T) {
	e := newWebEnv(t, webCfg{})
	w := e.dial("sub=panel", nil)
	w.skipInit(1)
	for _, msg := range []string{
		`not json`,
		`{"type": 5}`,
		`{"req": "x"}`,
		`{"type": "teleport", "req": "x"}`,
		`{"type": "sr", "value": 4194304, "req": "x"}`,
		`{"type": "sr", "value": "12", "req": "x"}`,
		`{"type": "sr", "value": 1.5, "req": "x"}`,
		`{"type": "sr", "req": "x"}`,
		`{"type": "momentary", "name": "EXAM", "req": "x"}`,
		`{"type": "momentary", "name": "SELF_DESTRUCT", "active": true, "req": "x"}`,
		`{"type": "latch", "name": "ENABLE_HALT", "value": "maybe", "req": "x"}`,
		`{"type": "latch", "name": "ENABLE_HALT", "value": 1, "req": "x"}`,
		`{"type": "knob", "name": "addr", "pos": 8, "req": "x"}`,
		`{"type": "knob", "name": "volume", "pos": 1, "req": "x"}`,
		`{"type": "key", "pos": "on", "req": "x"}`,
		`{"type": "selectSystem", "id": "../etc", "req": "x"}`,
		`{"type": "install", "id": "nope", "req": "x"}`,
		`{"type": "cancel", "task": "reboot", "id": "rt11", "req": "x"}`,
		`{"type": "speed", "value": "warp", "req": "x"}`,
		`{"type": "subscribe", "req": "x"}`,
		`{"type": "retryPower", "req": "x"}`,
	} {
		w.sendRaw(websocket.MessageText, []byte(msg))
		tm := w.await("toast", nil).json()
		if tm["level"] != "error" {
			t.Errorf("%s: toast %v", msg, tm)
		}
		if strings.Contains(msg, `"req": "x"`) && tm["req"] != "x" {
			t.Errorf("%s: req not echoed: %v", msg, tm)
		}
	}
	w.sendRaw(websocket.MessageBinary, []byte{0x20, 1})
	w.toast("Unsupported binary")
	if c := e.p.Controls(); c.SR != 0 || c.AddrKnob != 0 || c.Key != "power" {
		t.Fatalf("invalid messages changed controls: %+v", c)
	}
}

func TestConsoleInputWhileOff(t *testing.T) {
	e := newWebEnv(t, webCfg{key: "off"})
	w := e.dial("sub=console", nil)
	w.skipInit(1)
	for i := 0; i < 5; i++ {
		w.sendRaw(websocket.MessageBinary, []byte("\x10x"))
	}
	w.toast("machine is off")
	for _, m := range w.drain(200 * time.Millisecond) {
		if m.msgType() == "toast" {
			t.Fatal("console input errors are not rate limited")
		}
	}
}

func TestAnswerer(t *testing.T) {
	e := newWebEnv(t, webCfg{})
	a := e.dial("sub=console", nil)
	a.skipInit(1)
	idA := 1.0
	b := e.dial("sub=console", nil)
	b.skipInit(1)
	p := e.dial("sub=panel", nil) // not a console subscriber
	p.skipInit(1)
	answerer := func(w *wsClient, want float64) {
		t.Helper()
		w.state(func(s map[string]any) bool { return get(s, "console", "answerer") == want })
	}
	answerer(p, idA) // the oldest console subscriber
	b.sendRaw(websocket.MessageBinary, []byte("\x10x"))
	answerer(p, 2)                                      // the last to type
	p.sendRaw(websocket.MessageBinary, []byte("\x10y")) // types but has no terminal
	time.Sleep(100 * time.Millisecond)
	answerer(p, 1)
	_ = b.c.Close(websocket.StatusNormalClosure, "")
	answerer(a, idA)
	a.send(map[string]any{"type": "subscribe", "sub": []string{"panel"}})
	answerer(p, 0)
}

func TestProgressFanOutAndRetryPower(t *testing.T) {
	e := newWebEnv(t, webCfg{key: "off"})
	w := e.dial("sub=panel", nil)
	w.skipInit(1)
	other := e.dial("sub=panel", nil)
	other.skipInit(1)
	// rt11 needs a licence: power-on with it selected asks for an install.
	w.send(map[string]any{"type": "selectSystem", "id": "rt11"})
	w.state(func(s map[string]any) bool { return s["system"] == "rt11" })
	e.m.Lock()
	e.m.NoSystem = true
	e.m.Unlock()
	w.send(map[string]any{"type": "key", "pos": "power"})
	st := w.state(func(s map[string]any) bool { return s["needsInstall"] != nil })
	if get(st, "needsInstall", "id") != "rt11" || get(st, "needsInstall", "class") != "hobbyist" || st["error"] == "" {
		t.Fatalf("needsInstall state: %v", st)
	}
	// Installing without accepting the licence fails (sender only).
	w.send(map[string]any{"type": "install", "id": "rt11", "req": "i1"})
	if tm := w.toast("licence"); tm["req"] != "i1" {
		t.Fatalf("toast %v", tm)
	}
	e.sys.SetInstallFunc(func(ctx context.Context, id string, accept bool, req string) error {
		for _, ph := range []string{"download", "download", "download", "verify", "extract"} {
			e.sys.Progress(api.Progress{Task: "install", ID: id, Phase: ph, Req: req})
		}
		e.m.Lock()
		e.m.NoSystem = false
		e.m.Unlock()
		e.sys.SetInstalled(id, true)
		e.sys.Progress(api.Progress{Task: "install", ID: id, Phase: "done", Pct: 100, Req: req})
		return nil
	})
	w.send(map[string]any{"type": "install", "id": "rt11", "acceptLicense": true, "req": "i2"})
	for _, c := range []*wsClient{w, other} {
		var phases []string
		for {
			m := c.await("progress", nil).json()
			if m["req"] != "i2" || m["task"] != "install" || m["id"] != "rt11" {
				t.Fatalf("progress %v", m)
			}
			if len(phases) == 0 || phases[len(phases)-1] != m["phase"] {
				phases = append(phases, m["phase"].(string))
			}
			if m["phase"] == "done" {
				break
			}
		}
		if fmt.Sprint(phases) != "[download verify extract done]" {
			t.Fatalf("phases %v", phases)
		}
	}
	// The install of the selected system powers on (the key is on).
	w.state(func(s map[string]any) bool {
		return s["run"] == "running" && s["needsInstall"] == nil && s["error"] == ""
	})
	resp, err := http.Get(e.url("api/systems"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sys systemsMsg
	if err := json.NewDecoder(resp.Body).Decode(&sys); err != nil {
		t.Fatal(err)
	}
	if sys.Selected != "rt11" || !sys.List[1].Installed {
		t.Fatalf("systems after install: %+v", sys)
	}
}

func TestStateCoalescing(t *testing.T) {
	e := newWebEnv(t, webCfg{})
	w := e.dial("sub=", nil)
	w.skipInit(0)
	w.drain(300 * time.Millisecond)
	start := time.Now()
	for i := 1; i <= 400; i++ {
		e.p.Input(api.PanelInput{Kind: "sr", Int: i})
		time.Sleep(time.Millisecond)
	}
	elapsed := time.Since(start)
	msgs := w.drain(300 * time.Millisecond)
	n := 0
	var last map[string]any
	for _, m := range msgs {
		if m.msgType() == "state" {
			n++
			last = m.json()
		}
	}
	max := int(elapsed/(50*time.Millisecond)) + 3
	if n > max || n < 2 {
		t.Fatalf("%d state messages in %v (max %d)", n, elapsed, max)
	}
	if get(last, "controls", "sr") != 400.0 {
		t.Fatalf("last state has sr %v (an update was lost)", get(last, "controls", "sr"))
	}
	// Stats alone: at most once per second.
	stop := time.Now().Add(1500 * time.Millisecond)
	for i := 0; time.Now().Before(stop); i++ {
		e.m.SetStats(api.MachineStats{MIPS: float64(i), UptimeSec: float64(i)})
		time.Sleep(5 * time.Millisecond)
	}
	n = 0
	for _, m := range w.drain(200 * time.Millisecond) {
		if m.msgType() == "state" {
			n++
		}
	}
	if n < 1 || n > 3 {
		t.Fatalf("%d state messages for 1.5 s of stats changes", n)
	}
}

func TestOversizedMessage(t *testing.T) {
	e := newWebEnv(t, webCfg{})
	w := e.dial("sub=", nil)
	w.skipInit(0)
	w.sendRaw(websocket.MessageBinary, append([]byte{binConsole}, bytes.Repeat([]byte("a"), maxClientMsg)...))
	for range w.in {
	}
	if err := <-w.err; websocket.CloseStatus(err) != websocket.StatusMessageTooBig {
		t.Fatalf("close: %v", err)
	}
	if len(e.m.Stream().Input()) != 0 {
		t.Fatal("oversized input reached the console")
	}
	// Exactly the limit is fine.
	w2 := e.dial("sub=", nil)
	w2.skipInit(0)
	w2.sendRaw(websocket.MessageBinary, append([]byte{binConsole}, bytes.Repeat([]byte("b"), maxClientMsg-1)...))
	if !paneltest.WaitFor(5*time.Second, func() bool { return len(e.m.Stream().Input()) == maxClientMsg-1 }) {
		t.Fatalf("input %d bytes", len(e.m.Stream().Input()))
	}
}

// TestSlowClient: a client that stops reading is disconnected once its
// queue exceeds the limit; the others get every byte in order. The fast
// client reads in lockstep with the output (it can never fall behind,
// however loaded the host is) and the kernel send buffer is bounded as in
// Run, so the slow client is cut after a predictable amount of output.
func TestSlowClient(t *testing.T) {
	e := newWebEnv(t, webCfg{sendBuf: 32 << 10, tune: func(tu *tuning) {
		tu.maxQueuedBytes = 256 << 10
		tu.writeTimeout = 2 * time.Second // the slow client's blocked write ends its connection soon
	}})
	fast := e.dial("sub=console", nil)
	fastID := uint64(fast.next().json()["clientId"].(float64)) // hello
	for _, want := range []string{"state", "systems", "#console"} {
		if m := fast.next(); m.msgType() != want {
			t.Fatalf("got %s, want %s", m.msgType(), want)
		}
	}
	// The slow client: a small receive buffer and nobody reading.
	dialer := &net.Dialer{}
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := dialer.DialContext(ctx, network, addr)
		if tc, ok := c.(*net.TCPConn); ok {
			_ = tc.SetReadBuffer(4096)
		}
		return c, err
	}}}
	slow, _, err := websocket.Dial(context.Background(), e.wsURL("sub=console"), &websocket.DialOptions{HTTPClient: hc})
	if err != nil {
		t.Fatal(err)
	}
	defer slow.CloseNow()
	slow.SetReadLimit(-1)
	if !paneltest.WaitFor(5*time.Second, func() bool { return e.s.hub.clientCount() == 2 }) {
		t.Fatal("slow client not connected")
	}
	slowCut := func() bool { // the server gave up on the slow client
		e.s.hub.mu.Lock()
		defer e.s.hub.mu.Unlock()
		for id, c := range e.s.hub.clients {
			if id == fastID {
				continue
			}
			c.mu.Lock()
			closed := c.closed
			c.mu.Unlock()
			return closed
		}
		return true // gone
	}
	chunk := bytes.Repeat([]byte("0123456789abcdef"), 1024) // 16 KiB
	var all bytes.Buffer
	var got []byte
	emit := func() {
		chunk[0] = byte('A' + all.Len()/len(chunk)%26) // distinguishable chunks
		e.m.Stream().Emit(chunk)
		all.Write(chunk)
		for len(got) < all.Len() { // lockstep
			got = append(got, fast.await("#console", nil).data[1:]...)
		}
	}
	// The slow client's share: its socket buffers (about 32 KiB + 8 KiB)
	// plus maxQueuedBytes, i.e. about 19 chunks.
	for i := 0; i < 256 && !slowCut(); i++ {
		emit()
	}
	if !slowCut() {
		t.Fatalf("slow client still served after %d bytes", all.Len())
	}
	if !paneltest.WaitFor(15*time.Second, func() bool { return e.s.hub.clientCount() == 1 }) {
		t.Fatalf("slow client not disconnected after %d bytes", all.Len())
	}
	for i := 0; i < 20; i++ { // the others are unaffected
		emit()
	}
	if !bytes.Equal(got, all.Bytes()) {
		t.Fatalf("fast client: %d bytes differ from the %d sent", len(got), all.Len())
	}
}

func TestDeadClientIsDropped(t *testing.T) {
	e := newWebEnv(t, webCfg{tune: func(tu *tuning) { tu.pingInterval = 50 * time.Millisecond }})
	// A client that never reads never answers pings.
	c, _, err := websocket.Dial(context.Background(), e.wsURL("sub="), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	if !paneltest.WaitFor(5*time.Second, func() bool { return e.s.hub.clientCount() == 1 }) {
		t.Fatal("not connected")
	}
	if !paneltest.WaitFor(5*time.Second, func() bool { return e.s.hub.clientCount() == 0 }) {
		t.Fatal("dead client not dropped")
	}
	// A reading client answers pings and stays.
	w := e.dial("sub=", nil)
	time.Sleep(400 * time.Millisecond)
	if n := e.s.hub.clientCount(); n != 1 {
		t.Fatalf("live client dropped (%d clients)", n)
	}
	_ = w
}

func TestDisconnectReleasesMomentarySwitches(t *testing.T) {
	e := newWebEnv(t, webCfg{})
	w := e.dial("sub=", nil)
	w.skipInit(0)
	w.send(map[string]any{"type": "momentary", "name": "LAMP_TEST", "active": true})
	if !paneltest.WaitFor(5*time.Second, func() bool { return e.p.Controls().LampTest }) {
		t.Fatal("lamp test not pressed")
	}
	_ = w.c.Close(websocket.StatusNormalClosure, "")
	if !paneltest.WaitFor(5*time.Second, func() bool { return !e.p.Controls().LampTest }) {
		t.Fatal("lamp test still held by a disconnected client")
	}
}

func TestRunShutsDownGracefully(t *testing.T) {
	e := newWebEnv(t, webCfg{})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.s.Serve(ctx, ln) }()
	c, _, err := websocket.Dial(context.Background(), "ws://"+ln.Addr().String()+"/ws?sub=", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	for i := 0; i < 3; i++ { // hello, state, systems
		if _, _, err := c.Read(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	cancel()
	var readErr error
	for readErr == nil {
		_, _, readErr = c.Read(context.Background())
	}
	if websocket.CloseStatus(readErr) != websocket.StatusGoingAway {
		t.Fatalf("close status: %v", readErr)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
	if _, err := net.Dial("tcp", ln.Addr().String()); err == nil {
		t.Fatal("still listening")
	}
	// Run on a bad address fails.
	s2, _ := newServer(Options{Listen: "256.0.0.1:1", Logger: quietLogger()}, Deps{Machine: e.m, Panel: e.p, Systems: e.sys}, nil)
	if err := s2.Run(context.Background()); err == nil {
		t.Fatal("Run on a bad address succeeded")
	}
}

func TestToastsFromThePanelGoToEveryone(t *testing.T) {
	e := newWebEnv(t, webCfg{})
	e.sys.SetScript("bsd211", []api.ScriptStep{{Expect: "never", TimeoutSec: 1}})
	a := e.dial("sub=", nil)
	a.skipInit(0)
	b := e.dial("sub=", nil)
	b.skipInit(0)
	a.send(map[string]any{"type": "shutdownOS", "req": "sd"})
	for _, w := range []*wsClient{a, b} {
		pr := w.await("progress", nil).json()
		if pr["task"] != "shutdown" || pr["id"] != "bsd211" || pr["req"] != "sd" {
			t.Fatalf("progress %v", pr)
		}
	}
	for _, w := range []*wsClient{a, b} {
		w.toast("Pull the plug")
	}
	// An asynchronous hint (queued input ignored when it ran) goes to its sender only.
	e.m.Lock()
	e.m.PowerOnDelay = 200 * time.Millisecond
	e.m.Unlock()
	a.send(map[string]any{"type": "key", "pos": "off"})
	a.state(func(s map[string]any) bool { return s["run"] == "off" })
	a.send(map[string]any{"type": "key", "pos": "power"})
	a.send(map[string]any{"type": "momentary", "name": "EXAM", "active": true, "req": "late"})
	if tm := a.toast("halted"); tm["req"] != "late" {
		t.Fatalf("async hint %v", tm)
	}
	for _, m := range b.drain(300 * time.Millisecond) {
		if m.msgType() == "toast" {
			t.Fatalf("hint reached another client: %s", m.data)
		}
	}
}

func TestWebSocketClosedByServerOnStop(t *testing.T) {
	e := newWebEnv(t, webCfg{})
	w := e.dial("sub=", nil)
	w.skipInit(0)
	e.s.Close()
	for range w.in {
	}
	if err := <-w.err; websocket.CloseStatus(err) != websocket.StatusGoingAway && !errors.Is(err, context.Canceled) {
		t.Fatalf("close: %v", err)
	}
	// New connections are refused.
	_, resp, err := websocket.Dial(context.Background(), e.wsURL(""), nil)
	if err == nil {
		t.Fatal("connected to a closed server")
	}
	_ = resp
}

func TestFactoryResetAndSpeedWhileOff(t *testing.T) {
	e := newWebEnv(t, webCfg{})
	w := e.dial("sub=", nil)
	w.skipInit(0)
	// The selected system cannot be reset while it runs ...
	w.send(map[string]any{"type": "factoryReset", "id": "bsd211", "req": "fr1"})
	if tm := w.toast("Power the machine off"); tm["req"] != "fr1" {
		t.Fatalf("toast %v", tm)
	}
	// ... another one can.
	w.send(map[string]any{"type": "factoryReset", "id": "rt11", "req": "fr2"})
	if pr := w.await("progress", nil).json(); pr["task"] != "factoryReset" || pr["id"] != "rt11" || pr["phase"] != "done" || pr["req"] != "fr2" {
		t.Fatalf("progress %v", pr)
	}
	// Under LOCK nothing may change either.
	w.send(map[string]any{"type": "key", "pos": "lock"})
	w.state(func(s map[string]any) bool { return get(s, "controls", "key") == "lock" })
	w.send(map[string]any{"type": "selectSystem", "id": "rt11"})
	w.toast("locked")
	// Off: the reset runs, and the panel is asked to power on again (the key is on).
	w.send(map[string]any{"type": "key", "pos": "off"})
	w.state(func(s map[string]any) bool { return s["run"] == "off" })
	e.sys.SetFactoryResetFunc(func(ctx context.Context, id string, req string) error {
		e.sys.Progress(api.Progress{Task: "factoryReset", ID: id, Phase: "extract", Req: req})
		e.sys.Progress(api.Progress{Task: "factoryReset", ID: id, Phase: "done", Req: req})
		return nil
	})
	w.send(map[string]any{"type": "factoryReset", "id": "bsd211"})
	w.await("progress", func(m rx) bool { return m.json()["phase"] == "done" && m.json()["id"] == "bsd211" })
	if n := e.m.Count("PowerOn"); n != 1 { // only the autostart: the key is at OFF
		t.Fatalf("PowerOn called %d times", n)
	}
	// A failing reset is reported to the sender.
	e.sys.SetFactoryResetFunc(func(ctx context.Context, id string, req string) error { return errors.New("disk full") })
	w.send(map[string]any{"type": "factoryReset", "id": "bsd211", "req": "fr3"})
	if tm := w.toast("disk full"); tm["req"] != "fr3" || tm["level"] != "error" {
		t.Fatalf("toast %v", tm)
	}
	// Speed while off: persisted for the selected system, not applied to a machine.
	w.send(map[string]any{"type": "speed", "value": "authentic"})
	w.state(func(s map[string]any) bool { return s["speed"] == "authentic" })
	time.Sleep(50 * time.Millisecond)
	if n := e.m.Count("SetSpeed"); n != 0 {
		t.Fatalf("SetSpeed called %d times while off", n)
	}
	// Subscribing to the log later sends its history first.
	e.lg.Add("old line")
	w.send(map[string]any{"type": "subscribe", "sub": []string{"log"}})
	if lg := w.await("log", nil).json(); fmt.Sprint(lg["lines"]) != "[old line]" {
		t.Fatalf("log history %v", lg)
	}
}

// TestCancelIsNotAFailure: a user's cancel of an install or a factory
// reset ends with the "cancelled" progress, and no error toast.
func TestCancelIsNotAFailure(t *testing.T) {
	e := newWebEnv(t, webCfg{key: "off"})
	w := e.dial("sub=", nil)
	w.skipInit(0)
	cancels := func() int { return strings.Count(fmt.Sprint(e.sys.Calls()), "Cancel rt11") }
	// As systems.Manager does: Cancel ends the task's own context, and the
	// task reports "cancelled" and returns that context's error.
	cancelled := func(task, id, req string, n int) error {
		if !paneltest.WaitFor(5*time.Second, func() bool { return cancels() == n }) {
			return errors.New("no cancel arrived")
		}
		e.sys.Progress(api.Progress{Task: task, ID: id, Phase: "cancelled", Text: "Cancelled", Req: req})
		return fmt.Errorf("download: %w", context.Canceled)
	}
	e.sys.SetInstallFunc(func(ctx context.Context, id string, accept bool, req string) error {
		return cancelled("install", id, req, 1)
	})
	e.sys.SetFactoryResetFunc(func(ctx context.Context, id string, req string) error {
		return cancelled("factoryReset", id, req, 2)
	})
	for _, task := range []string{"install", "factoryReset"} {
		w.send(map[string]any{"type": task, "id": "rt11", "acceptLicense": true, "req": task})
		w.send(map[string]any{"type": "cancel", "task": task, "id": "rt11"})
		w.await("progress", func(m rx) bool { return m.json()["task"] == task && m.json()["phase"] == "cancelled" })
		for _, m := range w.drain(300 * time.Millisecond) {
			if m.msgType() == "toast" {
				t.Fatalf("%s cancelled: toast %s", task, m.data)
			}
		}
	}
}

// TestSelectRefusedWhileSimulatorRuns: systems.Manager refuses a selection
// while a simulator runs (a power-on that started after the guard, or a
// simulator still stopping); that is a refusal (warn), not a failure.
func TestSelectRefusedWhileSimulatorRuns(t *testing.T) {
	e := newWebEnv(t, webCfg{key: "off"})
	w := e.dial("sub=", nil)
	w.skipInit(0)
	e.sys.SetSelectErr(fmt.Errorf("the simulator is running: %w", api.ErrBusy))
	w.send(map[string]any{"type": "selectSystem", "id": "rt11", "req": "sel"})
	if tm := w.toast("cannot be selected while the simulator runs"); tm["level"] != "warn" || tm["req"] != "sel" {
		t.Fatalf("toast %v", tm)
	}
	e.sys.SetSelectErr(nil)
	w.send(map[string]any{"type": "selectSystem", "id": "rt11"})
	w.await("systems", func(m rx) bool { return m.json()["selected"] == "rt11" })
}
