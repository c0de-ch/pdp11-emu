package systems

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeFTP is a small in-process FTP server (passive mode only) with
// switchable misbehaviour.
type fakeFTP struct {
	t     *testing.T
	ln    net.Listener
	files map[string][]byte

	mu    sync.Mutex
	b     ftpBehaviour
	cmds  []string
	retrs int
	done  chan struct{}
}

type ftpBehaviour struct {
	banner     []string // greeting lines; more than one makes a multi-line reply
	noEPSV     bool     // answer EPSV with 500 (the client must fall back to PASV)
	noREST     bool     // answer REST with 502
	noSIZE     bool     // answer SIZE with 502
	dropAfter  int64    // first RETR: close the data connection after this many bytes (426)
	stallAfter int64    // first RETR: stop sending after this many bytes
}

// behave changes the behaviour for the following connections.
func (s *fakeFTP) behave(fn func(b *ftpBehaviour)) {
	s.mu.Lock()
	fn(&s.b)
	s.mu.Unlock()
}

func newFakeFTP(t *testing.T, files map[string][]byte) *fakeFTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeFTP{t: t, ln: ln, files: files, b: ftpBehaviour{banner: []string{"pdp11 test FTP server"}}, done: make(chan struct{})}
	t.Cleanup(func() { close(s.done); ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.session(c)
		}
	}()
	return s
}

func (s *fakeFTP) url(path string) string { return "ftp://" + s.ln.Addr().String() + path }

func (s *fakeFTP) commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.cmds...)
}

func (s *fakeFTP) session(c net.Conn) {
	defer c.Close()
	s.mu.Lock()
	b := s.b
	s.mu.Unlock()
	w := bufio.NewWriter(c)
	reply := func(format string, a ...any) {
		fmt.Fprintf(w, format+"\r\n", a...)
		w.Flush()
	}
	if len(b.banner) == 1 {
		reply("220 %s", b.banner[0])
	} else {
		reply("220-%s", b.banner[0])
		for _, l := range b.banner[1 : len(b.banner)-1] {
			reply("%s", l)
		}
		reply("220 %s", b.banner[len(b.banner)-1])
	}
	r := bufio.NewReader(c)
	var dataLn net.Listener
	var rest int64
	defer func() {
		if dataLn != nil {
			dataLn.Close()
		}
	}()
	listen := func() int {
		if dataLn != nil {
			dataLn.Close()
		}
		var err error
		if dataLn, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			s.t.Error(err)
			return 0
		}
		return dataLn.Addr().(*net.TCPAddr).Port
	}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		s.mu.Lock()
		s.cmds = append(s.cmds, line)
		s.mu.Unlock()
		verb, arg, _ := strings.Cut(line, " ")
		switch verb {
		case "USER":
			if arg != "anonymous" {
				reply("530 Login incorrect.")
			} else {
				reply("331 Please specify the password.")
			}
		case "PASS":
			reply("230-Welcome.\r\n230 Login successful.")
		case "TYPE":
			reply("200 Switching to Binary mode.")
		case "SIZE":
			if f, ok := s.files[arg]; ok && !b.noSIZE {
				reply("213 %d", len(f))
			} else if b.noSIZE {
				reply("502 SIZE not implemented.")
			} else {
				reply("550 Could not get file size.")
			}
		case "EPSV":
			if b.noEPSV {
				reply("500 Unknown command.")
			} else {
				reply("229 Entering Extended Passive Mode (|||%d|)", listen())
			}
		case "PASV":
			// A wrong address (as behind NAT): the client must use the
			// control connection's peer.
			p := listen()
			reply("227 Entering Passive Mode (10,99,99,99,%d,%d).", p>>8, p&0xff)
		case "REST":
			if b.noREST {
				reply("502 REST not implemented.")
				continue
			}
			rest, _ = strconv.ParseInt(arg, 10, 64)
			reply("350 Restart position accepted (%d).", rest)
		case "RETR":
			f, ok := s.files[arg]
			if !ok || dataLn == nil {
				reply("550 Failed to open file.")
				continue
			}
			s.mu.Lock()
			s.retrs++
			first := s.retrs == 1
			s.mu.Unlock()
			reply("150 Opening BINARY mode data connection for %s (%d bytes).", arg, len(f))
			s.transfer(dataLn, f[rest:], first, b, reply)
			dataLn.Close()
			dataLn, rest = nil, 0
		case "QUIT":
			reply("221 Goodbye.")
			return
		default:
			reply("502 Command not implemented.")
		}
	}
}

