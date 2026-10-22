#!/usr/bin/env python3
"""Boot matrix for the pdp11-emu system catalog (systems/<id>/).

Run with ``python3 -I tests/systems/bootmatrix.py [options]`` (standard library
only).  For every selected system it

1. finds the system's archives in the cache directories (size + sha256; a
   pdp11d cache, the research download tree, ...), downloads only what is
   missing (url, then mirrors; 3 attempts each) and extracts the listed files
   into sparse working copies (sha256 verified);
2. writes ``run.ini`` exactly like pdp11d (docs/DESIGN.md section 6: generated
   header, boot.ini with ${DISKn} expanded, optional terminal attach line);
3. starts the simulator the way pdp11d does: ``pdp11 run.ini`` with cwd and
   HOME = the run directory and stdin/stdout pipes, frames SCP replies on the
   ``sim> `` prompt and maps start-up diagnostics to boot.ini lines;
4. connects to the console over raw TCP, maps the panel export when the
   binary has the PANEL device, writes ``dialog.do`` (noexpect, nosend,
   dialog.ini) and sends ``do dialog.do`` and the boot command;
5. waits for ``readyPattern``, runs ``tests.session``, measures idle host CPU,
   types ``shutdownScript`` and verifies a clean halt and ``quit``;
6. optionally (``--tty``) appends pdp11d's ``attach -u [-m] <dev> <listener>``
   line, performs the OS-side steps from TTY_PROCEDURES and logs in over a
   telnet connection to the extra terminal line;
7. optionally (``--clock``) checks on the Unix systems that the guest clock
   keeps time: ``date``, CLOCK_SECS of wall time, ``date``.

``readyPattern`` and every ``expect`` of a step are literal, case-sensitive
substrings of the console output (like SimH EXPECT without -r).

Examples::

    python3 -I tests/systems/bootmatrix.py --simh simh/out/pdp11 \\
        --cache /opt/pdp11/cache --work /tmp/matrix
    python3 -I tests/systems/bootmatrix.py --system rt11 --boots 3 --second-boot
    python3 -I tests/systems/bootmatrix.py --system bsd211 --tty

The exit status is 0 when every run passed.
"""

from __future__ import annotations

import argparse
import ctypes
import dataclasses
import gzip
import hashlib
import json
import lzma
import mmap
import os
import posixpath
import queue
import re
import shutil
import signal
import socket
import struct
import subprocess
import sys
import tarfile
import threading
import time
import urllib.error
import urllib.request
import zipfile
from typing import IO, Callable, Optional

REPO = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", ".."))
DEFAULT_CATALOG = os.path.join(REPO, "systems")
DEFAULT_SIMH = os.path.join(REPO, "simh", "out", "pdp11")
DEFAULT_WORK = os.path.join(REPO, "tests", "systems", "out")

PROMPT = b"sim> "
BLOCK = 4096
STEP_TIMEOUT = 30.0          # catalog.Step default
INPUT_BURST = 32             # pdp11d: first bytes of a burst are written at once ...
INPUT_CPS = 100              # ... the rest is paced at InputCPS
HALT_WAIT_AFTER_SCRIPT = 30.0   # PANEL_LOGIC.md "shutdownOS": wait for the CPU to halt
SETTLE_AFTER_SCRIPT = 3.0       # ... or, if the script ends without a halt, 3 s more
ANSWER_DELAY = 0.25             # ... and the pause between an expect match and the next send
CLOCK_SECS = 20.0               # --clock: wall time between the two "date" commands

# DESIGN.md section 6 "stdout framing".
STOP_RE = re.compile(r"^(?P<why>.*), PC: (?P<pc>[0-7]+)(?: \((?P<insn>.*)\))?$")
ECHO_RE = re.compile(r"^(?P<file>/.*)-(?P<line>\d+)> (?P<cmd>.*)$")
SCP_NOISE = ("%SIM-INFO", "Resetting all devices...", "The GO and CONTINUE commands do not reset devices.")
# Informational start-up output, never a power-on error (DESIGN.md section 6
# step 4, simh.isInfo): SCP_NOISE, the notices of "attach -m dz", speed and
# CPU model changes, and "Sockets: ..." (a failed listener is always followed
# by its %SIM-ERROR, which still counts).
INFO_LINES = ("Modem control activated", "Modem control deactivated", "Idling disabled", "Throttling disabled")
INFO_PREFIXES = SCP_NOISE + ("Disabling ", "Sockets: ")


def is_info(line: str) -> bool:
    return line in INFO_LINES or line.startswith(INFO_PREFIXES)

# Panel export (simh/panel/pdp11_panel_shm.h). Version 3 only adds words in
# the reserved part of the header; the fields used here are the same in both.
SHM_MAGIC = 0x50313150
SHM_VERSIONS = (2, 3)
SHM_SIZE = 262400
OFF_MAGIC = 0                 # magic, version, size: three uint32
OFF_SR, OFF_SR_VALID = 104, 108
OFF_HALT_REQ, OFF_HALT_ACK = 112, 124


class HarnessError(Exception):
    """A run failed; the message says why."""


# ---------------------------------------------------------------------------
# Catalog
# ---------------------------------------------------------------------------

@dataclasses.dataclass
class Step:
    expect: str = ""
    send: str = ""
    timeout: float = STEP_TIMEOUT

    @classmethod
    def from_json(cls, d: dict) -> "Step":
        return cls(d.get("expect", ""), d.get("send", ""), float(d.get("timeoutSec") or STEP_TIMEOUT))


@dataclasses.dataclass
class FileSpec:
    path: str
    role: str
    sha256: str
    size: int
    sparse: bool


@dataclasses.dataclass
class Artifact:
    url: str
    mirrors: list[str]
    sha256: str
    size: int
    format: str
    files: list[FileSpec]


@dataclasses.dataclass
class System:
    id: str
    name: str
    memory: str
    speed: str
    boot: str
    ready: str
    boot_timeout: int
    terminals: Optional[dict]
    artifacts: list[Artifact]
    session: list[Step]
    shutdown: list[Step]
    boot_ini: str
    dialog: list[str]
    raw: dict

    @classmethod
    def load(cls, directory: str) -> "System":
        with open(os.path.join(directory, "system.json"), encoding="utf-8") as f:
            j = json.load(f)
        with open(os.path.join(directory, "boot.ini"), encoding="utf-8") as f:
            boot_ini = f.read()
        dialog: list[str] = []
        dpath = os.path.join(directory, "dialog.ini")
        if os.path.exists(dpath):
            with open(dpath, encoding="utf-8") as f:
                for line in f:
                    t = line.strip()
                    if t and not t.startswith((";", "#")):
                        dialog.append(t)
        arts = []
        for a in j["artifacts"]:
            files = [FileSpec(x["path"], x["role"], x["sha256"], int(x["size"]), bool(x.get("sparse")))
                     for x in a["files"]]
            arts.append(Artifact(a["url"], list(a.get("mirrors") or []), a["sha256"], int(a["size"]),
                                 a["format"], files))
        tests = j.get("tests") or {}
        return cls(
            id=j["id"], name=j["name"], memory=j["cpu"]["memory"], speed=j["speed"], boot=j["boot"],
            ready=j["readyPattern"], boot_timeout=int(j.get("bootTimeoutSec") or 60),
            terminals=j.get("terminals"), artifacts=arts,
            session=[Step.from_json(s) for s in tests.get("session") or []],
            shutdown=[Step.from_json(s) for s in j.get("shutdownScript") or []],
            boot_ini=boot_ini, dialog=dialog, raw=j)


