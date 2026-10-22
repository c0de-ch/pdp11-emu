package systems

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"pdp11emu/server/internal/api"
	"pdp11emu/server/internal/simh"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata/")

func TestListAndGet(t *testing.T) {
	f := newFixture(t, defaultSpecs())
	m := f.manager()
	list := m.List()
	if len(list) != 7 || list[0].ID != "bsd" {
		t.Fatalf("list order: %v", list)
	}
	bsd, ok := m.Get("bsd")
	a, _ := f.archive("bsd")
	if !ok || !bsd.Default || bsd.Installed || bsd.Installing || bsd.DownloadBytes != a.Size ||
		bsd.DiskBytes != int64(len(f.files["bsd"][0].data)) || !bsd.CanShutdown ||
		bsd.License.Text != "Licence text of bsd" || bsd.License.Class != "redistributable" ||
		len(bsd.Credentials) != 1 || bsd.Speed != api.SpeedFast || bsd.Console.Backspace != "del" ||
		bsd.NoLogin || bsd.BootTimeoutSec != 90 {
		t.Fatalf("bsd: %+v", bsd)
	}
	rt, _ := m.Get("rt11")
	if rt.Speed != api.SpeedAuthentic || rt.CanShutdown || rt.License.Class != "hobbyist" ||
		!rt.NoLogin || rt.BootTimeoutSec != defaultBootTimeoutSec {
		t.Fatalf("rt11: %+v", rt)
	}
	if _, ok := m.Get("nope"); ok {
		t.Fatal("unknown id found")
	}
	steps := m.ShutdownScript("bsd")
	if len(steps) != 2 || steps[0].Send != "shutdown -h now\r" || steps[1].Expect != "halting" || steps[1].TimeoutSec != 60 {
		t.Fatalf("shutdown script %+v", steps)
	}
	if m.ShutdownScript("v6") != nil {
		t.Fatal("v6 has no shutdown script")
	}
}

func TestSelectionAndSpeedPersist(t *testing.T) {
	f := newFixture(t, defaultSpecs())
	m := f.manager(func(o *Options) { o.Selected = "v6" }) // PDP11_SYSTEM on first start
	if m.Selected() != "v6" {
		t.Fatalf("selected %s", m.Selected())
	}
	if err := m.Select("tar"); err != nil {
		t.Fatal(err)
	}
	if err := m.Select("nope"); err == nil {
		t.Fatal("unknown id selected")
	}
	if err := m.SetSpeedFor("rt11", api.SpeedFast); err != nil {
		t.Fatal(err)
	}
	if err := m.SetSpeedFor("rt11", "warp"); err == nil {
		t.Fatal("bad speed accepted")
	}
	if m.SpeedFor("rt11") != api.SpeedFast || m.SpeedFor("v6") != api.SpeedFast || m.SpeedFor("nope") != api.SpeedFast {
		t.Fatal("speeds")
	}
	m.Close()

	// A new manager (restart) keeps the choice, even with another PDP11_SYSTEM.
	m2 := f.manager(func(o *Options) { o.Selected = "gz" })
	if m2.Selected() != "tar" || m2.SpeedFor("rt11") != api.SpeedFast {
		t.Fatalf("after restart: selected %s, rt11 %s", m2.Selected(), m2.SpeedFor("rt11"))
	}
	if info, _ := m2.Get("rt11"); info.Speed != api.SpeedFast {
		t.Fatal("SystemInfo.Speed ignores the override")
	}
	raw, _ := os.ReadFile(filepath.Join(f.data, "systems.json"))
	if !strings.Contains(string(raw), `"selected": "tar"`) {
		t.Fatalf("systems.json: %s", raw)
	}

	// A selection that left the catalog falls back to the default; garbage
	// is ignored.
	os.WriteFile(filepath.Join(f.data, "systems.json"), []byte(`{"selected":"gone","speeds":{"gone":"fast","v6":"warp"}}`), 0o644)
	if m3 := f.manager(); m3.Selected() != "bsd" || len(m3.state.Speeds) != 0 {
		t.Fatalf("selected %s speeds %v", m3.Selected(), m3.state.Speeds)
	}
	os.WriteFile(filepath.Join(f.data, "systems.json"), []byte(`{not json`), 0o644)
	if m4 := f.manager(func(o *Options) { o.Selected = "nope" }); m4.Selected() != "bsd" {
		t.Fatalf("selected %s", m4.Selected())
	}
}