func (s *fakeFTP) transfer(ln net.Listener, data []byte, first bool, b ftpBehaviour, reply func(string, ...any)) {
	ln.(*net.TCPListener).SetDeadline(time.Now().Add(5 * time.Second))
	dc, err := ln.Accept()
	if err != nil {
		reply("425 No data connection.")
		return
	}
	defer dc.Close()
	switch {
	case first && b.dropAfter > 0:
		dc.Write(data[:b.dropAfter])
		dc.Close()
		reply("426 Connection closed; transfer aborted.")
	case first && b.stallAfter > 0:
		dc.Write(data[:b.stallAfter])
		select { // keep the connection open without sending
		case <-s.done:
		case <-time.After(10 * time.Second):
		}
	default:
		dc.Write(data)
		dc.Close()
		reply("226 Transfer complete.")
	}
}

// ftpEnv is the v6 archive of the test catalog served by a fake FTP server.
func ftpEnv(t *testing.T) (*fixture, *Manager, *fakeFTP, []byte) {
	f := newFixture(t, defaultSpecs()[1:2])
	_, body := f.archive("v6")
	srv := newFakeFTP(t, map[string][]byte{"/pub/pdp11/uv6swre.zip": body})
	return f, f.manager(), srv, body
}

func checkDownloaded(t *testing.T, dir, sha string, body []byte) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(dir, sha))
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("downloaded file: %v (%d bytes, want %d)", err, len(got), len(body))
	}
	if _, err := os.Stat(filepath.Join(dir, sha+".part")); !os.IsNotExist(err) {
		t.Fatal(".part left behind")
	}
}