def load_catalog(directory: str) -> dict[str, System]:
    systems = {}
    for name in sorted(os.listdir(directory)):
        d = os.path.join(directory, name)
        if os.path.isfile(os.path.join(d, "system.json")):
            s = System.load(d)
            systems[s.id] = s
    return dict(sorted(systems.items(), key=lambda kv: (kv[1].raw.get("order", 0), kv[0])))


# ---------------------------------------------------------------------------
# Artifacts: cache lookup, download, extraction into sparse working copies
# ---------------------------------------------------------------------------

def sha256_file(path: str) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


class ArtifactStore:
    """Finds archives by size + sha256 in read-only caches; downloads missing ones."""

    def __init__(self, caches: list[str], download_dir: str, log: Callable[[str], None]):
        self.caches = [c for c in caches if os.path.isdir(c)]
        self.download_dir = download_dir
        self.log = log
        self._index: Optional[dict[int, list[str]]] = None
        self._hashes: dict[tuple[str, int, float], str] = {}

    def _scan(self) -> dict[int, list[str]]:
        if self._index is None:
            idx: dict[int, list[str]] = {}
            for root in self.caches + [self.download_dir]:
                for dirpath, _dirs, names in os.walk(root):
                    for n in names:
                        p = os.path.join(dirpath, n)
                        try:
                            st = os.stat(p)
                        except OSError:
                            continue
                        if os.path.isfile(p):
                            idx.setdefault(st.st_size, []).append(p)
            self._index = idx
        return self._index

    def _hash(self, path: str) -> str:
        st = os.stat(path)
        key = (path, st.st_size, st.st_mtime)
        if key not in self._hashes:
            self._hashes[key] = sha256_file(path)
        return self._hashes[key]

    def find(self, art: Artifact) -> Optional[str]:
        direct = os.path.join(self.download_dir, art.sha256)
        if os.path.isfile(direct) and os.path.getsize(direct) == art.size and self._hash(direct) == art.sha256:
            return direct
        for p in self._scan().get(art.size, []):
            if self._hash(p) == art.sha256:
                return p
        return None

    def get(self, art: Artifact) -> str:
        p = self.find(art)
        if p:
            return p
        return self.download(art)

    def download(self, art: Artifact) -> str:
        os.makedirs(self.download_dir, exist_ok=True)
        final = os.path.join(self.download_dir, art.sha256)
        part = final + ".part"
        tried = []
        for url in [art.url] + art.mirrors:
            for attempt, backoff in enumerate((2, 8, None)):
                tried.append(url)
                try:
                    self.log("download %s (attempt %d)" % (url, attempt + 1))
                    self._fetch(url, part, art.size)
                    got, have = sha256_file(part), os.path.getsize(part)
                    if have != art.size or got != art.sha256:
                        os.unlink(part)
                        raise HarnessError("size/sha256 mismatch (%d bytes, sha256 %s)" % (have, got))
                    os.replace(part, final)
                    self._index = None
                    return final
                except (OSError, urllib.error.URLError, HarnessError) as e:
                    self.log("  failed: %s" % e)
                    if backoff:
                        time.sleep(backoff)
        raise HarnessError("cannot fetch artifact %s (%d bytes, sha256 %s); tried %s"
                           % (posixpath.basename(art.url), art.size, art.sha256, ", ".join(dict.fromkeys(tried))))

    @staticmethod
    def _fetch(url: str, part: str, size: int) -> None:
        have = os.path.getsize(part) if os.path.exists(part) else 0
        if have > size:
            os.unlink(part)
            have = 0
        req = urllib.request.Request(url, headers={"User-Agent": "pdp11-emu-bootmatrix/1"})
        if have and url.startswith(("http://", "https://")):
            req.add_header("Range", "bytes=%d-" % have)
        with urllib.request.urlopen(req, timeout=60) as r:
            mode = "ab" if have and getattr(r, "status", 200) == 206 else "wb"
            with open(part, mode) as out:
                shutil.copyfileobj(r, out, 1 << 20)


def norm_member(name: str) -> str:
    """Archive member names are compared after normalisation ("./a" == "a")."""
    n = posixpath.normpath(name.replace("\\", "/"))
    return n.lstrip("/")


def write_sparse(src: IO[bytes], dst_path: str, expect_size: int) -> str:
    """Copy src to dst_path, skipping all-zero 4 KiB blocks; return the sha256 of the content."""
    h = hashlib.sha256()
    written = 0
    zero = bytes(BLOCK)
    with open(dst_path, "wb") as out:
        while True:
            chunk = src.read(1 << 20)
            if not chunk:
                break
            h.update(chunk)
            for i in range(0, len(chunk), BLOCK):
                b = chunk[i:i + BLOCK]
                if b == zero[:len(b)]:
                    out.seek(len(b), os.SEEK_CUR)
                else:
                    out.write(b)
            written += len(chunk)
            if written > expect_size:
                raise HarnessError("%s: more than the expected %d bytes" % (dst_path, expect_size))
        out.truncate(written)
    if written != expect_size:
        raise HarnessError("%s: %d bytes, expected %d" % (dst_path, written, expect_size))
    return h.hexdigest()


def extract(archive: str, art: Artifact, outdir: str) -> dict[str, str]:
    """Extract the artifact's listed files into outdir; return normalised member path -> file."""
    os.makedirs(outdir, exist_ok=True)
    wanted = {norm_member(f.path): f for f in art.files}
    out: dict[str, str] = {}

    def emit(spec: FileSpec, stream: IO[bytes]) -> None:
        dst = os.path.join(outdir, posixpath.basename(norm_member(spec.path)))
        if os.path.exists(dst):
            os.unlink(dst)
        got = write_sparse(stream, dst, spec.size)
        if got != spec.sha256:
            raise HarnessError("%s: sha256 %s, expected %s" % (spec.path, got, spec.sha256))
        out[norm_member(spec.path)] = dst

    fmt = art.format
    if fmt in ("raw", "gz", "xz"):
        if len(art.files) != 1:
            raise HarnessError("format %s must list exactly one file" % fmt)
        opener = {"raw": lambda p: open(p, "rb"), "gz": gzip.open, "xz": lzma.open}[fmt]
        with opener(archive) as s:
            emit(art.files[0], s)
    elif fmt == "zip":
        with zipfile.ZipFile(archive) as z:
            for info in z.infolist():
                spec = wanted.get(norm_member(info.filename))
                if spec and not info.is_dir():
                    with z.open(info) as s:
                        emit(spec, s)
    elif fmt in ("tgz", "tar", "tarZ"):
        proc = None
        if fmt == "tarZ":
            # compress(1) data: gzip decodes it (DESIGN.md section 4 runtime tools).
            proc = subprocess.Popen(["gzip", "-dc", archive], stdout=subprocess.PIPE)
            tf = tarfile.open(fileobj=proc.stdout, mode="r|")
        else:
            tf = tarfile.open(archive, mode="r|gz" if fmt == "tgz" else "r|")
        try:
            for m in tf:
                spec = wanted.get(norm_member(m.name))
                if spec and m.isfile():
                    s = tf.extractfile(m)
                    assert s is not None
                    emit(spec, s)
        finally:
            tf.close()
            if proc:
                proc.stdout.close()  # type: ignore[union-attr]
                proc.wait()
    else:
        raise HarnessError("unknown format %r" % fmt)
    missing = [f.path for f in art.files if norm_member(f.path) not in out]
    if missing:
        raise HarnessError("not found in %s: %s" % (posixpath.basename(art.url), ", ".join(missing)))
    return out


