// Package systems installs operating-system images and prepares
// simulator configurations (docs/DESIGN.md §7). Implements api.Systems
// and simh.RunConfigProvider.
//
// Data directory layout (DESIGN §5):
//
//	$DATA/systems.json      selected system + per-system speed overrides
//	$DATA/cache/<sha256>    verified archives (*.part while downloading)
//	$DATA/import/           archives dropped here by hand
//	$DATA/systems/<id>/     working disk images + .installed
package systems

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"pdp11emu/server/internal/api"
	"pdp11emu/server/internal/atomicfile"
	"pdp11emu/server/internal/catalog"
	"pdp11emu/server/internal/simh"
)

// Options configure the manager.
type Options struct {
	DataDir        string   // $DATA
	CacheDirs      []string // read-only caches searched before downloading (e.g. /opt/pdp11/cache)
	Selected       string   // initial selection if none persisted (empty: catalog default)
	AcceptLicenses []string // ids whose licences are accepted without asking (PDP11_ACCEPT_LICENSES)
	Logger         *slog.Logger
}

// ErrLicense is returned by Install when a licence must be accepted first.
var ErrLicense = errors.New("the licence must be accepted before installing")

// Manager implements api.Systems and simh.RunConfigProvider.
type Manager struct {
	cat  *catalog.Catalog
	opts Options
	log  *slog.Logger
	dl   downloader

	mu       sync.Mutex
	state    persisted
	tasks    map[string]*task // running install or factory reset, by system id
	leases   map[string]int   // simulators using a system's images (PrepareRun until they exited)
	progSubs []progressSub
	chgSubs  []changeSub
	nextID   uint64

	installing chan struct{} // capacity 1: installs are serialized
	dirty      chan struct{} // coalesced change notifications
	closed     chan struct{}
	wg         sync.WaitGroup
}

// persisted is the content of $DATA/systems.json.
type persisted struct {
	Selected string               `json:"selected"`
	Speeds   map[string]api.Speed `json:"speeds,omitempty"`
}

type task struct {
	kind   string // install | factoryReset
	cancel context.CancelFunc
}

type progressSub struct {
	id uint64
	fn func(api.Progress)
}

type changeSub struct {
	id uint64
	fn func()
}

// changeDelay coalesces bursts of changes into one notification.
const changeDelay = 50 * time.Millisecond

// New creates a manager over a validated catalog. It creates the data
// directories and loads the persisted selection.
func New(cat *catalog.Catalog, opts Options) (*Manager, error) {
	if cat == nil || len(cat.Systems) == 0 {
		return nil, errors.New("systems: empty catalog")
	}
	if opts.DataDir == "" {
		return nil, errors.New("systems: DataDir is required")
	}
	dataDir, err := filepath.Abs(opts.DataDir)
	if err != nil {
		return nil, err
	}
	opts.DataDir = dataDir
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	m := &Manager{
		cat:        cat,
		opts:       opts,
		log:        opts.Logger.With("component", "systems"),
		tasks:      map[string]*task{},
		leases:     map[string]int{},
		installing: make(chan struct{}, 1),
		dirty:      make(chan struct{}, 1),
		closed:     make(chan struct{}),
	}
	m.dl = newDownloader(m.log)
	for _, d := range []string{m.cacheDir(), m.importDir(), filepath.Join(dataDir, "systems")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return nil, fmt.Errorf("systems: %w", err)
		}
	}
	if err := m.load(); err != nil {
		return nil, err
	}
	m.wg.Add(1)
	go m.notifyLoop()
	return m, nil
}

// Close stops change notifications and cancels running installs.
func (m *Manager) Close() error {
	m.mu.Lock()
	for _, t := range m.tasks {
		t.cancel()
	}
	m.mu.Unlock()
	select {
	case <-m.closed:
	default:
		close(m.closed)
	}
	m.wg.Wait()
	return nil
}

func (m *Manager) cacheDir() string  { return filepath.Join(m.opts.DataDir, "cache") }
func (m *Manager) importDir() string { return filepath.Join(m.opts.DataDir, "import") }
func (m *Manager) stateFile() string { return filepath.Join(m.opts.DataDir, "systems.json") }

// SystemDir is the directory holding a system's working disk images.
func (m *Manager) SystemDir(id string) string {
	return filepath.Join(m.opts.DataDir, "systems", id)
}

