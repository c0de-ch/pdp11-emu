package web

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// logRecorder is a slog.Handler that keeps every message with its attributes.
type logRecorder struct {
	mu   sync.Mutex
	msgs []string
}

func (l *logRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (l *logRecorder) WithAttrs([]slog.Attr) slog.Handler       { return l }
func (l *logRecorder) WithGroup(string) slog.Handler            { return l }

func (l *logRecorder) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool { fmt.Fprintf(&b, " %s=%v", a.Key, a.Value); return true })
	l.mu.Lock()
	defer l.mu.Unlock()
	l.msgs = append(l.msgs, b.String())
	return nil
}

// count returns the number of messages containing text.
func (l *logRecorder) count(text string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, m := range l.msgs {
		if strings.Contains(m, text) {
			n++
		}
	}
	return n
}

func (l *logRecorder) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.msgs, "\n")
}

// doHost is do with another Host header.
func doHost(t *testing.T, method, url, host string, hdr http.Header) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	for k, v := range hdr {
		req.Header[k] = v
	}
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestHostCheckRules(t *testing.T) {
	for in, want := range map[string]string{
		"Attacker.Example.:8080": "attacker.example",
		"[::1]:80":               "::1",
		"[::1]":                  "::1",
		"LOCALHOST.":             "localhost",
		"pdp11":                  "pdp11",
		"pdp11:":                 "pdp11",
		"":                       "",
	} {
		if got := normalizeHost(in); got != want {
			t.Errorf("normalizeHost(%q) = %q, want %q", in, got, want)
		}
	}
	hc, err := newHostCheck("Pdp11.Example.Org.", []string{"pdp11.lan", " *.Example.NET. ", "10.0.0.1", "[::1]"},
		[]string{"*.example.com", "https://proxy.example.info", "pdp.example.org:8443"})
	if err != nil {
		t.Fatal(err)
	}
	for host, want := range map[string]bool{
		"":                     true, // HTTP/1.0 without Host
		"127.0.0.1":            true,
		"192.168.1.20":         true,
		"::1":                  true,
		"fe80::1%eth0":         true,
		"localhost":            true,
		"a.b.localhost":        true,
		"pdp11.example.org":    true, // os.Hostname()
		"pdp11":                true, // its first label
		"pdp11.local":          true, // mDNS
		"pdp11.lan":            true, // PDP11_HOSTS
		"x.example.net":        true,
		"a.b.example.net":      true,
		"example.net":          false, // "*." means subdomains only
		"pdp.example.com":      true,  // PDP11_ALLOW_ORIGINS host patterns
		"a.b.example.com":      true,
		"example.com":          false,
		"proxy.example.info":   true,
		"pdp.example.org":      true,
		"attacker.example":     false,
		"localhost.attacker.x": false,
		"pdp11.lan.attacker.x": false,
		"evil-pdp11.local":     false,
		"pdp11.local.x":        false,
		"127.0.0.1.nip.io":     false,
		"xn--bcher-kva.ch":     false,
	} {
		if got := hc.allows(host); got != want {
			t.Errorf("allows(%q) = %v, want %v", host, got, want)
		}
	}
	if d := hc.describe(); !strings.Contains(d, "pdp11.local") || !strings.Contains(d, "*.example.net") ||
		!strings.Contains(d, "proxy.example.info") || strings.Contains(d, "10.0.0.1") {
		t.Errorf("describe() = %q", d)
	}
	off, err := newHostCheck("", []string{"pdp11.lan", "*"}, nil)
	if err != nil || !off.allows("attacker.example") || off.describe() != "any (PDP11_HOSTS=*)" {
		t.Fatalf("PDP11_HOSTS=*: %v %v", err, off)
	}
	// The host name of the listen address (PDP11_LISTEN) is a name of this
	// machine; addresses and the always allowed names add nothing.
	lc, _ := newHostCheck("", nil, nil)
	for _, l := range []string{":8080", "0.0.0.0:80", "[::1]:80", "localhost:80", "Pdp11-Listen.Lan.:8080", "no-port"} {
		lc.addListenHost(l)
	}
	if !lc.allows("pdp11-listen.lan") || lc.allows("no-port") || lc.describe() != "IP addresses, localhost, *.localhost, pdp11-listen.lan" {
		t.Errorf("listen host: %q", lc.describe())
	}
	for _, bad := range []string{"pdp11.lan:8080", "http://pdp11.lan", "pdp*.lan", "*.", "*example.com", "*.*.example.com", "a..b", "a b", "[pdp11]"} {
		if _, err := newHostCheck("", []string{bad}, nil); err == nil || !strings.Contains(err.Error(), "PDP11_HOSTS") {
			t.Errorf("PDP11_HOSTS entry %q: %v", bad, err)
		}
	}
	e := newWebEnv(t, webCfg{})
	if _, err := newServer(Options{Hosts: []string{"pdp11.lan:8080"}}, Deps{Machine: e.m, Panel: e.p, Systems: e.sys}, nil); err == nil ||
		!strings.Contains(err.Error(), "PDP11_HOSTS") {
		t.Errorf("newServer with a host with a port: %v", err)
	}
}