def sparse_copy(src: str, dst: str) -> None:
    """Copy a (sparse) file, preserving holes and skipping all-zero 4 KiB blocks.

    Reads with pread at explicit offsets (SEEK_DATA/SEEK_HOLE move the fd offset,
    so a buffered reader on the same fd would read from the wrong place)."""
    size = os.path.getsize(src)
    zero = bytes(BLOCK)
    fdi = os.open(src, os.O_RDONLY)
    try:
        with open(dst, "wb") as fo:
            pos = 0
            while pos < size:
                try:
                    data = os.lseek(fdi, pos, os.SEEK_DATA)
                except OSError:          # ENXIO: only a hole remains
                    break
                try:
                    hole = os.lseek(fdi, data, os.SEEK_HOLE)
                except OSError:
                    hole = size
                off = data
                while off < hole:
                    chunk = os.pread(fdi, min(hole - off, 1 << 20), off)
                    if not chunk:
                        break
                    for i in range(0, len(chunk), BLOCK):
                        b = chunk[i:i + BLOCK]
                        if b != zero[:len(b)]:
                            fo.seek(off + i)
                            fo.write(b)
                    off += len(chunk)
                pos = hole
            fo.truncate(size)
    finally:
        os.close(fdi)


class Images:
    """Pristine extracted images (one extraction per artifact) and per-run working copies."""

    def __init__(self, store: ArtifactStore, pristine_dir: str, log: Callable[[str], None]):
        self.store = store
        self.pristine_dir = pristine_dir
        self.log = log

    def pristine(self, system: System) -> dict[str, str]:
        roles: dict[str, str] = {}
        for art in system.artifacts:
            d = os.path.join(self.pristine_dir, art.sha256)
            stamp = os.path.join(d, ".complete")
            if os.path.exists(stamp):
                with open(stamp) as f:
                    got = json.load(f)
            else:
                archive = self.store.get(art)
                self.log("extract %s (%s)" % (posixpath.basename(art.url), archive))
                shutil.rmtree(d, ignore_errors=True)
                got = extract(archive, art, d)
                with open(stamp, "w") as f:
                    json.dump(got, f)
            for f in art.files:
                roles[f.role] = got[norm_member(f.path)]
        return roles

    def working_copies(self, system: System, disk_dir: str) -> dict[str, str]:
        """Fresh sparse working copies of every file, sha256-verified (like an install)."""
        os.makedirs(disk_dir, exist_ok=True)
        specs = {f.role: f for a in system.artifacts for f in a.files}
        roles = {}
        for role, src in self.pristine(system).items():
            dst = os.path.join(disk_dir, os.path.basename(src))
            sparse_copy(src, dst)
            got = sha256_file(dst)
            if got != specs[role].sha256:
                raise HarnessError("working copy %s: sha256 %s, expected %s" % (dst, got, specs[role].sha256))
            roles[role] = dst
        return roles


# ---------------------------------------------------------------------------
# run.ini (DESIGN.md section 6, step 2)
# ---------------------------------------------------------------------------

def speed_lines(speed: str) -> list[str]:
    if speed == "fast":
        return ["set nothrottle", "set cpu idle"]
    if speed == "authentic":
        return ["set throttle 1M"]
    raise HarnessError("speed %r" % speed)


def header_lines(memory: str, speed: str, console_port: int, panel: Optional[str]) -> list[str]:
    lines = ["; generated by pdp11d - do not edit", "set cpu 11/70", "set cpu %s" % memory]
    lines += speed_lines(speed)
    lines += ["set console -u telnet=127.0.0.1:%d;notelnet" % console_port,
              "set console telnet=buffered=65536",
              "set tto 7b"]
    if panel:
        lines.append("attach panel %s" % panel)
    return lines


def expand(text: str, roles: dict[str, str]) -> str:
    def sub(m: re.Match) -> str:
        if m.group(1) not in roles:
            raise HarnessError("boot.ini uses undefined ${%s}" % m.group(1))
        return roles[m.group(1)]
    return re.sub(r"\$\{([A-Z0-9_]+)\}", sub, text)


def tty_attach_line(term: dict, listener: str) -> str:
    modem = " -m" if term.get("modem") and term["device"] == "dz" else ""
    return "attach -u%s %s %s" % (modem, term["device"], listener)


def make_run_ini(system: System, roles: dict[str, str], console_port: int, panel: Optional[str],
                 speed: str, tty_listener: Optional[str]) -> tuple[str, int]:
    head = header_lines(system.memory, speed, console_port, panel)
    body = expand(system.boot_ini, roles)
    text = "\n".join(head) + "\n" + body
    if not text.endswith("\n"):
        text += "\n"
    if tty_listener and system.terminals:
        text += tty_attach_line(system.terminals, tty_listener) + "\n"
    return text, len(head)


# ---------------------------------------------------------------------------
# Byte streams with waits
# ---------------------------------------------------------------------------

class Timeout(HarnessError):
    pass


