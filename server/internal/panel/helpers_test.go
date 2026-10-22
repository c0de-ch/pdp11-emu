package panel

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"pdp11emu/server/internal/api"
	"pdp11emu/server/internal/panel/paneltest"
)

// env is a panel over fakes, running in the background.
type env struct {
	t   *testing.T
	m   *paneltest.Machine
	src *paneltest.Source
	sys *paneltest.Systems
	p   *Panel

	cancel context.CancelFunc
	done   chan struct{}

	mu       sync.Mutex
	hints    []string // "client:text"
	toasts   []string // "level:text"
	progress []api.Progress
	frames   int
	lastSeq  uint16
	changes  int
}

type envOpt func(*envConfig)

type envConfig struct {
	opts  Options
	ctl   *controls // initial controls (nil: defaults)
	start bool
}

func withControls(c controls) envOpt { return func(cfg *envConfig) { cfg.ctl = &c } }
func withOptions(o Options) envOpt   { return func(cfg *envConfig) { cfg.opts = o } }
func notStarted() envOpt             { return func(cfg *envConfig) { cfg.start = false } }

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// newEnv creates a panel over fresh fakes and runs it. The default
// controls are the first-start defaults with Autostart on.
func newEnv(t *testing.T, opt ...envOpt) *env {
	t.Helper()
	cfg := envConfig{opts: Options{FrameHz: 200, Autostart: true}, start: true}
	for _, o := range opt {
		o(&cfg)
	}
	if cfg.opts.Logger == nil {
		cfg.opts.Logger = quietLogger()
	}
	e := &env{t: t, m: paneltest.NewMachine(), src: paneltest.NewSource(), sys: paneltest.DefaultSystems()}
	t.Cleanup(e.m.Close)
	p, err := New(e.m, e.src, e.sys, cfg.opts)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ctl != nil {
		p.ctl = *cfg.ctl
		p.srValue.Store(int32(cfg.ctl.sr))
	}
	e.p = p
	p.SubscribeHints(func(client uint64, req, text string) {
		e.mu.Lock()
		e.hints = append(e.hints, fmt.Sprintf("%d:%s", client, text))
		e.mu.Unlock()
	})
	p.Toasts(func(level, text, req string) {
		e.mu.Lock()
		e.toasts = append(e.toasts, level+":"+text)
		e.mu.Unlock()
	})
	p.SubscribeProgress(func(pr api.Progress) {
		e.mu.Lock()
		e.progress = append(e.progress, pr)
		e.mu.Unlock()
	})
	p.SubscribeFrames(func(f *[api.LampCount]byte, seq uint16) {
		e.mu.Lock()
		e.frames++
		e.lastSeq = seq
		e.mu.Unlock()
	})
	p.SubscribeChanges(func() {
		e.mu.Lock()
		e.changes++
		e.mu.Unlock()
	})
	if cfg.start {
		e.start()
	}
	return e
}

func (e *env) start() {
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	e.done = make(chan struct{})
	go func() {
		defer close(e.done)
		if err := e.p.Run(ctx); err != nil {
			e.t.Errorf("Run: %v", err)
		}
	}()
	e.t.Cleanup(e.stop)
	e.idle()
}

// stop ends Run and waits for it.
func (e *env) stop() {
	if e.cancel == nil {
		return
	}
	e.cancel()
	select {
	case <-e.done:
	case <-time.After(15 * time.Second):
		e.t.Error("Run did not return")
	}
	e.cancel = nil
}

// idle waits until the machine delivered its events and the panel has
// nothing queued or running.
func (e *env) idle() {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	stable := 0
	for stable < 3 {
		if time.Now().After(deadline) {
			e.p.mu.Lock()
			busy := e.p.busyLocked()
			n := len(e.p.queue)
			e.p.mu.Unlock()
			e.t.Fatalf("panel not idle (busy %q, %d queued)", busy, n)
		}
		e.m.Sync()
		e.p.mu.Lock()
		idle := e.p.idleLocked()
		e.p.mu.Unlock()
		if idle {
			stable++
		} else {
			stable = 0
		}
		time.Sleep(time.Millisecond)
	}
}

const client = 1

// timeout bounds waits for asynchronous effects.
func (e *env) timeout() time.Duration { return 5 * time.Second }

func (e *env) input(in api.PanelInput) string {
	if in.Client == 0 {
		in.Client = client
	}
	return e.p.Input(in)
}

