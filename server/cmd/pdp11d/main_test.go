package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestConfig(t *testing.T) {
	tests := []struct {
		name  string
		env   map[string]string
		args  []string
		check func(t *testing.T, c *config)
		err   string
	}{
		{name: "defaults", check: func(t *testing.T, c *config) {
			want := config{Listen: ":8080", Data: defaultDataDir(), SIMH: "/opt/pdp11/bin/pdp11", Cache: "/opt/pdp11/cache",
				FrameAncestors: "'self'", Autostart: true, PortBase: 23400, SHM: "/dev/shm/pdp11d-panel", FrameHz: 60, LogLevel: slog.LevelInfo}
			if !reflect.DeepEqual(*c, want) {
				t.Fatalf("got %+v\nwant %+v", *c, want)
			}
		}},
		{name: "environment", env: map[string]string{
			"PDP11_LISTEN": "127.0.0.1:9000", "PDP11_DATA": "/srv/pdp11", "PDP11_AUTOSTART": "false",
			"PDP11_ACCEPT_LICENSES": "rt11, rsts,,", "PDP11_ALLOW_ORIGINS": "pdp11.example.org",
			"PDP11_HEALTH_PUBLIC": "yes", "PDP11_SHUTDOWN_ON_STOP": "1", "PDP11_FRAME_HZ": "120",
			"PDP11_LOG": "debug", "PDP11_TTY_TELNET": ":4000", "PDP11_PASSWORD": "secret", "PDP11_CACHE": "/a:/b",
			"PDP11_HOSTS": "pdp11.lan, *.example.org", "PDP11_FRAME_ANCESTORS": "'self' https://dash.example.org",
			"PDP11_TRUSTED_PROXIES": "10.0.0.1,192.168.0.0/16",
		}, check: func(t *testing.T, c *config) {
			if c.Listen != "127.0.0.1:9000" || c.Data != "/srv/pdp11" || c.Autostart || !c.HealthPublic || !c.ShutdownOnStop ||
				c.FrameHz != 120 || c.LogLevel != slog.LevelDebug || c.TTYTelnet != ":4000" || c.Password != "secret" ||
				!reflect.DeepEqual(c.AcceptLicenses, []string{"rt11", "rsts"}) || !reflect.DeepEqual(c.AllowOrigins, []string{"pdp11.example.org"}) ||
				!reflect.DeepEqual(c.cacheDirs(), []string{"/a", "/b"}) || !reflect.DeepEqual(c.Hosts, []string{"pdp11.lan", "*.example.org"}) ||
				c.FrameAncestors != "'self' https://dash.example.org" || !reflect.DeepEqual(c.TrustedProxies, []string{"10.0.0.1", "192.168.0.0/16"}) {
				t.Fatalf("%+v", *c)
			}
		}},
		{name: "flags override the environment", env: map[string]string{"PDP11_LISTEN": ":1", "PDP11_PORT_BASE": "5000"},
			args: []string{"--listen", ":2", "-autostart=false", "--port-base", "6000", "--log", "warn",
				"--hosts", "a.lan", "--frame-ancestors", "'none'", "--trusted-proxies", "::1"},
			check: func(t *testing.T, c *config) {
				if c.Listen != ":2" || c.Autostart || c.PortBase != 6000 || c.LogLevel != slog.LevelWarn ||
					!reflect.DeepEqual(c.Hosts, []string{"a.lan"}) || c.FrameAncestors != "'none'" || !reflect.DeepEqual(c.TrustedProxies, []string{"::1"}) {
					t.Fatalf("%+v", *c)
				}
			}},
		// compose.yaml passes the settings that .env leaves out as empty strings
		{name: "empty web settings mean the defaults", env: map[string]string{
			"PDP11_PASSWORD": "", "PDP11_HOSTS": "", "PDP11_TRUSTED_PROXIES": "", "PDP11_FRAME_ANCESTORS": "",
		}, check: func(t *testing.T, c *config) {
			if c.Password != "" || c.Hosts != nil || c.TrustedProxies != nil || c.FrameAncestors != "'self'" {
				t.Fatalf("%+v", *c)
			}
		}},
		{name: "lists without entries", env: map[string]string{"PDP11_HOSTS": " , ", "PDP11_TRUSTED_PROXIES": ","},
			check: func(t *testing.T, c *config) {
				if c.Hosts != nil || c.TrustedProxies != nil {
					t.Fatalf("%+v", *c)
				}
			}},
		{name: "bad frame rate", env: map[string]string{"PDP11_FRAME_HZ": "200"}, err: "PDP11_FRAME_HZ"},
		{name: "bad boolean", env: map[string]string{"PDP11_AUTOSTART": "maybe"}, err: "PDP11_AUTOSTART"},
		{name: "bad port", args: []string{"--port-base", "70000"}, err: "--port-base"},
		{name: "bad log level", env: map[string]string{"PDP11_LOG": "chatty"}, err: "PDP11_LOG"},
		{name: "bad telnet listener", env: map[string]string{"PDP11_TTY_TELNET": "0.0.0.0:x"}, err: "PDP11_TTY_TELNET"},
		{name: "telnet listener with a blank", env: map[string]string{"PDP11_TTY_TELNET": ":40 00"}, err: "PDP11_TTY_TELNET"},
		{name: "shm path with a quote", env: map[string]string{"PDP11_SHM": "/dev/shm/a'b"}, err: "PDP11_SHM"},
		{name: "data path with a blank", env: map[string]string{"PDP11_DATA": "/srv/my data"}, err: "PDP11_DATA"},
		{name: "data path with a semicolon", args: []string{"--data", "/srv/a;b"}, err: "PDP11_DATA"},
		{name: "unknown flag", args: []string{"--colour"}, err: "colour"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			c, _, err := loadConfig(fs, tc.args, envMap(tc.env))
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("error %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, c)
		})
	}
}