func TestSubscribeChanges(t *testing.T) {
	f := newFixture(t, defaultSpecs())
	m := f.manager()
	var n atomic.Int32
	cancel := m.SubscribeChanges(func() { n.Add(1) })
	m.Select("v6")
	m.Select("tar")
	m.SetSpeedFor("v6", api.SpeedAuthentic)
	time.Sleep(4 * changeDelay)
	if got := n.Load(); got != 1 {
		t.Fatalf("%d notifications for a burst, want 1 (coalesced)", got)
	}
	m.Select("tar") // no change, no notification
	time.Sleep(4 * changeDelay)
	if n.Load() != 1 {
		t.Fatal("notified without a change")
	}
	if err := m.Install(context.Background(), "raw", false, ""); err != nil {
		t.Fatal(err)
	}
	time.Sleep(4 * changeDelay)
	if n.Load() < 2 {
		t.Fatal("install not notified")
	}
	cancel()
	before := n.Load()
	m.Select("v6")
	time.Sleep(4 * changeDelay)
	if n.Load() != before {
		t.Fatal("notified after cancel")
	}
}

// TestPrepareRun compares run.ini with golden files (the temporary data
// directory is written as $DATA). go test -run TestPrepareRun -update
// rewrites them.
func TestPrepareRun(t *testing.T) {
	f := newFixture(t, defaultSpecs())
	m := f.manager(func(o *Options) { o.AcceptLicenses = []string{"rt11"} })
	for _, id := range []string{"bsd", "v6", "rt11"} {
		if err := m.Install(context.Background(), id, false, ""); err != nil {
			t.Fatal(err)
		}
	}
	work := filepath.Join(f.data, "run")
	shmPath := filepath.Join(f.data, "panel")
	tests := []struct {
		golden string
		id     string
		ports  simh.Ports
		speed  api.Speed
	}{
		{"bsd-fast-tty.ini", "bsd", simh.Ports{Console: 23400, TTYTelnet: ":4000"}, api.SpeedFast},
		{"bsd-authentic.ini", "bsd", simh.Ports{Console: 23401}, api.SpeedAuthentic},
		{"v6-fast.ini", "v6", simh.Ports{Console: 23400, TTYTelnet: ":4000"}, api.SpeedFast},
		{"rt11-authentic.ini", "rt11", simh.Ports{Console: 23400}, api.SpeedAuthentic},
	}
	for _, tc := range tests {
		t.Run(tc.golden, func(t *testing.T) {
			if err := m.Select(tc.id); err != nil {
				t.Fatal(err)
			}
			rc, err := m.PrepareRun(work, tc.ports, shmPath, tc.speed)
			if err != nil {
				t.Fatal(err)
			}
			defer rc.Release()
			raw, err := os.ReadFile(rc.INIPath)
			if err != nil {
				t.Fatal(err)
			}
			got := strings.ReplaceAll(string(raw), f.data, "$DATA")
			path := filepath.Join("testdata", tc.golden)
			if *update {
				os.WriteFile(path, []byte(got), 0o644)
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run with -update)", err)
			}
			if got != string(want) {
				t.Fatalf("run.ini differs from %s:\n%s", path, got)
			}
			header := simh.RunHeader("1M", tc.speed, tc.ports.Console, shmPath)
			if rc.HeaderLines != len(header) || rc.SystemID != tc.id || rc.BootCommand != "boot rk0" ||
				rc.ReadyPattern != "login: " || rc.INIPath != filepath.Join(work, "run.ini") || len(rc.Disks) != len(f.files[tc.id]) {
				t.Fatalf("RunConfig %+v", rc)
			}
			lines := strings.Split(got, "\n")
			if lines[0] != "; generated by pdp11d - do not edit" || lines[len(header)-1] != "attach panel $DATA/panel" {
				t.Fatalf("header: %q", lines[:len(header)])
			}
		})
	}
	m.Select("bsd")
	rc, _ := m.PrepareRun(work, simh.Ports{Console: 23400}, shmPath, api.SpeedFast)
	if len(rc.Dialog) != 1 || rc.Dialog[0] != `expect -p "Press <CR> to boot" send "\r"; continue` {
		t.Fatalf("dialog %q", rc.Dialog)
	}
	rc.Release()

	// Failures: not installed, a disk missing, unusable paths.
	m.Select("tgz")
	if _, err := m.PrepareRun(work, simh.Ports{Console: 1}, shmPath, api.SpeedFast); !errors.Is(err, api.ErrNoSystem) {
		t.Fatalf("not installed: %v", err)
	}
	m.Select("v6")
	if _, err := m.PrepareRun(work, simh.Ports{Console: 1}, "/dev/shm/with space", api.SpeedFast); err == nil {
		t.Fatal("shm path with a blank accepted")
	}
	os.Remove(filepath.Join(m.SystemDir("v6"), "unix1_v6_rk.dsk"))
	if _, err := m.PrepareRun(work, simh.Ports{Console: 1}, shmPath, api.SpeedFast); err == nil {
		t.Fatal("missing disk accepted")
	}
}

