package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"pdp11emu/server/internal/api"
)

// channel is a subscribable stream of the protocol.
type channel int

const (
	chPanel channel = iota
	chConsole
	chLog
	numChannels
)

var channelNames = [numChannels]string{"panel", "console", "log"}

// parseChannels parses ?sub= (comma lists, possibly repeated). No
// parameter means "panel,console". Unknown names (e.g. the reserved
// tty0..tty7) are returned separately.
func parseChannels(params []string) (subs [numChannels]bool, unknown []string) {
	if params == nil {
		subs[chPanel], subs[chConsole] = true, true
		return subs, nil
	}
	for _, p := range params {
		for _, name := range strings.Split(p, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			found := false
			for ch, n := range channelNames {
				if n == name {
					subs[ch], found = true, true
				}
			}
			if !found {
				unknown = append(unknown, name)
			}
		}
	}
	return subs, unknown
}

type msgKind uint8

const (
	kindJSON    msgKind = iota // a complete text message
	kindBinary                 // a complete binary message (histories, frames)
	kindConsole                // live console output: consecutive ones are merged
	kindLog                    // one live log line: consecutive ones form one "log" message
)

type outMsg struct {
	kind msgKind
	data []byte
	line string // kindLog
}

func (m outMsg) size() int {
	if m.kind == kindLog {
		return len(m.line) + 4
	}
	return len(m.data)
}

// Limits of what the writer sends in one go.
const (
	maxMerged   = 64 << 10  // merged console output per message
	writeBudget = 256 << 10 // bytes taken from the queue before the frame slot is served
)

// subscription is one channel of one client. Output that arrives while
// the history is being queued waits in pending, so history and live data
// are neither reordered, lost nor duplicated.
type subscription struct {
	mu        sync.Mutex
	ready     bool
	cancelled bool
	pending   []outMsg
	cancel    func()
}

type client struct {
	h      *hub
	conn   *websocket.Conn
	id     uint64
	remote string
	ctx    context.Context
	cancel context.CancelFunc
	wake   chan struct{}

	mu           sync.Mutex
	queue        []outMsg
	deferred     []outMsg // live messages that arrived while the connection was initialized
	queued       int      // bytes in queue + deferred
	initializing bool
	frame        []byte // newest panel frame not yet sent
	closed       bool
	sub          [numChannels]*subscription
	consoleSeq   uint64
	toasted      map[string]time.Time // rate limit for repeated console input errors
}

// serve runs one WebSocket connection until it ends.
func (h *hub) serve(conn *websocket.Conn, r *http.Request, subs [numChannels]bool, unknown []string) {
	ctx, cancel := context.WithCancel(h.s.ctx)
	c := &client{
		h: h, conn: conn, remote: h.s.remote(r), ctx: ctx, cancel: cancel,
		wake: make(chan struct{}, 1), initializing: true, toasted: map[string]time.Time{},
	}
	if !h.register(c) {
		cancel()
		_ = conn.Close(websocket.StatusGoingAway, "server shutting down")
		return
	}
	h.log.Debug("client connected", "client", c.id, "remote", c.remote)
	conn.SetReadLimit(maxClientMsg)

	// Connect order: hello, state, systems, the histories, then live data.
	s := h.s
	c.push(outMsg{kind: kindJSON, data: mustJSON(helloMsg{
		Type: "hello", Protocol: protocolVersion, Server: s.opts.Version, ClientID: c.id,
		Lamps: api.LampCount, FrameHz: s.opts.FrameHz, Auth: s.opts.Password != "", Insecure: s.insecure,
	})}, false)
	c.push(outMsg{kind: kindJSON, data: mustJSON(s.buildState())}, false)
	c.push(outMsg{kind: kindJSON, data: mustJSON(s.buildSystems())}, false)
	for ch := channel(0); ch < numChannels; ch++ {
		if subs[ch] {
			c.subscribe(ch)
		}
	}
	if len(unknown) > 0 {
		c.push(outMsg{kind: kindJSON, data: toastMessage("warn", "Unsupported channels ignored: "+strings.Join(unknown, ", "), "")}, true)
	}
	c.finishInit()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); c.writeLoop() }()
	go func() { defer wg.Done(); c.pingLoop() }()
	c.readLoop()

	cancel()
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	for ch := channel(0); ch < numChannels; ch++ {
		c.unsubscribe(ch)
	}
	h.unregister(c)
	s.deps.Panel.ReleaseClient(c.id)
	_ = conn.CloseNow()
	wg.Wait()
	h.log.Debug("client disconnected", "client", c.id)
}

// push queues a message. live messages that arrive while the connection
// is initialized are held back until the initial messages are queued. A
// client whose queue exceeds the limit is disconnected (never silently
// skipped: it gets the whole history again when it reconnects).
func (c *client) push(m outMsg, live bool) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	if live && c.initializing {
		c.deferred = append(c.deferred, m)
	} else {
		c.queue = append(c.queue, m)
	}
	c.queued += m.size()
	over := c.queued > c.h.s.tune.maxQueuedBytes
	if over {
		c.closed = true
		c.queue, c.deferred = nil, nil
	}
	c.mu.Unlock()
	if over {
		c.h.log.Warn("disconnecting a client that fell too far behind", "client", c.id, "remote", c.remote)
		go c.closeWith(websocket.StatusPolicyViolation, "client too slow")
		return
	}
	signal(c.wake)
}

