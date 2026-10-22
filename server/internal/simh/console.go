package simh

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"sync"
	"time"

	"pdp11emu/server/internal/api"
)

const (
	historySize    = 256 << 10 // console output kept across power cycles
	inputQueueMax  = 64 << 10  // queued console input (ErrInputFull beyond)
	inputBurst     = 32        // bytes written at once before pacing starts
	defaultCPS     = 100
	writeTimeout   = 10 * time.Second
	reconnectDelay = time.Second
)

// console is the DL11 console line: one TCP connection to SimH per power
// cycle, an output history that survives power cycles, and a bounded,
// paced input queue. It implements api.Stream.
type console struct {
	log *slog.Logger
	cps float64

	// Output side (the reader goroutine).
	hmu    sync.Mutex
	hist   []byte // ring buffer of historySize bytes
	hpos   int    // next write position in hist
	hfull  bool   // hist has wrapped
	subs   []streamSub
	nextID uint64
	ready  readyMatcher

	// Connection and input side.
	mu     sync.Mutex
	link   *link    // the current power cycle's connection, nil when off
	conn   net.Conn // nil while not connected
	queue  []byte
	hold   bool // the CPU is not running: keep input queued
	tokens float64
	refill time.Time
	kick   chan struct{}
}

type streamSub struct {
	id uint64
	fn func([]byte)
}

// link owns the goroutines of one power cycle's console connection.
type link struct {
	ctx    context.Context
	cancel context.CancelFunc
	dial   func(context.Context) (net.Conn, error)
	wg     sync.WaitGroup
}

func newConsole(log *slog.Logger, cps int) *console {
	if cps <= 0 {
		cps = defaultCPS
	}
	return &console{
		log:    log,
		cps:    float64(cps),
		hist:   make([]byte, historySize),
		hold:   true,
		tokens: inputBurst,
		kick:   make(chan struct{}, 1),
	}
}

// attach starts using conn (already connected) for a new power cycle. If
// the connection drops while attached, dial is used to reconnect (with
// NOTELNET SimH does not replay output on a new connection).
func (c *console) attach(conn net.Conn, dial func(context.Context) (net.Conn, error)) {
	c.detach()
	ctx, cancel := context.WithCancel(context.Background())
	l := &link{ctx: ctx, cancel: cancel, dial: dial}
	c.mu.Lock()
	c.link, c.conn = l, conn
	c.queue, c.tokens, c.refill = nil, inputBurst, time.Now()
	c.mu.Unlock()
	l.wg.Add(2)
	go c.readLoop(l, conn)
	go c.writeLoop(l)
}

// detach closes the connection of the current power cycle and drops queued
// input.
func (c *console) detach() {
	c.mu.Lock()
	l, conn := c.link, c.conn
	c.link, c.conn, c.queue = nil, nil, nil
	c.mu.Unlock()
	if l == nil {
		return
	}
	l.cancel()
	if conn != nil {
		conn.Close()
	}
	l.wg.Wait()
}

// setHold makes the writer keep input queued while the CPU is not running
// (SimH only reads the console while executing; pacing would be pointless).
func (c *console) setHold(hold bool) {
	c.mu.Lock()
	c.hold = hold
	c.mu.Unlock()
	c.wake()
}

func (c *console) wake() {
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

func (c *console) readLoop(l *link, conn net.Conn) {
	defer l.wg.Done()
	buf := make([]byte, 16<<10)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			c.output(buf[:n])
		}
		if err == nil {
			continue
		}
		conn.Close()
		c.mu.Lock()
		if c.link == l {
			c.conn = nil
		}
		c.mu.Unlock()
		if l.ctx.Err() != nil {
			return
		}
		c.log.Warn("console connection lost, reconnecting", "err", err)
		for conn = nil; conn == nil; {
			select {
			case <-l.ctx.Done():
				return
			case <-time.After(reconnectDelay):
			}
			nc, err := l.dial(l.ctx)
			if err != nil {
				continue
			}
			c.mu.Lock()
			if c.link != l {
				c.mu.Unlock()
				nc.Close()
				return
			}
			c.conn, conn = nc, nc
			c.mu.Unlock()
			c.wake()
			c.log.Info("console reconnected")
		}
	}
}

