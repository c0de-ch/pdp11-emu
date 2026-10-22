package systems

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"pdp11emu/server/internal/catalog"
)

// downloader fetches archives over HTTP(S) (net/http, Range resume) and
// FTP (the built-in client in ftp.go, REST resume), into
// <dir>/<sha256>.part, renamed to <dir>/<sha256> once complete. The caller
// verifies the result.
type downloader struct {
	log     *slog.Logger
	client  *http.Client
	backoff []time.Duration // between the attempts on one URL
	connect time.Duration   // connection timeout
	stall   time.Duration   // abort when no data arrives for this long
}

const attemptsPerURL = 3

func newDownloader(log *slog.Logger) downloader {
	d := downloader{
		log:     log,
		backoff: []time.Duration{2 * time.Second, 8 * time.Second},
		connect: 20 * time.Second,
		stall:   60 * time.Second,
	}
	d.client = &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: d.connect, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   d.connect,
		ResponseHeaderTimeout: d.stall,
		// The archives are compressed already; transparent decompression
		// of a .gz served with Content-Encoding: gzip would corrupt them.
		DisableCompression: true,
		ForceAttemptHTTP2:  true,
	}}
	return d
}

// permanentError marks a failure that retrying the same URL cannot fix.
type permanentError struct{ error }

func (e permanentError) Unwrap() error { return e.error }

// download tries the URL and then the mirrors, attemptsPerURL times each.
func (d downloader) download(ctx context.Context, a catalog.Artifact, dir string, progress func(int64)) error {
	part := filepath.Join(dir, strings.ToLower(a.SHA256)+".part")
	final := filepath.Join(dir, strings.ToLower(a.SHA256))
	var tried []string
	for _, u := range append([]string{a.URL}, a.Mirrors...) {
		for attempt := 1; attempt <= attemptsPerURL; attempt++ {
			if attempt > 1 {
				wait := d.backoff[min(attempt-2, len(d.backoff)-1)]
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(wait):
				}
			}
			err := d.fetch(ctx, u, part, a.Size, progress)
			if err == nil {
				if err = verifyFile(ctx, part, a.Size, a.SHA256, nil); err == nil {
					return os.Rename(part, final)
				}
				os.Remove(part) // complete but wrong: never resume from it
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			d.log.Warn("download failed", "url", u, "attempt", attempt, "err", err)
			tried = append(tried, fmt.Sprintf("%s (attempt %d: %v)", u, attempt, err))
			var perm permanentError
			if errors.As(err, &perm) {
				break
			}
		}
	}
	return fmt.Errorf("cannot download %s (%d bytes, sha256 %s); tried %s", archiveName(a), a.Size, a.SHA256, strings.Join(tried, "; "))
}

func (d downloader) fetch(ctx context.Context, u, part string, size int64, progress func(int64)) error {
	have := int64(0)
	if fi, err := os.Stat(part); err == nil {
		have = fi.Size()
	}
	if have > size {
		os.Remove(part)
		have = 0
	}
	if have == size {
		return nil // complete; the caller verifies it
	}
	switch {
	case strings.HasPrefix(u, "http://"), strings.HasPrefix(u, "https://"):
		return d.fetchHTTP(ctx, u, part, have, size, progress)
	case strings.HasPrefix(u, "ftp://"):
		return d.fetchFTP(ctx, u, part, have, size, progress)
	}
	return permanentError{fmt.Errorf("unsupported URL scheme")}
}

func (d downloader) fetchHTTP(ctx context.Context, u, part string, have, size int64, progress func(int64)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return permanentError{err}
	}
	req.Header.Set("User-Agent", "pdp11d")
	if have > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	flags := os.O_WRONLY | os.O_CREATE
	switch {
	case resp.StatusCode == http.StatusPartialContent && have > 0:
		if start, ok := rangeStart(resp.Header.Get("Content-Range")); !ok || start != have {
			return fmt.Errorf("server resumed at %q, wanted byte %d", resp.Header.Get("Content-Range"), have)
		}
		flags |= os.O_APPEND
	case resp.StatusCode == http.StatusOK:
		have = 0 // no range support: start over
		flags |= os.O_TRUNC
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		os.Remove(part)
		return fmt.Errorf("HTTP %s (starting over)", resp.Status)
	case resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusRequestTimeout && resp.StatusCode != http.StatusTooManyRequests:
		return permanentError{fmt.Errorf("HTTP %s", resp.Status)}
	default:
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	f, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return err
	}
	// A watchdog cancels the request when no data arrives for d.stall.
	stall := time.AfterFunc(d.stall, cancel)
	defer stall.Stop()
	buf := make([]byte, 256<<10)
	n := have
	var werr error
	for {
		k, rerr := resp.Body.Read(buf)
		if k > 0 {
			stall.Reset(d.stall)
			if n+int64(k) > size {
				werr = permanentError{fmt.Errorf("server sent more than the expected %d bytes", size)}
				break
			}
			if _, werr = f.Write(buf[:k]); werr != nil {
				break
			}
			n += int64(k)
			progress(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			werr = rerr
			if ctx.Err() != nil && d.stalled(stall) {
				werr = fmt.Errorf("no data for %s", d.stall)
			}
			break
		}
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil && n != size {
		werr = fmt.Errorf("connection closed after %d of %d bytes", n, size)
	}
	return werr
}

// stalled reports whether the stall watchdog fired (as opposed to the
// caller cancelling).
func (d downloader) stalled(t *time.Timer) bool { return !t.Stop() }

// rangeStart parses "bytes <start>-<end>/<size>".
func rangeStart(cr string) (int64, bool) {
	rest, ok := strings.CutPrefix(cr, "bytes ")
	if !ok {
		return 0, false
	}
	start, _, ok := strings.Cut(rest, "-")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(start), 10, 64)
	return n, err == nil
}
