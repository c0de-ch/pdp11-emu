// Package catalog loads and validates the operating-system catalog
// (systems/<id>/system.json, boot.ini, dialog.ini). The structs here are
// the authoritative schema (docs/DESIGN.md §7).
package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
)

// System is one bootable operating system.
type System struct {
	ID             string       `json:"id"`
	Name           string       `json:"name"`
	Short          string       `json:"short"`
	Category       string       `json:"category"` // unix | dec
	Default        bool         `json:"default,omitempty"`
	Order          int          `json:"order"`
	Description    string       `json:"description"`
	License        License      `json:"license"`
	Credentials    []Credential `json:"credentials,omitempty"`
	NoLogin        bool         `json:"noLogin,omitempty"` // the console needs no login (RT-11, RSX V3.0)
	Shutdown       string       `json:"shutdown"`          // human-readable clean shutdown instructions
	ShutdownScript []Step       `json:"shutdownScript,omitempty"`
	Notes          string       `json:"notes"` // markdown with "## Log in", "## Things to try", "## Shut down"
	CPU            CPU          `json:"cpu"`
	Speed          string       `json:"speed"`        // fast | authentic
	Boot           string       `json:"boot"`         // SCP command, e.g. "boot rq0"
	ReadyPattern   string       `json:"readyPattern"` // console text that means "booted"
	BootTimeoutSec int          `json:"bootTimeoutSec,omitempty"`
	Console        ConsoleKeys  `json:"console"`
	Terminals      *Terminals   `json:"terminals,omitempty"`
	Artifacts      []Artifact   `json:"artifacts"`
	Tests          *Tests       `json:"tests,omitempty"`

	// Filled by Load.
	Dir         string `json:"-"` // directory in the catalog FS
	BootINI     string `json:"-"` // boot.ini: device setup, executed at simulator start
	DialogINI   string `json:"-"` // dialog.ini: expect/send boot dialogue, run before every boot (may be empty)
	LicenseText string `json:"-"` // contents of License.File, if any
}

// License describes the terms of a system's software.
type License struct {
	Name    string `json:"name"`
	Class   string `json:"class"`          // redistributable | hobbyist
	File    string `json:"file,omitempty"` // licence text file next to system.json
	Summary string `json:"summary"`
}

// Credential is a documented login.
type Credential struct {
	User     string `json:"user"`
	Password string `json:"password"`
	Note     string `json:"note,omitempty"`
}

// CPU options. The model is always 11/70.
type CPU struct {
	Memory string `json:"memory"` // e.g. "4M", "2M", "1M", "256K"
}

// ConsoleKeys tell the browser terminal how to map keys for this OS.
type ConsoleKeys struct {
	Backspace string `json:"backspace"` // del | bs | "#"
	Interrupt string `json:"interrupt"` // ctrl-c | del
	KeyHelp   string `json:"keyHelp,omitempty"`
}

// Terminals are extra serial lines, reachable only through the optional
// PDP11_TTY_TELNET listener (pdp11d generates the attach command).
type Terminals struct {
	Device     string `json:"device"` // dz | dci | dli
	Lines      int    `json:"lines"`
	Modem      bool   `json:"modem,omitempty"` // attach with -m (dz only)
	EnableHint string `json:"enableHint,omitempty"`
}

// Artifact is one downloadable archive.
type Artifact struct {
	URL     string         `json:"url"`
	Mirrors []string       `json:"mirrors,omitempty"`
	SHA256  string         `json:"sha256"`
	Size    int64          `json:"size"`
	Format  string         `json:"format"` // raw | gz | xz | zip | tgz | tar | tarZ
	Files   []ArtifactFile `json:"files"`
}

// ArtifactFile is a file extracted from an artifact into the working directory.
type ArtifactFile struct {
	Path   string `json:"path"` // path inside the archive (gz/xz/raw: output name)
	Role   string `json:"role"` // DISK0..DISK9 (becomes ${DISKn} in boot.ini)
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Sparse bool   `json:"sparse,omitempty"`
}

// Tests drive the automated boot matrix through the console.
type Tests struct {
	Session []Step `json:"session"` // after ReadyPattern: log in, run a command, check output
}

// Step waits for Expect (if set) on the console, then types Send (if set).
type Step struct {
	Expect     string `json:"expect,omitempty"`
	Send       string `json:"send,omitempty"`
	TimeoutSec int    `json:"timeoutSec,omitempty"` // default 30
}

// Catalog is the set of systems, sorted by Order then ID.
type Catalog struct {
	Systems []*System
	byID    map[string]*System
}

