//go:build matrix

// The clean shutdown from every console state through pdp11d itself, and the
// boot repair of a damaged 2.11BSD disk (opt-in, like the boot matrix; same
// flags):
//
//	go test -tags matrix -run 'TestShutdownStates|TestFsckRepair' -timeout 3h -v ./server/cmd/pdp11d/ -args \
//	    -cache DIR[:DIR...] [-work DIR] [-shm DIR] [-systems id,...] [-listen-port P] [-port-base Q]
//
// For every catalog system with a shutdown script and every console state it
// has (docs/DESIGN.md §7), it brings the console into that state and stops the
// machine twice: with shutdownOS and with SIGTERM under PDP11_SHUTDOWN_ON_STOP.
// A state the script recovers from must end in a clean stop. A state it refuses
// must fail at once and type nothing: shutdownOS gets its warn toast, the
// machine stays on, and after the state is left by hand (which shows that the
// program kept its work) shutdownOS succeeds; SIGTERM logs the reason at warn
// and still powers off.
package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"pdp11emu/server/internal/catalog"
)

// Fail at once: instead of the 30 s of a prompt that never comes, even on a
// loaded host; a refused SIGTERM only adds the power-off to that.
const (
	refusedWithin     = 10 * time.Second
	refusedStopWithin = 30 * time.Second
)

// consoleState is one state a user can leave the console in.
type consoleState struct {
	name  string
	setup []catalog.Step // typed after the ready prompt; the last Expect shows the state was reached
	// refused: the script must not go on, and its failure text holds this.
	refused string
	// typesNothing: the refusal comes before the script types anything.
	typesNothing bool
	// leave: typed by hand after the refusal to leave the state; its Expects
	// show that nothing was disturbed. Then shutdownOS must succeed.
	leave []catalog.Step
	// once: the state changes the disks for good (a new password), so it comes
	// last and is only tried with shutdownOS.
	once bool
}

func sends(s ...string) []catalog.Step {
	out := make([]catalog.Step, len(s))
	for i, x := range s {
		out[i] = catalog.Step{Send: x}
	}
	return out
}

func at(expect, send string) catalog.Step { return catalog.Step{Expect: expect, Send: send} }

