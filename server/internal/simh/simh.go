// Package simh runs the patched Open SIMH pdp11 process and implements
// api.Machine (docs/DESIGN.md §6).
//
// The simulator is driven over its stdin/stdout pipes (SCP commands, one at
// a time, each answered by the next "sim> " prompt), its console is a raw
// TCP connection on localhost, and halts are requested through the panel
// export in shared memory (internal/shm). SIGINT is never sent: with a piped
// stdin it can make SimH exit at the prompt.
package simh

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"pdp11emu/server/internal/api"
	"pdp11emu/server/internal/atomicfile"
	"pdp11emu/server/internal/shm"
)

// RunConfigProvider prepares the configuration for a power-on.
// Implemented by internal/systems.
type RunConfigProvider interface {
	// PrepareRun writes run.ini for the selected system into workDir and
	// returns what the machine needs to start and boot it. It fails if the
	// system is not installed or a working disk image is missing.
	PrepareRun(workDir string, ports Ports, shmPath string, speed api.Speed) (*RunConfig, error)
}

// Ports are the listeners the simulator opens.
type Ports struct {
	Console   int    // 127.0.0.1:Console, raw (NOTELNET)
	TTYTelnet string // optional PDP11_TTY_TELNET listener for the terminal lines ("" = none)
}

// RunConfig is the result of PrepareRun.
type RunConfig struct {
	SystemID     string
	INIPath      string   // absolute path of run.ini
	HeaderLines  int      // number of generated header lines at the top of run.ini
	BootCommand  string   // e.g. "boot rq0"
	Dialog       []string // dialog.ini lines (expect/send), run via "do" before every boot
	ReadyPattern string
	Disks        []string // absolute paths of the working disk images
	// Release, if set, is called once the simulator started with this
	// configuration has exited, or could not be started: until then the
	// provider keeps installs and factory resets off the disk images.
	Release func()
}

// Options configure the Machine.
type Options struct {
	Binary string // pdp11 executable
	// WorkDir is $DATA/run, the HOME and cwd of the simulator. The caller
	// holds the data directory lock (pdp11d serve): a simulator recorded in
	// its pid file is then an orphan of a pdp11d that died, and power-on
	// kills it.
	WorkDir        string
	SHMPath        string // panel export file
	ConsolePort    int    // PDP11_PORT_BASE
	TTYTelnet      string // PDP11_TTY_TELNET
	AcceptLicenses []string
	Logger         *slog.Logger
	LogLines       int // simulator output lines kept (default 2000)
	InputCPS       int // paste pacing, characters per second after the first 32 (default 100)
}

// timing of the simulator protocol (DESIGN §6); tests shorten some.
type timing struct {
	startup time.Duration // first prompt after spawning
	command time.Duration // reply to a command at the prompt
	halt    time.Duration // stop after a halt request
	quit    time.Duration // exit after "quit"
	term    time.Duration // exit after SIGTERM
	connect time.Duration // console connection attempts
	retry   time.Duration // between console connection attempts
}

var defaultTiming = timing{
	startup: 15 * time.Second,
	command: 10 * time.Second,
	halt:    2 * time.Second,
	quit:    3 * time.Second,
	term:    2 * time.Second,
	connect: 3 * time.Second,
	retry:   200 * time.Millisecond,
}

// Machine implements api.Machine.
type Machine struct {
	opts Options
	cfg  RunConfigProvider
	sys  api.Systems
	log  *slog.Logger
	to   timing

	// op serializes operations that use the SCP channel or change the
	// process (a channel so that waiting can be cancelled).
	op chan struct{}

	mu          sync.Mutex
	state       api.RunState
	lastErr     string
	sess        *session
	sessions    uint64
	speed       api.Speed // of the running simulator ("" before the first power-on)
	wantSpeed   api.Speed // requested while starting: applied once halted
	systemID    string
	ready       bool
	executed    bool // the CPU executed something since the process started
	bootGen     uint64
	startGen    uint64
	cancelStart context.CancelFunc // cancels a PowerOn or Boot restart in progress
	sr          uint16

	events  *eventBus
	console *console
	logbuf  *logBuffer
	panel   panelSource
}

