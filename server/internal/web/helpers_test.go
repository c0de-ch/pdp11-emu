package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"

	"pdp11emu/server/internal/api"
	"pdp11emu/server/internal/panel"
	"pdp11emu/server/internal/panel/paneltest"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// testUI is a minimal static tree.
func testUI() fstest.MapFS {
	return fstest.MapFS{
		"index.html":                 {Data: []byte(`<!doctype html><meta name="pdp11-version" content="{{VERSION}}"><p>{{VERSION}}</p>`)},
		"assets/js/app.js":           {Data: []byte("export const x = 1;\n")},
		"assets/css/panel.css":       {Data: []byte("body{}\n")},
		"assets/img/logo.svg":        {Data: []byte("<svg/>")},
		"assets/fonts/glass.woff2":   {Data: []byte("wOF2")},
		"assets/panel/geometry.json": {Data: []byte("{}")},
		"assets/vendor/blob.bin":     {Data: []byte{0, 1, 2}},
	}
}

// testVersion is the version the server reports for testUI: newWebEnv's
// default version plus the build id.
func testVersion() string { return "1.2.3+" + buildID(testUI()) }

type webEnv struct {
	t   *testing.T
	m   *paneltest.Machine
	src *paneltest.Source
	sys *paneltest.Systems
	lg  *paneltest.Log
	p   *panel.Panel
	s   *Server
	ts  *httptest.Server
}

type webCfg struct {
	opts      Options
	tune      func(*tuning)
	key       string // initial key: run the panel's autostart unless "off"
	noRun     bool   // do not start the panel
	noBooting bool
	logWrap   func(api.LogBuffer) api.LogBuffer // wraps the fake log given to the server
	sendBuf   int                               // bound the kernel send buffer per connection (as Run does)
}

// newWebEnv starts a panel over fakes (autostart: the machine runs) and
// the web server in an httptest server.
func newWebEnv(t *testing.T, cfg webCfg) *webEnv {
	t.Helper()
	e := &webEnv{t: t, m: paneltest.NewMachine(), src: paneltest.NewSource(), sys: paneltest.DefaultSystems(), lg: paneltest.NewLog()}
	t.Cleanup(e.m.Close)
	p, err := panel.New(e.m, e.src, e.sys, panel.Options{FrameHz: 100, Autostart: cfg.key != "off", Logger: quietLogger()})
	if err != nil {
		t.Fatal(err)
	}
	e.p = p
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = p.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	if cfg.key == "off" {
		p.Input(api.PanelInput{Kind: "key", Value: "off"})
	} else if !paneltest.WaitFor(5*time.Second, func() bool { return e.m.State() == api.RunRunning }) {
		t.Fatalf("machine not running: %s", e.m.State())
	}
	opts := cfg.opts
	if opts.Version == "" {
		opts.Version = "1.2.3"
	}
	if opts.Static == nil {
		opts.Static = testUI()
	}
	if opts.Logger == nil {
		opts.Logger = quietLogger()
	}
	opts.FrameHz = 100
	var lg api.LogBuffer = e.lg
	if cfg.logWrap != nil {
		lg = cfg.logWrap(lg)
	}
	s, err := newServer(opts, Deps{Machine: e.m, Panel: p, Systems: e.sys, Log: lg}, cfg.tune)
	if err != nil {
		t.Fatal(err)
	}
	e.s = s
	e.ts = httptest.NewUnstartedServer(s.Handler())
	if cfg.sendBuf > 0 {
		e.ts.Listener = sendBufferListener{e.ts.Listener, cfg.sendBuf}
	}
	e.ts.Start()
	t.Cleanup(func() { s.Close(); e.ts.Close() })
	// Make the panel publish a frame, so that panel subscribers get one.
	e.src.Push(api.Sample{Addr: api.PnlWait, Weight: 1})
	if !paneltest.WaitFor(5*time.Second, func() bool {
		s.hub.mu.Lock()
		defer s.hub.mu.Unlock()
		return s.hub.lastFrame != nil
	}) {
		t.Fatal("no panel frame")
	}
	return e
}

