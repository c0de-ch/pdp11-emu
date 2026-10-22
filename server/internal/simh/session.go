package simh

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"pdp11emu/server/internal/atomicfile"
	"pdp11emu/server/internal/shm"
)

var (
	errExited        = errors.New("simulator exited")
	errNotAtPrompt   = errors.New("simulator is not at its prompt")
	errNotResponding = errors.New("simulator not responding")
)

const (
	maxReplyLines = 1000 // output kept per command (the stop line is last)
	tailLines     = 20   // recent output kept for error messages
	stdinTimeout  = 5 * time.Second
)

// session is one simulator process: its pipes, the SCP command channel and
// the panel export it attached.
//
// SCP is strictly request/response: a command is written only at a prompt,
// and the next prompt ends its reply. A run command (boot, cont, run, go,
// step) is answered by the prompt that follows the CPU's stop. Lock order:
// Machine.op, session.mu, console locks, Machine.mu, event bus.
type session struct {
	m       *Machine
	id      uint64
	rc      *RunConfig
	pid     int
	stdin   *os.File
	started time.Time
	shm     atomic.Pointer[shm.Reader] // the panel export, once mapped

	readerDone chan struct{}
	exited     chan struct{} // closed when the process was reaped and its output drained
	waitErr    error         // valid once exited is closed
	release    func()        // RunConfig.Release (once), when the process has exited

	mu       sync.Mutex
	cur      *command // awaiting its prompt; initially the start-up output
	first    *command // the start-up output, completed by the first prompt
	eof      bool
	expected bool     // pdp11d asked the simulator to exit
	tail     []string // recent output lines

	statsMu   sync.Mutex
	statsT    time.Time
	statsN    uint64
	statsMIPS float64
}

// command is one SCP command and its reply.
type command struct {
	line    string
	run     bool // the CPU runs until the reply's prompt
	claimed bool // an operation handles the stop itself (no async stop handling)
	lines   []string
	done    chan struct{} // closed at the reply's prompt or at EOF
	err     error         // errExited if the simulator died first
}

// spawn starts the simulator for rc. The child gets its own process group
// and SIGTERM when pdp11d dies (Pdeathsig follows the thread that forked
// it, so that thread stays locked for the child's lifetime).
func (m *Machine) spawn(rc *RunConfig) (*session, error) {
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return nil, err
	}
	ini := rc.INIPath
	if rel, err := filepath.Rel(m.opts.WorkDir, rc.INIPath); err == nil && !strings.HasPrefix(rel, "..") {
		ini = rel // SimH echoes the absolute path either way
	}
	cmd := exec.Command(m.opts.Binary, ini)
	cmd.Dir = m.opts.WorkDir
	cmd.Env = simulatorEnv(m.opts.WorkDir)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, outW
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}

	m.mu.Lock()
	m.sessions++
	id := m.sessions
	m.mu.Unlock()
	s := &session{
		m:          m,
		id:         id,
		rc:         rc,
		stdin:      inW,
		readerDone: make(chan struct{}),
		exited:     make(chan struct{}),
		release:    func() {},
	}
	if rc.Release != nil {
		s.release = sync.OnceFunc(rc.Release)
	}
	s.first = &command{done: make(chan struct{})}
	s.cur = s.first

	started := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := cmd.Start(); err != nil {
			started <- err
			return
		}
		started <- nil
		err := cmd.Wait()
		<-s.readerDone
		s.waitErr = err
		close(s.exited)
	}()
	err = <-started
	inR.Close()
	outW.Close()
	if err != nil {
		inW.Close()
		outR.Close()
		return nil, fmt.Errorf("cannot start simulator %s: %w", m.opts.Binary, err)
	}
	s.pid = cmd.Process.Pid
	s.started = time.Now()
	go s.readLoop(outR)
	go func() {
		<-s.exited
		s.release()
		m.onSessionExit(s)
	}()
	return s, nil
}

// simulatorEnv is pdp11d's environment without its own settings (the
// password must not leak into the child), with HOME set to the work dir.
func simulatorEnv(workDir string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "PDP11_") || strings.HasPrefix(kv, "HOME=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "HOME="+workDir)
}

func (s *session) readLoop(r *os.File) {
	defer close(s.readerDone)
	defer r.Close()
	f := framer{onLine: s.onLine, onPrompt: s.onPrompt}
	buf := make([]byte, 64<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			f.feed(buf[:n])
		}
		if err != nil {
			break
		}
	}
	f.flush()
	s.onEOF()
}

func (s *session) onLine(line string) {
	s.m.logbuf.add(line)
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.cur; c != nil {
		if len(c.lines) >= maxReplyLines {
			c.lines = append(c.lines[:0], c.lines[len(c.lines)-maxReplyLines/2:]...)
		}
		c.lines = append(c.lines, line)
	}
	if strings.TrimSpace(line) != "" {
		if len(s.tail) >= tailLines {
			s.tail = append(s.tail[:0], s.tail[1:]...)
		}
		s.tail = append(s.tail, line)
	}
}