func (c *client) finishInit() {
	c.mu.Lock()
	c.queue = append(c.queue, c.deferred...)
	c.deferred = nil
	c.initializing = false
	c.mu.Unlock()
	signal(c.wake)
}

// setFrame offers the newest lamp frame (latest wins).
func (c *client) setFrame(msg []byte) {
	c.mu.Lock()
	ok := !c.closed && c.sub[chPanel] != nil
	if ok {
		c.frame = msg
	}
	c.mu.Unlock()
	if ok {
		signal(c.wake)
	}
}

// closeWith closes the connection with a status (the reader then ends
// the connection).
func (c *client) closeWith(code websocket.StatusCode, reason string) {
	c.mu.Lock()
	c.closed = true
	c.queue, c.deferred = nil, nil
	c.mu.Unlock()
	if err := c.conn.Close(code, reason); err != nil {
		c.h.log.Debug("close", "client", c.id, "err", err)
	}
	c.cancel()
}

type wsMsg struct {
	typ  websocket.MessageType
	data []byte
}

// take removes what the writer should send next: queued messages (up to
// writeBudget, consecutive console output and log lines merged) and the
// newest frame. A closed client has nothing to send (its close handshake
// is in progress; the writer ends with the connection's context).
func (c *client) take() (msgs []wsMsg, frame []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		c.frame = nil
		return nil, nil
	}
	budget := writeBudget
	for len(c.queue) > 0 && budget > 0 {
		m := c.queue[0]
		n := 1
		switch m.kind {
		case kindConsole:
			buf := m.data
			for n < len(c.queue) && c.queue[n].kind == kindConsole && len(buf)+len(c.queue[n].data)-1 <= maxMerged {
				if n == 1 {
					buf = append(make([]byte, 0, maxMerged), m.data...)
				}
				buf = append(buf, c.queue[n].data[1:]...)
				n++
			}
			msgs = append(msgs, wsMsg{websocket.MessageBinary, buf})
		case kindLog:
			lines := []string{m.line}
			for n < len(c.queue) && c.queue[n].kind == kindLog && len(lines) < 1000 {
				lines = append(lines, c.queue[n].line)
				n++
			}
			msgs = append(msgs, wsMsg{websocket.MessageText, mustJSON(logMsg{Type: "log", Lines: lines})})
		case kindBinary:
			msgs = append(msgs, wsMsg{websocket.MessageBinary, m.data})
		default:
			msgs = append(msgs, wsMsg{websocket.MessageText, m.data})
		}
		for i := 0; i < n; i++ {
			sz := c.queue[i].size()
			c.queued -= sz
			budget -= sz
			c.queue[i] = outMsg{}
		}
		c.queue = c.queue[n:]
	}
	if len(c.queue) == 0 {
		c.queue = nil // let the backing array go
	}
	frame, c.frame = c.frame, nil
	return msgs, frame
}

func (c *client) writeLoop() {
	defer c.cancel() // a failed write ends the connection
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-c.wake:
		}
		for {
			msgs, frame := c.take()
			if len(msgs) == 0 && frame == nil {
				break
			}
			for _, m := range msgs {
				if !c.write(m.typ, m.data) {
					return
				}
			}
			if frame != nil && !c.write(websocket.MessageBinary, frame) {
				return
			}
		}
	}
}

func (c *client) write(typ websocket.MessageType, data []byte) bool {
	ctx, cancel := context.WithTimeout(c.ctx, c.h.s.tune.writeTimeout)
	defer cancel()
	if err := c.conn.Write(ctx, typ, data); err != nil {
		if c.ctx.Err() == nil {
			c.h.log.Debug("write failed; closing", "client", c.id, "err", err)
		}
		return false
	}
	return true
}

// pingLoop pings every pingInterval; a client that misses two pongs in a
// row is dropped.
func (c *client) pingLoop() {
	t := time.NewTicker(c.h.s.tune.pingInterval)
	defer t.Stop()
	missed := 0
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-t.C:
		}
		ctx, cancel := context.WithTimeout(c.ctx, c.h.s.tune.pingInterval)
		err := c.conn.Ping(ctx)
		cancel()
		switch {
		case err == nil:
			missed = 0
		case c.ctx.Err() != nil:
			return
		default:
			missed++
			if missed >= 2 {
				c.h.log.Info("dropping a client that does not answer pings", "client", c.id, "remote", c.remote)
				_ = c.conn.CloseNow()
				c.cancel()
				return
			}
		}
	}
}

