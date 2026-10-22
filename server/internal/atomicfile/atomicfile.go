// Package atomicfile replaces small files (state, configuration, pid
// files) atomically: the new content goes to a temporary file in the same
// directory, which gets its mode, is synced and renamed over the target;
// then the directory is synced. A crash leaves either the old file or the
// new one, and a failed write (a full disk) is reported and leaves the old
// file as it was.
package atomicfile

import (
	"io/fs"
	"os"
	"path/filepath"
)

// WriteFile writes data to path with mode perm (set exactly, not reduced by
// the umask).
func WriteFile(path string, data []byte, perm fs.FileMode) (err error) {
	dir, base := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	f, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()
	if _, err = f.Write(data); err == nil {
		err = f.Chmod(perm)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	if d, derr := os.Open(dir); derr == nil {
		_ = d.Sync() // best effort: some file systems do not support it
		_ = d.Close()
	}
	return nil
}
