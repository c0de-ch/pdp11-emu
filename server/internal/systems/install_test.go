package systems

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"pdp11emu"
	"pdp11emu/server/internal/api"
	"pdp11emu/server/internal/catalog"
	"pdp11emu/server/internal/simh"
)

// checkInstalled verifies the working copies of id against the fixture.
func checkInstalled(t *testing.T, f *fixture, m *Manager, id string) {
	t.Helper()
	if !m.Installed(id) {
		t.Fatalf("%s not installed", id)
	}
	for _, fl := range f.files[id] {
		p := filepath.Join(m.SystemDir(id), filepath.FromSlash(fl.name))
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, fl.data) {
			t.Fatalf("%s: content differs", p)
		}
		if _, err := os.Stat(p + ".part"); err == nil {
			t.Fatalf("%s.part left behind", p)
		}
	}
}

func TestInstallFormats(t *testing.T) {
	f := newFixture(t, defaultSpecs())
	m := f.manager(func(o *Options) { o.AcceptLicenses = []string{"rt11"} })
	var pl progressLog
	m.SubscribeProgress(pl.add)
	for _, id := range []string{"bsd", "v6", "rt11", "raw", "gz", "tar", "tgz"} {
		t.Run(id, func(t *testing.T) {
			if m.Installed(id) {
				t.Fatal("installed before installing")
			}
			if err := m.Install(context.Background(), id, false, "req-"+id); err != nil {
				t.Fatal(err)
			}
			checkInstalled(t, f, m, id)
			ph := pl.phases(id)
			if ph[0] != "download" || ph[len(ph)-1] != "done" || !strings.Contains(strings.Join(ph, ","), "extract") {
				t.Fatalf("phases %v", ph)
			}
			if last := pl.last(id); last.Req != "req-"+id || last.Task != "install" || last.Pct != 100 {
				t.Fatalf("last progress %+v", last)
			}
			a, _ := f.archive(id)
			if _, err := os.Stat(filepath.Join(f.data, "cache", a.SHA256)); err != nil {
				t.Fatalf("archive not cached: %v", err)
			}
		})
	}
	// A second install is "done" at once, without downloading.
	hits := f.hitCount("/v6.zip")
	if err := m.Install(context.Background(), "v6", false, "again"); err != nil {
		t.Fatal(err)
	}
	if f.hitCount("/v6.zip") != hits || pl.last("v6").Req != "again" || pl.last("v6").Phase != "done" {
		t.Fatalf("reinstall: hits %d -> %d, %+v", hits, f.hitCount("/v6.zip"), pl.last("v6"))
	}
}

func TestSparseWorkingCopies(t *testing.T) {
	f := newFixture(t, defaultSpecs())
	m := f.manager()
	for _, id := range []string{"bsd", "gz", "v6"} {
		if err := m.Install(context.Background(), id, false, ""); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		id     string
		sparse bool
	}{{"bsd", true}, {"gz", true}, {"v6", false}} {
		fl := f.files[tc.id][0]
		fi, err := os.Stat(filepath.Join(m.SystemDir(tc.id), filepath.FromSlash(fl.name)))
		if err != nil {
			t.Fatal(err)
		}
		alloc := fi.Sys().(*syscall.Stat_t).Blocks * 512
		// diskImage leaves 4 of 5 blocks zero.
		if tc.sparse && alloc > fi.Size()/2 {
			t.Errorf("%s: %d of %d bytes allocated, want holes", tc.id, alloc, fi.Size())
		}
		if !tc.sparse && alloc < fi.Size() {
			t.Errorf("%s: %d of %d bytes allocated, want a full copy", tc.id, alloc, fi.Size())
		}
	}
}

