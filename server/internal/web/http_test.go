package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"
)

func do(t *testing.T, method, url string, hdr http.Header) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
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

func TestStaticFiles(t *testing.T) {
	e := newWebEnv(t, webCfg{opts: Options{Version: `9.9 <"x">`}})
	resp, body := do(t, "GET", e.url(""), nil)
	if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "no-store" ||
		resp.Header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("index: %d %v", resp.StatusCode, resp.Header)
	}
	if want := `content="9.9 &lt;&#34;x&#34;&gt;+` + buildID(testUI()) + `"`; !strings.Contains(body, want) || strings.Contains(body, "{{VERSION}}") {
		t.Fatalf("version not substituted (escaped): %s", body)
	}
	if resp, _ := do(t, "GET", e.url("?view=panel&chrome=0"), nil); resp.StatusCode != 200 {
		t.Fatalf("index with view: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "GET", e.url("index.html"), nil); resp.StatusCode != 200 {
		t.Fatalf("index.html: %d", resp.StatusCode)
	}
	for name, ctype := range map[string]string{
		"assets/js/app.js":           "text/javascript; charset=utf-8",
		"assets/css/panel.css":       "text/css; charset=utf-8",
		"assets/img/logo.svg":        "image/svg+xml",
		"assets/fonts/glass.woff2":   "font/woff2",
		"assets/panel/geometry.json": "application/json",
		"assets/vendor/blob.bin":     "application/octet-stream",
	} {
		resp, body := do(t, "GET", e.url(name), nil)
		etag := resp.Header.Get("ETag")
		if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != ctype || resp.Header.Get("Cache-Control") != "no-cache" ||
			!strings.HasPrefix(etag, `"`) || strings.HasPrefix(etag, "W/") || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: %d %v", name, resp.StatusCode, resp.Header)
			continue
		}
		if body != string(testUI()[name].Data) {
			t.Errorf("%s: body %q", name, body)
		}
		resp, body = do(t, "GET", e.url(name), http.Header{"If-None-Match": {etag}})
		if resp.StatusCode != http.StatusNotModified || body != "" {
			t.Errorf("%s revalidation: %d", name, resp.StatusCode)
		}
		if resp, _ := do(t, "GET", e.url(name), http.Header{"If-None-Match": {`"other"`}}); resp.StatusCode != 200 {
			t.Errorf("%s with a stale ETag: %d", name, resp.StatusCode)
		}
	}
	resp, body = do(t, "HEAD", e.url("assets/js/app.js"), nil)
	if resp.StatusCode != 200 || body != "" || resp.ContentLength != int64(len(testUI()["assets/js/app.js"].Data)) {
		t.Fatalf("HEAD: %d %d %q", resp.StatusCode, resp.ContentLength, body)
	}
	for _, p := range []string{"assets/", "assets/js", "assets/js/", "assets/nope.js", "nope", "web/index.html", "api/nope", "dev/mock-server.mjs"} {
		if resp, _ := do(t, "GET", e.url(p), nil); resp.StatusCode != 404 {
			t.Errorf("GET /%s: %d, want 404", p, resp.StatusCode)
		}
	}
	if resp, _ := do(t, "GET", e.url("assets/../index.html"), nil); resp.StatusCode == 200 && resp.Header.Get("Cache-Control") == "no-cache" {
		t.Error("path traversal served a file as an asset")
	}
	for _, p := range []string{"", "api/state", "assets/js/app.js"} {
		if resp, _ := do(t, "POST", e.url(p), nil); resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("POST /%s: %d", p, resp.StatusCode)
		}
	}
}

func TestStaticDirectoryIsRehashed(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(dir, "assets", "a.js")
	_ = os.WriteFile(filepath.Join(dir, "index.html"), []byte("{{VERSION}}"), 0o644)
	_ = os.WriteFile(f, []byte("one"), 0o644)
	id := buildID(os.DirFS(dir))
	e := newWebEnv(t, webCfg{opts: Options{Static: os.DirFS(dir)}})
	r1, _ := do(t, "GET", e.url("assets/a.js"), nil)
	_ = os.WriteFile(f, []byte("two!"), 0o644)
	_ = os.Chtimes(f, time.Now().Add(time.Hour), time.Now().Add(time.Hour))
	r2, body := do(t, "GET", e.url("assets/a.js"), nil)
	if body != "two!" || r1.Header.Get("ETag") == r2.Header.Get("ETag") {
		t.Fatalf("changed file: %q, ETags %s %s", body, r1.Header.Get("ETag"), r2.Header.Get("ETag"))
	}
	// The build id is computed once, at start.
	if _, body := do(t, "GET", e.url(""), nil); body != "1.2.3+"+id {
		t.Fatalf("index from a directory: %q, want 1.2.3+%s", body, id)
	}
}