// load reads systems.json; a missing or unreadable file means defaults.
func (m *Manager) load() error {
	raw, err := os.ReadFile(m.stateFile())
	first := errors.Is(err, os.ErrNotExist)
	switch {
	case first:
	case err != nil:
		return fmt.Errorf("systems: %w", err)
	default:
		if err := json.Unmarshal(raw, &m.state); err != nil {
			m.log.Warn("ignoring unreadable systems.json", "err", err)
			m.state = persisted{}
		}
	}
	if m.state.Speeds == nil {
		m.state.Speeds = map[string]api.Speed{}
	}
	for id, s := range m.state.Speeds {
		if _, ok := m.cat.Get(id); !ok || (s != api.SpeedFast && s != api.SpeedAuthentic) {
			delete(m.state.Speeds, id)
		}
	}
	if _, ok := m.cat.Get(m.state.Selected); !ok {
		if m.state.Selected != "" {
			m.log.Warn("persisted selection is not in the catalog", "system", m.state.Selected)
		}
		m.state.Selected = m.cat.Default().ID
		if _, ok := m.cat.Get(m.opts.Selected); ok {
			m.state.Selected = m.opts.Selected
		} else if m.opts.Selected != "" {
			m.log.Warn("PDP11_SYSTEM is not in the catalog; using the default", "system", m.opts.Selected)
		}
	}
	if first {
		// PDP11_SYSTEM applies to the first start only: record the choice.
		if err := m.save(); err != nil {
			return fmt.Errorf("systems: %w", err)
		}
	}
	return nil
}

// save writes systems.json atomically. Caller holds m.mu.
func (m *Manager) save() error {
	raw, err := json.MarshalIndent(m.state, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(m.stateFile(), append(raw, '\n'), 0o640)
}

// ---- api.Systems: catalog and selection ----------------------------------

func (m *Manager) List() []api.SystemInfo {
	out := make([]api.SystemInfo, 0, len(m.cat.Systems))
	for _, s := range m.cat.Systems {
		out = append(out, m.info(s))
	}
	return out
}

func (m *Manager) Get(id string) (api.SystemInfo, bool) {
	s, ok := m.cat.Get(id)
	if !ok {
		return api.SystemInfo{}, false
	}
	return m.info(s), true
}

func (m *Manager) info(s *catalog.System) api.SystemInfo {
	var dl, disk int64
	for _, a := range s.Artifacts {
		dl += a.Size
		for _, f := range a.Files {
			disk += f.Size
		}
	}
	creds := make([]api.Credential, 0, len(s.Credentials))
	for _, c := range s.Credentials {
		creds = append(creds, api.Credential{User: c.User, Password: c.Password, Note: c.Note})
	}
	m.mu.Lock()
	_, installing := m.tasks[s.ID]
	m.mu.Unlock()
	return api.SystemInfo{
		ID:            s.ID,
		Name:          s.Name,
		Short:         s.Short,
		Category:      s.Category,
		Description:   s.Description,
		Order:         s.Order,
		Default:       s.Default,
		Installed:     !installing && m.Installed(s.ID),
		Installing:    installing,
		DownloadBytes: dl,
		DiskBytes:     disk,
		License: api.LicenseInfo{
			Name:    s.License.Name,
			Class:   s.License.Class,
			Summary: s.License.Summary,
			Text:    s.LicenseText,
		},
		Credentials:    creds,
		NoLogin:        s.NoLogin,
		BootTimeoutSec: bootTimeoutSec(s),
		Shutdown:       s.Shutdown,
		CanShutdown:    len(s.ShutdownScript) > 0,
		Notes:          s.Notes,
		Speed:          m.SpeedFor(s.ID),
		Console: api.ConsoleKeys{
			Backspace: s.Console.Backspace,
			Interrupt: s.Console.Interrupt,
			KeyHelp:   s.Console.KeyHelp,
		},
	}
}

// defaultBootTimeoutSec applies to catalog entries without bootTimeoutSec.
const defaultBootTimeoutSec = 120

// bootTimeoutSec is the time a boot of s may take before the panel warns.
func bootTimeoutSec(s *catalog.System) int {
	if s.BootTimeoutSec > 0 {
		return s.BootTimeoutSec
	}
	return defaultBootTimeoutSec
}

func (m *Manager) Selected() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state.Selected
}

