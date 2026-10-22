package web

import (
	"bytes"
	"context"
	"log/slog"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"pdp11emu/server/internal/api"
)

// hub owns the WebSocket clients and fans out everything they receive.
//
// Lock order: progressRelay.mu → hub.mu → client.mu; a channel
// subscription's mu is taken before client.mu (the stream callbacks hold
// it while queueing). Nothing calls into the Machine, Panel or Systems
// while holding hub.mu or client.mu, except the cheap getters used to
// build messages.
type hub struct {
	s   *Server
	log *slog.Logger

	mu         sync.Mutex
	clients    map[uint64]*client
	nextID     uint64
	consoleSeq uint64 // order of console subscriptions (answerer fallback)
	closing    bool
	lastFrame  []byte // the newest panel frame message
	lastInput  uint64 // the client that most recently sent console input

	stateDirty   chan struct{}
	systemsDirty chan struct{}
	progress     progressRelay

	cancels []func()
	pumps   sync.WaitGroup
	conns   sync.WaitGroup
}

func newHub(s *Server) *hub {
	h := &hub{
		s:            s,
		log:          s.log,
		clients:      map[uint64]*client{},
		stateDirty:   make(chan struct{}, 1),
		systemsDirty: make(chan struct{}, 1),
	}
	h.progress = progressRelay{h: h, last: map[string]*progressState{}}
	return h
}

// start subscribes to the services and starts the pumps.
func (h *hub) start() {
	d := h.s.deps
	h.cancels = append(h.cancels,
		d.Panel.SubscribeFrames(h.onFrame),
		d.Panel.SubscribeChanges(h.markState),
		d.Panel.Toasts(func(level, text, req string) { h.broadcast(toastMessage(level, text, req)) }),
		d.Panel.SubscribeHints(func(client uint64, req, text string) { h.toastTo(client, "warn", text, req) }),
		d.Panel.SubscribeProgress(h.progress.publish),
		d.Machine.Subscribe(h.onMachineEvent),
		d.Systems.SubscribeChanges(func() { signal(h.systemsDirty); h.markState() }),
		d.Systems.SubscribeProgress(h.progress.publish),
	)
	h.pumps.Add(2)
	go func() { defer h.pumps.Done(); h.statePump(h.s.ctx) }()
	go func() { defer h.pumps.Done(); h.systemsPump(h.s.ctx) }()
}

// shutdown says "going away" to every client and refuses new ones.
func (h *hub) shutdown() {
	for _, cancel := range h.cancels {
		cancel()
	}
	h.mu.Lock()
	h.closing = true
	clients := make([]*client, 0, len(h.clients))
	for _, c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.Unlock()
	var wg sync.WaitGroup
	for _, c := range clients {
		wg.Add(1)
		go func() { defer wg.Done(); c.closeWith(websocket.StatusGoingAway, "server shutting down") }()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}

func (h *hub) wait() {
	h.pumps.Wait()
	h.conns.Wait()
}

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (h *hub) markState() { signal(h.stateDirty) }

func (h *hub) register(c *client) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closing {
		return false
	}
	h.nextID++
	c.id = h.nextID
	h.clients[c.id] = c
	h.conns.Add(1)
	h.markState()
	return true
}

func (h *hub) unregister(c *client) {
	h.mu.Lock()
	if _, ok := h.clients[c.id]; ok {
		delete(h.clients, c.id)
		h.conns.Done()
	}
	if h.lastInput == c.id {
		h.lastInput = 0
	}
	h.mu.Unlock()
	h.markState()
}

func (h *hub) nextConsoleSeq() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.consoleSeq++
	return h.consoleSeq
}

// inputFrom records the client that typed last (it answers terminal queries).
func (h *hub) inputFrom(id uint64) {
	h.mu.Lock()
	changed := h.lastInput != id
	h.lastInput = id
	h.mu.Unlock()
	if changed {
		h.markState()
	}
}

// viewers returns the number of clients and the answerer: the client that
// most recently sent console input, else the oldest console subscriber
// (PROTOCOL.md §1.4); 0 if no client shows the console.
func (h *hub) viewers() (n int, answerer uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c := h.clients[h.lastInput]; c != nil {
		if _, ok := c.consoleOrder(); ok {
			return len(h.clients), c.id
		}
	}
	best := uint64(math.MaxUint64)
	for _, c := range h.clients {
		if seq, ok := c.consoleOrder(); ok && seq < best {
			best, answerer = seq, c.id
		}
	}
	return len(h.clients), answerer
}

// broadcast queues a JSON message for every client.
func (h *hub) broadcast(data []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.clients {
		c.push(outMsg{kind: kindJSON, data: data}, true)
	}
}

// toastTo sends a toast to one client (answers to its own input).
func (h *hub) toastTo(id uint64, level, text, req string) {
	h.mu.Lock()
	c := h.clients[id]
	h.mu.Unlock()
	if c != nil {
		c.push(outMsg{kind: kindJSON, data: toastMessage(level, text, req)}, true)
	}
}