func (s *session) onPrompt() {
	s.mu.Lock()
	c := s.cur
	s.cur = nil
	if c != nil && c.run && !c.claimed {
		// The CPU stopped by itself (HALT, breakpoint, trap...): update the
		// machine state now, atomically with respect to claims.
		s.m.asyncStop(s, c)
	}
	s.mu.Unlock()
	if c != nil {
		close(c.done)
	} else {
		s.m.log.Debug("unsolicited simulator prompt", "session", s.id)
	}
}

func (s *session) onEOF() {
	s.mu.Lock()
	s.eof = true
	c := s.cur
	s.cur = nil
	s.mu.Unlock()
	if c != nil {
		c.err = errExited
		close(c.done)
	}
}

// send writes one command at the prompt. onSent runs with the session
// locked, before the reply can be processed (used to publish the running
// state before a stop can be seen).
func (s *session) send(line string, run, claimed bool, onSent func()) (*command, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.eof {
		return nil, errExited
	}
	if s.cur != nil {
		return nil, errNotAtPrompt
	}
	s.m.logbuf.add(prompt + line)
	_ = s.stdin.SetWriteDeadline(time.Now().Add(stdinTimeout))
	if _, err := s.stdin.Write([]byte(line + "\n")); err != nil {
		return nil, fmt.Errorf("simulator stdin: %w", err)
	}
	c := &command{line: line, run: run, claimed: claimed, done: make(chan struct{})}
	s.cur = c
	if onSent != nil {
		onSent()
	}
	return c, nil
}

// claimRun takes over the stop of the outstanding run command, if any:
// the caller (holding the machine's operation lock) handles it.
func (s *session) claimRun() *command {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.cur; c != nil && c.run && !c.claimed {
		c.claimed = true
		return c
	}
	return nil
}

// running reports whether a run command is outstanding.
func (s *session) running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur != nil && s.cur.run
}

func (s *session) setExpected() {
	s.mu.Lock()
	s.expected = true
	s.mu.Unlock()
}

func (s *session) recentOutput() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.tail, " | ")
}

// waitExit waits up to d for the process to exit.
func (s *session) waitExit(d time.Duration) bool {
	select {
	case <-s.exited:
		return true
	case <-time.After(d):
		return false
	}
}

// signal sends sig to the simulator (and with SIGKILL to its group).
func (s *session) signal(sig syscall.Signal) {
	select {
	case <-s.exited:
		return // reaped: the pid may be reused
	default:
	}
	pid := s.pid
	if sig == syscall.SIGKILL {
		pid = -pid
	}
	_ = syscall.Kill(pid, sig)
}

// exitStatus describes how the process ended (after exited is closed).
func (s *session) exitStatus() string {
	if s.waitErr == nil {
		return "exit status 0"
	}
	return s.waitErr.Error()
}

// pidFile management: the pid of the running simulator is recorded so a
// simulator orphaned by a pdp11d crash that Pdeathsig did not cover can be
// killed by the next start.

func pidFilePath(workDir string) string { return filepath.Join(workDir, "pdp11.pid") }

func writePidFile(workDir string, pid int) error {
	return atomicfile.WriteFile(pidFilePath(workDir), []byte(strconv.Itoa(pid)+"\n"), 0o640)
}

// removePidFile removes the pid file if it still names pid.
func removePidFile(workDir string, pid int) {
	b, err := os.ReadFile(pidFilePath(workDir))
	if err != nil {
		return
	}
	if p, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && p == pid {
		_ = os.Remove(pidFilePath(workDir))
	}
}

// killStale kills a simulator left over from a previous pdp11d, but only if
// /proc/<pid>/exe shows it is the configured binary (pids are reused). The
// caller holds the data directory lock, so that pdp11d is gone.
func killStale(workDir, binary string, log *slog.Logger) {
	path := pidFilePath(workDir)
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	defer os.Remove(path)
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 1 {
		return
	}
	exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return // gone (or not ours to inspect)
	}
	if !sameBinary(strings.TrimSuffix(exe, " (deleted)"), binary) {
		return
	}
	log.Warn("killing stale simulator", "pid", pid)
	_ = syscall.Kill(pid, syscall.SIGTERM)
	for i := 0; i < 20; i++ {
		time.Sleep(100 * time.Millisecond)
		if !alive(pid) {
			return
		}
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

// alive reports whether pid is a live (not zombie) process.
func alive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	// "pid (comm) S ...": the state follows the last ')'.
	if i := strings.LastIndexByte(string(stat), ')'); i >= 0 && i+2 < len(stat) {
		return stat[i+2] != 'Z' && stat[i+2] != 'X'
	}
	return true
}

func sameBinary(exe, binary string) bool {
	want, err := filepath.Abs(binary)
	if err != nil {
		return false
	}
	if r, err := filepath.EvalSymlinks(want); err == nil {
		want = r
	}
	return filepath.Clean(exe) == want
}
