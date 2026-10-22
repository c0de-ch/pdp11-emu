"""Drive a pdp11 simulator the way pdp11d does: SCP on stdin/stdout pipes,
the DL11 console on a localhost TCP port (raw, buffered), the panel export
in a file (see simtest.panel)."""

import os
import re
import signal
import socket
import subprocess
import threading
import time

PROMPT = b"sim> "
# DESIGN.md section 6: the stop line of a run command
STOP_RE = re.compile(rb"^(?P<why>[^\r\n]*), PC: (?P<pc>[0-7]+)(?: \((?P<insn>[^\r\n]*)\))?\r?$", re.M)

_live = set()                                   # processes to kill at exit


def kill_all():
    for p in list(_live):
        try:
            p.kill()
            p.wait(5)
        except Exception:
            pass
        _live.discard(p)


class Stream:
    """Bytes arriving from a reader thread, with arrival times."""

    def __init__(self, name):
        self.name = name
        self.buf = bytearray()
        self.times = []                         # (offset after chunk, monotonic time)
        self.cv = threading.Condition()
        self.closed = False

    def feed(self, data):
        with self.cv:
            if data:
                self.buf += data
                self.times.append((len(self.buf), time.monotonic()))
            else:
                self.closed = True
            self.cv.notify_all()

    def size(self):
        with self.cv:
            return len(self.buf)

    def text(self, start=0, end=None):
        with self.cv:
            return bytes(self.buf[start:end])

    def arrival(self, offset):
        """Monotonic time at which byte `offset` arrived."""
        with self.cv:
            for end, t in self.times:
                if end > offset:
                    return t
        return None

    def wait(self, pattern, start, timeout):
        """Wait until regex `pattern` (bytes) matches at or after `start`."""
        rx = re.compile(pattern, re.S) if isinstance(pattern, bytes) else pattern
        deadline = time.monotonic() + timeout
        with self.cv:
            while True:
                m = rx.search(self.buf, start)
                if m:
                    return m
                left = deadline - time.monotonic()
                if left <= 0 or self.closed:
                    tail = bytes(self.buf[start:][-600:])
                    raise TimeoutError("%s: no %r within %.1f s; output: %r"
                                       % (self.name, rx.pattern, timeout, tail))
                self.cv.wait(left)


def _reader(fd_read, stream):
    while True:
        try:
            d = fd_read()
        except OSError:
            d = b""
        stream.feed(d)
        if not d:
            return


def free_port():
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


class Stop:
    def __init__(self, why, pc, insn, text, t):
        self.why, self.pc, self.insn, self.text, self.time = why, pc, insn, text, t

    def __repr__(self):
        return "Stop(%r, PC %06o)" % (self.why, self.pc if self.pc is not None else 0)


class Console:
    """Raw TCP connection to the simulated console."""

    def __init__(self, port, timeout=5.0):
        deadline = time.monotonic() + timeout
        while True:
            try:
                self.sock = socket.create_connection(("127.0.0.1", port), timeout=2)
                break
            except OSError:
                if time.monotonic() > deadline:
                    raise
                time.sleep(0.05)
        self.sock.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
        self.sock.settimeout(None)
        self.out = Stream("console")
        threading.Thread(target=_reader, args=(lambda: self.sock.recv(65536), self.out),
                         daemon=True).start()

    def send(self, data, char_delay=0.0):
        if isinstance(data, str):
            data = data.encode("latin-1")
        if char_delay <= 0:
            self.sock.sendall(data)
            return
        for b in data:
            self.sock.sendall(bytes([b]))
            time.sleep(char_delay)

    def close(self):
        try:
            self.sock.close()
        except OSError:
            pass


