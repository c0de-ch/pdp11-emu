package simh

// A fake Open SIMH pdp11 for the unit tests: the test binary runs as the
// simulator when FAKESIMH=1. It reproduces the stdout framing of the real
// simulator (prompt without newline, "%SIM-INFO" CRLF lines while running,
// the blank line before stop lines, "<file>-<n>> <cmd>" echoes before
// messages of command files, output held back until the next prompt), the
// raw console listener, EXPECT/SEND, and the panel export with its halt
// handshake. A tiny "Unix" answers on the console after "boot".
//
// Directives in run.ini comments ("; fake: <word>[=<value>]") inject faults:
// ignore-halt, crash-on-boot, expect-stop, late-output, no-prompt,
// exit-on-start, slow-start=<ms>, halt-after=<ms>, sigterm-ignore.

import (
	"bufio"
	"fmt"
	"math"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"pdp11emu/server/internal/api"
	"pdp11emu/server/internal/shm"
)

func TestMain(m *testing.M) {
	if os.Getenv("FAKESIMH") == "1" {
		os.Exit(fakeMain(os.Args[1:]))
	}
	os.Setenv("FAKESIMH", "1") // inherited by the simulators the tests spawn
	os.Exit(m.Run())
}

type outLine struct {
	text string
	msg  bool // a message (SimH echoes the command-file line first)
}

type fakeExpect struct {
	pattern    string
	action     string
	persistent bool
}

type fakeSim struct {
	out *bufio.Writer
	in  *bufio.Reader
	dir map[string]string // directives

	memSize uint32
	mem     map[uint32]uint16
	regs    map[string]uint16
	dr      uint16
	units   map[string]string // attached unit -> file

	consoleL   net.Listener
	console    net.Conn
	pendingOut []byte

	shmMem []byte
	ringWr uint32
	instr  uint64

	expects []fakeExpect
	expBuf  []byte
	sendQ   []byte
	sendAt  time.Time
	sendGap time.Duration

	ranRun  bool
	running atomic.Bool
	sigterm atomic.Bool
	quit    bool
	os      *fakeOS
	runFor  time.Time // start of the current run
}

func fakeMain(args []string) int {
	f := &fakeSim{
		out:     bufio.NewWriterSize(os.Stdout, 1<<16),
		in:      bufio.NewReader(os.Stdin),
		dir:     map[string]string{},
		memSize: 1 << 20,
		mem:     map[uint32]uint16{},
		regs:    map[string]uint16{"PC": 0o104},
		units:   map[string]string{},
	}
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: pdp11 file")
		return 1
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Can't open file %s\n", args[0])
		return 1
	}
	abs, _ := filepath.Abs(args[0])
	lines := strings.Split(string(data), "\n")
	for _, l := range lines {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(l), "; fake: "); ok {
			k, v, _ := strings.Cut(strings.TrimSpace(rest), "=")
			f.dir[k] = v
		}
	}
	f.signals()
	if ms, ok := f.dir["slow-start"]; ok {
		n, _ := strconv.Atoi(ms)
		time.Sleep(time.Duration(n) * time.Millisecond)
	}
	f.printf("\nPDP-11 simulator Open SIMH V4.1-0 Current        git commit id: fake0000\n")
	f.doFile(abs, lines)
	if _, ok := f.dir["exit-on-start"]; ok {
		f.printf("fake: fatal start-up problem\n")
		f.out.Flush()
		return 1
	}
	if _, ok := f.dir["no-prompt"]; ok {
		f.out.Flush()
		select {}
	}
	return f.repl()
}

func (f *fakeSim) printf(format string, a ...any) { fmt.Fprintf(f.out, format, a...) }

func (f *fakeSim) signals() {
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		for sig := range ch {
			if sig == syscall.SIGINT {
				// pdp11d must never send SIGINT: leave evidence and die.
				os.WriteFile("SIGINT-RECEIVED", nil, 0o644)
				os.Exit(2)
			}
			if _, ok := f.dir["sigterm-ignore"]; ok {
				continue
			}
			if f.running.Load() {
				f.sigterm.Store(true)
			} else {
				os.Exit(143) // like dying by the signal at the prompt
			}
		}
	}()
}