func TestNoStaticFiles(t *testing.T) {
	e := newWebEnv(t, webCfg{opts: Options{Static: fstest.MapFS{}}})
	if resp, _ := do(t, "GET", e.url(""), nil); resp.StatusCode != 500 {
		t.Fatalf("missing index.html: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "GET", e.url("api/status"), nil); resp.StatusCode != 200 {
		t.Fatalf("API without UI: %d", resp.StatusCode)
	}
}

func TestREST(t *testing.T) {
	e := newWebEnv(t, webCfg{})
	getJSON := func(path string, want int) map[string]any {
		t.Helper()
		resp, body := do(t, "GET", e.url(path), nil)
		if resp.StatusCode != want || resp.Header.Get("Content-Type") != "application/json" || resp.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: %d %v", path, resp.StatusCode, resp.Header)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(body), &m); err != nil {
			t.Fatalf("%s: %v: %s", path, err, body)
		}
		return m
	}
	if st := getJSON("api/status", 200); st["ok"] != true || st["version"] != testVersion() || st["run"] != "running" ||
		st["system"] != "bsd211" || st["power"] != "on" {
		t.Fatalf("status %v", st)
	}
	if sys := getJSON("api/systems", 200); sys["type"] != "systems" || sys["selected"] != "bsd211" || len(sys["list"].([]any)) != 2 {
		t.Fatalf("systems %v", sys)
	}
	if st := getJSON("api/state", 200); st["type"] != "state" || st["run"] != "running" || get(st, "controls", "key") != "power" {
		t.Fatalf("state %v", st)
	}
	for i := 0; i < 300; i++ {
		e.lg.Add("line")
	}
	e.lg.Add("last")
	if lg := getJSON("api/log", 200); len(lg["lines"].([]any)) != defaultLogLines {
		t.Fatalf("log default: %d lines", len(lg["lines"].([]any)))
	}
	if lg := getJSON("api/log?lines=2", 200); len(lg["lines"].([]any)) != 2 || lg["lines"].([]any)[1] != "last" {
		t.Fatalf("log 2: %v", lg)
	}
	for _, q := range []string{"0", "-1", "abc", "10001"} {
		getJSON("api/log?lines="+q, 400)
	}
}

