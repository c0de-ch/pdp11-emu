package panel

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
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
	readyBeforeScript  = 25 * time.Second // a boot in progress: wait for its prompt first
	consoleQuiet       = 300 * time.Millisecond
	consoleQuietMax    = 2 * time.Second
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
// step it first judges the console if the step has IfLineEnds (skipping
// the step or, with Else, failing at once), then waits (≤ timeout) for Expect
// or the first of ExpectAny in the console output since the previous step,
// then types Send (answerDelay after a match). The wait texts are literal,
// case-sensitive substrings; before anything is typed they also match the
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
	pos := w.start
	res := waitMatched
	answer := false // an Expect matched and nothing was typed since
	// A boot the panel started is still in progress: the guest discards what
	// is typed before its login prompt, so the script has to wait for it.
	if p.bootInProgress() {
		progress("step", 0, "Waiting for the system to finish booting…")
		switch r := p.waitShutdown(o.ctx, readyBeforeScript, since, w, p.m.Ready); r {
		case waitMatched:
			answer = true // the prompt has just appeared
		case waitTimeout:
		default:
			res = r
		}
	}
	for i, s := range steps {
		if res != waitMatched {
			break
		}
		pct := 100 * float64(i) / total
		if len(s.IfLineEnds) > 0 {
			if i == 0 {
				progress("step", pct, "Checking the console…")
			}
			if res = p.waitQuiet(o.ctx, since, w, stream); res != waitMatched {
				break
			}
			if !w.lineEnds(s.IfLineEnds) {
				p.log.Debug("shutdown step skipped", "system", id, "ifLineEnds", s.IfLineEnds, "console", w.lastLine())
				if s.Else != "" {
					p.log.Warn("shutdown script: the console is not in a state it can go on from", "system", id, "console", w.lastLine())
					return fail("Shut down OS: " + s.Else)
				}
				continue
			}
		}
		if texts := s.ExpectAny; s.Expect != "" || len(texts) > 0 {
			if s.Expect != "" {
				texts = []string{s.Expect}
			}
			p.log.Debug("shutdown step", "system", id, "expect", texts)
			progress("step", pct, waitingText(texts))
			timeout := defaultStepTimeout
			if s.TimeoutSec > 0 {
				timeout = time.Duration(s.TimeoutSec) * time.Second
			}
			var end int
			res = p.waitShutdown(o.ctx, timeout, since, w, func() bool {
				for _, t := range texts {
					var ok bool
					if end, ok = w.find(pos, t); ok {
						return true
					}
				}
				return false
			})
			if res != waitMatched {
				if res == waitTimeout {
					p.log.Warn("shutdown script: a prompt did not appear", "system", id, "expect", joinTexts(texts, "", " or "), "console", w.lastLine())
					if s.Else != "" {
						return fail("Shut down OS: " + s.Else)
					}
					return fail(fmt.Sprintf("Shut down OS: “%s” did not appear on the console within %s.", joinTexts(texts, "”", " or “"), timeout))
				}
				break
			}
			if s.Expect != "" {
				pos = end
			}
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
			pos = w.typed()
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

// waitingText is the progress text of a wait.
func waitingText(texts []string) string {
	if len(texts) == 1 {
		return "Waiting for “" + scriptText(texts[0]) + "”"
	}
	return "Waiting for the console…"
}

// joinTexts shows the texts of a wait, each in the form of scriptText.
func joinTexts(texts []string, closing, sep string) string {
	shown := make([]string, len(texts))
	for i, t := range texts {
		shown[i] = scriptText(t)
	}
	return strings.Join(shown, closing+sep)
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

// waitQuiet waits until the console has been silent for consoleQuiet with no
// typed input left to send, because a judgement of it holds only then: a prompt
// may be on its way (at most consoleQuietMax: a console that never stops is
// judged as it is). waitMatched: it was judged; otherwise why the wait ended.
func (p *Panel) waitQuiet(ctx context.Context, since uint64, w *consoleWatch, s api.Stream) waitResult {
	deadline := time.Now().Add(consoleQuietMax)
	for {
		left := consoleQuiet - w.silence()
		if left <= 0 && s.Queued() > 0 {
			left = consoleQuiet / 4
		}
		rest := time.Until(deadline)
		if left <= 0 || rest <= 0 {
			return waitMatched
		}
		if r := p.waitShutdown(ctx, min(left, rest), since, nil, nil); r != waitTimeout {
			return r
		}
	}
}

// consoleWatch collects console output for the shutdown script. Positions
// are offsets into the watched text: the end of the history, of which only
// the current line counts for Expect (so a prompt that is already on screen
// matches, and older text does not), followed by all output since the watch
// started.
type consoleWatch struct {
	mu      sync.Mutex
	buf     []byte
	base    int       // offset of buf[0] (older text was discarded)
	start   int       // position of the history's last line end: where the first wait starts
	lastAdd time.Time // output or typing, whichever came last
	signal  chan struct{}
	cancel  func()
}

const (
	watchKeep = 64 << 10
	tailKeep  = 1 << 10 // what lineEnds looks at
)

// consoleMessages matches lines of 2.11BSD's syslog at the end of the output
// ("Mar 30 02:31:38 pdp11 login: ROOT LOGIN ON tty00"). They are printed where
// the cursor is, usually after a prompt, which still holds: what is typed next
// goes where it would have gone.
var consoleMessages = regexp.MustCompile(`(?:[A-Z][a-z][a-z] [ 0-9][0-9] [0-9][0-9]:[0-9][0-9]:[0-9][0-9] [^ \r\n]+ [^\r\n]*[\r\n]+)+$`)

func watchConsole(s api.Stream) *consoleWatch {
	w := &consoleWatch{signal: make(chan struct{}, 1), lastAdd: time.Now()}
	hist, cancel := s.SubscribeHistory(w.add)
	if len(hist) > tailKeep {
		hist = hist[len(hist)-tailKeep:]
	}
	w.mu.Lock()
	// Output delivered while SubscribeHistory ran follows the history.
	w.buf = append(append([]byte(nil), hist...), w.buf...)
	w.start = max(bytes.LastIndexAny(consoleMessages.ReplaceAll(hist, nil), "\r\n"), 0)
	w.cancel = cancel
	w.mu.Unlock()
	signal(w.signal)
	return w
}

func (w *consoleWatch) add(p []byte) {
	w.mu.Lock()
	w.buf = append(w.buf, p...)
	w.lastAdd = time.Now()
	if over := len(w.buf) - watchKeep; over > 0 {
		w.buf = append(w.buf[:0], w.buf[over:]...)
		w.base += over
	}
	w.mu.Unlock()
	signal(w.signal)
}

// silence returns the time since the last output or typing.
func (w *consoleWatch) silence() time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return time.Since(w.lastAdd)
}

// lineEnds reports whether the console output ends in one of the texts
// (DESIGN.md §7).
func (w *consoleWatch) lineEnds(texts []string) bool {
	w.mu.Lock()
	b := w.buf
	if len(b) > tailKeep {
		b = b[len(b)-tailKeep:]
	}
	tail := consoleTail(consoleMessages.ReplaceAll(b, nil))
	w.mu.Unlock()
	for _, t := range texts {
		if tailEndsIn(tail, t) {
			return true
		}
	}
	return false
}

// consoleTail reduces console output to what a reader of the screen takes from
// it: it starts with a line end, every run of line ends is one "\n" (so blank
// lines do not count), a cursor move to an absolute position (vi, jove) or down
// a line counts as a line end, a backspace takes the character before it back,
// and fill and other control characters and terminal controls are gone.
func consoleTail(raw []byte) string {
	out := make([]byte, 1, len(raw)+1)
	out[0] = '\n'
	lineEnd := func() {
		if out[len(out)-1] != '\n' {
			out = append(out, '\n')
		}
	}
	for i := 0; i < len(raw); i++ {
		switch c := raw[i]; {
		case c == '\r' || c == '\n':
			lineEnd()
		case c == '\b':
			if out[len(out)-1] != '\n' {
				out = out[:len(out)-1]
			}
		case c == 0x1b && i+1 < len(raw) && raw[i+1] == '[':
			i += 2
			for i < len(raw) && raw[i] < 0x40 {
				i++ // parameters
			}
			if i < len(raw) && (raw[i] == 'H' || raw[i] == 'f') {
				lineEnd()
			}
		case c == 0x1b && i+1 < len(raw):
			i++
			if raw[i] == 'D' || raw[i] == 'E' || raw[i] == 'M' {
				lineEnd()
			}
		case c >= 0x20 && c != 0x7f || c == '\t':
			out = append(out, c)
		}
	}
	return string(out)
}

// tailEndsIn reports whether a consoleTail ends in text, where "*" stands for
// anything on the same line. Matching runs from the end: the last piece must
// be the very end of the tail, each piece before it the nearest occurrence.
func tailEndsIn(tail, text string) bool {
	pieces := strings.Split(text, "*")
	last := pieces[len(pieces)-1]
	if !strings.HasSuffix(tail, last) {
		return false
	}
	rest := tail[:len(tail)-len(last)]
	for i := len(pieces) - 2; i >= 0; i-- {
		j := strings.LastIndex(rest, pieces[i])
		if j < 0 || strings.Contains(rest[j+len(pieces[i]):], "\n") {
			return false
		}
		rest = rest[:j]
	}
	return true
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

// lastLine returns the end of the last line of output that has any text, for
// the log when an expected prompt does not appear.
func (w *consoleWatch) lastLine() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	b := bytes.TrimRight(w.buf, " \r\n")
	if i := bytes.LastIndexAny(b, "\r\n"); i >= 0 {
		b = b[i+1:]
	}
	if len(b) > 80 {
		b = b[len(b)-80:]
	}
	return scriptText(string(b))
}

// typed notes that the script types now: it returns the position after the
// text seen so far, and the console counts as active from here on.
func (w *consoleWatch) typed() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lastAdd = time.Now()
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