func TestCommands(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"version"}, envMap(nil), &out, &errb); code != 0 || !strings.Contains(out.String(), version) {
		t.Fatalf("version: %d %q", code, out.String())
	}
	out.Reset()
	if code := run([]string{"check"}, envMap(nil), &out, &errb); code != 0 || !strings.Contains(out.String(), "valid") {
		t.Fatalf("check of the embedded catalog: %d %q %q", code, out.String(), errb.String())
	}
	errb.Reset()
	if code := run([]string{"frobnicate"}, envMap(nil), &out, &errb); code != 2 || !strings.Contains(errb.String(), "unknown command") {
		t.Fatalf("unknown command: %d %q", code, errb.String())
	}
	bad := t.TempDir()
	os.MkdirAll(filepath.Join(bad, "x"), 0o755)
	os.WriteFile(filepath.Join(bad, "x", "system.json"), []byte(`{"id":"x"}`), 0o644)
	os.WriteFile(filepath.Join(bad, "x", "boot.ini"), nil, 0o644)
	errb.Reset()
	if code := run([]string{"check", bad}, envMap(nil), &out, &errb); code != 1 || !strings.Contains(errb.String(), "invalid") {
		t.Fatalf("check of a bad catalog: %d %q", code, errb.String())
	}
	errb.Reset()
	if code := run([]string{"install"}, envMap(map[string]string{"PDP11_DATA": t.TempDir()}), &out, &errb); code != 2 {
		t.Fatalf("install without ids: %d", code)
	}
	errb.Reset()
	code := run([]string{"fetch", "--cache", t.TempDir(), "rt11"}, envMap(map[string]string{"PDP11_DATA": t.TempDir()}), &out, &errb)
	if code != 2 || !strings.Contains(errb.String(), "--accept-license") {
		t.Fatalf("fetch of a hobbyist system: %d %q", code, errb.String())
	}
}

