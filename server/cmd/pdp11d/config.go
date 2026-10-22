package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"

	"pdp11emu/server/internal/simh"
)

// config holds the settings of DESIGN §5: environment variables, overridden
// by "serve" flags of the same name in lower-case-dash form.
type config struct {
	Listen         string
	Data           string
	SIMH           string
	Cache          string
	Systems        string
	Web            string
	Password       string
	AllowOrigins   []string
	Hosts          []string
	FrameAncestors string
	TrustedProxies []string
	HealthPublic   bool
	System         string
	AcceptLicenses []string
	Autostart      bool
	ShutdownOnStop bool
	TTYTelnet      string
	PortBase       int
	SHM            string
	FrameHz        int
	LogLevel       slog.Level
}

// setting describes one configuration value.
type setting struct {
	env, flag, def, usage string
	set                   func(c *config, v string) error
}

func str(dst func(*config) *string) func(*config, string) error {
	return func(c *config, v string) error { *dst(c) = v; return nil }
}

func boolean(dst func(*config) *bool) func(*config, string) error {
	return func(c *config, v string) error {
		b, err := parseBool(v)
		*dst(c) = b
		return err
	}
}

func list(dst func(*config) *[]string) func(*config, string) error {
	return func(c *config, v string) error {
		*dst(c) = nil
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				*dst(c) = append(*dst(c), s)
			}
		}
		return nil
	}
}

func integer(dst func(*config) *int, lo, hi int) func(*config, string) error {
	return func(c *config, v string) error {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n < lo || n > hi {
			return fmt.Errorf("%q is not a number in %d..%d", v, lo, hi)
		}
		*dst(c) = n
		return nil
	}
}

func parseBool(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off", "":
		return false, nil
	}
	return false, fmt.Errorf("%q is not a boolean", v)
}

func parseLevel(v string) (slog.Level, error) {
	var l slog.Level
	err := l.UnmarshalText([]byte(strings.TrimSpace(v)))
	if err != nil {
		return 0, fmt.Errorf("%q is not a log level (debug, info, warn, error)", v)
	}
	return l, nil
}

// defaultDataDir is /data in a container, /var/lib/pdp11 otherwise.
func defaultDataDir() string {
	for _, f := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(f); err == nil {
			return "/data"
		}
	}
	return "/var/lib/pdp11"
}

func settings() []setting {
	return []setting{
		{"PDP11_LISTEN", "listen", ":8080", "HTTP listen address", str(func(c *config) *string { return &c.Listen })},
		{"PDP11_DATA", "data", defaultDataDir(), "data directory", str(func(c *config) *string { return &c.Data })},
		{"PDP11_SIMH", "simh", "/opt/pdp11/bin/pdp11", "patched simulator binary", str(func(c *config) *string { return &c.SIMH })},
		{"PDP11_CACHE", "cache", "/opt/pdp11/cache", "read-only archive cache searched before downloading", str(func(c *config) *string { return &c.Cache })},
		{"PDP11_SYSTEMS", "systems", "", "catalog directory that replaces the embedded catalog", str(func(c *config) *string { return &c.Systems })},
		{"PDP11_WEB", "web", "", "serve the UI from this directory (development)", str(func(c *config) *string { return &c.Web })},
		{"PDP11_PASSWORD", "password", "", "HTTP Basic auth password (empty: no authentication)", str(func(c *config) *string { return &c.Password })},
		{"PDP11_ALLOW_ORIGINS", "allow-origins", "", "extra allowed WebSocket Origin host patterns (comma list)", list(func(c *config) *[]string { return &c.AllowOrigins })},
		{"PDP11_HOSTS", "hosts", "", "extra host names the UI may be opened by while no password is set (comma list; *.example.com: every subdomain; *: no check)", list(func(c *config) *[]string { return &c.Hosts })},
		{"PDP11_FRAME_ANCESTORS", "frame-ancestors", "'self'", "CSP frame-ancestors source list: the pages that may show the UI in a frame", str(func(c *config) *string { return &c.FrameAncestors })},
		{"PDP11_TRUSTED_PROXIES", "trusted-proxies", "", "reverse proxies whose X-Forwarded-For names the client (comma list of IP addresses or CIDRs)", list(func(c *config) *[]string { return &c.TrustedProxies })},
		{"PDP11_HEALTH_PUBLIC", "health-public", "false", "serve GET /api/status without authentication", boolean(func(c *config) *bool { return &c.HealthPublic })},
		{"PDP11_SYSTEM", "system", "", "system selected on first start (default: the catalog default)", str(func(c *config) *string { return &c.System })},
		{"PDP11_ACCEPT_LICENSES", "accept-licenses", "", "system ids whose licences are accepted for unattended install (comma list)", list(func(c *config) *[]string { return &c.AcceptLicenses })},
		{"PDP11_AUTOSTART", "autostart", "true", "power on at startup if the key is at POWER/LOCK", boolean(func(c *config) *bool { return &c.Autostart })},
		{"PDP11_SHUTDOWN_ON_STOP", "shutdown-on-stop", "false", "on SIGTERM run the OS shutdown script before powering off", boolean(func(c *config) *bool { return &c.ShutdownOnStop })},
		{"PDP11_TTY_TELNET", "tty-telnet", "", "telnet listener for the system's extra terminal lines, e.g. :4000 (unauthenticated)", str(func(c *config) *string { return &c.TTYTelnet })},
		{"PDP11_PORT_BASE", "port-base", "23400", "localhost console port", integer(func(c *config) *int { return &c.PortBase }, 1, 65535)},
		{"PDP11_SHM", "shm", "/dev/shm/pdp11d-panel", "panel export file", str(func(c *config) *string { return &c.SHM })},
		{"PDP11_FRAME_HZ", "frame-hz", "60", "lamp frame rate (30-120)", integer(func(c *config) *int { return &c.FrameHz }, 30, 120)},
		{"PDP11_LOG", "log", "info", "log level: debug, info, warn, error", func(c *config, v string) error {
			l, err := parseLevel(v)
			c.LogLevel = l
			return err
		}},
	}
}

