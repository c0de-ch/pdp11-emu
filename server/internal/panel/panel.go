// Package panel implements the PDP-11/70 console logic and the lamp
// engine (docs/PANEL_LOGIC.md). It owns the power policy (key switch,
// autostart, boot and clean-shutdown requests).
//
// Concurrency model: Input, ReleaseClient, Controls and Console never block
// on the Machine. Every Machine operation runs on one worker goroutine, in
// the order the inputs arrived (console switches queue behind the
// operation in flight). Two things bypass the queue because they must take
// effect at once: turning the key to OFF (cancels the operation in flight
// and powers off) and the ENABLE/HALT level while a long operation (power
// on, boot, OS shutdown) is in flight. Machine events update the console
// state on the Machine's event goroutine; the frame loop reads the panel
// export and computes the lamps.
package panel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"pdp11emu/server/internal/api"
)

// Options configure a Panel.
type Options struct {
	StatePath string       // JSON file for persisted controls ($DATA/panel.json); "" = not persisted
	FrameHz   int          // lamp frames per second (default 60)
	Autostart bool         // at Run(): power on if the persisted key is power/lock
	Logger    *slog.Logger // nil: slog.Default()
}

// PowerStatus is the outcome of the panel's last power-on attempt. It is
// reset when the key is turned to OFF and when a power-on succeeds.
type PowerStatus struct {
	// Failed is set when the last power-on attempt failed while the key is
	// on (Machine.LastError says why).
	Failed bool
	// NeedsInstall is the id of the system whose power-on was refused with
	// api.ErrNoSystem (an install whose licence the user must accept), or "".
	NeedsInstall string
}

// Key switch positions and latch values (as in api.Controls).
const (
	keyOff   = "off"
	keyPower = "power"
	keyLock  = "lock"
)

// Momentary switch names (PROTOCOL.md "momentary").
const (
	swLoadAdrs = "LOAD_ADRS"
	swExam     = "EXAM"
	swDep      = "DEP"
	swCont     = "CONT"
	swStart    = "START"
	swLampTest = "LAMP_TEST"
)

// consoleSwitches are the momentary switches reported in Controls.Pressed.
var consoleSwitches = [...]string{swLoadAdrs, swExam, swDep, swCont, swStart}

// Timeouts of Machine operations run by the worker.
const (
	consoleOpTimeout = 15 * time.Second // examine, deposit, step, continue, start, reset
	haltTimeout      = 5 * time.Second
	bootTimeout      = 2 * time.Minute
	powerOffTimeout  = 30 * time.Second
	stopWait         = time.Second // how long to wait for the EvStop of a step/halt
	workerExitWait   = 10 * time.Second
	maxQueuedOps     = 1024
	sampleBuffer     = 8192 // the export ring holds 16384 samples; a reader gets at most half
)

// Panel is the shared front panel of the machine.
type Panel struct {
	m    api.Machine
	src  api.PanelSource
	sys  api.Systems
	opts Options
	log  *slog.Logger

	running atomic.Bool  // Run was called
	srValue atomic.Int32 // switch register value for the SR writer

	// execSem serializes Machine operations of the worker and ShutdownOS.
	execSem chan struct{}
	wake    chan struct{} // worker: queue changed
	changed chan struct{} // notifier: Controls/Console/PowerStatus changed
	saveReq chan struct{} // saver: persisted controls changed
	srReq   chan struct{} // SR writer: switch register changed

	mu        sync.Mutex
	ctl       controls
	holders   map[string]map[uint64]struct{} // momentary switch -> clients holding it
	cons      console
	consGen   uint64 // incremented when a stop or a power-on rewrites the console state
	power     PowerStatus
	stopSeq   uint64       // number of EvStop events seen
	observed  api.RunState // state of the last EvState seen
	lastStop  api.StopInfo // the most recent EvStop
	evSignal  chan struct{}
	queue     []*op
	inflight  *op
	powerOff  bool   // a pre-empting power-off is running
	sideHalt  bool   // a Halt bypassing the queue is running
	busyExtra string // ShutdownOS outside the worker
	saveGen   uint64 // incremented on every change of a persisted control
	runCtx    context.Context
	bootWatch *bootWatch // boot-timeout warning (bootwatch.go)

	saveMu   sync.Mutex // serializes writes of panel.json
	savedGen uint64     // saveGen of the last successful write (guarded by saveMu)

	frames   subscribers[func(frame *[api.LampCount]byte, seq uint16)]
	changes  subscribers[func()]
	toasts   subscribers[func(level, text, req string)]
	hints    subscribers[func(client uint64, req, text string)]
	progress subscribers[func(api.Progress)]

	// Owned by the frame loop.
	eng     lampEngine
	samples []api.Sample
}

