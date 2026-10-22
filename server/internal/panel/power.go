package panel

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"pdp11emu/server/internal/api"
)

// execPowerOn powers on (key OFF → POWER/LOCK, autostart, retryPower,
// toolbar boot while off). It boots unless ENABLE/HALT is at HALT when the
// power comes on, or always for the toolbar boot.
func (p *Panel) execPowerOn(o *op) {
	st := p.m.State()
	selected := p.sys.Selected()
	p.mu.Lock()
	if p.ctl.key == keyOff || (st != api.RunOff && st != api.RunError) {
		p.mu.Unlock()
		return
	}
	boot := o.boot || !p.ctl.effectiveHalt()
	p.mu.Unlock()

	var bw *bootWatch
	if boot {
		bw = p.expectBoot(o)
	}
	err := p.m.PowerOn(o.ctx, api.PowerOnOptions{Boot: boot})
	if err == nil {
		p.waitObserved(o.ctx, stopWait)
	} else {
		p.cancelBootWatch(bw)
	}

	p.mu.Lock()
	switch {
	case err == nil:
		p.power = PowerStatus{}
	case o.ctx.Err() != nil || p.ctl.key == keyOff:
		// Cancelled by the key: not a failure the user has to see.
	case errors.Is(err, api.ErrNoSystem):
		p.power = PowerStatus{Failed: true, NeedsInstall: selected}
	default:
		p.power = PowerStatus{Failed: true}
	}
	p.mu.Unlock()
	p.notify()
	if err != nil && o.ctx.Err() == nil {
		p.log.Info("power on failed", "err", err)
	}
}

// execBoot boots the selected system: power on if off, else Machine.Boot
// (which restarts the simulator when needed).
func (p *Panel) execBoot(o *op) {
	st := p.m.State()
	p.mu.Lock()
	key, effHalt := p.ctl.key, p.ctl.effectiveHalt()
	p.mu.Unlock()
	switch {
	case key == keyOff:
		return // the key was turned off after the request
	case key == keyLock:
		p.hint(o, hintBootLocked)
		return
	case effHalt:
		p.hint(o, hintBootHalt)
		return
	}
	switch st {
	case api.RunOff, api.RunError:
		p.execPowerOn(&op{kind: opPowerOn, boot: true, client: o.client, req: o.req, ctx: o.ctx})
	case api.RunHalted, api.RunRunning:
		ctx, cancel := context.WithTimeout(o.ctx, bootTimeout)
		defer cancel()
		bw := p.expectBoot(o)
		if err := p.m.Boot(ctx); err != nil {
			p.cancelBootWatch(bw)
			if o.ctx.Err() == nil {
				p.log.Info("boot failed", "err", err)
				p.hint(o, consoleErrHint(err))
			}
			return
		}
		p.cpuStarted()
	default:
		p.hint(o, stateHint(st))
	}
}

// Shutdown flow timing (PANEL_LOGIC.md §3 shutdownOS); variables for tests.
var (
	defaultStepTimeout = 30 * time.Second
	haltAfterScript    = 30 * time.Second // the last step typed a command: wait for the OS to halt
	settleAfterScript  = 3 * time.Second  // the script ended without a command (Unix V5-V7 sync)
)

// answerDelay is the pause between an Expect match and the next Send: an
// OS that prints its prompt before it posts the terminal read (RSX, RSTS)
// would lose characters typed at once (like the boot dialogue's
// "send -t after=250000").
const answerDelay = 250 * time.Millisecond

var (
	errNotRunning = errors.New(hintShutdownIdle)
	errNoScript   = errors.New(hintNoScript)
)

// ShutdownOS runs the clean-shutdown script of the running system and
// waits until the OS halted (or, for a script that ends without a
// command, until it settled). Unlike the shutdownOS input it neither powers
// off nor touches the controls: it is meant for daemon shutdown with
// PDP11_SHUTDOWN_ON_STOP, after which the caller calls Machine.PowerOff
// (and the next start autostarts again). It waits for the panel operation
// in flight and may be called before or after Run returned. It returns nil
// at once when the CPU is not running.
func (p *Panel) ShutdownOS(ctx context.Context) error {
	if err := p.acquireExec(ctx); err != nil {
		return err
	}
	defer p.releaseExec()
	p.mu.Lock()
	p.busyExtra = "shutdown"
	p.mu.Unlock()
	p.notify()
	defer func() {
		p.mu.Lock()
		p.busyExtra = ""
		p.mu.Unlock()
		p.notify()
	}()
	err := p.shutdown(&op{kind: opShutdown, ctx: ctx}, false)
	if errors.Is(err, errNotRunning) {
		return nil
	}
	return err
}