class ByteLog:
    """Growing byte buffer fed by a reader thread; supports waiting for literals."""

    def __init__(self, name: str, tee: Optional[IO[bytes]] = None):
        self.name = name
        self.buf = bytearray()
        self.cv = threading.Condition()
        self.closed = False
        self.tee = tee

    def feed(self, data: bytes) -> None:
        with self.cv:
            if data:
                self.buf += data
                if self.tee:
                    self.tee.write(data)
                    self.tee.flush()
            else:
                self.closed = True
            self.cv.notify_all()

    def size(self) -> int:
        with self.cv:
            return len(self.buf)

    def text(self, start: int = 0) -> str:
        with self.cv:
            return bytes(self.buf[start:]).decode("latin-1")

    def find(self, needle: bytes, start: int, timeout: float, abort: Optional[Callable[[], Optional[str]]] = None) -> int:
        """Wait until needle occurs at or after start; return the offset just past it."""
        deadline = time.monotonic() + timeout
        with self.cv:
            while True:
                i = self.buf.find(needle, start)
                if i >= 0:
                    return i + len(needle)
                if abort:
                    why = abort()
                    if why:
                        raise HarnessError("%s while waiting for %r on %s" % (why, needle, self.name))
                left = deadline - time.monotonic()
                if left <= 0 or self.closed:
                    tail = bytes(self.buf[max(start, len(self.buf) - 300):])
                    raise Timeout("%s: %r not seen within %.0f s%s; tail %r"
                                  % (self.name, needle, timeout, " (stream closed)" if self.closed else "", tail))
                self.cv.wait(min(left, 0.25))

    def wait_prompt(self, start: int, timeout: float) -> bytes:
        """Wait until the stream tail (after start) ends with the SCP prompt; return the reply."""
        deadline = time.monotonic() + timeout
        with self.cv:
            while True:
                if len(self.buf) - start >= len(PROMPT) and self.buf.endswith(PROMPT):
                    return bytes(self.buf[start:-len(PROMPT)])
                left = deadline - time.monotonic()
                if left <= 0 or self.closed:
                    raise Timeout("%s: no prompt within %.0f s%s; tail %r"
                                  % (self.name, timeout, " (simulator exited)" if self.closed else "",
                                     bytes(self.buf[max(start, len(self.buf) - 300):])))
                self.cv.wait(min(left, 0.25))

    def prompt_at_tail(self, start: int) -> bool:
        with self.cv:
            return len(self.buf) - start >= len(PROMPT) and self.buf.endswith(PROMPT)


def reader_thread(read: Callable[[int], bytes], log: ByteLog) -> threading.Thread:
    def run() -> None:
        while True:
            try:
                d = read(65536)
            except OSError:
                d = b""
            log.feed(d)
            if not d:
                return
    t = threading.Thread(target=run, daemon=True, name="reader-" + log.name)
    t.start()
    return t


# ---------------------------------------------------------------------------
# Simulator process, SCP channel, panel export
# ---------------------------------------------------------------------------

def _pdeathsig() -> None:
    """Child: SIGTERM when the harness dies (pdp11d uses Pdeathsig the same way)."""
    try:
        libc = ctypes.CDLL(None, use_errno=True)
        libc.prctl(1, signal.SIGTERM, 0, 0, 0)    # PR_SET_PDEATHSIG
    except Exception:
        pass


def cpu_seconds(pid: int) -> float:
    with open("/proc/%d/stat" % pid) as f:
        fields = f.read().rsplit(")", 1)[1].split()
    return (int(fields[11]) + int(fields[12])) / os.sysconf("SC_CLK_TCK")


def has_panel_device(binary: str, cwd: str) -> bool:
    """True if the simulator has the PANEL device of our patch (SIMH_PATCH.md)."""
    r = subprocess.run([binary], input=b"show panel\nquit\n", stdout=subprocess.PIPE,
                       stderr=subprocess.STDOUT, timeout=20, cwd=cwd)
    return b"Non-existent device" not in r.stdout and b"PANEL" in r.stdout


class Panel:
    """The halt handshake of the panel export (pdp11_panel_shm.h)."""

    def __init__(self, path: str):
        self.f = open(path, "r+b")
        self.m = mmap.mmap(self.f.fileno(), SHM_SIZE)
        magic, version, size = struct.unpack_from("<III", self.m, OFF_MAGIC)
        if magic != SHM_MAGIC or version not in SHM_VERSIONS or size != SHM_SIZE:
            self.m.close()
            self.f.close()
            raise HarnessError("panel export %s: magic %#x version %d size %d (want version %s, %d bytes)"
                               % (path, magic, version, size, " or ".join(map(str, SHM_VERSIONS)), SHM_SIZE))
        self.req = ctypes.c_uint32.from_buffer(self.m, OFF_HALT_REQ)
        self.ack = ctypes.c_uint32.from_buffer(self.m, OFF_HALT_ACK)
        self.sr = ctypes.c_uint32.from_buffer(self.m, OFF_SR)
        self.sr_valid = ctypes.c_uint32.from_buffer(self.m, OFF_SR_VALID)

    def set_switches(self, value: int) -> None:
        """What programs read at 17777570 (pdp11d writes it at every power-on)."""
        self.sr.value = value & 0o177777
        self.sr_valid.value = 1

    def cancel(self) -> None:
        self.req.value = self.ack.value

    def request_halt(self) -> None:
        self.req.value = (self.ack.value + 1) & 0xFFFFFFFF

    def close(self) -> None:
        del self.req, self.ack, self.sr, self.sr_valid
        self.m.close()
        self.f.close()


@dataclasses.dataclass
class StopInfo:
    reason: str
    pc: str
    insn: str


def parse_stop(text: str) -> Optional[StopInfo]:
    found = None
    for line in text.replace("\r\n", "\n").split("\n"):
        m = STOP_RE.match(line.rstrip("\r"))
        if m:
            found = StopInfo(m.group("why"), m.group("pc"), m.group("insn") or "")
    return found


def startup_diagnostics(output: str, ini_path: str, header: int) -> tuple[list[str], list[str]]:
    """Map SimH start-up output to run.ini lines (DESIGN.md section 6 step 4).

    Returns (errors, infos): every line after an echo '<ini>-<n>> <cmd>' up to the
    next echo or prompt is a diagnostic of that line; all but the informational
    ones (is_info) are errors.  No boot.ini uses ECHO, so its output (which
    pdp11d recognises by the text of the file's echo lines) is not handled."""
    errors, infos = [], []
    cur: Optional[tuple[int, str]] = None
    for raw in output.replace("\r\n", "\n").split("\n"):
        line = raw.rstrip("\r")
        m = ECHO_RE.match(line)
        if m and m.group("file") == ini_path:
            cur = (int(m.group("line")), m.group("cmd"))
            continue
        if cur is None or not line.strip():
            continue
        n, cmd = cur
        where = "boot.ini line %d" % (n - header) if n > header else "header line %d" % n
        msg = "%s (%s): %s" % (where, cmd, line)
        (infos if is_info(line) else errors).append(msg)
    return errors, infos