func toastMessage(level, text, req string) []byte {
	return mustJSON(toastMsg{Type: "toast", Level: level, Text: text, Req: req})
}

// onFrame runs on the panel's frame loop: latest frame wins per client.
func (h *hub) onFrame(frame *[api.LampCount]byte, seq uint16) {
	msg := frameMessage(frame, seq)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastFrame = msg
	for _, c := range h.clients {
		c.setFrame(msg)
	}
}

func (h *hub) onMachineEvent(ev api.MachineEvent) {
	if ev.Kind == api.EvError && ev.Text != "" {
		h.broadcast(toastMessage("error", ev.Text, ""))
	}
	h.markState()
}

func (h *hub) isClosing() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.closing
}

func (h *hub) clientCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

// statePump broadcasts the state when any field changed: at most every
// stateMinInterval, and when only the stats changed at most every
// statsMinInterval. Fields without change notifications (inputQueued) are
// polled.
func (h *hub) statePump(ctx context.Context) {
	tune := h.s.tune
	poll := time.NewTicker(tune.statePoll)
	defer poll.Stop()
	var (
		lastKey   []byte
		lastStats api.MachineStats
		lastSent  time.Time
	)
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.stateDirty:
		case <-poll.C:
		}
		if wait := tune.stateMinInterval - time.Since(lastSent); wait > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
		if h.clientCount() == 0 {
			lastKey = nil // a client that connects gets the state anyway
			continue
		}
		st := h.s.buildState()
		stats := st.Stats
		st.Stats = api.MachineStats{}
		key := mustJSON(st)
		statsDue := stats != lastStats && time.Since(lastSent) >= tune.statsMinInterval
		if bytes.Equal(key, lastKey) && !statsDue {
			continue
		}
		st.Stats = stats
		h.broadcast(mustJSON(st))
		lastKey, lastStats, lastSent = key, stats, time.Now()
	}
}

// systemsPump broadcasts the systems list when it changed (coalesced).
func (h *hub) systemsPump(ctx context.Context) {
	var last []byte
	var lastSent time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.systemsDirty:
		}
		if wait := h.s.tune.systemsMinInterval - time.Since(lastSent); wait > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
		if h.clientCount() == 0 {
			last = nil
			continue
		}
		msg := mustJSON(h.s.buildSystems())
		if bytes.Equal(msg, last) {
			continue
		}
		h.broadcast(msg)
		last, lastSent = msg, time.Now()
	}
}

// progressRelay forwards progress to every client. Phase changes and
// final phases (done, error, cancelled) go out at once and in order;
// updates within a phase are coalesced to one per progressInterval per
// task (the newest wins).
type progressRelay struct {
	h    *hub
	mu   sync.Mutex
	last map[string]*progressState
}

type progressState struct {
	phase   string
	sentAt  time.Time
	pending *api.Progress
	timer   *time.Timer
}

func finalPhase(p string) bool { return p == "done" || p == "error" || p == "cancelled" }

func (r *progressRelay) publish(p api.Progress) {
	key := p.Task + "\x00" + p.ID
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.last[key]
	if st == nil || st.phase != p.Phase || finalPhase(p.Phase) || now.Sub(st.sentAt) >= r.h.s.tune.progressInterval {
		if st != nil && st.timer != nil {
			st.timer.Stop()
		}
		if finalPhase(p.Phase) {
			delete(r.last, key)
		} else {
			r.last[key] = &progressState{phase: p.Phase, sentAt: now}
		}
		r.h.broadcast(mustJSON(progressMsg{Type: "progress", Progress: p}))
		return
	}
	st.pending = &p
	if st.timer == nil {
		st.timer = time.AfterFunc(r.h.s.tune.progressInterval-now.Sub(st.sentAt), func() { r.flush(key, st) })
	}
}

func (r *progressRelay) flush(key string, st *progressState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.last[key] != st || st.pending == nil {
		return // superseded by a phase change or a final phase
	}
	p := *st.pending
	st.pending, st.timer, st.sentAt = nil, nil, time.Now()
	r.h.broadcast(mustJSON(progressMsg{Type: "progress", Progress: p}))
}

// handleWS upgrades GET /ws?sub=... after the Origin check.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if !originAllowed(r, s.opts.AllowOrigins) {
		s.log.Warn("WebSocket from a foreign origin refused", "origin", r.Header.Get("Origin"), "host", r.Host, "remote", s.remote(r))
		http.Error(w, "origin not allowed (see PDP11_ALLOW_ORIGINS)", http.StatusForbidden)
		return
	}
	if s.hub.isClosing() {
		http.Error(w, "server shutting down", http.StatusServiceUnavailable)
		return
	}
	subs, unknown := parseChannels(r.URL.Query()["sub"])
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // checked above, with our own rules
		CompressionMode:    websocket.CompressionDisabled,
	})
	if err != nil {
		s.log.Debug("WebSocket upgrade failed", "err", err, "remote", s.remote(r))
		return
	}
	s.hub.serve(conn, r, subs, unknown)
}
