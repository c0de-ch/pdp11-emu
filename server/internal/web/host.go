package web

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"path"
	"strings"
	"sync"
	"time"
)

// Host check (DESIGN.md §10): while no password is set, every request must
// name a host the appliance is known by. A page that DNS rebinding moved
// onto pdp11d's address is same-origin with itself, so its WebSocket
// passes the Origin check; only its Host header, which names the
// attacker's domain, gives it away. With a password there is no check: a
// rebound page has neither the Basic credentials nor the cookie.

// hostWarnTrack bounds the refused host names whose warnings are rate
// limited, so that made-up names cannot flood the log.
const hostWarnTrack = 256

type hostCheck struct {
	anyHost  bool            // PDP11_HOSTS=*
	names    map[string]bool // this machine's names and PDP11_HOSTS entries
	suffixes []string        // PDP11_HOSTS "*.example.com" entries as ".example.com"
	patterns []string        // host parts of the PDP11_ALLOW_ORIGINS patterns (path.Match)
	allowed  []string        // what passes, for the startup log

	mu     sync.Mutex
	warned map[string]time.Time // refused host → last warning
}

// newHostCheck builds the check. Always allowed: an empty Host, IP
// literals, localhost and *.localhost. Also allowed: the machine's host
// name (as is, its first label and <first label>.local), the PDP11_HOSTS
// entries and the host parts of the PDP11_ALLOW_ORIGINS patterns (and,
// added by the server, the host name it listens on).
func newHostCheck(hostname string, hosts, origins []string) (*hostCheck, error) {
	hc := &hostCheck{names: map[string]bool{}, warned: map[string]time.Time{},
		allowed: []string{"IP addresses", "localhost", "*.localhost"}}
	if h := normalizeHost(hostname); h != "" && !isIP(h) {
		first, _, _ := strings.Cut(h, ".")
		hc.add(h)
		hc.add(first)
		hc.add(first + ".local")
	}
	for _, e := range hosts {
		h := strings.ToLower(strings.TrimSpace(e))
		if h == "*" {
			hc.anyHost = true
			continue
		}
		h = strings.TrimSuffix(h, ".")
		name, sub := strings.CutPrefix(h, "*.")
		switch {
		case isIP(strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")):
			// always allowed
		case !validHostName(name):
			return nil, fmt.Errorf(`web: PDP11_HOSTS: %q is not a host name (no scheme or port; "*.example.com" allows every subdomain)`, e)
		case sub:
			hc.suffixes = append(hc.suffixes, "."+name)
			hc.allowed = append(hc.allowed, h)
		default:
			hc.add(h)
		}
	}
	for _, o := range origins {
		p := originHostPattern(o)
		hc.patterns = append(hc.patterns, p)
		hc.allowed = append(hc.allowed, p)
	}
	return hc, nil
}

// add allows a (normalized) host name.
func (hc *hostCheck) add(name string) {
	if !hc.names[name] {
		hc.names[name] = true
		hc.allowed = append(hc.allowed, name)
	}
}

// addListenHost allows the host name of the listen address
// ("pdp11.lan:8080"): it names this machine, and "pdp11d health" sends it.
func (hc *hostCheck) addListenHost(listen string) {
	host, _, err := net.SplitHostPort(listen)
	if h := normalizeHost(host); err == nil && h != "" && !hc.allows(h) {
		hc.add(h)
	}
}

// normalizeHost reduces a Host header to the bare lower-case name: no
// port, no brackets around an IPv6 literal, no trailing dot.
func normalizeHost(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	} else if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		h = h[1 : len(h)-1]
	}
	return strings.TrimSuffix(strings.ToLower(h), ".")
}

func isIP(h string) bool {
	_, err := netip.ParseAddr(h)
	return err == nil
}

// validHostName: dot-separated labels of letters, digits, '-' and '_'
// (internationalized names in their xn-- form, as browsers send them).
func validHostName(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return false
			}
		}
	}
	return true
}

// originHostPattern returns the host part of a PDP11_ALLOW_ORIGINS
// pattern: "https://pdp.example.com:8443" → "pdp.example.com",
// "*.example.com" → "*.example.com".
func originHostPattern(p string) string {
	p = strings.ToLower(p)
	if _, rest, ok := strings.Cut(p, "://"); ok {
		p = rest
	}
	if i := strings.LastIndexByte(p, ':'); i > strings.LastIndexByte(p, ']') {
		p = p[:i]
	}
	if strings.HasPrefix(p, "[") && strings.HasSuffix(p, "]") {
		p = p[1 : len(p)-1]
	}
	return strings.TrimSuffix(p, ".")
}

// allows reports whether a request for host (normalized) may pass.
func (hc *hostCheck) allows(host string) bool {
	if hc.anyHost || host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") || hc.names[host] || isIP(host) {
		return true
	}
	for _, s := range hc.suffixes {
		if len(host) > len(s) && strings.HasSuffix(host, s) {
			return true
		}
	}
	for _, p := range hc.patterns {
		if ok, err := path.Match(p, host); err == nil && ok {
			return true
		}
	}
	return false
}

// describe lists what passes the check, for the startup log.
func (hc *hostCheck) describe() string {
	if hc.anyHost {
		return "any (PDP11_HOSTS=*)"
	}
	return strings.Join(hc.allowed, ", ")
}

// checkHost refuses, while no password is set, requests for host names
// that are not allowed (HTTP 421), before authentication and routing.
func (s *Server) checkHost(next http.Handler) http.Handler {
	if s.opts.Password != "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := normalizeHost(r.Host)
		if s.hosts.allows(host) {
			next.ServeHTTP(w, r)
			return
		}
		logged := host
		if len(logged) > 253 { // longer than any DNS name
			logged = logged[:253]
		}
		if s.hosts.shouldWarn(logged, time.Now()) {
			s.log.Warn("request refused: the host name is not allowed while no password is set (see PDP11_HOSTS)",
				"host", logged, "remote", s.remote(r))
		}
		http.Error(w, fmt.Sprintf("pdp11d: the host name %q is not allowed while no password is set. Add it to PDP11_HOSTS, or set PDP11_PASSWORD.", host),
			http.StatusMisdirectedRequest)
	})
}

// shouldWarn rate-limits the warning about a refused host to once a
// minute; beyond hostWarnTrack names within a minute, new ones are not
// logged.
func (hc *hostCheck) shouldWarn(host string, now time.Time) bool {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	last, seen := hc.warned[host]
	if seen && now.Sub(last) < time.Minute {
		return false
	}
	if !seen && len(hc.warned) >= hostWarnTrack {
		for h, t := range hc.warned {
			if now.Sub(t) >= time.Minute {
				delete(hc.warned, h)
			}
		}
		if len(hc.warned) >= hostWarnTrack {
			return false
		}
	}
	hc.warned[host] = now
	return true
}
