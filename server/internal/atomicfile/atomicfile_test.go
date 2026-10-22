package atomicfile

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// limitFileSize makes writes beyond n bytes fail (EFBIG, as ENOSPC would
// on a full disk) until the test ends. Go ignores SIGXFSZ.
func limitFileSize(t *testing.T, n uint64) {
	t.Helper()
	var old syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &old); err != nil {
		t.Fatal(err)
	}
	lim := syscall.Rlimit{Cur: n, Max: old.Max}
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &lim); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { syscall.Setrlimit(syscall.RLIMIT_FSIZE, &old) })
}

func onlyFile(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("directory holds %v (%v), want only the target", entries, err)
	}
}

func TestWriteFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.json")
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	for _, data := range [][]byte{[]byte("first\n"), []byte("second, longer\n")} {
		if err := WriteFile(p, data, 0o640); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(p)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("content %q (%v), want %q", got, err, data)
		}
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o640 {
			t.Fatalf("%v %v, want mode 0640 whatever the umask", fi, err)
		}
		onlyFile(t, dir)
	}
	if err := WriteFile(filepath.Join(dir, "missing", "x"), nil, 0o640); err == nil {
		t.Fatal("no error for a missing directory")
	}
}

// TestWriteFileFailure: a write that fails (a full disk) is reported, and
// the old file stays as it was.
func TestWriteFileFailure(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "dialog.do")
	if err := WriteFile(p, []byte("old content\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	limitFileSize(t, 0)
	if err := WriteFile(p, []byte("noexpect\nnosend\n"), 0o640); err == nil {
		t.Fatal("a failed write was not reported")
	}
	if got, _ := os.ReadFile(p); string(got) != "old content\n" {
		t.Fatalf("target now holds %q", got)
	}
	onlyFile(t, dir)
}
