package systems

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"pdp11emu/server/internal/api"
	"pdp11emu/server/internal/atomicfile"
	"pdp11emu/server/internal/catalog"
)

// markerName is the file in a system directory that records a completed
// installation.
const markerName = ".installed"

// marker is the content of .installed.
type marker struct {
	ID          string           `json:"id"`
	InstalledAt time.Time        `json:"installedAt"`
	Artifacts   []markerArtifact `json:"artifacts"`
}

type markerArtifact struct {
	SHA256 string       `json:"sha256"`
	Files  []markerFile `json:"files"`
}

type markerFile struct {
	Path   string `json:"path"`
	Role   string `json:"role"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Installed reports whether a system's working images are present: the
// marker of a completed installation exists and names this system, and
// every working file exists.
func (m *Manager) Installed(id string) bool {
	sys, ok := m.cat.Get(id)
	if !ok {
		return false
	}
	raw, err := os.ReadFile(filepath.Join(m.SystemDir(id), markerName))
	if err != nil {
		return false
	}
	var mk marker
	if json.Unmarshal(raw, &mk) != nil || mk.ID != id {
		return false
	}
	for _, a := range sys.Artifacts {
		for _, f := range a.Files {
			if fi, err := os.Stat(m.workingPath(id, f)); err != nil || !fi.Mode().IsRegular() {
				return false
			}
		}
	}
	return true
}

// Install downloads (cache, import dir, url, mirrors), verifies and
// extracts a system.
func (m *Manager) Install(ctx context.Context, id string, acceptLicense bool, req string) error {
	sys, ok := m.cat.Get(id)
	if !ok {
		return fmt.Errorf("unknown system %q", id)
	}
	if !acceptLicense && !m.accepted(sys) {
		return fmt.Errorf("%s (%s): %w", sys.Name, sys.License.Name, ErrLicense)
	}
	if m.Installed(id) {
		m.mu.Lock()
		_, busy := m.tasks[id]
		m.mu.Unlock()
		if !busy {
			m.publish(api.Progress{Task: "install", ID: id, Phase: "done", Pct: 100, Text: sys.Name + " is installed", Req: req})
			return nil
		}
	}
	return m.runTask(ctx, sys, "install", req)
}

// FactoryReset restores the working disk images from the pristine archive
// (downloading it again if the cache lost it).
func (m *Manager) FactoryReset(ctx context.Context, id string, req string) error {
	sys, ok := m.cat.Get(id)
	if !ok {
		return fmt.Errorf("unknown system %q", id)
	}
	if !m.Installed(id) {
		return fmt.Errorf("%s is not installed", sys.Name)
	}
	return m.runTask(ctx, sys, "factoryReset", req)
}

// Cancel cancels a running install or factory reset of id.
func (m *Manager) Cancel(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.tasks[id]; ok {
		t.cancel()
	}
}

// progressFunc reports a phase of a task.
type progressFunc func(phase string, done, total int64, text string)

// runTask runs an install or factory reset of sys: one task per system,
// one task at a time overall, progress to all subscribers.
func (m *Manager) runTask(ctx context.Context, sys *catalog.System, kind, req string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m.mu.Lock()
	if _, busy := m.tasks[sys.ID]; busy {
		m.mu.Unlock()
		return api.ErrBusy
	}
	if m.leases[sys.ID] > 0 {
		m.mu.Unlock()
		return fmt.Errorf("%s is in use by the simulator: %w", sys.Name, api.ErrBusy)
	}
	m.tasks[sys.ID] = &task{kind: kind, cancel: cancel}
	m.mu.Unlock()
	m.changed()
	defer func() {
		m.mu.Lock()
		delete(m.tasks, sys.ID)
		m.mu.Unlock()
		m.changed()
	}()

	progress := m.progressPublisher(kind, sys.ID, req)
	select {
	case m.installing <- struct{}{}:
	default:
		progress("download", 0, 0, "Waiting for another installation to finish")
		select {
		case m.installing <- struct{}{}:
		case <-ctx.Done():
			progress("cancelled", 0, 0, "Cancelled")
			return ctx.Err()
		}
	}
	defer func() { <-m.installing }()

	m.log.Info("installing", "task", kind, "system", sys.ID)
	err := m.install(ctx, sys, progress)
	// Decompression (the xz dictionary) and the copy buffers peak at about
	// 200 MB: return that memory now instead of at the scavenger's pace.
	debug.FreeOSMemory()
	switch {
	case err == nil:
		m.log.Info("installed", "task", kind, "system", sys.ID)
		progress("done", 1, 1, sys.Name+" is installed")
	case ctx.Err() != nil:
		m.log.Info("installation cancelled", "task", kind, "system", sys.ID)
		progress("cancelled", 0, 0, "Cancelled")
		return ctx.Err()
	default:
		m.log.Error("installation failed", "task", kind, "system", sys.ID, "err", err)
		progress("error", 0, 0, err.Error())
	}
	return err
}

// progressPublisher returns a progressFunc that publishes api.Progress,
// rate-limited to a few updates per second within a phase.
func (m *Manager) progressPublisher(kind, id, req string) progressFunc {
	var last time.Time
	lastPhase := ""
	return func(phase string, done, total int64, text string) {
		now := time.Now()
		if phase == lastPhase && done < total && now.Sub(last) < 250*time.Millisecond {
			return
		}
		last, lastPhase = now, phase
		p := api.Progress{Task: kind, ID: id, Phase: phase, Bytes: done, Total: total, Text: text, Req: req}
		if total > 0 {
			p.Pct = float64(done) * 100 / float64(total)
		}
		if phase == "done" {
			p.Pct = 100
		}
		m.publish(p)
	}
}

// install obtains every archive, checks the free space and replaces the
// working copies in two phases. First every file is extracted and verified
// next to its working copy (<path>.part): a cancel or an error in this
// phase removes those files and leaves the old images and the marker as
// they were. Then commit puts the new files in place.
func (m *Manager) install(ctx context.Context, sys *catalog.System, progress progressFunc) error {
	archives := make([]string, len(sys.Artifacts))
	for i, a := range sys.Artifacts {
		p, err := m.obtain(ctx, a, m.cacheDir(), progress)
		if err != nil {
			return err
		}
		archives[i] = p
	}
	dir := m.SystemDir(sys.ID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	// All new files coexist with the old copies until the commit.
	var need, total int64
	for _, a := range sys.Artifacts {
		for _, f := range a.Files {
			total += f.Size
			need += f.Size
			if f.Sparse {
				need -= f.Size * 3 / 4 // typically far less: holes are not written
			}
			// A file left by an interrupted attempt is overwritten.
			if fi, err := os.Stat(m.partPath(sys.ID, f)); err == nil {
				need -= allocated(fi)
			}
		}
	}
	if free, err := freeSpaceOf(dir); err == nil && free < need {
		return fmt.Errorf("not enough free space in %s: %s needed, %s available", dir, human(need), human(free))
	}

	mk := marker{ID: sys.ID}
	var written int64
	for i, a := range sys.Artifacts {
		ma := markerArtifact{SHA256: a.SHA256}
		err := extract(ctx, a, archives[i], func(f catalog.ArtifactFile) string { return m.partPath(sys.ID, f) },
			func(n int64) { progress("extract", written+n, total, "Extracting") })
		if err != nil {
			m.removeParts(sys)
			return err
		}
		for _, f := range a.Files {
			written += f.Size
			ma.Files = append(ma.Files, markerFile{Path: f.Path, Role: f.Role, SHA256: f.SHA256, Size: f.Size})
		}
		mk.Artifacts = append(mk.Artifacts, ma)
	}
	if err := ctx.Err(); err != nil {
		m.removeParts(sys)
		return err
	}
	return m.commit(sys, mk)
}

// commit puts the extracted files in place: it removes the marker (a
// partly replaced set of images must never look installed), renames every
// file over its working copy and writes the marker. It is short and not
// cancelled; a crash in between leaves the system without a marker, so
// that it is installed again.
func (m *Manager) commit(sys *catalog.System, mk marker) error {
	mk.InstalledAt = time.Now().UTC()
	raw, err := json.MarshalIndent(mk, "", "  ")
	if err != nil {
		m.removeParts(sys)
		return err
	}
	dir := m.SystemDir(sys.ID)
	if err := os.Remove(filepath.Join(dir, markerName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		m.removeParts(sys)
		return err
	}
	dirs := map[string]bool{}
	for _, a := range sys.Artifacts {
		for _, f := range a.Files {
			p := m.workingPath(sys.ID, f)
			if err := os.Rename(m.partPath(sys.ID, f), p); err != nil {
				m.removeParts(sys)
				return err
			}
			dirs[filepath.Dir(p)] = true
		}
	}
	// The renames must be on disk before the marker that vouches for them
	// (atomicfile syncs the marker's own directory).
	for d := range dirs {
		if d != dir {
			syncDir(d)
		}
	}
	return atomicfile.WriteFile(filepath.Join(dir, markerName), append(raw, '\n'), 0o640)
}

// partPath is where a file of an artifact is extracted before the commit.
func (m *Manager) partPath(id string, f catalog.ArtifactFile) string {
	return m.workingPath(id, f) + ".part"
}

// removeParts deletes the extracted files of an installation that did not
// complete.
func (m *Manager) removeParts(sys *catalog.System) {
	for _, a := range sys.Artifacts {
		for _, f := range a.Files {
			if err := os.Remove(m.partPath(sys.ID, f)); err != nil && !errors.Is(err, os.ErrNotExist) {
				m.log.Warn("cannot remove a partly installed file", "err", err)
			}
		}
	}
}

// obtain returns the path of a verified copy of archive a: from dst (the
// cache being filled), the read-only caches or the import directory, or
// downloaded into dst.
func (m *Manager) obtain(ctx context.Context, a catalog.Artifact, dst string, progress progressFunc) (string, error) {
	name := strings.ToLower(a.SHA256)
	verify := func(p string) error {
		return verifyFile(ctx, p, a.Size, a.SHA256, func(n int64) {
			progress("verify", n, a.Size, "Verifying "+archiveName(a))
		})
	}
	cached := filepath.Join(dst, name)
	switch err := verify(cached); {
	case err == nil:
		return cached, nil
	case ctx.Err() != nil:
		return "", ctx.Err()
	case !errors.Is(err, os.ErrNotExist):
		m.log.Warn("discarding a corrupt cached archive", "file", cached, "err", err)
		os.Remove(cached)
	}
	for _, d := range append(append([]string(nil), m.opts.CacheDirs...), m.cacheDir()) {
		p := filepath.Join(d, name)
		if filepath.Clean(p) == filepath.Clean(cached) {
			continue
		}
		if err := verify(p); err == nil {
			os.Remove(cached + ".part") // an older partial download is not needed
			return p, nil
		} else if ctx.Err() != nil {
			return "", ctx.Err()
		}
	}
	if filepath.Clean(dst) == filepath.Clean(m.cacheDir()) {
		if p, err := m.fromImport(ctx, a, dst, verify); err != nil || p != "" {
			if err == nil {
				os.Remove(cached + ".part")
			}
			return p, err
		}
	}
	if err := m.dl.download(ctx, a, dst, func(n int64) {
		progress("download", n, a.Size, "Downloading "+archiveName(a))
	}); err != nil {
		return "", fmt.Errorf("%w (you can also place the file in %s)", err, m.importDir())
	}
	if err := verify(cached); err != nil {
		return "", err
	}
	return cached, nil
}

// fromImport looks for the archive among the files dropped into
// $DATA/import (matching size and sha256) and moves it into dst.
func (m *Manager) fromImport(ctx context.Context, a catalog.Artifact, dst string, verify func(string) error) (string, error) {
	entries, err := os.ReadDir(m.importDir())
	if err != nil {
		return "", nil
	}
	for _, e := range entries {
		p := filepath.Join(m.importDir(), e.Name())
		fi, err := os.Stat(p)
		if err != nil || !fi.Mode().IsRegular() || fi.Size() != a.Size || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if err := verify(p); err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			continue
		}
		target := filepath.Join(dst, strings.ToLower(a.SHA256))
		if err := moveArchive(ctx, p, target, a); err != nil {
			return "", fmt.Errorf("importing %s: %w", p, err)
		}
		m.log.Info("imported archive", "file", e.Name(), "sha256", a.SHA256)
		return target, nil
	}
	return "", nil
}

// Fetch downloads and verifies the artifacts of the given systems into
// cacheDir without extracting them (used by "pdp11d fetch" at image build).
func (m *Manager) Fetch(ctx context.Context, cacheDir string, ids []string, progress func(api.Progress)) error {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return err
	}
	if progress == nil {
		progress = func(api.Progress) {}
	}
	for _, id := range ids {
		sys, ok := m.cat.Get(id)
		if !ok {
			return fmt.Errorf("unknown system %q", id)
		}
		for _, a := range sys.Artifacts {
			pf := func(phase string, done, total int64, text string) {
				p := api.Progress{Task: "install", ID: id, Phase: phase, Bytes: done, Total: total, Text: text}
				if total > 0 {
					p.Pct = float64(done) * 100 / float64(total)
				}
				progress(p)
			}
			if _, err := m.obtainInto(ctx, a, cacheDir, pf); err != nil {
				return fmt.Errorf("%s: %w", id, err)
			}
		}
		progress(api.Progress{Task: "install", ID: id, Phase: "done", Pct: 100, Text: sys.Name + " fetched"})
	}
	return nil
}

// obtainInto is obtain with the result guaranteed to be in dir (a copy is
// made when the archive was found elsewhere).
func (m *Manager) obtainInto(ctx context.Context, a catalog.Artifact, dir string, progress progressFunc) (string, error) {
	p, err := m.obtain(ctx, a, dir, progress)
	if err != nil {
		return "", err
	}
	target := filepath.Join(dir, strings.ToLower(a.SHA256))
	if filepath.Clean(p) == filepath.Clean(target) {
		return target, nil
	}
	if err := copyArchive(ctx, p, target, a); err != nil {
		return "", err
	}
	return target, nil
}

// ---- file helpers ------------------------------------------------------------

// verifyFile checks a file's size and sha256 (os.ErrNotExist if absent).
func verifyFile(ctx context.Context, p string, size int64, sum string, progress func(int64)) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if fi.Size() != size {
		return fmt.Errorf("%s: size %d, want %d", p, fi.Size(), size)
	}
	h := sha256.New()
	if _, err := io.Copy(h, &progressReader{ctx: ctx, r: f, fn: progress}); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, sum) {
		return fmt.Errorf("%s: sha256 %s, want %s", p, got, sum)
	}
	return nil
}

// progressReader reports the bytes read and honours cancellation.
type progressReader struct {
	ctx context.Context
	r   io.Reader
	n   int64
	fn  func(int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	if err := p.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := p.r.Read(b)
	p.n += int64(n)
	if p.fn != nil && n > 0 {
		p.fn(p.n)
	}
	return n, err
}

// moveArchive moves the verified archive src (an import) into a cache: a
// rename, or into another file system a verified copy, after which the
// source is removed.
func moveArchive(ctx context.Context, src, dst string, a catalog.Artifact) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	} else if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	if err := copyArchive(ctx, src, dst, a); err != nil {
		return err
	}
	return os.Remove(src)
}

// copyArchive copies the archive src to dst through a temporary file next
// to dst (not dst.part, which holds a resumable download) and renames it
// into place only once size and sha256 of what was written match a.
func copyArchive(ctx context.Context, src, dst string, a catalog.Artifact) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".copy-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	tmp.Close()
	sum, err := writeFile(ctx, name, in, a.Size, false, func(int64) {})
	if err == nil && !strings.EqualFold(sum, a.SHA256) {
		err = fmt.Errorf("copy of %s: sha256 %s, want %s", src, sum, a.SHA256)
	}
	if err == nil {
		err = os.Chmod(name, 0o644) // like a download: caches may be shared
	}
	if err == nil {
		err = os.Rename(name, dst)
	}
	if err != nil {
		os.Remove(name)
	}
	return err
}

// syncDir makes the entries of a directory durable (best effort: some
// file systems do not support it).
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

// freeSpaceOf is replaced in tests.
var freeSpaceOf = freeSpace

func freeSpace(dir string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}

// allocated returns the disk space a file occupies.
func allocated(fi os.FileInfo) int64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Blocks * 512
	}
	return fi.Size()
}

func human(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d KB", (n+1023)>>10)
}

// archiveName is the file name of an artifact's URL.
func archiveName(a catalog.Artifact) string {
	u := a.URL
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	if b := path.Base(u); b != "" && b != "/" && b != "." {
		return b
	}
	return a.SHA256
}

// cleanArchivePath normalizes a path inside an archive ("./a//b" -> "a/b").
func cleanArchivePath(p string) string {
	return strings.TrimPrefix(path.Clean("/"+strings.ReplaceAll(p, "\\", "/")), "/")
}