func (e *webEnv) url(path string) string { return e.ts.URL + "/" + path }

func (e *webEnv) wsURL(query string) string {
	u := "ws" + strings.TrimPrefix(e.ts.URL, "http") + "/ws"
	if query != "" {
		u += "?" + query
	}
	return u
}

type rx struct {
	typ  websocket.MessageType
	data []byte
}

func (r rx) json() map[string]any {
	var m map[string]any
	_ = json.Unmarshal(r.data, &m)
	return m
}

func (r rx) msgType() string {
	if r.typ == websocket.MessageBinary {
		if len(r.data) > 0 && r.data[0] == binPanelFrame {
			return "#frame"
		}
		if len(r.data) > 0 && r.data[0] == binConsole {
			return "#console"
		}
		return "#binary"
	}
	t, _ := r.json()["type"].(string)
	return t
}

// wsClient reads everything into a channel (a fast client).
type wsClient struct {
	t    *testing.T
	c    *websocket.Conn
	in   chan rx
	err  chan error
	stop context.CancelFunc
}

func (e *webEnv) dial(query string, hdr http.Header) *wsClient {
	e.t.Helper()
	c, resp, err := websocket.Dial(context.Background(), e.wsURL(query), &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		e.t.Fatalf("dial %s: %v (HTTP %d)", query, err, code)
	}
	c.SetReadLimit(-1)
	ctx, cancel := context.WithCancel(context.Background())
	w := &wsClient{t: e.t, c: c, in: make(chan rx, 100000), err: make(chan error, 1), stop: cancel}
	go func() {
		for {
			typ, data, err := c.Read(ctx)
			if err != nil {
				w.err <- err
				close(w.in)
				return
			}
			w.in <- rx{typ, data}
		}
	}()
	e.t.Cleanup(func() { cancel(); _ = c.CloseNow() })
	return w
}

// next returns the next message.
func (w *wsClient) next() rx {
	w.t.Helper()
	select {
	case m, ok := <-w.in:
		if !ok {
			w.t.Fatalf("connection closed: %v", <-w.err)
		}
		return m
	case <-time.After(5 * time.Second):
		w.t.Fatal("no message within 5 s")
	}
	return rx{}
}

// await returns the next message of the given type, skipping others.
func (w *wsClient) await(typ string, pred func(rx) bool) rx {
	w.t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case m, ok := <-w.in:
			if !ok {
				w.t.Fatalf("connection closed while waiting for %s: %v", typ, <-w.err)
			}
			if m.msgType() == typ && (pred == nil || pred(m)) {
				return m
			}
		case <-deadline:
			w.t.Fatalf("no %s message within 5 s", typ)
		}
	}
}

// drain collects messages until none arrived for quiet.
func (w *wsClient) drain(quiet time.Duration) []rx {
	var out []rx
	for {
		select {
		case m, ok := <-w.in:
			if !ok {
				return out
			}
			out = append(out, m)
		case <-time.After(quiet):
			return out
		}
	}
}

func (w *wsClient) send(v any) {
	w.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		w.t.Fatal(err)
	}
	w.sendRaw(websocket.MessageText, b)
}

func (w *wsClient) sendRaw(typ websocket.MessageType, b []byte) {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.c.Write(ctx, typ, b); err != nil {
		w.t.Fatalf("write: %v", err)
	}
}

// skipInit reads hello, state, systems and the histories of the given
// channels.
func (w *wsClient) skipInit(histories int) {
	w.t.Helper()
	for i := 0; i < 3+histories; i++ {
		w.next()
	}
}

// toast waits for a toast containing text.
func (w *wsClient) toast(text string) map[string]any {
	w.t.Helper()
	return w.await("toast", func(m rx) bool { return strings.Contains(m.json()["text"].(string), text) }).json()
}

// state waits for a state message satisfying pred.
func (w *wsClient) state(pred func(map[string]any) bool) map[string]any {
	w.t.Helper()
	return w.await("state", func(m rx) bool { return pred(m.json()) }).json()
}

func get(m map[string]any, path ...string) any {
	var v any = m
	for _, p := range path {
		mm, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = mm[p]
	}
	return v
}
