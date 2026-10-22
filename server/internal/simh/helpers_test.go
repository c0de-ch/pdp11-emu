package simh

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"pdp11emu/server/internal/api"
)

// testSystems is a minimal api.Systems for the machine tests.
type testSystems struct {
	mu       sync.Mutex
	selected string
	infos    map[string]api.SystemInfo
	speeds   map[string]api.Speed
	installs int
	onInst   func(id string) error
	subs     []func(api.Progress)
}

func newTestSystems() *testSystems {
	return &testSystems{
		selected: "fake",
		infos: map[string]api.SystemInfo{"fake": {
			ID: "fake", Name: "Fake Unix", Installed: true,
			License: api.LicenseInfo{Name: "Caldera", Class: "redistributable"},
		}},
		speeds: map[string]api.Speed{},
	}
}

func (s *testSystems) List() []api.SystemInfo { return nil }
func (s *testSystems) Get(id string) (api.SystemInfo, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, ok := s.infos[id]
	return i, ok
}
func (s *testSystems) Selected() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selected
}
func (s *testSystems) Select(id string) error { s.selected = id; return nil }
func (s *testSystems) Install(ctx context.Context, id string, accept bool, req string) error {
	s.mu.Lock()
	s.installs++
	fn := s.onInst
	s.mu.Unlock()
	if fn != nil {
		if err := fn(id); err != nil {
			return err
		}
	}
	s.mu.Lock()
	i := s.infos[id]
	i.Installed = true
	s.infos[id] = i
	subs := s.subs
	s.mu.Unlock()
	for _, fn := range subs {
		fn(api.Progress{Task: "install", ID: id, Phase: "done", Pct: 100})
	}
	return nil
}
func (s *testSystems) FactoryReset(ctx context.Context, id, req string) error { return nil }
func (s *testSystems) Cancel(id string)                                       {}
func (s *testSystems) SubscribeProgress(fn func(api.Progress)) func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subs = append(s.subs, fn)
	return func() {}
}
func (s *testSystems) SubscribeChanges(fn func()) func() { return func() {} }
func (s *testSystems) SpeedFor(id string) api.Speed {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sp, ok := s.speeds[id]; ok {
		return sp
	}
	return api.SpeedFast
}
func (s *testSystems) SetSpeedFor(id string, sp api.Speed) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.speeds[id] = sp
	return nil
}
func (s *testSystems) ShutdownScript(id string) []api.ScriptStep { return nil }

// testProvider writes run.ini like internal/systems does, with a boot.ini
// supplied by the test (${DISK0} is a scratch file).
type testProvider struct {
	mu       sync.Mutex
	dir      string
	bootINI  []string
	dialog   []string
	boot     string
	ready    string
	memory   string
	modem    bool
	calls    int
	released int // RunConfig.Release calls
	speeds   []api.Speed
}

func (p *testProvider) PrepareRun(workDir string, ports Ports, shmPath string, speed api.Speed) (*RunConfig, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.speeds = append(p.speeds, speed)
	disk := filepath.Join(p.dir, "disk0.dsk")
	if _, err := os.Stat(disk); err != nil {
		if err := os.WriteFile(disk, make([]byte, 4096), 0o644); err != nil {
			return nil, err
		}
	}
	header := RunHeader(p.memory, speed, ports.Console, shmPath)
	lines := append([]string{}, header...)
	for _, l := range p.bootINI {
		lines = append(lines, strings.ReplaceAll(l, "${DISK0}", disk))
	}
	if ports.TTYTelnet != "" {
		lines = append(lines, TerminalsAttach("dz", p.modem, ports.TTYTelnet))
	}
	ini := filepath.Join(workDir, "run.ini")
	if err := os.WriteFile(ini, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return nil, err
	}
	return &RunConfig{
		SystemID: "fake", INIPath: ini, HeaderLines: len(header), BootCommand: p.boot,
		Dialog: p.dialog, ReadyPattern: p.ready, Disks: []string{disk},
		Release: func() {
			p.mu.Lock()
			p.released++
			p.mu.Unlock()
		},
	}, nil
}

// leases returns PrepareRun calls minus RunConfig.Release calls.
func (p *testProvider) leases() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls - p.released
}

// harness runs one Machine against the fake simulator.
type harness struct {
	t      *testing.T
	m      *Machine
	sys    *testSystems
	prov   *testProvider
	dir    string
	events chan api.MachineEvent
}

type harnessOpt func(*Options, *testProvider)

func withDirectives(d ...string) harnessOpt {
	return func(_ *Options, p *testProvider) {
		for _, x := range d {
			p.bootINI = append(p.bootINI, "; fake: "+x)
		}
	}
}