func TestAuth(t *testing.T) {
	e := newWebEnv(t, webCfg{opts: Options{Password: "s3cret"}})
	basic := func(pw string) http.Header {
		r, _ := http.NewRequest("GET", "/", nil)
		r.SetBasicAuth("anyone", pw)
		return r.Header
	}
	for _, p := range []string{"", "api/state", "api/status", "assets/js/app.js", "ws"} {
		resp, _ := do(t, "GET", e.url(p), nil)
		if resp.StatusCode != 401 || !strings.HasPrefix(resp.Header.Get("WWW-Authenticate"), "Basic ") {
			t.Errorf("/%s without auth: %d %v", p, resp.StatusCode, resp.Header)
		}
		if resp, _ := do(t, "GET", e.url(p), basic("wrong")); resp.StatusCode != 401 {
			t.Errorf("/%s with a wrong password: %d", p, resp.StatusCode)
		}
	}
	resp, _ := do(t, "GET", e.url(""), basic("s3cret"))
	if resp.StatusCode != 200 {
		t.Fatalf("with the password: %d", resp.StatusCode)
	}
	var session *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie {
			session = c
		}
	}
	if session == nil || !session.HttpOnly || session.SameSite != http.SameSiteStrictMode || session.Path != "/" || session.Secure {
		t.Fatalf("session cookie %+v", session)
	}
	cookie := http.Header{"Cookie": {session.Name + "=" + session.Value}}
	if resp, _ := do(t, "GET", e.url("api/state"), cookie); resp.StatusCode != 200 {
		t.Fatalf("with the session cookie: %d", resp.StatusCode)
	}
	// A cookie authenticates itself; no new cookie.
	if resp, _ := do(t, "GET", e.url(""), cookie); len(resp.Cookies()) != 0 {
		t.Fatalf("cookie re-issued: %v", resp.Cookies())
	}
	forged := http.Header{"Cookie": {session.Name + "=" + strings.Repeat("A", len(session.Value))}}
	if resp, _ := do(t, "GET", e.url("api/state"), forged); resp.StatusCode != 401 {
		t.Fatalf("forged cookie: %d", resp.StatusCode)
	}
	// WebSocket: refused without auth, accepted with the cookie (pop-outs) or Basic.
	if _, resp, err := websocket.Dial(context.Background(), e.wsURL(""), nil); err == nil || resp == nil || resp.StatusCode != 401 {
		t.Fatalf("WebSocket without auth: %v %v", err, resp)
	}
	w := e.dial("sub=", cookie)
	if hello := w.next().json(); hello["auth"] != true || hello["insecure"] != false {
		t.Fatalf("hello %v", hello)
	}
	e.dial("sub=", basic("s3cret"))

	// Behind a TLS-terminating proxy with a sub-path.
	resp, _ = do(t, "GET", e.url(""), merge(basic("s3cret"), http.Header{"X-Forwarded-Proto": {"https"}, "X-Forwarded-Prefix": {"/pdp11"}}))
	if c := resp.Cookies(); len(c) != 1 || !c[0].Secure || c[0].Path != "/pdp11" {
		t.Fatalf("proxied cookie %+v", c)
	}
	resp, _ = do(t, "GET", e.url(""), merge(basic("s3cret"), http.Header{"X-Forwarded-Prefix": {"/a;b"}}))
	if c := resp.Cookies(); len(c) != 1 || c[0].Path != "/" {
		t.Fatalf("bad prefix not ignored: %+v", c)
	}
}

func merge(hs ...http.Header) http.Header {
	out := http.Header{}
	for _, h := range hs {
		for k, v := range h {
			out[k] = v
		}
	}
	return out
}

func TestHealthPublic(t *testing.T) {
	e := newWebEnv(t, webCfg{opts: Options{Password: "pw", HealthPublic: true}})
	if resp, body := do(t, "GET", e.url("api/status"), nil); resp.StatusCode != 200 || !strings.Contains(body, `"ok":true`) {
		t.Fatalf("public status: %d %s", resp.StatusCode, body)
	}
	if resp, _ := do(t, "HEAD", e.url("api/status"), nil); resp.StatusCode != 200 {
		t.Fatalf("public status HEAD: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "GET", e.url("api/state"), nil); resp.StatusCode != 401 {
		t.Fatalf("state is not public: %d", resp.StatusCode)
	}
}

func TestLoginRateLimit(t *testing.T) {
	e := newWebEnv(t, webCfg{opts: Options{Password: "pw"}})
	wrong := http.Header{"Authorization": {"Basic " + "dTp3cm9uZw=="}} // u:wrong
	for i := 0; i < authFailLimit; i++ {
		if resp, _ := do(t, "GET", e.url("api/state"), wrong); resp.StatusCode != 401 {
			t.Fatalf("attempt %d: %d", i, resp.StatusCode)
		}
	}
	resp, _ := do(t, "GET", e.url("api/state"), wrong)
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("after %d failures: %d", authFailLimit, resp.StatusCode)
	}
	// Even the right password waits until the window has passed ...
	right := http.Header{"Authorization": {"Basic " + "dTpwdw=="}} // u:pw
	if resp, _ := do(t, "GET", e.url("api/state"), right); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("right password while blocked: %d", resp.StatusCode)
	}
	// ... which the limiter measures with its clock.
	a := e.s.auth
	a.fails.mu.Lock()
	a.fails.now = func() time.Time { return time.Now().Add(authFailWindow) }
	a.fails.mu.Unlock()
	if resp, _ := do(t, "GET", e.url("api/state"), right); resp.StatusCode != 200 {
		t.Fatalf("after the window: %d", resp.StatusCode)
	}
	// Requests without credentials (the browser's first try) do not count.
	for i := 0; i < 2*authFailLimit; i++ {
		do(t, "GET", e.url("api/state"), nil)
	}
	if resp, _ := do(t, "GET", e.url("api/state"), right); resp.StatusCode != 200 {
		t.Fatalf("blocked by requests without credentials: %d", resp.StatusCode)
	}
}

