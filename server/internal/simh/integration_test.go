package simh

// Integration tests with the real patched simulator (simh/out/pdp11, or
// PDP11_TEST_SIMH). The Unix V6 tests take the catalog's V6 kit from
// PDP11_TEST_CACHE (catalog archives named by their sha256, as "pdp11d
// fetch --cache" writes them). Each test is skipped when what it needs is
// missing.

import (
	"archive/zip"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pdp11emu"
	"pdp11emu/server/internal/api"
	"pdp11emu/server/internal/catalog"
)

func realBinary(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("PDP11_TEST_SIMH")
	if bin == "" {
		bin, _ = filepath.Abs("../../../simh/out/pdp11")
	}
	if fi, err := os.Stat(bin); err != nil || fi.Mode()&0o111 == 0 {
		t.Skipf("patched simulator not available at %s", bin)
	}
	return bin
}

// v6Disks extracts the disks of the catalog's Unix V6 kit from
// PDP11_TEST_CACHE into dir (or skips the test) and returns their paths.
func v6Disks(t *testing.T, dir string) []string {
	t.Helper()
	cache := os.Getenv("PDP11_TEST_CACHE")
	if cache == "" {
		t.Skip("PDP11_TEST_CACHE (a directory of catalog archives) is not set")
	}
	sub, err := fs.Sub(pdp11emu.Systems, "systems")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.Load(sub)
	if err != nil {
		t.Fatal(err)
	}
	sys, ok := cat.Get("unixv6")
	if !ok {
		t.Fatal("unixv6 is not in the catalog")
	}
	a := sys.Artifacts[0]
	zr, err := zip.OpenReader(filepath.Join(cache, strings.ToLower(a.SHA256)))
	if err != nil {
		t.Skipf("the unixv6 archive is not in PDP11_TEST_CACHE: %v", err)
	}
	defer zr.Close()
	var disks []string
	for _, f := range a.Files {
		r, err := zr.Open(f.Path)
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, filepath.Base(f.Path))
		out, err := os.Create(p)
		if err == nil {
			_, err = io.Copy(out, r)
			if cerr := out.Close(); err == nil {
				err = cerr
			}
		}
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		disks = append(disks, p)
	}
	return disks
}

// v6Harness runs the real simulator on fresh copies of the V6 disks.
func v6Harness(t *testing.T) *harness {
	t.Helper()
	bin := realBinary(t)
	h := newHarness(t, func(o *Options, p *testProvider) {
		o.Binary = bin
		o.InputCPS = 100
		p.bootINI = []string{"set rq disabled", "set tq disabled", "set rk enabled"}
		for i, disk := range v6Disks(t, p.dir) {
			p.bootINI = append(p.bootINI, "attach -e rk"+string(rune('0'+i))+" "+disk)
		}
		p.dialog = []string{`expect "@" send delay=20000,"rkunix\r"; continue`}
		p.ready = "login: "
	})
	h.m.to = defaultTiming
	return h
}

func (h *harness) login(w *consoleWatch) {
	h.t.Helper()
	h.m.Console().Write([]byte("root\r"))
	w.waitFor(h.t, "# ")
}

