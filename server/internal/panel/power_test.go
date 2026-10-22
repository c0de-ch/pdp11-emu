package panel

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"pdp11emu/server/internal/api"
	"pdp11emu/server/internal/panel/paneltest"
)

func offControls(halt bool) controls {
	c := defaultControls()
	c.key, c.halt = keyOff, halt
	return c
}

func TestKeyPolicy(t *testing.T) {
	tests := []struct {
		name    string
		ctl     controls
		setup   func(m *paneltest.Machine)
		actions []string
		ops     []string
		state   api.RunState
		key     string
		power   PowerStatus
	}{
		{name: "OFF powers off", ctl: defaultControls(), actions: []string{"key off"},
			ops: []string{"PowerOff"}, state: api.RunOff, key: keyOff},
		{name: "OFF → POWER boots with ENABLE", ctl: offControls(false), actions: []string{"key power"},
			ops: []string{"PowerOn boot=true"}, state: api.RunRunning, key: keyPower},
		{name: "OFF → POWER with HALT powers on halted", ctl: offControls(true), actions: []string{"key power"},
			ops: []string{"PowerOn boot=false"}, state: api.RunHalted, key: keyPower},
		{name: "OFF → LOCK boots: LOCK forces ENABLE", ctl: offControls(true), actions: []string{"key lock"},
			ops: []string{"PowerOn boot=true"}, state: api.RunRunning, key: keyLock},
		{name: "POWER ↔ LOCK changes no power", ctl: defaultControls(), actions: []string{"key lock", "key power", "key lock"},
			ops: nil, state: api.RunRunning, key: keyLock},
		{name: "not installed: the key stays, the UI is asked for an install", ctl: offControls(false),
			setup:   func(m *paneltest.Machine) { m.NoSystem = true },
			actions: []string{"key power"}, ops: []string{"PowerOn boot=true"}, state: api.RunOff, key: keyPower,
			power: PowerStatus{Failed: true, NeedsInstall: "bsd211"}},
		{name: "turning the key off clears a power-on failure", ctl: offControls(false),
			setup:   func(m *paneltest.Machine) { m.NoSystem = true },
			actions: []string{"key power", "key off"}, ops: []string{"PowerOn boot=true", "PowerOff"}, state: api.RunOff, key: keyOff},
		{name: "power-on error", ctl: offControls(false),
			setup:   func(m *paneltest.Machine) { m.PowerOnErr = errors.New("boot.ini line 3: no such device") },
			actions: []string{"key power"}, ops: []string{"PowerOn boot=true"}, state: api.RunError, key: keyPower,
			power: PowerStatus{Failed: true}},
		{name: "retryPower powers on when the key is on and the machine off", ctl: offControls(false),
			setup:   func(m *paneltest.Machine) { m.NoSystem = true },
			actions: []string{"key power", "fix", "retry"}, ops: []string{"PowerOn boot=true", "PowerOn boot=true"},
			state: api.RunRunning, key: keyPower},
		{name: "retryPower does nothing with the key at OFF", ctl: offControls(false), actions: []string{"retry"},
			ops: nil, state: api.RunOff, key: keyOff},
		{name: "retryPower does nothing while running", ctl: defaultControls(), actions: []string{"retry"},
			ops: nil, state: api.RunRunning, key: keyPower},
		{name: "retryPower after a crash", ctl: defaultControls(), actions: []string{"crash", "retry"},
			ops: []string{"PowerOn boot=true"}, state: api.RunRunning, key: keyPower},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, withControls(tt.ctl), notStarted())
			if tt.setup != nil {
				tt.setup(e.m)
			}
			e.start()
			e.m.ResetCalls()
			for _, a := range tt.actions {
				switch a {
				case "fix":
					e.m.Lock()
					e.m.NoSystem = false
					e.m.Unlock()
				case "crash":
					e.m.SetState(api.RunError)
					e.idle()
				default:
					e.do(a)
				}
			}
			if got := e.ops(); !slices.Equal(got, tt.ops) {
				t.Errorf("ops = %q, want %q", got, tt.ops)
			}
			if st := e.m.State(); st != tt.state {
				t.Errorf("state = %s, want %s", st, tt.state)
			}
			if k := e.key(); k != tt.key {
				t.Errorf("key = %s, want %s", k, tt.key)
			}
			if ps := e.p.PowerStatus(); ps != tt.power {
				t.Errorf("PowerStatus = %+v, want %+v", ps, tt.power)
			}
		})
	}
}

