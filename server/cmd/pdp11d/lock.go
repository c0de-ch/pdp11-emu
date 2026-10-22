package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// lockName is the file in the data directory that serve and install lock:
// one data directory serves one pdp11d (DESIGN.md §5).
const lockName = ".pdp11d.lock"

// busyError reports a data directory that another pdp11d holds.
type busyError struct {
	pid string // as the holder recorded it, or "unknown"
	dir string
}

func (e *busyError) Error() string {
	return fmt.Sprintf("another pdp11d (pid %s) is using the data directory %s; one data directory serves one instance", e.pid, e.dir)
}

// lockDataDir creates the data directory if needed, takes its lock
// (flock, exclusive, not waiting: a *busyError if another pdp11d holds
// it) and records our pid in the lock file. The lock lasts until the file
// is closed or the process ends; the descriptor is close-on-exec, so the
// simulator never holds it.
func lockDataDir(dir string) (*os.File, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, lockName)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, &busyError{pid: lockHolder(path), dir: dir}
		}
		return nil, fmt.Errorf("cannot lock %s: %w", path, err)
	}
	// The pid only informs a second instance; the lock is what counts.
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}
	return f, nil
}

// lockHolder returns the pid recorded in the lock file, or "unknown".
func lockHolder(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "unknown"
	}
	if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
		return strconv.Itoa(pid)
	}
	return "unknown"
}