func join(parts ...[]catalog.Step) []catalog.Step {
	var out []catalog.Step
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

const (
	notAtPrompt = "so nothing was typed"
	nowFile     = "/tmp/states.txt"
)

// shutdownStates lists the states of a system: the prepare steps run once, at
// the first boot on the fresh data directory.
func shutdownStates(id string) (prepare []catalog.Step, states []consoleState) {
	switch id {
	case "bsd211", "bsd211-pl499":
		prompt := "] "
		if id == "bsd211-pl499" {
			prompt = "> "
		}
		root := sends("root\r")
		user := sends("user\r")
		// pl499's user prompt is "> ", which a shell also prints while it waits for
		// more input (a here-document): the script must not send Ctrl+D there
		userShell := consoleState{name: "another user's shell", setup: join(user, []catalog.Step{{Expect: prompt}})}
		suspended := "suspended jobs"
		leaveSuspended := []catalog.Step{at("", "fg\r"), at("", ":q!\r"), {Expect: prompt}}
		if id == "bsd211-pl499" {
			userShell.refused, userShell.typesNothing = notAtPrompt, true
			userShell.leave = []catalog.Step{at("", "\x04"), {Expect: "login: "}}
			suspended = notAtPrompt
			// back at "> ", which the script refuses as well: log out too
			leaveSuspended = append(leaveSuspended, at("", "\x04"), catalog.Step{Expect: "login: "})
		}
		return nil, []consoleState{
			{name: "login prompt"},
			{name: "half-typed login name", setup: []catalog.Step{{Send: "ro"}, {Expect: "ro"}}},
			{name: "login name erased again", setup: []catalog.Step{{Send: "xy\x7f\x7f"}, {Expect: "\b \b\b \b"}}},
			{name: "root shell", setup: join(root, []catalog.Step{{Expect: "# "}})},
			{name: "root shell after leaving vi", setup: join(root, []catalog.Step{at("# ", "vi "+nowFile+"\r"), at("[New file]", ":q!\r"), {Expect: "# "}})},
			{name: "root shell after leaving jove", setup: join(root, []catalog.Step{at("# ", "jove "+nowFile+"\r"), at("JOVE", "\x18\x03"), {Expect: "# "}})},
			{name: "root shell with a syslog line after it",
				setup: join(root, []catalog.Step{at("# ", "(sleep 2; logger -p auth.notice states test) &\r"), {Expect: "# "}, {Expect: "states test"}})},
			{name: "half-typed command with a syslog line after it", refused: notAtPrompt, typesNothing: true,
				setup: join(root, []catalog.Step{at("# ", "(sleep 2; logger -p auth.notice states test) &\r"), at("# ", "ls /us"), {Expect: "/us"}, {Expect: "states test"}}),
				leave: []catalog.Step{at("", "\x15\r"), {Expect: "# "}}},
			userShell,
			{name: "another user with a suspended job", refused: suspended, typesNothing: id == "bsd211-pl499",
				setup: join(user, []catalog.Step{at(prompt, "vi "+nowFile+"\r"), at("[New file]", "\x1a"), {Expect: "Suspended"}, {Expect: prompt}}),
				leave: leaveSuspended},
			{name: "half-typed command", refused: notAtPrompt, typesNothing: true,
				setup: join(root, []catalog.Step{at("# ", "ls /us"), {Expect: "/us"}}),
				leave: []catalog.Step{at("", "\x15\r"), {Expect: "# "}}},
			{name: "inside vi", refused: notAtPrompt, typesNothing: true,
				setup: join(root, []catalog.Step{at("# ", "vi "+nowFile+"\r"), at("[New file]", "ihello from a user"), {Expect: "a user"}}),
				leave: []catalog.Step{at("", "\x1b:wq\r"), at("# ", "cat "+nowFile+"\r"), {Expect: "hello from a user"}, {Expect: "# "}}},
			{name: "a running program", refused: notAtPrompt, typesNothing: true,
				setup: join(root, []catalog.Step{at("# ", "sleep 15; echo finished\r"), {Expect: "\n"}}),
				leave: []catalog.Step{{Expect: "finished"}, {Expect: "# "}}},
			{name: "a password prompt", refused: notAtPrompt, typesNothing: true,
				setup: []catalog.Step{{Send: "daemon\r"}, {Expect: "Password:"}},
				leave: []catalog.Step{at("", "\r"), {Expect: "Login incorrect"}, {Expect: "login: "}}},
			{name: "root has a password", refused: "asks for a password", once: true,
				setup: join(root, []catalog.Step{at("# ", "passwd\r"), at("New password:", "secret1\r"), at("Retype new password:", "secret1\r"), at("# ", "\x04"), {Expect: "login: "}}),
				leave: []catalog.Step{at("", "secret1\r"), {Expect: "# "}}},
		}
	case "unixv7":
		root := []catalog.Step{{Send: "root\r"}, at("Password:", "root\r")}
		return nil, []consoleState{
			{name: "login prompt"},
			{name: "half-typed login name", setup: []catalog.Step{{Send: "ro"}, {Expect: "ro"}}},
			{name: "after a failed login", setup: []catalog.Step{{Send: "nosuch\r"}, at("Password:", "\r"), {Expect: "Login incorrect"}, {Expect: "login: "}}},
			{name: "root shell", setup: join(root, []catalog.Step{{Expect: "# "}})},
			{name: "another user's shell", setup: []catalog.Step{{Send: "dmr\r"}, {Expect: "$ "}}},
			{name: "half-typed command", refused: notAtPrompt, typesNothing: true,
				setup: join(root, []catalog.Step{at("# ", "ls /us"), {Expect: "/us"}}),
				leave: []catalog.Step{at("", "@\r"), {Expect: "# "}}},
			{name: "inside ed", refused: notAtPrompt, typesNothing: true,
				setup: join(root, []catalog.Step{at("# ", "rm "+nowFile+"\r"), at("# ", "ed "+nowFile+"\r"), at("?", "a\rhello from a user\r"), {Expect: "a user"}}),
				leave: []catalog.Step{at("", ".\rw\rq\r"), at("# ", "cat "+nowFile+"\r"), {Expect: "hello from a user"}, {Expect: "# "}}},
			{name: "a password prompt", refused: notAtPrompt, typesNothing: true,
				setup: []catalog.Step{{Send: "root\r"}, {Expect: "Password:"}},
				leave: []catalog.Step{at("", "root\r"), {Expect: "# "}}},
			{name: "the root password changed", refused: "root password", once: true,
				setup: join(root, []catalog.Step{at("# ", "passwd\r"), at("New password:", "secret1\r"), at("Retype new password:", "secret1\r"), at("# ", "\x04"), {Expect: "login: "}}),
				leave: []catalog.Step{at("", "root\r"), at("Password:", "secret1\r"), {Expect: "# "}}},
		}
	case "unixv6", "unixv5":
		other := "ken"
		if id == "unixv5" {
			other = "bin"
		}
		root := sends("root\r")
		return nil, []consoleState{
			{name: "login prompt"},
			{name: "half-typed login name", setup: []catalog.Step{{Send: "ro"}, {Expect: "ro"}}},
			{name: "after a failed login", setup: []catalog.Step{{Send: "nosuch\r"}, {Expect: "Login incorrect."}, {Expect: "Name: "}}},
			{name: "half-typed name after a failed login", setup: []catalog.Step{{Send: "nosuch\r"}, at("Name: ", "ro"), {Expect: "ro"}}},
			{name: "root shell", setup: join(root, []catalog.Step{{Expect: "# "}})},
			{name: "another user's shell", setup: []catalog.Step{{Send: other + "\r"}, {Expect: "% "}}},
			{name: "half-typed command", refused: notAtPrompt, typesNothing: true,
				setup: join(root, []catalog.Step{at("# ", "ls /us"), {Expect: "/us"}}),
				leave: []catalog.Step{at("", "@\r"), {Expect: "# "}}},
			{name: "inside ed", refused: notAtPrompt, typesNothing: true,
				setup: join(root, []catalog.Step{at("# ", "rm "+nowFile+"\r"), at("# ", "ed "+nowFile+"\r"), at("?", "a\rhello from a user\r"), {Expect: "a user"}}),
				leave: []catalog.Step{at("", ".\rw\rq\r"), at("# ", "cat "+nowFile+"\r"), {Expect: "hello from a user"}, {Expect: "# "}}},
		}
	case "rsx11mp":
		return nil, []consoleState{
			{name: "MCR prompt", setup: []catalog.Step{{Expect: "\n>"}}},
			{name: "half-typed HELLO", setup: []catalog.Step{at("\n>", "HELLO US"), {Expect: "HELLO US"}}},
			{name: "logged out", setup: []catalog.Step{at("\n>", "BYE\r"), {Expect: "logged off"}, {Expect: "\n>"}}},
			{name: "another user, not privileged", setup: []catalog.Step{at("\n>", "BYE\r"), {Expect: "logged off"}, at("\n>", "HELLO USER\r"), at("Password:", "USER\r"), {Expect: "<EOF>"}, {Expect: "\n>"}}},
			{name: "half-typed command", refused: notAtPrompt, typesNothing: true,
				setup: []catalog.Step{at("\n>", "TI"), {Expect: "TI"}},
				leave: []catalog.Step{at("", "\x15"), {Expect: "\n>"}}},
			{name: "inside PIP", refused: notAtPrompt, typesNothing: true,
				setup: []catalog.Step{at("\n>", "PIP\r"), {Expect: "PIP>"}},
				leave: []catalog.Step{at("", "\x1a"), {Expect: "\n>"}}},
		}
	case "rsts":
		login := func(account, password string) []catalog.Step {
			return []catalog.Step{{Send: "HELLO " + account + "\r"}, at("Password:", password+"\r")}
		}
		system := join(login("1,2", "SYSTEM"), []catalog.Step{at("Job number to attach to? ", "\r"), {Expect: "Ready"}})
		account := join(system, []catalog.Step{at("", "RUN $REACT\r"), at("Function? ", "E\r"), at("Proj,Prog? ", "100,2\r"),
			at("Disk:Password? ", "SY:USER\r"), at("Quota? ", "100\r"), at("Cluster Size? ", "\r"), at("Account Name? ", "USER\r"),
			at("Proj,Prog? ", "\x03"), {Expect: "Ready"}})
		return account, []consoleState{
			{name: "logged out after the boot"},
			{name: "half-typed HELLO", setup: []catalog.Step{{Send: "HELLO 1"}, {Expect: "HELLO 1"}}},
			{name: "Ready", setup: system},
			{name: "logged out with BYE/F", setup: join(system, []catalog.Step{at("", "BYE/F\r"), {Expect: "BYE/F"}})},
			{name: "logged out with BYE", setup: join(system, []catalog.Step{at("", "BYE\r"), at("Confirm: ", "Y\r"), {Expect: "logged off"}, {Expect: "Good "}})},
			{name: "another user", setup: join(login("100,2", "USER"), []catalog.Step{{Expect: "Ready"}})},
			{name: "half-typed command", refused: notAtPrompt, typesNothing: true,
				setup: join(system, []catalog.Step{at("", "PRI"), {Expect: "PRI"}}),
				leave: []catalog.Step{at("", "\x15"), at("", "PRINT 6*7\r"), {Expect: " 42 "}, {Expect: "Ready"}}},
			{name: "BASIC lines typed", refused: notAtPrompt, typesNothing: true,
				setup: join(system, []catalog.Step{at("", "10 PRINT 6*7\r"), {Expect: "PRINT 6*7"}}),
				leave: []catalog.Step{at("", "RUN\r"), {Expect: " 42 "}, {Expect: "Ready"}}},
			{name: "inside TECO", refused: notAtPrompt, typesNothing: true,
				setup: join(system, []catalog.Step{at("", "TECO\r"), {Expect: "*"}}),
				leave: []catalog.Step{at("", "EX\x1b\x1b"), {Expect: "Ready"}}},
			{name: "a password prompt", refused: notAtPrompt, typesNothing: true,
				setup: []catalog.Step{{Send: "HELLO 1,2\r"}, {Expect: "Password:"}},
				leave: []catalog.Step{at("", "SYSTEM\r"), at("Job number to attach to? ", "\r"), {Expect: "Ready"}}},
		}
	}
	return nil, nil
}

func TestShutdownStates(t *testing.T) {
	if *mxCache == "" {
		t.Skip("needs -cache DIR or PDP11_TEST_CACHE (archives named by their sha256)")
	}
	simhBin, _ := filepath.Abs(*mxSIMH)
	if _, err := os.Stat(simhBin); err != nil {
		t.Skipf("patched simulator: %v", err)
	}
	cat, err := loadCatalog("")
	if err != nil {
		t.Fatal(err)
	}
	only := map[string]bool{}
	for _, id := range strings.Split(*mxSystems, ",") {
		if id != "" {
			only[id] = true
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
	if err := os.MkdirAll(shmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, sys := range cat.Systems {
		if len(sys.ShutdownScript) == 0 || (len(only) > 0 && !only[sys.ID]) {
			continue
		}
		prepare, states := shutdownStates(sys.ID)
		if len(states) == 0 {
			t.Errorf("%s has a shutdown script but no console states to try", sys.ID)
			continue
		}
		t.Run(sys.ID, func(t *testing.T) {
			r := &stateRun{sys: sys, simh: simhBin, dir: filepath.Join(work, "states-"+sys.ID), shm: shmDir}
			if err := os.RemoveAll(r.dir); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(r.dir, "data"), 0o755); err != nil {
				t.Fatal(err)
			}
			// The disks take gigabytes of memory on a RAM disk: keep only the logs.
			defer os.RemoveAll(filepath.Join(r.dir, "data"))
			if prepare != nil {
				r.prepare(t, prepare)
			}
			for _, st := range states {
				t.Run(st.name+"/shutdownOS", func(t *testing.T) { r.shutdownOS(t, st) })
				if !st.once {
					t.Run(st.name+"/SIGTERM", func(t *testing.T) { r.sigterm(t, st) })
				}
			}
		})
	}
}

// stateRun is one system's data directory, used for all its states in turn.
type stateRun struct {
	sys         *catalog.System
	simh        string
	dir, shm    string
	cleanBefore bool // the previous start stopped cleanly
	noWait      bool // boot returns without waiting for the ready prompt
}

// boot starts pdp11d on the system's data directory and waits for the ready
// prompt; it returns the console position after it.
func (r *stateRun) boot(t *testing.T, name string, extra map[string]string) (*daemon, *viewer, int) {
	t.Helper()
	n := startNo
	startNo++
	listen := fmt.Sprintf("127.0.0.1:%d", *mxListen+n%20)
	base := *mxPort + 10*(n%10)
	shm := filepath.Join(r.shm, fmt.Sprintf("panel-%s-%d", r.sys.ID, n))
	t.Cleanup(func() { os.Remove(shm) })
	env := map[string]string{
		"PATH": "/usr/bin:/bin", "HOME": r.dir, childEnv: "1",
		"PDP11_DATA": filepath.Join(r.dir, "data"), "PDP11_CACHE": *mxCache, "PDP11_SIMH": r.simh,
		"PDP11_SYSTEM": r.sys.ID, "PDP11_ACCEPT_LICENSES": r.sys.ID, "PDP11_LISTEN": listen,
		"PDP11_PORT_BASE": strconv.Itoa(base), "PDP11_SHM": shm, "PDP11_LOG": "debug",
	}
	for k, v := range extra {
		env[k] = v
	}
	stem := filepath.Join(r.dir, fmt.Sprintf("%03d-%s", n, strings.NewReplacer(" ", "-", "/", "-", "'", "").Replace(name)))
	logPath := stem + ".log"
	d, err := startDaemon(env, logPath)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(d.kill)
	v, err := dialViewer(listen, 15*time.Second)
	if err != nil {
		t.Fatalf("WebSocket: %v", err)
	}
	t.Cleanup(func() {
		v.close()
		_ = os.WriteFile(stem+".console", v.consoleCopy(), 0o644)
	})
	st, ok := v.waitState(10*time.Second, func(stateMsg) bool { return true })
	if !ok {
		t.Fatalf("no state: %s", v.lastState())
	}
	if st.Key == "off" {
		v.send(map[string]any{"type": "key", "pos": "power", "req": "states"})
	}
	if r.noWait {
		return d, v, 0
	}
	limit := time.Duration(r.sys.BootTimeoutSec+60) * time.Second
	st, ok = v.waitState(limit, func(s stateMsg) bool { return s.Ready || s.Run == "error" })
	if !ok || !st.Ready {
		t.Fatalf("not ready within %s: %s", limit, v.lastState())
	}
	con := v.consoleCopy()
	if r.cleanBefore && strings.HasPrefix(r.sys.ID, "bsd211") && bytes.Contains(con, []byte("** Phase")) {
		t.Errorf("the boot after a clean stop checked the file systems")
	}
	r.cleanBefore = false
	i := v.find(0, []byte(r.sys.ReadyPattern))
	if i < 0 {
		t.Fatalf("state.ready without %q on the console", r.sys.ReadyPattern)
	}
	return d, v, i + len(r.sys.ReadyPattern)
}

// cleanStop sends shutdownOS and requires a clean end: run off, key OFF,
// progress done, no warning, and SIGTERM ends pdp11d with status 0.
func (r *stateRun) cleanStop(t *testing.T, d *daemon, v *viewer) {
	t.Helper()
	before := len(v.toastsWith(""))
	budget := shutdownBudget(r.sys)
	s0 := time.Now()
	v.send(map[string]any{"type": "shutdownOS", "req": "states"})
	if _, ok := v.waitState(budget, func(s stateMsg) bool { return s.Run == "off" && s.Power == "off" && s.Key == "off" }); !ok {
		t.Fatalf("not off with the key at OFF within %s: %s; toasts %q; console tail %q", budget, v.lastState(), v.toastsWith(""), tail(v))
	}
	if !v.progressDone() {
		t.Errorf("no progress \"done\"")
	}
	if toasts := v.toastsWith(""); len(toasts) > before {
		t.Errorf("toasts %q", toasts[before:])
	}
	t.Logf("clean stop in %.1f s", time.Since(s0).Seconds())
	if code, _ := d.stop(30 * time.Second); code != 0 {
		t.Errorf("pdp11d exit status %d after SIGTERM", code)
	}
	r.cleanBefore = true
}

// checkConsole fails on the marks of a script typed into the wrong state, in
// the output from position from on.
func checkConsole(t *testing.T, v *viewer, from int) {
	t.Helper()
	con := v.consoleCopy()[from:]
	for _, bad := range []string{"root: not found", "Login incorrect", "Sorry"} {
		if bytes.Contains(con, []byte(bad)) {
			t.Errorf("the console shows %q: %q", bad, con)
		}
	}
}

func tail(v *viewer) []byte {
	con := v.consoleCopy()
	return con[max(0, len(con)-400):]
}

func (r *stateRun) prepare(t *testing.T, steps []catalog.Step) {
	d, v, mark := r.boot(t, "prepare", nil)
	if err := v.steps(mark, steps); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	r.cleanStop(t, d, v)
}

// shutdownOS brings the console into the state and uses Shut down OS.
func (r *stateRun) shutdownOS(t *testing.T, st consoleState) {
	d, v, mark := r.boot(t, st.name+"-shutdownOS", nil)
	if err := v.steps(mark, st.setup); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if st.refused == "" {
		from := v.size()
		r.cleanStop(t, d, v)
		checkConsole(t, v, from)
		return
	}
	time.Sleep(time.Second) // the end of the setup's echo
	size := v.size()
	before := len(v.toastsWith(""))
	s0 := time.Now()
	v.send(map[string]any{"type": "shutdownOS", "req": "states"})
	var toast string
	for {
		if toasts := v.toastsWith(""); len(toasts) > before {
			toast = toasts[before]
			break
		}
		if time.Since(s0) > refusedWithin {
			t.Fatalf("no toast within %s: %s; console tail %q", refusedWithin, v.lastState(), tail(v))
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Logf("refused in %.1f s: %s", time.Since(s0).Seconds(), toast)
	if !strings.Contains(toast, st.refused) || !strings.Contains(toast, "Pull the plug") {
		t.Errorf("toast %q, want %q", toast, st.refused)
	}
	if cur, _ := v.waitState(time.Second, func(s stateMsg) bool { return s.Run == "running" && s.Key == "power" }); cur.Run != "running" || cur.Key != "power" {
		t.Errorf("after the refusal: %s", v.lastState())
	}
	time.Sleep(500 * time.Millisecond)
	if st.typesNothing && v.size() != size {
		t.Errorf("the script typed something: %q", v.consoleCopy()[size:])
	}
	// The state is left by hand, and the machine shuts down cleanly from there.
	if err := v.steps(v.size(), st.leave); err != nil {
		t.Fatalf("leaving the state: %v", err)
	}
	time.Sleep(time.Second)
	r.cleanStop(t, d, v)
}

// sigterm brings the console into the state and stops pdp11d with
// PDP11_SHUTDOWN_ON_STOP.
func (r *stateRun) sigterm(t *testing.T, st consoleState) {
	d, v, mark := r.boot(t, st.name+"-SIGTERM", map[string]string{"PDP11_SHUTDOWN_ON_STOP": "1"})
	if err := v.steps(mark, st.setup); err != nil {
		t.Fatalf("setup: %v", err)
	}
	logPath := d.logPath
	code, took := d.stop(90 * time.Second)
	if code != 0 {
		t.Fatalf("pdp11d exit status %d after SIGTERM", code)
	}
	logs, _ := os.ReadFile(logPath)
	if !bytes.Contains(logs, []byte("shutting the guest OS down")) || !bytes.Contains(logs, []byte("powered off")) {
		t.Errorf("the log misses the stop:\n%s", logs)
	}
	warned := bytes.Contains(logs, []byte("guest OS shutdown incomplete"))
	if st.refused == "" {
		if warned {
			t.Errorf("the guest was not shut down cleanly:\n%s", logTail(logs))
		}
		t.Logf("clean stop in %.1f s", took.Seconds())
		r.cleanBefore = true
		return
	}
	if !warned || !bytes.Contains(logs, []byte(st.refused)) {
		t.Errorf("no warning that says %q:\n%s", st.refused, logTail(logs))
	}
	if took > refusedStopWithin {
		t.Errorf("the refused stop took %s", took)
	}
	t.Logf("refused, powered off in %.1f s", took.Seconds())
}

func logTail(logs []byte) []byte {
	return logs[max(0, len(logs)-1500):]
}

// TestFsckRepair: the dialog.ini rule that answers "Automatic reboot failed" on
// 2.11BSD. A disk that fsck can repair comes up by itself; on one it cannot
// repair the console says so and stays at a root shell, instead of rebooting
// for ever, and Shut down OS still works from there.
func TestFsckRepair(t *testing.T) {
	if *mxCache == "" {
		t.Skip("needs -cache DIR or PDP11_TEST_CACHE (archives named by their sha256)")
	}
	simhBin, _ := filepath.Abs(*mxSIMH)
	if _, err := os.Stat(simhBin); err != nil {
		t.Skipf("patched simulator: %v", err)
	}
	cat, err := loadCatalog("")
	if err != nil {
		t.Fatal(err)
	}
	work := *mxWork
	if work == "" {
		work = t.TempDir()
	}
	shmDir := *mxSHM
	if shmDir == "" {
		shmDir = work
	}
	if err := os.MkdirAll(shmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The last partition of each disk: it is checked last, and neither the root
	// file system nor /sbin is on it. Sector numbers from "disklabel ra0".
	for id, partition := range map[string]int64{"bsd211": 851804, "bsd211-pl499": 32768} {
		sys, ok := cat.Get(id)
		if !ok {
			t.Fatalf("%s is not in the catalog", id)
		}
		if *mxSystems != "" && !strings.Contains(","+*mxSystems+",", ","+id+",") {
			continue
		}
		t.Run(id, func(t *testing.T) {
			r := &stateRun{sys: sys, simh: simhBin, dir: filepath.Join(work, "fsck-"+id), shm: shmDir}
			if err := os.RemoveAll(r.dir); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(r.dir, "data"), 0o755); err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(filepath.Join(r.dir, "data"))
			image := filepath.Join(r.dir, "data", "systems", id, sys.Artifacts[0].Files[0].Path)
			offset := partition * 512

			t.Run("a repairable inode", func(t *testing.T) { r.fsckRepairable(t, image, offset) })
			t.Run("a destroyed superblock", func(t *testing.T) { r.fsckUnrepairable(t, image, offset) })
		})
	}
}

// fsckGaveUp is the output of the dialog.ini rule, at the start of a line: the
// command line it types also holds these words.
const fsckGaveUp = "\nfsck cannot repair this disk"

// patchImage overwrites part of a disk image.
func patchImage(t *testing.T, image string, at int64, data []byte) {
	t.Helper()
	f, err := os.OpenFile(image, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteAt(data, at); err != nil {
		t.Fatal(err)
	}
}

// fsckRepairable pulls the plug, which leaves the file systems to be checked,
// and gives an inode of the partition an impossible mode, which "fsck -p" may
// not repair and "fsck -y" clears.
func (r *stateRun) fsckRepairable(t *testing.T, image string, partition int64) {
	d, v, mark := r.boot(t, "repairable", nil)
	if err := v.steps(mark, []catalog.Step{{Send: "root\r"}, at("# ", "sync\r"), {Expect: "# "}}); err != nil {
		t.Fatal(err)
	}
	v.send(map[string]any{"type": "key", "pos": "off", "req": "fsck"})
	if _, ok := v.waitState(30*time.Second, func(s stateMsg) bool { return s.Run == "off" }); !ok {
		t.Fatalf("not off: %s", v.lastState())
	}
	const inodeList, inodeSize = 2 * 1024, 64
	patchImage(t, image, partition+inodeList+9*inodeSize, []byte{0xff, 0xf1, 1, 0}) // inode 10: mode 170777, one link
	size := v.size()
	s0 := time.Now()
	v.send(map[string]any{"type": "key", "pos": "power", "req": "fsck"})
	limit := time.Duration(r.sys.BootTimeoutSec+60) * time.Second
	if _, ok := v.waitState(limit, func(s stateMsg) bool { return s.Ready || s.Run == "error" }); !ok {
		t.Fatalf("not ready within %s: %s; console tail %q", limit, v.lastState(), tail(v))
	}
	con := v.consoleCopy()[size:]
	for _, want := range []string{"UNEXPECTED INCONSISTENCY", "Automatic reboot failed", "FILE SYSTEM WAS MODIFIED", "login: "} {
		if !bytes.Contains(con, []byte(want)) {
			t.Errorf("the console does not show %q: %q", want, con)
		}
	}
	for _, bad := range []string{"NEED SCRATCH FILE", fsckGaveUp} {
		if bytes.Contains(con, []byte(bad)) {
			t.Errorf("the console shows %q: %q", bad, con)
		}
	}
	t.Logf("repaired and up in %.1f s", time.Since(s0).Seconds())
	r.cleanStop(t, d, v)
}

// fsckUnrepairable destroys the superblock of the partition: fsck says "Size
// check" and goes on, every time.
func (r *stateRun) fsckUnrepairable(t *testing.T, image string, partition int64) {
	const superblock = 1024
	patchImage(t, image, partition+superblock, bytes.Repeat([]byte{0xff}, 1024))
	d, v, _ := r.bootNoReady(t, "unrepairable")
	s0 := time.Now()
	for !bytes.Contains(v.consoleCopy(), []byte(fsckGaveUp)) {
		if time.Since(s0) > 4*time.Minute {
			t.Fatalf("the console does not say it: %s; tail %q", v.lastState(), tail(v))
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("gave up after %.1f s", time.Since(s0).Seconds())
	// It stays at the shell; a boot would show within this time.
	time.Sleep(45 * time.Second)
	con := v.consoleCopy()
	if n := bytes.Count(con, []byte("Automatic reboot failed")); n != 1 {
		t.Errorf("%d rounds: %q", n, tail(v))
	}
	if !bytes.HasSuffix(con, []byte("# ")) {
		t.Errorf("not at a root shell: %q", tail(v))
	}
	if st, _ := v.waitState(time.Second, func(stateMsg) bool { return true }); st.Ready || st.Run != "running" {
		t.Errorf("state %s", v.lastState())
	}
	for len(v.toastsWith("has not reached its prompt")) == 0 {
		if time.Since(s0) > time.Duration(r.sys.BootTimeoutSec+60)*time.Second {
			t.Fatalf("no boot-timeout warning: %q", v.toastsWith(""))
		}
		time.Sleep(100 * time.Millisecond)
	}
	r.cleanStop(t, d, v)
}

// bootNoReady starts pdp11d and turns the key if needed, without waiting for
// the ready prompt.
func (r *stateRun) bootNoReady(t *testing.T, name string) (*daemon, *viewer, int) {
	t.Helper()
	r.noWait = true
	defer func() { r.noWait = false }()
	return r.boot(t, name, nil)
}
