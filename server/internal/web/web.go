// Package web serves the UI, the REST API and the WebSocket protocol
// (docs/PROTOCOL.md).
//
// Layout: the HTTP side (static files, REST, authentication, origin check)
// is stateless; the hub owns the WebSocket clients. Every client has one
// writer goroutine fed by an ordered, byte-bounded queue (console output is
// never reordered or dropped: a client that falls too far behind is
// disconnected and catches up from the history when it reconnects) plus a
// "latest wins" slot for lamp frames. Broadcast state is coalesced by pumps
// (state ≤ 20/s, stats alone ≤ 1/s, systems, progress).
package web

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"pdp11emu/server/internal/api"
	"pdp11emu/server/internal/panel"
)

// Options configure the web server.
type Options struct {
	Listen         string       // e.g. ":8080"
	Password       string       // HTTP Basic auth password; empty = no auth, the host check instead
	AllowOrigins   []string     // extra allowed WebSocket origins
	Hosts          []string     // extra host names allowed without a password (PDP11_HOSTS)
	TrustedProxies []string     // proxies whose X-Forwarded-For names the client: IPs, CIDRs
	FrameAncestors string       // CSP frame-ancestors source list; empty = 'self'
	HealthPublic   bool         // allow unauthenticated GET /api/status
	Version        string       // reported (plus the build id) in hello, /api/status and index.html
	FrameHz        int          // reported in hello
	Static         fs.FS        // UI files: index.html, assets/...
	Logger         *slog.Logger // nil: slog.Default()
}

// Deps are the services the web server exposes.
type Deps struct {
	Machine api.Machine
	Panel   *panel.Panel
	Systems api.Systems
	Log     api.LogBuffer
}

// tuning holds the protocol timings and limits (shortened by tests).
type tuning struct {
	pingInterval       time.Duration // server pings; two missed pongs drop the connection
	writeTimeout       time.Duration // one message to one client
	maxQueuedBytes     int           // per client; beyond it the client is disconnected
	stateMinInterval   time.Duration // state messages ≤ 20/s
	statsMinInterval   time.Duration // state changes that are only stats ≤ 1/s
	statePoll          time.Duration // re-check fields without change notifications (inputQueued)
	systemsMinInterval time.Duration
	progressInterval   time.Duration // non-final progress of one task
	logHistoryLines    int
	shutdownWait       time.Duration // Run: how long to wait for connections to end
}

func defaultTuning() tuning {
	return tuning{
		pingInterval:       20 * time.Second,
		writeTimeout:       10 * time.Second,
		maxQueuedBytes:     4 << 20,
		stateMinInterval:   50 * time.Millisecond,
		statsMinInterval:   time.Second,
		statePoll:          250 * time.Millisecond,
		systemsMinInterval: 100 * time.Millisecond,
		progressInterval:   100 * time.Millisecond,
		logHistoryLines:    1000,
		shutdownWait:       5 * time.Second,
	}
}

// Limits of the protocol.
const (
	protocolVersion = 1
	maxClientMsg    = 64 << 10 // WebSocket read limit
	defaultLogLines = 200      // GET api/log
	maxLogLines     = 10000
)

// Server is the HTTP/WebSocket server.
type Server struct {
	opts     Options
	deps     Deps
	log      *slog.Logger
	tune     tuning
	insecure bool

	auth    *authenticator
	hosts   *hostCheck
	proxies []netip.Prefix
	csp     string // Content-Security-Policy of every response
	static  *staticFiles
	hub     *hub
	handler http.Handler

	ctx    context.Context // server lifetime: hub pumps, installs, resets
	cancel context.CancelFunc
	tasks  sync.WaitGroup // background operations started by clients

	closeOnce sync.Once
}

// New creates a server.
func New(opts Options, deps Deps) (*Server, error) { return newServer(opts, deps, nil) }