func TestAutostart(t *testing.T) {
	for _, tt := range []struct {
		name      string
		autostart bool
		key       string
		halt      bool
		want      []string
	}{
		{"persisted POWER boots", true, keyPower, false, []string{"PowerOn boot=true"}},
		{"persisted LOCK boots", true, keyLock, true, []string{"PowerOn boot=true"}},
		{"persisted POWER with HALT powers on halted", true, keyPower, true, []string{"PowerOn boot=false"}},
		{"persisted OFF stays off", true, keyOff, false, nil},
		{"autostart disabled", false, keyPower, false, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := defaultControls()
			c.key, c.halt = tt.key, tt.halt
			e := newEnv(t, withControls(c), withOptions(Options{FrameHz: 100, Autostart: tt.autostart}))
			if got := e.ops(); !slices.Equal(got, tt.want) {
				t.Fatalf("ops = %q, want %q", got, tt.want)
			}
			if k := e.key(); k != tt.key {
				t.Fatalf("key changed to %s", k)
			}
		})
	}
}

func TestKeyOffPreemptsSlowPowerOn(t *testing.T) {
	e := newEnv(t, withControls(offControls(false)))
	e.m.Lock()
	e.m.PowerOnDelay = 10 * time.Second // e.g. installing the default system
	e.m.Unlock()
	e.input(api.PanelInput{Kind: "key", Value: keyPower})
	if !paneltest.WaitFor(5*time.Second, func() bool { return e.m.State() == api.RunStarting }) {
		t.Fatal("not starting")
	}
	if b := e.p.Console().Busy; b != "power on" {
		t.Fatalf("busy = %q", b)
	}
	// Queued console work is dropped when the power goes.
	e.input(api.PanelInput{Kind: "momentary", Name: swExam, Active: true})
	start := time.Now()
	e.input(api.PanelInput{Kind: "key", Value: keyOff})
	e.idle()
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("power off took %v behind the power-on", d)
	}
	if st := e.m.State(); st != api.RunOff {
		t.Fatalf("state %s", st)
	}
	if ps := e.p.PowerStatus(); ps.Failed {
		t.Fatalf("a cancelled power-on is reported as failed: %+v", ps)
	}
	if n := e.m.Count("Examine"); n != 0 {
		t.Fatal("queued EXAM ran after the power was cut")
	}
	// And it can be powered on again.
	e.m.Lock()
	e.m.PowerOnDelay = 0
	e.m.Unlock()
	e.do("key power")
	if st := e.m.State(); st != api.RunRunning {
		t.Fatalf("state after power on: %s", st)
	}
}

func TestKeyTogglingEndsInTheLastPosition(t *testing.T) {
	e := newEnv(t, withControls(offControls(false)))
	e.m.Lock()
	e.m.PowerOnDelay = 20 * time.Millisecond
	e.m.Unlock()
	for i := 0; i < 10; i++ {
		e.input(api.PanelInput{Kind: "key", Value: keyPower})
		e.input(api.PanelInput{Kind: "key", Value: keyOff})
	}
	e.input(api.PanelInput{Kind: "key", Value: keyPower})
	e.idle()
	if st := e.m.State(); st != api.RunRunning {
		t.Fatalf("state %s, want running", st)
	}
	for i := 0; i < 5; i++ {
		e.input(api.PanelInput{Kind: "key", Value: keyOff})
		e.input(api.PanelInput{Kind: "key", Value: keyLock})
	}
	e.input(api.PanelInput{Kind: "key", Value: keyOff})
	e.idle()
	if st := e.m.State(); st != api.RunOff {
		t.Fatalf("state %s, want off", st)
	}
}

