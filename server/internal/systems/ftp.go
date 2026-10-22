package systems

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The built-in FTP client for ftp:// artifact URLs (RFC 959, EPSV from
// RFC 2428, REST STREAM and SIZE from RFC 3659): anonymous login (or the
// URL's user and password), binary type, a passive data connection (EPSV,
// then PASV), resume with REST. Timeouts are the downloader's: connect for
// every connection, stall for every reply (as a whole: further lines of a
// multi-line reply do not extend it) and every read of the data. Replies
// are bounded in size and lines, and so is the number of "120" replies
// before the greeting: a misbehaving server cannot hold pdp11d's memory or
// the install slot.
//
// Reply classes: 1xx/2xx/3xx progress, 4xx transient (retried), 5xx
// permanent for this URL (the next mirror is tried), except 530 (often
// "too many users").

const (
	ftpAnonUser      = "anonymous"
	ftpAnonPass      = "pdp11d@"
	ftpMaxLine       = 8 << 10  // longest reply line accepted
	ftpMaxReply      = 64 << 10 // longest reply accepted (all its lines)
	ftpMaxReplyLines = 1000     // most lines of a multi-line reply
	ftpMaxNotReady   = 10       // most "120 service ready in nnn minutes" replies
)

var errFTPReplyTooLong = errors.New("FTP reply too long")

// ftpReply is one (possibly multi-line) reply of the server.
type ftpReply struct {
	code int
	text string // the lines without their codes, joined with "\n"
}

func (r ftpReply) String() string {
	line, _, _ := strings.Cut(r.text, "\n")
	return strconv.Itoa(r.code) + " " + strings.TrimSpace(line)
}

// ftpControl is the control connection.
type ftpControl struct {
	conn  net.Conn
	r     *bufio.Reader
	stall time.Duration
}

// readLine reads one reply line without its line end. The read deadline
// is the reply's (readReply).
func (c *ftpControl) readLine() (string, error) {
	var line []byte
	for {
		chunk, err := c.r.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > ftpMaxLine {
			return "", errors.New("FTP reply line too long")
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			if err == io.EOF {
				err = errors.New("the server closed the control connection")
			}
			return "", err
		}
		return strings.TrimRight(string(line), "\r\n"), nil
	}
}

// replyCode parses the "ddd" of a reply line and the character after it
// (' ', '-', or 0 at the end of the line).
func replyCode(line string) (code int, sep byte, ok bool) {
	if len(line) < 3 {
		return 0, 0, false
	}
	n, err := strconv.Atoi(line[:3])
	if err != nil || n < 100 || n > 599 {
		return 0, 0, false
	}
	if len(line) == 3 {
		return n, 0, true
	}
	if line[3] != ' ' && line[3] != '-' {
		return 0, 0, false
	}
	return n, line[3], true
}

// readReply reads a reply, within the stall timeout as a whole. A
// multi-line reply starts with "ddd-" and ends with a line "ddd " of the
// same code; the lines in between may hold anything (RFC 959 §4.2).
func (c *ftpControl) readReply() (ftpReply, error) {
	if err := c.conn.SetReadDeadline(time.Now().Add(c.stall)); err != nil {
		return ftpReply{}, err
	}
	line, err := c.readLine()
	if err != nil {
		return ftpReply{}, err
	}
	code, sep, ok := replyCode(line)
	if !ok {
		return ftpReply{}, fmt.Errorf("not an FTP reply: %q", line)
	}
	lines := []string{strings.TrimSpace(line[min(4, len(line)):])}
	size := len(line)
	for sep == '-' {
		if line, err = c.readLine(); err != nil {
			return ftpReply{}, err
		}
		if size += len(line); size > ftpMaxReply || len(lines) >= ftpMaxReplyLines {
			return ftpReply{}, errFTPReplyTooLong
		}
		if c2, s2, ok := replyCode(line); ok && c2 == code && s2 != '-' {
			lines = append(lines, strings.TrimSpace(line[min(4, len(line)):]))
			break
		}
		lines = append(lines, line)
	}
	return ftpReply{code: code, text: strings.Join(lines, "\n")}, nil
}

// cmd sends a command and reads its reply.
func (c *ftpControl) cmd(line string) (ftpReply, error) {
	if err := c.conn.SetWriteDeadline(time.Now().Add(c.stall)); err != nil {
		return ftpReply{}, err
	}
	if _, err := io.WriteString(c.conn, line+"\r\n"); err != nil {
		return ftpReply{}, err
	}
	return c.readReply()
}

// ftpFailure turns an unexpected reply into an error.
func ftpFailure(what string, r ftpReply) error {
	err := fmt.Errorf("FTP %s: %s", what, r)
	if r.code >= 500 && r.code != 530 {
		return permanentError{err}
	}
	return err
}

var rePASV = regexp.MustCompile(`(\d{1,3}),(\d{1,3}),(\d{1,3}),(\d{1,3}),(\d{1,3}),(\d{1,3})`)

// epsvPort parses "229 Entering Extended Passive Mode (|||6446|)".
func epsvPort(text string) (int, bool) {
	i, j := strings.IndexByte(text, '('), strings.LastIndexByte(text, ')')
	if i < 0 || j < i+6 {
		return 0, false
	}
	f := text[i+1 : j]
	parts := strings.Split(f, f[:1])
	if len(parts) != 5 {
		return 0, false
	}
	p, err := strconv.Atoi(parts[3])
	return p, err == nil && p > 0 && p < 65536
}