func withBootINI(lines ...string) harnessOpt {
	return func(_ *Options, p *testProvider) { p.bootINI = lines }
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func testLogger(t *testing.T) *slog.Logger {
	if testing.Verbose() {
		return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newHarness(t *testing.T, opts ...harnessOpt) *harness {
	t.Helper()
	dir := t.TempDir()
	prov := &testProvider{
		dir:     dir,
		bootINI: []string{"set rk enabled", "attach -e rk0 ${DISK0}"},
		dialog:  []string{`expect "@" send -t after=2000,"rkunix\r"; continue`},
		boot:    "boot rk0",
		ready:   "login: ",
		memory:  "1M",
	}
	o := Options{
		Binary:      os.Args[0],
		WorkDir:     filepath.Join(dir, "run"),
		SHMPath:     filepath.Join(dir, "panel"),
		ConsolePort: freePort(t),
		InputCPS:    2000,
		Logger:      testLogger(t),
	}
	for _, fn := range opts {
		fn(&o, prov)
	}
	sys := newTestSystems()
	m, err := New(o, prov, sys)
	if err != nil {
		t.Fatal(err)
	}
	m.to.halt = time.Second
	m.to.startup = 5 * time.Second
	m.to.command = 5 * time.Second
	h := &harness{t: t, m: m, sys: sys, prov: prov, dir: dir, events: make(chan api.MachineEvent, 1024)}
	m.Subscribe(func(ev api.MachineEvent) { h.events <- ev })
	t.Cleanup(func() {
		// Close delivers the queued events first. A test that never reads
		// h.events can fill it (TestConcurrentOperations makes 500-1000+
		// events in its 2 s without -race), and the blocked subscriber
		// would then hang Close: drain the channel meanwhile.
		drained := make(chan struct{})
		go func() {
			for {
				select {
				case <-h.events:
				case <-drained:
					return
				}
			}
		}()
		err := m.Close()
		close(drained)
		if err != nil {
			t.Errorf("Close: %v", err)
		}
		if _, err := os.Stat(filepath.Join(o.WorkDir, "SIGINT-RECEIVED")); err == nil {
			t.Error("the simulator received SIGINT")
		}
	})
	return h
}

func (h *harness) ctx() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	h.t.Cleanup(cancel)
	return ctx
}

func describe(ev api.MachineEvent) string {
	switch ev.Kind {
	case api.EvState:
		return "state:" + string(ev.State)
	case api.EvStop:
		return "stop:" + ev.Stop.Reason
	case api.EvError:
		return "error"
	case api.EvReady:
		return "ready"
	}
	return fmt.Sprintf("kind%d", ev.Kind)
}

// expect waits for the given event sequence (exactly, in order).
func (h *harness) expect(want ...string) []api.MachineEvent {
	h.t.Helper()
	var got []api.MachineEvent
	for _, w := range want {
		select {
		case ev := <-h.events:
			got = append(got, ev)
			if d := describe(ev); d != w {
				h.t.Fatalf("event %d: got %s, want %s (sequence so far %v)", len(got), d, w, descs(got))
			}
		case <-time.After(10 * time.Second):
			h.t.Fatalf("timeout waiting for %s (got %v)", w, descs(got))
		}
	}
	return got
}

// quiet asserts that no event arrives within d.
func (h *harness) quiet(d time.Duration) {
	h.t.Helper()
	select {
	case ev := <-h.events:
		h.t.Fatalf("unexpected event %s (%+v)", describe(ev), ev)
	case <-time.After(d):
	}
}

func (h *harness) drain() {
	for {
		select {
		case <-h.events:
		default:
			return
		}
	}
}

func descs(evs []api.MachineEvent) []string {
	var out []string
	for _, e := range evs {
		out = append(out, describe(e))
	}
	return out
}

// consoleWatch collects console output from now on.
type consoleWatch struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	cancel func()
}

func (h *harness) watch() *consoleWatch {
	w := &consoleWatch{}
	_, w.cancel = h.m.Console().SubscribeHistory(func(p []byte) {
		w.mu.Lock()
		w.buf.Write(p)
		w.mu.Unlock()
	})
	h.t.Cleanup(w.cancel)
	return w
}

func (w *consoleWatch) waitFor(t *testing.T, s string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		w.mu.Lock()
		ok := strings.Contains(w.buf.String(), s)
		w.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	t.Fatalf("console: %q not seen; got %q", s, w.buf.String())
}

// powerOnBooted powers on with boot and waits until the fake Unix is ready.
func (h *harness) powerOnBooted() {
	h.t.Helper()
	if err := h.m.PowerOn(h.ctx(), api.PowerOnOptions{Boot: true}); err != nil {
		h.t.Fatalf("PowerOn: %v (LastError %q)", err, h.m.LastError())
	}
	h.expect("state:starting", "state:halted", "state:running", "ready")
}

func (h *harness) pid() int {
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	if h.m.sess == nil {
		return 0
	}
	return h.m.sess.pid
}

func netListenTCP() (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }

func netListenTCPPort(port int) (net.Listener, error) {
	return net.Listen("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)))
}

var syscallSignal0 = syscall.Signal(0)

// startFake starts a fake simulator outside any Machine (a "stale" one):
// it waits at its prompt until killed.
func startFake(t *testing.T, dir, ini string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], ini)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "FAKESIMH=1")
	if _, err := cmd.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	return cmd
}

func startSleep(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	return cmd
}