func TestHaltLevel(t *testing.T) {
	t.Run("HALT while running halts", func(t *testing.T) {
		e := runningEnv(t)
		e.m.SetPC(0o1234)
		e.do("halt")
		if st := e.m.State(); st != api.RunHalted {
			t.Fatalf("state %s", st)
		}
		if c := e.cons(); c.addr != 0o1234 || c.lastStop != "Front panel HALT" {
			t.Fatalf("console %+v", c)
		}
		e.do("enable") // returning to ENABLE does nothing
		if st := e.m.State(); st != api.RunHalted || slices.Contains(e.ops(), "Continue") {
			t.Fatalf("state %s, ops %q", st, e.ops())
		}
	})
	t.Run("LOCK forces ENABLE; POWER makes HALT effective again", func(t *testing.T) {
		e := runningEnv(t)
		e.do("key lock")
		if h := e.do("halt"); !strings.Contains(h, "LOCK") {
			t.Fatalf("hint %q", h)
		}
		if st := e.m.State(); st != api.RunRunning {
			t.Fatalf("halted under LOCK")
		}
		if c := e.p.Controls(); c.EnableHalt != "halt" {
			t.Fatal("paddle did not move under LOCK")
		}
		e.do("key power")
		if st := e.m.State(); st != api.RunHalted {
			t.Fatalf("state %s after LOCK → POWER with HALT down", st)
		}
	})
	t.Run("a CPU started by someone else is halted again", func(t *testing.T) {
		e := haltedEnv(t)
		e.m.SetState(api.RunRunning)
		e.idle()
		if st := e.m.State(); st != api.RunHalted {
			t.Fatalf("state %s", st)
		}
	})
	t.Run("HALT during a booting power-on halts once it runs", func(t *testing.T) {
		e := newEnv(t, withControls(offControls(false)))
		e.m.Lock()
		e.m.PowerOnDelay = 150 * time.Millisecond
		e.m.Unlock()
		e.input(api.PanelInput{Kind: "key", Value: keyPower})
		paneltest.WaitFor(5*time.Second, func() bool { return e.m.State() == api.RunStarting })
		e.input(api.PanelInput{Kind: "latch", Name: "ENABLE_HALT", Value: "halt"})
		e.idle()
		if st := e.m.State(); st != api.RunHalted {
			t.Fatalf("state %s", st)
		}
	})
}

func TestBootInput(t *testing.T) {
	running := func(t *testing.T) *env { return runningEnv(t) }
	tests := []struct {
		name  string
		env   func(t *testing.T) *env
		pre   []string
		hint  string
		ops   []string
		state api.RunState
	}{
		{name: "refused under LOCK", env: running, pre: []string{"key lock"}, hint: "unlock the console", state: api.RunRunning},
		{name: "refused with HALT", env: func(t *testing.T) *env { return haltedEnv(t) }, hint: "ENABLE to boot", state: api.RunHalted},
		{name: "running: reboot", env: running, ops: []string{"Boot"}, state: api.RunRunning},
		{name: "halted with ENABLE", env: func(t *testing.T) *env { return haltedEnv(t) }, pre: []string{"enable"},
			ops: []string{"Boot"}, state: api.RunRunning},
		{name: "off: the key turns to POWER and it boots", env: func(t *testing.T) *env {
			e := newEnv(t, withControls(offControls(false)))
			e.m.ResetCalls()
			return e
		}, ops: []string{"PowerOn boot=true"}, state: api.RunRunning},
		{name: "after a crash", env: running, pre: []string{"crash"}, ops: []string{"PowerOn boot=true"}, state: api.RunRunning},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := tt.env(t)
			for _, a := range tt.pre {
				if a == "crash" {
					e.m.SetState(api.RunError)
					e.idle()
					continue
				}
				e.do(a)
			}
			e.m.ResetCalls()
			h := e.do("boot")
			if tt.hint == "" && h != "" || tt.hint != "" && !strings.Contains(h, tt.hint) {
				t.Fatalf("hint %q, want %q", h, tt.hint)
			}
			if got := e.ops(); !slices.Equal(got, tt.ops) {
				t.Fatalf("ops = %q, want %q", got, tt.ops)
			}
			if st := e.m.State(); st != tt.state {
				t.Fatalf("state %s, want %s", st, tt.state)
			}
			if tt.hint == "" && e.key() != keyPower {
				t.Fatalf("key = %s", e.key())
			}
		})
	}
	t.Run("refused while already starting", func(t *testing.T) {
		e := newEnv(t, withControls(offControls(false)))
		e.m.Lock()
		e.m.PowerOnDelay = 200 * time.Millisecond
		e.m.Unlock()
		e.input(api.PanelInput{Kind: "key", Value: keyPower})
		paneltest.WaitFor(5*time.Second, func() bool { return e.m.State() == api.RunStarting })
		if h := e.input(api.PanelInput{Kind: "boot"}); !strings.Contains(h, "already starting") {
			t.Fatalf("hint %q", h)
		}
		e.idle()
		if n := e.m.Count("PowerOn") + e.m.Count("Boot"); n != 1 {
			t.Fatalf("%d power-ons/boots", n)
		}
	})
}