class Sim:
    """One simulator process.  setup: SCP lines executed from the start-up
    file before the console and panel lines."""

    def __init__(self, binary, workdir, setup=(), console=True, panel=None,
                 name="sim", timeout=20.0, env=None):
        os.makedirs(workdir, exist_ok=True)
        self.workdir = workdir
        lines = list(setup)
        self.port = None
        if console:
            self.port = free_port()
            lines += ["set console -u telnet=127.0.0.1:%d;notelnet" % self.port,
                      "set console telnet=buffered=65536"]
        if panel:
            lines.append("attach panel %s" % panel)
        self.ini = os.path.join(workdir, name + ".ini")
        with open(self.ini, "w") as f:
            f.write("\n".join(lines) + "\n")
        e = dict(os.environ if env is None else env)
        e["HOME"] = workdir
        self.proc = subprocess.Popen([binary, self.ini], cwd=workdir, env=e,
                                     stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                     stderr=subprocess.STDOUT, bufsize=0,
                                     start_new_session=True)
        _live.add(self.proc)
        self.out = Stream(name)
        threading.Thread(target=_reader, args=(lambda: os.read(self.proc.stdout.fileno(), 65536),
                                               self.out), daemon=True).start()
        m = self.out.wait(re.escape(PROMPT), 0, timeout)
        self.startup = self.out.text(0, m.start()).decode("latin-1")
        self.mark = m.end()
        if "%SIM-ERROR" in self.startup:
            self.close()
            raise RuntimeError("simulator start-up failed:\n" + self.startup)
        self.console = Console(self.port) if console else None
        self.running = False

    # ---- SCP ------------------------------------------------------------

    def _write(self, line):
        self.proc.stdin.write(line.encode("latin-1") + b"\n")
        self.proc.stdin.flush()

    def command(self, line, timeout=10.0):
        """Execute an SCP command while halted; return its output."""
        assert not self.running, "command %r while running" % line
        start = self.out.size()
        self._write(line)
        m = self.out.wait(re.escape(PROMPT), start, timeout)
        self.mark = m.end()
        return self.out.text(start, m.start()).decode("latin-1").strip("\r\n")

    def commands(self, lines, timeout=10.0):
        return [self.command(l, timeout) for l in lines]

    def examine(self, what):
        """Value of an EXAMINE (register name or octal address)."""
        r = self.command("examine " + what)
        m = re.match(r"^\S+:\s+([0-7]+)\b", r.strip())     # PSW: 000000\tCM=K ...
        if not m:
            raise RuntimeError("examine %s: %r" % (what, r))
        return int(m.group(1), 8)

    def start(self, line):
        """Write a run command (cont, go, run, boot, step); return the
        stdout offset after which its stop output appears."""
        assert not self.running
        start = self.out.size()
        self._write(line)
        self.running = True
        self.run_start = start
        return start

    def wait_stop(self, timeout=10.0, since=None):
        """Wait for the prompt after a run command; return the Stop."""
        start = self.run_start if since is None else since
        m = self.out.wait(re.escape(PROMPT), start, timeout)
        self.running = False
        self.mark = m.end()
        text = self.out.text(start, m.start())
        t = self.out.arrival(m.start())
        stops = list(STOP_RE.finditer(text))
        if not stops:
            return Stop("", None, None, text.decode("latin-1"), t)
        s = stops[-1]
        return Stop(s.group("why").decode("latin-1").strip(), int(s.group("pc"), 8),
                    (s.group("insn") or b"").decode("latin-1"), text.decode("latin-1"), t)

    def first_output_time(self, since):
        """Arrival time of the first byte written after offset `since`."""
        self.out.wait(rb".", since, 10)
        return self.out.arrival(since)

    # ---- process ------------------------------------------------------------

    def cpu_seconds(self):
        with open("/proc/%d/stat" % self.proc.pid) as f:
            fields = f.read().rsplit(")", 1)[1].split()
        return (int(fields[11]) + int(fields[12])) / os.sysconf("SC_CLK_TCK")

    def thread_cpu_ns(self):
        """On-CPU nanoseconds of the main thread (the one that runs
        sim_instr), from schedstat: unaffected by preemption; None if the
        kernel does not provide it."""
        try:
            with open("/proc/%d/task/%d/schedstat" % (self.proc.pid, self.proc.pid)) as f:
                return int(f.read().split()[0])
        except (OSError, ValueError, IndexError):
            return None

    def interrupt(self):
        os.kill(self.proc.pid, signal.SIGINT)

    def close(self):
        if self.proc.poll() is None:
            try:
                if not self.running:
                    self._write("quit")
                    self.proc.wait(3)
            except Exception:
                pass
        if self.proc.poll() is None:
            self.proc.kill()
            try:
                self.proc.wait(5)
            except Exception:
                pass
        if self.console:
            self.console.close()
        _live.discard(self.proc)