// New creates a Machine (powered off).
func New(opts Options, cfg RunConfigProvider, sys api.Systems) (*Machine, error) {
	if opts.Binary == "" || opts.WorkDir == "" || opts.SHMPath == "" {
		return nil, errors.New("simh: Binary, WorkDir and SHMPath are required")
	}
	if cfg == nil || sys == nil {
		return nil, errors.New("simh: a RunConfigProvider and Systems are required")
	}
	if opts.ConsolePort <= 0 || opts.ConsolePort > 65535 {
		return nil, fmt.Errorf("simh: console port %d out of range", opts.ConsolePort)
	}
	var err error
	if strings.Contains(opts.Binary, "/") {
		// exec would resolve a relative path against the child's cwd.
		if opts.Binary, err = filepath.Abs(opts.Binary); err != nil {
			return nil, err
		}
	}
	if opts.WorkDir, err = filepath.Abs(opts.WorkDir); err != nil {
		return nil, err
	}
	if opts.SHMPath, err = filepath.Abs(opts.SHMPath); err != nil {
		return nil, err
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	log := opts.Logger.With("component", "simh")
	return &Machine{
		opts:    opts,
		cfg:     cfg,
		sys:     sys,
		log:     log,
		to:      defaultTiming,
		op:      make(chan struct{}, 1),
		state:   api.RunOff,
		events:  newEventBus(log),
		console: newConsole(log, opts.InputCPS),
		logbuf:  newLogBuffer(opts.LogLines),
	}, nil
}

// Close powers the machine off and stops event delivery.
func (m *Machine) Close() error {
	err := m.PowerOff(context.Background())
	m.events.close()
	return err
}

// Log returns the simulator output buffer.
func (m *Machine) Log() api.LogBuffer { return m.logbuf }

// PanelSource returns the panel export reader; it follows the simulator
// across power cycles (ok=false from ReadFrame while none is mapped).
func (m *Machine) PanelSource() api.PanelSource { return &m.panel }

// ---- simple accessors -------------------------------------------------

func (m *Machine) State() api.RunState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

func (m *Machine) LastError() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastErr
}

func (m *Machine) Subscribe(fn func(api.MachineEvent)) (cancel func()) {
	return m.events.subscribe(fn)
}

func (m *Machine) Console() api.Stream { return m.console }

func (m *Machine) Ready() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ready
}

func (m *Machine) SystemID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.systemID
}

// Speed is the speed of the running simulator (requested while starting);
// with no simulator it is the selected system's speed.
func (m *Machine) Speed() api.Speed {
	m.mu.Lock()
	s := m.speed
	if m.wantSpeed != "" {
		s = m.wantSpeed
	} else if m.sess == nil && m.state != api.RunStarting {
		s = ""
	}
	m.mu.Unlock()
	if s == "" {
		s = m.sys.SpeedFor(m.sys.Selected())
	}
	return s
}

func (m *Machine) SetSwitchRegister(v uint16) {
	m.mu.Lock()
	m.sr = v
	s := m.sess
	m.mu.Unlock()
	if s != nil {
		if r := s.shm.Load(); r != nil {
			r.SetSR(v)
		}
	}
}

func (m *Machine) Stats() api.MachineStats {
	m.mu.Lock()
	s, st := m.sess, m.state
	m.mu.Unlock()
	if s == nil {
		return api.MachineStats{}
	}
	out := api.MachineStats{UptimeSec: time.Since(s.started).Seconds()}
	r := s.shm.Load()
	if r == nil || st != api.RunRunning {
		return out
	}
	if snap, ok := r.Snapshot(); ok {
		out.SamplesPerSec = snap.SamplesPerSec
		out.MIPS = s.mips(snap.InstrCount)
	}
	return out
}

// mips derives the instruction rate from instr_count over ≥ 1 s windows.
func (s *session) mips(count uint64) float64 {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	now := time.Now()
	if s.statsT.IsZero() || count < s.statsN {
		s.statsT, s.statsN = now, count
		return s.statsMIPS
	}
	if dt := now.Sub(s.statsT); dt >= time.Second {
		s.statsMIPS = float64(count-s.statsN) / dt.Seconds() / 1e6
		s.statsT, s.statsN = now, count
	}
	return s.statsMIPS
}

// ---- state and events ---------------------------------------------------

// setStateLocked changes the reported state (EvState). Caller holds m.mu.
func (m *Machine) setStateLocked(st api.RunState) {
	if m.state == st {
		return
	}
	m.log.Info("machine state", "from", m.state, "to", st)
	m.state = st
	m.events.emit(api.MachineEvent{Kind: api.EvState, State: st})
	m.console.setHold(st != api.RunRunning)
}

func (m *Machine) setState(st api.RunState) {
	m.mu.Lock()
	m.setStateLocked(st)
	m.mu.Unlock()
}

// emitStopLocked reports a stop (EvStop, plus EvError for output that
// explains a stop without a stop line). Caller holds m.mu.
func (m *Machine) emitStopLocked(stop api.StopInfo, errs []string) {
	m.log.Info("CPU stopped", "reason", stop.Reason, "pc", fmt.Sprintf("%06o", stop.PC), "insn", stop.Insn, "requested", stop.Requested)
	m.events.emit(api.MachineEvent{Kind: api.EvStop, Stop: &stop})
	if len(errs) > 0 {
		m.events.emit(api.MachineEvent{Kind: api.EvError, Text: "simulator: " + strings.Join(errs, "; ")})
	}
}

// stopInfo completes a parsed stop: without a stop line the PC comes from
// the register block, which the simulator refreshes whenever it stops.
func stopInfo(s *session, res runResult) api.StopInfo {
	stop := res.stop
	if !res.stopLine {
		if r := s.shm.Load(); r != nil {
			if snap, ok := r.Snapshot(); ok {
				stop.PC = snap.R[7]
			}
		}
	}
	return stop
}