func TestRealV6Session(t *testing.T) {
	h := v6Harness(t)
	m, ctx := h.m, h.ctx()
	w := h.watch()
	h.powerOnBooted()
	h.login(w)
	m.Console().Write([]byte("echo hello from v6\r"))
	w.waitFor(t, "hello from v6\r\n# ")

	// Panel export while running.
	time.Sleep(1200 * time.Millisecond)
	m.Stats()
	time.Sleep(1100 * time.Millisecond)
	st := m.Stats()
	n, snap, ok := m.PanelSource().ReadFrame(make([]api.Sample, 16384))
	t.Logf("stats %+v; frame: %d samples, state %d, %d samples/s", st, n, snap.State, snap.SamplesPerSec)
	if !ok || n == 0 || snap.SamplesPerSec == 0 || st.MIPS <= 0 {
		t.Fatalf("no panel export data: ok=%v n=%d snap=%+v stats=%+v", ok, n, snap, st)
	}

	// Halt through the shm handshake.
	if err := m.Halt(ctx); err != nil {
		t.Fatal(err)
	}
	ev := h.expect("stop:Front panel HALT", "state:halted")
	t.Logf("stopped at PC %06o (%s)", ev[0].Stop.PC, ev[0].Stop.Insn)

	// The live switch register is what programs read at 17777570.
	m.SetSwitchRegister(0o1234)
	if v, err := m.Examine(ctx, api.Location{Addr: 0o17777570}); err != nil || v != 0o1234 {
		t.Fatalf("SR = %o, %v", v, err)
	}
	// Deposit/examine in memory V6 does not use (it is an 18-bit kernel).
	if err := m.Deposit(ctx, api.Location{Addr: 0o3000000}, 0o123456); err != nil {
		t.Fatal(err)
	}
	if v, err := m.Examine(ctx, api.Location{Addr: 0o3000000}); err != nil || v != 0o123456 {
		t.Fatalf("examine = %o, %v", v, err)
	}
	for _, loc := range []api.Location{{Addr: 0o17777700}, {Addr: 0o4000000}, {Addr: 0o17777000}} {
		if _, err := m.Examine(ctx, loc); !errors.Is(err, api.ErrAddress) {
			t.Fatalf("examine %o: %v, want ErrAddress", loc.Addr, err)
		}
	}
	pc, err := m.Examine(ctx, api.Location{Register: "PC"})
	if err != nil || pc != ev[0].Stop.PC {
		t.Fatalf("PC = %o, %v (stop said %o)", pc, err, ev[0].Stop.PC)
	}
	// V6 runs with memory management on: a kernel virtual address maps
	// through KIPAR, and virtual and physical examine agree.
	loc := api.Location{Addr: 0o1000, Virtual: true, Mode: api.ModeKernel}
	pa, err := m.Translate(ctx, loc)
	if err != nil {
		t.Fatal(err)
	}
	vv, err1 := m.Examine(ctx, loc)
	pv, err2 := m.Examine(ctx, api.Location{Addr: pa})
	if err1 != nil || err2 != nil || vv != pv {
		t.Fatalf("virtual %o = %o (%v), physical %o = %o (%v)", loc.Addr, vv, err1, pa, pv, err2)
	}
	t.Logf("kernel 1000 -> %o (%06o)", pa, vv)
	// Kernel page 6 maps the u. area of the current process: not identity.
	loc = api.Location{Addr: 0o140000, Virtual: true, Mode: api.ModeKernel, DSpace: true}
	pa, err = m.Translate(ctx, loc)
	if err != nil {
		t.Fatal(err)
	}
	vv, err1 = m.Examine(ctx, loc)
	pv, err2 = m.Examine(ctx, api.Location{Addr: pa})
	if err1 != nil || err2 != nil || vv != pv || pa == 0o140000 {
		t.Fatalf("u. area: virtual 140000 = %o (%v), physical %o = %o (%v)", vv, err1, pa, pv, err2)
	}
	t.Logf("kernel 140000 -> %o (%06o)", pa, vv)
	if pa, err := m.Translate(ctx, api.Location{Addr: 0o177570, Virtual: true, Mode: api.ModeKernel, DSpace: true}); err != nil || pa != 0o17777570 {
		t.Fatalf("kernel I/O page -> %o, %v", pa, err)
	}

	// Step, continue, transparent speed changes.
	if err := m.Step(ctx); err != nil {
		t.Fatal(err)
	}
	ev = h.expect("stop:Step expired")
	h.quiet(100 * time.Millisecond)
	if err := m.Continue(ctx); err != nil {
		t.Fatal(err)
	}
	h.expect("state:running")
	for _, sp := range []api.Speed{api.SpeedAuthentic, api.SpeedFast} {
		if err := m.SetSpeed(ctx, sp); err != nil {
			t.Fatal(err)
		}
		h.quiet(100 * time.Millisecond)
		m.Console().Write([]byte("echo speed " + string(sp) + "\r"))
		w.waitFor(t, "speed "+string(sp)+"\r\n# ")
	}

	// Halt races: a halt right after a continue stops that run.
	for i := 0; i < 10; i++ {
		if err := m.Halt(ctx); err != nil {
			t.Fatal(err)
		}
		h.expect("stop:Front panel HALT", "state:halted")
		if err := m.Continue(ctx); err != nil {
			t.Fatal(err)
		}
		h.expect("state:running")
	}

	// Shut down cleanly and power off.
	m.Console().Write([]byte("sync\r"))
	w.waitFor(t, "sync\r\n# ")
	if err := m.PowerOff(ctx); err != nil {
		t.Fatal(err)
	}
	h.expect("state:stopping", "stop:Front panel HALT", "state:off")
	if strings.Contains(strings.Join(m.Log().Lines(0), "\n"), "SIM-ERROR") {
		t.Fatalf("simulator errors in the log:\n%s", strings.Join(m.Log().Lines(0), "\n"))
	}
}