// doFile executes a command file the way SimH's DO does: before the first
// message of a command, "<file>-<n>> <cmd>" is printed.
func (f *fakeSim) doFile(path string, lines []string) {
	for i, raw := range lines {
		l := strings.TrimSpace(raw)
		if l == "" || l[0] == ';' || l[0] == '#' {
			continue
		}
		echoed := false
		for _, o := range f.exec(l) {
			if o.msg && !echoed {
				f.printf("%s-%d> %s\n", path, i+1, l)
				echoed = true
			}
			f.printf("%s\n", o.text)
		}
		if f.quit {
			return
		}
	}
}

func (f *fakeSim) repl() int {
	for {
		f.printf("sim> ")
		f.out.Flush()
		line, err := f.in.ReadString('\n')
		if err != nil {
			return 0
		}
		cmd := strings.TrimSpace(line)
		if cmd == "" {
			continue
		}
		for _, o := range f.exec(cmd) {
			f.printf("%s\n", o.text)
		}
		if f.quit {
			f.out.Flush()
			return 0
		}
	}
}

func msg(format string, a ...any) []outLine {
	return []outLine{{fmt.Sprintf(format, a...), true}}
}

func (f *fakeSim) exec(line string) []outLine {
	w := strings.Fields(line)
	cmd := strings.ToLower(w[0])
	switch cmd {
	case "set":
		return f.set(line, w)
	case "attach":
		return f.attach(w)
	case "echo":
		return []outLine{{strings.TrimSpace(line[len(w[0]):]), false}}
	case "examine":
		return f.examine(w[1:])
	case "deposit":
		return f.deposit(w[1:])
	case "reset":
		f.regs["PSW"], f.regs["MMR0"], f.regs["MMR3"] = 0, 0, 0
		return nil
	case "do":
		return f.do(w[1:])
	case "expect":
		return f.expect(line)
	case "send":
		return f.send(line)
	case "noexpect":
		f.expects = nil
		return nil
	case "nosend":
		f.sendQ = nil
		return nil
	case "boot":
		return f.boot(w[1:])
	case "cont", "continue", "go", "run":
		if len(w) > 1 {
			a, err := strconv.ParseUint(w[1], 8, 16)
			if err != nil {
				return msg("Invalid argument")
			}
			f.regs["PC"] = uint16(a)
		}
		var pre []outLine
		if cmd == "run" {
			if f.ranRun {
				pre = []outLine{{"Resetting all devices...  This may not have been your intention.", false},
					{"The GO and CONTINUE commands do not reset devices.", false}}
			}
			f.ranRun = true
		}
		return append(pre, f.runCPU(false)...)
	case "step":
		return f.runCPU(true)
	case "show":
		return []outLine{{"fake show output", false}}
	case "quit", "exit", "bye":
		f.quit = true
		return []outLine{{"Goodbye", false}}
	}
	return msg("Unknown command")
}

func (f *fakeSim) set(line string, w []string) []outLine {
	if len(w) < 2 {
		return msg("Too few arguments")
	}
	arg := strings.ToLower(strings.Join(w[1:], " "))
	switch {
	case arg == "cpu 11/70":
		return []outLine{{"Disabling XQ", false}}
	case strings.HasPrefix(arg, "cpu ") && (strings.HasSuffix(arg, "m") || strings.HasSuffix(arg, "k")):
		n, err := strconv.Atoi(arg[4 : len(arg)-1])
		if err != nil {
			return msg("%%SIM-ERROR: Invalid argument")
		}
		unit := uint32(1 << 10)
		if strings.HasSuffix(arg, "m") {
			unit = 1 << 20
		}
		f.memSize = uint32(n) * unit
		return nil
	case arg == "nothrottle", arg == "cpu idle", arg == "tto 7b", strings.HasPrefix(arg, "console telnet=buffered"):
		return nil
	case arg == "throttle 1m":
		return []outLine{{"Idling disabled", false}}
	case strings.HasPrefix(arg, "console -u telnet="):
		addr := strings.TrimSuffix(line[strings.Index(strings.ToLower(line), "telnet=")+7:], ";notelnet")
		l, err := net.Listen("tcp", addr)
		if err != nil {
			return []outLine{{"Sockets: bind error 98 - Address already in use", false},
				{"%SIM-ERROR: Can't open network port: " + addr, true}}
		}
		f.consoleL = l
		return msg("%%SIM-INFO: Listening on port %s", addr)
	case arg == "panel refresh":
		f.refresh()
		return nil
	case strings.HasPrefix(arg, "bogus"):
		return msg("%%SIM-ERROR: Non-existent device: BOGUS")
	}
	return nil // device settings
}

