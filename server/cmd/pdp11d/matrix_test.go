//go:build matrix

// Boot matrix through pdp11d itself (opt-in, needs the patched simulator
// and the archives; no network):
//
//	go test -tags matrix -run TestMatrix -timeout 3h -v ./server/cmd/pdp11d/ -args \
//	    -cache DIR[:DIR...] [-work DIR] [-shm DIR] [-systems id,...] [-rounds N] \
//	    [-listen-port 18080] [-port-base 23500] [-clock]
//
// -cache defaults to PDP11_TEST_CACHE (catalog archives named by their
// sha256, as "pdp11d fetch --cache DIR" writes them).
//
// For every catalog system it runs the real serve path in a child process
// (this test binary re-executed as pdp11d) on a fresh, empty data
// directory with PDP11_SYSTEM=<id> and PDP11_ACCEPT_LICENSES=<id> and the
// default autostart. A WebSocket client (sub=panel,console,log) waits for
// state.ready, types the catalog's tests.session on the console like a
// typist, checks the panel frames (RUN lit while running), shuts the OS
// down with shutdownOS (RT-11: key OFF) and requires run off and the key at
// OFF within the script's budget; then SIGTERM ends pdp11d.
//
// 2.11BSD (bsd211) gets two more starts on the same data directory: the
// key turned to POWER boots without fsck (ready < 20 s), a telnet login on
// a DZ line works (PDP11_TTY_TELNET) and SIGTERM with
// PDP11_SHUTDOWN_ON_STOP=1 shuts the OS down cleanly; the next start
// autostarts (the key stayed at POWER), again without fsck.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"

	"pdp11emu/server/internal/catalog"
)

var (
	mxCache   = flag.String("cache", os.Getenv("PDP11_TEST_CACHE"), "read-only archive caches (PDP11_CACHE syntax)")
	mxWork    = flag.String("work", "", "work directory for data directories and logs (default: a temporary directory)")
	mxSHM     = flag.String("shm", "", "directory for the PDP11_SHM files (default: the work directory)")
	mxSystems = flag.String("systems", "", "comma list of system ids (default: the whole catalog)")
	mxRounds  = flag.Int("rounds", 1, "run the matrix this many times")
	mxListen  = flag.Int("listen-port", 18080, "first HTTP port on 127.0.0.1 (one per pdp11d start, 20 in turn)")
	mxPort    = flag.Int("port-base", 23500, "first PDP11_PORT_BASE (10 in turn, steps of 10; +5 is the telnet listener)")
	mxSIMH    = flag.String("simh", "../../../simh/out/pdp11", "patched simulator")
	mxTypist  = flag.Duration("typist", 40*time.Millisecond, "delay between typed characters")
	mxClock   = flag.Bool("clock", false, "Unix systems: after the session, check that the guest clock keeps time while idle (date, 20 s, date)")
)

// childEnv marks the re-executed test binary that runs pdp11d.
const childEnv = "PDP11D_MATRIX_CHILD"