func TestOrigin(t *testing.T) {
	e := newWebEnv(t, webCfg{opts: Options{AllowOrigins: []string{"*.example.com", "https://proxy.example.net"}}})
	host := strings.TrimPrefix(e.ts.URL, "http://")
	tests := []struct {
		origin string
		ok     bool
	}{
		{"", true}, // not a browser
		{"http://" + host, true},
		{"http://" + strings.ToUpper(host), true},
		{"http://evil.example.org", false},
		{"null", false},
		{"http://pdp.example.com", true},
		{"https://pdp.example.com:8443", false}, // the pattern has no port
		{"https://proxy.example.net", true},
		{"http://proxy.example.net", false}, // the pattern names the scheme
		{"http://example.com.evil.org", false},
	}
	for _, tt := range tests {
		hdr := http.Header{}
		if tt.origin != "" {
			hdr.Set("Origin", tt.origin)
		}
		c, resp, err := websocket.Dial(context.Background(), e.wsURL("sub="), &websocket.DialOptions{HTTPHeader: hdr})
		if tt.ok {
			if err != nil {
				t.Errorf("origin %q refused: %v", tt.origin, err)
				continue
			}
			c.CloseNow()
		} else if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
			t.Errorf("origin %q: err %v, resp %v", tt.origin, err, resp)
			if c != nil {
				c.CloseNow()
			}
		}
	}
	if _, err := newServer(Options{AllowOrigins: []string{"[bad"}}, Deps{Machine: e.m, Panel: e.p, Systems: e.sys}, nil); err == nil {
		t.Fatal("bad origin pattern accepted")
	}
}

func TestOriginDefaultPorts(t *testing.T) {
	r := httptest.NewRequest("GET", "http://pdp:80/ws", nil)
	r.Host = "pdp:80"
	r.Header.Set("Origin", "http://pdp")
	if !originAllowed(r, nil) {
		t.Fatal("default port not normalized")
	}
	r.Host = "pdp:443"
	r.Header.Set("Origin", "https://pdp")
	if !originAllowed(r, nil) {
		t.Fatal("default https port not normalized")
	}
	r.Header.Set("Origin", "http://pdp")
	if originAllowed(r, nil) {
		t.Fatal("http origin for an https host:443 accepted")
	}
}

func TestInsecureDetection(t *testing.T) {
	for _, tt := range []struct {
		listen, password string
		insecure         bool
	}{
		{":8080", "", true},
		{"0.0.0.0:8080", "", true},
		{"[::]:8080", "", true},
		{"192.168.1.5:8080", "", true},
		{"127.0.0.1:8080", "", false},
		{"[::1]:8080", "", false},
		{"localhost:8080", "", false},
		{":8080", "pw", false},
	} {
		got := tt.password == "" && !loopbackListener(tt.listen)
		if got != tt.insecure {
			t.Errorf("%s password %q: insecure %v", tt.listen, tt.password, got)
		}
	}
}

func TestSendBufferListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	bl := sendBufferListener{ln, 64 << 10}
	go func() {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err == nil {
			defer c.Close()
			time.Sleep(100 * time.Millisecond)
		}
	}()
	c, err := bl.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	raw, err := c.(*net.TCPConn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var size int
	_ = raw.Control(func(fd uintptr) { size, _ = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF) })
	if size < 64<<10 || size > 4*(64<<10) { // Linux reports twice the requested size
		t.Fatalf("SO_SNDBUF = %d", size)
	}
}

const defaultCSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; " +
	"font-src 'self' data:; connect-src 'self' ws: wss:; frame-ancestors 'self'; object-src 'none'; base-uri 'self'; form-action 'self'"

