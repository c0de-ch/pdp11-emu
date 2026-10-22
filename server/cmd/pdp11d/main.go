// Command pdp11d runs the PDP-11/70 appliance: it supervises the patched
// SimH simulator, implements the front panel and serves the web UI.
//
//	pdp11d [serve] [flags]                      run the appliance (default)
//	pdp11d fetch [--cache DIR] [id...]          download + verify archives into a cache
//	pdp11d install [--accept-license] id...     install systems into the data directory
//	pdp11d check [dir]                          validate the catalog
//	pdp11d health [--url URL] [--timeout D]     probe a running pdp11d (container health checks)
//	pdp11d version
//
// Settings come from PDP11_* environment variables (docs/DESIGN.md §5);
// flags of the same name in lower-case-dash form override them.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"pdp11emu"
	"pdp11emu/server/internal/api"
	"pdp11emu/server/internal/catalog"
	"pdp11emu/server/internal/panel"
	"pdp11emu/server/internal/simh"
	"pdp11emu/server/internal/systems"
	"pdp11emu/server/internal/web"
)

var version = "0.1.0-dev"

// Shutdown budgets (DESIGN §6: the OS shutdown script gets ≤ 45 s; the
// service manager allows 60-90 s in total).
const (
	shutdownScriptTimeout = 45 * time.Second
	webStopTimeout        = 10 * time.Second
	powerOffTimeout       = 20 * time.Second
)

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "serve":
		return serve(args, getenv, stderr)
	case "fetch":
		return fetch(args, getenv, stdout, stderr)
	case "install":
		return install(args, getenv, stdout, stderr)
	case "check":
		return check(args, getenv, stdout, stderr)
	case "health":
		return health(args, getenv, stdout, stderr)
	case "version":
		fmt.Fprintln(stdout, "pdp11d", version)
		return 0
	case "help":
		usage(stdout)
		return 0
	}
	fmt.Fprintf(stderr, "pdp11d: unknown command %q\n", cmd)
	usage(stderr)
	return 2
}

func usage(w io.Writer) {
	fmt.Fprint(w, `usage:
  pdp11d [serve] [flags]                    run the appliance
  pdp11d fetch [--cache DIR] [id...]        download and verify archives into a cache
                                            (default: all redistributable systems)
  pdp11d install [--accept-license] id...   install systems into the data directory
  pdp11d check [dir]                        validate the catalog
  pdp11d health [--url URL] [--timeout D]   exit 0 if the local pdp11d answers api/status
  pdp11d version
Run "pdp11d serve -h" for the settings (PDP11_* environment variables).
`)
}

// setup parses the settings of a sub-command and loads the catalog.
func setup(name string, args []string, getenv func(string) string, stderr io.Writer, extra func(*flag.FlagSet)) (*config, []string, *catalog.Catalog, *slog.Logger, error) {
	fs := flag.NewFlagSet("pdp11d "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	if extra != nil {
		extra(fs)
	}
	cfg, rest, err := loadConfig(fs, args, getenv)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	log := newLogger(stderr, cfg.LogLevel)
	cat, err := loadCatalog(cfg.Systems)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return cfg, rest, cat, log, nil
}

// loadCatalog loads and validates the embedded catalog, or dir (PDP11_SYSTEMS).
func loadCatalog(dir string) (*catalog.Catalog, error) {
	var fsys fs.FS
	if dir != "" {
		fsys = os.DirFS(dir)
	} else {
		sub, err := fs.Sub(pdp11emu.Systems, "systems")
		if err != nil {
			return nil, err
		}
		fsys = sub
	}
	cat, err := catalog.Load(fsys)
	if err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	if errs := cat.Validate(); len(errs) > 0 {
		return nil, fmt.Errorf("catalog %s is invalid:\n  %w", catalogName(dir), errors.Join(errs...))
	}
	return cat, nil
}

func catalogName(dir string) string {
	if dir == "" {
		return "(embedded)"
	}
	return dir
}

// staticFS is the UI: the embedded files, or a directory (PDP11_WEB).
func staticFS(dir string) (fs.FS, error) {
	if dir != "" {
		if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
			return nil, fmt.Errorf("PDP11_WEB: %w", err)
		}
		return os.DirFS(dir), nil
	}
	return fs.Sub(pdp11emu.Web, "web")
}

// ---- serve ------------------------------------------------------------------