class Simulator:
    """pdp11 on stdin/stdout pipes, like internal/simh."""

    def __init__(self, binary: str, ini: str, rundir: str, log: IO[bytes]):
        env = dict(os.environ, HOME=rundir)
        self.proc = subprocess.Popen([binary, ini], cwd=rundir, env=env, stdin=subprocess.PIPE,
                                     stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                     start_new_session=True, preexec_fn=_pdeathsig, bufsize=0)
        self.out = ByteLog("simh-stdout", log)
        assert self.proc.stdout is not None
        reader_thread(self.proc.stdout.read, self.out)
        self.running = False
        self.run_start = 0
        self.run_time = 0.0
        self.panel: Optional[Panel] = None

    @property
    def pid(self) -> int:
        return self.proc.pid

    def alive(self) -> bool:
        return self.proc.poll() is None

    def first_prompt(self, timeout: float = 15.0) -> str:
        return self.out.wait_prompt(0, timeout).decode("latin-1")

    def command(self, cmd: str, timeout: float = 10.0) -> str:
        if self.running:
            raise HarnessError("SCP command %r while running" % cmd)
        start = self.out.size()
        self._write(cmd + "\n")
        return self.out.wait_prompt(start, timeout).decode("latin-1")

    def _write(self, s: str) -> None:
        assert self.proc.stdin is not None
        self.proc.stdin.write(s.encode("latin-1"))
        self.proc.stdin.flush()

    def run(self, cmd: str) -> None:
        """Send a run command (boot/cont/...); cancel a stale halt request first."""
        if self.panel:
            self.panel.cancel()
        self.run_start = self.out.size()
        self.run_time = time.monotonic()
        self._write(cmd + "\n")
        self.running = True

    def stopped(self) -> bool:
        return self.running and self.out.prompt_at_tail(self.run_start)

    def wait_stop(self, timeout: float) -> StopInfo:
        reply = self.out.wait_prompt(self.run_start, timeout).decode("latin-1")
        self.running = False
        if self.panel:
            self.panel.cancel()
        return parse_stop(reply) or StopInfo("", "", "")

    def halt(self, timeout: float = 2.0) -> StopInfo:
        """Front-panel halt via the panel export (DESIGN.md "Halt handshake").

        Without the PANEL device (stock SimH) the harness falls back to SIGINT, which
        pdp11d never uses: a SIGINT that arrives before SimH re-entered sim_instr is
        lost (run_cmd clears stop_cpu), and one at the prompt makes SimH exit. So the
        fallback waits until the run command is at least 0.5 s old and retries."""
        if not self.running:
            raise HarnessError("halt while not running")
        if self.stopped():
            return self.wait_stop(1)
        if self.panel:
            self.panel.request_halt()
            try:
                return self.wait_stop(timeout)
            except Timeout as e:
                raise HarnessError("simulator not responding to halt: %s" % e) from None
        time.sleep(max(0.0, 0.5 - (time.monotonic() - self.run_time)))
        for _ in range(3):
            if self.stopped():
                break
            os.kill(self.proc.pid, signal.SIGINT)
            try:
                return self.wait_stop(timeout)
            except Timeout:
                continue
        if self.stopped():
            return self.wait_stop(1)
        raise HarnessError("simulator not responding to SIGINT")

    def quit(self, timeout: float = 3.0) -> Optional[int]:
        if self.running:
            raise HarnessError("quit while running")
        try:
            self._write("quit\n")
            assert self.proc.stdin is not None
            self.proc.stdin.close()
        except OSError:
            pass
        try:
            return self.proc.wait(timeout)
        except subprocess.TimeoutExpired:
            return None

    def kill(self) -> None:
        if self.panel:
            try:
                self.panel.close()
            except Exception:
                pass
            self.panel = None
        if self.proc.poll() is None:
            for sig, wait in ((signal.SIGTERM, 2), (signal.SIGKILL, 5)):
                try:
                    os.killpg(self.proc.pid, sig)
                except ProcessLookupError:
                    break
                try:
                    self.proc.wait(wait)
                    break
                except subprocess.TimeoutExpired:
                    continue


# ---------------------------------------------------------------------------
# Console and telnet lines
# ---------------------------------------------------------------------------

class PacedWriter:
    """pdp11d's console input path: the first INPUT_BURST bytes of a burst at once, then INPUT_CPS."""

    def __init__(self, sock: socket.socket):
        self.sock = sock
        self.q: "queue.Queue[Optional[bytes]]" = queue.Queue()
        self.cv = threading.Condition()
        self.pending = 0
        self.t = threading.Thread(target=self._run, daemon=True, name="paced-writer")
        self.t.start()

    def write(self, data: bytes) -> None:
        with self.cv:
            self.pending += len(data)
        self.q.put(data)

    def drain(self, timeout: float = 60) -> None:
        with self.cv:
            self.cv.wait_for(lambda: self.pending == 0, timeout)

    def close(self) -> None:
        self.q.put(None)

    def _sent(self, n: int) -> None:
        with self.cv:
            self.pending -= n
            if self.pending == 0:
                self.cv.notify_all()

    def _run(self) -> None:
        burst = 0
        while True:
            try:
                data = self.q.get(timeout=0.05)
            except queue.Empty:
                burst = 0               # idle: the next write starts a new burst
                continue
            if data is None:
                return
            for i, b in enumerate(data):
                if burst >= INPUT_BURST:
                    time.sleep(1.0 / INPUT_CPS)
                try:
                    self.sock.sendall(bytes([b]))
                except OSError:
                    self._sent(len(data) - i)
                    return
                burst += 1
                self._sent(1)


def connect_tcp(port: int, total: float, every: float = 0.2) -> socket.socket:
    deadline = time.monotonic() + total
    while True:
        try:
            s = socket.create_connection(("127.0.0.1", port), timeout=2)
            s.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
            s.settimeout(None)
            return s
        except OSError:
            if time.monotonic() >= deadline:
                raise HarnessError("cannot connect to 127.0.0.1:%d" % port) from None
            time.sleep(every)


class Line:
    """A guest serial line over TCP (the console, raw; or a telnet line)."""

    def __init__(self, name: str, port: int, log: IO[bytes], telnet: bool = False, connect_secs: float = 3.0):
        self.sock = connect_tcp(port, connect_secs)
        self.telnet = telnet
        self._iac_rest = b""
        self.out = ByteLog(name, log)
        self.writer = PacedWriter(self.sock)
        self.mark = 0
        reader_thread(self._recv, self.out)

    def _recv(self, n: int) -> bytes:
        while True:
            d = self.sock.recv(n)
            if not d:
                return b""
            if self.telnet:
                d = self._strip_iac(d)
                if not d:
                    continue            # only telnet negotiation: keep reading
            return bytes(b & 0x7F for b in d)

    TELNET_OK = (0, 1, 3)               # BINARY, ECHO, SUPPRESS-GO-AHEAD

    def _strip_iac(self, d: bytes) -> bytes:
        """Remove telnet commands (SimH's listener sends WILL/DO options on connect)."""
        d = self._iac_rest + d
        self._iac_rest = b""
        out = bytearray()
        i = 0
        while i < len(d):
            c = d[i]
            if c != 255:
                out.append(c)
                i += 1
                continue
            if i + 1 >= len(d):
                self._iac_rest = d[i:]
                break
            cmd = d[i + 1]
            if cmd == 255:                  # escaped 0xFF data byte
                out.append(255)
                i += 2
            elif cmd in (251, 252, 253, 254):   # WILL WONT DO DONT <option>
                if i + 2 >= len(d):
                    self._iac_rest = d[i:]
                    break
                opt = d[i + 2]
                if cmd in (251, 253):
                    ok = opt in self.TELNET_OK
                    reply = (253 if ok else 254) if cmd == 251 else (251 if ok else 252)
                    try:
                        self.sock.sendall(bytes([255, reply, opt]))
                    except OSError:
                        pass
                i += 3
            else:
                i += 2
        return bytes(out)

    def send(self, text: str) -> None:
        self.writer.write(text.encode("latin-1"))

    def expect(self, needle: str, timeout: float, abort: Optional[Callable[[], Optional[str]]] = None) -> None:
        """Wait for needle in output produced since the previous expect/send boundary."""
        self.mark = self.out.find(needle.encode("latin-1"), self.mark, timeout, abort)

    def boundary(self) -> None:
        """Later expects only see output produced from now on."""
        self.mark = self.out.size()

    def close(self) -> None:
        self.writer.close()
        try:
            self.sock.shutdown(socket.SHUT_RDWR)
        except OSError:
            pass
        self.sock.close()