// TestSecurityHeaders: every response carries the CSP; with the default
// frame-ancestors also X-Frame-Options.
func TestSecurityHeaders(t *testing.T) {
	check := func(what string, h http.Header, csp, xfo string) {
		t.Helper()
		if h.Get("Content-Security-Policy") != csp || h.Get("X-Frame-Options") != xfo ||
			h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Referrer-Policy") != "same-origin" {
			t.Errorf("%s: %v", what, h)
		}
	}
	e := newWebEnv(t, webCfg{})
	for _, p := range []string{"", "assets/js/app.js", "api/status", "nope"} {
		resp, _ := do(t, "GET", e.url(p), nil)
		check("/"+p, resp.Header, defaultCSP, "SAMEORIGIN")
	}
	resp, _ := doHost(t, "GET", e.url(""), "attacker.example", nil)
	check("421", resp.Header, defaultCSP, "SAMEORIGIN")
	c, resp, err := websocket.Dial(context.Background(), e.wsURL("sub="), nil)
	if err != nil {
		t.Fatal(err)
	}
	c.CloseNow()
	check("WebSocket handshake", resp.Header, defaultCSP, "SAMEORIGIN")

	e = newWebEnv(t, webCfg{opts: Options{Password: "pw", FrameAncestors: "'self' https://dash.example.com"}})
	csp := strings.Replace(defaultCSP, "frame-ancestors 'self';", "frame-ancestors 'self' https://dash.example.com;", 1)
	resp, _ = do(t, "GET", e.url(""), nil)
	check("401, other ancestors", resp.Header, csp, "")
	e = newWebEnv(t, webCfg{opts: Options{FrameAncestors: "'none'"}})
	resp, _ = do(t, "GET", e.url(""), nil)
	check("'none'", resp.Header, strings.Replace(defaultCSP, "frame-ancestors 'self'", "frame-ancestors 'none'", 1), "")
	for _, bad := range []string{"'self'; script-src *", "'self', https://a.example", "self", "'self' none", "'self'\n", " "} {
		if _, err := newServer(Options{FrameAncestors: bad}, Deps{Machine: e.m, Panel: e.p, Systems: e.sys}, nil); err == nil ||
			!strings.Contains(err.Error(), "PDP11_FRAME_ANCESTORS") {
			t.Errorf("frame ancestors %q: %v", bad, err)
		}
	}
}

