package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Authentication (DESIGN.md §10): with a password, every request needs
// HTTP Basic auth (any user name) or the session cookie set after a
// successful Basic auth (pop-out windows and WebSocket handshakes use it).
// Session tokens are self-authenticating (random nonce + HMAC with a key
// made at start), so they need no server-side storage and become invalid
// when pdp11d restarts or the password changes.

const sessionCookie = "pdp11d_session"

// Brute-force protection: requests that carry wrong credentials count;
// after authFailLimit of them within authFailWindow a client gets 429,
// even with the right password (else the limit would not limit; valid
// session cookies keep working).
const (
	authFailLimit  = 20
	authFailWindow = time.Minute
	authFailTrack  = 4096 // clients tracked at most
)

type authenticator struct {
	enabled      bool
	healthPublic bool
	proxies      []netip.Prefix // PDP11_TRUSTED_PROXIES
	pwHash       [32]byte
	key          [32]byte
	fails        failLimiter
}

func newAuthenticator(password string, healthPublic bool, proxies []netip.Prefix, log *slog.Logger) *authenticator {
	a := &authenticator{enabled: password != "", healthPublic: healthPublic, proxies: proxies}
	a.pwHash = sha256.Sum256([]byte(password))
	if _, err := rand.Read(a.key[:]); err != nil {
		panic("web: no randomness for session keys: " + err.Error())
	}
	a.fails.m = map[string]*failEntry{}
	a.fails.now = time.Now
	a.fails.max = authFailTrack
	a.fails.log = log
	return a
}