// New creates the panel. It loads persisted controls from opts.StatePath
// and applies the persisted switch register with m.SetSwitchRegister.
func New(m api.Machine, src api.PanelSource, sys api.Systems, opts Options) (*Panel, error) {
	if m == nil || sys == nil {
		return nil, errors.New("panel: a Machine and Systems are required")
	}
	if opts.FrameHz <= 0 {
		opts.FrameHz = 60
	}
	if opts.FrameHz > 1000 {
		opts.FrameHz = 1000
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	log := opts.Logger.With("component", "panel")
	ctl, err := loadControls(opts.StatePath, log)
	if err != nil {
		return nil, err
	}
	p := &Panel{
		m:        m,
		src:      src,
		sys:      sys,
		opts:     opts,
		log:      log,
		execSem:  make(chan struct{}, 1),
		wake:     make(chan struct{}, 1),
		changed:  make(chan struct{}, 1),
		saveReq:  make(chan struct{}, 1),
		srReq:    make(chan struct{}, 1),
		ctl:      ctl,
		holders:  make(map[string]map[uint64]struct{}),
		evSignal: make(chan struct{}),
		runCtx:   context.Background(),
		samples:  make([]api.Sample, sampleBuffer),
	}
	for _, n := range consoleSwitches {
		p.holders[n] = make(map[uint64]struct{})
	}
	p.holders[swLampTest] = make(map[uint64]struct{})
	p.observed = m.State()
	p.srValue.Store(int32(ctl.sr))
	m.SetSwitchRegister(uint16(ctl.sr & addrLow))
	// The subscription lives as long as the panel: ShutdownOS, used while
	// the daemon stops, needs stop events after Run has returned.
	m.Subscribe(p.onMachineEvent)
	return p, nil
}

// Run runs the frame loop and event handling until ctx is done.
func (p *Panel) Run(ctx context.Context) error {
	if !p.running.CompareAndSwap(false, true) {
		return errors.New("panel: Run called twice")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	p.mu.Lock()
	p.runCtx = ctx
	p.mu.Unlock()

	var wg sync.WaitGroup
	worker := make(chan struct{})
	go func() { defer close(worker); p.workerLoop(ctx) }()
	for _, loop := range []func(context.Context){p.notifyLoop, p.saveLoop, p.srLoop} {
		wg.Add(1)
		go func() { defer wg.Done(); loop(ctx) }()
	}

	if p.opts.Autostart {
		p.mu.Lock()
		if p.ctl.key != keyOff {
			p.enqueueLocked(&op{kind: opPowerOn})
		}
		p.mu.Unlock()
	}

	p.frameLoop(ctx)

	cancel()
	p.mu.Lock()
	p.runCtx = context.Background() // a late key OFF still pulls the plug
	p.dropBootWatchLocked()
	p.mu.Unlock()
	wg.Wait()
	select {
	case <-worker:
	case <-time.After(workerExitWait):
		p.log.Warn("panel worker did not stop in time")
	}
	p.flushSave()
	return nil
}

// Input applies a control or machine-control request from a browser
// (validated for shape by the caller, for semantics here). It never blocks:
// Machine operations run on the panel's worker goroutine.
// The returned string is a user-facing reason when the input was ignored
// ("" when accepted); the caller toasts it to the sender only. Inputs that
// are queued behind another operation and turn out to be ignored when they
// run are reported through SubscribeHints.
func (p *Panel) Input(in api.PanelInput) string {
	switch in.Kind {
	case "sr":
		return p.inputSR(in)
	case "momentary":
		return p.inputMomentary(in)
	case "latch":
		return p.inputLatch(in)
	case "knob":
		return p.inputKnob(in)
	case "key":
		return p.inputKey(in)
	case "boot":
		return p.inputBoot(in)
	case "shutdownOS":
		return p.inputShutdown(in)
	case "retryPower":
		return p.inputRetryPower(in)
	}
	return fmt.Sprintf("Unknown panel input %q.", in.Kind)
}

// ReleaseClient releases momentary switches held by a disconnected client.
func (p *Panel) ReleaseClient(client uint64) {
	p.mu.Lock()
	changed := false
	for _, set := range p.holders {
		if _, ok := set[client]; ok {
			delete(set, client)
			changed = changed || len(set) == 0
		}
	}
	p.mu.Unlock()
	if changed {
		p.notify()
	}
}

// Controls returns the current control positions.
func (p *Panel) Controls() api.Controls {
	p.mu.Lock()
	defer p.mu.Unlock()
	c := api.Controls{
		SR:         p.ctl.sr,
		EnableHalt: latchName(p.ctl.halt, "halt", "enable"),
		SinstSbus:  latchName(p.ctl.sbus, "bus", "inst"),
		AddrKnob:   p.ctl.addrKnob,
		DataKnob:   p.ctl.dataKnob,
		Key:        p.ctl.key,
		LampTest:   len(p.holders[swLampTest]) > 0,
		Pressed:    make(map[string]bool, len(consoleSwitches)),
	}
	for _, n := range consoleSwitches {
		c.Pressed[n] = len(p.holders[n]) > 0
	}
	return c
}

// Console returns the console state for the UI.
func (p *Panel) Console() api.ConsoleInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	return api.ConsoleInfo{
		Addr:     p.cons.addr,
		AdrsErr:  p.cons.adrsErr,
		LastStop: p.cons.lastStop,
		PC:       p.cons.pc,
		Busy:     p.busyLocked(),
	}
}

// PowerStatus reports the outcome of the last power-on attempt (for the
// state message's "error" and "needsInstall"). Changes are announced
// through SubscribeChanges.
func (p *Panel) PowerStatus() PowerStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.power
}