// TestImageLeases: from PrepareRun until RunConfig.Release a simulator uses
// the selected system's images: an install or factory reset of that system
// and a selection change fail with ErrBusy meanwhile (they cannot race a
// power-on that the caller's "is it off?" check missed). A PrepareRun that
// fails leases nothing.
func TestImageLeases(t *testing.T) {
	f := newFixture(t, defaultSpecs())
	m := f.manager()
	ctx := context.Background()
	for _, id := range []string{"v6", "tar"} {
		if err := m.Install(ctx, id, false, ""); err != nil {
			t.Fatal(err)
		}
	}
	m.Select("v6")
	work, shmPath := filepath.Join(f.data, "run"), filepath.Join(f.data, "panel")
	rc, err := m.PrepareRun(work, simh.Ports{Console: 1}, shmPath, api.SpeedFast)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.FactoryReset(ctx, "v6", ""); !errors.Is(err, api.ErrBusy) {
		t.Fatalf("factory reset of the running system: %v", err)
	}
	os.Remove(filepath.Join(m.SystemDir("v6"), "unix1_v6_rk.dsk"))
	if err := m.Install(ctx, "v6", false, ""); !errors.Is(err, api.ErrBusy) {
		t.Fatalf("install of the running system: %v", err)
	}
	if err := m.Select("tar"); !errors.Is(err, api.ErrBusy) || m.Selected() != "v6" {
		t.Fatalf("selection change while running: %v", err)
	}
	if err := m.Select("v6"); err != nil {
		t.Fatal(err)
	}
	if err := m.FactoryReset(ctx, "tar", ""); err != nil {
		t.Fatalf("factory reset of another system: %v", err)
	}
	rc.Release()
	rc.Release()
	if err := m.Install(ctx, "v6", false, ""); err != nil {
		t.Fatal(err)
	}
	if err := m.Select("tar"); err != nil {
		t.Fatal(err)
	}
	m.Select("tgz") // not installed
	if _, err := m.PrepareRun(work, simh.Ports{Console: 1}, shmPath, api.SpeedFast); !errors.Is(err, api.ErrNoSystem) {
		t.Fatalf("PrepareRun: %v", err)
	}
	if err := m.Select("v6"); err != nil {
		t.Fatalf("a failed PrepareRun kept its lease: %v", err)
	}
}
