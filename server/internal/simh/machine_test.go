package simh

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"pdp11emu/server/internal/api"
)

// TestSession walks through a whole power cycle: boot dialogue, login and a
// command on the console, live switch register, halt, examine/deposit,
// step, continue, a transparent speed change, statistics, power off.
func TestSession(t *testing.T) {
	h := newHarness(t)
	m := h.m
	if _, _, ok := m.PanelSource().ReadFrame(make([]api.Sample, 8)); ok {
		t.Fatal("panel source ok while off")
	}
	h.powerOnBooted()
	if m.State() != api.RunRunning || !m.Ready() || m.SystemID() != "fake" || !m.Console().Connected() {
		t.Fatalf("state %s ready %v system %q connected %v", m.State(), m.Ready(), m.SystemID(), m.Console().Connected())
	}
	pidFile, err := os.ReadFile(filepath.Join(m.opts.WorkDir, "pdp11.pid"))
	if err != nil || strings.TrimSpace(string(pidFile)) != strconv.Itoa(h.pid()) {
		t.Fatalf("pid file %q (%v), pid %d", pidFile, err, h.pid())
	}

	// The boot dialogue answered the "@" prompt; log in and run commands.
	hist, cancel := m.Console().SubscribeHistory(func([]byte) {})
	cancel()
	if !strings.Contains(string(hist), "@rkunix") || !strings.Contains(string(hist), "lo\x7fgin: ") {
		t.Fatalf("history %q", hist)
	}
	w := h.watch()
	if _, err := m.Console().Write([]byte("root\r")); err != nil {
		t.Fatal(err)
	}
	w.waitFor(t, "# ")
	m.Console().Write([]byte("echo hello world\r"))
	w.waitFor(t, "hello world\r\n# ")
	m.SetSwitchRegister(0o123456)
	m.Console().Write([]byte("sr\r"))
	w.waitFor(t, "SR=123456")

	// Panel export and statistics while running.
	time.Sleep(1100 * time.Millisecond)
	m.Stats()
	time.Sleep(1100 * time.Millisecond)
	if st := m.Stats(); st.MIPS <= 0 || st.UptimeSec < 2 || st.SamplesPerSec == 0 {
		t.Fatalf("stats %+v", st)
	}
	if n, snap, ok := m.PanelSource().ReadFrame(make([]api.Sample, 64)); !ok || n == 0 || snap.InstrCount == 0 {
		t.Fatalf("ReadFrame: n=%d ok=%v snap=%+v", n, ok, snap)
	}

	// Halt: EvStop before EvState(halted).
	if err := m.Halt(h.ctx()); err != nil {
		t.Fatal(err)
	}
	ev := h.expect("stop:Front panel HALT", "state:halted")
	if !ev[0].Stop.Requested || ev[0].Stop.PC == 0 {
		t.Fatalf("stop %+v", ev[0].Stop)
	}
	if err := m.Halt(h.ctx()); err != nil { // no-op when halted
		t.Fatal(err)
	}
	if _, err := m.Console().Write([]byte("x")); err != nil {
		t.Fatalf("console input while halted: %v", err)
	}
	if m.Console().Queued() != 1 {
		t.Fatalf("queued %d (input is held while halted)", m.Console().Queued())
	}
	m.Console().CancelInput()

	// Examine and deposit.
	ctx := h.ctx()
	if err := m.Deposit(ctx, api.Location{Addr: 0o1000}, 0o12345); err != nil {
		t.Fatal(err)
	}
	if v, err := m.Examine(ctx, api.Location{Addr: 0o1000}); err != nil || v != 0o12345 {
		t.Fatalf("examine = %o, %v", v, err)
	}
	if v, err := m.Examine(ctx, api.Location{Addr: 0o1000, Virtual: true, Mode: api.ModeKernel, DSpace: true}); err != nil || v != 0o12345 {
		t.Fatalf("virtual examine = %o, %v", v, err)
	}
	if err := m.Deposit(ctx, api.Location{Register: "R00"}, 0o777); err != nil {
		t.Fatal(err)
	}
	if v, err := m.Examine(ctx, api.Location{Register: "r00"}); err != nil || v != 0o777 {
		t.Fatalf("R00 = %o, %v", v, err)
	}
	for _, loc := range []api.Location{{Addr: 0o17777700}, {Addr: 0o4000000}, {Addr: 0o20000000}, {Register: "R6"}} {
		if _, err := m.Examine(ctx, loc); !errors.Is(err, api.ErrAddress) {
			t.Fatalf("examine %+v: %v, want ErrAddress", loc, err)
		}
	}
	if err := m.Deposit(ctx, api.Location{Addr: 0o17777000}, 1); !errors.Is(err, api.ErrAddress) {
		t.Fatalf("deposit to the I/O page hole: %v", err)
	}
	if pa, err := m.Translate(ctx, api.Location{Addr: 0o177570, Virtual: true, Mode: api.ModeUser}); err != nil || pa != 0o17777570 {
		t.Fatalf("translate = %o, %v", pa, err)
	}
	if pa, err := m.Translate(ctx, api.Location{Addr: 0o1234}); err != nil || pa != 0o1234 {
		t.Fatalf("translate physical = %o, %v", pa, err)
	}
	if _, err := m.Translate(ctx, api.Location{Register: "PC"}); err == nil {
		t.Fatal("translate of a register")
	}

	// Step: only EvStop, the state stays halted.
	if err := m.Step(ctx); err != nil {
		t.Fatal(err)
	}
	h.expect("stop:Step expired")
	h.quiet(100 * time.Millisecond)
	if m.State() != api.RunHalted {
		t.Fatalf("state %s after step", m.State())
	}

	// Continue, then a transparent speed change: no events at all.
	if err := m.Continue(ctx); err != nil {
		t.Fatal(err)
	}
	h.expect("state:running")
	if _, err := m.Examine(ctx, api.Location{Addr: 0}); !errors.Is(err, api.ErrNotHalted) {
		t.Fatalf("examine while running: %v", err)
	}
	if err := m.SetSpeed(ctx, api.SpeedAuthentic); err != nil {
		t.Fatal(err)
	}
	h.quiet(200 * time.Millisecond)
	if m.Speed() != api.SpeedAuthentic || m.State() != api.RunRunning {
		t.Fatalf("speed %s state %s", m.Speed(), m.State())
	}
	if !logged(m, "sim> set throttle 1M") {
		t.Fatalf("speed command not sent; log %q", m.Log().Lines(30))
	}
	m.Console().Write([]byte("echo still alive\r"))
	w.waitFor(t, "still alive")

	// Power off: stopping, the requested stop, off.
	if err := m.PowerOff(ctx); err != nil {
		t.Fatal(err)
	}
	ev = h.expect("state:stopping", "stop:Front panel HALT", "state:off")
	if !ev[1].Stop.Requested {
		t.Fatal("power-off stop not marked requested")
	}
	if m.Console().Connected() || m.SystemID() != "" || m.Ready() {
		t.Fatal("still connected/ready after power off")
	}
	if _, _, ok := m.PanelSource().ReadFrame(make([]api.Sample, 8)); ok {
		t.Fatal("panel source ok after power off")
	}
	if _, err := m.Console().Write([]byte("x")); !errors.Is(err, api.ErrNotPowered) {
		t.Fatalf("write after power off: %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.opts.WorkDir, "pdp11.pid")); !os.IsNotExist(err) {
		t.Fatalf("pid file left behind: %v", err)
	}
	if !logged(m, "Goodbye") {
		t.Fatal("simulator did not quit")
	}
}

func logged(m *Machine, s string) bool {
	for _, l := range m.Log().Lines(0) {
		if strings.Contains(l, s) {
			return true
		}
	}
	return false
}

func TestOperationsWhenOff(t *testing.T) {
	h := newHarness(t)
	m, ctx := h.m, h.ctx()
	for name, err := range map[string]error{
		"Halt":         m.Halt(ctx),
		"Continue":     m.Continue(ctx),
		"Step":         m.Step(ctx),
		"Start":        m.Start(ctx, 0o1000),
		"ConsoleReset": m.ConsoleReset(ctx),
		"Boot":         m.Boot(ctx),
		"SetSpeed":     m.SetSpeed(ctx, api.SpeedFast),
		"Deposit":      m.Deposit(ctx, api.Location{}, 0),
	} {
		if !errors.Is(err, api.ErrNotPowered) {
			t.Errorf("%s: %v, want ErrNotPowered", name, err)
		}
	}
	if _, err := m.Examine(ctx, api.Location{}); !errors.Is(err, api.ErrNotPowered) {
		t.Errorf("Examine: %v", err)
	}
	if err := m.PowerOff(ctx); err != nil {
		t.Fatal(err)
	}
	h.quiet(50 * time.Millisecond)
	if m.Speed() != api.SpeedFast || m.State() != api.RunOff {
		t.Fatalf("speed %s state %s", m.Speed(), m.State())
	}
}

// TestBootRestartsAfterExecution: the boot dialogue only works in a fresh
// simulator, so Boot after the CPU ran restarts the process.
func TestBootRestartsAfterExecution(t *testing.T) {
	h := newHarness(t)
	h.powerOnBooted()
	pid := h.pid()
	if err := h.m.Boot(h.ctx()); err != nil {
		t.Fatal(err)
	}
	h.expect("state:starting", "state:halted", "state:running", "ready")
	if h.pid() == pid || h.pid() == 0 {
		t.Fatalf("pid %d after restart (was %d)", h.pid(), pid)
	}
	if h.prov.calls != 2 {
		t.Fatalf("PrepareRun called %d times", h.prov.calls)
	}
}

// TestBootFreshSimulator: a simulator that has executed nothing boots in
// place.
func TestBootFreshSimulator(t *testing.T) {
	h := newHarness(t)
	if err := h.m.PowerOn(h.ctx(), api.PowerOnOptions{}); err != nil {
		t.Fatal(err)
	}
	h.expect("state:starting", "state:halted")
	pid := h.pid()
	if err := h.m.Boot(h.ctx()); err != nil {
		t.Fatal(err)
	}
	h.expect("state:running", "ready")
	if h.pid() != pid {
		t.Fatal("fresh simulator was restarted")
	}
	// A deposit, a console reset and START do not boot; the next Boot restarts.
	if err := h.m.Halt(h.ctx()); err != nil {
		t.Fatal(err)
	}
	h.expect("stop:Front panel HALT", "state:halted")
	if err := h.m.ConsoleReset(h.ctx()); err != nil {
		t.Fatal(err)
	}
	h.quiet(50 * time.Millisecond)
}

// TestNaturalStop: a HALT instruction stops the CPU without any request.
func TestNaturalStop(t *testing.T) {
	h := newHarness(t)
	m, ctx := h.m, h.ctx()
	if err := m.PowerOn(ctx, api.PowerOnOptions{}); err != nil {
		t.Fatal(err)
	}
	h.expect("state:starting", "state:halted")
	if err := m.Deposit(ctx, api.Location{Addr: 0o1000}, 0); err != nil { // HALT
		t.Fatal(err)
	}
	if err := m.Start(ctx, 0o1000); err != nil {
		t.Fatal(err)
	}
	ev := h.expect("state:running", "stop:HALT instruction", "state:halted")
	if s := ev[1].Stop; s.PC != 0o1002 || s.Insn != "HALT" || s.Requested {
		t.Fatalf("stop %+v", s)
	}
	// The same with CONT, and the output of a second "run" (reset noise).
	if err := m.Start(ctx, 0o1000); err != nil {
		t.Fatal(err)
	}
	h.expect("state:running", "stop:HALT instruction", "state:halted")
	if !logged(m, "Resetting all devices") {
		t.Fatal("reset warning not logged")
	}
	if err := m.Deposit(ctx, api.Location{Register: "PC"}, 0o1000); err != nil {
		t.Fatal(err)
	}
	if err := m.Step(ctx); err != nil { // stepping a HALT
		t.Fatal(err)
	}
	h.expect("stop:HALT instruction")
	h.quiet(50 * time.Millisecond)
}

// TestHaltRace: a halt requested right after a run command was written must
// stop that run (the simulator honours it at its first sample).
func TestHaltRace(t *testing.T) {
	h := newHarness(t)
	m, ctx := h.m, h.ctx()
	if err := m.PowerOn(ctx, api.PowerOnOptions{}); err != nil {
		t.Fatal(err)
	}
	h.expect("state:starting", "state:halted")
	if err := m.Deposit(ctx, api.Location{Addr: 0o1000}, 0o777); err != nil { // BR .
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if err := m.Start(ctx, 0o1000); err != nil {
			t.Fatal(err)
		}
		if err := m.Halt(ctx); err != nil {
			t.Fatalf("round %d: %v", i, err)
		}
		h.expect("state:running", "stop:Front panel HALT", "state:halted")
	}
}

// TestBootFailures: boot commands that stop without a stop line.
func TestBootFailures(t *testing.T) {
	t.Run("non-existent device", func(t *testing.T) {
		h := newHarness(t)
		h.prov.boot = "boot xx0"
		err := h.m.PowerOn(h.ctx(), api.PowerOnOptions{Boot: true})
		if err != nil {
			t.Fatal(err)
		}
		ev := h.expect("state:starting", "state:halted", "state:running", "stop:", "error", "state:halted")
		if !strings.Contains(ev[4].Text, "Non-existent device: XX0") {
			t.Fatalf("error %q", ev[4].Text)
		}
		if ev[3].Stop.PC != 0o104 {
			t.Fatalf("PC from the register block: %o", ev[3].Stop.PC)
		}
	})
	t.Run("expect stop", func(t *testing.T) {
		h := newHarness(t, withDirectives("expect-stop"))
		if err := h.m.PowerOn(h.ctx(), api.PowerOnOptions{Boot: true}); err != nil {
			t.Fatal(err)
		}
		ev := h.expect("state:starting", "state:halted", "state:running", "stop:", "error", "state:halted")
		if !strings.Contains(ev[4].Text, "EXPECT-ACTION-OUTPUT") {
			t.Fatalf("error %q", ev[4].Text)
		}
	})
	t.Run("bad dialogue", func(t *testing.T) {
		h := newHarness(t)
		h.prov.dialog = []string{`expect "@" send "x"; continue`, "expect"}
		err := h.m.PowerOn(h.ctx(), api.PowerOnOptions{Boot: true})
		if err == nil || !strings.Contains(err.Error(), "dialog line 2") {
			t.Fatalf("PowerOn: %v", err)
		}
		h.expect("state:starting", "state:halted", "error")
		if h.m.State() != api.RunHalted {
			t.Fatalf("state %s", h.m.State())
		}
	})
	t.Run("late output", func(t *testing.T) {
		h := newHarness(t, withDirectives("late-output"))
		h.powerOnBooted()
		if err := h.m.Halt(h.ctx()); err != nil {
			t.Fatal(err)
		}
		h.expect("stop:Front panel HALT", "state:halted")
		if !logged(h.m, "late line from the simulator") {
			t.Fatal("late output not logged")
		}
	})
}

func TestStartupDiagnostics(t *testing.T) {
	tests := []struct {
		name string
		opts []harnessOpt
		want []string // in LastError; empty: must power on
	}{
		{"missing disk", []harnessOpt{withBootINI("set rk enabled", "attach -e rk0 /nonexistent/disk.dsk")},
			[]string{"boot.ini line 2 (attach -e rk0 /nonexistent/disk.dsk)", "Cannot open"}},
		{"bad device", []harnessOpt{withBootINI("set bogus on", "attach -e rk0 ${DISK0}")},
			[]string{"boot.ini line 1 (set bogus on): Non-existent device: BOGUS"}},
		{"echo and informational messages", []harnessOpt{withBootINI("attach -e rk0 ${DISK0}", "echo Booting fake Unix", "set rk enabled")}, nil},
		{"modem control notice", []harnessOpt{func(o *Options, p *testProvider) {
			o.TTYTelnet = "127.0.0.1:" + strconv.Itoa(freePort(t))
			p.modem = true
		}}, nil},
		{"terminal listener busy", []harnessOpt{func(o *Options, p *testProvider) {
			l, _ := netListen(t)
			o.TTYTelnet = l
		}}, []string{"terminal listener PDP11_TTY_TELNET", "Can't open network port"}},
		{"exit during start-up", []harnessOpt{withDirectives("exit-on-start")}, []string{"exited during start-up", "fatal start-up problem"}},
		{"no prompt", []harnessOpt{withDirectives("no-prompt")}, []string{"did not reach its prompt"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.opts...)
			h.m.to.startup = 2 * time.Second
			err := h.m.PowerOn(h.ctx(), api.PowerOnOptions{Boot: true})
			if tc.want == nil {
				if err != nil {
					t.Fatalf("PowerOn: %v", err)
				}
				h.expect("state:starting", "state:halted", "state:running", "ready")
				return
			}
			if err == nil {
				t.Fatal("PowerOn succeeded")
			}
			h.expect("state:starting", "state:error")
			for _, w := range tc.want {
				if !strings.Contains(h.m.LastError(), w) {
					t.Errorf("LastError %q does not contain %q", h.m.LastError(), w)
				}
			}
			if h.pid() != 0 {
				t.Fatal("simulator left running")
			}
		})
	}
}