// SubscribeFrames registers fn for lamp frames (called from the frame loop;
// frame is only valid during the call; fn must not block). Frames are
// delivered when any lamp changed and at least once per second; seq counts
// delivered frames.
func (p *Panel) SubscribeFrames(fn func(frame *[api.LampCount]byte, seq uint16)) (cancel func()) {
	return p.frames.add(fn)
}

// SubscribeChanges registers fn, called (coalesced, without arguments)
// whenever Controls(), Console() or PowerStatus() changed.
func (p *Panel) SubscribeChanges(fn func()) (cancel func()) { return p.changes.add(fn) }

// Toasts registers fn for messages the panel wants every client to see
// (e.g. "Set ENABLE/HALT to ENABLE to boot" goes to the sender via Input's
// return value instead; this is for asynchronous results).
func (p *Panel) Toasts(fn func(level, text, req string)) (cancel func()) { return p.toasts.add(fn) }

// SubscribeHints registers fn for user-facing reasons why a queued input
// was ignored when it ran (it waited behind another operation, so Input
// could not answer synchronously). Deliver them to that client only.
// fn must not block.
func (p *Panel) SubscribeHints(fn func(client uint64, req, text string)) (cancel func()) {
	return p.hints.add(fn)
}

// SubscribeProgress registers fn for progress of panel tasks (task
// "shutdown"); deliver it to every client. fn must not block.
func (p *Panel) SubscribeProgress(fn func(api.Progress)) (cancel func()) {
	return p.progress.add(fn)
}

// notify schedules a (coalesced) change notification.
func (p *Panel) notify() { signal(p.changed) }

// signal does a non-blocking send on a 1-buffered channel.
func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (p *Panel) notifyLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.changed:
			p.changes.each(func(fn func()) { fn() })
		}
	}
}

// srLoop applies switch register changes. It decouples Input from
// Machine.SetSwitchRegister (latest value wins).
func (p *Panel) srLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.srReq:
			p.m.SetSwitchRegister(uint16(p.srValue.Load()) & addrLow)
		}
	}
}

// baseContextLocked is the context for operations started outside the
// worker (pre-empting power-off, bypassing halt).
func (p *Panel) baseContextLocked() context.Context { return p.runCtx }

func (p *Panel) toastAll(level, text, req string) {
	p.toasts.each(func(fn func(level, text, req string)) { fn(level, text, req) })
}

func (p *Panel) hint(o *op, text string) {
	if text == "" {
		return
	}
	if o.client == 0 {
		p.log.Debug("panel operation ignored", "op", o.name(), "reason", text)
		return
	}
	p.hints.each(func(fn func(client uint64, req, text string)) { fn(o.client, o.req, text) })
}

func (p *Panel) publishProgress(pr api.Progress) {
	p.progress.each(func(fn func(api.Progress)) { fn(pr) })
}

func latchName(down bool, downName, upName string) string {
	if down {
		return downName
	}
	return upName
}