func serve(args []string, getenv func(string) string, stderr io.Writer) int {
	cfg, _, cat, log, err := setup("serve", args, getenv, stderr, nil)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(stderr, "pdp11d:", err)
		return 2
	}
	log.Info("pdp11d starting", "version", version, "data", cfg.Data, "simulator", cfg.SIMH, "listen", cfg.Listen)
	// Before anything writes into the data directory: a second pdp11d on
	// it would kill this one's simulator as a stale one, or share its disks.
	lock, err := lockDataDir(cfg.Data)
	var busy *busyError
	switch {
	case errors.As(err, &busy):
		fmt.Fprintln(stderr, "pdp11d:", err)
		return 2
	case err != nil:
		log.Error("cannot start", "err", err)
		return 1
	}
	defer lock.Close()
	// Bound before the panel can power on: a busy or forbidden port ends
	// pdp11d here, with the machine untouched.
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		log.Error("cannot start", "err", err)
		return 1
	}
	defer ln.Close()

	mgr, err := systems.New(cat, systems.Options{
		DataDir:        cfg.Data,
		CacheDirs:      cfg.cacheDirs(),
		Selected:       cfg.System,
		AcceptLicenses: cfg.AcceptLicenses,
		Logger:         log,
	})
	if err != nil {
		log.Error("cannot start", "err", err)
		return 1
	}
	defer mgr.Close()
	machine, err := simh.New(simh.Options{
		Binary:         cfg.SIMH,
		WorkDir:        filepath.Join(cfg.Data, "run"),
		SHMPath:        cfg.SHM,
		ConsolePort:    cfg.PortBase,
		TTYTelnet:      cfg.TTYTelnet,
		AcceptLicenses: cfg.AcceptLicenses,
		Logger:         log,
	}, mgr, mgr)
	if err != nil {
		log.Error("cannot start", "err", err)
		return 1
	}
	if _, err := os.Stat(cfg.SIMH); err != nil {
		log.Warn("simulator binary not found; powering on will fail", "simulator", cfg.SIMH, "err", err)
	}
	pnl, err := panel.New(machine, machine.PanelSource(), mgr, panel.Options{
		StatePath: filepath.Join(cfg.Data, "panel.json"),
		FrameHz:   cfg.FrameHz,
		Autostart: cfg.Autostart,
		Logger:    log,
	})
	if err != nil {
		log.Error("cannot start", "err", err)
		machine.Close()
		return 1
	}
	static, err := staticFS(cfg.Web)
	if err != nil {
		log.Error("cannot start", "err", err)
		machine.Close()
		return 1
	}
	srv, err := web.New(web.Options{
		Listen:         cfg.Listen,
		Password:       cfg.Password,
		AllowOrigins:   cfg.AllowOrigins,
		Hosts:          cfg.Hosts,
		TrustedProxies: cfg.TrustedProxies,
		FrameAncestors: cfg.FrameAncestors,
		HealthPublic:   cfg.HealthPublic,
		Version:        version,
		FrameHz:        cfg.FrameHz,
		Static:         static,
		Logger:         log,
	}, web.Deps{Machine: machine, Panel: pnl, Systems: mgr, Log: machine.Log()})
	if err != nil {
		log.Error("cannot start", "err", err)
		machine.Close()
		return 1
	}

	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigs)

	webCtx, stopWeb := context.WithCancel(context.Background())
	panelCtx, stopPanel := context.WithCancel(context.Background())
	webDone := make(chan error, 1)
	panelDone := make(chan error, 1)
	go func() { webDone <- srv.Serve(webCtx, ln) }()
	go func() { panelDone <- pnl.Run(panelCtx) }()

	code := 0
	select {
	case sig := <-sigs:
		log.Info("shutting down", "signal", sig.String())
	case err := <-webDone:
		log.Error("web server failed", "err", err)
		webDone <- nil
		code = 1
	case err := <-panelDone:
		log.Error("panel failed", "err", err)
		panelDone <- nil
		code = 1
	}
	// A second signal ends pdp11d at once; Pdeathsig then stops the
	// simulator (it flushes its disks at the stop).
	go func() {
		sig := <-sigs
		log.Warn("second signal: exiting immediately", "signal", sig.String())
		os.Exit(130)
	}()
	shutdown(cfg, log, machine, pnl, stopWeb, webDone, stopPanel, panelDone)
	log.Info("pdp11d stopped")
	return code
}

// shutdown implements DESIGN §6 "SIGTERM/SIGINT to pdp11d": stop accepting
// connections, stop the panel (its controls stay as they are, so the next
// start autostarts again), optionally shut the guest OS down, power off.
func shutdown(cfg *config, log *slog.Logger, machine *simh.Machine, pnl *panel.Panel,
	stopWeb context.CancelFunc, webDone <-chan error, stopPanel context.CancelFunc, panelDone <-chan error) {
	stopWeb()
	select {
	case err := <-webDone:
		if err != nil {
			log.Warn("web server shutdown", "err", err)
		}
	case <-time.After(webStopTimeout):
		log.Warn("web server did not stop in time")
	}
	stopPanel()
	if err := <-panelDone; err != nil && !errors.Is(err, context.Canceled) {
		log.Warn("panel shutdown", "err", err)
	}
	if cfg.ShutdownOnStop && machine.State() == api.RunRunning {
		log.Info("shutting the guest OS down (PDP11_SHUTDOWN_ON_STOP)", "system", machine.SystemID())
		ctx, cancel := context.WithTimeout(context.Background(), shutdownScriptTimeout)
		if err := pnl.ShutdownOS(ctx); err != nil {
			log.Warn("guest OS shutdown incomplete; powering off anyway", "err", err)
		}
		cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), powerOffTimeout)
	defer cancel()
	if err := machine.PowerOff(ctx); err != nil {
		log.Warn("power off", "err", err)
	}
	machine.Close()
}