// testCache returns PDP11_TEST_CACHE (catalog archives named by their
// sha256, as "pdp11d fetch --cache" writes them) if it holds the archive
// of system id, else it skips the test.
func testCache(t *testing.T, id string) string {
	t.Helper()
	dir := os.Getenv("PDP11_TEST_CACHE")
	if dir == "" {
		t.Skip("PDP11_TEST_CACHE (a directory of catalog archives) is not set")
	}
	cat, err := loadCatalog("")
	if err != nil {
		t.Fatal(err)
	}
	s, ok := cat.Get(id)
	if !ok {
		t.Fatalf("%s is not in the catalog", id)
	}
	if _, err := os.Stat(filepath.Join(dir, strings.ToLower(s.Artifacts[0].SHA256))); err != nil {
		t.Skipf("the %s archive is not in PDP11_TEST_CACHE: %v", id, err)
	}
	return dir
}

// dirNames lists a directory.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// TestDataDirLock: serve and install refuse a data directory that another
// pdp11d holds (exit status 2, nothing written into it); fetch does not
// lock. The lock file (0600) records the holder's pid, and its descriptor
// is not inherited by the simulator.
func TestDataDirLock(t *testing.T) {
	data := t.TempDir()
	lock, err := lockDataDir(data)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	path := filepath.Join(data, lockName)
	if raw, _ := os.ReadFile(path); string(raw) != fmt.Sprintf("%d\n", os.Getpid()) {
		t.Fatalf("lock file holds %q", raw)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("lock file: %v %v", fi, err)
	}
	if flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, lock.Fd(), syscall.F_GETFD, 0); errno != 0 || flags&syscall.FD_CLOEXEC == 0 {
		t.Fatal("the lock descriptor is not close-on-exec")
	}
	env := map[string]string{"PDP11_DATA": data, "PDP11_LISTEN": "127.0.0.1:0", "PDP11_SIMH": "/nonexistent",
		"PDP11_SHM": filepath.Join(t.TempDir(), "panel"), "PDP11_CACHE": t.TempDir()}
	busy := func(pid string) string {
		return "pdp11d: another pdp11d (pid " + pid + ") is using the data directory " + data + "; one data directory serves one instance\n"
	}
	for _, args := range [][]string{{"serve"}, {"install", "unixv6"}} {
		var errb bytes.Buffer
		if code := run(args, envMap(env), io.Discard, &errb); code != 2 || !strings.HasSuffix(errb.String(), busy(strconv.Itoa(os.Getpid()))) {
			t.Fatalf("%s: exit %d, stderr %q", args[0], code, errb.String())
		}
	}
	os.WriteFile(path, []byte("garbage"), 0o600)
	var errb bytes.Buffer
	if code := run([]string{"serve"}, envMap(env), io.Discard, &errb); code != 2 || !strings.HasSuffix(errb.String(), busy("unknown")) {
		t.Fatalf("serve: exit %d, stderr %q", code, errb.String())
	}
	if names := dirNames(t, data); len(names) != 1 || names[0] != lockName {
		t.Fatalf("the locked data directory holds %v", names)
	}
	t.Run("fetch", func(t *testing.T) {
		// It does not lock, and it takes the archive from another cache.
		ro := testCache(t, "unixv6")
		dst := t.TempDir()
		var out bytes.Buffer
		if code := run([]string{"fetch", "unixv6"}, envMap(map[string]string{"PDP11_DATA": data, "PDP11_CACHE": dst + ":" + ro}), &out, io.Discard); code != 0 {
			t.Fatalf("fetch: exit %d: %s", code, out.String())
		}
		if strings.Contains(out.String(), "download") || len(dirNames(t, dst)) != 1 {
			t.Fatalf("fetch did not copy from the second cache: %s %v", out.String(), dirNames(t, dst))
		}
	})
	lock.Close()
	l2, err := lockDataDir(data)
	if err != nil {
		t.Fatalf("after the holder released it: %v", err)
	}
	l2.Close()
}

// TestServeBusyPort: a listener that cannot be bound ends serve (exit
// status 1) before the machine is powered on or the data directory used.
func TestServeBusyPort(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	data := filepath.Join(t.TempDir(), "data")
	env := map[string]string{"PDP11_DATA": data, "PDP11_LISTEN": l.Addr().String(), "PDP11_SIMH": "/nonexistent",
		"PDP11_SHM": filepath.Join(t.TempDir(), "panel"), "PDP11_SYSTEM": "unixv6", "PDP11_CACHE": os.Getenv("PDP11_TEST_CACHE")}
	var logs syncBuffer
	if code := run([]string{"serve"}, envMap(env), io.Discard, &logs); code != 1 || !strings.Contains(logs.String(), "address already in use") {
		t.Fatalf("exit %d:\n%s", code, logs.String())
	}
	if strings.Contains(logs.String(), "machine state") {
		t.Fatalf("powered on:\n%s", logs.String())
	}
	if names := dirNames(t, data); len(names) != 1 || names[0] != lockName {
		t.Fatalf("the data directory holds %v", names)
	}
}

