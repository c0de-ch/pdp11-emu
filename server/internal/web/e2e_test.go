//go:build e2e

// End-to-end test against the real patched simulator (opt-in):
//
//	go test -tags e2e -run E2E ./server/internal/web/ [-args -simh /path/to/pdp11]
//	    [-bsd211 /path/to/2.11BSD_rq.dsk] [-port N]
//
// go test runs the tests in this package's directory, so relative paths
// are relative to server/internal/web (-simh defaults to the repository's
// simh/out/pdp11): give absolute ones. A file named by a flag that does
// not exist fails the test; without the flag the test is skipped.
//
// It powers on a machine without disks, toggles in the light chaser of the
// UI tutorial (UI.md §6) through the WebSocket exactly as the UI would,
// starts it and checks the DISPLAY REGISTER lamps, then halts it and
// examines memory.
package web

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/bits"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"pdp11emu/server/internal/api"
	"pdp11emu/server/internal/panel"
	"pdp11emu/server/internal/panel/paneltest"
	"pdp11emu/server/internal/simh"
)

var (
	simhBinary = flag.String("simh", "../../../simh/out/pdp11", "patched simulator for the e2e test")
	portBase   = flag.Int("port", 0, "console ports for the e2e tests: N and N+1 (0: free ports)")
)

// needFile skips the test when the file named by a flag is missing, or
// fails it when the flag was given explicitly.
func needFile(t *testing.T, flagName, file string) {
	t.Helper()
	_, err := os.Stat(file)
	if err == nil {
		return
	}
	var given *flag.Flag
	flag.Visit(func(f *flag.Flag) {
		if f.Name == flagName {
			given = f
		}
	})
	switch {
	case given == nil:
		t.Skipf("needs -args -%s: %v", flagName, err)
	case !filepath.IsAbs(given.Value.String()):
		t.Fatalf("-%s: %v (go test runs in the package directory: give an absolute path)", flagName, err)
	}
	t.Fatalf("-%s: %v", flagName, err)
}

// toggleIn is a RunConfigProvider for a machine without disks.
type toggleIn struct{}

func (toggleIn) PrepareRun(workDir string, ports simh.Ports, shmPath string, speed api.Speed) (*simh.RunConfig, error) {
	hdr := simh.RunHeader("256K", speed, ports.Console, shmPath)
	ini := filepath.Join(workDir, "run.ini")
	if err := os.WriteFile(ini, []byte(strings.Join(hdr, "\n")+"\n"), 0o644); err != nil {
		return nil, err
	}
	return &simh.RunConfig{SystemID: "toggle", INIPath: ini, HeaderLines: len(hdr), BootCommand: "boot rk0"}, nil
}

// consolePort returns the console port of the n-th e2e test: -port + n,
// or a free port.
func consolePort(t *testing.T, n int) int {
	if *portBase > 0 {
		return *portBase + n
	}
	return freePort(t)
}

