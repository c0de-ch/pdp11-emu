package systems

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"pdp11emu/server/internal/api"
)

// spaceError: the disks do not fit on the data volume.
type spaceError struct {
	dir        string
	need, free int64
}

func (e *spaceError) Error() string {
	return fmt.Sprintf("not enough free space in %s: %s needed, %s available", e.dir, human(e.need), human(e.free))
}

// wrongFileError: a file exists, but it is not the one the catalog expects.
type wrongFileError struct{ msg string }

func (e *wrongFileError) Error() string { return e.msg }

// reportedError is a task failure that its "error" progress already told
// every client about (api.ErrReported).
type reportedError struct{ error }

func (e reportedError) Unwrap() error { return e.error }

func (e reportedError) Is(target error) bool { return target == api.ErrReported }

// describeFailure says what went wrong with a task and what to do next, for
// the person watching; Detail keeps the error itself.
func (m *Manager) describeFailure(kind string, err error) *api.TaskFailure {
	f := &api.TaskFailure{Task: kind, Detail: err.Error()}
	var dl *downloadError
	var sp *spaceError
	switch {
	case errors.As(err, &dl):
		f.Summary, f.Hint = dl.explain(m.importDir())
	case errors.As(err, &sp):
		f.Summary = fmt.Sprintf("There is not enough free disk space: %s needed, %s available.", sizeText(sp.need), sizeText(sp.free))
		f.Hint = fmt.Sprintf("Free some space in `%s` and try again.", sp.dir)
	default:
		f.Summary = "The disk images could not be unpacked or written."
		f.Hint = "Try again. If it fails again, the technical details say what went wrong."
	}
	return f
}

// explain turns the last attempt of a failed download into the reason and
// the two ways forward: try again, or fetch the file by hand.
func (e *downloadError) explain(importDir string) (summary, hint string) {
	name := archiveName(e.archive)
	byHand := fmt.Sprintf("download `%s` (%s) yourself from %s, put it in `%s` and try again: pdp11d looks there first.", name, sizeText(e.archive.Size), mdLink(e.archive.URL), importDir)
	last := error(errors.New("no source"))
	if n := len(e.attempts); n > 0 {
		last = e.attempts[n-1].err
	}
	var wrong *wrongFileError
	var perm permanentError
	switch {
	case errors.As(last, &wrong):
		return fmt.Sprintf("The download server has a different file than the one this version expects (%s).", name),
			"It may have been replaced there. If you have the original, " + byHand
	case errors.As(last, &perm):
		brief := briefError(last)
		if strings.Contains(brief, "HTTP 404") || strings.Contains(brief, "HTTP 410") {
			return fmt.Sprintf("The download server does not have %s any more (%s).", name, brief),
				"If you have a copy, " + byHand
		}
		return fmt.Sprintf("The download server cannot provide %s (%s).", name, brief),
			"If you have a copy, " + byHand
	}
	retry := "Check that this machine can reach the internet and try again. Or " + byHand
	var ue *url.Error
	var oe *net.OpError
	if errors.As(last, &ue) || errors.As(last, &oe) {
		return fmt.Sprintf("The download server could not be reached (%s).", briefError(last)), retry
	}
	return fmt.Sprintf("The download of %s stopped (%s).", name, briefError(last)), retry
}

// sizeText writes a size like the UI's download and disk sizes (decimal units).
func sizeText(n int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	v, i := float64(n), 0
	for v >= 1000 && i < len(units)-1 {
		v /= 1000
		i++
	}
	if v >= 100 || i == 0 {
		return fmt.Sprintf("%.0f %s", v, units[i])
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

// mdLink is a URL as an inline markdown link.
func mdLink(u string) string { return fmt.Sprintf("[%s](%s)", u, u) }