// ---- end to end with the real simulator --------------------------------------

func e2eSetup(t *testing.T) (env map[string]string, base string) {
	t.Helper()
	bin, _ := filepath.Abs("../../../simh/out/pdp11")
	if v := os.Getenv("PDP11_TEST_SIMH"); v != "" {
		bin = v
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skip("patched simulator not available")
	}
	// Archives are only read from the cache: no network needed.
	cache := testCache(t, "unixv6")
	dir := t.TempDir()
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	l, _ = net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return map[string]string{
		"PDP11_DATA": filepath.Join(dir, "data"), "PDP11_SIMH": bin, "PDP11_CACHE": cache,
		"PDP11_SHM": filepath.Join(dir, "panel"), "PDP11_LISTEN": addr, "PDP11_PORT_BASE": fmt.Sprint(port),
		"PDP11_SYSTEM": "unixv6", "PDP11_LOG": "debug",
	}, "http://" + addr
}

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func getJSON(url string, v any) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}

// TestServeEndToEnd: an empty data directory ends with UNIX V6 booted to
// its login prompt, with no clicks; SIGTERM powers off and leaves nothing
// running; the next start autostarts without installing again.
func TestServeEndToEnd(t *testing.T) {
	env, base := e2eSetup(t)
	for round := 1; round <= 2; round++ {
		var logs syncBuffer
		done := make(chan int, 1)
		go func() { done <- run([]string{"serve"}, envMap(env), io.Discard, &logs) }()
		deadline := time.Now().Add(60 * time.Second)
		var state struct {
			Run, System string
			Ready       bool
		}
		for {
			if err := getJSON(base+"/api/state", &state); err == nil && state.Ready {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("round %d: not ready (state %+v); log:\n%s", round, state, logs.String())
			}
			select {
			case code := <-done:
				t.Fatalf("serve exited with %d:\n%s", code, logs.String())
			case <-time.After(100 * time.Millisecond):
			}
		}
		if state.Run != "running" || state.System != "unixv6" {
			t.Fatalf("state %+v", state)
		}
		var lines struct{ Lines []string }
		if err := getJSON(base+"/api/log?lines=50", &lines); err != nil || !strings.Contains(strings.Join(lines.Lines, "\n"), "sim> boot rk0") {
			t.Fatalf("log %v %q", err, lines.Lines)
		}
		installs := strings.Count(logs.String(), "msg=installed")
		if round == 1 && installs != 1 || round == 2 && installs != 0 {
			t.Fatalf("round %d: %d installs", round, installs)
		}
		syscall.Kill(os.Getpid(), syscall.SIGTERM)
		select {
		case code := <-done:
			if code != 0 {
				t.Fatalf("exit code %d", code)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("serve did not stop")
		}
		if !strings.Contains(logs.String(), "powered off") {
			t.Fatalf("no power off:\n%s", logs.String())
		}
		if _, err := os.Stat(filepath.Join(env["PDP11_DATA"], "run", "pdp11.pid")); !os.IsNotExist(err) {
			t.Fatal("pid file left behind (simulator still running?)")
		}
	}
}

// TestServeShutdownOnStop: with PDP11_SHUTDOWN_ON_STOP, SIGTERM types the
// system's shutdown script on the console before powering off.
func TestServeShutdownOnStop(t *testing.T) {
	env, base := e2eSetup(t)
	// A one-system catalog: the embedded unixv6 plus a shutdown script.
	src, _ := filepath.Abs("../../../systems/unixv6")
	cat := filepath.Join(t.TempDir(), "unixv6")
	os.MkdirAll(cat, 0o755)
	for _, f := range []string{"boot.ini", "dialog.ini", "LICENSE.txt"} {
		b, err := os.ReadFile(filepath.Join(src, f))
		if err != nil {
			t.Skipf("catalog entry: %v", err)
		}
		os.WriteFile(filepath.Join(cat, f), b, 0o644)
	}
	var sys map[string]any
	raw, _ := os.ReadFile(filepath.Join(src, "system.json"))
	if err := json.Unmarshal(raw, &sys); err != nil {
		t.Fatal(err)
	}
	sys["default"] = true
	sys["shutdownScript"] = []map[string]any{
		{"expect": "login: ", "send": "root\r"},
		{"expect": "# ", "send": "sync\r"},
		{"expect": "# "},
	}
	raw, _ = json.Marshal(sys)
	os.WriteFile(filepath.Join(cat, "system.json"), raw, 0o644)
	env["PDP11_SYSTEMS"] = filepath.Dir(cat)
	env["PDP11_SHUTDOWN_ON_STOP"] = "true"

	var logs syncBuffer
	done := make(chan int, 1)
	go func() { done <- run([]string{"serve"}, envMap(env), io.Discard, &logs) }()
	deadline := time.Now().Add(60 * time.Second)
	for {
		var state struct{ Ready bool }
		if err := getJSON(base+"/api/state", &state); err == nil && state.Ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not ready; log:\n%s", logs.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	start := time.Now()
	syscall.Kill(os.Getpid(), syscall.SIGTERM)
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit code %d", code)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("serve did not stop")
	}
	out := logs.String()
	if !strings.Contains(out, "shutting the guest OS down") || strings.Contains(out, "shutdown incomplete") || !strings.Contains(out, "powered off") {
		t.Fatalf("shutdown sequence:\n%s", out)
	}
	// The script ends without typing anything: the panel lets the OS
	// settle (3 s) before the power-off.
	if d := time.Since(start); d < 3*time.Second {
		t.Fatalf("stopped after %s: the script did not run", d)
	}
}

// TestServeSecondInstance: a second serve or install on the data directory
// of a running pdp11d exits with status 2 (also with the same listener)
// and leaves the first one's simulator alone.
func TestServeSecondInstance(t *testing.T) {
	env, base := e2eSetup(t)
	var logs syncBuffer
	done := make(chan int, 1)
	go func() { done <- run([]string{"serve"}, envMap(env), io.Discard, &logs) }()
	deadline := time.Now().Add(60 * time.Second)
	for {
		var state struct{ Ready bool }
		if err := getJSON(base+"/api/state", &state); err == nil && state.Ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not ready; log:\n%s", logs.String())
		}
		select {
		case code := <-done:
			t.Fatalf("serve exited with %d:\n%s", code, logs.String())
		case <-time.After(100 * time.Millisecond):
		}
	}
	pidFile := filepath.Join(env["PDP11_DATA"], "run", "pdp11.pid")
	pid, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("another pdp11d (pid %d) is using the data directory", os.Getpid())
	for _, args := range [][]string{{"serve"}, {"install", "unixv6"}} {
		var errb syncBuffer
		if code := run(args, envMap(env), io.Discard, &errb); code != 2 || !strings.Contains(errb.String(), want) {
			t.Fatalf("second %s: exit %d:\n%s", args[0], code, errb.String())
		}
	}
	var state struct{ Run string }
	if err := getJSON(base+"/api/state", &state); err != nil || state.Run != "running" {
		t.Fatalf("first instance: %v %+v", err, state)
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(pid)))
	if now, _ := os.ReadFile(pidFile); !bytes.Equal(now, pid) || syscall.Kill(n, 0) != nil {
		t.Fatalf("simulator %s replaced or gone (pid file %q)", pid, now)
	}
	syscall.Kill(os.Getpid(), syscall.SIGTERM)
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit code %d", code)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("serve did not stop")
	}
	if strings.Contains(logs.String(), "stale simulator") || !strings.Contains(logs.String(), "powered off") {
		t.Fatalf("first instance log:\n%s", logs.String())
	}
}