// TestRealV6Reboot: Boot after the CPU ran restarts the simulator and runs
// the dialogue again; power cycles reuse ports and the panel file.
func TestRealV6Reboot(t *testing.T) {
	h := v6Harness(t)
	w := h.watch()
	h.powerOnBooted()
	pid := h.pid()
	start := time.Now()
	if err := h.m.Boot(h.ctx()); err != nil {
		t.Fatal(err)
	}
	h.expect("state:starting", "state:halted", "state:running", "ready")
	t.Logf("restart to login took %s", time.Since(start))
	if h.pid() == pid {
		t.Fatal("not restarted")
	}
	h.login(w)
	for i := 0; i < 2; i++ {
		if err := h.m.PowerOff(h.ctx()); err != nil {
			t.Fatal(err)
		}
		h.expect("state:stopping", "stop:Front panel HALT", "state:off")
		h.powerOnBooted()
	}
	h.login(w)
}

// TestRealDisklessDeposits runs a tiny program deposited into a simulator
// without disks: a natural HALT, START, CONT and console output.
func TestRealDisklessDeposits(t *testing.T) {
	bin := realBinary(t)
	h := newHarness(t, func(o *Options, p *testProvider) {
		o.Binary = bin
		p.bootINI = nil
	})
	h.m.to = defaultTiming
	m, ctx := h.m, h.ctx()
	w := h.watch()
	if err := m.PowerOn(ctx, api.PowerOnOptions{}); err != nil {
		t.Fatalf("PowerOn: %v (%s)", err, m.LastError())
	}
	h.expect("state:starting", "state:halted")
	// 1000: mov #101,r4; 1004: tstb @#177564; bpl 1004; movb r4,@#177566;
	// inc r4; cmp r4,#133; bne 1004; halt
	prog := []uint16{0o12704, 0o101, 0o105737, 0o177564, 0o100375, 0o110437, 0o177566, 0o5204, 0o20427, 0o133, 0o1367, 0}
	for i, word := range prog {
		if err := m.Deposit(ctx, api.Location{Addr: 0o1000 + uint32(2*i)}, word); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Start(ctx, 0o1000); err != nil {
		t.Fatal(err)
	}
	ev := h.expect("state:running", "stop:HALT instruction", "state:halted")
	if ev[1].Stop.PC != 0o1030 {
		t.Fatalf("stop %+v", ev[1].Stop)
	}
	if v, err := m.Examine(ctx, api.Location{Register: "R04"}); err != nil || v != 0o133 {
		t.Fatalf("R4 = %o, %v", v, err)
	}
	// Run it again. SimH sends the last character of a run only when the
	// CPU runs again, and START (SimH "run") resets the DL11, which drops
	// it: the output of both runs ends in "Y".
	if err := m.Start(ctx, 0o1000); err != nil {
		t.Fatal(err)
	}
	h.expect("state:running", "stop:HALT instruction", "state:halted")
	w.waitFor(t, "ABCDEFGHIJKLMNOPQRSTUVWXYABCDEFGHIJKLMNOPQRSTUVWXY")
	if err := m.ConsoleReset(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.Deposit(ctx, api.Location{Register: "PC"}, 0o1000); err != nil {
		t.Fatal(err)
	}
	if err := m.Step(ctx); err != nil {
		t.Fatal(err)
	}
	ev = h.expect("stop:Step expired")
	if ev[0].Stop.PC != 0o1004 {
		t.Fatalf("step stopped at %o", ev[0].Stop.PC)
	}
}

// TestRealHaltEndsWait: a halt from the console ends the WAIT state, as on
// a KB11-C. Halted in a WAIT that no interrupt ends, every step executes
// one instruction (the BR after the WAIT, then the WAIT, ...), and CONT
// after a deposit to PC runs the new code at once, also at IPL 7.
func TestRealHaltEndsWait(t *testing.T) {
	bin := realBinary(t)
	h := newHarness(t, func(o *Options, p *testProvider) {
		o.Binary = bin
		p.bootINI = nil
	})
	h.m.to = defaultTiming
	m, ctx := h.m, h.ctx()
	if err := m.PowerOn(ctx, api.PowerOnOptions{}); err != nil {
		t.Fatalf("PowerOn: %v (%s)", err, m.LastError())
	}
	h.expect("state:starting", "state:halted")
	// 1000: wait; 1002: br 1000. 2000: mov #123,r0; 2004: halt.
	for a, w := range map[uint32]uint16{0o1000: 0o1, 0o1002: 0o776, 0o2000: 0o12700, 0o2002: 0o123, 0o2004: 0} {
		if err := m.Deposit(ctx, api.Location{Addr: a}, w); err != nil {
			t.Fatal(err)
		}
	}
	haltInWait := func() {
		t.Helper()
		if err := m.Start(ctx, 0o1000); err != nil {
			t.Fatal(err)
		}
		h.expect("state:running")
		time.Sleep(200 * time.Millisecond)
		if err := m.Halt(ctx); err != nil {
			t.Fatal(err)
		}
		ev := h.expect("stop:Front panel HALT", "state:halted")
		if ev[0].Stop.PC != 0o1002 {
			t.Fatalf("halted at %06o, not in the WAIT", ev[0].Stop.PC)
		}
	}

	haltInWait()
	for i, want := range []uint16{0o1000, 0o1002, 0o1000, 0o1002} {
		if err := m.Step(ctx); err != nil {
			t.Fatal(err)
		}
		if ev := h.expect("stop:Step expired"); ev[0].Stop.PC != want {
			t.Fatalf("step %d stopped at %06o, want %06o", i+1, ev[0].Stop.PC, want)
		}
	}

	haltInWait()
	for _, d := range []struct {
		loc api.Location
		v   uint16
	}{{api.Location{Addr: 0o17777776}, 0o340}, {api.Location{Register: "R00"}, 0}, {api.Location{Register: "PC"}, 0o2000}} {
		if err := m.Deposit(ctx, d.loc, d.v); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Continue(ctx); err != nil {
		t.Fatal(err)
	}
	if ev := h.expect("state:running", "stop:HALT instruction", "state:halted"); ev[1].Stop.PC != 0o2006 {
		t.Fatalf("stopped at %06o, want 002006", ev[1].Stop.PC)
	}
	if v, err := m.Examine(ctx, api.Location{Register: "R00"}); err != nil || v != 0o123 {
		t.Fatalf("R0 = %o, %v: the new code did not run", v, err)
	}
}