func (f *fakeSim) attach(w []string) []outLine {
	var sw []string
	i := 1
	for ; i < len(w) && strings.HasPrefix(w[i], "-"); i++ {
		sw = append(sw, w[i])
	}
	if i+1 >= len(w) {
		return msg("Too few arguments")
	}
	unit, file := strings.ToLower(w[i]), w[i+1]
	switch {
	case unit == "panel":
		return f.attachPanel(file)
	case unit == "dz" || unit == "dci" || unit == "dli":
		addr := file
		if strings.HasPrefix(addr, ":") {
			addr = "127.0.0.1" + addr // keep the test listener local
		}
		l, err := net.Listen("tcp", addr)
		if err != nil {
			return []outLine{{"Sockets: bind error 98 - Address already in use", false},
				{"%SIM-ERROR: Can't open network port: " + file, true}}
		}
		_ = l // accepted by nobody: enough for the tests
		out := msg("%%SIM-INFO: Listening on port %s", file)
		for _, s := range sw {
			if s == "-m" {
				out = append(out, outLine{"Modem control activated", false})
			}
		}
		return out
	}
	if _, err := os.Stat(file); err != nil {
		return msg("%%SIM-ERROR: %s: Cannot open '%s' - No such file or directory", strings.ToUpper(unit), file)
	}
	f.units[unit] = file
	return msg("%%SIM-INFO: %s: '%s' Contains a fake file system", strings.ToUpper(unit), file)
}

// ---- panel export -------------------------------------------------------------

func (f *fakeSim) attachPanel(path string) []outLine {
	fd, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return msg("%%SIM-ERROR: PANEL: %v", err)
	}
	defer fd.Close()
	if err := fd.Truncate(shm.FileSize); err != nil {
		return msg("%%SIM-ERROR: PANEL: %v", err)
	}
	mem, err := syscall.Mmap(int(fd.Fd()), 0, shm.FileSize, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		return msg("%%SIM-ERROR: PANEL: %v", err)
	}
	f.shmMem = mem
	valid := f.load(shm.OffMagic) == shm.Magic && f.load(shm.OffVersion) == shm.Version
	if valid {
		f.store(shm.OffHaltAck, f.load(shm.OffHaltReq))
	} else {
		for i := range mem {
			mem[i] = 0
		}
	}
	f.store(shm.OffMagic, shm.Magic)
	f.store(shm.OffVersion, shm.Version)
	f.store(shm.OffSize, shm.FileSize)
	f.store(shm.OffRingSize, shm.RingSize)
	f.ringWr = f.load(shm.OffRingWr)
	f.refresh()
	return msg("%%SIM-INFO: PANEL: export attached to %s", path)
}

func (f *fakeSim) word(off int) *uint32 { return (*uint32)(unsafe.Pointer(&f.shmMem[off])) }
func (f *fakeSim) load(off int) uint32  { return atomic.LoadUint32(f.word(off)) }
func (f *fakeSim) store(off int, v uint32) {
	if f.shmMem != nil {
		atomic.StoreUint32(f.word(off), v)
	}
}

// refresh writes the register block under the seqlock.
func (f *fakeSim) refresh() {
	if f.shmMem == nil {
		return
	}
	seq := f.load(shm.OffSeq)
	f.store(shm.OffSeq, seq+1)
	for i, r := range []string{"R00", "R01", "R02", "R03", "R04", "R05", "KSP", "PC"} {
		f.store(shm.OffR+4*i, uint32(f.regs[r]))
	}
	f.store(shm.OffPSW, uint32(f.regs["PSW"]))
	f.store(shm.OffDR, uint32(f.dr))
	f.store(shm.OffMMR0, uint32(f.regs["MMR0"]))
	f.store(shm.OffMMR3, uint32(f.regs["MMR3"]))
	f.store(shm.OffSampleInterval, 1000)
	f.store(shm.OffSamplesPerSec, 20000)
	atomic.StoreUint64((*uint64)(unsafe.Pointer(&f.shmMem[shm.OffSimTime])), math.Float64bits(float64(f.instr)))
	atomic.StoreUint64((*uint64)(unsafe.Pointer(&f.shmMem[shm.OffInstrCount])), f.instr)
	f.store(shm.OffSeq, seq+2)
}

