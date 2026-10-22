package systems

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ulikunitz/xz"

	"pdp11emu/server/internal/catalog"
)

// blockSize is the unit of sparse writes: all-zero blocks become holes.
const blockSize = 4096

// extract writes the listed files of archive (format a.Format) to
// target(file), verifying size and sha256 of each (a file that fails is
// removed). progress receives the bytes written so far for this artifact.
func extract(ctx context.Context, a catalog.Artifact, archive string, target func(catalog.ArtifactFile) string, progress func(int64)) error {
	var base int64
	put := func(f catalog.ArtifactFile, r io.Reader) error {
		err := writeVerified(ctx, target(f), r, f, func(n int64) { progress(base + n) })
		base += f.Size
		return err
	}
	switch a.Format {
	case "raw", "gz", "xz":
		if len(a.Files) != 1 {
			return fmt.Errorf("a %s artifact holds exactly one file, the catalog lists %d", a.Format, len(a.Files))
		}
		in, err := os.Open(archive)
		if err != nil {
			return err
		}
		defer in.Close()
		var r io.Reader = bufio.NewReaderSize(in, 1<<20)
		switch a.Format {
		case "gz":
			zr, err := gzip.NewReader(r)
			if err != nil {
				return fmt.Errorf("%s: %w", archive, err)
			}
			defer zr.Close()
			r = zr
		case "xz":
			xr, err := xz.NewReader(r)
			if err != nil {
				return fmt.Errorf("%s: %w", archive, err)
			}
			r = xr
		}
		return put(a.Files[0], r)
	case "zip":
		return extractZip(archive, a.Files, put)
	case "tar", "tgz", "tarZ":
		return extractTar(ctx, a.Format, archive, a.Files, put)
	}
	return fmt.Errorf("unknown archive format %q", a.Format)
}

func extractZip(archive string, files []catalog.ArtifactFile, put func(catalog.ArtifactFile, io.Reader) error) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return fmt.Errorf("%s: %w", archive, err)
	}
	defer zr.Close()
	byName := map[string]*zip.File{}
	for _, f := range zr.File {
		byName[cleanArchivePath(f.Name)] = f
	}
	for _, f := range files {
		zf, ok := byName[cleanArchivePath(f.Path)]
		if !ok || zf.FileInfo().IsDir() {
			return fmt.Errorf("%s: %s not found in the archive", archive, f.Path)
		}
		rc, err := zf.Open()
		if err != nil {
			return fmt.Errorf("%s: %s: %w", archive, f.Path, err)
		}
		err = put(f, rc)
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func extractTar(ctx context.Context, format, archive string, files []catalog.ArtifactFile, put func(catalog.ArtifactFile, io.Reader) error) error {
	in, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer in.Close()
	var r io.Reader = bufio.NewReaderSize(in, 1<<20)
	var stderr bytes.Buffer
	switch format {
	case "tgz":
		zr, err := gzip.NewReader(r)
		if err != nil {
			return fmt.Errorf("%s: %w", archive, err)
		}
		defer zr.Close()
		r = zr
	case "tarZ":
		// compress(1) data: gzip decodes it ("gzip -dc").
		cmd := exec.CommandContext(ctx, "gzip", "-dc")
		cmd.Stdin = r
		cmd.Stderr = &stderr
		out, err := cmd.StdoutPipe()
		if err != nil {
			return err
		}
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("gzip: %w", err)
		}
		// Closing the pipe stops gzip when the rest is not needed; its exit
		// status does not matter (every file is verified by sha256).
		defer func() {
			out.Close()
			_ = cmd.Wait()
		}()
		r = out
	}
	want := map[string]catalog.ArtifactFile{}
	for _, f := range files {
		want[cleanArchivePath(f.Path)] = f
	}
	tr := tar.NewReader(r)
	for len(want) > 0 {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			if msg := strings.TrimSpace(stderr.String()); msg != "" {
				err = fmt.Errorf("%w (gzip: %s)", err, msg)
			}
			return fmt.Errorf("%s: %w", archive, err)
		}
		name := cleanArchivePath(h.Name)
		f, ok := want[name]
		if !ok {
			continue
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
			return fmt.Errorf("%s: %s is not a regular file in the archive", archive, f.Path)
		}
		if err := put(f, tr); err != nil {
			return err
		}
		delete(want, name)
	}
	for _, f := range want {
		return fmt.Errorf("%s: %s not found in the archive", archive, f.Path)
	}
	return nil
}

// writeVerified writes r to dst (sparse if f.Sparse) and checks size and
// sha256; dst is removed if it fails.
func writeVerified(ctx context.Context, dst string, r io.Reader, f catalog.ArtifactFile, progress func(int64)) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	sum, err := writeFile(ctx, dst, r, f.Size, f.Sparse, progress)
	if err == nil && !strings.EqualFold(sum, f.SHA256) {
		err = fmt.Errorf("%s: sha256 %s, want %s (%d bytes)", f.Path, sum, f.SHA256, f.Size)
	}
	if err != nil {
		os.Remove(dst)
		return err
	}
	return nil
}

// writeFile copies exactly size bytes from r into a new file and returns
// their sha256. With sparse, all-zero blocks are skipped (holes).
func writeFile(ctx context.Context, path string, r io.Reader, size int64, sparse bool, progress func(int64)) (string, error) {
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o640)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	buf := make([]byte, 256*blockSize)
	var off int64
	werr := func() error {
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			n, rerr := io.ReadFull(r, buf)
			if n > 0 {
				if off+int64(n) > size {
					return fmt.Errorf("%s: more than the expected %d bytes", filepath.Base(path), size)
				}
				h.Write(buf[:n])
				if err := writeBlocks(out, buf[:n], off, sparse); err != nil {
					return err
				}
				off += int64(n)
				progress(off)
			}
			if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
				return nil
			}
			if rerr != nil {
				return fmt.Errorf("%s: %w", filepath.Base(path), rerr)
			}
		}
	}()
	if werr == nil && off != size {
		werr = fmt.Errorf("%s: %d bytes, want %d", filepath.Base(path), off, size)
	}
	if werr == nil {
		werr = out.Truncate(size) // sets the length after a trailing hole
	}
	if werr == nil {
		werr = out.Sync()
	}
	if cerr := out.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return "", werr
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

var zeroBlock [blockSize]byte

// writeBlocks writes p at off; with sparse, runs of all-zero blocks are
// left as holes (the file is freshly truncated, so holes read as zeros).
func writeBlocks(out *os.File, p []byte, off int64, sparse bool) error {
	if !sparse {
		_, err := out.WriteAt(p, off)
		return err
	}
	start := -1 // start of the pending run of data blocks
	flush := func(end int) error {
		if start < 0 {
			return nil
		}
		_, err := out.WriteAt(p[start:end], off+int64(start))
		start = -1
		return err
	}
	for i := 0; i < len(p); i += blockSize {
		end := min(i+blockSize, len(p))
		if bytes.Equal(p[i:end], zeroBlock[:end-i]) {
			if err := flush(i); err != nil {
				return err
			}
			continue
		}
		if start < 0 {
			start = i
		}
	}
	return flush(len(p))
}