func TestWriteBlocksTrailingHole(t *testing.T) {
	data := make([]byte, 5*blockSize+100)
	copy(data[blockSize:], "data")
	p := filepath.Join(t.TempDir(), "f")
	sum, err := writeFile(context.Background(), p, bytes.NewReader(data), int64(len(data)), true, func(int64) {})
	if err != nil || sum != sha(data) {
		t.Fatalf("writeFile: %s, %v", sum, err)
	}
	got, _ := os.ReadFile(p)
	if !bytes.Equal(got, data) {
		t.Fatal("content differs")
	}
	if _, err := writeFile(context.Background(), p, bytes.NewReader(data), int64(len(data)-1), true, func(int64) {}); err == nil {
		t.Fatal("oversized input accepted")
	}
	if _, err := writeFile(context.Background(), p, bytes.NewReader(data), int64(len(data)+1), true, func(int64) {}); err == nil {
		t.Fatal("short input accepted")
	}
}

func TestLicence(t *testing.T) {
	f := newFixture(t, defaultSpecs())
	m := f.manager()
	err := m.Install(context.Background(), "rt11", false, "")
	if !errors.Is(err, ErrLicense) {
		t.Fatalf("hobbyist install without acceptance: %v", err)
	}
	if err := m.Install(context.Background(), "rt11", true, ""); err != nil {
		t.Fatal(err)
	}
	if err := m.Install(context.Background(), "nope", true, ""); err == nil {
		t.Fatal("unknown id accepted")
	}
}

func TestDownloadFailures(t *testing.T) {
	t.Run("sha mismatch everywhere names file, size, sha and URLs", func(t *testing.T) {
		specs := defaultSpecs()[1:2] // v6
		specs[0].mirrors = []string{"/mirror/v6.zip"}
		f := newFixture(t, specs)
		f.content["/mirror/v6.zip"] = append([]byte(nil), f.content["/v6.zip"]...)
		f.content["/v6.zip"][100] ^= 1
		f.content["/mirror/v6.zip"][200] ^= 1
		m := f.manager()
		err := m.Install(context.Background(), "v6", false, "")
		if err == nil {
			t.Fatal("corrupt download accepted")
		}
		a, _ := f.archive("v6")
		for _, w := range []string{"v6.zip", strconv.FormatInt(a.Size, 10), a.SHA256, f.srv.URL + "/v6.zip", f.srv.URL + "/mirror/v6.zip", "attempt 3", "import"} {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("error lacks %q: %v", w, err)
			}
		}
		if f.hitCount("/v6.zip") != attemptsPerURL || f.hitCount("/mirror/v6.zip") != attemptsPerURL {
			t.Errorf("hits %d / %d", f.hitCount("/v6.zip"), f.hitCount("/mirror/v6.zip"))
		}
		if m.Installed("v6") {
			t.Fatal("installed")
		}
		entries, _ := os.ReadDir(filepath.Join(f.data, "cache"))
		if len(entries) != 0 {
			t.Fatalf("cache holds %v", entries)
		}
	})
	t.Run("404 moves to the mirror at once", func(t *testing.T) {
		specs := defaultSpecs()[1:2]
		specs[0].mirrors = []string{"/mirror/v6.zip"}
		f := newFixture(t, specs)
		f.content["/mirror/v6.zip"] = f.content["/v6.zip"]
		delete(f.content, "/v6.zip")
		m := f.manager()
		if err := m.Install(context.Background(), "v6", false, ""); err != nil {
			t.Fatal(err)
		}
		checkInstalled(t, f, m, "v6")
		if f.hitCount("/v6.zip") != 1 {
			t.Fatalf("404 retried %d times", f.hitCount("/v6.zip"))
		}
	})
	t.Run("server errors are retried", func(t *testing.T) {
		f := newFixture(t, defaultSpecs()[1:2])
		n := 0
		f.setHandler("/v6.zip", func(w http.ResponseWriter, r *http.Request) {
			if n++; n < 3 {
				http.Error(w, "busy", http.StatusServiceUnavailable)
				return
			}
			f.mu.Lock()
			body := f.content["/v6.zip"]
			f.mu.Unlock()
			http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(body))
		})
		m := f.manager()
		if err := m.Install(context.Background(), "v6", false, ""); err != nil {
			t.Fatal(err)
		}
		checkInstalled(t, f, m, "v6")
	})
}

