"""Performance (docs/SIMH_PATCH.md): cost of the hooks, host CPU while idle."""

import os
import statistics
import sys
import time
import unittest

from simtest import config, panel, progs, simh
from simtest.config import CFG

N_SOB = 100000000           # instructions per benchmark run
N_MIX = 60000000
LIMIT_DETACHED = 0.03       # slowdown against the unpatched baseline
LIMIT_ATTACHED = 0.10
LIMIT_IDLE_CPU = 0.10       # host CPU share in a WAIT loop, SET CPU IDLE=2
IDLE_MARGIN = 0.03          # ... or at most this much above the unpatched run


def pin(sim, cpu):
    try:
        os.sched_setaffinity(sim.proc.pid, {cpu})
    except (AttributeError, OSError):
        pass


class TestPerf(unittest.TestCase):

    def setUp(self):
        if CFG.quick:
            self.skipTest("--quick")

    def start(self, name, binary, attach, setup=(), console=False):
        wd = config.workdir("perf-" + name.split()[0])
        shm = os.path.join(wd, "panel.shm")
        lines = ["set cpu 11/70", "set cpu 4M"] + list(setup)
        for prog in ("SOB_LOOP", "COPY_MIX", "BLINKER"):
            lines += progs.deposits(progs.program(prog))
        sim = simh.Sim(binary, wd, lines, console=console, panel=shm if attach else None,
                       name=name.split()[0])
        self.addCleanup(sim.close)
        p = None
        if attach:
            p = panel.Panel(shm)
            self.addCleanup(p.close)
        return sim, p

    def test_overhead(self):
        """SOB loop and memory-copy loop: median MIPS of interleaved runs,
        measured with the on-CPU time of the simulator thread (wall time when
        the kernel has no schedstat), all simulators on the same host CPU."""
        variants = [("detached", CFG.bin, False), ("attached 50k/s", CFG.bin, True)]
        if CFG.baseline:
            variants.insert(0, ("baseline", CFG.baseline, False))
        cpu = max(os.sched_getaffinity(0)) if hasattr(os, "sched_getaffinity") else None
        rigs = []
        for name, binary, attach in variants:
            sim, p = self.start(name, binary, attach)
            if cpu is not None:
                pin(sim, cpu)                           # same core for every run
            rigs.append((name, sim, p))
        sob, mix = progs.program("SOB_LOOP"), progs.program("COPY_MIX")
        loops = [("SOB loop", sob["SOB0"], N_SOB), ("copy loop", mix["MIX"], N_MIX)]
        mips = {(n, l): [] for n, s, p in rigs for l, a, k in loops}
        wall = {(n, l): [] for n, s, p in rigs for l, a, k in loops}
        for r in range(CFG.rounds):
            for name, sim, p in rigs:
                for loop, addr, n in loops:
                    if p:
                        p.cancel_halt()
                    sim.command("deposit pc %o" % addr)
                    c = sim.thread_cpu_ns()
                    t = time.monotonic()
                    sim.start("step %d" % n)
                    stop = sim.wait_stop(120)
                    self.assertEqual(stop.why, "Step expired")
                    dt = stop.time - t
                    if c is not None:
                        dt = (sim.thread_cpu_ns() - c) / 1e9
                    mips[(name, loop)].append(n / dt / 1e6)
                    wall[(name, loop)].append(n / (stop.time - t) / 1e6)
        med = {k: statistics.median(v) for k, v in mips.items()}
        wmed = {k: statistics.median(v) for k, v in wall.items()}
        ref = "baseline" if CFG.baseline else "detached"
        clock = "on-CPU" if rigs[0][1].thread_cpu_ns() is not None else "wall"
        lines = ["overhead, median of %d runs, %s time (MIPS; vs %s; wall-time MIPS in brackets):"
                 % (CFG.rounds, clock, ref)]
        failures = []
        for name, sim, p in rigs:
            cells = []
            for loop, a, k in loops:
                slow = 1 - med[(name, loop)] / med[(ref, loop)]
                cells.append("%s %.1f (%+.1f%%) [%.1f]" % (loop, med[(name, loop)], -100 * slow,
                                                       wmed[(name, loop)]))
                limit = LIMIT_ATTACHED if p else LIMIT_DETACHED
                if name != ref and slow > limit:
                    failures.append("%s %s: %.1f%% slower (limit %.0f%%)"
                                    % (name, loop, 100 * slow, 100 * limit))
            if p:
                regs = p.regs()
                cells.append("%d samples/s, interval %d" % (regs["sps"], regs["interval"]))
            lines.append("  %-15s %s" % (name, ", ".join(cells)))
        if not CFG.baseline:
            lines.append("  (no baseline: %s)" % (CFG.baseline_note or "not built"))
        config.report("\n    ".join(lines))
        self.assertFalse(failures, failures)

    def test_idle_host_cpu(self):
        """WAIT loop with SET CPU IDLE=2 and the export attached: <= 10 % of a
        host CPU, or (on a host where the unpatched simulator already needs
        more) at most 3 points more than the unpatched simulator measured at
        the same time.  Throttled at 1 MIPS for comparison."""
        runs = [("attached, idle", CFG.bin, True, ["set cpu idle=2"]),
                ("%s, idle" % ("baseline" if CFG.baseline else "detached"),
                 CFG.baseline or CFG.bin, False, ["set cpu idle=2"]),
                ("attached, throttle 1M", CFG.bin, True, ["set throttle 1M"])]
        rigs = []
        for name, binary, attach, setup in runs:
            sim, p = self.start(name, binary, attach, setup + ["deposit system sr 100000"])
            if p:
                p.set_sr(0o100000)
                p.cancel_halt()
            sim.start("go %o" % progs.program("BLINKER")["START"])
            rigs.append((name, sim, p))
        time.sleep(4.0)                                 # idle needs a stable clock
        c0 = [s.cpu_seconds() for n, s, p in rigs]
        t0 = time.monotonic()
        time.sleep(8.0)
        dt = time.monotonic() - t0
        share = [(s.cpu_seconds() - c) / dt for (n, s, p), c in zip(rigs, c0)]
        for name, sim, p in rigs:
            if p:
                self.assertEqual(p.u32("state"), panel.ST_WAIT)
                p.request_halt()
                self.assertEqual(sim.wait_stop(2).why, "Front panel HALT")
            else:
                sim.interrupt()
                sim.wait_stop(2)
        config.report("host CPU in a WAIT loop: " + ", ".join(
            "%s %.1f%%" % (n, 100 * c) for (n, s, p), c in zip(rigs, share)))
        self.assertLessEqual(share[0], max(LIMIT_IDLE_CPU, share[1] + IDLE_MARGIN))


if __name__ == "__main__":
    sys.exit("run via run_tests.py")