// pasvPort parses "227 Entering Passive Mode (h1,h2,h3,h4,p1,p2)". The
// address is ignored: the data connection goes to the control connection's
// peer (servers behind NAT announce private addresses; it also rules out
// connections to third parties).
func pasvPort(text string) (int, bool) {
	m := rePASV.FindStringSubmatch(text)
	if m == nil {
		return 0, false
	}
	hi, _ := strconv.Atoi(m[5])
	lo, _ := strconv.Atoi(m[6])
	p := hi<<8 | lo
	return p, hi < 256 && lo < 256 && p > 0
}

// passive opens the data connection: EPSV, or PASV if the server does not
// support EPSV.
func (c *ftpControl) passive(ctx context.Context, dialer *net.Dialer) (net.Conn, error) {
	host, _, err := net.SplitHostPort(c.conn.RemoteAddr().String())
	if err != nil {
		return nil, err
	}
	r, err := c.cmd("EPSV")
	if err != nil {
		return nil, err
	}
	port, ok := 0, false
	if r.code == 229 {
		if port, ok = epsvPort(r.text); !ok {
			return nil, fmt.Errorf("FTP EPSV: cannot parse %q", r.String())
		}
	} else {
		if r, err = c.cmd("PASV"); err != nil {
			return nil, err
		}
		if r.code != 227 {
			return nil, ftpFailure("PASV", r)
		}
		if port, ok = pasvPort(r.text); !ok {
			return nil, fmt.Errorf("FTP PASV: cannot parse %q", r.String())
		}
	}
	return dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
}

// fetchFTP downloads an ftp:// URL into part, which holds have bytes of an
// earlier attempt (resumed with REST when the server supports it).
func (d downloader) fetchFTP(ctx context.Context, rawURL, part string, have, size int64, progress func(int64)) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return permanentError{err}
	}
	file := u.Path
	if file == "" || strings.HasSuffix(file, "/") {
		return permanentError{fmt.Errorf("no file name in %s", rawURL)}
	}
	addr := u.Host
	if u.Port() == "" {
		addr = net.JoinHostPort(u.Hostname(), "21")
	}
	user, pass := ftpAnonUser, ftpAnonPass
	if u.User != nil {
		user = u.User.Username()
		if p, ok := u.User.Password(); ok {
			pass = p
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	dialer := &net.Dialer{Timeout: d.connect, KeepAlive: 30 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	// Cancelling the context closes both connections, which ends any read.
	var mu sync.Mutex
	conns := []net.Conn{conn}
	stop := context.AfterFunc(ctx, func() {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			c.Close()
		}
	})
	defer stop()
	defer conn.Close()
	failed := func(err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}

	c := &ftpControl{conn: conn, r: bufio.NewReaderSize(conn, 4096), stall: d.stall}
	r, err := c.readReply()
	for n := 1; err == nil && r.code == 120; n++ { // "service ready in nnn minutes"
		if n == ftpMaxNotReady {
			return fmt.Errorf("FTP greeting: the server is still not ready after %d replies (%s)", n, r)
		}
		r, err = c.readReply()
	}
	if err != nil {
		return failed(err)
	}
	if r.code != 220 {
		return ftpFailure("greeting", r)
	}
	if r, err = c.cmd("USER " + user); err == nil && r.code == 331 {
		r, err = c.cmd("PASS " + pass)
	}
	if err != nil {
		return failed(err)
	}
	if r.code != 230 && r.code != 202 {
		return ftpFailure("login", r)
	}
	if r, err = c.cmd("TYPE I"); err != nil {
		return failed(err)
	} else if r.code != 200 {
		return ftpFailure("TYPE I", r)
	}
	// A different size means the file changed upstream: no point in
	// downloading it (servers without SIZE are fine).
	if r, err = c.cmd("SIZE " + file); err != nil {
		return failed(err)
	} else if r.code == 213 {
		if n, perr := strconv.ParseInt(strings.TrimSpace(r.text), 10, 64); perr == nil && n != size {
			return permanentError{fmt.Errorf("the server has %d bytes, expected %d", n, size)}
		}
	}

	data, err := c.passive(ctx, dialer)
	if err != nil {
		return failed(err)
	}
	mu.Lock()
	conns = append(conns, data)
	mu.Unlock()
	defer data.Close()
	// REST must come right before RETR.
	if have > 0 {
		if r, err = c.cmd("REST " + strconv.FormatInt(have, 10)); err != nil {
			return failed(err)
		}
		if r.code != 350 {
			d.log.Info("FTP server cannot resume; starting over", "url", rawURL, "reply", r.String())
			have = 0
		}
	}
	if r, err = c.cmd("RETR " + file); err != nil {
		return failed(err)
	}
	if r.code != 150 && r.code != 125 {
		return ftpFailure("RETR", r)
	}

	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if have > 0 {
		flags = os.O_WRONLY | os.O_CREATE | os.O_APPEND
	}
	f, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return err
	}
	buf := make([]byte, 256<<10)
	n := have
	var werr error
	for {
		if err := data.SetReadDeadline(time.Now().Add(d.stall)); err != nil {
			werr = err
			break
		}
		k, rerr := data.Read(buf)
		if k > 0 {
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
			var ne net.Error
			if errors.As(rerr, &ne) && ne.Timeout() {
				werr = fmt.Errorf("no data for %s", d.stall)
			}
			break
		}
	}
	data.Close()
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return failed(werr)
	}
	// The transfer's final reply: 226/250 complete, 4xx/5xx aborted.
	if r, err = c.readReply(); err != nil {
		return failed(fmt.Errorf("after %d of %d bytes: %w", n, size, err))
	}
	if r.code != 226 && r.code != 250 {
		return ftpFailure(fmt.Sprintf("RETR (after %d of %d bytes)", n, size), r)
	}
	if n != size {
		return fmt.Errorf("transfer ended after %d of %d bytes", n, size)
	}
	c.stall = 2 * time.Second
	_, _ = c.cmd("QUIT") // best effort
	return nil
}