type waitResult int

const (
	waitMatched waitResult = iota
	waitStopped            // the CPU stopped by itself (HALT instruction, trap...)
	waitHalted             // stopped by request (HALT switch)
	waitDown               // the machine went off or into error
	waitTimeout
	waitCancelled
)

// shutdown types the system's shutdown script on the console: for each
// step it waits (≤ timeout) for Expect in the console output since the
// previous step, then types Send (answerDelay after a match). Expect is a
// literal, case-sensitive substring; the first step's also matches the
// text after the last newline of the history (a prompt already on
// screen). Then it waits for the CPU to halt (30 s) or, if the script ends
// without typing anything, 3 s more. Any stop that was not requested from
// the panel ends the waits (the OS halted: power off); a HALT from the
// panel cancels. powerOff: finally turn the key to OFF and power off (the
// shutdownOS input).
func (p *Panel) shutdown(o *op, powerOff bool) error {
	if p.m.State() != api.RunRunning {
		p.hint(o, hintShutdownIdle)
		return errNotRunning
	}
	id := p.runningSystem()
	steps := p.sys.ShutdownScript(id)
	if len(steps) == 0 {
		p.hint(o, hintNoScript)
		return errNoScript
	}
	progress := func(phase string, pct float64, text string) {
		p.publishProgress(api.Progress{Task: "shutdown", ID: id, Phase: phase, Pct: pct, Text: text, Req: o.req})
	}
	fail := func(text string) error {
		progress("error", 0, text)
		p.toastAll("warn", text+" Pull the plug (turn the key to OFF) only if you are sure the OS is idle.", o.req)
		return errors.New(text)
	}

	stream := p.m.Console()
	if stream == nil {
		return fail("Shut down OS: the console is not available.")
	}
	w := watchConsole(stream)
	defer w.close()
	since := p.stopCount()
	total := float64(len(steps) + 1)
	pos := 0
	res := waitMatched
	answer := false // an Expect matched and nothing was typed since
	for i, s := range steps {
		pct := 100 * float64(i) / total
		if s.Expect != "" {
			p.log.Debug("shutdown step", "system", id, "expect", s.Expect)
			progress("step", pct, "Waiting for “"+scriptText(s.Expect)+"”")
			timeout := defaultStepTimeout
			if s.TimeoutSec > 0 {
				timeout = time.Duration(s.TimeoutSec) * time.Second
			}
			var end int
			res = p.waitShutdown(o.ctx, timeout, since, w, func() bool {
				var ok bool
				end, ok = w.find(pos, s.Expect)
				return ok
			})
			if res != waitMatched {
				if res == waitTimeout {
					return fail(fmt.Sprintf("Shut down OS: “%s” did not appear on the console within %s.", scriptText(s.Expect), timeout))
				}
				break
			}
			pos = end
			answer = true
		}
		if s.Send != "" {
			if answer {
				answer = false
				if res = p.waitShutdown(o.ctx, answerDelay, since, nil, nil); res != waitTimeout {
					break
				}
				res = waitMatched
			}
			p.log.Debug("shutdown step", "system", id, "send", s.Send)
			progress("step", pct, typingText(s.Send))
			pos = w.end()
			if _, err := stream.Write([]byte(s.Send)); err != nil {
				return fail("Shut down OS: cannot type on the console: " + err.Error())
			}
		}
	}
	if res == waitMatched {
		progress("step", 100*(total-1)/total, "Waiting for the system to halt…")
		if steps[len(steps)-1].Send != "" {
			res = p.waitShutdown(o.ctx, haltAfterScript, since, nil, nil)
			if res == waitTimeout {
				return fail(fmt.Sprintf("Shut down OS: the OS did not halt within %s.", haltAfterScript))
			}
		} else {
			res = p.waitShutdown(o.ctx, settleAfterScript, since, nil, nil)
			if res == waitTimeout {
				res = waitStopped // settled: safe to power off
			}
		}
	}
	switch res {
	case waitCancelled:
		progress("cancelled", 0, "Shutdown cancelled.")
		return o.ctx.Err()
	case waitHalted:
		progress("cancelled", 0, "Shutdown cancelled: the CPU was halted from the panel.")
		p.toastAll("info", "Shut down OS cancelled: the CPU was halted from the panel.", o.req)
		return errors.New("shutdown cancelled by a halt")
	case waitDown:
		return fail("Shut down OS: the machine stopped unexpectedly.")
	}

	if !powerOff {
		progress("done", 100, "The OS has shut down.")
		return nil
	}
	progress("step", 100*(total-0.5)/total, "Powering off")
	p.mu.Lock()
	p.ctl.key = keyOff
	p.power = PowerStatus{}
	p.changedPersistentLocked()
	p.mu.Unlock()
	p.notify()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(o.ctx), powerOffTimeout)
	defer cancel()
	if err := p.m.PowerOff(ctx); err != nil {
		return fail("Shut down OS: power off failed: " + err.Error())
	}
	progress("done", 100, "The OS has shut down; power is off.")
	return nil
}

