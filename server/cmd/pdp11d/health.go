package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// health probes a running pdp11d (Docker HEALTHCHECK, scripts): it GETs
// api/status from the address in PDP11_LISTEN (or --url) and exits 0 when
// the daemon answers {"ok": true}. PDP11_PASSWORD, if set, is sent as Basic
// auth, so the check also works without PDP11_HEALTH_PUBLIC.
func health(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("pdp11d health", flag.ContinueOnError)
	fs.SetOutput(stderr)
	url := fs.String("url", "", "status URL (default: api/status on the PDP11_LISTEN address)")
	timeout := fs.Duration("timeout", 5*time.Second, "request timeout")
	cfg, _, err := loadConfig(fs, args, getenv)
	if err != nil {
		fmt.Fprintln(stderr, "pdp11d health:", err)
		return 2
	}
	target := *url
	if target == "" {
		target, err = statusURL(cfg.Listen)
		if err != nil {
			fmt.Fprintln(stderr, "pdp11d health:", err)
			return 2
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		fmt.Fprintln(stderr, "pdp11d health:", err)
		return 2
	}
	if cfg.Password != "" {
		req.SetBasicAuth("pdp11", cfg.Password)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintln(stderr, "pdp11d health:", err)
		return 1
	}
	defer resp.Body.Close()
	var st struct {
		OK      bool   `json:"ok"`
		Version string `json:"version"`
		Run     string `json:"run"`
		System  string `json:"system"`
		Power   string `json:"power"`
	}
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(stderr, "pdp11d health: %s: HTTP %d\n", target, resp.StatusCode)
		return 1
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&st); err != nil || !st.OK {
		fmt.Fprintf(stderr, "pdp11d health: %s: unexpected reply\n", target)
		return 1
	}
	fmt.Fprintf(stdout, "ok version=%s power=%s run=%s system=%s\n", st.Version, st.Power, st.Run, st.System)
	return 0
}

// statusURL turns a listen address into the URL of its api/status; an
// unspecified host (":8080", "0.0.0.0:8080", "[::]:8080") means loopback.
func statusURL(listen string) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("PDP11_LISTEN %q: %w", listen, err)
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
		if ip != nil && ip.To4() == nil {
			host = "::1"
		}
	}
	return "http://" + net.JoinHostPort(host, port) + "/api/status", nil
}