func TestMain(m *testing.M) {
	if os.Getenv(childEnv) == "1" {
		os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

// result is one row of the matrix.
type result struct {
	System    string  `json:"system"`
	Start     string  `json:"start"`
	InstallS  float64 `json:"installS"`  // install (log "installing" → "installed")
	ReadyS    float64 `json:"readyS"`    // process start (or key POWER) → state.ready
	BootS     float64 `json:"bootS"`     // run "running" → state.ready
	Session   string  `json:"session"`   // ok | n/a | failed
	Frames    int     `json:"frames"`    // panel frames in the 2 s after ready
	ShutdownS float64 `json:"shutdownS"` // shutdownOS (or key OFF) → run off, key off
	StopS     float64 `json:"stopS"`     // SIGTERM → exit
	Result    string  `json:"result"`
	Notes     string  `json:"notes,omitempty"`
}

var startNo int // pdp11d starts so far: picks ports and files

func TestMatrix(t *testing.T) {
	if *mxCache == "" {
		t.Skip("needs -cache DIR or PDP11_TEST_CACHE (archives named by their sha256)")
	}
	simhBin, _ := filepath.Abs(*mxSIMH)
	if _, err := os.Stat(simhBin); err != nil {
		t.Skipf("patched simulator: %v", err)
	}
	cat, err := loadCatalog("") // the embedded catalog, as the child uses
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	if *mxSystems != "" {
		ids = strings.Split(*mxSystems, ",")
	} else {
		for _, s := range cat.Systems {
			ids = append(ids, s.ID)
		}
	}
	work := *mxWork
	if work == "" {
		work = t.TempDir()
	}
	shmDir := *mxSHM
	if shmDir == "" {
		shmDir = work
	}
	for _, d := range []string{work, shmDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mx := &matrix{simh: simhBin, work: work, shm: shmDir}
	var results []result
	for round := 1; round <= *mxRounds; round++ {
		for _, id := range ids {
			sys, ok := cat.Get(id)
			if !ok {
				t.Fatalf("unknown system %q", id)
			}
			t.Run(fmt.Sprintf("%s#%d", id, round), func(t *testing.T) {
				results = append(results, mx.system(t, sys, round)...)
			})
		}
	}
	table := matrixTable(results)
	fmt.Print(table)
	t.Log("\n" + table)
	raw, _ := json.MarshalIndent(results, "", " ")
	_ = os.WriteFile(filepath.Join(work, "matrix.json"), raw, 0o644)
}

type matrix struct {
	simh, work, shm string
}

// startCfg describes one pdp11d start.
type startCfg struct {
	name    string
	env     map[string]string // extra settings
	keyOn   bool              // power on with the key (the persisted key is OFF)
	session bool              // type tests.session
	tty     bool              // telnet login on the first terminal line
	noFsck  bool              // 2.11BSD: the boot must find clean file systems
	stop    string            // "shutdownOS", "keyOff" or "sigterm" (PDP11_SHUTDOWN_ON_STOP)
}

func (mx *matrix) system(t *testing.T, sys *catalog.System, round int) []result {
	dir := filepath.Join(mx.work, fmt.Sprintf("%s-%d", sys.ID, round))
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	stop := "shutdownOS"
	if len(sys.ShutdownScript) == 0 {
		stop = "keyOff"
	}
	var out []result
	out = append(out, mx.start(t, sys, dir, startCfg{name: "fresh", session: true, stop: stop}))
	if sys.ID == "bsd211" && out[0].Result == "PASS" {
		out = append(out, mx.start(t, sys, dir, startCfg{name: "key on, tty, SIGTERM", keyOn: true, tty: true, noFsck: true, stop: "sigterm",
			env: map[string]string{"PDP11_TTY_TELNET": "TTY", "PDP11_SHUTDOWN_ON_STOP": "1"}}))
		out = append(out, mx.start(t, sys, dir, startCfg{name: "autostart", noFsck: true, stop: "shutdownOS"}))
	}
	return out
}

// start runs pdp11d once and returns its matrix row.
func (mx *matrix) start(t *testing.T, sys *catalog.System, dir string, cfg startCfg) (r result) {
	n := startNo
	startNo++
	r = result{System: sys.ID, Start: cfg.name, Session: "n/a"}
	listen := fmt.Sprintf("127.0.0.1:%d", *mxListen+n%20)
	base := *mxPort + 10*(n%10)
	shm := filepath.Join(mx.shm, fmt.Sprintf("panel-%s-%d", sys.ID, n))
	defer os.Remove(shm)
	env := map[string]string{
		"PATH": "/usr/bin:/bin", "HOME": dir, childEnv: "1",
		"PDP11_DATA": filepath.Join(dir, "data"), "PDP11_CACHE": *mxCache, "PDP11_SIMH": mx.simh,
		"PDP11_SYSTEM": sys.ID, "PDP11_ACCEPT_LICENSES": sys.ID, "PDP11_LISTEN": listen,
		"PDP11_PORT_BASE": strconv.Itoa(base), "PDP11_SHM": shm, "PDP11_LOG": "debug",
	}
	for k, v := range cfg.env {
		if v == "TTY" {
			v = fmt.Sprintf("127.0.0.1:%d", base+5)
		}
		env[k] = v
	}
	logPath := filepath.Join(dir, fmt.Sprintf("pdp11d-%d.log", n))
	conPath := filepath.Join(dir, fmt.Sprintf("console-%d.txt", n))
	evPath := filepath.Join(dir, fmt.Sprintf("events-%d.txt", n))
	fail := func(format string, a ...any) result {
		r.Result = "FAIL: " + fmt.Sprintf(format, a...)
		t.Errorf("%s (%s): %s\n  pdp11d log: %s\n  console: %s", sys.ID, cfg.name, r.Result, logPath, conPath)
		return r
	}

	d, err := startDaemon(env, logPath)
	if err != nil {
		return fail("start: %v", err)
	}
	defer d.kill()
	v, err := dialViewer(listen, 15*time.Second)
	if err != nil {
		return fail("WebSocket: %v", err)
	}
	defer func() {
		v.close()
		_ = os.WriteFile(conPath, v.consoleCopy(), 0o644)
		v.mu.Lock()
		_ = os.WriteFile(evPath, v.events.Bytes(), 0o644)
		v.mu.Unlock()
	}()

	// Power on (autostart, or the key), then wait for the ready prompt.
	t0 := d.start
	limit := time.Duration(sys.BootTimeoutSec+60) * time.Second
	if cfg.keyOn {
		if _, ok := v.waitState(10*time.Second, func(s stateMsg) bool { return s.Power == "off" && s.Run == "off" }); !ok {
			return fail("not off at start (key OFF persisted): %s", v.lastState())
		}
		t0 = time.Now()
		v.send(map[string]any{"type": "key", "pos": "power", "req": "matrix"})
	}
	st, ok := v.waitState(limit, func(s stateMsg) bool { return s.Ready || s.Run == "error" })
	if !ok || !st.Ready {
		return fail("not ready within %s: %s", limit, v.lastState())
	}
	r.ReadyS = secs(time.Since(t0))
	if run := v.firstRunning(); !run.IsZero() {
		r.BootS = secs(st.at.Sub(run))
	}
	r.InstallS = installSeconds(logPath)
	if w := v.toastsWith("has not reached its prompt"); len(w) > 0 {
		r.Notes += "boot-timeout toast; "
	}
	readyPat := []byte(sys.ReadyPattern)
	mark := v.find(0, readyPat)
	if mark < 0 {
		return fail("state.ready without %q on the console", sys.ReadyPattern)
	}
	mark += len(readyPat)

	// Panel frames while running: RUN lit.
	f0, _ := v.frameStats()
	time.Sleep(2 * time.Second)
	f1, runMin := v.frameStats()
	r.Frames = f1 - f0
	if r.Frames < 2 {
		return fail("%d panel frames in 2 s", r.Frames)
	}
	if runMin < 200 {
		return fail("RUN lamp %d while running", runMin)
	}

	if cfg.noFsck {
		con := v.consoleCopy()
		if !bytes.Contains(con, []byte("file system is clean; not checking")) || bytes.Contains(con, []byte("** Phase")) ||
			bytes.Contains(con, []byte("SALVAGED")) || bytes.Contains(con, []byte("FILE SYSTEM WAS MODIFIED")) {
			return fail("the boot checked the file systems (fsck)")
		}
		if r.BootS >= 20 {
			return fail("boot took %.1f s (no fsck: < 20 s)", r.BootS)
		}
	}

	if cfg.session && sys.Tests != nil {
		r.Session = "failed"
		if err := v.steps(mark, sys.Tests.Session); err != nil {
			return fail("tests.session: %v", err)
		}
		r.Session = "ok"
	}

	if *mxClock && cfg.session && sys.Category == "unix" {
		guest, wall, err := v.clockRate()
		if err != nil {
			return fail("clock probe: %v", err)
		}
		r.Notes += fmt.Sprintf("guest clock +%d s in %.1f s; ", guest, wall)
		if float64(guest) < wall/2 || float64(guest) > wall*2 {
			return fail("the guest clock advanced %d s in %.1f s of wall time", guest, wall)
		}
	}

	if cfg.tty {
		if err := bsdTTYLogin(fmt.Sprintf("127.0.0.1:%d", base+5)); err != nil {
			return fail("telnet line: %v", err)
		}
		r.Notes += "telnet login + hang-up ok; "
	}

	if err := runHealth(listen); err != nil {
		return fail("pdp11d health: %v", err)
	}

	switch cfg.stop {
	case "shutdownOS", "keyOff":
		budget := shutdownBudget(sys)
		s0 := time.Now()
		if cfg.stop == "shutdownOS" {
			v.send(map[string]any{"type": "shutdownOS", "req": "matrix"})
		} else {
			v.send(map[string]any{"type": "key", "pos": "off", "req": "matrix"})
		}
		if _, ok := v.waitState(budget, func(s stateMsg) bool { return s.Run == "off" && s.Power == "off" && s.Key == "off" }); !ok {
			return fail("%s: not off with the key at OFF within %s: %s; toasts %q", cfg.stop, budget, v.lastState(), v.toastsWith(""))
		}
		r.ShutdownS = secs(time.Since(s0))
		if cfg.stop == "shutdownOS" && !v.progressDone() {
			return fail("shutdownOS: no progress \"done\"")
		}
		code, took := d.stop(30 * time.Second)
		r.StopS = secs(took)
		if code != 0 {
			return fail("pdp11d exit status %d after SIGTERM", code)
		}
	case "sigterm":
		code, took := d.stop(90 * time.Second)
		r.StopS = secs(took)
		r.ShutdownS = r.StopS
		logs, _ := os.ReadFile(logPath)
		if code != 0 {
			return fail("pdp11d exit status %d after SIGTERM", code)
		}
		// The web server stops first, so the console of this shutdown is
		// only in the log.
		if !bytes.Contains(logs, []byte("shutting the guest OS down")) || bytes.Contains(logs, []byte("shutdown incomplete")) ||
			!bytes.Contains(logs, []byte("powered off")) {
			return fail("SIGTERM with PDP11_SHUTDOWN_ON_STOP: no clean shutdown in the log")
		}
		r.Notes += "clean shutdown on SIGTERM; "
	}
	if _, err := os.Stat(filepath.Join(dir, "data", "run", "pdp11.pid")); !os.IsNotExist(err) {
		return fail("pid file left behind (simulator still running?)")
	}
	r.Result = "PASS"
	return r
}

// shutdownBudget is the longest the shutdown script may take (PANEL_LOGIC
// §3): every step's timeout, the answer delays, the wait for the halt (or
// the settle time), the power-off, and some slack.
func shutdownBudget(sys *catalog.System) time.Duration {
	d := 15 * time.Second
	for _, s := range sys.ShutdownScript {
		if s.Expect != "" {
			to := s.TimeoutSec
			if to <= 0 {
				to = 30
			}
			d += time.Duration(to)*time.Second + 250*time.Millisecond
		}
	}
	if n := len(sys.ShutdownScript); n > 0 && sys.ShutdownScript[n-1].Send != "" {
		d += 30 * time.Second
	} else {
		d += 3 * time.Second
	}
	return d + 30*time.Second // power off
}

func secs(d time.Duration) float64 {
	return float64(d.Round(100*time.Millisecond)) / float64(time.Second)
}

var reLogTime = regexp.MustCompile(`^time=(\S+) `)

// installSeconds reads the install duration from the pdp11d log.
func installSeconds(logPath string) float64 {
	raw, err := os.ReadFile(logPath)
	if err != nil {
		return 0
	}
	var from, to time.Time
	for _, line := range strings.Split(string(raw), "\n") {
		m := reLogTime.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, m[1])
		if err != nil {
			continue
		}
		switch {
		case strings.Contains(line, "msg=installing ") && from.IsZero():
			from = at
		case strings.Contains(line, "msg=installed "):
			to = at
		}
	}
	if from.IsZero() || to.IsZero() {
		return 0
	}
	return secs(to.Sub(from))
}

func runHealth(listen string) error {
	var out, errb bytes.Buffer
	if code := run([]string{"health"}, envMap(map[string]string{"PDP11_LISTEN": listen}), &out, &errb); code != 0 {
		return fmt.Errorf("exit %d: %s", code, strings.TrimSpace(errb.String()))
	}
	if !strings.Contains(out.String(), "run=running") {
		return fmt.Errorf("unexpected status %q", out.String())
	}
	return nil
}

func matrixTable(rs []result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n%-13s %-22s %9s %9s %10s %8s %6s %11s %7s  %s\n", "system", "start", "install s", "ready s", "boot>rdy s", "session", "frames", "shutdown s", "stop s", "result")
	pass := 0
	for _, r := range rs {
		if r.Result == "PASS" {
			pass++
		}
		res := r.Result
		if r.Notes != "" {
			res += " (" + strings.TrimSuffix(r.Notes, "; ") + ")"
		}
		fmt.Fprintf(&b, "%-13s %-22s %9.1f %9.1f %10.1f %8s %6d %11.1f %7.1f  %s\n",
			r.System, r.Start, r.InstallS, r.ReadyS, r.BootS, r.Session, r.Frames, r.ShutdownS, r.StopS, res)
	}
	fmt.Fprintf(&b, "%d of %d starts passed\n", pass, len(rs))
	return b.String()
}

// ---- pdp11d child process ---------------------------------------------------

type daemon struct {
	cmd   *exec.Cmd
	start time.Time
	done  chan struct{}
	code  int
}

func startDaemon(env map[string]string, logPath string) (*daemon, error) {
	logf, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(os.Args[0], "serve")
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	d := &daemon{cmd: cmd, start: time.Now(), done: make(chan struct{})}
	if err := cmd.Start(); err != nil {
		logf.Close()
		return nil, err
	}
	go func() {
		err := cmd.Wait()
		logf.Close()
		d.code = 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			d.code = ee.ExitCode()
		} else if err != nil {
			d.code = -1
		}
		close(d.done)
	}()
	return d, nil
}

// stop sends SIGTERM and waits for the exit.
func (d *daemon) stop(timeout time.Duration) (code int, took time.Duration) {
	start := time.Now()
	_ = d.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-d.done:
		return d.code, time.Since(start)
	case <-time.After(timeout):
		d.kill()
		return -1, time.Since(start)
	}
}

