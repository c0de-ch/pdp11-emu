package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testCatalog writes a two-system catalog: "dflt" (default, never
// downloaded) and "hob" (hobbyist, served by srv over plain HTTP).
func testCatalog(t *testing.T, srv *httptest.Server, archive, disk []byte) string {
	t.Helper()
	dir := t.TempDir()
	sum := func(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
	write := func(id string, def bool, class, url string, archive []byte) {
		os.MkdirAll(filepath.Join(dir, id), 0o755)
		sys := map[string]any{
			"id": id, "name": "System " + id, "short": id, "category": "dec", "order": 1, "default": def,
			"description": "d", "license": map[string]any{"name": "L", "class": class, "summary": "s", "file": "LICENSE"},
			"noLogin": true, "shutdown": "s", "notes": "## Log in\n## Things to try\n## Shut down\n",
			"cpu": map[string]any{"memory": "1M"}, "speed": "fast", "boot": "boot rk0", "readyPattern": ".",
			"console": map[string]any{"backspace": "del", "interrupt": "ctrl-c"},
			"artifacts": []map[string]any{{"url": url, "sha256": sum(archive), "size": len(archive), "format": "zip",
				"files": []map[string]any{{"path": "disk.dsk", "role": "DISK0", "sha256": sum(disk), "size": len(disk)}}}},
		}
		raw, _ := json.Marshal(sys)
		os.WriteFile(filepath.Join(dir, id, "system.json"), raw, 0o644)
		os.WriteFile(filepath.Join(dir, id, "boot.ini"), []byte("attach -e rk0 ${DISK0}\n"), 0o644)
		os.WriteFile(filepath.Join(dir, id, "LICENSE"), []byte("THE "+id+" LICENCE TEXT"), 0o644)
	}
	write("dflt", true, "redistributable", "https://example.invalid/x.zip", []byte("another archive"))
	write("hob", false, "hobbyist", srv.URL+"/hob.zip", archive)
	return dir
}

func TestInstallAndFetchCommands(t *testing.T) {
	disk := bytes.Repeat([]byte("PDP-11"), 1000)
	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	w, _ := zw.Create("disk.dsk")
	w.Write(disk)
	zw.Close()
	archive := zbuf.Bytes()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hob.zip" {
			http.NotFound(w, r)
			return
		}
		w.Write(archive)
	}))
	defer srv.Close()
	cat := testCatalog(t, srv, archive, disk)
	data := t.TempDir()
	env := envMap(map[string]string{"PDP11_SYSTEMS": cat, "PDP11_DATA": data, "PDP11_CACHE": ""})

	var out, errb bytes.Buffer
	if code := run([]string{"install", "hob"}, env, &out, &errb); code != 1 || !strings.Contains(errb.String(), "THE hob LICENCE TEXT") {
		t.Fatalf("install without acceptance: %d\n%s", code, errb.String())
	}
	errb.Reset()
	if code := run([]string{"install", "--accept-license", "hob"}, env, &out, &errb); code != 0 {
		t.Fatalf("install: %d\n%s", code, errb.String())
	}
	if got, _ := os.ReadFile(filepath.Join(data, "systems", "hob", "disk.dsk")); !bytes.Equal(got, disk) {
		t.Fatal("not installed")
	}
	if !strings.Contains(out.String(), "hob: done") {
		t.Fatalf("progress output %q", out.String())
	}

	cache := t.TempDir()
	errb.Reset()
	env2 := envMap(map[string]string{"PDP11_SYSTEMS": cat, "PDP11_DATA": t.TempDir(), "PDP11_ACCEPT_LICENSES": "hob"})
	if code := run([]string{"fetch", "--cache", cache, "hob"}, env2, &out, &errb); code != 0 {
		t.Fatalf("fetch: %d\n%s", code, errb.String())
	}
	s := sha256.Sum256(archive)
	if _, err := os.Stat(filepath.Join(cache, hex.EncodeToString(s[:]))); err != nil {
		t.Fatal(err)
	}
	// No ids: every redistributable system (here only dflt, whose archive
	// is already in this cache, so nothing is downloaded).
	cache2 := t.TempDir()
	other := []byte("another archive")
	o := sha256.Sum256(other)
	os.WriteFile(filepath.Join(cache2, hex.EncodeToString(o[:])), other, 0o644)
	errb.Reset()
	if code := run([]string{"fetch", "--cache", cache2}, env2, &out, &errb); code != 0 {
		t.Fatalf("fetch of the redistributable systems: %d\n%s", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(cache2, hex.EncodeToString(s[:]))); err == nil {
		t.Fatal("the hobbyist system was fetched without being named")
	}
}

func TestHealthCommand(t *testing.T) {
	for in, want := range map[string]string{
		":8080":          "http://127.0.0.1:8080/api/status",
		"0.0.0.0:80":     "http://127.0.0.1:80/api/status",
		"[::]:8080":      "http://[::1]:8080/api/status",
		"10.0.0.5:9000":  "http://10.0.0.5:9000/api/status",
		"localhost:8080": "http://localhost:8080/api/status",
	} {
		if got, err := statusURL(in); err != nil || got != want {
			t.Errorf("statusURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := statusURL("8080"); err == nil {
		t.Error("statusURL accepted a bare port")
	}

	var gotPass string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, gotPass, _ = r.BasicAuth()
		switch r.URL.Path {
		case "/api/status":
			w.Write([]byte(`{"ok":true,"version":"9.9","run":"running","system":"bsd211","power":"on"}`))
		case "/bad/api/status":
			w.Write([]byte(`{"ok":false}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	env := envMap(map[string]string{"PDP11_LISTEN": strings.TrimPrefix(srv.URL, "http://"), "PDP11_PASSWORD": "s3cret"})
	var out, errb bytes.Buffer
	if code := run([]string{"health"}, env, &out, &errb); code != 0 || !strings.Contains(out.String(), "run=running") || gotPass != "s3cret" {
		t.Fatalf("health: %d %q %q (password %q)", code, out.String(), errb.String(), gotPass)
	}
	for _, u := range []string{srv.URL + "/bad/api/status", srv.URL + "/missing"} {
		if code := run([]string{"health", "--url", u}, env, &out, &errb); code != 1 {
			t.Errorf("health --url %s: exit %d, want 1", u, code)
		}
	}
	srv.Close()
	if code := run([]string{"health"}, env, &out, &errb); code != 1 {
		t.Errorf("health with nothing listening: exit %d, want 1", code)
	}
}