// TestResume: an interrupted download continues with a Range request.
func TestResume(t *testing.T) {
	f := newFixture(t, defaultSpecs()[1:2])
	_, body := f.archive("v6")
	first := true
	f.setHandler("/v6.zip", func(w http.ResponseWriter, r *http.Request) {
		if first {
			first = false
			// Promise everything, deliver half, drop the connection.
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.WriteHeader(http.StatusOK)
			w.Write(body[:len(body)/2])
			w.(http.Flusher).Flush()
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(body))
	})
	m := f.manager()
	if err := m.Install(context.Background(), "v6", false, ""); err != nil {
		t.Fatal(err)
	}
	checkInstalled(t, f, m, "v6")
	if len(f.ranges) != 1 || f.ranges[0] != "/v6.zip bytes="+strconv.Itoa(len(body)/2)+"-" {
		t.Fatalf("range requests %q", f.ranges)
	}
}

// TestResumeNotSupported: a server that ignores Range sends everything again.
func TestResumeNotSupported(t *testing.T) {
	f := newFixture(t, defaultSpecs()[1:2])
	_, body := f.archive("v6")
	os.MkdirAll(filepath.Join(f.data, "cache"), 0o755)
	a, _ := f.archive("v6")
	os.WriteFile(filepath.Join(f.data, "cache", a.SHA256+".part"), body[:1000], 0o644)
	f.setHandler("/v6.zip", func(w http.ResponseWriter, r *http.Request) {
		w.Write(body) // 200, whole body, whatever the Range
	})
	m := f.manager()
	if err := m.Install(context.Background(), "v6", false, ""); err != nil {
		t.Fatal(err)
	}
	checkInstalled(t, f, m, "v6")
}

func TestStall(t *testing.T) {
	f := newFixture(t, defaultSpecs()[1:2])
	_, body := f.archive("v6")
	n := 0
	f.setHandler("/v6.zip", func(w http.ResponseWriter, r *http.Request) {
		if n++; n == 1 {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.Write(body[:1000])
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
			return
		}
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(body))
	})
	m := f.manager()
	m.dl.stall = 200 * time.Millisecond
	start := time.Now()
	if err := m.Install(context.Background(), "v6", false, ""); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("stalled download detected after %s", d)
	}
	checkInstalled(t, f, m, "v6")
}

func TestCachesAndImport(t *testing.T) {
	t.Run("read-only cache", func(t *testing.T) {
		f := newFixture(t, defaultSpecs()[1:2])
		a, body := f.archive("v6")
		ro := t.TempDir()
		os.WriteFile(filepath.Join(ro, a.SHA256), body, 0o444)
		delete(f.content, "/v6.zip") // no network
		m := f.manager(func(o *Options) { o.CacheDirs = []string{"/nonexistent", ro} })
		if err := m.Install(context.Background(), "v6", false, ""); err != nil {
			t.Fatal(err)
		}
		checkInstalled(t, f, m, "v6")
		if f.hitCount("/v6.zip") != 0 {
			t.Fatal("downloaded although cached")
		}
	})
	t.Run("import directory", func(t *testing.T) {
		f := newFixture(t, defaultSpecs()[1:2])
		a, body := f.archive("v6")
		delete(f.content, "/v6.zip")
		m := f.manager()
		imp := filepath.Join(f.data, "import")
		os.WriteFile(filepath.Join(imp, "decoy.zip"), append(bytes.Clone(body[:len(body)-1]), 'x'), 0o644) // same size, wrong sha
		os.WriteFile(filepath.Join(imp, "uv6swre.zip"), body, 0o644)
		if err := m.Install(context.Background(), "v6", false, ""); err != nil {
			t.Fatal(err)
		}
		checkInstalled(t, f, m, "v6")
		if _, err := os.Stat(filepath.Join(imp, "uv6swre.zip")); !os.IsNotExist(err) {
			t.Fatal("imported file not moved")
		}
		if _, err := os.Stat(filepath.Join(imp, "decoy.zip")); err != nil {
			t.Fatal("non-matching file touched")
		}
		if _, err := os.Stat(filepath.Join(f.data, "cache", a.SHA256)); err != nil {
			t.Fatal("imported file not in the cache")
		}
	})
	t.Run("corrupt cache entry is replaced", func(t *testing.T) {
		f := newFixture(t, defaultSpecs()[1:2])
		a, body := f.archive("v6")
		m := f.manager()
		bad := bytes.Clone(body)
		bad[10] ^= 0xff
		os.WriteFile(filepath.Join(f.data, "cache", a.SHA256), bad, 0o644)
		if err := m.Install(context.Background(), "v6", false, ""); err != nil {
			t.Fatal(err)
		}
		checkInstalled(t, f, m, "v6")
		if f.hitCount("/v6.zip") != 1 {
			t.Fatal("not downloaded again")
		}
	})
}