func (a *authenticator) middleware(next http.Handler) http.Handler {
	if !a.enabled {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := limiterKey(r, a.proxies)
		ok := a.validSession(r)
		_, pass, basic := r.BasicAuth()
		blocked := basic && !ok && a.fails.blocked(key)
		if !ok && basic && !blocked {
			if a.checkPassword(pass) {
				ok = true
				a.setSession(w, r)
			} else {
				a.fails.fail(key)
			}
		}
		if ok || a.public(r) {
			next.ServeHTTP(w, r)
			return
		}
		if blocked {
			w.Header().Set("Retry-After", strconv.Itoa(int(authFailWindow/time.Second)))
			http.Error(w, "too many failed logins; try again later", http.StatusTooManyRequests)
			return
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="PDP-11/70", charset="UTF-8"`)
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "authentication required", http.StatusUnauthorized)
	})
}

// public: the health check, if configured.
func (a *authenticator) public(r *http.Request) bool {
	return a.healthPublic && r.URL.Path == "/api/status" && (r.Method == http.MethodGet || r.Method == http.MethodHead)
}

func (a *authenticator) checkPassword(pass string) bool {
	h := sha256.Sum256([]byte(pass))
	return subtle.ConstantTimeCompare(h[:], a.pwHash[:]) == 1
}

// newToken returns nonce || HMAC(key, nonce), base64url.
func (a *authenticator) newToken() string {
	var b [16 + sha256.Size]byte
	if _, err := rand.Read(b[:16]); err != nil {
		panic("web: no randomness for session tokens: " + err.Error())
	}
	m := hmac.New(sha256.New, a.key[:])
	m.Write(b[:16])
	copy(b[16:], m.Sum(nil))
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func (a *authenticator) validSession(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	b, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil || len(b) != 16+sha256.Size {
		return false
	}
	m := hmac.New(sha256.New, a.key[:])
	m.Write(b[:16])
	return hmac.Equal(m.Sum(nil), b[16:])
}

func (a *authenticator) setSession(w http.ResponseWriter, r *http.Request) {
	secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    a.newToken(),
		Path:     cookiePath(r),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
}

// cookiePath scopes the cookie to the application: behind a reverse proxy
// that strips a sub-path and says so (X-Forwarded-Prefix) to that path,
// else to the whole site.
func cookiePath(r *http.Request) string {
	p := r.Header.Get("X-Forwarded-Prefix")
	if p == "" || !strings.HasPrefix(p, "/") || strings.ContainsAny(p, ";,\"\\ \t\r\n") {
		return "/"
	}
	if u, err := url.PathUnescape(p); err != nil || u != p {
		return "/"
	}
	return path.Clean(p)
}

// parseProxies parses PDP11_TRUSTED_PROXIES: IP addresses and CIDRs.
func parseProxies(list []string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, s := range list {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			a, aerr := netip.ParseAddr(s)
			if aerr != nil {
				return nil, fmt.Errorf("web: PDP11_TRUSTED_PROXIES: %q is not an IP address or CIDR", s)
			}
			a = a.Unmap().WithZone("")
			p = netip.PrefixFrom(a, a.BitLen())
		} else if p.Addr().Is4In6() && p.Bits() >= 96 {
			p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// clientAddr returns the address of the client that sent r: the peer's
// or, when the peer is a trusted proxy, the rightmost X-Forwarded-For
// entry that is not a trusted proxy itself (entries further left come from
// the client and are never believed); proxied says which. Without the
// header, or with only trusted entries, it is the peer's. Forwarded (RFC
// 7239) is not parsed. The address is invalid if RemoteAddr is not one.
func clientAddr(r *http.Request, trusted []netip.Prefix) (addr netip.Addr, proxied bool) {
	peer := parseAddr(r.RemoteAddr)
	if !isTrusted(peer, trusted) {
		return peer, false
	}
	var hops []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		for _, h := range strings.Split(v, ",") {
			if h = strings.TrimSpace(h); h != "" {
				hops = append(hops, h)
			}
		}
	}
	for i := len(hops) - 1; i >= 0; i-- {
		a := parseAddr(hops[i])
		if !a.IsValid() {
			break // not written by a proxy we know: fall back to the peer
		}
		if !isTrusted(a, trusted) {
			return a, true
		}
	}
	return peer, false
}

// parseAddr accepts an IP address, with or without brackets and port.
func parseAddr(s string) netip.Addr {
	a, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(s, "["), "]"))
	if err != nil {
		ap, err := netip.ParseAddrPort(s)
		if err != nil {
			return netip.Addr{}
		}
		a = ap.Addr()
	}
	return a.Unmap().WithZone("")
}

func isTrusted(a netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// limiterKey is the failed-login limiter's key for the client of r: its
// IPv4 address, or the /64 of its IPv6 address (a host is usually given
// a whole /64).
func limiterKey(r *http.Request, trusted []netip.Prefix) string {
	a, _ := clientAddr(r, trusted)
	switch {
	case !a.IsValid():
		return r.RemoteAddr
	case a.Is4():
		return a.String()
	}
	return netip.PrefixFrom(a, 64).Masked().String()
}

type failEntry struct {
	count int
	start time.Time
}

// failLimiter counts failed logins per client key within authFailWindow,
// for at most max keys. When the table is full, expired entries are
// dropped first, then those that are not blocked; a blocked entry is
// never dropped before it expires, so many addresses cannot lift a block.
// While only blocked entries remain, a new key counts as blocked: under
// such an attack new logins wait, valid session cookies keep working.
type failLimiter struct {
	mu        sync.Mutex
	m         map[string]*failEntry
	now       func() time.Time
	max       int
	fullUntil time.Time // the table holds only blocked entries until then
	warned    time.Time // last warning about a full table
	log       *slog.Logger
}

func (f *failLimiter) blocked(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	if e := f.m[key]; e != nil {
		if now.Sub(e.start) < authFailWindow {
			return e.count >= authFailLimit
		}
		delete(f.m, key)
	}
	return !f.room(now)
}

func (f *failLimiter) fail(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	e := f.m[key]
	if e != nil && now.Sub(e.start) >= authFailWindow {
		delete(f.m, key)
		e = nil
	}
	if e == nil {
		if !f.room(now) {
			return // a new key counts as blocked anyway
		}
		e = &failEntry{start: now}
		f.m[key] = e
	}
	e.count++
}

// room reports whether the table can take a new key, making room when it
// is full. Called with mu held.
func (f *failLimiter) room(now time.Time) bool {
	if len(f.m) < f.max {
		return true
	}
	if now.Before(f.fullUntil) {
		return false // nothing expired since the table held only blocked entries
	}
	for k, e := range f.m {
		if now.Sub(e.start) >= authFailWindow {
			delete(f.m, k)
		}
	}
	if len(f.m) < f.max {
		return true
	}
	for k, e := range f.m {
		if e.count < authFailLimit {
			delete(f.m, k)
		}
	}
	full := len(f.m) >= f.max
	if full {
		oldest := now
		for _, e := range f.m {
			if e.start.Before(oldest) {
				oldest = e.start
			}
		}
		f.fullUntil = oldest.Add(authFailWindow)
	}
	if now.Sub(f.warned) >= time.Minute {
		f.warned = now
		if full {
			f.log.Warn("failed logins from too many addresses: logins from new addresses are refused until blocks expire (valid sessions keep working)",
				"blocked", len(f.m))
		} else {
			f.log.Warn("failed logins from too many addresses: the limiter forgot the addresses that were not blocked",
				"blocked", len(f.m))
		}
	}
	return !full
}

// originAllowed implements the WebSocket Origin check: the Origin must
// name the host the request was sent to, or match PDP11_ALLOW_ORIGINS
// (path.Match patterns against the host, or against scheme://host when
// the pattern contains "://"). Requests without Origin are not from a
// browser (browsers always send it on WebSocket handshakes), so they
// cannot be cross-site; they still need the password.
func originAllowed(r *http.Request, allow []string) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) ||
		strings.EqualFold(stripDefaultPort(u.Host, u.Scheme), stripDefaultPort(r.Host, u.Scheme)) {
		return true
	}
	for _, pat := range allow {
		target := u.Host
		if strings.Contains(pat, "://") {
			target = u.Scheme + "://" + u.Host
		}
		if ok, err := path.Match(strings.ToLower(pat), strings.ToLower(target)); err == nil && ok {
			return true
		}
	}
	return false
}

func stripDefaultPort(host, scheme string) string {
	switch {
	case scheme == "http" && strings.HasSuffix(host, ":80"):
		return strings.TrimSuffix(host, ":80")
	case scheme == "https" && strings.HasSuffix(host, ":443"):
		return strings.TrimSuffix(host, ":443")
	}
	return host
}