// do runs one action of the test language and waits until it settled:
//
//	sr <octal>, load, exam, dep, cont, start, (switch presses)
//	halt, enable, sbus, sinst, (latches)
//	addr <0-7>, data <0-7>, key <off|power|lock>, boot, shutdown, retry
func (e *env) do(action string) string {
	e.t.Helper()
	f := strings.Fields(action)
	var h string
	switch f[0] {
	case "sr":
		v, err := strconv.ParseUint(f[1], 8, 32)
		if err != nil {
			e.t.Fatal(err)
		}
		h = e.input(api.PanelInput{Kind: "sr", Int: int(v)})
	case "load", "exam", "dep", "cont", "start":
		name := map[string]string{"load": swLoadAdrs, "exam": swExam, "dep": swDep, "cont": swCont, "start": swStart}[f[0]]
		h = e.input(api.PanelInput{Kind: "momentary", Name: name, Active: true})
		e.input(api.PanelInput{Kind: "momentary", Name: name, Active: false})
	case "halt", "enable":
		h = e.input(api.PanelInput{Kind: "latch", Name: "ENABLE_HALT", Value: f[0]})
	case "sbus", "sinst":
		h = e.input(api.PanelInput{Kind: "latch", Name: "SINST_SBUS", Value: strings.TrimPrefix(f[0], "s")})
	case "addr", "data":
		n, _ := strconv.Atoi(f[1])
		h = e.input(api.PanelInput{Kind: "knob", Name: f[0], Int: n})
	case "key":
		h = e.input(api.PanelInput{Kind: "key", Value: f[1]})
	case "boot":
		h = e.input(api.PanelInput{Kind: "boot"})
	case "shutdown":
		h = e.input(api.PanelInput{Kind: "shutdownOS"})
	case "retry":
		h = e.input(api.PanelInput{Kind: "retryPower"})
	default:
		e.t.Fatalf("unknown action %q", action)
	}
	e.idle()
	return h
}

// cons returns a copy of the console state.
func (e *env) cons() console {
	e.p.mu.Lock()
	defer e.p.mu.Unlock()
	return e.p.cons
}

func (e *env) key() string { return e.p.Controls().Key }

func (e *env) hintList() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.hints...)
}

func (e *env) toastList() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.toasts...)
}

func (e *env) progressList() []api.Progress {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]api.Progress(nil), e.progress...)
}

// fmtLoc formats a location: "1000" (physical, octal), "KD:1000"
// (virtual kernel D space), "R00" (register).
func fmtLoc(l api.Location) string {
	if l.Register != "" {
		return l.Register
	}
	if !l.Virtual {
		return strconv.FormatUint(uint64(l.Addr), 8)
	}
	mode := map[api.CPUMode]string{api.ModeKernel: "K", api.ModeSuper: "S", api.ModeUser: "U"}[l.Mode]
	space := "I"
	if l.DSpace {
		space = "D"
	}
	return mode + space + ":" + strconv.FormatUint(uint64(l.Addr), 8)
}

// ops formats the console-relevant Machine calls since the last ResetCalls.
func (e *env) ops() []string {
	var out []string
	for _, c := range e.m.Calls() {
		switch c.Name {
		case "Examine", "Translate":
			out = append(out, c.Name+" "+fmtLoc(c.Loc))
		case "Deposit":
			out = append(out, fmt.Sprintf("Deposit %s=%o", fmtLoc(c.Loc), c.Value))
		case "Start":
			out = append(out, fmt.Sprintf("Start %o", c.Value))
		case "PowerOn":
			out = append(out, fmt.Sprintf("PowerOn boot=%v", c.Boot))
		case "SetSpeed":
		default:
			out = append(out, c.Name)
		}
	}
	return out
}

// haltedEnv returns a powered-on, halted machine (key POWER, HALT down).
func haltedEnv(t *testing.T, opt ...envOpt) *env {
	t.Helper()
	c := defaultControls()
	c.halt = true
	e := newEnv(t, append([]envOpt{withControls(c)}, opt...)...)
	if st := e.m.State(); st != api.RunHalted {
		t.Fatalf("state %s, want halted", st)
	}
	e.m.ResetCalls()
	return e
}

// runningEnv returns a booted, running machine (key POWER, ENABLE).
func runningEnv(t *testing.T, opt ...envOpt) *env {
	t.Helper()
	e := newEnv(t, opt...)
	if st := e.m.State(); st != api.RunRunning {
		t.Fatalf("state %s, want running", st)
	}
	e.m.ResetCalls()
	return e
}