// netListen occupies a local port and returns its address.
func netListen(t *testing.T) (string, func()) {
	t.Helper()
	l, err := netListenTCP()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l.Addr().String(), func() { l.Close() }
}

func TestConsolePortBusy(t *testing.T) {
	h := newHarness(t)
	l, err := netListenTCPPort(h.m.opts.ConsolePort)
	if err != nil {
		t.Skip(err)
	}
	defer l.Close()
	if err := h.m.PowerOn(h.ctx(), api.PowerOnOptions{}); err == nil {
		t.Fatal("PowerOn succeeded")
	}
	if e := h.m.LastError(); !strings.Contains(e, "run.ini line 6 (set console") || !strings.Contains(e, "Can't open network port") {
		t.Fatalf("LastError %q", e)
	}
}

func TestHaltNotHonoured(t *testing.T) {
	h := newHarness(t, withDirectives("ignore-halt"))
	h.powerOnBooted()
	err := h.m.Halt(h.ctx())
	if err == nil {
		t.Fatal("Halt succeeded")
	}
	ev := h.expect("error", "state:error")
	if !strings.Contains(ev[0].Text, "not responding") || !strings.Contains(h.m.LastError(), "not responding") {
		t.Fatalf("error %q / %q", ev[0].Text, h.m.LastError())
	}
	if h.pid() != 0 {
		t.Fatal("simulator left running")
	}
	// From error, power on again works.
	h.prov.bootINI = []string{"attach -e rk0 ${DISK0}"}
	h.powerOnBooted()
}

