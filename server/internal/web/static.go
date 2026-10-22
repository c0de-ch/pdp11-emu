package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"html"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// staticFiles serves index.html (with the version substituted, never
// cached) and assets/... (revalidated with a strong ETag on every use, so
// a new build is picked up at once).
type staticFiles struct {
	fsys    fs.FS
	version string
	log     *slog.Logger

	mu    sync.Mutex
	cache map[string]*asset
}

type asset struct {
	data    []byte
	etag    string
	size    int64
	modTime time.Time
}

func newStaticFiles(fsys fs.FS, version string, log *slog.Logger) *staticFiles {
	if fsys == nil {
		log.Warn("no UI files configured; only the API is served")
	}
	return &staticFiles{fsys: fsys, version: version, log: log, cache: map[string]*asset{}}
}

func (st *staticFiles) serveIndex(w http.ResponseWriter, r *http.Request) {
	if st.fsys == nil {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(st.fsys, "index.html")
	if err != nil {
		st.log.Error("cannot read index.html", "err", err)
		http.Error(w, "the UI is not available", http.StatusInternalServerError)
		return
	}
	data = bytes.ReplaceAll(data, []byte("{{VERSION}}"), []byte(html.EscapeString(st.version)))
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Length", strconv.Itoa(len(data)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(data)
}

func (st *staticFiles) serveAsset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	if st.fsys == nil || !fs.ValidPath(name) || strings.HasSuffix(r.URL.Path, "/") {
		http.NotFound(w, r)
		return
	}
	a, err := st.load(name)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			st.log.Error("cannot read asset", "name", name, "err", err)
		}
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", contentType(name))
	h.Set("Cache-Control", "no-cache")
	h.Set("ETag", a.etag)
	http.ServeContent(w, r, name, a.modTime, bytes.NewReader(a.data))
}

// withBuildID appends "+" and the build id of the UI files to version
// (PROTOCOL.md §1.2), unless it already has a "+" part or there is no UI.
// Pages reload when the version they were served with differs from the
// server's, so this makes every UI change reload them, even when the
// version string stays the same (a build without VERSION).
func withBuildID(version string, fsys fs.FS) string {
	if fsys == nil || strings.Contains(version, "+") {
		return version
	}
	return version + "+" + buildID(fsys)
}

// buildID is the first 12 hex digits of SHA-256 over the served UI files
// (index.html and assets/...) sorted by path: for each its path, NUL and
// its content. Files that cannot be read are left out (they cannot be
// served either).
func buildID(fsys fs.FS) string {
	names := []string{"index.html"}
	_ = fs.WalkDir(fsys, "assets", func(name string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			names = append(names, name)
		}
		return nil
	})
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			continue
		}
		h.Write([]byte(name))
		h.Write([]byte{0})
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// load returns an asset, hashing it once per content version (the
// embedded files never change; a development directory may).
func (st *staticFiles) load(name string) (*asset, error) {
	fi, err := fs.Stat(st.fsys, name)
	if err != nil {
		return nil, err
	}
	if fi.IsDir() {
		return nil, fs.ErrNotExist
	}
	st.mu.Lock()
	a := st.cache[name]
	st.mu.Unlock()
	if a != nil && a.size == fi.Size() && a.modTime.Equal(fi.ModTime()) {
		return a, nil
	}
	data, err := fs.ReadFile(st.fsys, name)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	a = &asset{
		data:    data,
		etag:    `"` + base64.RawURLEncoding.EncodeToString(sum[:18]) + `"`,
		size:    int64(len(data)),
		modTime: fi.ModTime(),
	}
	if a.size != fi.Size() {
		a.modTime = time.Time{} // changed while reading: do not trust the cache key
	}
	st.mu.Lock()
	st.cache[name] = a
	st.mu.Unlock()
	return a, nil
}

// contentTypes are set explicitly: ES modules need a JavaScript type and
// the system's MIME table may be missing in a slim container.
var contentTypes = map[string]string{
	".html":  "text/html; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".mjs":   "text/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".json":  "application/json",
	".map":   "application/json",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".gif":   "image/gif",
	".webp":  "image/webp",
	".ico":   "image/x-icon",
	".woff":  "font/woff",
	".woff2": "font/woff2",
	".ttf":   "font/ttf",
	".otf":   "font/otf",
	".txt":   "text/plain; charset=utf-8",
	".md":    "text/markdown; charset=utf-8",
	".wasm":  "application/wasm",
	".xml":   "application/xml",
}

func contentType(name string) string {
	ext := strings.ToLower(path.Ext(name))
	if t, ok := contentTypes[ext]; ok {
		return t
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return t
	}
	return "application/octet-stream"
}