// ---- fetch / install / check ----------------------------------------------

// printProgress renders install progress as text lines.
func printProgress(w io.Writer) func(api.Progress) {
	last := map[string]string{}
	var lastPct float64
	return func(p api.Progress) {
		key := p.ID + "/" + p.Phase
		switch p.Phase {
		case "download", "extract", "verify":
			if last[p.ID] == key && p.Pct-lastPct < 10 && p.Pct < 100 {
				return
			}
			lastPct = p.Pct
			fmt.Fprintf(w, "%s: %s %.0f%% (%s)\n", p.ID, p.Phase, p.Pct, p.Text)
		default:
			fmt.Fprintf(w, "%s: %s: %s\n", p.ID, p.Phase, p.Text)
		}
		last[p.ID] = key
	}
}

func fetch(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	var accept bool
	cfg, ids, cat, log, err := setup("fetch", args, getenv, stderr, func(fs *flag.FlagSet) {
		fs.BoolVar(&accept, "accept-license", false, "also fetch systems whose licence is not redistributable")
	})
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(stderr, "pdp11d fetch:", err)
		return 2
	}
	dirs := cfg.cacheDirs()
	if len(dirs) == 0 {
		fmt.Fprintln(stderr, "pdp11d fetch: no cache directory (--cache)")
		return 2
	}
	if len(ids) == 0 {
		for _, s := range cat.Systems {
			if s.License.Class == "redistributable" {
				ids = append(ids, s.ID)
			}
		}
	}
	for _, id := range ids {
		s, ok := cat.Get(id)
		if !ok {
			fmt.Fprintf(stderr, "pdp11d fetch: unknown system %q\n", id)
			return 2
		}
		if s.License.Class != "redistributable" && !accept && !slices.Contains(cfg.AcceptLicenses, id) {
			fmt.Fprintf(stderr, "pdp11d fetch: %s is under the %s (%s); accept it with --accept-license\n", id, s.License.Name, s.License.Class)
			return 2
		}
	}
	// Every cache is searched before downloading (the first is filled).
	mgr, err := systems.New(cat, systems.Options{DataDir: cfg.Data, CacheDirs: dirs, Logger: log})
	if err != nil {
		fmt.Fprintln(stderr, "pdp11d fetch:", err)
		return 1
	}
	defer mgr.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := mgr.Fetch(ctx, dirs[0], ids, printProgress(stdout)); err != nil {
		fmt.Fprintln(stderr, "pdp11d fetch:", err)
		return 1
	}
	return 0
}

func install(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	var accept bool
	cfg, ids, cat, log, err := setup("install", args, getenv, stderr, func(fs *flag.FlagSet) {
		fs.BoolVar(&accept, "accept-license", false, "accept the licences of the named systems")
	})
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(stderr, "pdp11d install:", err)
		return 2
	}
	if len(ids) == 0 {
		fmt.Fprintln(stderr, "pdp11d install: name the systems to install")
		return 2
	}
	lock, err := lockDataDir(cfg.Data)
	var busy *busyError
	switch {
	case errors.As(err, &busy):
		fmt.Fprintln(stderr, "pdp11d:", err)
		return 2
	case err != nil:
		fmt.Fprintln(stderr, "pdp11d install:", err)
		return 1
	}
	defer lock.Close()
	mgr, err := systems.New(cat, systems.Options{DataDir: cfg.Data, CacheDirs: cfg.cacheDirs(), AcceptLicenses: cfg.AcceptLicenses, Logger: log})
	if err != nil {
		fmt.Fprintln(stderr, "pdp11d install:", err)
		return 1
	}
	defer mgr.Close()
	mgr.SubscribeProgress(printProgress(stdout))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	code := 0
	for _, id := range ids {
		if err := mgr.Install(ctx, id, accept, ""); err != nil {
			fmt.Fprintf(stderr, "pdp11d install %s: %v\n", id, err)
			if errors.Is(err, systems.ErrLicense) {
				if s, ok := cat.Get(id); ok && s.LicenseText != "" {
					fmt.Fprintf(stderr, "\n%s\n\nRe-run with --accept-license to accept it.\n", s.LicenseText)
				}
			}
			code = 1
		}
	}
	return code
}

func check(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("pdp11d check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfg, rest, err := loadConfig(fs, args, getenv)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(stderr, "pdp11d check:", err)
		return 2
	}
	dir := cfg.Systems
	if len(rest) > 0 {
		dir = rest[0]
	}
	cat, err := loadCatalog(dir)
	if err != nil {
		fmt.Fprintln(stderr, "pdp11d check:", err)
		return 1
	}
	for _, s := range cat.Systems {
		def := ""
		if s.Default {
			def = " (default)"
		}
		fmt.Fprintf(stdout, "%-14s %-16s %-10s %s%s\n", s.ID, s.License.Class, s.Speed, s.Name, def)
	}
	fmt.Fprintf(stdout, "catalog %s: %d systems, valid\n", catalogName(dir), len(cat.Systems))
	return 0
}