func TestFreeSpace(t *testing.T) {
	f := newFixture(t, defaultSpecs()[1:2])
	m := f.manager()
	freeSpaceOf = func(string) (int64, error) { return 1000, nil }
	defer func() { freeSpaceOf = freeSpace }()
	err := m.Install(context.Background(), "v6", false, "")
	if err == nil || !strings.Contains(err.Error(), "not enough free space") {
		t.Fatalf("Install: %v", err)
	}
	if m.Installed("v6") {
		t.Fatal("installed")
	}
}

func TestCancelAndBusy(t *testing.T) {
	f := newFixture(t, defaultSpecs())
	_, body := f.archive("v6")
	release := make(chan struct{})
	f.setHandler("/v6.zip", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.Write(body[:100])
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
			w.Write(body[100:])
		}
	})
	m := f.manager()
	var pl progressLog
	m.SubscribeProgress(pl.add)
	errc := make(chan error, 1)
	go func() { errc <- m.Install(context.Background(), "v6", false, "") }()
	waitFor(t, func() bool { i, _ := m.Get("v6"); return i.Installing })

	if err := m.Install(context.Background(), "v6", false, ""); !errors.Is(err, api.ErrBusy) {
		t.Fatalf("second install of the same system: %v", err)
	}
	// Another system waits for the first install.
	other := make(chan error, 1)
	go func() { other <- m.Install(context.Background(), "raw", false, "") }()
	waitFor(t, func() bool { return pl.last("raw").Text == "Waiting for another installation to finish" })

	a, _ := f.archive("v6")
	part := filepath.Join(f.data, "cache", a.SHA256+".part")
	waitFor(t, func() bool { fi, err := os.Stat(part); return err == nil && fi.Size() == 100 })
	m.Cancel("v6")
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled install: %v", err)
	}
	if pl.last("v6").Phase != "cancelled" {
		t.Fatalf("last v6 progress %+v", pl.last("v6"))
	}
	if err := <-other; err != nil {
		t.Fatal(err)
	}
	if i, _ := m.Get("v6"); i.Installing || i.Installed {
		t.Fatalf("v6 after cancel: %+v", i)
	}
	// The partial download is kept for resuming.
	close(release)
	f.setHandler("/v6.zip", nil)
	if err := m.Install(context.Background(), "v6", false, ""); err != nil {
		t.Fatal(err)
	}
	checkInstalled(t, f, m, "v6")
	if len(f.ranges) == 0 {
		t.Fatal("the next install did not resume")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timeout")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestFactoryReset(t *testing.T) {
	f := newFixture(t, defaultSpecs())
	m := f.manager()
	if err := m.FactoryReset(context.Background(), "v6", ""); err == nil {
		t.Fatal("factory reset of a system that is not installed")
	}
	if err := m.Install(context.Background(), "v6", false, ""); err != nil {
		t.Fatal(err)
	}
	disk := filepath.Join(m.SystemDir("v6"), "unix0_v6_rk.dsk")
	os.WriteFile(disk, []byte("scribbled by the guest"), 0o644)
	var pl progressLog
	m.SubscribeProgress(pl.add)
	hits := f.hitCount("/v6.zip")
	if err := m.FactoryReset(context.Background(), "v6", "r1"); err != nil {
		t.Fatal(err)
	}
	checkInstalled(t, f, m, "v6")
	if f.hitCount("/v6.zip") != hits {
		t.Fatal("factory reset downloaded although the archive is cached")
	}
	if last := pl.last("v6"); last.Task != "factoryReset" || last.Phase != "done" || last.Req != "r1" {
		t.Fatalf("progress %+v", last)
	}
	// The cache lost the archive: it is downloaded again.
	a, _ := f.archive("v6")
	os.Remove(filepath.Join(f.data, "cache", a.SHA256))
	if err := m.FactoryReset(context.Background(), "v6", ""); err != nil {
		t.Fatal(err)
	}
	if f.hitCount("/v6.zip") != hits+1 {
		t.Fatal("not downloaded again")
	}
}