// Get returns the system with the given id.
func (c *Catalog) Get(id string) (*System, bool) {
	s, ok := c.byID[id]
	return s, ok
}

// Default returns the system flagged default (Validate guarantees one).
func (c *Catalog) Default() *System {
	for _, s := range c.Systems {
		if s.Default {
			return s
		}
	}
	if len(c.Systems) > 0 {
		return c.Systems[0]
	}
	return nil
}

// Load reads every <dir>/system.json (plus boot.ini, optional dialog.ini
// and the licence file) from fsys. Directories without system.json are
// ignored. Load does not validate; call Validate.
func Load(fsys fs.FS) (*Catalog, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	c := &Catalog{byID: map[string]*System{}}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		raw, err := fs.ReadFile(fsys, path.Join(e.Name(), "system.json"))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		s := &System{}
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(s); err != nil {
			return nil, fmt.Errorf("%s/system.json: %w", e.Name(), err)
		}
		s.Dir = e.Name()
		ini, err := fs.ReadFile(fsys, path.Join(e.Name(), "boot.ini"))
		if err != nil {
			return nil, fmt.Errorf("%s/boot.ini: %w", e.Name(), err)
		}
		s.BootINI = string(ini)
		if dlg, err := fs.ReadFile(fsys, path.Join(e.Name(), "dialog.ini")); err == nil {
			s.DialogINI = string(dlg)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%s/dialog.ini: %w", e.Name(), err)
		}
		if s.License.File != "" {
			txt, err := fs.ReadFile(fsys, path.Join(e.Name(), s.License.File))
			if err != nil {
				return nil, fmt.Errorf("%s/%s: %w", e.Name(), s.License.File, err)
			}
			s.LicenseText = string(txt)
		}
		if _, dup := c.byID[s.ID]; dup {
			return nil, fmt.Errorf("duplicate system id %q", s.ID)
		}
		c.byID[s.ID] = s
		c.Systems = append(c.Systems, s)
	}
	sort.SliceStable(c.Systems, func(i, j int) bool {
		if c.Systems[i].Order != c.Systems[j].Order {
			return c.Systems[i].Order < c.Systems[j].Order
		}
		return c.Systems[i].ID < c.Systems[j].ID
	})
	return c, nil
}