// TestHostCheck: without a password, a request for an unknown host name
// (DNS rebinding) is refused with 421 on every route, the WebSocket
// handshake included.
func TestHostCheck(t *testing.T) {
	rec := &logRecorder{}
	e := newWebEnv(t, webCfg{opts: Options{Listen: "pdp11-listen.test:8080", Hosts: []string{"pdp11.lan", "*.example.net"},
		AllowOrigins: []string{"*.example.com"}, Logger: slog.New(rec)}})
	port := e.ts.Listener.Addr().(*net.TCPAddr).Port
	hostname, _ := os.Hostname()
	first, _, _ := strings.Cut(strings.ToLower(hostname), ".")
	allowed := []string{"127.0.0.1", "[::1]", "localhost", "LocalHost.", "pdp.localhost", "pdp11.lan", "a.b.example.net", "pdp.example.com",
		"pdp11-listen.test"}
	if hostname != "" {
		allowed = append(allowed, hostname, first, first+".local")
	}
	for _, h := range allowed {
		for _, p := range []string{"", "api/status", "assets/js/app.js"} {
			if resp, _ := doHost(t, "GET", e.url(p), fmt.Sprintf("%s:%d", h, port), nil); resp.StatusCode != 200 {
				t.Errorf("Host %s, /%s: %d", h, p, resp.StatusCode)
			}
		}
	}
	const refused = `pdp11d: the host name "attacker.example" is not allowed while no password is set. Add it to PDP11_HOSTS, or set PDP11_PASSWORD.`
	for _, p := range []string{"", "index.html", "api/status", "api/state", "assets/js/app.js", "nope", "ws"} {
		resp, body := doHost(t, "GET", e.url(p), fmt.Sprintf("attacker.example:%d", port), nil)
		if resp.StatusCode != http.StatusMisdirectedRequest || strings.TrimSpace(body) != refused ||
			resp.Header.Get("Content-Type") != "text/plain; charset=utf-8" {
			t.Errorf("Host attacker.example, /%s: %d %v %q", p, resp.StatusCode, resp.Header, body)
		}
	}
	for _, h := range []string{"example.net", "example.com", "pdp11.lan.attacker.example"} {
		if resp, _ := doHost(t, "GET", e.url("api/state"), h, nil); resp.StatusCode != http.StatusMisdirectedRequest {
			t.Errorf("Host %s: %d", h, resp.StatusCode)
		}
	}
	// The WebSocket handshake of a rebound page: Origin and Host name the
	// attacker's domain, so the Origin check alone would pass it.
	rebound := fmt.Sprintf("attacker.example:%d", port)
	_, resp, err := websocket.Dial(context.Background(), e.wsURL("sub=console"), &websocket.DialOptions{
		Host: rebound, HTTPHeader: http.Header{"Origin": {"http://" + rebound}}})
	if err == nil || resp == nil || resp.StatusCode != http.StatusMisdirectedRequest {
		t.Fatalf("rebound WebSocket: %v %v", err, resp)
	}
	if n := e.s.hub.clientCount(); n != 0 {
		t.Fatalf("%d clients after a refused handshake", n)
	}
	named := fmt.Sprintf("pdp11.lan:%d", port)
	c, _, err := websocket.Dial(context.Background(), e.wsURL("sub="), &websocket.DialOptions{
		Host: named, HTTPHeader: http.Header{"Origin": {"http://" + named}}})
	if err != nil {
		t.Fatalf("WebSocket by an allowed name: %v", err)
	}
	c.CloseNow()
	// HTTP/1.0 without a Host header.
	conn, err := net.Dial("tcp", e.ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprint(conn, "GET /api/status HTTP/1.0\r\n\r\n")
	if line, _ := bufio.NewReader(conn).ReadString('\n'); !strings.HasPrefix(line, "HTTP/1.0 200") {
		t.Fatalf("HTTP/1.0 without Host: %q", line)
	}
	// Each refused host is logged at most once a minute.
	if n := rec.count("host=attacker.example "); n != 1 {
		t.Fatalf("%d warnings for attacker.example:\n%s", n, rec)
	}
	if n := rec.count("not allowed while no password is set"); n != 4 {
		t.Fatalf("%d warnings for 4 refused hosts:\n%s", n, rec)
	}
}

func TestHostCheckWarningsAreBounded(t *testing.T) {
	hc, _ := newHostCheck("", nil, nil)
	now := time.Now()
	if !hc.shouldWarn("a.example", now) || hc.shouldWarn("a.example", now.Add(59*time.Second)) || !hc.shouldWarn("a.example", now.Add(time.Minute)) {
		t.Fatal("not once a minute")
	}
	for i := 0; i < 2*hostWarnTrack; i++ {
		hc.shouldWarn(fmt.Sprintf("h%d.example", i), now)
	}
	if len(hc.warned) > hostWarnTrack {
		t.Fatalf("%d names tracked", len(hc.warned))
	}
	if !hc.shouldWarn("late.example", now.Add(2*time.Minute)) {
		t.Fatal("no warning once the old names expired")
	}
}

// TestHostCheckOff: PDP11_HOSTS=* disables the check, and with a password
// there is none (a rebound page has no credentials).
func TestHostCheckOff(t *testing.T) {
	e := newWebEnv(t, webCfg{opts: Options{Hosts: []string{"*"}}})
	if resp, _ := doHost(t, "GET", e.url("api/state"), "attacker.example", nil); resp.StatusCode != 200 {
		t.Fatalf("PDP11_HOSTS=*: %d", resp.StatusCode)
	}
	e = newWebEnv(t, webCfg{opts: Options{Password: "pw"}})
	if resp, _ := doHost(t, "GET", e.url("api/state"), "attacker.example", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("with a password, no credentials: %d", resp.StatusCode)
	}
	if resp, _ := doHost(t, "GET", e.url("api/state"), "attacker.example", http.Header{"Authorization": {"Basic dTpwdw=="}}); resp.StatusCode != 200 {
		t.Fatalf("with a password and credentials: %d", resp.StatusCode)
	}
}

// TestStartupLog: serve logs whether authentication is on, and without a
// password what the host check allows.
func TestStartupLog(t *testing.T) {
	for _, tc := range []struct {
		opts Options
		want []string
	}{
		{Options{Hosts: []string{"pdp11.lan"}}, []string{"authentication: off", "allowedHosts=IP addresses, localhost, *.localhost, ", "pdp11.lan"}},
		{Options{Password: "pw", TrustedProxies: []string{"10.0.0.0/8"}}, []string{"authentication: on", "proxies=[10.0.0.0/8]"}},
	} {
		rec := &logRecorder{}
		tc.opts.Logger = slog.New(rec)
		e := newWebEnv(t, webCfg{opts: tc.opts})
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- e.s.Serve(ctx, ln) }()
		cancel()
		<-done
		for _, w := range tc.want {
			if rec.count(w) != 1 {
				t.Errorf("no %q in the log:\n%s", w, rec)
			}
		}
		if rec.count("authentication: ") != 1 {
			t.Errorf("authentication logged %d times", rec.count("authentication: "))
		}
	}
}