func TestInstalledRequiresFiles(t *testing.T) {
	f := newFixture(t, defaultSpecs())
	m := f.manager()
	if err := m.Install(context.Background(), "tar", false, ""); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(m.SystemDir("tar"), "Disks", "b.dsk"))
	if m.Installed("tar") {
		t.Fatal("installed with a disk missing")
	}
	if err := m.Install(context.Background(), "tar", false, ""); err != nil {
		t.Fatal(err)
	}
	checkInstalled(t, f, m, "tar")
	os.WriteFile(filepath.Join(m.SystemDir("tar"), markerName), []byte(`{"id":"other"}`), 0o644)
	if m.Installed("tar") {
		t.Fatal("marker of another system accepted")
	}
}

func TestFetch(t *testing.T) {
	f := newFixture(t, defaultSpecs())
	m := f.manager()
	dir := filepath.Join(t.TempDir(), "cache")
	var pl progressLog
	if err := m.Fetch(context.Background(), dir, []string{"v6", "bsd"}, pl.add); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"v6", "bsd"} {
		a, body := f.archive(id)
		got, err := os.ReadFile(filepath.Join(dir, a.SHA256))
		if err != nil || !bytes.Equal(got, body) {
			t.Fatalf("%s not fetched: %v", id, err)
		}
		if m.Installed(id) {
			t.Fatalf("%s installed by Fetch", id)
		}
	}
	hits := f.hitCount("/v6.zip")
	if err := m.Fetch(context.Background(), dir, []string{"v6"}, nil); err != nil {
		t.Fatal(err)
	}
	if f.hitCount("/v6.zip") != hits {
		t.Fatal("fetched again")
	}
	if err := m.Fetch(context.Background(), dir, []string{"nope"}, nil); err == nil {
		t.Fatal("unknown id")
	}
	// The fetched cache serves an installation (as PDP11_CACHE).
	f2 := newFixture(t, defaultSpecs()[1:2])
	f2.cat = f.cat
	m2 := f2.manager(func(o *Options) { o.CacheDirs = []string{dir} })
	f.mu.Lock()
	delete(f.content, "/v6.zip")
	f.mu.Unlock()
	if err := m2.Install(context.Background(), "v6", false, ""); err != nil {
		t.Fatal(err)
	}
}

// testArchive returns the archive of a catalog system from
// PDP11_TEST_CACHE (catalog archives named by their sha256, as "pdp11d
// fetch --cache" writes them), or skips the test.
func testArchive(t *testing.T, id string) (catalog.Artifact, string) {
	t.Helper()
	dir := os.Getenv("PDP11_TEST_CACHE")
	if dir == "" {
		t.Skip("PDP11_TEST_CACHE (a directory of catalog archives) is not set")
	}
	sub, err := fs.Sub(pdp11emu.Systems, "systems")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.Load(sub)
	if err != nil {
		t.Fatal(err)
	}
	s, ok := cat.Get(id)
	if !ok {
		t.Fatalf("%s is not in the catalog", id)
	}
	a := s.Artifacts[0]
	p := filepath.Join(dir, strings.ToLower(a.SHA256))
	if _, err := os.Stat(p); err != nil {
		t.Skipf("the %s archive is not in PDP11_TEST_CACHE: %v", id, err)
	}
	return a, p
}