def free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


# ---------------------------------------------------------------------------
# OS-side procedures for the extra terminal lines (--tty)
# ---------------------------------------------------------------------------

@dataclasses.dataclass
class TtyProcedure:
    """console: steps typed on the console after the session (enable the lines);
    line: steps on the telnet connection (log in, check, log out);
    after_hangup: with modem control, a session left open is hung up when the telnet
    connection closes: log in, drop the connection, reconnect and run these steps."""
    console: list[Step]
    line: list[Step]
    after_hangup: Optional[list[Step]] = None


def _steps(*pairs: tuple[str, str]) -> list[Step]:
    return [Step(e, s) for e, s in pairs]


# Ctrl+D ends the login shell ("exit" does not end 2.11BSD's login sh on these lines).
BSD_TTY_LINE = _steps(("login: ", "root\r"), ("# ", "tty\r"), ("/dev/tty00", ""), ("# ", "\004"), ("login: ", ""),
                      ("", "root\r"), ("# ", ""))
BSD_AFTER_HANGUP = _steps(("login: ", ""))

TTY_PROCEDURES: dict[str, TtyProcedure] = {
    # pl482 already runs gettys on tty00-tty07 (on in /etc/ttys).
    "bsd211": TtyProcedure(console=[], line=BSD_TTY_LINE, after_hangup=BSD_AFTER_HANGUP),
    # pl499 ships tty00-tty07 "off": switch them on and tell init (the enableHint).
    "bsd211-pl499": TtyProcedure(
        console=_steps(("", "sed -e '/^tty0[0-7]/s/off/on/' /etc/ttys > /tmp/ttys && cp /tmp/ttys /etc/ttys && kill -HUP 1 && echo TTYS-ON\r"),
                       ("TTYS-ON\r\n", "")),
        line=BSD_TTY_LINE, after_hangup=BSD_AFTER_HANGUP),
    # RSX-11M-PLUS V3.0: the DZ lines are TT2:-TT11:, answered by MCR.
    "rsx11mp": TtyProcedure(
        console=[],
        line=_steps(("", "\r"), (">", "HELLO SYSTEM\r"), ("Password:", "SYSTEM\r"), (">", "TIM\r"),
                    ("-", ""), (">", "BYE\r"), ("logged off", ""))),
}


# ---------------------------------------------------------------------------
# Guest clock (--clock)
# ---------------------------------------------------------------------------

CLOCK_RE = re.compile(r"\b(\d\d):(\d\d):(\d\d)\b")


def clock_seconds(text: str) -> Optional[int]:
    """The time of day that date printed (its HH:MM:SS), in seconds."""
    m = CLOCK_RE.search(text)
    if not m:
        return None
    h, mi, se = (int(g) for g in m.groups())
    return h * 3600 + mi * 60 + se


def clock_ok(guest: int, wall: float) -> bool:
    """Whether the guest clock kept time: within 10 % of the wall time, plus 1 s
    for date's resolution (a 50 Hz kernel under a 60 Hz clock gains 20 %)."""
    return abs(guest - wall) <= 1.0 + 0.1 * wall


# ---------------------------------------------------------------------------
# One boot
# ---------------------------------------------------------------------------

@dataclasses.dataclass
class Result:
    system: str
    run: int
    boot: int
    ok: bool = False
    phase: str = "prepare"
    error: str = ""
    boot_secs: Optional[float] = None
    session_secs: Optional[float] = None
    idle_cpu_pct: Optional[float] = None
    shutdown_secs: Optional[float] = None
    halt: str = ""
    exit_code: Optional[int] = None
    tty: str = ""
    clock: str = ""
    diagnostics: list[str] = dataclasses.field(default_factory=list)
    log: str = ""