func (c *client) readLoop() {
	for {
		typ, data, err := c.conn.Read(c.ctx)
		if err != nil {
			if websocket.CloseStatus(err) == websocket.StatusMessageTooBig {
				c.h.log.Info("client message too large; closed", "client", c.id, "limit", maxClientMsg)
			}
			return
		}
		switch typ {
		case websocket.MessageText:
			c.handleText(data)
		case websocket.MessageBinary:
			c.handleBinary(data)
		}
	}
}

// consoleOrder returns the console subscription order of a console
// subscriber. Called with hub.mu held.
func (c *client) consoleOrder() (uint64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.consoleSeq, c.sub[chConsole] != nil && !c.closed
}

// subscribe starts a channel: its history is queued first, then live data.
func (c *client) subscribe(ch channel) {
	h := c.h
	sub := &subscription{}
	switch ch {
	case chPanel:
		h.mu.Lock() // consistent with onFrame: no frame between history and live
		c.mu.Lock()
		c.sub[chPanel] = sub
		c.mu.Unlock()
		frame := h.lastFrame
		h.mu.Unlock()
		if frame != nil {
			c.push(outMsg{kind: kindBinary, data: frame}, false)
		}
		sub.ready = true
		return
	case chConsole:
		seq := h.nextConsoleSeq()
		c.mu.Lock()
		c.sub[chConsole], c.consoleSeq = sub, seq
		c.mu.Unlock()
		defer h.markState() // the answerer may change
		stream := h.s.deps.Machine.Console()
		var hist []byte
		if stream != nil {
			hist, sub.cancel = stream.SubscribeHistory(func(p []byte) {
				c.gate(sub, outMsg{kind: kindConsole, data: append([]byte{binConsole}, p...)})
			})
		}
		// The client resets its terminal and writes this one message as
		// the history (an empty one too, so the reset always happens).
		c.open(sub, outMsg{kind: kindBinary, data: append([]byte{binConsole}, hist...)})
	case chLog:
		c.mu.Lock()
		c.sub[chLog] = sub
		c.mu.Unlock()
		lb := h.s.deps.Log
		var hist []string
		if lb != nil {
			hist, sub.cancel = lb.SubscribeHistory(h.s.tune.logHistoryLines, func(lines []string) {
				for _, line := range lines {
					c.gate(sub, outMsg{kind: kindLog, line: line})
				}
			})
		}
		if hist == nil {
			hist = []string{}
		}
		c.open(sub, outMsg{kind: kindJSON, data: mustJSON(logMsg{Type: "log", Lines: hist})})
	}
}

// gate delivers live data of a subscription.
func (c *client) gate(sub *subscription, m outMsg) {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if sub.cancelled {
		return
	}
	if !sub.ready {
		sub.pending = append(sub.pending, m)
		return
	}
	c.push(m, true)
}

// open queues the history and what arrived meanwhile, then goes live.
func (c *client) open(sub *subscription, history outMsg) {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if sub.cancelled {
		return
	}
	c.push(history, false)
	for _, m := range sub.pending {
		c.push(m, true)
	}
	sub.pending = nil
	sub.ready = true
}

func (c *client) unsubscribe(ch channel) {
	c.mu.Lock()
	sub := c.sub[ch]
	c.sub[ch] = nil
	if ch == chPanel {
		c.frame = nil
	}
	c.mu.Unlock()
	if sub == nil {
		return
	}
	sub.mu.Lock()
	sub.cancelled = true
	cancel := sub.cancel
	sub.pending = nil
	sub.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if ch == chConsole {
		c.h.markState()
	}
}

func (c *client) subscribed(ch channel) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sub[ch] != nil
}

// toast answers the client's own input.
func (c *client) toast(level, text, req string) {
	c.push(outMsg{kind: kindJSON, data: toastMessage(level, text, req)}, true)
}

// toastLimited toasts at most once per 2 s per key (console input errors
// would otherwise come with every keystroke).
func (c *client) toastLimited(key, level, text string) {
	now := time.Now()
	c.mu.Lock()
	last, seen := c.toasted[key]
	if seen && now.Sub(last) < 2*time.Second {
		c.mu.Unlock()
		return
	}
	c.toasted[key] = now
	c.mu.Unlock()
	c.toast(level, text, "")
}

func (c *client) handleBinary(data []byte) {
	if len(data) == 0 {
		return
	}
	switch data[0] {
	case binConsole:
		p := data[1:]
		if len(p) == 0 {
			return
		}
		stream := c.h.s.deps.Machine.Console()
		if stream == nil {
			c.toastLimited("console", "warn", "The console is not available.")
			return
		}
		if _, err := stream.Write(p); err != nil {
			switch {
			case errors.Is(err, api.ErrNotPowered):
				c.toastLimited("off", "warn", "The machine is off: console input is not sent.")
			case errors.Is(err, api.ErrInputFull):
				c.toastLimited("full", "warn", "Console input queue is full: wait for the paste to finish (or cancel it).")
			default:
				c.toastLimited("err", "error", "Console input failed: "+err.Error())
			}
			return
		}
		c.h.inputFrom(c.id)
		c.h.markState() // inputQueued
	default:
		c.toastLimited("bin", "error", "Unsupported binary message type.")
	}
}
