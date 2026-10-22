"""Reader/writer for the PANEL export file (simh/panel/pdp11_panel_shm.h).

The offsets mirror the C header; tests.test_build cross-checks them against
the header with the host C compiler.

The header words are accessed through ctypes views, i.e. with single aligned
32-bit loads and stores like the bridge's sync/atomic operations.
struct.pack_into must not be used for them: it zeroes the bytes before it
writes the value, and the simulator takes any halt_req != halt_ack, even a
transient 0, as a halt request."""

import collections
import ctypes
import mmap
import struct
import time

MAGIC = 0x50313150
VERSION = 3
RING = 16384
SIZE = 256 + 16 * RING

OFF = {
    "magic": 0, "version": 4, "size": 8, "ring_size": 12,
    "ring_wr": 16, "state": 20, "seq": 24, "stop_count": 28,
    "r": 32, "psw": 64, "dr": 68, "mmr0": 72, "mmr3": 76,
    "last_pa": 80, "last_data": 84, "sim_time": 88,
    "sample_interval": 96, "samples_per_sec": 100,
    "sr": 104, "sr_valid": 108, "halt_req": 112, "target_rate": 116,
    "trap_count": 120, "halt_ack": 124, "pc_pa": 128, "instr_count": 136,
    "dr_on": 144, "weight_sum": 208, "ring": 256,
}

PA_MASK = 0x003FFFFF
WR = 1 << 22
BYTE = 1 << 23
IFETCH = 1 << 24
V_MODE = 25
WAIT = 1 << 27
MMU = 1 << 28
M22 = 1 << 29
DSPACE = 1 << 30
IOPAGE = 1 << 31

ST_HALTED, ST_RUNNING, ST_WAIT = 0, 1, 2

Sample = collections.namedtuple("Sample", "addr data va weight pc ir")
_SAMPLE = struct.Struct("<IHHIHH")
assert _SAMPLE.size == 16


def mode(addr):
    return (addr >> V_MODE) & 3


def pa(addr):
    return addr & PA_MASK


class Panel:
    def __init__(self, path):
        self.path = path
        self.f = open(path, "r+b")
        self.m = mmap.mmap(self.f.fileno(), SIZE)
        self.words = (ctypes.c_uint32 * (OFF["ring"] // 4)).from_buffer(self.m)

    def close(self):
        del self.words                          # release the buffer export
        self.m.close()
        self.f.close()

    def u32(self, name):
        return self.words[OFF[name] // 4]

    def w32(self, name, value):
        self.words[OFF[name] // 4] = value & 0xFFFFFFFF

    def u64(self, name):
        lo, hi = self.words[OFF[name] // 4], self.words[OFF[name] // 4 + 1]
        return lo | (hi << 32)

    def header_ok(self):
        return (self.u32("magic"), self.u32("version"), self.u32("size"),
                self.u32("ring_size")) == (MAGIC, VERSION, SIZE, RING)

    def dr_time(self):
        """DISPLAY REGISTER accumulators: (dr_on[0..15], weight_sum), us
        modulo 2^32 (differences of two reads give the per-bit duty)."""
        base = OFF["dr_on"] // 4
        return [self.words[base + i] for i in range(16)], self.u32("weight_sum")

    def regs(self, timeout=1.0):
        """Register block, read under the seqlock."""
        deadline = time.monotonic() + timeout
        while True:
            s1 = self.u32("seq")
            if not s1 & 1:
                r = struct.unpack_from("<8I", self.m, OFF["r"])
                psw, dr, mmr0, mmr3, last_pa, last_data = struct.unpack_from("<6I", self.m, OFF["psw"])
                sim_time, = struct.unpack_from("<d", self.m, OFF["sim_time"])
                interval, sps = struct.unpack_from("<2I", self.m, OFF["sample_interval"])
                pc_pa = self.u32("pc_pa")
                icount = self.u64("instr_count")
                if self.u32("seq") == s1:
                    return dict(r=r, psw=psw, dr=dr, mmr0=mmr0, mmr3=mmr3, last_pa=last_pa,
                                last_data=last_data, sim_time=sim_time, interval=interval,
                                sps=sps, pc_pa=pc_pa, instr_count=icount, seq=s1)
            if time.monotonic() > deadline:
                raise TimeoutError("seqlock: seq stays odd or changing (%d)" % s1)

    # bridge side of the protocol (what pdp11d does)

    def set_sr(self, value, valid=True):
        self.w32("sr", value)
        self.w32("sr_valid", 1 if valid else 0)

    def request_halt(self):
        """halt_req = halt_ack + 1; returns the time of the store."""
        self.w32("halt_req", self.u32("halt_ack") + 1)
        return time.monotonic()

    def cancel_halt(self):
        self.w32("halt_req", self.u32("halt_ack"))

    def set_target_rate(self, n):
        self.w32("target_rate", n)

    def reader(self):
        return RingReader(self)


class RingReader:
    """Consumes the ring like the bridge: entries [last, ring_wr)."""

    def __init__(self, panel):
        self.p = panel
        self.last = panel.u32("ring_wr")
        self.lost = 0

    def read(self):
        wr = self.p.u32("ring_wr")
        n = (wr - self.last) & 0xFFFFFFFF
        if n > RING // 2:                       # fell behind: skip ahead
            skip = n - RING // 2
            self.lost += skip
            self.last = (self.last + skip) & 0xFFFFFFFF
            n = RING // 2
        out = []
        i = self.last % RING
        while n > 0:
            k = min(n, RING - i)
            base = OFF["ring"] + 16 * i
            out.extend(Sample._make(t) for t in _SAMPLE.iter_unpack(self.p.m[base:base + 16 * k]))
            n -= k
            i = 0
        self.last = wr
        return out

    def collect(self, seconds, interval=0.002):
        """Read continuously for `seconds`; return all samples."""
        out = []
        end = time.monotonic() + seconds
        while time.monotonic() < end:
            time.sleep(interval)
            out.extend(self.read())
        out.extend(self.read())
        return out