// TestRealTarZ extracts a real compress(1) archive: the catalog's RT-11
// kit.
func TestRealTarZ(t *testing.T) {
	a, src := testArchive(t, "rt11")
	if _, err := exec.LookPath("gzip"); err != nil {
		t.Skip("no gzip")
	}
	dir := t.TempDir()
	target := func(f catalog.ArtifactFile) string { return filepath.Join(dir, filepath.Base(f.Path)) }
	if err := extract(context.Background(), a, src, target, func(int64) {}); err != nil {
		t.Fatal(err)
	}
	for _, f := range a.Files {
		if fi, err := os.Stat(target(f)); err != nil || fi.Size() != f.Size {
			t.Fatalf("%s: %v", f.Path, err)
		}
	}
}

// blockAt makes the extraction stop when it opens path: a FIFO without a
// reader blocks the open. unblock lets the open return (the write then
// fails: a FIFO cannot be written at an offset).
func blockAt(t *testing.T, path string) (unblock func()) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o640); err != nil {
		t.Fatal(err)
	}
	return func() {
		if r, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0); err == nil {
			t.Cleanup(func() { r.Close() })
		}
	}
}

// TestFactoryResetAborted: a factory reset of a multi-disk system that is
// cancelled, or fails, in the middle of the extraction keeps the old
// images and the marker (the system stays installed), and leaves nothing
// of the attempt behind. While it runs, a power-on is refused.
func TestFactoryResetAborted(t *testing.T) {
	for _, cancel := range []bool{true, false} {
		t.Run(fmt.Sprintf("cancel=%v", cancel), func(t *testing.T) {
			f := newFixture(t, defaultSpecs()[1:2]) // v6: two disks in a zip
			m := f.manager()
			if err := m.Install(context.Background(), "v6", false, ""); err != nil {
				t.Fatal(err)
			}
			var pl progressLog
			m.SubscribeProgress(pl.add)
			dir := m.SystemDir("v6")
			disks := []string{filepath.Join(dir, "unix0_v6_rk.dsk"), filepath.Join(dir, "unix1_v6_rk.dsk")}
			for i, d := range disks {
				os.WriteFile(d, []byte(fmt.Sprintf("guest data %d", i)), 0o640)
			}
			mk, _ := os.ReadFile(filepath.Join(dir, markerName))
			unblock := blockAt(t, disks[1]+".part")
			errc := make(chan error, 1)
			go func() { errc <- m.FactoryReset(context.Background(), "v6", "") }()
			// The first disk is extracted, the second one waits.
			first := f.files["v6"][0].data
			waitFor(t, func() bool {
				a, _ := os.ReadFile(disks[0] + ".part")
				b, _ := os.ReadFile(disks[0])
				return bytes.Equal(a, first) || bytes.Equal(b, first)
			})
			if _, err := m.PrepareRun(filepath.Join(f.data, "run"), simh.Ports{Console: 1}, filepath.Join(f.data, "panel"), api.SpeedFast); !errors.Is(err, api.ErrBusy) {
				t.Fatalf("power-on during the reset: %v, want ErrBusy", err)
			}
			if cancel {
				m.Cancel("v6")
			}
			unblock()
			err := <-errc
			if cancel && !errors.Is(err, context.Canceled) || !cancel && (err == nil || errors.Is(err, context.Canceled)) {
				t.Fatalf("FactoryReset: %v", err)
			}
			if last := pl.last("v6"); cancel && last.Phase != "cancelled" || !cancel && last.Phase != "error" {
				t.Fatalf("last progress %+v", last)
			}
			if !m.Installed("v6") {
				t.Fatal("the system is no longer installed")
			}
			if got, _ := os.ReadFile(filepath.Join(dir, markerName)); !bytes.Equal(got, mk) {
				t.Fatalf("marker changed: %s", got)
			}
			for i, d := range disks {
				if got, _ := os.ReadFile(d); string(got) != fmt.Sprintf("guest data %d", i) {
					t.Fatalf("%s was replaced", d)
				}
				if _, err := os.Lstat(d + ".part"); !os.IsNotExist(err) {
					t.Fatalf("%s.part left behind (%v)", d, err)
				}
			}
			// The next reset replaces both disks.
			if err := m.FactoryReset(context.Background(), "v6", ""); err != nil {
				t.Fatal(err)
			}
			checkInstalled(t, f, m, "v6")
		})
	}
}