// loadConfig reads the environment (getenv), then the flags in args, which
// override it (extra flags of a sub-command may be defined on fs before).
// It returns the remaining arguments.
func loadConfig(fs *flag.FlagSet, args []string, getenv func(string) string) (*config, []string, error) {
	c := &config{}
	var errs []error
	values := map[string]*string{}
	all := settings()
	for _, s := range all {
		v := s.def
		if e := getenv(s.env); e != "" {
			v = e
		}
		if err := s.set(c, v); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.env, err))
		}
		values[s.flag] = fs.String(s.flag, v, s.usage+" ("+s.env+")")
	}
	if len(errs) > 0 {
		return nil, nil, errors.Join(errs...)
	}
	if err := fs.Parse(args); err != nil {
		return nil, nil, err
	}
	fs.Visit(func(f *flag.Flag) {
		for _, s := range all {
			if s.flag == f.Name {
				if err := s.set(c, *values[s.flag]); err != nil {
					errs = append(errs, fmt.Errorf("--%s: %w", s.flag, err))
				}
			}
		}
	})
	if len(errs) > 0 {
		return nil, nil, errors.Join(errs...)
	}
	return c, fs.Args(), c.validate()
}

func (c *config) validate() error {
	if c.Listen == "" || c.Data == "" || c.SIMH == "" || c.SHM == "" {
		return errors.New("PDP11_LISTEN, PDP11_DATA, PDP11_SIMH and PDP11_SHM must not be empty")
	}
	if err := simh.CheckPath(c.SHM); err != nil {
		return fmt.Errorf("PDP11_SHM: %w", err)
	}
	// The working disk images are SimH arguments too ($DATA/systems/...).
	if err := simh.CheckPath(c.Data); err != nil {
		return fmt.Errorf("PDP11_DATA: %w", err)
	}
	if c.TTYTelnet != "" {
		if err := simh.CheckPath(c.TTYTelnet); err != nil {
			return fmt.Errorf("PDP11_TTY_TELNET: %w", err)
		}
		port := c.TTYTelnet
		if _, p, err := net.SplitHostPort(c.TTYTelnet); err == nil {
			port = p
		}
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("PDP11_TTY_TELNET: %q is not [host]:port or a port", c.TTYTelnet)
		}
	}
	return nil
}

// cacheDirs is the list of read-only caches (PDP11_CACHE may list several,
// separated by ':').
func (c *config) cacheDirs() []string {
	var out []string
	for _, d := range strings.Split(c.Cache, ":") {
		if d = strings.TrimSpace(d); d != "" {
			out = append(out, d)
		}
	}
	return out
}

func newLogger(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
}