func TestUnexpectedExit(t *testing.T) {
	h := newHarness(t)
	h.powerOnBooted()
	w := h.watch()
	h.m.Console().Write([]byte("root\r"))
	w.waitFor(t, "# ")
	h.m.Console().Write([]byte("crash\r"))
	ev := h.expect("error", "state:error")
	if !strings.Contains(ev[0].Text, "exited unexpectedly") || !strings.Contains(ev[0].Text, "exit status 3") {
		t.Fatalf("error %q", ev[0].Text)
	}
	if h.m.SystemID() != "" || h.m.Console().Connected() {
		t.Fatal("still connected")
	}
	if err := h.m.PowerOff(h.ctx()); err != nil {
		t.Fatal(err)
	}
	h.expect("state:off")
}

func TestPowerOffCancelsPowerOn(t *testing.T) {
	h := newHarness(t, withDirectives("slow-start=5000"))
	h.m.to.startup = 10 * time.Second
	errc := make(chan error, 1)
	go func() { errc <- h.m.PowerOn(context.Background(), api.PowerOnOptions{Boot: true}) }()
	h.expect("state:starting")
	start := time.Now()
	if err := h.m.PowerOff(h.ctx()); err != nil {
		t.Fatal(err)
	}
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("PowerOn: %v", err)
	}
	if d := time.Since(start); d > 4*time.Second {
		t.Fatalf("power off took %s", d)
	}
	h.expect("state:off")
	if h.m.State() != api.RunOff || h.m.LastError() != "" {
		t.Fatalf("state %s, LastError %q", h.m.State(), h.m.LastError())
	}
}