// TestFactoryResetFreeSpace: the new images are extracted next to the old
// ones, so a reset needs their whole size free; a refused reset leaves the
// system as it was.
func TestFactoryResetFreeSpace(t *testing.T) {
	f := newFixture(t, defaultSpecs()[1:2]) // v6, not sparse
	m := f.manager()
	if err := m.Install(context.Background(), "v6", false, ""); err != nil {
		t.Fatal(err)
	}
	disk := filepath.Join(m.SystemDir("v6"), "unix0_v6_rk.dsk")
	os.WriteFile(disk, []byte("guest data"), 0o640)
	var need int64
	for _, fl := range f.files["v6"] {
		need += int64(len(fl.data))
	}
	defer func() { freeSpaceOf = freeSpace }()
	freeSpaceOf = func(string) (int64, error) { return need - 1, nil }
	err := m.FactoryReset(context.Background(), "v6", "")
	if err == nil || !strings.Contains(err.Error(), "not enough free space") {
		t.Fatalf("FactoryReset with %d of %d bytes free: %v", need-1, need, err)
	}
	if got, _ := os.ReadFile(disk); !m.Installed("v6") || string(got) != "guest data" {
		t.Fatal("a refused reset changed the system")
	}
	freeSpaceOf = func(string) (int64, error) { return need, nil }
	if err := m.FactoryReset(context.Background(), "v6", ""); err != nil {
		t.Fatal(err)
	}
	checkInstalled(t, f, m, "v6")
}

// limitFileSize makes writes beyond n bytes fail (EFBIG, as ENOSPC would
// on a full disk; Go ignores SIGXFSZ) until restore or the end of the test.
func limitFileSize(t *testing.T, n uint64) (restore func()) {
	t.Helper()
	var old syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &old); err != nil {
		t.Fatal(err)
	}
	lim := syscall.Rlimit{Cur: n, Max: old.Max}
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &lim); err != nil {
		t.Fatal(err)
	}
	restore = func() { syscall.Setrlimit(syscall.RLIMIT_FSIZE, &old) }
	t.Cleanup(restore)
	return restore
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// TestCopyArchive: a copy into a cache is renamed into place only when
// complete and verified, through a name of its own (a resumable download
// of the archive is not touched), and a failed write is reported.
func TestCopyArchive(t *testing.T) {
	data := diskImage(10, 50*1024)
	a := catalog.Artifact{SHA256: sha(data), Size: int64(len(data))}
	src := filepath.Join(t.TempDir(), "kit.zip")
	os.WriteFile(src, data, 0o600)
	cache := t.TempDir()
	dst := filepath.Join(cache, a.SHA256)
	os.WriteFile(dst+".part", []byte("resumable download"), 0o644)
	ctx := context.Background()

	if err := copyArchive(ctx, src, dst, a); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst); !bytes.Equal(got, data) {
		t.Fatal("copy differs")
	}
	if fi, err := os.Stat(dst); err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("copy: %v %v", fi, err)
	}
	os.Remove(dst)
	bad := a
	bad.SHA256 = sha([]byte("another archive"))
	short := a
	short.Size++
	for _, x := range []catalog.Artifact{bad, short} {
		if err := copyArchive(ctx, src, dst, x); err == nil {
			t.Fatalf("copy accepted for %+v", x)
		}
	}
	restore := limitFileSize(t, 4096)
	err := copyArchive(ctx, src, dst, a)
	restore()
	if err == nil {
		t.Fatal("a failed write was not reported")
	}
	if names := dirNames(t, cache); len(names) != 1 || names[0] != a.SHA256+".part" {
		t.Fatalf("cache holds %v, want only the download's .part", names)
	}
	if got, _ := os.ReadFile(dst + ".part"); string(got) != "resumable download" {
		t.Fatalf(".part now holds %q", got)
	}
}