// scriptText shows a script string in a progress text: line ends are
// dropped, other control characters shown as ^X, surrounding blanks
// trimmed (the raw strings go to the debug log).
func scriptText(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\r' || r == '\n':
		case r < 0x20:
			b.WriteByte('^')
			b.WriteRune(r + '@')
		case r == 0x7f:
			b.WriteString("^?")
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// typingText is the progress text of a send step; a send of a bare line
// end (RSX and RSTS answers) is shown as pressing Return.
func typingText(send string) string {
	if t := scriptText(send); t != "" {
		return "Typing “" + t + "”"
	}
	return "Pressing Return"
}

// waitShutdown waits until check() is true (if check is set), the CPU
// stopped, the machine went down, the timeout expired or ctx is done.
func (p *Panel) waitShutdown(ctx context.Context, timeout time.Duration, since uint64, w *consoleWatch, check func() bool) waitResult {
	t := time.NewTimer(timeout)
	defer t.Stop()
	var data <-chan struct{}
	if w != nil {
		data = w.signal
	}
	for {
		if ctx.Err() != nil {
			// First: pulling the plug also stops the CPU "by request".
			return waitCancelled
		}
		if check != nil && check() {
			return waitMatched
		}
		p.mu.Lock()
		seq, stop, ev := p.stopSeq, p.lastStop, p.evSignal
		p.mu.Unlock()
		if seq > since {
			if stop.Requested {
				return waitHalted
			}
			return waitStopped
		}
		switch p.m.State() {
		case api.RunOff, api.RunError, api.RunStopping, api.RunStarting:
			return waitDown
		}
		select {
		case <-ctx.Done():
			return waitCancelled
		case <-t.C:
			return waitTimeout
		case <-ev:
		case <-data:
		}
	}
}

// consoleWatch collects console output for the shutdown script. Positions
// are offsets into the watched text: the current line of the history (so
// a prompt that is already on screen matches the first step) followed by
// all output since the watch started.
type consoleWatch struct {
	mu     sync.Mutex
	buf    []byte
	base   int // offset of buf[0] (older text was discarded)
	signal chan struct{}
	cancel func()
}

const watchKeep = 64 << 10

func watchConsole(s api.Stream) *consoleWatch {
	w := &consoleWatch{signal: make(chan struct{}, 1)}
	hist, cancel := s.SubscribeHistory(w.add)
	if i := bytes.LastIndexAny(hist, "\r\n"); i >= 0 {
		hist = hist[i+1:]
	}
	w.mu.Lock()
	// Output delivered while SubscribeHistory ran follows the history.
	w.buf = append(append([]byte(nil), hist...), w.buf...)
	w.cancel = cancel
	w.mu.Unlock()
	signal(w.signal)
	return w
}

func (w *consoleWatch) add(p []byte) {
	w.mu.Lock()
	w.buf = append(w.buf, p...)
	if over := len(w.buf) - watchKeep; over > 0 {
		w.buf = append(w.buf[:0], w.buf[over:]...)
		w.base += over
	}
	w.mu.Unlock()
	signal(w.signal)
}

// find looks for s in the text after position from and returns the
// position after the match.
func (w *consoleWatch) find(from int, s string) (int, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	i := from - w.base
	if i < 0 {
		i = 0
	}
	if i > len(w.buf) {
		return 0, false
	}
	j := bytes.Index(w.buf[i:], []byte(s))
	if j < 0 {
		return 0, false
	}
	return w.base + i + j + len(s), true
}

// end returns the position after the text seen so far.
func (w *consoleWatch) end() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.base + len(w.buf)
}

func (w *consoleWatch) close() {
	w.mu.Lock()
	cancel := w.cancel
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