// shell makes the fake console behave like a Unix shell for the
// shutdown script: echo + prompt, and a HALT after "shutdown -h now".
func shell(e *env, haltAfter time.Duration) {
	s := e.m.Stream()
	s.OnWrite = func(p []byte) {
		go func() {
			in := string(p)
			s.EmitString(strings.ReplaceAll(in, "\r", "\r\n"))
			switch {
			case strings.HasPrefix(in, "shutdown -h now"):
				s.EmitString("syncing disks... done\r\nhalting\r\n")
				if haltAfter >= 0 {
					time.Sleep(haltAfter)
					e.m.StopCPU("HALT instruction", 0o14)
				}
			default:
				s.EmitString("# ")
			}
		}()
	}
}

func shortShutdown(t *testing.T) {
	old := [3]time.Duration{defaultStepTimeout, haltAfterScript, settleAfterScript}
	defaultStepTimeout, haltAfterScript, settleAfterScript = time.Second, 300*time.Millisecond, 100*time.Millisecond
	t.Cleanup(func() { defaultStepTimeout, haltAfterScript, settleAfterScript = old[0], old[1], old[2] })
}

func phases(pr []api.Progress) []string {
	var out []string
	for _, p := range pr {
		if p.Task != "shutdown" || p.ID != "bsd211" {
			out = append(out, "bad:"+p.Task+"/"+p.ID)
		}
		if len(out) == 0 || out[len(out)-1] != p.Phase {
			out = append(out, p.Phase)
		}
	}
	return out
}