// TestBuildID: the reported version carries a build id of the UI files,
// the same in hello, api/status and index.html, that changes with any
// served file.
func TestBuildID(t *testing.T) {
	id := buildID(testUI())
	if len(id) != 12 || strings.Trim(id, "0123456789abcdef") != "" {
		t.Fatalf("build id %q", id)
	}
	changed := func(what string, edit func(fstest.MapFS)) {
		t.Helper()
		ui := testUI()
		edit(ui)
		if buildID(ui) == id {
			t.Errorf("%s: same build id", what)
		}
	}
	changed("asset content", func(ui fstest.MapFS) { ui["assets/js/app.js"] = &fstest.MapFile{Data: []byte("export const x = 2;\n")} })
	changed("index.html", func(ui fstest.MapFS) { ui["index.html"] = &fstest.MapFile{Data: []byte("{{VERSION}}")} })
	changed("new asset", func(ui fstest.MapFS) { ui["assets/js/more.js"] = &fstest.MapFile{Data: []byte("")} })
	changed("renamed asset", func(ui fstest.MapFS) {
		ui["assets/js/app2.js"] = ui["assets/js/app.js"]
		delete(ui, "assets/js/app.js")
	})
	ui := testUI()
	ui["dev/mock-server.mjs"] = &fstest.MapFile{Data: []byte("not served")}
	if buildID(ui) != id {
		t.Error("a file that is not served changed the build id")
	}

	e := newWebEnv(t, webCfg{})
	_, body := do(t, "GET", e.url(""), nil)
	var st statusMsg
	_, raw := do(t, "GET", e.url("api/status"), nil)
	_ = json.Unmarshal([]byte(raw), &st)
	hello := e.dial("sub=", nil).next().json()
	if want := "1.2.3+" + id; st.Version != want || hello["server"] != want || !strings.Contains(body, `content="`+want+`"`) {
		t.Fatalf("version: status %q, hello %v, index %q; want %q", st.Version, hello["server"], body, want)
	}
	// A version with a build part already, or no UI: nothing appended.
	if v := withBuildID("1.2.3+git.abc", testUI()); v != "1.2.3+git.abc" {
		t.Fatalf("version with +: %q", v)
	}
	s, err := newServer(Options{Version: "1.2.3", Logger: quietLogger()}, Deps{Machine: e.m, Panel: e.p, Systems: e.sys}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.status().Version != "1.2.3" {
		t.Fatalf("without UI: %q", s.status().Version)
	}
}

func TestTrustedProxies(t *testing.T) {
	trusted, err := parseProxies([]string{"10.0.0.0/8", "192.0.2.1", "2001:db8:ffff::/48", "::ffff:172.16.0.0/108"})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(trusted) != "[10.0.0.0/8 192.0.2.1/32 2001:db8:ffff::/48 172.16.0.0/12]" {
		t.Fatalf("parsed %v", trusted)
	}
	for _, bad := range []string{"10.0.0.0/33", "proxy.lan", "10.0.0.1:80", ""} {
		if _, err := parseProxies([]string{bad}); err == nil || !strings.Contains(err.Error(), "PDP11_TRUSTED_PROXIES") {
			t.Errorf("%q: %v", bad, err)
		}
	}
	for _, tt := range []struct {
		remote  string
		xff     []string
		want    string
		proxied bool
	}{
		{"198.51.100.7:4000", nil, "198.51.100.7", false},
		{"198.51.100.7:4000", []string{"10.0.0.1"}, "198.51.100.7", false}, // not from a proxy: never believed
		{"10.0.0.2:4000", nil, "10.0.0.2", false},
		{"10.0.0.2:4000", []string{"203.0.113.5"}, "203.0.113.5", true},
		{"10.0.0.2:4000", []string{"1.1.1.1, 203.0.113.5"}, "203.0.113.5", true}, // the client wrote 1.1.1.1
		{"10.0.0.2:4000", []string{"198.51.100.1, 10.9.9.9"}, "198.51.100.1", true},
		{"10.0.0.2:4000", []string{"10.1.1.1, 192.0.2.1"}, "10.0.0.2", false}, // all trusted
		{"10.0.0.2:4000", []string{"1.1.1.1", "203.0.113.9"}, "203.0.113.9", true},
		{"10.0.0.2:4000", []string{"203.0.113.5, unknown"}, "10.0.0.2", false},
		{"10.0.0.2:4000", []string{"203.0.113.5, "}, "203.0.113.5", true},
		{"192.0.2.1:80", []string{"[2001:db8::5]:443"}, "2001:db8::5", true},
		{"192.0.2.1:80", []string{"203.0.113.5:1234"}, "203.0.113.5", true},
		{"[::ffff:10.0.0.2]:4000", []string{"203.0.113.5"}, "203.0.113.5", true},
		{"[2001:db8:ffff:1::9]:4000", []string{"2001:db8:1::7"}, "2001:db8:1::7", true},
		{"172.20.0.3:4000", []string{"::ffff:203.0.113.5"}, "203.0.113.5", true},
		{"@", []string{"203.0.113.5"}, "invalid IP", false},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = tt.remote
		for _, v := range tt.xff {
			r.Header.Add("X-Forwarded-For", v)
		}
		if a, proxied := clientAddr(r, trusted); a.String() != tt.want || proxied != tt.proxied {
			t.Errorf("%s %q: %v %v, want %s %v", tt.remote, tt.xff, a, proxied, tt.want, tt.proxied)
		}
	}
}

func TestLimiterKeys(t *testing.T) {
	for remote, want := range map[string]string{
		"203.0.113.5:1":                        "203.0.113.5",
		"[::ffff:203.0.113.5]:1":               "203.0.113.5",
		"[2001:db8:1:2::1]:1":                  "2001:db8:1:2::/64",
		"[2001:db8:1:2:ffff:ffff:ffff:ffff]:1": "2001:db8:1:2::/64",
		"[2001:db8:1:3::1]:1":                  "2001:db8:1:3::/64",
		"[fe80::1%eth0]:1":                     "fe80::/64",
		"@":                                    "@",
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		if got := limiterKey(r, nil); got != want {
			t.Errorf("limiterKey(%s) = %q, want %q", remote, got, want)
		}
	}
}

// TestLoginRateLimitBehindProxy: behind a trusted proxy the limit counts
// each client (IPv6 by /64), not the proxy.
func TestLoginRateLimitBehindProxy(t *testing.T) {
	e := newWebEnv(t, webCfg{opts: Options{Password: "pw", TrustedProxies: []string{"127.0.0.1"}}})
	login := func(client, auth string) int {
		t.Helper()
		resp, _ := do(t, "GET", e.url("api/state"), http.Header{"Authorization": {"Basic " + auth}, "X-Forwarded-For": {client}})
		return resp.StatusCode
	}
	const wrong, right = "dTp3cm9uZw==", "dTpwdw==" // u:wrong, u:pw
	for i := 0; i < authFailLimit; i++ {
		if code := login("203.0.113.1", wrong); code != 401 {
			t.Fatalf("attempt %d: %d", i, code)
		}
	}
	if code := login("203.0.113.1", right); code != http.StatusTooManyRequests {
		t.Fatalf("blocked client with the right password: %d", code)
	}
	if code := login("203.0.113.2", right); code != 200 {
		t.Fatalf("another client behind the same proxy: %d", code)
	}
	if code := login("10.9.9.9, 203.0.113.1", right); code != http.StatusTooManyRequests {
		t.Fatalf("blocked client with a made-up X-Forwarded-For entry: %d", code)
	}
	for i := 0; i < authFailLimit; i++ {
		login(fmt.Sprintf("2001:db8:1:2::%x", i+1), wrong) // one host's /64
	}
	if code := login("2001:db8:1:2:abcd::1", right); code != http.StatusTooManyRequests {
		t.Fatalf("same /64: %d", code)
	}
	if code := login("2001:db8:1:3::1", right); code != 200 {
		t.Fatalf("another /64: %d", code)
	}
}

// TestFailLimiterOverflow: a full table forgets expired, then unblocked
// entries, never a blocked one before it expires; while only blocked ones
// remain, a new key counts as blocked.
func TestFailLimiterOverflow(t *testing.T) {
	rec := &logRecorder{}
	t0 := time.Unix(1_000_000, 0)
	clock := t0
	f := &failLimiter{m: map[string]*failEntry{}, now: func() time.Time { return clock }, max: 4, log: slog.New(rec)}
	block := func(key string) {
		for f.m[key] == nil || f.m[key].count < authFailLimit {
			f.fail(key)
		}
	}
	tracked := func() string {
		var keys []string
		for k := range f.m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return strings.Join(keys, ",")
	}
	block("a")
	clock = t0.Add(10 * time.Second)
	block("b")
	f.fail("c")
	f.fail("d")
	f.fail("e") // full: c and d are forgotten, a and b stay blocked
	if tracked() != "a,b,e" || !f.blocked("a") || !f.blocked("b") || f.blocked("e") {
		t.Fatalf("after the overflow: %s", tracked())
	}
	block("e")
	block("f")
	if !f.blocked("g") {
		t.Fatal("a new key is not blocked while only blocked entries are tracked")
	}
	f.fail("g")
	if tracked() != "a,b,e,f" {
		t.Fatalf("a new key was tracked in a table of blocked entries: %s", tracked())
	}
	if n := rec.count("failed logins from too many addresses"); n != 1 {
		t.Fatalf("%d warnings within a minute:\n%s", n, rec)
	}
	clock = t0.Add(authFailWindow) // a's block ends
	if f.blocked("g") {
		t.Fatal("a new key is still blocked after the oldest block expired")
	}
	f.fail("g")
	if tracked() != "b,e,f,g" || !f.blocked("b") {
		t.Fatalf("after a's block expired: %s", tracked())
	}
	clock = t0.Add(authFailWindow + 30*time.Second) // b, e and f expired
	f.fail("h")
	f.fail("i")
	f.fail("j")
	clock = t0.Add(authFailWindow + 40*time.Second)
	f.fail("k") // full of unblocked entries, more than a minute after the first warning
	if n := rec.count("failed logins from too many addresses"); n != 2 {
		t.Fatalf("%d warnings after two minutes:\n%s", n, rec)
	}
}