func count(cmds []string, prefix string) int {
	n := 0
	for _, c := range cmds {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func TestFTPDownload(t *testing.T) {
	f, m, srv, body := ftpEnv(t)
	srv.behave(func(b *ftpBehaviour) {
		b.banner = []string{"Welcome to the", "  archive: a continuation line without a code", "220-also not the end", "ready."}
		b.noEPSV = true
	})
	a, _ := f.archive("v6")
	a.URL, a.Mirrors = srv.url("/pub/pdp11/uv6swre.zip"), nil
	dir := t.TempDir()
	var last int64
	if err := m.dl.download(context.Background(), a, dir, func(n int64) { last = n }); err != nil {
		t.Fatal(err)
	}
	checkDownloaded(t, dir, a.SHA256, body)
	if last != int64(len(body)) {
		t.Fatalf("progress ended at %d of %d", last, len(body))
	}
	cmds := srv.commands()
	want := []string{"USER anonymous", "PASS pdp11d@", "TYPE I", "SIZE /pub/pdp11/uv6swre.zip", "EPSV", "PASV", "RETR /pub/pdp11/uv6swre.zip", "QUIT"}
	if strings.Join(cmds, "|") != strings.Join(want, "|") {
		t.Fatalf("commands %q, want %q", cmds, want)
	}
}

func TestFTPResumeAfterDroppedDataConnection(t *testing.T) {
	f, m, srv, body := ftpEnv(t)
	drop := int64(len(body) / 3)
	srv.behave(func(b *ftpBehaviour) { b.dropAfter = drop })
	a, _ := f.archive("v6")
	a.URL, a.Mirrors = srv.url("/pub/pdp11/uv6swre.zip"), nil
	dir := t.TempDir()
	if err := m.dl.download(context.Background(), a, dir, func(int64) {}); err != nil {
		t.Fatal(err)
	}
	checkDownloaded(t, dir, a.SHA256, body)
	cmds := srv.commands()
	if count(cmds, "RETR") != 2 || count(cmds, "EPSV") != 2 || count(cmds, fmt.Sprintf("REST %d", drop)) != 1 {
		t.Fatalf("commands %q: want a second RETR after REST %d", cmds, drop)
	}
}

func TestFTPWithoutREST(t *testing.T) {
	f, m, srv, body := ftpEnv(t)
	srv.behave(func(b *ftpBehaviour) { b.dropAfter, b.noREST = 1000, true })
	a, _ := f.archive("v6")
	a.URL, a.Mirrors = srv.url("/pub/pdp11/uv6swre.zip"), nil
	dir := t.TempDir()
	if err := m.dl.download(context.Background(), a, dir, func(int64) {}); err != nil {
		t.Fatal(err)
	}
	checkDownloaded(t, dir, a.SHA256, body) // started over, not appended
}

func TestFTPStall(t *testing.T) {
	f, m, srv, body := ftpEnv(t)
	srv.behave(func(b *ftpBehaviour) { b.stallAfter = 1000 })
	m.dl.stall = 300 * time.Millisecond
	a, _ := f.archive("v6")
	a.URL, a.Mirrors = srv.url("/pub/pdp11/uv6swre.zip"), nil
	dir := t.TempDir()
	start := time.Now()
	if err := m.dl.download(context.Background(), a, dir, func(int64) {}); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < m.dl.stall || d > 5*time.Second {
		t.Fatalf("download took %v (stall timeout %v)", d, m.dl.stall)
	}
	checkDownloaded(t, dir, a.SHA256, body)
	if cmds := srv.commands(); count(cmds, "REST 1000") != 1 {
		t.Fatalf("commands %q: no resume after the stall", cmds)
	}
}

func TestFTPMissingFileTriesTheNextMirror(t *testing.T) {
	f, m, srv, body := ftpEnv(t)
	a, _ := f.archive("v6")
	httpsURL := a.URL
	a.URL = srv.url("/pub/missing/uv6swre.zip")
	a.Mirrors = []string{srv.url("/pub/pdp11/uv6swre.zip")}
	dir := t.TempDir()
	if err := m.dl.download(context.Background(), a, dir, func(int64) {}); err != nil {
		t.Fatal(err)
	}
	checkDownloaded(t, dir, a.SHA256, body)
	if cmds := srv.commands(); count(cmds, "RETR /pub/missing/") != 1 {
		t.Fatalf("commands %q: a 550 must not be retried", cmds)
	}
	// Only missing everywhere: the error names the reply and every URL.
	a.Mirrors = []string{srv.url("/pub/gone/uv6swre.zip")}
	err := m.dl.download(context.Background(), a, t.TempDir(), func(int64) {})
	if err == nil || !strings.Contains(err.Error(), "550") || !strings.Contains(err.Error(), "/pub/gone/") || !strings.Contains(err.Error(), a.SHA256) {
		t.Fatalf("error %v", err)
	}
	// An FTP failure falls through to an HTTPS mirror.
	a.Mirrors = []string{httpsURL}
	dir = t.TempDir()
	if err := m.dl.download(context.Background(), a, dir, func(int64) {}); err != nil {
		t.Fatal(err)
	}
	checkDownloaded(t, dir, a.SHA256, body)
}

func TestFTPSizeMismatchAndLogin(t *testing.T) {
	f, m, srv, body := ftpEnv(t)
	a, _ := f.archive("v6")
	a.URL, a.Mirrors = srv.url("/pub/pdp11/uv6swre.zip"), nil
	a.Size++
	err := m.dl.download(context.Background(), a, t.TempDir(), func(int64) {})
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("the server has %d bytes", len(body))) {
		t.Fatalf("error %v", err)
	}
	if n := count(srv.commands(), "SIZE"); n != 1 {
		t.Fatalf("%d SIZE commands: a size mismatch must not be retried", n)
	}
	// A user from the URL is used instead of anonymous (refused here: 530
	// is retried, it often means "too many users").
	a.Size--
	a.URL = "ftp://joe:secret@" + srv.ln.Addr().String() + "/pub/pdp11/uv6swre.zip"
	err = m.dl.download(context.Background(), a, t.TempDir(), func(int64) {})
	if err == nil || !strings.Contains(err.Error(), "530") || count(srv.commands(), "USER joe") != attemptsPerURL {
		t.Fatalf("error %v, commands %q", err, srv.commands())
	}
}

func TestFTPCancel(t *testing.T) {
	f, m, srv, _ := ftpEnv(t)
	srv.behave(func(b *ftpBehaviour) { b.stallAfter = 1000 })
	a, _ := f.archive("v6")
	a.URL, a.Mirrors = srv.url("/pub/pdp11/uv6swre.zip"), nil
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	start := time.Now()
	err := m.dl.download(ctx, a, t.TempDir(), func(int64) {})
	if !errors.Is(err, context.Canceled) || time.Since(start) > 3*time.Second {
		t.Fatalf("error %v after %v", err, time.Since(start))
	}
}

func TestFTPReplyParsing(t *testing.T) {
	for _, tc := range []struct {
		in   string
		code int
		text string
		err  bool
	}{
		{in: "220 ready\r\n", code: 220, text: "ready"},
		{in: "230\r\n", code: 230, text: ""},
		{in: "220-first\r\nsecond\r\n220-third\r\n 220 indented\r\n220 last\r\n", code: 220, text: "first\nsecond\n220-third\n 220 indented\nlast"},
		{in: "150-a\n150 b\n", code: 150, text: "a\nb"},
		{in: "hello\r\n", err: true},
		{in: "220-unterminated\r\n", err: true},
	} {
		c := &ftpControl{conn: nopConn{}, r: bufio.NewReader(strings.NewReader(tc.in)), stall: time.Second}
		r, err := c.readReply()
		if (err != nil) != tc.err || (!tc.err && (r.code != tc.code || r.text != tc.text)) {
			t.Errorf("%q: %+v %v", tc.in, r, err)
		}
	}
	if p, ok := epsvPort("Entering Extended Passive Mode (|||6446|)"); !ok || p != 6446 {
		t.Errorf("EPSV: %d %v", p, ok)
	}
	if p, ok := epsvPort("Entering Extended Passive Mode (!!!21!)"); !ok || p != 21 {
		t.Errorf("EPSV with another delimiter: %d %v", p, ok)
	}
	if _, ok := epsvPort("Entering Extended Passive Mode"); ok {
		t.Error("EPSV without a port parsed")
	}
	if p, ok := pasvPort("Entering Passive Mode (192,168,1,2,195,149)."); !ok || p != 195<<8|149 {
		t.Errorf("PASV: %d %v", p, ok)
	}
	if p, ok := pasvPort("=127,0,0,1,4,1"); !ok || p != 1025 {
		t.Errorf("PASV without parentheses: %d %v", p, ok)
	}
}

// nopConn satisfies net.Conn for the reply parser test.
type nopConn struct{ net.Conn }

func (nopConn) SetReadDeadline(time.Time) error { return nil }

// rawFTP accepts one connection and lets serve play the server.
func rawFTP(t *testing.T, serve func(w *bufio.Writer) error) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		w := bufio.NewWriter(c)
		if serve(w) == nil {
			w.Flush()
		}
	}()
	return "ftp://" + ln.Addr().String() + "/pub/x.zip"
}