func newServer(opts Options, deps Deps, adjust func(*tuning)) (*Server, error) {
	if deps.Machine == nil || deps.Panel == nil || deps.Systems == nil {
		return nil, errors.New("web: Machine, Panel and Systems are required")
	}
	for _, p := range opts.AllowOrigins {
		if _, err := path.Match(p, ""); err != nil {
			return nil, fmt.Errorf("web: PDP11_ALLOW_ORIGINS: bad pattern %q: %w", p, err)
		}
	}
	hostname, _ := os.Hostname()
	hosts, err := newHostCheck(hostname, opts.Hosts, opts.AllowOrigins)
	if err != nil {
		return nil, err
	}
	hosts.addListenHost(opts.Listen)
	proxies, err := parseProxies(opts.TrustedProxies)
	if err != nil {
		return nil, err
	}
	if opts.FrameAncestors == "" {
		opts.FrameAncestors = "'self'"
	}
	if err := checkFrameAncestors(opts.FrameAncestors); err != nil {
		return nil, err
	}
	if opts.FrameHz <= 0 {
		opts.FrameHz = 60
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	opts.Version = withBuildID(opts.Version, opts.Static)
	s := &Server{
		opts:     opts,
		deps:     deps,
		log:      opts.Logger.With("component", "web"),
		tune:     defaultTuning(),
		insecure: opts.Password == "" && !loopbackListener(opts.Listen),
		hosts:    hosts,
		proxies:  proxies,
		csp:      contentSecurityPolicy(opts.FrameAncestors),
	}
	if adjust != nil {
		adjust(&s.tune)
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.auth = newAuthenticator(opts.Password, opts.HealthPublic, proxies, s.log)
	s.static = newStaticFiles(opts.Static, opts.Version, s.log)
	s.hub = newHub(s)
	s.handler = s.routes()
	s.hub.start()
	return s, nil
}

// Handler returns the HTTP handler (UI, /api, /ws).
func (s *Server) Handler() http.Handler { return s.handler }

// Run listens on opts.Listen until ctx is done, then shuts down gracefully.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.opts.Listen)
	if err != nil {
		s.Close()
		return fmt.Errorf("web: %w", err)
	}
	return s.Serve(ctx, ln)
}

// Serve is Run on a listener the caller opened (pdp11d serve binds it
// before the panel can power on); it closes ln.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	if s.opts.Password != "" {
		s.log.Info("authentication: on")
	} else {
		s.log.Info("authentication: off", "allowedHosts", s.hosts.describe())
	}
	if len(s.proxies) > 0 {
		s.log.Info("trusted proxies: their X-Forwarded-For names the client", "proxies", fmt.Sprint(s.proxies))
	}
	if s.insecure {
		s.log.Warn("no password set on a non-loopback listener: anyone on the network can control the machine (set PDP11_PASSWORD)",
			"listen", ln.Addr().String())
	}
	s.log.Info("listening", "addr", ln.Addr().String(), "version", s.opts.Version)
	srv := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelDebug),
		BaseContext:       func(net.Listener) context.Context { return s.ctx },
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(sendBufferListener{ln, sendBuffer}) }()
	select {
	case err := <-errc:
		s.Close()
		return fmt.Errorf("web: %w", err)
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), s.tune.shutdownWait)
	defer cancel()
	err := srv.Shutdown(sctx) // stops accepting; WebSockets are hijacked, Close ends them
	s.Close()
	if serr := <-errc; serr != nil && !errors.Is(serr, http.ErrServerClosed) && err == nil {
		err = serr
	}
	if err != nil {
		return fmt.Errorf("web: shutdown: %w", err)
	}
	return nil
}

// Close disconnects every WebSocket client ("going away", so browsers
// reconnect to the next server) and stops the background work. Run calls
// it; call it yourself only when serving Handler() elsewhere.
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		s.hub.shutdown()
		s.cancel()
		done := make(chan struct{})
		go func() { s.tasks.Wait(); s.hub.wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(s.tune.shutdownWait):
			s.log.Warn("web: background work did not stop in time")
		}
	})
	return nil
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/systems", s.handleSystems)
	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("GET /api/log", s.handleLog)
	mux.HandleFunc("GET /ws", s.handleWS)
	mux.HandleFunc("GET /{$}", s.static.serveIndex)
	mux.HandleFunc("GET /index.html", s.static.serveIndex)
	mux.HandleFunc("GET /assets/", s.static.serveAsset)
	return s.securityHeaders(s.checkHost(s.auth.middleware(mux)))
}

// remote names the client of r in log lines: RemoteAddr, or behind a
// trusted proxy the client address the proxy reported.
func (s *Server) remote(r *http.Request) string {
	if a, proxied := clientAddr(r, s.proxies); proxied {
		return a.String()
	}
	return r.RemoteAddr
}

// sendBuffer bounds the kernel's send buffer per connection. Autotuning
// would let a stalled browser absorb megabytes before its client queue
// even starts to grow; with this bound it is disconnected after about
// maxQueuedBytes + sendBuffer. 256 KiB still carries far more than the UI
// needs on any link (the assets are about 1 MB, live traffic a few KB/s).
const sendBuffer = 256 << 10

// sendBufferListener applies sendBuffer to accepted TCP connections.
type sendBufferListener struct {
	net.Listener
	size int
}

func (l sendBufferListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if tc, ok := c.(*net.TCPConn); ok && err == nil {
		_ = tc.SetWriteBuffer(l.size) // best effort
	}
	return c, err
}

// securityHeaders sets headers every response should carry (DESIGN.md
// §10). X-Frame-Options covers browsers without CSP frame-ancestors; it
// can only express the default ('self').
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	sameOrigin := strings.EqualFold(strings.TrimSpace(s.opts.FrameAncestors), "'self'")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", s.csp)
		if sameOrigin {
			h.Set("X-Frame-Options", "SAMEORIGIN")
		}
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// contentSecurityPolicy allows only the appliance's own files and
// WebSocket. xterm.js's DOM renderer injects <style> elements, hence
// 'unsafe-inline' for styles (scripts are never inline).
func contentSecurityPolicy(frameAncestors string) string {
	return "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; " +
		"font-src 'self' data:; connect-src 'self' ws: wss:; frame-ancestors " + strings.TrimSpace(frameAncestors) +
		"; object-src 'none'; base-uri 'self'; form-action 'self'"
}

// checkFrameAncestors accepts one CSP source list for PDP11_FRAME_ANCESTORS:
// a ';' would start another directive and a ',' another policy, and the
// keywords need their quotes ('self', not self).
func checkFrameAncestors(v string) error {
	bad := strings.TrimSpace(v) == ""
	for _, c := range v {
		bad = bad || c < ' ' || c > '~' || c == ';' || c == ','
	}
	for _, f := range strings.Fields(v) {
		bad = bad || strings.EqualFold(f, "self") || strings.EqualFold(f, "none")
	}
	if bad {
		return fmt.Errorf("web: PDP11_FRAME_ANCESTORS %q is not a CSP source list (e.g. \"'self' https://dashboard.example.com\")", v)
	}
	return nil
}

// loopbackListener reports whether a listen address only accepts local
// connections.
func loopbackListener(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i] // zone
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