// asyncStop handles a stop nobody waited for (HALT instruction, breakpoint,
// trap, failed boot command...). Called with s.mu held, so a concurrent
// claim cannot miss it.
func (m *Machine) asyncStop(s *session, c *command) {
	res := parseRun(c.lines)
	if r := s.shm.Load(); r != nil {
		r.CancelHalt() // a halt request that raced this stop must not stop the next run
	}
	stop := stopInfo(s, res)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sess != s {
		return
	}
	m.emitStopLocked(stop, res.errs)
	if m.state == api.RunRunning {
		m.setStateLocked(api.RunHalted)
	}
}

// finishStop reports a stop that an operation claimed and waited for.
func (m *Machine) finishStop(s *session, c *command, halted bool) api.StopInfo {
	res := parseRun(c.lines)
	if r := s.shm.Load(); r != nil {
		r.CancelHalt()
	}
	stop := stopInfo(s, res)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.emitStopLocked(stop, res.errs)
	if halted && m.state == api.RunRunning {
		m.setStateLocked(api.RunHalted)
	}
	return stop
}

func (m *Machine) onReady(s *session, gen uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sess == s && m.bootGen == gen && !m.ready {
		m.ready = true
		m.log.Info("system ready", "system", m.systemID)
		m.events.emit(api.MachineEvent{Kind: api.EvReady})
	}
}

// ---- operation lock -----------------------------------------------------