class Runner:
    def __init__(self, args: argparse.Namespace, images: Images, panel_capable: bool):
        self.args = args
        self.images = images
        self.panel_capable = panel_capable
        self.tty_port = 0

    def say(self, msg: str) -> None:
        if not self.args.quiet:
            print("  " + msg, flush=True)

    def run(self, system: System, run_no: int, boot_no: int, disk_dir: str, fresh: bool) -> Result:
        res = Result(system.id, run_no, boot_no)
        rundir = os.path.join(os.path.dirname(disk_dir), "run%d" % boot_no)
        shutil.rmtree(rundir, ignore_errors=True)
        os.makedirs(rundir)
        res.log = os.path.join(rundir, "transcript.log")
        sim: Optional[Simulator] = None
        lines: list[Line] = []
        with open(res.log, "wb") as log:
            try:
                if fresh:
                    shutil.rmtree(disk_dir, ignore_errors=True)
                    roles = self.images.working_copies(system, disk_dir)
                else:
                    roles = {f.role: os.path.join(disk_dir, posixpath.basename(norm_member(f.path)))
                             for a in system.artifacts for f in a.files}
                    for p in roles.values():
                        if not os.path.exists(p):
                            raise HarnessError("working copy %s missing" % p)
                sim, console = self._power_on(system, roles, rundir, log, res)
                lines.append(console)
                self._boot(system, sim, console, rundir, res)
                if not self.args.no_session:
                    self._session(system, sim, console, res)
                if self.args.clock and system.raw.get("category") == "unix":
                    self._clock(sim, console, res)
                if self.args.tty:
                    self._tty(system, sim, console, log, res, lines)
                self._idle(sim, res)
                self._shutdown(system, sim, console, res)
                res.ok = True
                res.phase = "done"
            except (HarnessError, OSError, subprocess.SubprocessError) as e:
                res.error = str(e)
                log.write(("\n[[harness] FAILED in %s: %s]\n" % (res.phase, e)).encode())
            finally:
                for ln in lines:
                    ln.close()
                if sim:
                    sim.kill()
        return res

    # -- phases -------------------------------------------------------------

    def _power_on(self, system: System, roles: dict[str, str], rundir: str, log: IO[bytes],
                  res: Result) -> tuple[Simulator, Line]:
        res.phase = "power-on"
        port = free_port()
        panel_path = os.path.join(rundir, "panel.shm") if self.panel_capable else None
        tty = None
        if self.args.tty and system.terminals:
            self.tty_port = free_port()
            tty = self.args.tty_listener.replace("PORT", str(self.tty_port))
        speed = self.args.speed or system.speed
        text, header = make_run_ini(system, roles, port, panel_path, speed, tty)
        ini = os.path.join(rundir, "run.ini")
        with open(ini, "w") as f:
            f.write(text)
        log.write(b"[[harness] run.ini]\n" + text.encode() + b"[[harness] simulator output]\n")
        sim = Simulator(self.args.simh, ini, rundir, log)
        try:
            out = sim.first_prompt(15)
        except Timeout:
            sim.kill()
            raise
        errors, infos = startup_diagnostics(out, ini, header)
        res.diagnostics = errors
        allowed = [e for e in errors if any(re.search(p, e) for p in self.args.allow_diag)]
        fatal = [e for e in errors if e not in allowed]
        if fatal:
            sim.kill()
            raise HarnessError("power-on error(s): " + " | ".join(fatal))
        if panel_path:
            sim.panel = Panel(panel_path)
            sim.panel.set_switches(self.args.sr)
        console = Line("console", port, log, connect_secs=3.0)
        return sim, console

    def _boot(self, system: System, sim: Simulator, console: Line, rundir: str, res: Result) -> None:
        res.phase = "boot"
        with open(os.path.join(rundir, "dialog.do"), "w") as f:
            f.write("noexpect\nnosend\n" + "".join(l + "\n" for l in system.dialog))
        reply = sim.command("do dialog.do")
        bad = [l for l in reply.replace("\r", "").split("\n") if l.strip() and not l.startswith(SCP_NOISE)]
        if bad:
            raise HarnessError("do dialog.do: " + " | ".join(bad))
        t0 = time.monotonic()
        console.boundary()
        sim.run(system.boot)
        timeout = self.args.boot_timeout or max(120.0, 3.0 * system.boot_timeout)
        console.expect(system.ready, timeout, abort=lambda: self._unexpected_stop(sim))
        res.boot_secs = round(time.monotonic() - t0, 2)
        self.say("%s ready after %.1f s" % (system.id, res.boot_secs))

    @staticmethod
    def _unexpected_stop(sim: Simulator) -> Optional[str]:
        if sim.stopped():
            st = parse_stop(sim.out.text(sim.run_start))
            return "CPU stopped (%s)" % ("%s, PC %s" % (st.reason, st.pc) if st else "no stop line")
        if not sim.alive():
            return "simulator exited"
        return None

    def _steps(self, steps: list[Step], sim: Simulator, line: Line, what: str, answer_delay: float = 0.0) -> None:
        """Types steps; answer_delay is the pause before the first send after an
        expect matched (pdp11d's shutdown scripts: ANSWER_DELAY)."""
        answer = False
        for i, st in enumerate(steps):
            if st.expect:
                try:
                    line.expect(st.expect, st.timeout, abort=lambda: self._unexpected_stop(sim))
                except HarnessError as e:
                    raise HarnessError("%s step %d: %s" % (what, i + 1, e)) from None
                answer = True
            if st.send:
                if answer and answer_delay > 0:
                    time.sleep(answer_delay)
                answer = False
                line.boundary()
                line.send(st.send)

    def _session(self, system: System, sim: Simulator, console: Line, res: Result) -> None:
        res.phase = "session"
        t0 = time.monotonic()
        self._steps(system.session, sim, console, "tests.session")
        console.writer.drain()
        res.session_secs = round(time.monotonic() - t0, 2)

    def _clock(self, sim: Simulator, console: Line, res: Result) -> None:
        """At the shell prompt the session left: date, CLOCK_SECS, date."""
        res.phase = "clock"
        secs: list[int] = []
        at: list[float] = []
        for i in range(2):
            if i:
                time.sleep(CLOCK_SECS)
            console.boundary()
            start = console.mark
            at.append(time.monotonic())
            console.send("date\r")
            console.expect("# ", STEP_TIMEOUT, abort=lambda: self._unexpected_stop(sim))
            t = clock_seconds(console.out.text(start)[:console.mark - start])
            if t is None:
                raise HarnessError("no time in the output of date: %r" % console.out.text(start)[:200])
            secs.append(t)
        guest = (secs[1] - secs[0]) % 86400
        wall = at[1] - at[0]
        res.clock = "+%d s in %.1f s" % (guest, wall)
        self.say("%s: guest clock %s" % (res.system, res.clock))
        if not clock_ok(guest, wall):
            raise HarnessError("the guest clock advanced %d s in %.1f s of wall time" % (guest, wall))

    def _idle(self, sim: Simulator, res: Result) -> None:
        res.phase = "idle"
        secs = self.args.idle_secs
        if secs <= 0:
            return
        time.sleep(1.0)
        a = cpu_seconds(sim.pid)
        time.sleep(secs)
        b = cpu_seconds(sim.pid)
        if sim.stopped():
            raise HarnessError("CPU stopped while idle")
        res.idle_cpu_pct = round(100.0 * (b - a) / secs, 1)

    def _shutdown(self, system: System, sim: Simulator, console: Line, res: Result) -> None:
        res.phase = "shutdown"
        t0 = time.monotonic()
        console.boundary()
        steps = system.shutdown
        if steps:
            self._steps(steps, sim, console, "shutdownScript", answer_delay=ANSWER_DELAY)
            console.writer.drain()
        ends_with_send = bool(steps and steps[-1].send)
        stop: Optional[StopInfo] = None
        if ends_with_send:
            # The OS is expected to halt by itself (e.g. "HALT instruction").
            try:
                stop = sim.wait_stop(HALT_WAIT_AFTER_SCRIPT)
            except Timeout:
                raise HarnessError("CPU did not halt within %.0f s after the shutdown script" % HALT_WAIT_AFTER_SCRIPT) from None
        else:
            time.sleep(SETTLE_AFTER_SCRIPT if steps else 0.5)
            if sim.stopped():
                stop = sim.wait_stop(1)
            else:
                stop = sim.halt()
        res.halt = "%s, PC %s" % (stop.reason, stop.pc) if stop.reason else "(no stop line)"
        if ends_with_send and stop.reason != "HALT instruction":
            raise HarnessError("unexpected stop after shutdown: %s" % res.halt)
        res.shutdown_secs = round(time.monotonic() - t0, 2)
        res.phase = "quit"
        code = sim.quit()
        res.exit_code = code
        if code != 0:
            raise HarnessError("simulator did not quit cleanly (exit status %r)" % code)

    def _tty(self, system: System, sim: Simulator, console: Line, log: IO[bytes], res: Result,
             lines: list[Line]) -> None:
        res.phase = "tty"
        proc = TTY_PROCEDURES.get(system.id)
        if not system.terminals:
            res.tty = "n/a"
            return
        if not proc:
            res.tty = "no procedure"
            return
        console.boundary()
        self._steps(proc.console, sim, console, "tty enable")
        console.writer.drain()
        line = Line("tty", self.tty_port, log, telnet=True, connect_secs=5.0)
        lines.append(line)
        self._steps(proc.line, sim, line, "tty line")
        line.writer.drain()
        time.sleep(1.0)
        res.tty = "login ok"
        if proc.after_hangup is not None:
            # The line still has a logged-in shell: drop the connection (carrier
            # loss with modem control) and expect a fresh login on reconnection.
            line.close()
            time.sleep(2.0)
            line = Line("tty", self.tty_port, log, telnet=True, connect_secs=5.0)
            lines.append(line)
            self._steps(proc.after_hangup, sim, line, "tty after hang-up")
            res.tty = "login ok, hang-up ok"
        self.say("%s: %s on the telnet line" % (system.id, res.tty))