func TestShutdownOS(t *testing.T) {
	t.Run("types the script, waits for the HALT, powers off", func(t *testing.T) {
		shortShutdown(t)
		e := runningEnv(t)
		shell(e, 50*time.Millisecond)
		if h := e.input(api.PanelInput{Kind: "shutdownOS", Req: "r1"}); h != "" {
			t.Fatalf("hint %q", h)
		}
		e.idle()
		if got := string(e.m.Stream().Input()); got != "sync\rshutdown -h now\r" {
			t.Fatalf("typed %q", got)
		}
		if st := e.m.State(); st != api.RunOff || e.key() != keyOff {
			t.Fatalf("state %s key %s", st, e.key())
		}
		if got := phases(e.progressList()); !slices.Equal(got, []string{"step", "done"}) {
			t.Fatalf("phases %q", got)
		}
		if pr := e.progressList(); pr[0].Req != "r1" {
			t.Fatalf("req not echoed: %+v", pr[0])
		}
		if ts := e.toastList(); len(ts) != 0 {
			t.Fatalf("toasts %q", ts)
		}
	})
	t.Run("progress texts are readable", func(t *testing.T) {
		shortShutdown(t)
		e := runningEnv(t)
		e.sys.SetScript("bsd211", []api.ScriptStep{{Send: "sync\r"}, {Expect: "# ", Send: "\r"}, {Expect: "# ", Send: "shutdown -h now\r"}})
		shell(e, 50*time.Millisecond)
		e.do("shutdown")
		var texts []string
		for _, p := range e.progressList() {
			texts = append(texts, p.Text)
		}
		want := []string{"Typing “sync”", "Waiting for “#”", "Pressing Return", "Waiting for “#”", "Typing “shutdown -h now”",
			"Waiting for the system to halt…", "Powering off", "The OS has shut down; power is off."}
		if !slices.Equal(texts, want) {
			t.Fatalf("progress texts %q, want %q", texts, want)
		}
		if got, want := scriptText(" \x03halt\x7f\r\n "), "^Chalt^?"; got != want {
			t.Fatalf("scriptText = %q, want %q", got, want)
		}
	})
	t.Run("the first expect matches a prompt already on screen", func(t *testing.T) {
		shortShutdown(t)
		e := runningEnv(t)
		e.sys.SetScript("bsd211", []api.ScriptStep{{Expect: "# ", Send: "shutdown -h now\r"}})
		e.m.Stream().EmitString("login: root\r\n# ")
		shell(e, 0)
		e.do("shutdown")
		if got := string(e.m.Stream().Input()); got != "shutdown -h now\r" {
			t.Fatalf("typed %q", got)
		}
		if st := e.m.State(); st != api.RunOff {
			t.Fatalf("state %s", st)
		}
	})
	t.Run("old output does not satisfy a later expect", func(t *testing.T) {
		shortShutdown(t)
		e := runningEnv(t)
		e.sys.SetScript("bsd211", []api.ScriptStep{{Send: "x\r"}, {Expect: "OK", Send: "y\r", TimeoutSec: 1}})
		e.m.Stream().EmitString("OK earlier\r\n")
		e.do("shutdown")
		if got := string(e.m.Stream().Input()); got != "x\r" {
			t.Fatalf("typed %q", got)
		}
		if st := e.m.State(); st != api.RunRunning {
			t.Fatalf("state %s", st)
		}
	})
	t.Run("script without a final command (V7 sync) settles, then powers off", func(t *testing.T) {
		shortShutdown(t)
		e := runningEnv(t)
		e.sys.SetScript("bsd211", []api.ScriptStep{{Send: "sync\r"}, {Expect: "# ", Send: "sync\r"}, {Expect: "# "}})
		shell(e, -1)
		e.do("shutdown")
		if got := string(e.m.Stream().Input()); got != "sync\rsync\r" {
			t.Fatalf("typed %q", got)
		}
		if st := e.m.State(); st != api.RunOff || e.key() != keyOff {
			t.Fatalf("state %s key %s", st, e.key())
		}
	})
	t.Run("expect timeout: toast, power stays on", func(t *testing.T) {
		shortShutdown(t)
		e := runningEnv(t)
		e.do("shutdown") // nobody answers "sync"
		if st := e.m.State(); st != api.RunRunning || e.key() != keyPower {
			t.Fatalf("state %s key %s", st, e.key())
		}
		ts := e.toastList()
		if len(ts) != 1 || !strings.HasPrefix(ts[0], "warn:") || !strings.Contains(ts[0], "Pull the plug") {
			t.Fatalf("toasts %q", ts)
		}
		if got := phases(e.progressList()); !slices.Equal(got, []string{"step", "error"}) {
			t.Fatalf("phases %q", got)
		}
	})
	t.Run("the OS does not halt: toast", func(t *testing.T) {
		shortShutdown(t)
		e := runningEnv(t)
		shell(e, -1) // prints "halting" but never halts
		e.do("shutdown")
		if st := e.m.State(); st != api.RunRunning {
			t.Fatalf("state %s", st)
		}
		if ts := e.toastList(); len(ts) != 1 || !strings.Contains(ts[0], "did not halt") {
			t.Fatalf("toasts %q", ts)
		}
	})
	t.Run("HALT switch cancels", func(t *testing.T) {
		shortShutdown(t)
		haltAfterScript = 5 * time.Second
		e := runningEnv(t)
		shell(e, -1)
		e.input(api.PanelInput{Kind: "shutdownOS"})
		paneltest.WaitFor(5*time.Second, func() bool { return strings.Contains(string(e.m.Stream().Input()), "shutdown") })
		e.do("halt")
		if st := e.m.State(); st != api.RunHalted || e.key() != keyPower {
			t.Fatalf("state %s key %s", st, e.key())
		}
		if got := phases(e.progressList()); got[len(got)-1] != "cancelled" {
			t.Fatalf("phases %q", got)
		}
		if n := e.m.Count("PowerOff"); n != 0 {
			t.Fatal("powered off after a cancelled shutdown")
		}
	})
	t.Run("key OFF pulls the plug", func(t *testing.T) {
		shortShutdown(t)
		defaultStepTimeout = 10 * time.Second
		e := runningEnv(t)
		e.input(api.PanelInput{Kind: "shutdownOS"})
		paneltest.WaitFor(5*time.Second, func() bool { return len(e.m.Stream().Input()) > 0 })
		start := time.Now()
		e.do("key off")
		if time.Since(start) > 2*time.Second {
			t.Fatal("key OFF waited for the shutdown script")
		}
		if st := e.m.State(); st != api.RunOff {
			t.Fatalf("state %s", st)
		}
		if got := phases(e.progressList()); got[len(got)-1] != "cancelled" {
			t.Fatalf("phases %q", got)
		}
		if ts := e.toastList(); len(ts) != 0 {
			t.Fatalf("toasts %q", ts)
		}
	})
	t.Run("refused when not running, without a script, or twice", func(t *testing.T) {
		shortShutdown(t)
		e := haltedEnv(t)
		if h := e.do("shutdown"); !strings.Contains(h, "not running") {
			t.Fatalf("hint %q", h)
		}
		e.do("enable")
		e.do("cont")
		e.sys.SetScript("bsd211", nil)
		if h := e.do("shutdown"); !strings.Contains(h, "no automatic shutdown") {
			t.Fatalf("hint %q", h)
		}
		e.sys.SetScript("bsd211", []api.ScriptStep{{Expect: "never"}})
		e.input(api.PanelInput{Kind: "shutdownOS"})
		if h := e.input(api.PanelInput{Kind: "shutdownOS"}); !strings.Contains(h, "already in progress") {
			t.Fatalf("hint %q", h)
		}
		if h := e.input(api.PanelInput{Kind: "boot"}); !strings.Contains(h, "shutdown is already in progress") {
			t.Fatalf("boot during shutdown: hint %q", h)
		}
	})
}