var (
	reID     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,30}$`)
	reSHA    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	reMemory = regexp.MustCompile(`^[0-9]+[KM]$`)
	reBoot   = regexp.MustCompile(`^boot [a-z]{2,4}[0-9]?$`)
	reRole   = regexp.MustCompile(`^DISK[0-9]$`)
	reVar    = regexp.MustCompile(`\$\{([A-Z0-9_]+)\}`)
	// SimH devices/units boot.ini may SET or ATTACH (disks, tapes, serial
	// lines, clock, printers, readers). Not: cpu, console, remote, panel,
	// networking (xu, xq).
	reDevice  = regexp.MustCompile(`(?i)^(rq|rp|rl|rk|hk|rx|ry|tm|tq|ts|tu|dz|dci|dco|dli|dlo|vh|lpt|ptr|ptp|clk|cr|kg|rha|rhb|rhc)[0-9]{0,2}$`)
	reNetDev  = regexp.MustCompile(`(?i)^(xu|xq|xqb)$`)
	reExpect  = regexp.MustCompile(`(?i)^\s*expect\b`)
	reSend    = regexp.MustCompile(`(?i)^\s*send\b`)
	reCont    = regexp.MustCompile(`(?i);\s*continue\s*$`)
	formats   = map[string]bool{"raw": true, "gz": true, "xz": true, "zip": true, "tgz": true, "tar": true, "tarZ": true}
	classes   = map[string]bool{"redistributable": true, "hobbyist": true}
	tdevices  = map[string]bool{"dz": true, "dci": true, "dli": true}
	backspace = map[string]bool{"del": true, "bs": true, "#": true}
	interrupt = map[string]bool{"ctrl-c": true, "del": true}
)

// fields splits a SimH command line into words (quotes are not special
// for the checks done here).
func fields(line string) []string { return strings.Fields(line) }

// checkBootLine validates one boot.ini line; it returns "" if allowed.
func checkBootLine(line string, s *System) string {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, ";") || strings.HasPrefix(t, "#") {
		return ""
	}
	w := fields(t)
	cmd := strings.ToLower(w[0])
	switch cmd {
	case "set":
		if len(w) < 3 {
			return "set needs a device and an option"
		}
		if reNetDev.MatchString(w[1]) {
			if len(w) == 3 && strings.EqualFold(w[2], "disabled") {
				return ""
			}
			return "networking devices may only be disabled (networking is not part of v1)"
		}
		if !reDevice.MatchString(w[1]) {
			return fmt.Sprintf("set %s: only disk/tape/serial/clock/printer devices may be set here (the header owns cpu, console, throttle, remote, panel, prompt)", w[1])
		}
		return ""
	case "attach":
		// attach [-switches...] <unit> <file>
		var sw []string
		i := 1
		for ; i < len(w) && strings.HasPrefix(w[i], "-"); i++ {
			sw = append(sw, strings.ToLower(w[i]))
		}
		if i+1 >= len(w) {
			return "attach needs a unit and a file"
		}
		unit, file := w[i], strings.Join(w[i+1:], " ")
		if s.Terminals != nil && strings.HasPrefix(strings.ToLower(unit), s.Terminals.Device) {
			return "the terminals device is attached by pdp11d (PDP11_TTY_TELNET); do not attach it in boot.ini"
		}
		if !reDevice.MatchString(unit) || strings.HasPrefix(strings.ToLower(unit), "dz") ||
			strings.HasPrefix(strings.ToLower(unit), "dc") || strings.HasPrefix(strings.ToLower(unit), "dl") ||
			strings.HasPrefix(strings.ToLower(unit), "vh") {
			return fmt.Sprintf("attach %s: only disk and tape units may be attached here", unit)
		}
		if !regexp.MustCompile(`^\$\{DISK[0-9]\}$`).MatchString(file) {
			return "attach file must be exactly ${DISKn}"
		}
		hasE := false
		for _, x := range sw {
			if strings.Contains(x, "e") {
				hasE = true
			}
		}
		if !hasE {
			return "attach of a disk image must use -e (fail instead of creating an empty disk)"
		}
		return ""
	case "deposit":
		if len(w) != 3 {
			return "deposit needs a location and a value"
		}
		return ""
	case "echo":
		return ""
	}
	return fmt.Sprintf("command %q is not allowed in boot.ini (allowed: set, attach, deposit, echo, comments; expect/send belong in dialog.ini)", w[0])
}

// checkDialogLine validates one dialog.ini line; it returns "" if allowed.
func checkDialogLine(line string) string {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, ";") || strings.HasPrefix(t, "#") {
		return ""
	}
	switch {
	case reExpect.MatchString(t):
		if !reCont.MatchString(t) {
			return "every expect rule must have an action ending in '; continue' (otherwise the CPU stops silently)"
		}
		return ""
	case reSend.MatchString(t):
		return ""
	}
	return "dialog.ini may only contain expect and send commands"
}

// Validate checks the catalog; it returns every problem found.
func (c *Catalog) Validate() []error {
	var errs []error
	add := func(s *System, f string, a ...any) {
		errs = append(errs, fmt.Errorf("%s: %s", s.Dir, fmt.Sprintf(f, a...)))
	}
	defaults := 0
	for _, s := range c.Systems {
		if !reID.MatchString(s.ID) {
			add(s, "bad id %q", s.ID)
		}
		if s.ID != s.Dir {
			add(s, "id %q must equal directory name", s.ID)
		}
		if s.Default {
			defaults++
			if s.License.Class != "redistributable" {
				add(s, "the default system must be redistributable (it is installed automatically)")
			}
			https := len(s.Artifacts) > 0
			for _, a := range s.Artifacts {
				ok := strings.HasPrefix(a.URL, "https://")
				for _, m := range a.Mirrors {
					ok = ok || strings.HasPrefix(m, "https://")
				}
				https = https && ok
			}
			if !https {
				add(s, "every artifact of the default system needs an https source")
			}
		}
		if s.Name == "" || s.Short == "" || s.Description == "" {
			add(s, "name, short and description are required")
		}
		if s.Category != "unix" && s.Category != "dec" {
			add(s, "category %q invalid", s.Category)
		}
		if !classes[s.License.Class] {
			add(s, "license.class %q invalid", s.License.Class)
		}
		if s.License.Name == "" || s.License.Summary == "" {
			add(s, "license.name and license.summary are required")
		}
		if s.License.Class != "redistributable" && s.LicenseText == "" {
			add(s, "license.file is required for class %q", s.License.Class)
		}
		if len(s.Credentials) == 0 && !s.NoLogin {
			add(s, "credentials are required (or noLogin: true)")
		}
		if s.Shutdown == "" {
			add(s, "shutdown instructions are required")
		}
		for _, h := range []string{"## Log in", "## Things to try", "## Shut down"} {
			if !strings.Contains(s.Notes, h) {
				add(s, "notes must contain a %q section", h)
			}
		}
		if !reMemory.MatchString(s.CPU.Memory) {
			add(s, "cpu.memory %q invalid", s.CPU.Memory)
		}
		if s.Speed != "fast" && s.Speed != "authentic" {
			add(s, "speed %q invalid", s.Speed)
		}
		if !reBoot.MatchString(s.Boot) {
			add(s, "boot %q invalid", s.Boot)
		}
		if s.ReadyPattern == "" {
			add(s, "readyPattern is required")
		}
		if !backspace[s.Console.Backspace] || !interrupt[s.Console.Interrupt] {
			add(s, "console.backspace (del|bs|#) and console.interrupt (ctrl-c|del) are required")
		}
		if t := s.Terminals; t != nil {
			if !tdevices[t.Device] || t.Lines < 1 || t.Lines > 16 || (t.Modem && t.Device != "dz") {
				add(s, "terminals invalid: %+v", *t)
			}
		}
		if len(s.Artifacts) == 0 {
			add(s, "at least one artifact is required")
		}
		roles := map[string]bool{}
		for i, a := range s.Artifacts {
			for _, u := range append([]string{a.URL}, a.Mirrors...) {
				if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "ftp://") {
					add(s, "artifact %d: url scheme of %q", i, u)
				}
			}
			if !reSHA.MatchString(a.SHA256) || a.Size <= 0 || !formats[a.Format] {
				add(s, "artifact %d: sha256/size/format invalid", i)
			}
			if len(a.Files) == 0 {
				add(s, "artifact %d: no files", i)
			}
			for _, f := range a.Files {
				if !reRole.MatchString(f.Role) || roles[f.Role] {
					add(s, "artifact %d: role %q invalid or duplicate", i, f.Role)
				}
				roles[f.Role] = true
				if !reSHA.MatchString(f.SHA256) || f.Size <= 0 || f.Path == "" || strings.Contains(f.Path, "..") || strings.HasPrefix(f.Path, "/") {
					add(s, "artifact %d: file %q sha256/size/path invalid", i, f.Path)
				}
			}
		}
		if strings.Count(s.BootINI, "${") != len(reVar.FindAllString(s.BootINI, -1)) {
			add(s, "boot.ini: malformed ${...} (variables are upper case: ${DISK0})")
		}
		used := map[string]bool{}
		for _, m := range reVar.FindAllStringSubmatch(s.BootINI, -1) {
			v := m[1]
			if !reRole.MatchString(v) || !roles[v] {
				add(s, "boot.ini uses ${%s} but no artifact file has that role", v)
			}
			used[v] = true
		}
		for r := range roles {
			if !used[r] {
				add(s, "artifact role %s is never attached in boot.ini", r)
			}
		}
		for n, line := range strings.Split(s.BootINI, "\n") {
			if msg := checkBootLine(line, s); msg != "" {
				add(s, "boot.ini line %d %q: %s", n+1, strings.TrimSpace(line), msg)
			}
		}
		if strings.Contains(s.DialogINI, "${") {
			add(s, "%s", "dialog.ini must not use ${...} variables (SimH %VAR% substitutions are fine)")
		}
		for n, line := range strings.Split(s.DialogINI, "\n") {
			if msg := checkDialogLine(line); msg != "" {
				add(s, "dialog.ini line %d %q: %s", n+1, strings.TrimSpace(line), msg)
			}
		}
		for i, st := range s.ShutdownScript {
			if st.Expect == "" && st.Send == "" {
				add(s, "shutdownScript step %d is empty", i)
			}
		}
		if s.Tests != nil {
			for i, st := range s.Tests.Session {
				if st.Expect == "" && st.Send == "" {
					add(s, "tests.session step %d is empty", i)
				}
			}
		}
	}
	if defaults != 1 {
		errs = append(errs, fmt.Errorf("exactly one system must be default, found %d", defaults))
	}
	return errs
}

// DialogLines returns the non-empty, non-comment lines of dialog.ini.
func (s *System) DialogLines() []string {
	var out []string
	for _, l := range strings.Split(s.DialogINI, "\n") {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, ";") || strings.HasPrefix(t, "#") {
			continue
		}
		out = append(out, t)
	}
	return out
}

// Expand substitutes ${VAR} in boot.ini text; unknown variables are an error.
func Expand(text string, vars map[string]string) (string, error) {
	var missing []string
	out := reVar.ReplaceAllStringFunc(text, func(m string) string {
		name := m[2 : len(m)-1]
		v, ok := vars[name]
		if !ok {
			missing = append(missing, name)
			return m
		}
		return v
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("undefined variables: %s", strings.Join(missing, ", "))
	}
	return out, nil
}
