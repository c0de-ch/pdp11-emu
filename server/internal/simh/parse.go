package simh

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"pdp11emu/server/internal/api"
)

var (
	// reStop matches SimH's stop line, e.g. "HALT instruction, PC: 001006 (HALT)"
	// (greedy reason, end anchored).
	reStop = regexp.MustCompile(`^(.*), PC: ([0-7]+)(?: \((.*)\))?$`)
	// reExamine matches a successful "examine" reply ("1000:\t000123",
	// "R00:\t000000").
	reExamine = regexp.MustCompile(`^([0-7]+|[A-Z0-9]+):\t([0-7]{6})$`)
	// reEcho matches SimH's echo of a do-file command that produced a
	// message: "<abs path>/run.ini-<n>> <cmd>".
	reEcho = regexp.MustCompile(`^(.*)-([0-9]+)> (.*)$`)
	// reSubst matches a SimH %VAR% substitution in a command line.
	reSubst = regexp.MustCompile(`%[^%]*%`)
)

// Reasons and messages with a fixed meaning.
const (
	reasonPanelHalt = "Front panel HALT"
	reasonStep      = "Step expired"
)

// addressErrors are SimH's replies to EXAMINE/DEPOSIT of a location that
// does not exist or cannot be mapped (wrapped in api.ErrAddress).
var addressErrors = []string{"Address space exceeded", "Relocation error", "Invalid argument"}

// isInfo reports SimH output that is purely informational: %SIM-INFO
// messages, the reset warning printed by RUN, and a few plain notices that
// configuration commands print on success.
func isInfo(line string) bool {
	switch {
	case strings.HasPrefix(line, "%SIM-INFO"),
		strings.HasPrefix(line, "Resetting all devices..."),
		line == "The GO and CONTINUE commands do not reset devices.",
		// "attach -m dz ..." (modem control), speed changes, CPU models
		line == "Modem control activated", line == "Modem control deactivated",
		line == "Idling disabled", line == "Throttling disabled",
		strings.HasPrefix(line, "Disabling "),
		// a failed listener is always followed by its %SIM-ERROR
		strings.HasPrefix(line, "Sockets: "):
		return true
	}
	return false
}

// meaningful returns the lines that are neither blank nor informational.
func meaningful(lines []string) []string {
	var out []string
	for _, l := range lines {
		if strings.TrimSpace(l) == "" || isInfo(l) {
			continue
		}
		out = append(out, l)
	}
	return out
}

// runResult is the outcome of a run command (boot, cont, run, go, step).
type runResult struct {
	stop     api.StopInfo
	stopLine bool     // a stop line was found
	errs     []string // without a stop line: the non-informational output
}

// parseRun interprets the output between a run command and the next prompt.
// The stop reason is the last line that matches the stop pattern; without
// one the CPU stopped silently (e.g. an EXPECT rule without "continue") and
// any other output is an error (e.g. "%SIM-ERROR: Non-existent device").
func parseRun(lines []string) runResult {
	var r runResult
	for i := len(lines) - 1; i >= 0; i-- {
		m := reStop.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		pc, err := strconv.ParseUint(m[2], 8, 32)
		if err != nil {
			continue
		}
		r.stopLine = true
		r.stop = api.StopInfo{Reason: m[1], PC: uint16(pc), Insn: m[3]}
		r.stop.Requested = r.stop.Reason == reasonPanelHalt
		return r
	}
	r.errs = meaningful(lines)
	return r
}

// parseExamine interprets the reply to "examine <loc>".
func parseExamine(cmd string, lines []string) (uint16, error) {
	rest := meaningful(lines)
	if len(rest) == 1 {
		if m := reExamine.FindStringSubmatch(rest[0]); m != nil {
			v, err := strconv.ParseUint(m[2], 8, 16)
			if err == nil {
				return uint16(v), nil
			}
		}
	}
	return 0, replyError(cmd, rest)
}

// replyError classifies a non-empty reply of a command that should have
// printed nothing (or a value).
func replyError(cmd string, rest []string) error {
	if len(rest) == 0 {
		return fmt.Errorf("%s: no reply", cmd)
	}
	text := strings.TrimPrefix(strings.Join(rest, "; "), "%SIM-ERROR: ")
	for _, e := range addressErrors {
		if strings.HasPrefix(rest[0], e) {
			return fmt.Errorf("%s: %w: %s", cmd, api.ErrAddress, text)
		}
	}
	return fmt.Errorf("%s: %s", cmd, text)
}

// checkEmpty returns nil when a command printed nothing but informational
// output (deposit, set, reset), else the classified error.
func checkEmpty(cmd string, lines []string) error {
	if rest := meaningful(lines); len(rest) > 0 {
		return replyError(cmd, rest)
	}
	return nil
}

// diagnostic is a problem reported while SimH executed a command file.
type diagnostic struct {
	Line int    // line number in the command file
	Cmd  string // the command as echoed by SimH
	Text string // the message
}

// parseDiagnostics attributes the start-up output of a command file to its
// lines: every line following SimH's echo "<file>-<n>> <cmd>" up to the next
// echo is a message about line n. Output before the first echo (banner,
// CPU model notices) belongs to no line. Messages that only report success
// (isInfo) and the output of the file's own ECHO commands are dropped.
//
// file is the base name of the command file ("run.ini"); fileLines its
// contents, used to recognise ECHO output (SimH prints it without an echo
// of its own, so it would otherwise be attributed to an earlier line).
func parseDiagnostics(lines []string, file string, fileLines []string) []diagnostic {
	type echoCmd struct {
		line int
		re   *regexp.Regexp
	}
	var echoes []echoCmd
	for i, l := range fileLines {
		t := strings.TrimSpace(l)
		w := strings.Fields(t)
		if len(w) == 0 || !strings.EqualFold(w[0], "echo") {
			continue
		}
		text := strings.TrimSpace(t[len(w[0]):])
		// SimH substitutes %VAR% before executing; accept any value there.
		pat := reSubst.ReplaceAllString(regexp.QuoteMeta(text), ".*")
		echoes = append(echoes, echoCmd{line: i + 1, re: regexp.MustCompile("^" + pat + "$")})
	}

	var out []diagnostic
	cur := 0 // command-file line the output belongs to (0 = none)
	cmd := ""
	for _, l := range lines {
		if m := reEcho.FindStringSubmatch(l); m != nil && (m[1] == file || strings.HasSuffix(m[1], "/"+file)) {
			n, err := strconv.Atoi(m[2])
			if err == nil {
				cur, cmd = n, m[3]
				continue
			}
		}
		if cur == 0 || strings.TrimSpace(l) == "" || isInfo(l) {
			continue
		}
		matched := false
		for _, e := range echoes {
			if e.line > cur && e.re.MatchString(l) {
				cur, cmd, matched = e.line, "echo", true
				break
			}
		}
		if matched {
			continue
		}
		out = append(out, diagnostic{Line: cur, Cmd: cmd, Text: strings.TrimPrefix(l, "%SIM-ERROR: ")})
	}
	return out
}
