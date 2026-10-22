package systems

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/ulikunitz/xz"

	"pdp11emu/server/internal/api"
	"pdp11emu/server/internal/catalog"
)

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// diskImage returns deterministic test data of n bytes with long runs of
// zero blocks (so sparse writes leave holes).
func diskImage(seed int64, n int) []byte {
	b := make([]byte, n)
	rng := rand.New(rand.NewSource(seed))
	for off := 0; off < n; off += blockSize {
		if (off/blockSize)%5 != 0 { // 4 of 5 blocks stay zero
			continue
		}
		end := min(off+blockSize, n)
		rng.Read(b[off:end])
	}
	return b
}

type file struct {
	name string
	data []byte
}

func buildArchive(t *testing.T, format string, files []file) []byte {
	t.Helper()
	var buf bytes.Buffer
	switch format {
	case "raw":
		buf.Write(files[0].data)
	case "gz":
		w := gzip.NewWriter(&buf)
		w.Write(files[0].data)
		w.Close()
	case "xz":
		w, err := xz.NewWriter(&buf)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(files[0].data)
		w.Close()
	case "zip":
		w := zip.NewWriter(&buf)
		w.Create("README.txt")
		for _, f := range files {
			fw, _ := w.Create(f.name)
			fw.Write(f.data)
		}
		w.Close()
	case "tar", "tgz", "tarZ":
		var tbuf bytes.Buffer
		tw := tar.NewWriter(&tbuf)
		tw.WriteHeader(&tar.Header{Name: "./Disks/", Typeflag: tar.TypeDir, Mode: 0o755})
		tw.WriteHeader(&tar.Header{Name: "./README", Size: 5, Mode: 0o644})
		tw.Write([]byte("hello"))
		for _, f := range files {
			tw.WriteHeader(&tar.Header{Name: "./" + f.name, Size: int64(len(f.data)), Mode: 0o644, Typeflag: tar.TypeReg})
			tw.Write(f.data)
		}
		tw.Close()
		if format == "tar" {
			return tbuf.Bytes()
		}
		// "gzip -dc" decodes gzip as well as compress(1) data, so a gzip'd
		// tar exercises the tarZ pipeline (TestRealTarZ uses a real .Z).
		w := gzip.NewWriter(&buf)
		w.Write(tbuf.Bytes())
		w.Close()
	default:
		t.Fatalf("format %s", format)
	}
	return buf.Bytes()
}

// fixture is a test catalog plus an HTTP server holding its archives.
type fixture struct {
	t       *testing.T
	cat     *catalog.Catalog
	srv     *httptest.Server
	mu      sync.Mutex
	content map[string][]byte // URL path -> body
	handler map[string]http.HandlerFunc
	hits    map[string]int
	ranges  []string // Range headers seen
	files   map[string][]file
	data    string // DataDir
}

type sysSpec struct {
	id       string
	format   string
	class    string
	speed    string
	sparse   bool
	files    []file
	extra    map[string]any // more system.json fields
	bootINI  string
	dialog   string
	mirrors  []string
	urlPath  string // default "/<id>.<format>"
	terminal map[string]any
}

func defaultSpecs() []sysSpec {
	return []sysSpec{
		{id: "bsd", format: "xz", class: "redistributable", sparse: true,
			files:    []file{{"bsd.dsk", diskImage(1, 300*1024)}},
			terminal: map[string]any{"device": "dz", "lines": 8, "modem": true},
			bootINI:  "; 2.11BSD-like\r\nset rq enabled\nset rq0 rauser=1000\nattach -e rq0 ${DISK0}\nset dli enabled\nset dz lines=8\n",
			dialog:   "; skip the countdown\nexpect -p \"Press <CR> to boot\" send \"\\r\"; continue\n",
			extra: map[string]any{"default": true, "bootTimeoutSec": 90, "shutdownScript": []map[string]any{
				{"send": "shutdown -h now\r"}, {"expect": "halting", "timeoutSec": 60}}}},
		{id: "v6", format: "zip", class: "redistributable",
			files:   []file{{"unix0_v6_rk.dsk", diskImage(2, 64*1024)}, {"unix1_v6_rk.dsk", diskImage(3, 48*1024)}},
			bootINI: "set rk enabled\nattach -e rk0 ${DISK0}\nattach -e rk1 ${DISK1}\n",
			dialog:  "expect \"@\" send delay=20000,\"rkunix\\r\"; continue\n"},
		{id: "rt11", format: "tarZ", class: "hobbyist", speed: "authentic", extra: map[string]any{"noLogin": true},
			files:   []file{{"Disks/rtv53_rl.dsk", diskImage(4, 40*1024)}},
			bootINI: "set rl enabled\nattach -e rl0 ${DISK0}\n"},
		{id: "raw", format: "raw", class: "redistributable", files: []file{{"disk.img", diskImage(5, 20*1024)}},
			bootINI: "attach -e rk0 ${DISK0}\n"},
		{id: "gz", format: "gz", class: "redistributable", sparse: true, files: []file{{"gz.dsk", diskImage(6, 100*1024)}},
			bootINI: "attach -e rp0 ${DISK0}\n"},
		{id: "tar", format: "tar", class: "redistributable", files: []file{{"Disks/a.dsk", diskImage(7, 30*1024)}, {"Disks/b.dsk", diskImage(8, 10*1024)}},
			bootINI: "attach -e rk0 ${DISK0}\nattach -e rk1 ${DISK1}\n"},
		{id: "tgz", format: "tgz", class: "redistributable", files: []file{{"x/tgz.dsk", diskImage(9, 25*1024)}},
			bootINI: "attach -e hk0 ${DISK0}\n"},
	}
}