// TestFTPBoundedReplies: a greeting that never ends, one that trickles in
// line by line, and endless "120" replies all fail soon instead of
// growing pdp11d's memory or holding the install slot.
func TestFTPBoundedReplies(t *testing.T) {
	for _, tc := range []struct {
		name  string
		serve func(w *bufio.Writer) error
		want  string
	}{
		{"endless multi-line reply", func(w *bufio.Writer) error {
			fmt.Fprintf(w, "220-welcome\r\n")
			for i := 0; i < 20000; i++ { // 2 MB, then the reply stays open
				if _, err := fmt.Fprintf(w, "%098d\r\n", i); err != nil {
					return err
				}
			}
			w.Flush()
			time.Sleep(3 * time.Second)
			return nil
		}, errFTPReplyTooLong.Error()},
		{"reply trickling in", func(w *bufio.Writer) error {
			fmt.Fprintf(w, "220-welcome\r\n")
			for i := 0; i < 150; i++ { // a line every 20 ms for 3 s
				fmt.Fprintf(w, "line %d\r\n", i)
				if err := w.Flush(); err != nil {
					return err
				}
				time.Sleep(20 * time.Millisecond)
			}
			return nil
		}, "i/o timeout"},
		{"never ready", func(w *bufio.Writer) error {
			for i := 0; i < 300; i++ { // for 3 s
				fmt.Fprintf(w, "120 Service ready in 1 minute.\r\n")
				if err := w.Flush(); err != nil {
					return err
				}
				time.Sleep(10 * time.Millisecond)
			}
			return nil
		}, "still not ready after 10 replies"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDownloader(testLog(t))
			d.stall = 300 * time.Millisecond
			start := time.Now()
			err := d.fetchFTP(context.Background(), rawFTP(t, tc.serve), filepath.Join(t.TempDir(), "x.part"), 0, 100, func(int64) {})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v, want %q", err, tc.want)
			}
			var perm permanentError
			if errors.As(err, &perm) {
				t.Fatalf("%v is permanent: the next attempt may well work", err)
			}
			if d := time.Since(start); d > 2*time.Second {
				t.Fatalf("failed only after %s", d)
			}
		})
	}
}