func (f *fakeSim) samples(n int) {
	if f.shmMem == nil {
		return
	}
	pc := uint32(f.regs["PC"])
	for i := 0; i < n; i++ {
		off := shm.OffRing + int(f.ringWr%shm.RingSize)*shm.SampleSize
		f.store(off, pc|api.PnlIFetch)
		f.store(off+4, uint32(0o77301)|pc<<16)
		f.store(off+8, 50)
		f.store(off+12, pc|0o77301<<16)
		f.ringWr++
		f.store(shm.OffRingWr, f.ringWr)
	}
	f.instr += uint64(n) * 1000
}

// ---- memory -----------------------------------------------------------------

// location parses examine/deposit arguments into a register name or a
// physical address; errText is SimH's reply for a bad location.
func (f *fakeSim) location(args []string) (reg string, pa uint32, label string, rest []string, errText string) {
	if len(args) == 0 {
		return "", 0, "", nil, "Too few arguments"
	}
	virtual, dspace := false, false
	mode := api.ModeKernel
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		switch strings.ToLower(args[0]) {
		case "-v":
			virtual = true
		case "-k":
			mode = api.ModeKernel
		case "-s":
			mode = api.ModeSuper
		case "-u":
			mode = api.ModeUser
		case "-t":
			dspace = true
		}
		args = args[1:]
	}
	if len(args) == 0 {
		return "", 0, "", nil, "Too few arguments"
	}
	a, rest := args[0], args[1:]
	if r := strings.ToUpper(a); fakeRegister(r) {
		return r, 0, r, rest, ""
	}
	v, err := strconv.ParseUint(a, 8, 32)
	if err != nil || v > maxPhys {
		return "", 0, "", nil, "Invalid argument"
	}
	if virtual {
		if v > 0o177777 {
			return "", 0, "", nil, "Address space exceeded"
		}
		p, _ := translate(uint16(v), mode, dspace, f.regs["MMR0"], f.regs["MMR3"], func(n string) (uint16, error) { return f.regs[n], nil })
		return "", p, a, rest, ""
	}
	return "", uint32(v), a, rest, ""
}

// fakeRegister reports the CPU registers the fake knows (SimH names).
func fakeRegister(r string) bool {
	if registers[r] || r == "PSW" || r == "MMR0" || r == "MMR3" || r == "WAIT" {
		return true
	}
	if len(r) == 6 && strings.ContainsRune("KSU", rune(r[0])) && strings.ContainsRune("ID", rune(r[1])) &&
		r[2:5] == "PAR" && r[5] >= '0' && r[5] <= '7' {
		return true
	}
	return false
}

func (f *fakeSim) memOK(pa uint32) bool {
	return pa < f.memSize || pa == 0o17777570 || pa == 0o17777572 || pa == 0o17777516 || pa == 0o17777776
}

func (f *fakeSim) examine(args []string) []outLine {
	reg, pa, label, _, e := f.location(args)
	if e != "" {
		return msg("%s", e)
	}
	var v uint16
	switch {
	case reg != "":
		v = f.regs[reg]
	case !f.memOK(pa):
		return msg("Address space exceeded")
	case pa == 0o17777570:
		v = f.sr()
	case pa == 0o17777572:
		v = f.regs["MMR0"]
	case pa == 0o17777516:
		v = f.regs["MMR3"]
	case pa == 0o17777776:
		v = f.regs["PSW"]
	default:
		v = f.mem[pa&^1]
	}
	return []outLine{{fmt.Sprintf("%s:\t%06o", label, v), false}}
}