func (m *Machine) lock(ctx context.Context) error {
	select {
	case m.op <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Machine) unlock() { <-m.op }

// stateError is the error for an operation that cannot run in state st.
func stateError(st api.RunState, needHalted bool) error {
	switch st {
	case api.RunHalted:
		return nil
	case api.RunRunning:
		if needHalted {
			return api.ErrNotHalted
		}
		return nil
	case api.RunStarting, api.RunStopping:
		return api.ErrBusy
	}
	return api.ErrNotPowered
}

// poweredOp acquires the operation lock for an operation that needs a
// powered machine (halted, or also running unless needHalted). It fails
// fast, without waiting, when the state does not allow the operation.
func (m *Machine) poweredOp(ctx context.Context, needHalted bool) (*session, error) {
	if err := stateError(m.State(), needHalted); err != nil {
		return nil, err
	}
	if err := m.lock(ctx); err != nil {
		return nil, err
	}
	m.mu.Lock()
	s, st := m.sess, m.state
	m.mu.Unlock()
	err := stateError(st, needHalted)
	if err == nil && s == nil {
		err = api.ErrNotPowered
	}
	if err != nil {
		m.unlock()
		return nil, err
	}
	return s, nil
}

// ---- SCP primitives (caller holds the operation lock) ---------------------

// exec runs a command at the prompt and returns its reply lines. A
// simulator that does not answer is declared failed.
func (m *Machine) exec(s *session, line string) ([]string, error) {
	c, err := s.send(line, false, false, nil)
	if err != nil {
		return nil, err
	}
	select {
	case <-c.done:
	case <-time.After(m.to.command):
		m.fail(s, fmt.Sprintf("simulator not responding (no prompt after %q)", line))
		return nil, errNotResponding
	}
	if c.err != nil {
		return nil, c.err
	}
	return c.lines, nil
}

// execQuiet runs a command that prints nothing on success.
func (m *Machine) execQuiet(s *session, line string) error {
	lines, err := m.exec(s, line)
	if err != nil {
		return err
	}
	return checkEmpty(line, lines)
}

// run writes a run command; the machine is running once it is written.
func (m *Machine) run(s *session, line string) error {
	if r := s.shm.Load(); r != nil {
		r.CancelHalt() // before any run command (handshake)
	}
	_, err := s.send(line, true, false, func() {
		m.mu.Lock()
		m.executed = true
		m.setStateLocked(api.RunRunning)
		m.mu.Unlock()
	})
	return err
}

// resume continues after a transparent stop: the reported state stays
// running and no events are emitted.
func (m *Machine) resume(s *session) error {
	if r := s.shm.Load(); r != nil {
		r.CancelHalt()
	}
	_, err := s.send("cont", true, false, nil)
	return err
}

// haltRun stops the running CPU through the shm handshake and returns the
// claimed run command with its stop output, or nil if the CPU was not
// running (any stop before the claim was handled asynchronously).
func (m *Machine) haltRun(s *session) (*command, error) {
	r := s.shm.Load()
	if r == nil {
		return nil, errors.New("no panel export mapped")
	}
	c := s.claimRun()
	if c == nil {
		return nil, nil
	}
	r.RequestHalt()
	select {
	case <-c.done:
	case <-time.After(m.to.halt):
		return nil, errNotResponding
	}
	if c.err != nil {
		return nil, c.err
	}
	return c, nil
}

// endWait ends the WAIT state after a halt or step from the console, as
// the KB11-C's console halt ends the WAIT microloop: SimH would otherwise
// go back into the WAIT at the next CONT or step (a step would not get past
// it, and CONT after a deposit to PC would wait for an interrupt before it
// runs the new code). Transparent stops leave the guest's WAIT alone.
// Caller holds the operation lock; the CPU is halted.
func (m *Machine) endWait(s *session) error {
	err := m.execQuiet(s, "deposit WAIT 0")
	if err != nil && !errors.Is(err, errNotResponding) && !errors.Is(err, errExited) {
		m.log.Warn("cannot end the WAIT state", "err", err)
		return nil
	}
	return err
}

// refreshPanel makes the register block current while halted.
func (m *Machine) refreshPanel(s *session) {
	if err := m.execQuiet(s, "set panel refresh"); err != nil && !errors.Is(err, errNotResponding) {
		m.log.Warn("panel refresh failed", "err", err)
	}
}

// fail declares the simulator broken: state error (with an EvError toast)
// and the process is torn down. Caller holds the operation lock.
func (m *Machine) fail(s *session, msg string) {
	m.log.Error("simulator failed", "err", msg)
	m.mu.Lock()
	if m.sess == s {
		m.lastErr = msg
		m.systemID, m.ready = "", false
		m.events.emit(api.MachineEvent{Kind: api.EvError, Text: msg})
		m.setStateLocked(api.RunError)
	}
	m.mu.Unlock()
	m.teardown(s, false)
}

// ---- operations -----------------------------------------------------------

// PowerOn starts the simulator for the selected system (installing it
// first when allowed) and, with opts.Boot, boots it.
func (m *Machine) PowerOn(ctx context.Context, o api.PowerOnOptions) error {
	switch st := m.State(); st {
	case api.RunHalted, api.RunRunning:
		return nil
	case api.RunStarting, api.RunStopping:
		return api.ErrBusy
	}
	id := m.sys.Selected()
	info, ok := m.sys.Get(id)
	if !ok {
		m.setLastError(fmt.Sprintf("selected system %q is not in the catalog", id))
		return api.ErrNoSystem
	}
	install := !info.Installed
	if install && info.License.Class != "redistributable" && !slices.Contains(m.opts.AcceptLicenses, id) {
		m.setLastError(fmt.Sprintf("%s is not installed; its licence (%s) must be accepted before it can be installed", info.Name, info.License.Name))
		return api.ErrNoSystem
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m.mu.Lock()
	if m.state != api.RunOff && m.state != api.RunError {
		m.mu.Unlock()
		return api.ErrBusy
	}
	gen := m.beginStartLocked(cancel)
	m.lastErr = ""
	m.mu.Unlock()
	defer m.endStart(gen)

	if err := m.lock(ctx); err != nil {
		return m.startFailed(gen, err)
	}
	defer m.unlock()
	if install {
		m.log.Info("installing system before power-on", "system", id)
		if err := m.install(ctx, id); err != nil {
			return m.startFailed(gen, fmt.Errorf("installing %s: %w", info.Name, err))
		}
	}
	s, err := m.startSession(ctx, false)
	if err != nil {
		return m.startFailed(gen, err)
	}
	if o.Boot {
		return m.boot(s)
	}
	return nil
}

func (m *Machine) setLastError(msg string) {
	m.mu.Lock()
	m.lastErr = msg
	m.mu.Unlock()
}

// beginStartLocked enters state starting for a power-on or restart whose
// context PowerOff may cancel; it returns the start's generation. Caller
// holds m.mu (the state check and the registration are one step).
func (m *Machine) beginStartLocked(cancel context.CancelFunc) uint64 {
	m.startGen++
	m.cancelStart = cancel
	m.setStateLocked(api.RunStarting)
	return m.startGen
}

func (m *Machine) endStart(gen uint64) {
	m.mu.Lock()
	if m.startGen == gen {
		m.cancelStart = nil
	}
	m.mu.Unlock()
}

// startFailed records a failed power-on or restart: cancelled (PowerOff or
// the caller) → off; otherwise → error with LastError.
func (m *Machine) startFailed(gen uint64, err error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.startGen != gen || m.state != api.RunStarting {
		return err // superseded; the state belongs to someone else now
	}
	m.systemID, m.ready = "", false
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		m.setStateLocked(api.RunOff)
		return err
	}
	m.lastErr = err.Error()
	m.log.Error("power-on failed", "err", err)
	m.setStateLocked(api.RunError)
	return err
}

// install installs a system for PowerOn, also when another client's
// installation of it is already running (ErrBusy): then it waits for that.
func (m *Machine) install(ctx context.Context, id string) error {
	result := make(chan api.Progress, 1)
	cancel := m.sys.SubscribeProgress(func(p api.Progress) {
		if p.ID == id && p.Task == "install" && (p.Phase == "done" || p.Phase == "error" || p.Phase == "cancelled") {
			select {
			case result <- p:
			default:
			}
		}
	})
	defer cancel()
	err := m.sys.Install(ctx, id, true, "")
	if !errors.Is(err, api.ErrBusy) {
		return err
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case p := <-result:
			if p.Phase == "done" {
				return nil
			}
			return fmt.Errorf("installation %s: %s", p.Phase, p.Text)
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			if info, ok := m.sys.Get(id); ok && !info.Installing {
				if info.Installed {
					return nil
				}
				return errors.New("installation did not complete")
			}
		}
	}
}