func newFixture(t *testing.T, specs []sysSpec) *fixture {
	t.Helper()
	f := &fixture{t: t, content: map[string][]byte{}, handler: map[string]http.HandlerFunc{}, hits: map[string]int{}, files: map[string][]file{}}
	// TLS: the catalog requires an https source for the default system.
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	fsys := fstest.MapFS{}
	hasDefault := false
	for _, sp := range specs {
		hasDefault = hasDefault || sp.extra["default"] == true
	}
	for i, sp := range specs {
		if !hasDefault && i == 0 {
			sp.extra = map[string]any{"default": true}
		}
		archive := buildArchive(t, sp.format, sp.files)
		p := sp.urlPath
		if p == "" {
			p = "/" + sp.id + "." + sp.format
		}
		f.content[p] = archive
		f.files[sp.id] = sp.files
		var afiles []map[string]any
		for i, fl := range sp.files {
			afiles = append(afiles, map[string]any{"path": fl.name, "role": fmt.Sprintf("DISK%d", i), "sha256": sha(fl.data), "size": len(fl.data), "sparse": sp.sparse})
		}
		mirrors := []string{}
		for _, m := range sp.mirrors {
			mirrors = append(mirrors, f.srv.URL+m)
		}
		speed := sp.speed
		if speed == "" {
			speed = "fast"
		}
		sys := map[string]any{
			"id": sp.id, "name": "System " + sp.id, "short": sp.id, "category": "unix", "order": len(fsys),
			"description": "test system", "license": map[string]any{"name": "Test licence", "class": sp.class, "summary": "test", "file": "LICENSE"},
			"credentials": []map[string]any{{"user": "root", "password": ""}}, "shutdown": "type shutdown",
			"notes": "## Log in\n## Things to try\n## Shut down\n", "cpu": map[string]any{"memory": "1M"},
			"speed": speed, "boot": "boot rk0", "readyPattern": "login: ",
			"console":   map[string]any{"backspace": "del", "interrupt": "ctrl-c"},
			"artifacts": []map[string]any{{"url": f.srv.URL + p, "mirrors": mirrors, "sha256": sha(archive), "size": len(archive), "format": sp.format, "files": afiles}},
		}
		if sp.terminal != nil {
			sys["terminals"] = sp.terminal
		}
		for k, v := range sp.extra {
			sys[k] = v
		}
		raw, err := json.MarshalIndent(sys, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		fsys[sp.id+"/system.json"] = &fstest.MapFile{Data: raw}
		fsys[sp.id+"/boot.ini"] = &fstest.MapFile{Data: []byte(sp.bootINI)}
		fsys[sp.id+"/LICENSE"] = &fstest.MapFile{Data: []byte("Licence text of " + sp.id)}
		if sp.dialog != "" {
			fsys[sp.id+"/dialog.ini"] = &fstest.MapFile{Data: []byte(sp.dialog)}
		}
	}
	cat, err := catalog.Load(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if errs := cat.Validate(); len(errs) > 0 {
		t.Fatalf("test catalog invalid: %v", errs)
	}
	f.cat = cat
	f.data = t.TempDir()
	return f
}

func (f *fixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.hits[r.URL.Path]++
	if rg := r.Header.Get("Range"); rg != "" {
		f.ranges = append(f.ranges, r.URL.Path+" "+rg)
	}
	h := f.handler[r.URL.Path]
	body, ok := f.content[r.URL.Path]
	f.mu.Unlock()
	if h != nil {
		h(w, r)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(body))
}

func (f *fixture) archive(id string) (catalog.Artifact, []byte) {
	s, _ := f.cat.Get(id)
	a := s.Artifacts[0]
	return a, f.content[strings.TrimPrefix(a.URL, f.srv.URL)]
}

func (f *fixture) setHandler(path string, h http.HandlerFunc) {
	f.mu.Lock()
	f.handler[path] = h
	f.mu.Unlock()
}

func (f *fixture) hitCount(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[path]
}

func testLog(t *testing.T) *slog.Logger {
	if testing.Verbose() {
		return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// manager creates a Manager over the fixture with fast retries.
func (f *fixture) manager(opts ...func(*Options)) *Manager {
	f.t.Helper()
	o := Options{DataDir: f.data, Logger: testLog(f.t)}
	for _, fn := range opts {
		fn(&o)
	}
	m, err := New(f.cat, o)
	if err != nil {
		f.t.Fatal(err)
	}
	m.dl.backoff = []time.Duration{10 * time.Millisecond, 20 * time.Millisecond}
	tr := m.dl.client.Transport.(*http.Transport).Clone()
	tr.TLSClientConfig = f.srv.Client().Transport.(*http.Transport).TLSClientConfig
	m.dl.client = &http.Client{Transport: tr}
	f.t.Cleanup(func() { m.Close() })
	return m
}

// progressLog records progress events.
type progressLog struct {
	mu sync.Mutex
	ev []api.Progress
}

func (p *progressLog) add(ev api.Progress) {
	p.mu.Lock()
	p.ev = append(p.ev, ev)
	p.mu.Unlock()
}

func (p *progressLog) phases(id string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, e := range p.ev {
		if e.ID == id && (len(out) == 0 || out[len(out)-1] != e.Phase) {
			out = append(out, e.Phase)
		}
	}
	return out
}

func (p *progressLog) last(id string) api.Progress {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := len(p.ev) - 1; i >= 0; i-- {
		if p.ev[i].ID == id {
			return p.ev[i]
		}
	}
	return api.Progress{}
}
