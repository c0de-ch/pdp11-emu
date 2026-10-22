package paneltest

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"pdp11emu/server/internal/api"
)

// Systems is a fake api.Systems.
type Systems struct {
	mu               sync.Mutex
	installFunc      func(ctx context.Context, id string, accept bool, req string) error
	factoryResetFunc func(ctx context.Context, id string, req string) error
	selectErr        error
	list             []api.SystemInfo
	selected         string
	speeds           map[string]api.Speed
	scripts          map[string][]api.ScriptStep
	calls            []string
	progSubs         map[int]func(api.Progress)
	chgSubs          map[int]func()
	next             int
	cancelled        []string
}

// NewSystems returns a catalog of the given systems; the first one
// flagged Default (or the first one) is selected.
func NewSystems(list ...api.SystemInfo) *Systems {
	s := &Systems{
		list:     slices.Clone(list),
		speeds:   map[string]api.Speed{},
		scripts:  map[string][]api.ScriptStep{},
		progSubs: map[int]func(api.Progress){},
		chgSubs:  map[int]func(){},
	}
	for _, si := range list {
		if si.Default {
			s.selected = si.ID
		}
	}
	if s.selected == "" && len(list) > 0 {
		s.selected = list[0].ID
	}
	return s
}

// DefaultSystems is a small catalog: bsd211 (default, installed,
// redistributable, with a shutdown script, boot timeout 90 s) and rt11
// (hobbyist, not installed, no script, no login, boot timeout 60 s).
func DefaultSystems() *Systems {
	s := NewSystems(
		api.SystemInfo{ID: "bsd211", Name: "2.11BSD", Short: "2.11BSD", Category: "unix", Default: true, Installed: true,
			License: api.LicenseInfo{Name: "BSD", Class: "redistributable"}, Speed: api.SpeedFast, CanShutdown: true,
			Credentials: []api.Credential{{User: "root"}}, BootTimeoutSec: 90,
			Console: api.ConsoleKeys{Backspace: "del", Interrupt: "ctrl-c"}},
		api.SystemInfo{ID: "rt11", Name: "RT-11", Short: "RT-11", Category: "dec", Order: 1,
			License: api.LicenseInfo{Name: "Mentec hobbyist", Class: "hobbyist"}, Speed: api.SpeedAuthentic,
			NoLogin: true, Credentials: []api.Credential{}, BootTimeoutSec: 60,
			Console: api.ConsoleKeys{Backspace: "del", Interrupt: "ctrl-c"}},
	)
	s.scripts["bsd211"] = []api.ScriptStep{
		{Send: "sync\r"},
		{Expect: "# ", Send: "shutdown -h now\r"},
	}
	return s
}

func (s *Systems) List() []api.SystemInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := slices.Clone(s.list)
	for i := range out {
		out[i].Speed = s.speedLocked(out[i].ID)
		out[i].CanShutdown = len(s.scripts[out[i].ID]) > 0
	}
	return out
}

func (s *Systems) Get(id string) (api.SystemInfo, bool) {
	for _, si := range s.List() {
		if si.ID == id {
			return si, true
		}
	}
	return api.SystemInfo{}, false
}

func (s *Systems) Selected() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selected
}

func (s *Systems) Select(id string) error {
	s.mu.Lock()
	s.calls = append(s.calls, "Select "+id)
	if s.indexLocked(id) < 0 {
		s.mu.Unlock()
		return fmt.Errorf("unknown system %q", id)
	}
	if err := s.selectErr; err != nil {
		s.mu.Unlock()
		return err
	}
	s.selected = id
	s.mu.Unlock()
	s.changed()
	return nil
}

func (s *Systems) indexLocked(id string) int {
	for i, si := range s.list {
		if si.ID == id {
			return i
		}
	}
	return -1
}

// Install reports progress and marks the system installed.
func (s *Systems) Install(ctx context.Context, id string, accept bool, req string) error {
	s.mu.Lock()
	s.calls = append(s.calls, fmt.Sprintf("Install %s %v", id, accept))
	fn := s.installFunc
	i := s.indexLocked(id)
	s.mu.Unlock()
	if i < 0 {
		return fmt.Errorf("unknown system %q", id)
	}
	if fn != nil {
		return fn(ctx, id, accept, req)
	}
	si, _ := s.Get(id)
	if si.License.Class != "redistributable" && !accept {
		return fmt.Errorf("%s: the licence must be accepted", id)
	}
	s.Progress(api.Progress{Task: "install", ID: id, Phase: "download", Pct: 50, Req: req})
	s.SetInstalled(id, true)
	s.Progress(api.Progress{Task: "install", ID: id, Phase: "done", Pct: 100, Req: req})
	return nil
}