// startSession runs DESIGN §6 steps 2-6: run.ini, spawn, start-up
// diagnostics, panel export, switch register, console; state halted. The
// speed is the one requested while starting, else the current one (keep,
// for restarts) or the system's. Caller holds the operation lock; on
// failure nothing is left running.
func (m *Machine) startSession(ctx context.Context, keep bool) (*session, error) {
	if err := os.MkdirAll(m.opts.WorkDir, 0o750); err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(m.opts.WorkDir, "simh.ini")); err == nil {
		return nil, fmt.Errorf("refusing to start the simulator: %s exists (SimH would execute it); remove it", filepath.Join(m.opts.WorkDir, "simh.ini"))
	}
	m.mu.Lock()
	speed := m.wantSpeed
	if speed == "" && keep {
		speed = m.speed
	}
	m.mu.Unlock()
	if speed == "" {
		speed = m.sys.SpeedFor(m.sys.Selected())
	}
	rc, err := m.cfg.PrepareRun(m.opts.WorkDir, Ports{Console: m.opts.ConsolePort, TTYTelnet: m.opts.TTYTelnet}, m.opts.SHMPath, speed)
	if err != nil {
		return nil, err
	}
	killStale(m.opts.WorkDir, m.opts.Binary, m.log)
	s, err := m.spawn(rc)
	if err != nil {
		if rc.Release != nil {
			rc.Release()
		}
		return nil, err
	}
	m.log.Info("simulator started", "pid", s.pid, "system", rc.SystemID, "speed", speed)
	if err := writePidFile(m.opts.WorkDir, s.pid); err != nil {
		m.log.Warn("cannot write pid file", "err", err)
	}
	m.mu.Lock()
	m.sess = s
	m.systemID = rc.SystemID
	m.speed = speed
	m.executed, m.ready = false, false
	m.mu.Unlock()

	if err := m.bringUp(ctx, s); err != nil {
		m.teardown(s, false)
		return nil, err
	}

	m.mu.Lock()
	m.setStateLocked(api.RunHalted)
	want := m.wantSpeed
	m.wantSpeed = ""
	m.mu.Unlock()
	if want != "" && want != speed {
		if err := m.applySpeed(s, want); err != nil {
			m.log.Warn("speed change after power-on failed", "err", err)
		}
	}
	return s, nil
}

// bringUp waits for the first prompt and checks the start-up output, maps
// the panel export and connects the console.
func (m *Machine) bringUp(ctx context.Context, s *session) error {
	select {
	case <-s.first.done:
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(m.to.startup):
		return fmt.Errorf("the simulator did not reach its prompt within %s (output: %s)", m.to.startup, s.recentOutput())
	}
	if s.first.err != nil {
		s.waitExit(time.Second)
		return fmt.Errorf("the simulator exited during start-up (%s): %s", s.exitStatus(), s.recentOutput())
	}
	if err := m.checkStartup(s); err != nil {
		return err
	}
	r, err := shm.Open(m.opts.SHMPath)
	if err != nil {
		return fmt.Errorf("panel export: %w", err)
	}
	s.shm.Store(r)
	m.panel.set(r)
	m.mu.Lock()
	sr := m.sr
	m.mu.Unlock()
	r.SetSR(sr)
	r.CancelHalt()

	conn, err := m.dialConsole(ctx)
	if err != nil {
		return err
	}
	m.console.attach(conn, m.dialConsoleOnce)
	return nil
}

// checkStartup turns the diagnostics of run.ini into a power-on error.
func (m *Machine) checkStartup(s *session) error {
	raw, err := os.ReadFile(s.rc.INIPath)
	if err != nil {
		return err
	}
	fileLines := strings.Split(string(raw), "\n")
	diags := parseDiagnostics(s.first.lines, filepath.Base(s.rc.INIPath), fileLines)
	if len(diags) == 0 {
		return nil
	}
	msgs := make([]string, 0, len(diags))
	for _, d := range diags {
		msgs = append(msgs, m.describeLine(s.rc, fileLines, d))
	}
	return fmt.Errorf("simulator configuration failed: %s", strings.Join(msgs, "; "))
}

// describeLine names a diagnostic's origin: the generated header, the
// system's boot.ini, or the PDP11_TTY_TELNET terminal listener.
func (m *Machine) describeLine(rc *RunConfig, fileLines []string, d diagnostic) string {
	text := ""
	if d.Line >= 1 && d.Line <= len(fileLines) {
		text = strings.TrimSpace(fileLines[d.Line-1])
	}
	switch {
	case d.Line <= rc.HeaderLines:
		return fmt.Sprintf("run.ini line %d (%s): %s", d.Line, d.Cmd, d.Text)
	case m.opts.TTYTelnet != "" && strings.HasPrefix(text, "attach -u ") && strings.HasSuffix(text, " "+m.opts.TTYTelnet):
		return fmt.Sprintf("terminal listener PDP11_TTY_TELNET=%s (%s): %s", m.opts.TTYTelnet, d.Cmd, d.Text)
	}
	return fmt.Sprintf("boot.ini line %d (%s): %s", d.Line-rc.HeaderLines, d.Cmd, d.Text)
}