// otherFileSystem returns a new directory on another file system than the
// test's temporary directories (/dev/shm), or skips the test.
func otherFileSystem(t *testing.T) string {
	t.Helper()
	var a, b syscall.Stat_t
	if syscall.Stat(t.TempDir(), &a) != nil || syscall.Stat("/dev/shm", &b) != nil || a.Dev == b.Dev {
		t.Skip("needs /dev/shm on another file system than the temporary directory")
	}
	dir, err := os.MkdirTemp("/dev/shm", "pdp11d-test-")
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// TestImportFromAnotherFileSystem: an archive in an import directory on
// another file system is copied; while the disk is full the install fails
// and the user's file stays where it was, nothing is cached.
func TestImportFromAnotherFileSystem(t *testing.T) {
	other := otherFileSystem(t)
	f := newFixture(t, defaultSpecs()[1:2])
	a, body := f.archive("v6")
	delete(f.content, "/v6.zip") // no network
	m := f.manager()
	imp := filepath.Join(f.data, "import")
	os.Remove(imp)
	if err := os.Symlink(other, imp); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(other, "uv6swre.zip")
	os.WriteFile(src, body, 0o644)
	cached := filepath.Join(f.data, "cache", a.SHA256)

	restore := limitFileSize(t, 4096)
	err := m.Install(context.Background(), "v6", false, "")
	restore()
	if err == nil || !strings.Contains(err.Error(), "importing") {
		t.Fatalf("install on a full disk: %v", err)
	}
	if got, _ := os.ReadFile(src); !bytes.Equal(got, body) {
		t.Fatal("the imported file was lost")
	}
	if names := dirNames(t, filepath.Join(f.data, "cache")); len(names) != 0 {
		t.Fatalf("cache holds %v", names)
	}
	if err := m.Install(context.Background(), "v6", false, ""); err != nil {
		t.Fatal(err)
	}
	checkInstalled(t, f, m, "v6")
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("imported file not moved")
	}
	if got, _ := os.ReadFile(cached); !bytes.Equal(got, body) {
		t.Fatal("cache copy differs")
	}
}

// TestFetchSearchesEveryCache: an archive in any read-only cache is copied
// into the fetch cache instead of being downloaded.
func TestFetchSearchesEveryCache(t *testing.T) {
	f := newFixture(t, defaultSpecs()[1:2])
	a, body := f.archive("v6")
	ro := t.TempDir()
	os.WriteFile(filepath.Join(ro, a.SHA256), body, 0o444)
	dst := t.TempDir()
	m := f.manager(func(o *Options) { o.CacheDirs = []string{dst, ro} })
	if err := m.Fetch(context.Background(), dst, []string{"v6"}, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dst, a.SHA256)); !bytes.Equal(got, body) {
		t.Fatal("not fetched")
	}
	if f.hitCount("/v6.zip") != 0 {
		t.Fatal("downloaded although another cache had it")
	}
}

// TestModes: data directories are created 0750 and disk images 0640, state
// files are 0640 whatever the umask: other local users cannot read the
// guest disks.
func TestModes(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	f := newFixture(t, defaultSpecs()[1:2])
	m := f.manager()
	if err := m.Install(context.Background(), "v6", false, ""); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(f.data, "run")
	rc, err := m.PrepareRun(work, simh.Ports{Console: 1}, filepath.Join(f.data, "panel"), api.SpeedFast)
	if err != nil {
		t.Fatal(err)
	}
	rc.Release()
	dir := m.SystemDir("v6")
	for p, want := range map[string]os.FileMode{
		filepath.Join(f.data, "cache"): 0o750, filepath.Join(f.data, "import"): 0o750, dir: 0o750, work: 0o750,
		filepath.Join(dir, "unix0_v6_rk.dsk"): 0o640, filepath.Join(dir, markerName): 0o640,
		filepath.Join(f.data, "systems.json"): 0o640, rc.INIPath: 0o640,
	} {
		if fi, err := os.Stat(p); err != nil {
			t.Error(err)
		} else if fi.Mode().Perm() != want {
			t.Errorf("%s: mode %v, want %v", p, fi.Mode().Perm(), want)
		}
	}
}