// writeLoop sends queued input: the first inputBurst bytes of a burst at
// once, then paced at cps (a token bucket of inputBurst tokens).
func (c *console) writeLoop(l *link) {
	defer l.wg.Done()
	for {
		var chunk []byte
		var conn net.Conn
		wait := time.Duration(-1)
		c.mu.Lock()
		now := time.Now()
		c.tokens += now.Sub(c.refill).Seconds() * c.cps
		if c.tokens > inputBurst {
			c.tokens = inputBurst
		}
		c.refill = now
		if len(c.queue) > 0 && !c.hold && c.conn != nil {
			if n := int(c.tokens); n >= 1 {
				n = min(n, len(c.queue))
				chunk = append([]byte(nil), c.queue[:n]...)
				c.queue = c.queue[n:]
				if len(c.queue) == 0 {
					c.queue = nil
				}
				c.tokens -= float64(n)
				conn = c.conn
			} else {
				wait = time.Duration((1 - c.tokens) / c.cps * float64(time.Second))
			}
		}
		c.mu.Unlock()

		if chunk != nil {
			_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if _, err := conn.Write(chunk); err != nil && l.ctx.Err() == nil {
				c.log.Warn("console input lost", "bytes", len(chunk), "err", err)
			}
			continue
		}
		var timer <-chan time.Time
		if wait >= 0 {
			timer = time.After(wait)
		}
		select {
		case <-l.ctx.Done():
			return
		case <-c.kick:
		case <-timer:
		}
	}
}

// output records guest output and passes it to the subscribers.
func (c *console) output(p []byte) {
	c.hmu.Lock()
	defer c.hmu.Unlock()
	for rest := p; len(rest) > 0; {
		n := copy(c.hist[c.hpos:], rest)
		rest = rest[n:]
		c.hpos += n
		if c.hpos == len(c.hist) {
			c.hpos, c.hfull = 0, true
		}
	}
	c.ready.feed(p)
	for _, s := range c.subs {
		s.fn(p)
	}
}

// history returns a copy of the recorded output. Caller holds hmu.
func (c *console) history() []byte {
	if !c.hfull {
		return append([]byte(nil), c.hist[:c.hpos]...)
	}
	out := make([]byte, 0, len(c.hist))
	out = append(out, c.hist[c.hpos:]...)
	return append(out, c.hist[:c.hpos]...)
}

// armReady starts looking for pattern in the output that follows; fire is
// called (with the output lock held, so it must not block) on the first
// match.
func (c *console) armReady(pattern string, fire func()) {
	c.hmu.Lock()
	defer c.hmu.Unlock()
	c.ready.arm(pattern, fire)
}

func (c *console) disarmReady() {
	c.hmu.Lock()
	defer c.hmu.Unlock()
	c.ready.arm("", nil)
}

// Write implements api.Stream.
func (c *console) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return 0, api.ErrNotPowered
	}
	if len(c.queue)+len(p) > inputQueueMax {
		return 0, api.ErrInputFull
	}
	c.queue = append(c.queue, p...)
	c.wake()
	return len(p), nil
}

// SubscribeHistory implements api.Stream.
func (c *console) SubscribeHistory(fn func(p []byte)) ([]byte, func()) {
	c.hmu.Lock()
	defer c.hmu.Unlock()
	id := c.nextID
	c.nextID++
	c.subs = append(c.subs, streamSub{id, fn})
	return c.history(), func() {
		c.hmu.Lock()
		defer c.hmu.Unlock()
		for i, s := range c.subs {
			if s.id == id {
				c.subs = append(c.subs[:i:i], c.subs[i+1:]...)
				return
			}
		}
	}
}

// Connected implements api.Stream.
func (c *console) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn != nil
}

// Queued implements api.Stream.
func (c *console) Queued() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.queue)
}

// CancelInput implements api.Stream.
func (c *console) CancelInput() {
	c.mu.Lock()
	c.queue = nil
	c.mu.Unlock()
}

// readyMatcher finds a plain-text pattern in the console output, across
// read boundaries. NUL and DEL (fill characters of old boot programs and
// RT-11) are ignored.
type readyMatcher struct {
	pattern []byte
	tail    []byte
	fire    func()
}

func (r *readyMatcher) arm(pattern string, fire func()) {
	r.pattern, r.tail, r.fire = nil, nil, nil
	if pattern != "" && fire != nil {
		r.pattern, r.fire = []byte(pattern), fire
	}
}

func (r *readyMatcher) feed(p []byte) {
	if r.pattern == nil {
		return
	}
	buf := r.tail
	for _, b := range p {
		if b != 0 && b != 0x7f {
			buf = append(buf, b)
		}
	}
	if bytes.Contains(buf, r.pattern) {
		fire := r.fire
		r.arm("", nil)
		fire()
		return
	}
	if keep := len(r.pattern) - 1; len(buf) > keep {
		buf = append(buf[:0], buf[len(buf)-keep:]...)
	}
	r.tail = buf
}

var _ api.Stream = (*console)(nil)