func (m *Machine) consoleAddr() string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(m.opts.ConsolePort))
}

func (m *Machine) dialConsoleOnce(ctx context.Context) (net.Conn, error) {
	d := net.Dialer{Timeout: time.Second}
	return d.DialContext(ctx, "tcp", m.consoleAddr())
}

// dialConsole connects to the console listener, retrying (SimH accepts the
// connection when it next polls).
func (m *Machine) dialConsole(ctx context.Context) (net.Conn, error) {
	deadline := time.Now().Add(m.to.connect)
	for {
		conn, err := m.dialConsoleOnce(ctx)
		if err == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("cannot connect to the simulator console at %s: %w", m.consoleAddr(), err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(m.to.retry):
		}
	}
}

// boot runs the boot sequence in a simulator that has executed nothing:
// a fresh dialogue (noexpect, nosend, dialog.ini), then the boot command.
// Caller holds the operation lock; the CPU is halted.
func (m *Machine) boot(s *session) error {
	rc := s.rc
	var b strings.Builder
	b.WriteString("noexpect\nnosend\n")
	for _, l := range rc.Dialog {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	if err := atomicfile.WriteFile(filepath.Join(m.opts.WorkDir, "dialog.do"), []byte(b.String()), 0o640); err != nil {
		return m.bootFailed(err)
	}
	lines, err := m.exec(s, "do dialog.do")
	if err != nil {
		return m.bootFailed(err)
	}
	if bad := dialogErrors(lines, rc.Dialog); len(bad) > 0 {
		return m.bootFailed(fmt.Errorf("boot dialogue: %s", strings.Join(bad, "; ")))
	}
	m.mu.Lock()
	m.ready = false
	m.bootGen++
	gen := m.bootGen
	m.mu.Unlock()
	if rc.ReadyPattern != "" {
		m.console.armReady(rc.ReadyPattern, func() { m.onReady(s, gen) })
	}
	if err := m.run(s, rc.BootCommand); err != nil {
		m.console.disarmReady()
		return m.bootFailed(err)
	}
	m.log.Info("booting", "system", rc.SystemID, "command", rc.BootCommand)
	return nil
}

// bootFailed reports a boot that could not start; the CPU stays halted.
func (m *Machine) bootFailed(err error) error {
	err = fmt.Errorf("boot failed: %w", err)
	m.log.Error("boot failed", "err", err)
	m.events.emit(api.MachineEvent{Kind: api.EvError, Text: err.Error()})
	return err
}

// dialogErrors returns the problems SimH reported while executing
// dialog.do (lines 1-2 are noexpect/nosend, then the dialog lines).
func dialogErrors(lines, dialog []string) []string {
	fileLines := append([]string{"noexpect", "nosend"}, dialog...)
	var out []string
	for _, d := range parseDiagnostics(lines, "dialog.do", fileLines) {
		if d.Line > 2 {
			out = append(out, fmt.Sprintf("dialog line %d (%s): %s", d.Line-2, d.Cmd, d.Text))
		} else {
			out = append(out, fmt.Sprintf("%s: %s", d.Cmd, d.Text))
		}
	}
	if len(out) == 0 {
		// Errors of "do" itself come without an echo ("File open error").
		for _, l := range meaningful(lines) {
			if !reEcho.MatchString(l) {
				out = append(out, l)
			}
		}
	}
	return out
}

// Boot boots the selected system; a simulator that has executed anything
// is restarted first, because the boot dialogue only works in a fresh one.
func (m *Machine) Boot(ctx context.Context) error {
	s, err := m.poweredOp(ctx, false)
	if err != nil {
		return err
	}
	defer m.unlock()
	m.mu.Lock()
	fresh := m.state == api.RunHalted && !m.executed
	m.mu.Unlock()
	if fresh {
		return m.boot(s)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m.mu.Lock()
	gen := m.beginStartLocked(cancel)
	m.mu.Unlock()
	defer m.endStart(gen)
	m.log.Info("restarting the simulator to boot")
	if s.running() {
		// A maintenance stop: no events (the state is already starting).
		if c, err := m.haltRun(s); err == nil && c != nil {
			if r := s.shm.Load(); r != nil {
				r.CancelHalt()
			}
		}
	}
	m.teardown(s, true)
	s, err = m.startSession(ctx, true)
	if err != nil {
		return m.startFailed(gen, err)
	}
	return m.boot(s)
}

// PowerOff halts the CPU and quits the simulator; it cancels a PowerOn or
// restart in progress.
func (m *Machine) PowerOff(ctx context.Context) error {
	m.mu.Lock()
	if m.cancelStart != nil {
		m.cancelStart()
	}
	m.mu.Unlock()
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.unlock()
	m.mu.Lock()
	s := m.sess
	if s == nil {
		m.systemID, m.ready = "", false
		m.setStateLocked(api.RunOff)
		m.mu.Unlock()
		return nil
	}
	m.setStateLocked(api.RunStopping)
	m.mu.Unlock()

	if s.running() {
		c, err := m.haltRun(s)
		switch {
		case err != nil:
			m.log.Warn("power off: no stop after the halt request; terminating the simulator", "err", err)
		case c != nil:
			m.finishStop(s, c, false)
		}
	}
	m.teardown(s, true)
	m.mu.Lock()
	m.systemID, m.ready = "", false
	m.setStateLocked(api.RunOff)
	m.mu.Unlock()
	m.log.Info("powered off")
	return nil
}

// teardown ends a session: "quit" at the prompt (if asked), then closing
// stdin and SIGTERM, then SIGKILL; it releases the console, the panel
// export and the pid file. Caller holds the operation lock.
func (m *Machine) teardown(s *session, quit bool) {
	s.setExpected()
	m.console.disarmReady()
	m.console.detach() // before the simulator closes its end
	exited := false
	if quit && !s.running() {
		if _, err := s.send("quit", false, false, nil); err == nil {
			exited = s.waitExit(m.to.quit)
		}
	}
	if !exited {
		// A halted SimH exits at stdin EOF; a running one stops at SIGTERM
		// and then reads the EOF.
		s.stdin.Close()
		s.signal(syscall.SIGTERM)
		if !s.waitExit(m.to.term) {
			m.log.Warn("simulator did not exit; killing it", "pid", s.pid)
			s.signal(syscall.SIGKILL)
			s.waitExit(5 * time.Second)
		}
	}
	s.stdin.Close()
	select {
	case <-s.exited:
		s.release() // before the caller reports the machine off
	default:
	}
	if r := s.shm.Swap(nil); r != nil {
		m.panel.clear(r)
		r.Close()
	}
	removePidFile(m.opts.WorkDir, s.pid)
	m.mu.Lock()
	if m.sess == s {
		m.sess = nil
	}
	m.mu.Unlock()
}

// onSessionExit runs when a simulator process has ended. If pdp11d did not
// ask for that, the machine goes to state error.
func (m *Machine) onSessionExit(s *session) {
	_ = m.lock(context.Background())
	defer m.unlock()
	m.mu.Lock()
	current := m.sess == s
	m.mu.Unlock()
	if !current {
		return
	}
	m.fail(s, fmt.Sprintf("the simulator exited unexpectedly (%s); last output: %s", s.exitStatus(), s.recentOutput()))
}

// Halt stops the CPU after the current instruction (no-op when halted).
func (m *Machine) Halt(ctx context.Context) error {
	if st := m.State(); st == api.RunHalted {
		return nil
	}
	s, err := m.poweredOp(ctx, false)
	if err != nil {
		return err
	}
	defer m.unlock()
	c, err := m.haltRun(s)
	if err != nil {
		return m.opFailed(s, err)
	}
	if c != nil {
		if err := m.endWait(s); err != nil {
			return err
		}
		m.finishStop(s, c, true)
	}
	return nil
}

// opFailed turns a lost or unresponsive simulator into state error.
func (m *Machine) opFailed(s *session, err error) error {
	if errors.Is(err, errNotResponding) {
		m.mu.Lock()
		current := m.sess == s
		m.mu.Unlock()
		if current {
			m.fail(s, "simulator not responding (no stop within 2 s of a halt request)")
		}
	}
	return err
}

// Continue resumes execution at PC (SimH "cont").
func (m *Machine) Continue(ctx context.Context) error {
	s, err := m.poweredOp(ctx, true)
	if err != nil {
		return err
	}
	defer m.unlock()
	return m.run(s, "cont")
}

// Start performs a console START: reset all devices and run at addr.
func (m *Machine) Start(ctx context.Context, addr uint16) error {
	s, err := m.poweredOp(ctx, true)
	if err != nil {
		return err
	}
	defer m.unlock()
	return m.run(s, fmt.Sprintf("run %o", addr))
}

// Step executes one instruction and returns when the CPU halted again.
// Only EvStop is emitted; State() stays halted.
func (m *Machine) Step(ctx context.Context) error {
	s, err := m.poweredOp(ctx, true)
	if err != nil {
		return err
	}
	defer m.unlock()
	if r := s.shm.Load(); r != nil {
		r.CancelHalt()
	}
	c, err := s.send("step", true, true, func() {
		m.mu.Lock()
		m.executed = true
		m.mu.Unlock()
	})
	if err != nil {
		return err
	}
	select {
	case <-c.done:
	case <-time.After(m.to.halt):
		// One instruction cannot take this long: stop it like a HALT switch.
		if r := s.shm.Load(); r != nil {
			r.RequestHalt()
		}
		select {
		case <-c.done:
		case <-time.After(m.to.halt):
			m.fail(s, "simulator not responding (step did not complete)")
			return errNotResponding
		}
	}
	if c.err != nil {
		return c.err
	}
	if err := m.endWait(s); err != nil {
		return err
	}
	m.finishStop(s, c, false)
	return nil
}

// ConsoleReset performs a console START with HALT: reset, stay halted.
func (m *Machine) ConsoleReset(ctx context.Context) error {
	s, err := m.poweredOp(ctx, true)
	if err != nil {
		return err
	}
	defer m.unlock()
	if err := m.execQuiet(s, "reset"); err != nil {
		return err
	}
	m.refreshPanel(s)
	return nil
}

// Examine reads a location (CPU halted).
func (m *Machine) Examine(ctx context.Context, loc api.Location) (uint16, error) {
	arg, err := locationArg(loc)
	if err != nil {
		return 0, err
	}
	s, err := m.poweredOp(ctx, true)
	if err != nil {
		return 0, err
	}
	defer m.unlock()
	return m.examine(s, arg)
}

func (m *Machine) examine(s *session, arg string) (uint16, error) {
	cmd := "examine " + arg
	lines, err := m.exec(s, cmd)
	if err != nil {
		return 0, err
	}
	return parseExamine(cmd, lines)
}

// Deposit writes a location (CPU halted) and refreshes the panel registers.
func (m *Machine) Deposit(ctx context.Context, loc api.Location, v uint16) error {
	arg, err := locationArg(loc)
	if err != nil {
		return err
	}
	s, err := m.poweredOp(ctx, true)
	if err != nil {
		return err
	}
	defer m.unlock()
	if err := m.execQuiet(s, fmt.Sprintf("deposit %s %o", arg, v)); err != nil {
		return err
	}
	m.refreshPanel(s)
	return nil
}

// Translate returns the 22-bit physical address of a location.
func (m *Machine) Translate(ctx context.Context, loc api.Location) (uint32, error) {
	if loc.Register != "" {
		return 0, fmt.Errorf("register %s has no physical address", loc.Register)
	}
	if _, err := locationArg(loc); err != nil {
		return 0, err
	}
	if !loc.Virtual {
		return loc.Addr, nil
	}
	s, err := m.poweredOp(ctx, true)
	if err != nil {
		return 0, err
	}
	defer m.unlock()
	mmr0, err := m.examine(s, "MMR0")
	if err != nil {
		return 0, err
	}
	mmr3, err := m.examine(s, "MMR3")
	if err != nil {
		return 0, err
	}
	return translate(uint16(loc.Addr), loc.Mode, loc.DSpace, mmr0, mmr3, func(name string) (uint16, error) {
		return m.examine(s, name)
	})
}

// SetSpeed changes the speed of the running simulator, transparently
// halting and continuing it if needed. It never persists anything.
func (m *Machine) SetSpeed(ctx context.Context, sp api.Speed) error {
	if sp != api.SpeedFast && sp != api.SpeedAuthentic {
		return fmt.Errorf("invalid speed %q", sp)
	}
	m.mu.Lock()
	switch m.state {
	case api.RunOff, api.RunError:
		m.mu.Unlock()
		return api.ErrNotPowered
	case api.RunStopping:
		m.mu.Unlock()
		return api.ErrBusy
	case api.RunStarting:
		m.wantSpeed = sp // applied once the simulator is up (startSession)
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.unlock()
	m.mu.Lock()
	s, st := m.sess, m.state
	if st == api.RunStarting {
		m.wantSpeed = sp
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()
	if err := stateError(st, false); err != nil || s == nil {
		return api.ErrNotPowered
	}
	if st == api.RunHalted {
		return m.applySpeed(s, sp)
	}

	// Running: a transparent maintenance stop.
	c, err := m.haltRun(s)
	if err != nil {
		return m.opFailed(s, err)
	}
	if c == nil {
		return m.applySpeed(s, sp) // stopped by itself meanwhile
	}
	if res := parseRun(c.lines); !res.stopLine || res.stop.Reason != reasonPanelHalt {
		// A real stop raced the request: it is reported, the CPU stays halted.
		m.finishStop(s, c, true)
		return m.applySpeed(s, sp)
	}
	err = m.applySpeed(s, sp)
	if rerr := m.resume(s); rerr != nil {
		return rerr
	}
	return err
}

// applySpeed sends the speed commands at the prompt.
func (m *Machine) applySpeed(s *session, sp api.Speed) error {
	for _, cmd := range SpeedCommands(sp) {
		if err := m.execQuiet(s, cmd); err != nil {
			return err
		}
	}
	m.mu.Lock()
	m.speed = sp
	m.mu.Unlock()
	m.log.Info("speed changed", "speed", sp)
	return nil
}

// ---- panel source ---------------------------------------------------------

// panelSource forwards to the panel export of the current simulator.
type panelSource struct {
	r atomic.Pointer[shm.Reader]
}

func (p *panelSource) set(r *shm.Reader)   { p.r.Store(r) }
func (p *panelSource) clear(r *shm.Reader) { p.r.CompareAndSwap(r, nil) }

func (p *panelSource) ReadFrame(dst []api.Sample) (int, api.PanelSnapshot, bool) {
	r := p.r.Load()
	if r == nil {
		return 0, api.PanelSnapshot{}, false
	}
	return r.ReadFrame(dst)
}

var (
	_ api.Machine     = (*Machine)(nil)
	_ api.PanelSource = (*panelSource)(nil)
)