// kill ends the child's process group (the simulator would follow by
// Pdeathsig anyway).
func (d *daemon) kill() {
	select {
	case <-d.done:
		return
	default:
	}
	_ = syscall.Kill(-d.cmd.Process.Pid, syscall.SIGKILL)
	<-d.done
}

// ---- WebSocket viewer -----------------------------------------------------

type stateMsg struct {
	Run      string `json:"run"`
	Power    string `json:"power"`
	Ready    bool   `json:"ready"`
	Error    string `json:"error"`
	Controls struct {
		Key string `json:"key"`
	} `json:"controls"`
	Key string `json:"-"`
	at  time.Time
}

type viewer struct {
	c      *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	console  []byte
	states   []stateMsg
	frames   int
	runMin   int // lowest RUN lamp in frames received while running (since the last frameStats)
	toasts   []string
	progress []map[string]any
	readErr  error
	events   bytes.Buffer // timestamped messages except frames (for diagnosis)
	t0       time.Time
}

func dialViewer(listen string, timeout time.Duration) (*viewer, error) {
	deadline := time.Now().Add(timeout)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		c, _, err := websocket.Dial(ctx, "ws://"+listen+"/ws?sub=panel,console,log", nil)
		cancel()
		if err == nil {
			c.SetReadLimit(-1)
			v := &viewer{c: c, runMin: 255, t0: time.Now()}
			v.ctx, v.cancel = context.WithCancel(context.Background())
			go v.readLoop()
			return v, nil
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (v *viewer) close() {
	v.cancel()
	_ = v.c.CloseNow()
}

func (v *viewer) readLoop() {
	for {
		typ, data, err := v.c.Read(v.ctx)
		if err != nil {
			v.mu.Lock()
			v.readErr = err
			v.mu.Unlock()
			return
		}
		now := time.Now()
		v.mu.Lock()
		if typ == websocket.MessageText {
			fmt.Fprintf(&v.events, "%8.3f %s\n", now.Sub(v.t0).Seconds(), data)
		} else if len(data) > 0 && data[0] == 0x10 {
			fmt.Fprintf(&v.events, "%8.3f console %q\n", now.Sub(v.t0).Seconds(), data[1:])
		}
		switch {
		case typ == websocket.MessageBinary && len(data) == 68 && data[0] == 0x01:
			v.frames++
			if n := len(v.states); n > 0 && v.states[n-1].Run == "running" && int(data[4+42]) < v.runMin {
				v.runMin = int(data[4+42])
			}
		case typ == websocket.MessageBinary && len(data) > 0 && data[0] == 0x10:
			v.console = append(v.console, data[1:]...)
		case typ == websocket.MessageText:
			var m struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			_ = json.Unmarshal(data, &m)
			switch m.Type {
			case "state":
				var s stateMsg
				if json.Unmarshal(data, &s) == nil {
					s.Key, s.at = s.Controls.Key, now
					v.states = append(v.states, s)
				}
			case "toast":
				v.toasts = append(v.toasts, m.Text)
			case "progress":
				var p map[string]any
				_ = json.Unmarshal(data, &p)
				v.progress = append(v.progress, p)
			}
		}
		v.mu.Unlock()
	}
}

func (v *viewer) send(msg map[string]any) {
	raw, _ := json.Marshal(msg)
	v.mu.Lock()
	fmt.Fprintf(&v.events, "%8.3f sent %s\n", time.Since(v.t0).Seconds(), raw)
	v.mu.Unlock()
	ctx, cancel := context.WithTimeout(v.ctx, 5*time.Second)
	defer cancel()
	_ = v.c.Write(ctx, websocket.MessageText, raw)
}

// typeText types s on the console, one character per message.
func (v *viewer) typeText(s string) error {
	v.mu.Lock()
	fmt.Fprintf(&v.events, "%8.3f typing %q\n", time.Since(v.t0).Seconds(), s)
	v.mu.Unlock()
	for i := 0; i < len(s); i++ {
		ctx, cancel := context.WithTimeout(v.ctx, 5*time.Second)
		err := v.c.Write(ctx, websocket.MessageBinary, []byte{0x10, s[i]})
		cancel()
		if err != nil {
			return err
		}
		time.Sleep(*mxTypist)
	}
	return nil
}

func (v *viewer) waitState(timeout time.Duration, pred func(stateMsg) bool) (stateMsg, bool) {
	deadline := time.Now().Add(timeout)
	for {
		v.mu.Lock()
		var st stateMsg
		ok := len(v.states) > 0 && pred(v.states[len(v.states)-1])
		if len(v.states) > 0 {
			st = v.states[len(v.states)-1]
		}
		closed := v.readErr != nil
		v.mu.Unlock()
		if ok {
			return st, true
		}
		if closed || time.Now().After(deadline) {
			return st, false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (v *viewer) lastState() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.states) == 0 {
		return fmt.Sprintf("no state (connection: %v)", v.readErr)
	}
	s := v.states[len(v.states)-1]
	return fmt.Sprintf("power %s run %s ready %v key %s error %q", s.Power, s.Run, s.Ready, s.Key, s.Error)
}

// firstRunning is when the run state last turned to running (the boot).
func (v *viewer) firstRunning() time.Time {
	v.mu.Lock()
	defer v.mu.Unlock()
	var at time.Time
	prev := ""
	for _, s := range v.states {
		if s.Run == "running" && prev != "running" {
			at = s.at
		}
		prev = s.Run
	}
	return at
}

func (v *viewer) frameStats() (frames, runMin int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	frames, runMin = v.frames, v.runMin
	v.runMin = 255
	return frames, runMin
}

func (v *viewer) toastsWith(s string) []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	var out []string
	for _, t := range v.toasts {
		if strings.Contains(t, s) {
			out = append(out, t)
		}
	}
	return out
}

func (v *viewer) progressDone() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, p := range v.progress {
		if p["task"] == "shutdown" && p["phase"] == "done" {
			return true
		}
	}
	return false
}

func (v *viewer) consoleCopy() []byte {
	v.mu.Lock()
	defer v.mu.Unlock()
	return bytes.Clone(v.console)
}

// find returns the position of needle in the console output at or after
// from, or -1.
func (v *viewer) find(from int, needle []byte) int {
	v.mu.Lock()
	defer v.mu.Unlock()
	if from > len(v.console) {
		return -1
	}
	i := bytes.Index(v.console[from:], needle)
	if i < 0 {
		return -1
	}
	return from + i
}

func (v *viewer) size() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.console)
}