func freePort(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestE2EToggleInLightChaser(t *testing.T) {
	bin, err := filepath.Abs(*simhBinary)
	if err != nil {
		t.Fatal(err)
	}
	needFile(t, "simh", bin)
	dir := t.TempDir()
	sys := paneltest.NewSystems(api.SystemInfo{ID: "toggle", Name: "Toggle-in", Default: true, Installed: true,
		License: api.LicenseInfo{Class: "redistributable"}, Speed: api.SpeedAuthentic})
	m, err := simh.New(simh.Options{Binary: bin, WorkDir: filepath.Join(dir, "run"), SHMPath: filepath.Join(dir, "panel"),
		ConsolePort: consolePort(t, 0), Logger: e2eLogger()}, toggleIn{}, sys)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "run"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = m.PowerOff(ctx)
		_ = m.Close()
	})
	p, err := panel.New(m, m.PanelSource(), sys, panel.Options{StatePath: filepath.Join(dir, "panel.json"), FrameHz: 60, Logger: quietLogger()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = p.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	s, err := newServer(Options{Version: "e2e", FrameHz: 60, Static: testUI(), Logger: quietLogger()},
		Deps{Machine: m, Panel: p, Systems: sys, Log: m.Log()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	e := &webEnv{t: t, s: s, p: p, sys: sys}
	e.ts = httptest.NewServer(s.Handler())
	t.Cleanup(func() { s.Close(); e.ts.Close() })

	w := e.dial("sub=panel", nil)
	w.skipInit(0)
	send := func(v map[string]any) { w.send(v) }
	waitState := func(what string, pred func(map[string]any) bool) map[string]any {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			m := w.next()
			if m.msgType() == "state" && pred(m.json()) {
				return m.json()
			}
			if m.msgType() == "toast" || (m.msgType() == "state" && os.Getenv("E2E_VERBOSE") != "") {
				t.Logf("%s: %.300s", m.msgType(), m.data)
			}
		}
		t.Fatalf("timeout waiting for %s", what)
		return nil
	}
	press := func(name string) {
		send(map[string]any{"type": "momentary", "name": name, "active": true})
		send(map[string]any{"type": "momentary", "name": name, "active": false})
		time.Sleep(30 * time.Millisecond) // let the console operation run (it is queued anyway)
	}
	sr := func(v uint32) { send(map[string]any{"type": "sr", "value": v}) }

	// 1. Key OFF, HALT, key POWER: the CPU powers up halted.
	send(map[string]any{"type": "key", "pos": "off"})
	send(map[string]any{"type": "latch", "name": "ENABLE_HALT", "value": "halt"})
	send(map[string]any{"type": "key", "pos": "power"})
	waitState("halted", func(s map[string]any) bool { return s["run"] == "halted" })

	// 2./3. Toggle in the UI.md §6 program with LOAD ADRS and DEP (DEP
	// steps +2). NOP instead of a carry-clearing CLR R1 (SOB leaves R1 at
	// 0 for the next delay), so the light keeps walking through C:
	//	1000 MOV #1,R0; 1004 MOV R0,@#177570; 1010 NOP; 1012 SOB R1,.; 1014 ROL R0; 1016 BR 1004
	prog := []uint32{0o012700, 0o000001, 0o010037, 0o177570, 0o000240, 0o077101, 0o006100, 0o000772}
	sr(0o1000)
	press("LOAD_ADRS")
	for _, word := range prog {
		sr(word)
		press("DEP")
	}
	waitState("address 1016", func(s map[string]any) bool { return get(s, "console", "addr") == float64(0o1016) })

	// Read it back with EXAM: DATA PATHS shows each word (halted display).
	sr(0o1000)
	press("LOAD_ADRS")
	for i, word := range prog {
		press("EXAM")
		f := w.await("#frame", func(m rx) bool { return lampWord(m.data, api.LampD0, 16) == word })
		if a := lampWord(f.data, api.LampA0, 22); a != uint32(0o1000+2*i) {
			t.Fatalf("EXAM %d: ADDRESS lamps %o", i, a)
		}
	}

	// 4./5. LOAD ADRS 1000, ENABLE, START; DATA knob to DISPLAY REGISTER.
	sr(0o1000)
	press("LOAD_ADRS")
	send(map[string]any{"type": "latch", "name": "ENABLE_HALT", "value": "enable"})
	press("START")
	waitState("running", func(s map[string]any) bool { return s["run"] == "running" })
	send(map[string]any{"type": "knob", "name": "data", "pos": 3})
	// The light walks through all 16 bits and C, and starts again at bit 0.
	seen := map[int]bool{}
	wrapped := false // bit 0 seen after bit 15
	deadline := time.Now().Add(15 * time.Second)
	for !(wrapped && len(seen) >= 6) && time.Now().Before(deadline) {
		f := w.await("#frame", nil)
		if os.Getenv("E2E_VERBOSE") != "" {
			t.Logf("frame: A=%08o D=%06o RUN=%d knobDR=%d", lampWord(f.data, api.LampA0, 22), lampWord(f.data, api.LampD0, 16),
				f.data[4+api.LampRun], f.data[4+api.LampDisplayReg])
		}
		if f.data[4+api.LampDisplayReg] != 255 {
			continue
		}
		dr := lampWord(f.data, api.LampD0, 16)
		if n := bits.OnesCount32(dr); n > 1 {
			t.Fatalf("DISPLAY REGISTER shows %06o: more than one light", dr)
		} else if n == 1 {
			b := bits.TrailingZeros32(dr)
			wrapped = wrapped || (b == 0 && seen[15])
			seen[b] = true
		}
		if f.data[4+api.LampRun] < 200 {
			t.Fatalf("RUN lamp %d while running", f.data[4+api.LampRun])
		}
	}
	if len(seen) < 6 || !wrapped {
		t.Fatalf("the light did not keep walking: saw bits %v, wrapped %v", seen, wrapped)
	}

	// HALT: the CPU stops inside the loop; the address shows the PC.
	send(map[string]any{"type": "latch", "name": "ENABLE_HALT", "value": "halt"})
	st := waitState("halted again", func(s map[string]any) bool { return s["run"] == "halted" })
	pc := uint32(get(st, "console", "pc").(float64))
	if pc < 0o1004 || pc > 0o1020 || get(st, "console", "lastStop") != "Front panel HALT" {
		t.Fatalf("stop: pc %o, %v", pc, st["console"])
	}
	t.Logf("halted at %s after walking over bits %v", strconv.FormatUint(uint64(pc), 8), seen)

	// Key OFF.
	send(map[string]any{"type": "key", "pos": "off"})
	waitState("off", func(s map[string]any) bool { return s["run"] == "off" })
}

func e2eLogger() *slog.Logger {
	if os.Getenv("E2E_VERBOSE") != "" {
		return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return quietLogger()
}

// lampWord reads n lamps from base out of a frame message (lit = 255).
func lampWord(msg []byte, base, n int) uint32 {
	var v uint32
	for i := 0; i < n; i++ {
		if msg[4+base+i] == 255 {
			v |= 1 << i
		}
	}
	return v
}

var bsdImage = flag.String("bsd211", "", "pristine 2.11BSD pl482 RA disk image (2.11BSD_rq.dsk) for the e2e boot test")

// bsdConfig boots 2.11BSD from an MSCP disk (as research r3 did).
type bsdConfig struct{ disk string }

func (c bsdConfig) PrepareRun(workDir string, ports simh.Ports, shmPath string, speed api.Speed) (*simh.RunConfig, error) {
	hdr := simh.RunHeader("4M", speed, ports.Console, shmPath)
	body := []string{"set rq enabled", "set rq0 rauser=1000", "attach -e rq0 " + c.disk, "set rl disabled",
		"set rp disabled", "set tq disabled", "set xu disabled", "set dz enabled", "set dz lines=8", "set dz vector=310"}
	ini := filepath.Join(workDir, "run.ini")
	if err := os.WriteFile(ini, []byte(strings.Join(append(hdr, body...), "\n")+"\n"), 0o644); err != nil {
		return nil, err
	}
	return &simh.RunConfig{SystemID: "bsd211", INIPath: ini, HeaderLines: len(hdr), BootCommand: "boot rq0",
		Dialog: []string{`expect -p "Press <CR> to boot" send "\r"; continue`}, ReadyPattern: "login: ", Disks: []string{c.disk}}, nil
}

// sparseCopy copies a disk image, skipping all-zero blocks.
func sparseCopy(t *testing.T, src, dst string) {
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	buf := make([]byte, 64<<10)
	var size int64
	for {
		n, err := io.ReadFull(in, buf)
		if n > 0 {
			zero := true
			for _, b := range buf[:n] {
				if b != 0 {
					zero = false
					break
				}
			}
			if !zero {
				if _, err := out.WriteAt(buf[:n], size); err != nil {
					t.Fatal(err)
				}
			}
			size += int64(n)
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := out.Truncate(size); err != nil {
		t.Fatal(err)
	}
}

// TestE2EBoot211BSD boots 2.11BSD through the panel (autostart), checks
// that the idle pattern in R0 is crisp in DATA PATHS, logs in on the
// console and shuts the OS down with shutdownOS.
func TestE2EBoot211BSD(t *testing.T) {
	bin, _ := filepath.Abs(*simhBinary)
	needFile(t, "simh", bin)
	if *bsdImage == "" {
		t.Skip("needs -args -bsd211 <2.11BSD_rq.dsk>")
	}
	needFile(t, "bsd211", *bsdImage)
	dir := t.TempDir()
	disk := filepath.Join(dir, "2.11BSD_rq.dsk")
	sparseCopy(t, *bsdImage, disk)
	sys := paneltest.NewSystems(api.SystemInfo{ID: "bsd211", Name: "2.11BSD", Default: true, Installed: true,
		License: api.LicenseInfo{Class: "redistributable"}, Speed: api.SpeedFast})
	sys.SetScript("bsd211", []api.ScriptStep{{Send: "sync\r"}, {Expect: "# ", Send: "shutdown -h now\r"}})
	m, err := simh.New(simh.Options{Binary: bin, WorkDir: filepath.Join(dir, "run"), SHMPath: filepath.Join(dir, "panel"),
		ConsolePort: consolePort(t, 1), Logger: e2eLogger()}, bsdConfig{disk: disk}, sys)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Join(dir, "run"), 0o755)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = m.PowerOff(ctx)
		_ = m.Close()
	})
	p, err := panel.New(m, m.PanelSource(), sys, panel.Options{FrameHz: 60, Autostart: true, Logger: quietLogger()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = p.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	s, err := newServer(Options{Version: "e2e", FrameHz: 60, Static: testUI(), Logger: quietLogger()},
		Deps{Machine: m, Panel: p, Systems: sys, Log: m.Log()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	e := &webEnv{t: t, s: s, p: p, sys: sys}
	e.ts = httptest.NewServer(s.Handler())
	t.Cleanup(func() { s.Close(); e.ts.Close() })

	w := e.dial("sub=panel,console", nil)
	var console strings.Builder
	var frames [][]byte
	wait := func(what string, timeout time.Duration, pred func(rx) bool) {
		t.Helper()
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			select {
			case m, ok := <-w.in:
				if !ok {
					t.Fatalf("connection closed waiting for %s", what)
				}
				switch m.msgType() {
				case "#console":
					console.Write(m.data[1:])
				case "#frame":
					frames = append(frames, m.data)
				case "toast":
					t.Logf("toast: %s", m.data)
				}
				if pred(m) {
					return
				}
			case <-time.After(time.Second):
			}
		}
		t.Fatalf("timeout waiting for %s; console:\n%s", what, console.String())
	}
	start := time.Now()
	wait("ready", 3*time.Minute, func(m rx) bool { return m.msgType() == "state" && m.json()["ready"] == true })
	t.Logf("2.11BSD at login: after %v", time.Since(start).Round(100*time.Millisecond))

	// Idle: the pattern in R0 during WAIT dominates DATA PATHS.
	time.Sleep(3 * time.Second)
	frames = nil
	wait("frames", 30*time.Second, func(rx) bool { return len(frames) >= 120 })
	crisp, patterns := 0, map[uint32]bool{}
	for _, f := range frames {
		blurry, bright := 0, uint32(0)
		for b := 0; b < 16; b++ {
			v := f[4+api.LampD0+b]
			switch {
			case v >= 215:
				bright |= 1 << b
			case v > 40:
				blurry++
			}
		}
		if blurry <= 2 && bits.OnesCount32(bright) >= 6 {
			crisp++
			patterns[bright] = true
		}
	}
	t.Logf("%d of %d frames crisp; %d distinct idle patterns", crisp, len(frames), len(patterns))
	if crisp < len(frames)*3/4 || len(patterns) < 4 {
		var dump []string
		for _, f := range frames[:10] {
			dump = append(dump, fmt.Sprint(f[4+api.LampD0:4+api.LampD0+16]))
		}
		t.Fatalf("idle pattern not crisp:\n%s", strings.Join(dump, "\n"))
	}

	// Log in and shut down cleanly.
	w.sendRaw(websocket.MessageBinary, []byte("\x10root\r"))
	wait("shell", 30*time.Second, func(rx) bool { return strings.HasSuffix(console.String(), "# ") })
	w.send(map[string]any{"type": "shutdownOS", "req": "bye"})
	wait("power off", 2*time.Minute, func(m rx) bool {
		if m.msgType() == "progress" {
			t.Logf("progress: %s", m.data)
		}
		return m.msgType() == "state" && m.json()["run"] == "off" && m.json()["power"] == "off"
	})
	if !strings.Contains(console.String(), "halting") {
		t.Fatalf("no clean shutdown on the console:\n%s", console.String())
	}
	t.Logf("clean shutdown after %v", time.Since(start).Round(100*time.Millisecond))
}