func (f *fakeSim) deposit(args []string) []outLine {
	reg, pa, _, rest, e := f.location(args)
	if e != "" {
		return msg("%s", e)
	}
	if len(rest) != 1 {
		return msg("Too few arguments")
	}
	v, err := strconv.ParseUint(rest[0], 8, 16)
	if err != nil {
		return msg("Invalid argument")
	}
	switch {
	case reg != "":
		f.regs[reg] = uint16(v)
	case !f.memOK(pa):
		return msg("Address space exceeded")
	case pa == 0o17777570:
		f.dr = uint16(v)
	case pa == 0o17777572:
		f.regs["MMR0"] = uint16(v)
	case pa == 0o17777516:
		f.regs["MMR3"] = uint16(v)
	case pa == 0o17777776:
		f.regs["PSW"] = uint16(v)
	default:
		f.mem[pa&^1] = uint16(v)
	}
	return nil
}

func (f *fakeSim) sr() uint16 {
	if f.shmMem != nil && f.load(shm.OffSRValid) != 0 {
		return uint16(f.load(shm.OffSR))
	}
	return 0
}

// ---- do / expect / send -------------------------------------------------------------

func (f *fakeSim) do(args []string) []outLine {
	if len(args) == 0 {
		return msg("Too few arguments")
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		return msg("File open error")
	}
	abs, _ := filepath.Abs(args[0])
	f.doFile(abs, strings.Split(string(data), "\n"))
	return nil
}

func (f *fakeSim) expect(line string) []outLine {
	rest := strings.TrimSpace(line[len("expect"):])
	e := fakeExpect{}
	for strings.HasPrefix(rest, "-") {
		sw, r, _ := strings.Cut(rest, " ")
		if strings.Contains(sw, "p") {
			e.persistent = true
		}
		rest = strings.TrimSpace(r)
	}
	if !strings.HasPrefix(rest, `"`) {
		return msg("Too few arguments")
	}
	end := strings.Index(rest[1:], `"`)
	if end < 0 {
		return msg("Invalid argument")
	}
	e.pattern = unescape(rest[1 : end+1])
	e.action = strings.TrimSpace(rest[end+2:])
	f.expects = append(f.expects, e)
	return nil
}

// send queues console input: send [-t] [after=n,][delay=n,]"text".
func (f *fakeSim) send(line string) []outLine {
	rest := strings.TrimSpace(line[len("send"):])
	micro := false
	if strings.HasPrefix(rest, "-t ") {
		micro, rest = true, strings.TrimSpace(rest[3:])
	}
	after, delay := 1000, 1000
	for !strings.HasPrefix(rest, `"`) {
		kv, r, ok := strings.Cut(rest, ",")
		if !ok {
			return msg("Invalid argument")
		}
		k, v, _ := strings.Cut(kv, "=")
		n, _ := strconv.Atoi(v)
		if k == "after" {
			after = n
		} else if k == "delay" {
			delay = n
		}
		rest = r
	}
	text := unescape(strings.Trim(rest, `"`))
	unit := time.Microsecond
	if !micro {
		unit = time.Microsecond / 50 // "instructions" at a fake 50 MIPS
	}
	f.sendQ = append(f.sendQ, text...)
	f.sendAt = time.Now().Add(time.Duration(after) * unit)
	f.sendGap = time.Duration(delay) * unit
	return nil
}

func unescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch c := s[i]; {
		case c == 'r':
			b.WriteByte('\r')
		case c == 'n':
			b.WriteByte('\n')
		case c >= '0' && c <= '7':
			j := i
			for j < len(s) && j < i+3 && s[j] >= '0' && s[j] <= '7' {
				j++
			}
			n, _ := strconv.ParseUint(s[i:j], 8, 8)
			b.WriteByte(byte(n))
			i = j - 1
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// guestOutput is console output of the guest: it goes through the EXPECT
// rules and to the console connection. It returns a pending EXPECT stop.
func (f *fakeSim) guestOutput(p []byte) (stop bool, out []outLine) {
	if f.console != nil {
		f.console.Write(p)
	} else {
		f.pendingOut = append(f.pendingOut, p...)
	}
	for _, c := range p {
		f.expBuf = append(f.expBuf, c)
		if len(f.expBuf) > 512 {
			f.expBuf = append(f.expBuf[:0], f.expBuf[len(f.expBuf)-256:]...)
		}
		for i := 0; i < len(f.expects); i++ {
			e := f.expects[i]
			if !strings.HasSuffix(string(f.expBuf), e.pattern) {
				continue
			}
			f.expBuf = f.expBuf[:0]
			if !e.persistent {
				f.expects = append(f.expects[:i], f.expects[i+1:]...)
				i--
			}
			actions := strings.Split(e.action, ";")
			cont := false
			for _, a := range actions {
				a = strings.TrimSpace(a)
				switch {
				case a == "continue":
					cont = true
				case strings.HasPrefix(a, "send"):
					f.send(a)
				case strings.HasPrefix(a, "echo"):
					out = append(out, outLine{strings.TrimSpace(a[4:]), false})
				}
			}
			if !cont {
				stop = true
			}
		}
	}
	return stop, out
}

// ---- console ----------------------------------------------------------------

func (f *fakeSim) pollConsole() {
	if f.console == nil && f.consoleL != nil {
		tl := f.consoleL.(*net.TCPListener)
		tl.SetDeadline(time.Now().Add(time.Microsecond))
		if c, err := tl.Accept(); err == nil {
			f.console = c
			if len(f.pendingOut) > 0 {
				c.Write(f.pendingOut)
				f.pendingOut = nil
			}
		}
	}
}

// consoleInput returns the bytes typed on the console since the last call
// (SimH reads the console only while the CPU runs).
func (f *fakeSim) consoleInput() []byte {
	var in []byte
	if f.console != nil {
		buf := make([]byte, 4096)
		f.console.SetReadDeadline(time.Now().Add(100 * time.Microsecond))
		if n, err := f.console.Read(buf); n > 0 {
			in = append(in, buf[:n]...)
		} else if err != nil && !os.IsTimeout(err) {
			f.console.Close()
			f.console = nil
		}
	}
	if len(f.sendQ) > 0 && time.Now().After(f.sendAt) {
		in = append(in, f.sendQ[0])
		f.sendQ = f.sendQ[1:]
		f.sendAt = time.Now().Add(f.sendGap)
	}
	return in
}

// ---- CPU ---------------------------------------------------------------------

func (f *fakeSim) boot(args []string) []outLine {
	if len(args) != 1 {
		return msg("Too few arguments")
	}
	unit := strings.ToLower(args[0])
	dev := strings.TrimRight(unit, "0123456789")
	switch dev {
	case "rk", "rl", "rp", "rq", "hk":
	default:
		return msg("%%SIM-ERROR: Non-existent device: %s", strings.ToUpper(unit))
	}
	if _, ok := f.units[unit]; !ok {
		return []outLine{{"", false}, {fmt.Sprintf("Unit not attached, PC: %06o (HALT)", f.regs["PC"]), false}}
	}
	if _, ok := f.dir["crash-on-boot"]; ok {
		os.Exit(3)
	}
	f.regs["PSW"], f.regs["MMR0"], f.regs["MMR3"] = 0, 0, 0
	f.regs["PC"] = 0o2000
	f.os = &fakeOS{f: f}
	return f.runCPU(false)
}

// runCPU runs until a halt request, SIGTERM, a HALT, an EXPECT stop or
// (step) one instruction, and returns the stop output.
func (f *fakeSim) runCPU(step bool) []outLine {
	if f.console == nil {
		f.printf("%%SIM-INFO: Running with Buffered Console\r\n")
		f.out.Flush()
	}
	f.pollConsole()
	f.running.Store(true)
	f.store(shm.OffState, api.PnlRunning)
	f.runFor = time.Now()
	reason, insn, extra := f.loop(step)
	f.running.Store(false)
	f.refresh()
	f.store(shm.OffState, api.PnlHalted)
	f.store(shm.OffStopCount, f.load(shm.OffStopCount)+1)
	out := append(extra, outLine{"", false})
	if reason == "" { // EXPECT stop: no stop line
		return extra
	}
	return append(out, outLine{fmt.Sprintf("%s, PC: %06o (%s)", reason, f.regs["PC"], insn), false})
}

func (f *fakeSim) loop(step bool) (reason, insn string, extra []outLine) {
	_, ignoreHalt := f.dir["ignore-halt"]
	if _, ok := f.dir["late-output"]; ok {
		f.printf("late line from the simulator\n") // held back until the prompt
	}
	haltAfter := time.Duration(-1)
	if v, ok := f.dir["halt-after"]; ok && f.os != nil {
		n, _ := strconv.Atoi(v)
		haltAfter = time.Duration(n) * time.Millisecond
	}
	for first := true; ; first = false {
		// The halt handshake is checked at every sample, the first included.
		if !ignoreHalt && f.shmMem != nil {
			if req := f.load(shm.OffHaltReq); req != f.load(shm.OffHaltAck) {
				f.store(shm.OffHaltAck, req)
				return reasonPanelHalt, "SOB R3,1062", extra
			}
		}
		if f.sigterm.Swap(false) {
			return "SIGTERM received", "BR 2000", extra
		}
		if f.os == nil && first {
			pc := uint32(f.regs["PC"])
			if f.mem[pc] == 0 { // HALT
				f.regs["PC"] += 2
				return "HALT instruction", "HALT", extra
			}
		}
		if step {
			f.regs["PC"] += 2
			f.samples(1)
			return reasonStep, "MOV R0,R1", extra
		}
		f.samples(20)
		f.refresh() // the real simulator refreshes the block every 64 samples
		f.pollConsole()
		if f.os != nil {
			if _, ok := f.dir["expect-stop"]; ok {
				return "", "", []outLine{{"EXPECT-ACTION-OUTPUT", false}}
			}
			if r := f.os.run(f.consoleInput()); r != "" {
				return r, "HALT", extra
			}
			if f.os.expectStop {
				f.os.expectStop = false
				return "", "", append(extra, f.os.expectOut...)
			}
			if haltAfter >= 0 && time.Since(f.runFor) > haltAfter {
				f.regs["PC"] = 0o16
				return "HALT instruction", "HALT", extra
			}
		}
		time.Sleep(time.Millisecond)
	}
}

// fakeOS is a minimal Unix V6 lookalike: "@" boot prompt, login, shell.
type fakeOS struct {
	f          *fakeSim
	state      int // 0 boot prompt, 1 login, 2 shell
	line       []byte
	started    bool
	expectStop bool
	expectOut  []outLine
}

func (o *fakeOS) print(s string) {
	stop, out := o.f.guestOutput([]byte(s))
	if stop {
		o.expectStop = true
		o.expectOut = append(o.expectOut, out...)
	}
}

// run consumes console input and returns a stop reason ("" = keep running).
func (o *fakeOS) run(in []byte) string {
	if !o.started {
		o.started = true
		o.print("@")
	}
	for _, c := range in {
		if c != '\r' && c != '\n' {
			o.line = append(o.line, c)
			o.print(string(c))
			continue
		}
		line := strings.TrimSpace(string(o.line))
		o.line = o.line[:0]
		switch o.state {
		case 0:
			if line == "rkunix" {
				o.print("\r\nmem = 1035\r\n\x00\x00RESTRICTED RIGHTS\r\n\r\nlo\x7fgin: ")
				o.state = 1
			} else {
				o.print("\r\n@")
			}
		case 1:
			if line == "root" {
				o.print("\r\n# ")
				o.state = 2
			} else {
				o.print("\r\nLogin incorrect\r\nlogin: ")
			}
		case 2:
			switch {
			case line == "halt":
				o.print("\r\n")
				o.f.regs["PC"] = 0o16
				return "HALT instruction"
			case line == "crash":
				os.Exit(3)
			case line == "sr":
				o.print(fmt.Sprintf("\r\nSR=%06o\r\n# ", o.f.sr()))
			case strings.HasPrefix(line, "echo "):
				o.print("\r\n" + line[5:] + "\r\n# ")
			case strings.HasPrefix(line, "flood "):
				n, _ := strconv.Atoi(line[6:])
				o.print("\r\n" + strings.Repeat("x", n) + "\r\n# ")
			case line == "":
				o.print("\r\n# ")
			default:
				o.print("\r\n" + line + ": not found\r\n# ")
			}
		}
	}
	return ""
}