// steps runs expect/send steps like the shutdown runner: an Expect is a
// literal substring of the output after the previous Send (or after mark).
func (v *viewer) steps(mark int, steps []catalog.Step) error {
	for i, s := range steps {
		if s.Expect != "" {
			to := time.Duration(s.TimeoutSec) * time.Second
			if to <= 0 {
				to = 30 * time.Second
			}
			deadline := time.Now().Add(to)
			for {
				if j := v.find(mark, []byte(s.Expect)); j >= 0 {
					mark = j + len(s.Expect)
					break
				}
				if time.Now().After(deadline) {
					con := v.consoleCopy()
					return fmt.Errorf("step %d: %q not seen within %s; console tail %q", i+1, s.Expect, to, con[max(0, len(con)-300):])
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
		if s.Send != "" {
			time.Sleep(250 * time.Millisecond) // a typist reads the prompt first
			mark = v.size()
			if err := v.typeText(s.Send); err != nil {
				return fmt.Errorf("step %d: typing: %w", i+1, err)
			}
		}
	}
	return nil
}

var reClock = regexp.MustCompile(`\b(\d\d):(\d\d):(\d\d)\b`)

// clockRate types "date" twice, 20 s apart, at a Unix shell prompt and
// returns how far the guest clock advanced and the wall time in between.
func (v *viewer) clockRate() (guest int, wall float64, err error) {
	var secs [2]int
	var at [2]time.Time
	for i := range secs {
		if i > 0 {
			time.Sleep(20 * time.Second)
		}
		from := v.size()
		at[i] = time.Now()
		if err := v.typeText("date\r"); err != nil {
			return 0, 0, err
		}
		deadline := time.Now().Add(60 * time.Second)
		for {
			if j := v.find(from+5, []byte("# ")); j >= 0 {
				m := reClock.FindSubmatch(v.consoleCopy()[from:j])
				if m == nil {
					return 0, 0, fmt.Errorf("no time in the output of date")
				}
				h, _ := strconv.Atoi(string(m[1]))
				mi, _ := strconv.Atoi(string(m[2]))
				se, _ := strconv.Atoi(string(m[3]))
				secs[i] = h*3600 + mi*60 + se
				break
			}
			if time.Now().After(deadline) {
				return 0, 0, fmt.Errorf("date: no prompt within 60 s")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	return (secs[1] - secs[0] + 86400) % 86400, at[1].Sub(at[0]).Seconds(), nil
}

// ---- telnet line (2.11BSD DZ) ---------------------------------------------

type telnetLine struct {
	c    net.Conn
	mu   sync.Mutex
	buf  []byte
	err  error
	mark int
}

func dialTelnet(addr string, timeout time.Duration) (*telnetLine, error) {
	deadline := time.Now().Add(timeout)
	for {
		c, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			l := &telnetLine{c: c}
			go l.readLoop()
			return l, nil
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// readLoop strips telnet commands (SimH's listener negotiates on connect)
// and answers them: BINARY, ECHO and SUPPRESS-GO-AHEAD are accepted.
func (l *telnetLine) readLoop() {
	const iac, will, wont, do, dont, sb, se = 255, 251, 252, 253, 254, 250, 240
	ok := map[byte]bool{0: true, 1: true, 3: true}
	buf := make([]byte, 4096)
	var pend []byte
	for {
		n, err := l.c.Read(buf)
		if err != nil {
			l.mu.Lock()
			l.err = err
			l.mu.Unlock()
			return
		}
		d := append(pend, buf[:n]...)
		pend = nil
		var out []byte
		for i := 0; i < len(d); i++ {
			if d[i] != iac {
				out = append(out, d[i]&0x7f)
				continue
			}
			if i+1 >= len(d) {
				pend = append(pend, d[i:]...)
				break
			}
			switch cmd := d[i+1]; cmd {
			case iac:
				out = append(out, 0x7f)
				i++
			case will, wont, do, dont:
				if i+2 >= len(d) {
					pend = append(pend, d[i:]...)
					i = len(d)
					break
				}
				opt := d[i+2]
				switch cmd {
				case will:
					reply := byte(dont)
					if ok[opt] {
						reply = do
					}
					_, _ = l.c.Write([]byte{iac, reply, opt})
				case do:
					reply := byte(wont)
					if ok[opt] {
						reply = will
					}
					_, _ = l.c.Write([]byte{iac, reply, opt})
				}
				i += 2
			case sb:
				j := bytes.Index(d[i:], []byte{iac, se})
				if j < 0 {
					pend = append(pend, d[i:]...)
					i = len(d)
					break
				}
				i += j + 1
			default:
				i++
			}
		}
		l.mu.Lock()
		l.buf = append(l.buf, out...)
		l.mu.Unlock()
	}
}

func (l *telnetLine) expect(needle string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		l.mu.Lock()
		i := bytes.Index(l.buf[l.mark:], []byte(needle))
		if i >= 0 {
			l.mark += i + len(needle)
		}
		tail, err := l.buf[max(0, len(l.buf)-200):], l.err
		l.mu.Unlock()
		if i >= 0 {
			return nil
		}
		if err != nil && !errors.Is(err, io.EOF) || time.Now().After(deadline) {
			return fmt.Errorf("%q not seen (connection: %v); tail %q", needle, err, tail)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (l *telnetLine) send(s string) error {
	l.mu.Lock()
	l.mark = len(l.buf)
	l.mu.Unlock()
	for i := 0; i < len(s); i++ {
		if _, err := l.c.Write([]byte{s[i]}); err != nil {
			return err
		}
		time.Sleep(*mxTypist)
	}
	return nil
}

// bsdTTYLogin logs in on the first DZ line, checks the terminal, logs out
// with ^D, logs in again and hangs up: with modem control the shell is
// killed and a fresh login prompt greets the next connection.
func bsdTTYLogin(addr string) error {
	l, err := dialTelnet(addr, 10*time.Second)
	if err != nil {
		return err
	}
	steps := []struct{ expect, send string }{
		{"login: ", "root\r"}, {"# ", "tty\r"}, {"/dev/tty00", ""}, {"# ", "\004"},
		{"login: ", "root\r"}, {"# ", ""},
	}
	for i, s := range steps {
		if s.expect != "" {
			if err := l.expect(s.expect, 30*time.Second); err != nil {
				l.c.Close()
				return fmt.Errorf("step %d: %w", i+1, err)
			}
		}
		if s.send != "" {
			time.Sleep(250 * time.Millisecond)
			if err := l.send(s.send); err != nil {
				l.c.Close()
				return err
			}
		}
	}
	l.c.Close() // hang up while logged in
	time.Sleep(2 * time.Second)
	l, err = dialTelnet(addr, 10*time.Second)
	if err != nil {
		return err
	}
	defer l.c.Close()
	if err := l.expect("login: ", 30*time.Second); err != nil {
		return fmt.Errorf("after the hang-up: %w", err)
	}
	return nil
}