// Select changes the selected system. It fails with ErrBusy while a
// simulator runs (callers ensure the machine is off; this closes the race
// with a power-on that starts meanwhile).
func (m *Manager) Select(id string) error {
	if _, ok := m.cat.Get(id); !ok {
		return fmt.Errorf("unknown system %q", id)
	}
	m.mu.Lock()
	if m.state.Selected == id {
		m.mu.Unlock()
		return nil
	}
	if len(m.leases) > 0 {
		m.mu.Unlock()
		return fmt.Errorf("the simulator is running: %w", api.ErrBusy)
	}
	prev := m.state.Selected
	m.state.Selected = id
	err := m.save()
	if err != nil {
		m.state.Selected = prev
	}
	m.mu.Unlock()
	if err != nil {
		return fmt.Errorf("saving the selection: %w", err)
	}
	m.log.Info("system selected", "system", id)
	m.changed()
	return nil
}

func (m *Manager) SpeedFor(id string) api.Speed {
	m.mu.Lock()
	s, ok := m.state.Speeds[id]
	m.mu.Unlock()
	if ok {
		return s
	}
	if sys, ok := m.cat.Get(id); ok && sys.Speed == string(api.SpeedAuthentic) {
		return api.SpeedAuthentic
	}
	return api.SpeedFast
}

func (m *Manager) SetSpeedFor(id string, s api.Speed) error {
	if _, ok := m.cat.Get(id); !ok {
		return fmt.Errorf("unknown system %q", id)
	}
	if s != api.SpeedFast && s != api.SpeedAuthentic {
		return fmt.Errorf("invalid speed %q", s)
	}
	m.mu.Lock()
	prev, had := m.state.Speeds[id]
	if had && prev == s {
		m.mu.Unlock()
		return nil
	}
	m.state.Speeds[id] = s
	err := m.save()
	if err != nil {
		if had {
			m.state.Speeds[id] = prev
		} else {
			delete(m.state.Speeds, id)
		}
	}
	m.mu.Unlock()
	if err != nil {
		return fmt.Errorf("saving the speed: %w", err)
	}
	m.changed()
	return nil
}

func (m *Manager) ShutdownScript(id string) []api.ScriptStep {
	s, ok := m.cat.Get(id)
	if !ok || len(s.ShutdownScript) == 0 {
		return nil
	}
	out := make([]api.ScriptStep, len(s.ShutdownScript))
	for i, st := range s.ShutdownScript {
		out[i] = api.ScriptStep{Expect: st.Expect, Send: st.Send, TimeoutSec: st.TimeoutSec}
	}
	return out
}

// accepted reports whether a system may be installed without asking.
func (m *Manager) accepted(s *catalog.System) bool {
	return s.License.Class == "redistributable" || slices.Contains(m.opts.AcceptLicenses, s.ID)
}

// ---- subscriptions --------------------------------------------------------

func (m *Manager) SubscribeProgress(fn func(api.Progress)) func() {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.nextID
	m.nextID++
	m.progSubs = append(m.progSubs, progressSub{id, fn})
	return func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.progSubs = slices.DeleteFunc(slices.Clone(m.progSubs), func(s progressSub) bool { return s.id == id })
	}
}

func (m *Manager) SubscribeChanges(fn func()) func() {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.nextID
	m.nextID++
	m.chgSubs = append(m.chgSubs, changeSub{id, fn})
	return func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.chgSubs = slices.DeleteFunc(slices.Clone(m.chgSubs), func(s changeSub) bool { return s.id == id })
	}
}

// publish delivers progress to every subscriber (on the caller's
// goroutine; subscribers must not block).
func (m *Manager) publish(p api.Progress) {
	m.mu.Lock()
	subs := m.progSubs
	m.mu.Unlock()
	for _, s := range subs {
		s.fn(p)
	}
}

// changed schedules a (coalesced) change notification.
func (m *Manager) changed() {
	select {
	case m.dirty <- struct{}{}:
	default:
	}
}