func TestShutdownOSForDaemonStop(t *testing.T) {
	shortShutdown(t)
	e := runningEnv(t)
	shell(e, 20*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var busy atomic.Value
	go func() {
		paneltest.WaitFor(5*time.Second, func() bool {
			b := e.p.Console().Busy
			if b != "" {
				busy.Store(b)
			}
			return b != ""
		})
	}()
	if err := e.p.ShutdownOS(ctx); err != nil {
		t.Fatal(err)
	}
	if st := e.m.State(); st != api.RunHalted {
		t.Fatalf("state %s (ShutdownOS must not power off)", st)
	}
	if e.key() != keyPower || e.m.Count("PowerOff") != 0 {
		t.Fatalf("key %s, PowerOff %d", e.key(), e.m.Count("PowerOff"))
	}
	if b, _ := busy.Load().(string); b != "shutdown" {
		t.Fatalf("busy during ShutdownOS = %q", b)
	}
	// Not running: nothing to do.
	if err := e.p.ShutdownOS(ctx); err != nil {
		t.Fatalf("second ShutdownOS: %v", err)
	}
	// It also works after Run returned.
	e.do("enable")
	e.do("cont")
	e.stop()
	if err := e.p.ShutdownOS(ctx); err != nil {
		t.Fatal(err)
	}
	if st := e.m.State(); st != api.RunHalted {
		t.Fatalf("state %s", st)
	}
}

// TestShutdownAnswerDelay: a Send that follows an Expect match is typed
// answerDelay later (RSX/RSTS post their read after the prompt); a stop
// during that pause ends the script like any other stop.
func TestShutdownAnswerDelay(t *testing.T) {
	type typed struct {
		at   time.Time
		text string
	}
	record := func(e *env) func() []typed {
		var mu sync.Mutex
		var got []typed
		e.m.Stream().OnWrite = func(p []byte) {
			mu.Lock()
			got = append(got, typed{time.Now(), string(p)})
			mu.Unlock()
		}
		return func() []typed {
			mu.Lock()
			defer mu.Unlock()
			return slices.Clone(got)
		}
	}
	// watching waits until the shutdown runner subscribed to the console.
	watching := func(e *env) {
		if !paneltest.WaitFor(5*time.Second, func() bool { return e.m.Stream().Subscribers() > 0 }) {
			t.Fatal("the shutdown script does not watch the console")
		}
	}
	t.Run("the answer to a prompt waits", func(t *testing.T) {
		shortShutdown(t)
		e := runningEnv(t)
		e.sys.SetScript("bsd211", []api.ScriptStep{{Send: "HELLO 1,2\r"}, {Expect: "Password:", Send: "SYSTEM\r"}, {Expect: "Option: "}})
		writes := record(e)
		e.input(api.PanelInput{Kind: "shutdownOS"})
		watching(e)
		if !paneltest.WaitFor(5*time.Second, func() bool { return len(writes()) == 1 }) {
			t.Fatalf("first send (no expect) not typed: %v", writes())
		}
		prompt := time.Now()
		e.m.Stream().EmitString("Password:")
		if !paneltest.WaitFor(5*time.Second, func() bool { return len(writes()) == 2 }) {
			t.Fatalf("answer not typed: %v", writes())
		}
		w := writes()
		if w[1].text != "SYSTEM\r" || w[1].at.Sub(prompt) < answerDelay {
			t.Fatalf("answer %q typed %v after the prompt, want >= %v", w[1].text, w[1].at.Sub(prompt), answerDelay)
		}
		e.m.Stream().EmitString("\r\nOption: ")
		e.idle()
		if st := e.m.State(); st != api.RunOff || e.key() != keyOff {
			t.Fatalf("state %s key %s", st, e.key())
		}
	})
	t.Run("the OS halting during the pause powers off without typing", func(t *testing.T) {
		shortShutdown(t)
		e := runningEnv(t)
		e.sys.SetScript("bsd211", []api.ScriptStep{{Expect: "# ", Send: "shutdown -h now\r"}})
		writes := record(e)
		e.input(api.PanelInput{Kind: "shutdownOS"})
		watching(e)
		e.m.Stream().EmitString("# ")
		e.m.StopCPU("HALT instruction", 0o14)
		e.idle()
		if w := writes(); len(w) != 0 {
			t.Fatalf("typed %v after the CPU halted", w)
		}
		if st := e.m.State(); st != api.RunOff || e.key() != keyOff {
			t.Fatalf("state %s key %s", st, e.key())
		}
	})
	t.Run("HALT during the pause cancels", func(t *testing.T) {
		shortShutdown(t)
		e := runningEnv(t)
		e.sys.SetScript("bsd211", []api.ScriptStep{{Expect: "# ", Send: "shutdown -h now\r"}})
		writes := record(e)
		e.input(api.PanelInput{Kind: "shutdownOS"})
		watching(e)
		e.m.Stream().EmitString("# ")
		e.input(api.PanelInput{Kind: "latch", Name: "ENABLE_HALT", Value: "halt"})
		e.idle()
		if w := writes(); len(w) != 0 {
			t.Fatalf("typed %v after the HALT", w)
		}
		if st := e.m.State(); st != api.RunHalted || e.key() != keyPower {
			t.Fatalf("state %s key %s", st, e.key())
		}
		if got := phases(e.progressList()); got[len(got)-1] != "cancelled" {
			t.Fatalf("phases %q", got)
		}
	})
}