func (s *Systems) FactoryReset(ctx context.Context, id string, req string) error {
	s.mu.Lock()
	s.calls = append(s.calls, "FactoryReset "+id)
	fn := s.factoryResetFunc
	s.mu.Unlock()
	if fn != nil {
		return fn(ctx, id, req)
	}
	s.Progress(api.Progress{Task: "factoryReset", ID: id, Phase: "done", Pct: 100, Req: req})
	return nil
}

func (s *Systems) Cancel(id string) {
	s.mu.Lock()
	s.calls = append(s.calls, "Cancel "+id)
	s.cancelled = append(s.cancelled, id)
	s.mu.Unlock()
}

func (s *Systems) SubscribeProgress(fn func(api.Progress)) (cancel func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	id := s.next
	s.progSubs[id] = fn
	return func() { s.mu.Lock(); delete(s.progSubs, id); s.mu.Unlock() }
}

func (s *Systems) SubscribeChanges(fn func()) (cancel func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	id := s.next
	s.chgSubs[id] = fn
	return func() { s.mu.Lock(); delete(s.chgSubs, id); s.mu.Unlock() }
}

func (s *Systems) speedLocked(id string) api.Speed {
	if sp, ok := s.speeds[id]; ok {
		return sp
	}
	if i := s.indexLocked(id); i >= 0 && s.list[i].Speed != "" {
		return s.list[i].Speed
	}
	return api.SpeedFast
}

func (s *Systems) SpeedFor(id string) api.Speed {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.speedLocked(id)
}

func (s *Systems) SetSpeedFor(id string, sp api.Speed) error {
	s.mu.Lock()
	s.calls = append(s.calls, fmt.Sprintf("SetSpeedFor %s %s", id, sp))
	if s.indexLocked(id) < 0 {
		s.mu.Unlock()
		return fmt.Errorf("unknown system %q", id)
	}
	s.speeds[id] = sp
	s.mu.Unlock()
	s.changed()
	return nil
}

func (s *Systems) ShutdownScript(id string) []api.ScriptStep {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.scripts[id])
}

// --- test helpers ---

// SetInstallFunc replaces the default install (which reports
// download/done progress and marks the system installed).
func (s *Systems) SetInstallFunc(fn func(ctx context.Context, id string, accept bool, req string) error) {
	s.mu.Lock()
	s.installFunc = fn
	s.mu.Unlock()
}

// SetFactoryResetFunc replaces the default factory reset.
func (s *Systems) SetFactoryResetFunc(fn func(ctx context.Context, id string, req string) error) {
	s.mu.Lock()
	s.factoryResetFunc = fn
	s.mu.Unlock()
}

// SetSelectErr makes Select fail with err (nil: Select works again).
func (s *Systems) SetSelectErr(err error) {
	s.mu.Lock()
	s.selectErr = err
	s.mu.Unlock()
}

// SetScript sets a system's shutdown script.
func (s *Systems) SetScript(id string, steps []api.ScriptStep) {
	s.mu.Lock()
	s.scripts[id] = steps
	s.mu.Unlock()
	s.changed()
}

// SetInstalled changes a system's installed flag.
func (s *Systems) SetInstalled(id string, inst bool) {
	s.mu.Lock()
	if i := s.indexLocked(id); i >= 0 {
		s.list[i].Installed = inst
	}
	s.mu.Unlock()
	s.changed()
}

// SetBootTimeout changes a system's BootTimeoutSec.
func (s *Systems) SetBootTimeout(id string, secs int) {
	s.mu.Lock()
	if i := s.indexLocked(id); i >= 0 {
		s.list[i].BootTimeoutSec = secs
	}
	s.mu.Unlock()
	s.changed()
}

// Progress publishes progress to the subscribers.
func (s *Systems) Progress(p api.Progress) {
	s.mu.Lock()
	subs := make([]func(api.Progress), 0, len(s.progSubs))
	for _, fn := range s.progSubs {
		subs = append(subs, fn)
	}
	s.mu.Unlock()
	for _, fn := range subs {
		fn(p)
	}
}

func (s *Systems) changed() {
	s.mu.Lock()
	subs := make([]func(), 0, len(s.chgSubs))
	for _, fn := range s.chgSubs {
		subs = append(subs, fn)
	}
	s.mu.Unlock()
	for _, fn := range subs {
		fn()
	}
}

// Calls returns the recorded calls ("Select rt11", "Install rt11 true", ...).
func (s *Systems) Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

var _ api.Systems = (*Systems)(nil)