func (m *Manager) notifyLoop() {
	defer m.wg.Done()
	for {
		select {
		case <-m.closed:
			return
		case <-m.dirty:
		}
		select { // let a burst of changes settle
		case <-m.closed:
			return
		case <-time.After(changeDelay):
		}
		select { // changes during the delay are covered by this notification
		case <-m.dirty:
		default:
		}
		m.mu.Lock()
		subs := m.chgSubs
		m.mu.Unlock()
		for _, s := range subs {
			s.fn()
		}
	}
}

// ---- simh.RunConfigProvider -------------------------------------------------

// PrepareRun implements simh.RunConfigProvider: it writes run.ini = the
// generated header + boot.ini with ${DISKn} expanded to the working copies
// (+ the terminal listener attach when configured). It leases the selected
// system's images until RunConfig.Release: the selection is read and the
// lease taken in one step, so a selection change, an install or a factory
// reset happens either before (PrepareRun then fails with ErrBusy while it
// runs) or after the simulator has exited (they fail with ErrBusy until
// then).
func (m *Manager) PrepareRun(workDir string, ports simh.Ports, shmPath string, speed api.Speed) (rc *simh.RunConfig, err error) {
	m.mu.Lock()
	id := m.state.Selected
	sys, ok := m.cat.Get(id)
	_, busy := m.tasks[id]
	if ok && !busy {
		m.leases[id]++
	}
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("selected system %q is not in the catalog", id)
	}
	if busy {
		return nil, fmt.Errorf("%s is being installed: %w", sys.Name, api.ErrBusy)
	}
	release := sync.OnceFunc(func() {
		m.mu.Lock()
		if m.leases[id]--; m.leases[id] <= 0 {
			delete(m.leases, id)
		}
		m.mu.Unlock()
	})
	defer func() {
		if err != nil {
			release()
		}
	}()
	if !m.Installed(id) {
		return nil, fmt.Errorf("%s: %w", sys.Name, api.ErrNoSystem)
	}
	if err := simh.CheckPath(shmPath); err != nil {
		return nil, err
	}
	vars := map[string]string{}
	var disks []string
	for _, a := range sys.Artifacts {
		for _, f := range a.Files {
			p := m.workingPath(id, f)
			fi, err := os.Stat(p)
			if err != nil || !fi.Mode().IsRegular() {
				return nil, fmt.Errorf("%s: working disk image %s is missing (a factory reset restores it)", sys.Name, p)
			}
			if err := simh.CheckPath(p); err != nil {
				return nil, err
			}
			vars[f.Role] = p
			disks = append(disks, p)
		}
	}
	body, err := catalog.Expand(strings.ReplaceAll(sys.BootINI, "\r\n", "\n"), vars)
	if err != nil {
		return nil, fmt.Errorf("%s/boot.ini: %w", sys.Dir, err)
	}
	header := simh.RunHeader(sys.CPU.Memory, speed, ports.Console, shmPath)
	lines := append(slices.Clone(header), strings.Split(strings.TrimRight(body, "\n"), "\n")...)
	if sys.Terminals != nil && ports.TTYTelnet != "" {
		if err := simh.CheckPath(ports.TTYTelnet); err != nil {
			return nil, fmt.Errorf("PDP11_TTY_TELNET: %w", err)
		}
		lines = append(lines, simh.TerminalsAttach(sys.Terminals.Device, sys.Terminals.Modem, ports.TTYTelnet))
	}
	if err := os.MkdirAll(workDir, 0o750); err != nil {
		return nil, err
	}
	ini, err := filepath.Abs(filepath.Join(workDir, "run.ini"))
	if err != nil {
		return nil, err
	}
	if err := atomicfile.WriteFile(ini, []byte(strings.Join(lines, "\n")+"\n"), 0o640); err != nil {
		return nil, err
	}
	return &simh.RunConfig{
		SystemID:     id,
		INIPath:      ini,
		HeaderLines:  len(header),
		BootCommand:  sys.Boot,
		Dialog:       sys.DialogLines(),
		ReadyPattern: sys.ReadyPattern,
		Disks:        disks,
		Release:      release,
	}, nil
}

// workingPath is where a file of an artifact is installed.
func (m *Manager) workingPath(id string, f catalog.ArtifactFile) string {
	return filepath.Join(m.SystemDir(id), filepath.FromSlash(cleanArchivePath(f.Path)))
}

var (
	_ api.Systems            = (*Manager)(nil)
	_ simh.RunConfigProvider = (*Manager)(nil)
)