func TestSetSpeedWhileStarting(t *testing.T) {
	h := newHarness(t, withDirectives("slow-start=500"))
	errc := make(chan error, 1)
	go func() { errc <- h.m.PowerOn(context.Background(), api.PowerOnOptions{}) }()
	h.expect("state:starting")
	if err := h.m.SetSpeed(h.ctx(), api.SpeedAuthentic); err != nil {
		t.Fatal(err)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	h.expect("state:halted")
	if h.m.Speed() != api.SpeedAuthentic {
		t.Fatalf("speed %s", h.m.Speed())
	}
	// Either run.ini had it, or it was applied at the prompt.
	if h.prov.speeds[0] != api.SpeedAuthentic && !logged(h.m, "sim> set throttle 1M") {
		t.Fatalf("speed not applied: run.ini speed %s", h.prov.speeds[0])
	}
}

func TestSpeedFromSystems(t *testing.T) {
	h := newHarness(t)
	h.sys.speeds["fake"] = api.SpeedAuthentic
	if err := h.m.PowerOn(h.ctx(), api.PowerOnOptions{}); err != nil {
		t.Fatal(err)
	}
	h.expect("state:starting", "state:halted")
	if h.prov.speeds[0] != api.SpeedAuthentic || h.m.Speed() != api.SpeedAuthentic {
		t.Fatalf("speed %s / %s", h.prov.speeds[0], h.m.Speed())
	}
	if err := h.m.SetSpeed(h.ctx(), api.SpeedFast); err != nil { // halted: no stop at all
		t.Fatal(err)
	}
	h.quiet(50 * time.Millisecond)
	if !slices.Contains(h.m.Log().Lines(0), "sim> set cpu idle") { // not idle=2
		t.Fatal("fast commands not sent")
	}
}

func TestInstallOnPowerOn(t *testing.T) {
	t.Run("redistributable", func(t *testing.T) {
		h := newHarness(t)
		i := h.sys.infos["fake"]
		i.Installed = false
		h.sys.infos["fake"] = i
		if err := h.m.PowerOn(h.ctx(), api.PowerOnOptions{}); err != nil {
			t.Fatal(err)
		}
		if h.sys.installs != 1 {
			t.Fatalf("%d installs", h.sys.installs)
		}
	})
	t.Run("licence not accepted", func(t *testing.T) {
		h := newHarness(t)
		h.sys.infos["fake"] = api.SystemInfo{ID: "fake", Name: "RT-11", License: api.LicenseInfo{Name: "Mentec", Class: "hobbyist"}}
		if err := h.m.PowerOn(h.ctx(), api.PowerOnOptions{}); !errors.Is(err, api.ErrNoSystem) {
			t.Fatalf("PowerOn: %v", err)
		}
		h.quiet(50 * time.Millisecond) // the state stays off
		if h.m.State() != api.RunOff || !strings.Contains(h.m.LastError(), "licence") || h.sys.installs != 0 {
			t.Fatalf("state %s LastError %q installs %d", h.m.State(), h.m.LastError(), h.sys.installs)
		}
	})
	t.Run("accepted by configuration", func(t *testing.T) {
		h := newHarness(t, func(o *Options, _ *testProvider) { o.AcceptLicenses = []string{"fake"} })
		h.sys.infos["fake"] = api.SystemInfo{ID: "fake", Name: "RT-11", License: api.LicenseInfo{Class: "hobbyist"}}
		if err := h.m.PowerOn(h.ctx(), api.PowerOnOptions{}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("install fails", func(t *testing.T) {
		h := newHarness(t)
		h.sys.infos["fake"] = api.SystemInfo{ID: "fake", Name: "Fake", License: api.LicenseInfo{Class: "redistributable"}}
		h.sys.onInst = func(string) error { return errors.New("download failed: no route to host") }
		if err := h.m.PowerOn(h.ctx(), api.PowerOnOptions{}); err == nil {
			t.Fatal("PowerOn succeeded")
		}
		h.expect("state:starting", "state:error")
		if !strings.Contains(h.m.LastError(), "no route to host") {
			t.Fatalf("LastError %q", h.m.LastError())
		}
	})
}

func TestStaleSimulatorKilled(t *testing.T) {
	h := newHarness(t)
	work := h.m.opts.WorkDir
	os.MkdirAll(work, 0o755)
	ini := filepath.Join(work, "stale.ini")
	os.WriteFile(ini, []byte("set cpu 11/70\n"), 0o644)
	stale := startFake(t, work, "stale.ini")
	os.WriteFile(filepath.Join(work, "pdp11.pid"), []byte(strconv.Itoa(stale.Process.Pid)+"\n"), 0o644)

	// A pid file naming another program is left alone.
	other := startSleep(t)
	if err := h.m.PowerOn(h.ctx(), api.PowerOnOptions{}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- stale.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stale simulator not killed")
	}
	h.m.PowerOff(h.ctx())
	os.WriteFile(filepath.Join(work, "pdp11.pid"), []byte(strconv.Itoa(other.Process.Pid)+"\n"), 0o644)
	if err := h.m.PowerOn(h.ctx(), api.PowerOnOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := other.Process.Signal(syscallSignal0); err != nil {
		t.Fatalf("unrelated process was killed: %v", err)
	}
}

func TestSimhIniRefused(t *testing.T) {
	h := newHarness(t)
	os.MkdirAll(h.m.opts.WorkDir, 0o755)
	os.WriteFile(filepath.Join(h.m.opts.WorkDir, "simh.ini"), []byte("set cpu 11/45\n"), 0o644)
	if err := h.m.PowerOn(h.ctx(), api.PowerOnOptions{}); err == nil {
		t.Fatal("PowerOn succeeded")
	}
	if !strings.Contains(h.m.LastError(), "simh.ini") || h.m.State() != api.RunError {
		t.Fatalf("state %s LastError %q", h.m.State(), h.m.LastError())
	}
}

func TestPowerCycles(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 3; i++ {
		h.powerOnBooted()
		if _, _, ok := h.m.PanelSource().ReadFrame(make([]api.Sample, 8)); !ok {
			t.Fatalf("cycle %d: no panel export", i)
		}
		if err := h.m.PowerOff(h.ctx()); err != nil {
			t.Fatal(err)
		}
		h.expect("state:stopping", "stop:Front panel HALT", "state:off")
	}
	hist, cancel := h.m.Console().SubscribeHistory(func([]byte) {})
	cancel()
	if n := strings.Count(string(hist), "@rkunix"); n != 3 {
		t.Fatalf("history holds %d boots, want 3 (it survives power cycles)", n)
	}
}

// TestConcurrentOperations hammers the machine from several goroutines; the
// race detector and the absence of deadlocks are the point.
func TestConcurrentOperations(t *testing.T) {
	h := newHarness(t)
	h.powerOnBooted()
	ctx := h.ctx()
	m := h.m
	var wg sync.WaitGroup
	stop := time.Now().Add(2 * time.Second)
	var mu sync.Mutex
	unexpected := []error{}
	ok := func(err error) {
		if err == nil || errors.Is(err, api.ErrNotHalted) || errors.Is(err, api.ErrBusy) {
			return
		}
		mu.Lock()
		unexpected = append(unexpected, err)
		mu.Unlock()
	}
	for g := 0; g < 6; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; time.Now().Before(stop); i++ {
				switch (g + i) % 7 {
				case 0:
					ok(m.Halt(ctx))
				case 1:
					ok(m.Continue(ctx))
				case 2:
					_, err := m.Examine(ctx, api.Location{Addr: 0o1000})
					ok(err)
				case 3:
					ok(m.SetSpeed(ctx, []api.Speed{api.SpeedFast, api.SpeedAuthentic}[i%2]))
				case 4:
					ok(m.Step(ctx))
				case 5:
					m.SetSwitchRegister(uint16(i))
					m.Stats()
					m.Console().Write([]byte("x"))
				case 6:
					ok(m.Deposit(ctx, api.Location{Addr: 0o2000}, uint16(i)))
				}
			}
		}(g)
	}
	wg.Wait()
	if len(unexpected) > 0 {
		t.Fatalf("unexpected errors: %v", unexpected)
	}
	if st := m.State(); st != api.RunHalted && st != api.RunRunning {
		t.Fatalf("state %s", st)
	}
	if err := m.PowerOff(ctx); err != nil {
		t.Fatal(err)
	}
	if m.State() != api.RunOff {
		t.Fatalf("state %s", m.State())
	}
}

func TestLogBuffer(t *testing.T) {
	h := newHarness(t)
	var mu sync.Mutex
	var live []string
	cancel := h.m.Log().Subscribe(func(l string) {
		mu.Lock()
		live = append(live, l)
		mu.Unlock()
	})
	if err := h.m.PowerOn(h.ctx(), api.PowerOnOptions{}); err != nil {
		t.Fatal(err)
	}
	h.m.Examine(h.ctx(), api.Location{Addr: 0o1000})
	cancel()
	lines := h.m.Log().Lines(0)
	joined := strings.Join(lines, "\n")
	for _, w := range []string{"PDP-11 simulator Open SIMH", "Listening on port", "sim> examine 1000", "1000:\t000000"} {
		if !strings.Contains(joined, w) {
			t.Errorf("log lacks %q:\n%s", w, joined)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(live, "\n") != joined {
		t.Errorf("subscriber saw %d lines, buffer has %d", len(live), len(lines))
	}
	if got := h.m.Log().Lines(2); len(got) != 2 || got[1] != lines[len(lines)-1] {
		t.Errorf("Lines(2) = %q", got)
	}
}

// TestConcurrentPowerOn: one power-on wins, the other is busy, and PowerOff
// still cancels the winner.
func TestConcurrentPowerOn(t *testing.T) {
	h := newHarness(t, withDirectives("slow-start=3000"))
	h.m.to.startup = 10 * time.Second
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { errs <- h.m.PowerOn(context.Background(), api.PowerOnOptions{Boot: true}) }()
	}
	h.expect("state:starting")
	busy := <-errs // the loser returns at once
	if !errors.Is(busy, api.ErrBusy) {
		t.Fatalf("second PowerOn: %v", busy)
	}
	if err := h.m.PowerOff(h.ctx()); err != nil {
		t.Fatal(err)
	}
	if err := <-errs; !errors.Is(err, context.Canceled) {
		t.Fatalf("winning PowerOn: %v", err)
	}
	h.expect("state:off")
}

// TestConsoleStopsEndWait: a halt from the console and every step end the
// WAIT state ("deposit WAIT 0") before the stop is reported; transparent
// stops (speed change, the halt of a Boot restart) and power off do not
// touch it.
func TestConsoleStopsEndWait(t *testing.T) {
	h := newHarness(t)
	m, ctx, log := h.m, h.ctx(), h.m.Log()
	ends := func() int {
		n := 0
		for _, l := range log.Lines(0) {
			if l == "sim> deposit WAIT 0" {
				n++
			}
		}
		return n
	}
	var mu sync.Mutex
	var atStop []int // ends() when each EvStop was delivered
	m.Subscribe(func(ev api.MachineEvent) {
		if ev.Kind == api.EvStop {
			mu.Lock()
			atStop = append(atStop, ends())
			mu.Unlock()
		}
	})
	stops := func() []int {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(atStop)
	}
	h.powerOnBooted()
	if err := m.SetSpeed(ctx, api.SpeedAuthentic); err != nil {
		t.Fatal(err)
	}
	h.quiet(50 * time.Millisecond)
	if err := m.Halt(ctx); err != nil {
		t.Fatal(err)
	}
	h.expect("stop:Front panel HALT", "state:halted")
	for i := 0; i < 2; i++ {
		if err := m.Step(ctx); err != nil {
			t.Fatal(err)
		}
		h.expect("stop:Step expired")
	}
	waitUntil(t, "the third stop", func() bool { return len(stops()) == 3 })
	if got := stops(); !slices.Equal(got, []int{1, 2, 3}) {
		t.Fatalf("WAIT ended %v times when the stops were reported, want [1 2 3] (log %q)", got, m.Log().Lines(40))
	}
	if err := m.Continue(ctx); err != nil {
		t.Fatal(err)
	}
	h.expect("state:running")
	if err := m.Boot(ctx); err != nil {
		t.Fatal(err)
	}
	h.expect("state:starting", "state:halted", "state:running", "ready")
	if err := m.PowerOff(ctx); err != nil {
		t.Fatal(err)
	}
	h.expect("state:stopping", "stop:Front panel HALT", "state:off")
	if n := ends(); n != 3 {
		t.Fatalf("WAIT ended %d times, want 3 (log %q)", n, m.Log().Lines(60))
	}
}

// TestRunConfigReleased: every configuration PrepareRun returned is
// released exactly once, when its simulator has exited or could not be
// started; after a power off before the state is reported off.
func TestRunConfigReleased(t *testing.T) {
	h := newHarness(t)
	m, p := h.m, h.prov
	var mu sync.Mutex
	atOff := -1
	m.Subscribe(func(ev api.MachineEvent) {
		if ev.Kind == api.EvState && ev.State == api.RunOff {
			mu.Lock()
			atOff = p.leases()
			mu.Unlock()
		}
	})
	h.powerOnBooted()
	if n := p.leases(); n != 1 {
		t.Fatalf("%d leases while running", n)
	}
	if err := m.Boot(h.ctx()); err != nil { // restart: a new simulator
		t.Fatal(err)
	}
	h.expect("state:starting", "state:halted", "state:running", "ready")
	if n := p.leases(); n != 1 || p.calls != 2 {
		t.Fatalf("%d leases after %d PrepareRun calls", n, p.calls)
	}
	if err := m.PowerOff(h.ctx()); err != nil {
		t.Fatal(err)
	}
	h.expect("state:stopping", "stop:Front panel HALT", "state:off")
	off := func() int {
		mu.Lock()
		defer mu.Unlock()
		return atOff
	}
	waitUntil(t, "EvState(off)", func() bool { return off() >= 0 })
	if off() != 0 || p.leases() != 0 {
		t.Fatalf("leases when off was reported: %d, now %d", off(), p.leases())
	}

	// The simulator dies.
	h.powerOnBooted()
	w := h.watch()
	m.Console().Write([]byte("root\r"))
	w.waitFor(t, "# ")
	m.Console().Write([]byte("crash\r"))
	h.expect("error", "state:error")
	waitUntil(t, "the release after the crash", func() bool { return p.leases() == 0 })

	// It cannot be started at all.
	m.opts.Binary = filepath.Join(h.dir, "no-such-simulator")
	if err := m.PowerOn(h.ctx(), api.PowerOnOptions{}); err == nil {
		t.Fatal("PowerOn without a simulator")
	}
	h.expect("state:starting", "state:error")
	if n := p.leases(); n != 0 {
		t.Fatalf("%d leases after a failed start", n)
	}
}