# ---------------------------------------------------------------------------
# Matrix
# ---------------------------------------------------------------------------

def print_matrix(results: list[Result]) -> None:
    cols = ("system", "run", "boot", "result", "boot s", "session s", "idle CPU %", "shutdown s", "halt", "exit", "tty",
            "clock", "error")
    rows = []
    for r in results:
        rows.append((r.system, str(r.run), str(r.boot), "PASS" if r.ok else "FAIL",
                     "" if r.boot_secs is None else "%.1f" % r.boot_secs,
                     "" if r.session_secs is None else "%.1f" % r.session_secs,
                     "" if r.idle_cpu_pct is None else "%.1f" % r.idle_cpu_pct,
                     "" if r.shutdown_secs is None else "%.1f" % r.shutdown_secs,
                     r.halt, "" if r.exit_code is None else str(r.exit_code), r.tty, r.clock,
                     ("%s: %s" % (r.phase, r.error))[:140] if r.error else ""))
    print_table(cols, rows)

    # Per system: pass count and ranges over all boots.
    def span(vals: list[float]) -> str:
        if not vals:
            return ""
        lo, hi = min(vals), max(vals)
        return "%.1f" % lo if lo == hi else "%.1f-%.1f" % (lo, hi)

    summary = []
    for sid in dict.fromkeys(r.system for r in results):
        rs = [r for r in results if r.system == sid]
        summary.append((sid, "%d/%d" % (sum(r.ok for r in rs), len(rs)),
                        span([r.boot_secs for r in rs if r.boot_secs is not None]),
                        span([r.idle_cpu_pct for r in rs if r.idle_cpu_pct is not None]),
                        span([r.shutdown_secs for r in rs if r.shutdown_secs is not None])))
    print_table(("system", "passed", "boot s", "idle CPU %", "shutdown s"), summary)
    passed = sum(r.ok for r in results)
    print("\n%d of %d runs passed" % (passed, len(results)))


def print_table(cols: tuple[str, ...], rows: list[tuple[str, ...]]) -> None:
    widths = [max([len(c)] + [len(row[i]) for row in rows]) for i, c in enumerate(cols)]
    fmt = "  ".join("%%-%ds" % w for w in widths)
    print()
    print(fmt % cols)
    print(fmt % tuple("-" * w for w in widths))
    for row in rows:
        print(fmt % row)


def main(argv: Optional[list[str]] = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0],
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--catalog", default=DEFAULT_CATALOG, help="catalog directory (default: %(default)s)")
    ap.add_argument("--simh", default=DEFAULT_SIMH, help="simulator binary (default: %(default)s)")
    ap.add_argument("--cache", action="append", default=[], help="read-only archive cache, searched recursively (repeatable)")
    ap.add_argument("--work", default=DEFAULT_WORK, help="work directory (downloads, images, runs)")
    ap.add_argument("--system", action="append", default=[], help="system id (repeatable; default: all)")
    ap.add_argument("--boots", type=int, default=1, help="independent runs per system, each from a fresh working copy")
    ap.add_argument("--second-boot", action="store_true", help="after each run, boot a fresh simulator process on the same disks again")
    ap.add_argument("--speed", choices=("fast", "authentic"), help="override the catalog speed")
    ap.add_argument("--idle-secs", type=float, default=10.0, help="idle host CPU measurement window (0 = skip)")
    ap.add_argument("--boot-timeout", type=float, default=0.0, help="seconds to wait for readyPattern (default: max(120, 3 x bootTimeoutSec))")
    ap.add_argument("--no-session", action="store_true", help="skip tests.session (shutdownScript then starts from the state right after boot)")
    ap.add_argument("--tty", action="store_true", help="also verify a telnet login on the extra terminal lines")
    ap.add_argument("--tty-listener", default="127.0.0.1:PORT", help="listener spec passed to attach (PORT = a free port)")
    ap.add_argument("--clock", action="store_true",
                    help="Unix systems: after the session, check that the guest clock keeps time (date, %.0f s, date)" % CLOCK_SECS)
    ap.add_argument("--allow-diag", action="append", default=[], help="regex of start-up diagnostics not treated as power-on errors")
    ap.add_argument("--no-panel", action="store_true", help="do not attach the panel export even if the binary has it")
    ap.add_argument("--sr", type=lambda v: int(v, 8), default=0, help="switch register (octal) written at power-on, like pdp11d")
    ap.add_argument("--json", help="write the results as JSON to this file")
    ap.add_argument("--quiet", action="store_true")
    args = ap.parse_args(argv)
    if args.clock and args.no_session:
        ap.error("--clock needs the session (it types date at the shell prompt the session leaves)")

    if not os.access(args.simh, os.X_OK):
        ap.error("simulator %s not found (build it with simh/build.sh or pass --simh)" % args.simh)
    args.simh = os.path.abspath(args.simh)
    catalog = load_catalog(args.catalog)
    ids = args.system or list(catalog)
    unknown = [i for i in ids if i not in catalog]
    if unknown:
        ap.error("unknown system(s): %s (catalog: %s)" % (", ".join(unknown), ", ".join(catalog)))

    work = os.path.abspath(args.work)
    os.makedirs(work, exist_ok=True)
    log = (lambda m: None) if args.quiet else (lambda m: print("  " + m, flush=True))
    store = ArtifactStore([os.path.abspath(c) for c in args.cache], os.path.join(work, "cache"), log)
    images = Images(store, os.path.join(work, "pristine"), log)
    panel = (not args.no_panel) and has_panel_device(args.simh, work)
    print("simulator %s (%s)" % (args.simh, "PANEL export attached" if panel else "no PANEL device: halts use SIGINT"), flush=True)
    runner = Runner(args, images, panel)

    results: list[Result] = []
    for sid in ids:
        system = catalog[sid]
        for run_no in range(1, args.boots + 1):
            disk_dir = os.path.join(work, "runs", sid, "r%d" % run_no, "disks")
            boots = 2 if args.second_boot else 1
            for boot_no in range(1, boots + 1):
                print("%s run %d boot %d" % (sid, run_no, boot_no), flush=True)
                r = runner.run(system, run_no, boot_no, disk_dir, fresh=(boot_no == 1))
                results.append(r)
                print("  -> %s%s" % ("PASS" if r.ok else "FAIL", "" if r.ok else " (%s: %s)" % (r.phase, r.error)), flush=True)
                if not r.ok:
                    break
    print_matrix(results)
    if args.json:
        with open(args.json, "w") as f:
            json.dump([dataclasses.asdict(r) for r in results], f, indent=1)
    return 0 if results and all(r.ok for r in results) else 1


if __name__ == "__main__":
    sys.exit(main())
