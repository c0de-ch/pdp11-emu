"""Functional tests of the PANEL export (docs/SIMH_PATCH.md, "Functional")."""

import collections
import os
import random
import statistics
import sys
import time
import unittest

from simtest import config, panel, progs, simh
from simtest.config import CFG
from simtest.panel import (BYTE, DSPACE, IFETCH, IOPAGE, M22, MMU, WAIT, WR,
                           ST_HALTED, ST_RUNNING, ST_WAIT, mode, pa)

BASE = ["set cpu 11/70", "set cpu 4M"]
FPH = "Front panel HALT"
TICK = 1 / 60.0                                 # KW11-L line clock


class Rig:
    """One simulator with a program loaded and the export attached, driven
    with the bridge's rules (cancel before every run command)."""

    def __init__(self, test, name, programs=(), setup=(), extra=(), attach=True,
                 console=True, binary=None):
        self.test = test
        self.wd = config.workdir(name)
        self.shm = os.path.join(self.wd, "panel.shm")
        lines = BASE + list(setup)
        for prog in programs:
            lines += progs.deposits(prog)
        lines += list(extra)
        self.sim = simh.Sim(binary or CFG.bin, self.wd, lines, console=console,
                            panel=self.shm if attach else None, name=name)
        test.addCleanup(self.sim.close)
        self.p = None
        if attach:
            self.p = panel.Panel(self.shm)
            test.addCleanup(self.p.close)

    def run(self, cmd):
        self.p.cancel_halt()
        return self.sim.start(cmd)

    def halt(self, timeout=2.0):
        """Request a halt; return (Stop, latency from the request)."""
        t = self.p.request_halt()
        stop = self.sim.wait_stop(timeout)
        self.test.assertEqual(stop.why, FPH, stop.text)
        return stop, stop.time - t

    def wait_state(self, want, timeout=5.0):
        end = time.monotonic() + timeout
        while self.p.u32("state") != want:
            if time.monotonic() > end:
                self.test.fail("state %d, want %d" % (self.p.u32("state"), want))
            time.sleep(0.002)


def blinker():
    return progs.program("BLINKER")


class TestPanel(unittest.TestCase):

    # ---- layout, state, ring ------------------------------------------------

    def test_header_and_registers_after_attach(self):
        prog = blinker()
        rig = Rig(self, "header", [prog], extra=["deposit pc 1000", "set panel refresh"])
        p = rig.p
        self.assertTrue(p.header_ok())
        self.assertEqual(p.u32("state"), ST_HALTED)
        self.assertEqual(p.u32("halt_ack"), p.u32("halt_req"))
        self.assertEqual(p.u32("seq") & 1, 0)
        self.assertEqual((p.u32("sr_valid"), p.u32("target_rate")), (0, 0))
        regs = p.regs()
        self.assertEqual(regs["r"][7], 0o1000)
        self.assertEqual(regs["pc_pa"], 0o1000)
        self.assertEqual(regs["interval"], 1000)

    def test_ring_advances_only_while_running(self):
        rig = Rig(self, "ring", [blinker()])
        p = rig.p
        wr0 = p.u32("ring_wr")
        time.sleep(0.3)
        self.assertEqual(p.u32("ring_wr"), wr0, "samples while halted (before the first run)")
        t0 = time.monotonic()
        rig.run("go 1000")
        time.sleep(0.5)
        self.assertEqual(p.u32("state"), ST_RUNNING)
        wr1 = p.u32("ring_wr")
        rate = (wr1 - wr0) / (time.monotonic() - t0)
        self.assertGreater(rate, 10000)
        rig.halt()
        self.assertEqual(p.u32("state"), ST_HALTED)
        wr2 = p.u32("ring_wr")
        time.sleep(0.3)
        self.assertEqual(p.u32("ring_wr"), wr2, "samples while halted")
        config.report("ring: %.0f samples/s while running, none while halted" % rate)

    def check_weights(self, rig, seconds, label):
        r = rig.p.reader()
        t0 = time.monotonic()
        rig.run("go 1000")
        samples = r.collect(seconds)
        stop, _ = rig.halt()
        samples += r.read()
        self.assertEqual(r.lost, 0, "the reader fell behind")
        wall = stop.time - t0
        total = sum(s.weight for s in samples) / 1e6
        self.assertLessEqual(abs(total - wall), 0.10 * wall,
                             "weights %.3f s, wall %.3f s" % (total, wall))
        self.assertTrue(all(s.weight >= 1 for s in samples))
        config.report("weights (%s): sum %.3f s over %.3f s of wall time (%+.1f%%), %d samples"
                      % (label, total, wall, 100 * (total / wall - 1), len(samples)))
        return samples

    def test_weights_sum_to_wall_time_busy(self):
        rig = Rig(self, "weights-busy", [blinker()])
        self.check_weights(rig, 1.0, "busy")

    def test_weights_sum_to_wall_time_idle(self):
        rig = Rig(self, "weights-idle", [blinker()], setup=["set cpu idle=2"])
        rig.p.set_sr(0o100000)
        samples = self.check_weights(rig, 3.0, "WAIT, idle on")
        self.assertTrue(any(s.addr & WAIT for s in samples))

    def frame_duty(self, rig, seconds, lamp):
        """Read the ring every 1/60 s like the lamp engine; return the
        per-frame duty (0..255) of lamp(sample), the samples and the wall
        time read."""
        r = rig.p.reader()
        r.read()
        duties, samples = [], []
        t0 = time.monotonic()
        nxt = t0
        while time.monotonic() - t0 < seconds:
            nxt += 1 / 60.0
            time.sleep(max(0.0, nxt - time.monotonic()))
            frame = r.read()
            w = sum(s.weight for s in frame)
            if w:
                duties.append(255.0 * sum(s.weight for s in frame if lamp(s)) / w)
                samples += frame
        self.assertEqual(r.lost, 0, "the reader fell behind")
        return duties, samples, time.monotonic() - t0

    def wait_throttled(self, rig, mips=2.0, timeout=15.0):
        """Wait until the CPU runs below `mips` (SimH starts throttling
        after three calibration seconds)."""
        end = time.monotonic() + timeout
        ic, t = rig.p.regs()["instr_count"], time.monotonic()
        while True:
            time.sleep(0.25)
            ic2, t2 = rig.p.regs()["instr_count"], time.monotonic()
            if (ic2 - ic) / (t2 - t) < mips * 1e6:
                return
            self.assertLess(t2, end, "the throttle did not start")
            ic, t = ic2, t2

    def test_weights_throttled(self):
        """Throttled (SimH runs ~1000 instructions at host speed, then sleeps
        a millisecond) the weights stand for the work, not for the wall time
        since the previous sample: a lamp lit at a fifth of the instruction
        boundaries (MASTER) is as steady from frame to frame as unthrottled,
        and the weights still add up to the wall time."""
        prog = progs.program("IO_FIFTH")
        master = lambda s: s.addr & IOPAGE and not s.addr & WAIT
        got = {}
        for speed in ("set nothrottle", "set throttle 1M"):
            rig = Rig(self, "weights-" + speed.split()[-1], [prog], setup=[speed])
            rig.run("go 1000")
            if speed != "set nothrottle":
                self.wait_throttled(rig)
            time.sleep(0.5)                             # one adaptation window
            duties, samples, wall = self.frame_duty(rig, 2.0, master)
            rig.halt()
            got[speed] = (statistics.mean(duties), statistics.pstdev(duties))
            weights = sorted((s.weight for s in samples), reverse=True)
            top = sum(weights[:len(weights) // 50]) / sum(weights)
            config.report("weights, %s: MASTER per frame %.1f +- %.1f (of 255, %d frames); the heaviest "
                          "2%% of the samples carry %.1f%% of the weight; weights %.3f s in %.3f s"
                          % (speed, got[speed][0], got[speed][1], len(duties), 100 * top,
                             sum(weights) / 1e6, wall))
            self.assertLess(top, 0.10, "a few samples carry the weight")
            self.assertLessEqual(abs(sum(weights) / 1e6 - wall), 0.05 * wall)
            self.assertLessEqual(abs(got[speed][0] - 51), 10)
        self.assertLessEqual(got["set throttle 1M"][1], 2 * got["set nothrottle"][1] + 1)

    # ---- WAIT samples -------------------------------------------------------

    def check_wait_samples(self, rig, prog, samples, pc_va, pc_phys, m, mapped):
        waits = [s for s in samples if s.addr & WAIT]
        self.assertTrue(waits, "no WAIT samples")
        for s in waits:
            self.assertEqual(s.va, pc_va)
            self.assertEqual(s.pc, pc_va)
            self.assertEqual(pa(s.addr), pc_phys)
            self.assertEqual(s.data, 0o12345)          # R0
            self.assertEqual(s.ir, 0o000001)
            self.assertEqual(mode(s.addr), m)
            self.assertEqual(bool(s.addr & MMU), mapped)
            self.assertEqual(bool(s.addr & M22), mapped)
            self.assertFalse(s.addr & (WR | BYTE | IFETCH | DSPACE | IOPAGE))
        return waits

    def test_wait_samples_idle_on(self):
        prog = blinker()
        rig = Rig(self, "wait-idle", [prog], setup=["set cpu idle=2"])
        p = rig.p
        p.set_sr(0o100000)                              # WAIT loop
        rig.run("go 1000")
        time.sleep(3.0)                                 # idling needs a stable clock (2 s)
        rig.wait_state(ST_WAIT)
        regs0 = p.regs()
        samples = p.reader().collect(1.0)
        waits = self.check_wait_samples(rig, prog, samples, prog["WAITPC"], prog["WAITPC"], 0, False)
        regs = p.regs()
        self.assertEqual(regs["r"][0], 0o12345)
        self.assertEqual(regs["r"][7], prog["WAITPC"])
        self.assertEqual(regs["pc_pa"], prog["WAITPC"])
        self.assertGreater(regs["sim_time"], regs0["sim_time"])  # refreshed at WAIT samples
        # one sample after every sleep (it carries the time slept), plus
        # countdown samples while sim_idle spins out the rest of each tick
        slept = [s.weight for s in waits if s.weight >= 2000]
        self.assertTrue(30 <= len(slept) <= 130, len(slept))
        self.assertLessEqual(len(samples), 2 * 50000)
        share = sum(slept) / 1e6
        self.assertGreater(share, 0.5)
        rig.halt()
        config.report("WAIT, idle on: %d sleep samples/s (median %.1f ms, %.0f%% of the time), "
                      "%d spin WAIT samples/s, %d instruction samples/s"
                      % (len(slept), statistics.median(slept) / 1000, 100 * share,
                         len(waits) - len(slept), len(samples) - len(waits)))

    def test_wait_samples_throttled(self):
        prog = blinker()
        rig = Rig(self, "wait-throttle", [prog], setup=["set throttle 1M"])
        rig.p.set_sr(0o100000)
        rig.run("go 1000")
        time.sleep(1.5)                                 # throttle calibration
        samples = rig.p.reader().collect(1.0)
        waits = self.check_wait_samples(rig, prog, samples, prog["WAITPC"], prog["WAITPC"], 0, False)
        self.assertGreater(len(waits), 5000)
        self.assertEqual(rig.p.u32("state"), ST_WAIT)
        rig.halt()
        config.report("WAIT, throttle 1M: %d WAIT samples/s of %d" % (len(waits), len(samples)))

    def test_user_mode_mapping(self):
        """Mode/MMU/M22/D space flags, VA vs PA, rebuilt fetches and WAIT
        samples relocated in the current (user) mode."""
        k, u = progs.program("MMU_KERNEL"), progs.program("MMU_USER")
        upa, dpa = progs.MMU_USER_PA, progs.MMU_DATA_PA
        rig = Rig(self, "mmu", [k], extra=progs.deposits(u, upa))
        p = rig.p
        rig.run("go 1000")
        time.sleep(0.2)
        samples = p.reader().collect(0.5)
        stop, _ = rig.halt()
        user = [s for s in samples if mode(s.addr) == 3]
        self.assertGreater(len(user), 0.9 * len(samples))
        fetch = [s for s in user if s.addr & IFETCH]
        data = [s for s in user if not s.addr & (IFETCH | WAIT)]
        self.assertTrue(fetch and data)
        for s in user:
            self.assertTrue(s.addr & MMU and s.addr & M22)
        for s in fetch:
            self.assertIn(s.va, [a for a, n, t, src in u.listing])
            self.assertEqual(pa(s.addr), upa + s.va)
            self.assertEqual(s.data, u.word_at(s.va))
            self.assertFalse(s.addr & DSPACE)
        for s in data:
            self.assertIn(s.va, (0o2000, 0o2002))
            self.assertEqual(pa(s.addr), dpa + s.va)
            self.assertTrue(s.addr & DSPACE)
        self.assertTrue(any(s.addr & WR for s in data))
        regs = p.regs()
        self.assertEqual(regs["psw"] >> 14, 3)
        self.assertEqual(regs["mmr0"] & 1, 1)
        self.assertEqual(regs["mmr3"], 0o21)
        self.assertEqual(regs["pc_pa"], upa + regs["r"][7])
        # WAIT in user mode (idling off: busy-wait samples)
        rig.sim.command("deposit %o 1" % (dpa + 0o2002))
        rig.run("cont")
        time.sleep(0.2)
        samples = p.reader().collect(0.5)
        rig.halt()
        self.check_wait_samples(rig, u, samples, u["UWPC"], upa + u["UWPC"], 3, True)
        regs = p.regs()
        self.assertEqual(regs["r"][7], u["UWPC"])
        self.assertEqual(regs["pc_pa"], upa + u["UWPC"])

    def test_mode_of_data_cycles(self):
        """A data cycle carries the mode it was made in, not the mode at the
        sample: MTPI writes in the previous (user) mode; RTI pops the kernel
        stack and only then switches to user mode."""
        rig = Rig(self, "mode-mtpi", [progs.program("MTPI_LOOP")])
        rig.run("go 1000")
        time.sleep(0.1)
        samples = rig.p.reader().collect(0.3)
        rig.halt()
        writes = [s for s in samples if pa(s.addr) == 0o400100 and s.addr & WR]
        self.assertTrue(writes, "no MTPI write samples")
        self.assertEqual({mode(s.addr) for s in writes}, {3}, "MTPI write not in the previous mode")
        self.assertEqual({s.va for s in writes}, {0o100})
        rig = Rig(self, "mode-rti", [progs.program("RTI_LOOP")],
                  extra=["deposit 400000 %o" % progs.MODE_USER_TRAP])
        rig.run("go 1000")
        time.sleep(0.1)
        samples = rig.p.reader().collect(0.3)
        rig.halt()
        pops = [s for s in samples if pa(s.addr) == 0o776 and not s.addr & (WR | IFETCH)]
        self.assertTrue(pops, "no RTI pop samples")
        self.assertEqual({mode(s.addr) for s in pops}, {0}, "RTI kernel stack pop not in kernel mode")
        trapped = [s for s in samples if s.addr & IFETCH and mode(s.addr) == 3]
        for s in trapped:                              # the user TRAP, fetched in user mode
            self.assertEqual((pa(s.addr), s.ir), (0o400000, progs.MODE_USER_TRAP))
        config.report("mode of data cycles: %d MTPI writes in user mode, %d RTI pops in kernel mode"
                      % (len(writes), len(pops)))

    def test_fp_store_address(self):
        """The FP11's multi-word stores (STD: 4 words, STCFI long: 2 words)
        show the virtual address of their last word, not the one of the
        operand address read before them."""
        rig = Rig(self, "fp", [progs.program("FP_STORE")])
        rig.run("go 1000")
        time.sleep(0.1)
        samples = rig.p.reader().collect(0.3)
        rig.halt()
        std = [s for s in samples if pa(s.addr) == 0o3006 and s.addr & WR]
        stc = [s for s in samples if pa(s.addr) == 0o4002 and s.addr & WR]
        self.assertTrue(std and stc, (len(std), len(stc)))
        self.assertEqual({s.va for s in std}, {0o3006})
        self.assertEqual({s.va for s in stc}, {0o4002})
        for s in std + stc:
            self.assertEqual(mode(s.addr), 0)
            self.assertFalse(s.addr & (DSPACE | BYTE | IFETCH))

    # ---- bridge -> simulator ------------------------------------------------

    def test_switch_register_live(self):
        """sr reaches the guest (R5 <- SR every loop) within 10 ms, no halt."""
        rig = Rig(self, "sr", [blinker()])
        p = rig.p
        rig.run("go 1000")
        time.sleep(0.2)
        lat = []
        for v in (0o052525, 0o025252, 0o000777, 0o077000, 0o012345,
                  0o000001, 0o040000, 0o007070, 0o070707, 0o000000):
            t = time.monotonic()
            p.set_sr(v)
            while p.regs()["r"][5] != v:
                if time.monotonic() - t > 1.0:
                    self.fail("SR %06o not seen by the guest" % v)
                time.sleep(0.0002)
            lat.append(time.monotonic() - t)
        self.assertTrue(rig.sim.running)
        self.assertLessEqual(max(lat), 0.010)
        rig.halt()
        config.report("live SR: guest sees it after %.2f ms median, %.2f ms max"
                      % (1000 * statistics.median(lat), 1000 * max(lat)))

    def test_halt_request_busy(self):
        rig = Rig(self, "halt-busy", [blinker()])
        lat = []
        rig.run("go 1000")
        for i in range(20):
            time.sleep(0.05)
            stop, dt = rig.halt()
            lat.append(dt)
            self.assertEqual(rig.p.u32("halt_ack"), rig.p.u32("halt_req"))
            rig.run("cont")
        rig.halt()
        self.assertLessEqual(max(lat), 0.025)
        config.report("halt request, busy: stop after %.2f ms median, %.2f ms max (20 runs)"
                      % (1000 * statistics.median(lat), 1000 * max(lat)))

    def test_halt_request_idle(self):
        rig = Rig(self, "halt-idle", [blinker()], setup=["set cpu idle=2"])
        rig.p.set_sr(0o100000)
        rig.run("go 1000")
        time.sleep(3.0)
        lat = []
        for i in range(10):
            rig.wait_state(ST_WAIT)
            stop, dt = rig.halt()
            lat.append(dt)
            rig.run("cont")
            time.sleep(0.3)
        rig.halt()
        self.assertLessEqual(max(lat), TICK + 0.008)
        config.report("halt request, idle (WAIT, sleeping): stop after %.1f ms median, "
                      "%.1f ms max (one tick = %.1f ms)"
                      % (1000 * statistics.median(lat), 1000 * max(lat), 1000 * TICK))

    def test_cont_then_immediate_halt_request(self):
        """cont written and the halt requested at once (SimH may not even have
        read the command yet): it stops, within 25 ms, 20 times out of 20."""
        rig = Rig(self, "cont-halt", [blinker()])
        rig.run("go 1000")
        time.sleep(0.05)
        rig.halt()
        lat = []
        for i in range(20):
            rig.p.cancel_halt()
            rig.sim.start("cont")
            t = rig.p.request_halt()
            stop = rig.sim.wait_stop(1.0)
            self.assertEqual(stop.why, FPH, stop.text)
            lat.append(stop.time - t)
        self.assertLessEqual(max(lat), 0.025)
        config.report("cont + immediate halt request: 20/20 stopped, %.2f ms median, %.2f ms max"
                      % (1000 * statistics.median(lat), 1000 * max(lat)))

    def test_cancel_protocol(self):
        """A request that races a HALT instruction must not stop the next run
        once the bridge cancelled it; without the cancel it is honoured."""
        prog = progs.program("HALTER")
        rig = Rig(self, "cancel", [prog])
        p, sim = rig.p, rig.sim
        after_halt = prog["HHALT"] + 2
        rig.run("go 6000")
        stop = sim.wait_stop(2.0)
        self.assertEqual((stop.why, stop.pc), ("HALT instruction", after_halt))
        # request arriving just after the natural stop, then the bridge's cancel
        p.request_halt()
        p.cancel_halt()
        t = time.monotonic()
        sim.start("cont")
        stop = sim.wait_stop(2.0)
        self.assertEqual(stop.why, "HALT instruction", stop.text)
        self.assertGreater(stop.time - t, 0.0005)
        # negative control: a pending request is never discarded, not even at entry
        p.request_halt()
        sim.start("cont")
        stop = sim.wait_stop(2.0)
        self.assertEqual((stop.why, stop.pc), (FPH, after_halt))
        # randomized races between the request and the HALT instruction
        counts = {FPH: 0, "HALT instruction": 0}
        for i in range(40):
            rig.run("cont")                             # cancels first
            time.sleep(random.uniform(0.001, 0.006))
            t = p.request_halt()
            stop = sim.wait_stop(2.0)
            self.assertIn(stop.why, counts, stop.text)
            counts[stop.why] += 1
            if stop.why == FPH:
                self.assertGreaterEqual(stop.time, t, "Front panel HALT before the request")
        config.report("cancel protocol: 40 racing requests -> %d Front panel HALT, "
                      "%d HALT instruction, no spurious stop" % (counts[FPH], counts["HALT instruction"]))

    def test_halt_is_transparent_to_tracing(self):
        """A front panel halt between two instructions must not add a T bit
        trace trap: R1 (one per traced instruction) keeps its relation to R0
        at every stop, over 60 halt/cont cycles."""
        prog = progs.program("TRACER")
        # R1 - 2 * R0 when stopped before the instruction at PC
        legal = {prog["TLOOP"]: (0,), prog["TLOOP"] + 2: (-1,),
                 prog["TRC"]: (-2, -1), prog["TRC"] + 2: (-1, 0)}
        rig = Rig(self, "tbit", [prog])
        rig.run("go %o" % prog["TSTART"])
        seen = set()
        for i in range(60):
            time.sleep(random.uniform(0.001, 0.004))
            stop, _ = rig.halt()
            r0, r1 = rig.sim.examine("R0"), rig.sim.examine("R1")
            d = (r1 - 2 * r0) & 0o177777                   # 16-bit registers wrap
            d = d - 0o200000 if d & 0o100000 else d
            self.assertIn(stop.pc, legal)
            self.assertIn(d, legal[stop.pc], "stop %d at %06o: R0 %06o R1 %06o"
                          % (i, stop.pc, r0, r1))
            seen.add(stop.pc)
            rig.run("cont")
        rig.halt()
        self.assertGreater(len(seen), 1)

    # ---- counters and the register block ------------------------------------

    def test_trap_count(self):
        prog = progs.program("ODD_TRAP")
        rig = Rig(self, "trap", [prog])
        n0 = rig.p.u32("trap_count")
        rig.run("go 2000")
        stop = rig.sim.wait_stop(2.0)
        self.assertEqual((stop.why, stop.pc), ("HALT instruction", prog["TRAP4"] + 2))
        self.assertEqual(rig.p.u32("trap_count"), n0 + 1)
        # non-existent memory: a 32 KB machine, reference to 100000
        prog = progs.program("NXM_TRAP")
        nxm = Rig(self, "trap-nxm", [prog], setup=["set cpu 32K"])
        n0 = nxm.p.u32("trap_count")
        nxm.run("go 2000")
        stop = nxm.sim.wait_stop(2.0)
        self.assertEqual((stop.why, stop.pc), ("HALT instruction", prog["NTRAP"] + 2))
        self.assertEqual(nxm.p.u32("trap_count"), n0 + 1)

    def test_abort_loop(self):
        """An abort in every loop (odd address, RTI from the trap) restarts
        the lost countdown at random: no forced sample of the trap push, the
        sample rate stays at the target, instr_count follows the instructions
        executed (= sim_time here, no WAIT), and halt requests are honoured."""
        prog = progs.program("ABORT_LOOP")
        rig = Rig(self, "abort", [prog])
        p, sim = rig.p, rig.sim
        sim.command("deposit pc %o" % prog["ASTART"])
        ic = p.regs()["instr_count"]
        rig.run("step 3000000")
        self.assertEqual(sim.wait_stop(30).why, "Step expired")
        stepped = p.regs()["instr_count"] - ic
        self.assertLessEqual(abs(stepped - 3000000), 300000, stepped)
        rig.run("go %o" % prog["ASTART"])
        time.sleep(0.3)
        r0, wr0, tr0, t0 = p.regs(), p.u32("ring_wr"), p.u32("trap_count"), time.monotonic()
        samples = p.reader().collect(1.0)
        r1, wr1, tr1, t1 = p.regs(), p.u32("ring_wr"), p.u32("trap_count"), time.monotonic()
        _, lat = rig.halt()
        rate = ((wr1 - wr0) & 0xFFFFFFFF) / (t1 - t0)
        instr = r1["instr_count"] - r0["instr_count"]
        cycles = r1["sim_time"] - r0["sim_time"]
        traps = ((tr1 - tr0) & 0xFFFFFFFF) / (t1 - t0)
        cycle = collections.Counter()
        for s in samples:
            cycle[(s.addr, s.va)] += s.weight
        heaviest = max(cycle.values()) / sum(cycle.values())
        config.report("abort loop: %.0f aborts/s, %.0f samples/s, instr_count %.4g/s vs %.4g cycles/s, "
                      "heaviest bus cycle %.0f%% of the weight; STEP 3000000 -> instr_count %d; halt %.2f ms"
                      % (traps, rate, instr / (t1 - t0), cycles / (t1 - t0), 100 * heaviest, stepped,
                         1000 * lat))
        self.assertGreater(traps, 1e5)
        self.assertLessEqual(abs(rate - 50000), 10000)
        self.assertLessEqual(abs(instr - cycles), 0.05 * cycles)
        self.assertLess(heaviest, 0.4)                  # 4 boundaries per loop
        self.assertLessEqual(lat, 0.025)

    def test_display_register_accumulators(self):
        """dr_on[i] / weight_sum: the time-weighted DISPLAY REGISTER (bit 0
        on for 66 of every 261 instruction boundaries, bit 1 for the rest);
        weight_sum advances with the weights of the samples."""
        rig = Rig(self, "dr", [progs.program("DR_DUTY")])
        p = rig.p
        rig.run("go 1000")
        time.sleep(0.2)
        r = p.reader()
        r.read()
        on0, sum0 = p.dr_time()
        samples = r.collect(1.0)
        rig.halt()
        samples += r.read()
        on1, sum1 = p.dr_time()
        dsum = (sum1 - sum0) & 0xFFFFFFFF
        duty = [((b - a) & 0xFFFFFFFF) / dsum for a, b in zip(on0, on1)]
        config.report("DISPLAY REGISTER on-time: bit 0 %.1f%% (expected %.1f%%), bit 1 %.1f%%, %.3f s"
                      % (100 * duty[0], 100 * progs.DR_DUTY_BIT0, 100 * duty[1], dsum / 1e6))
        self.assertLessEqual(abs(duty[0] - progs.DR_DUTY_BIT0), 0.03)
        self.assertLessEqual(abs(duty[1] - (1 - progs.DR_DUTY_BIT0)), 0.03)
        self.assertEqual(duty[2:], [0.0] * 14)
        self.assertLessEqual(abs(dsum - sum(s.weight for s in samples)), 0.01 * dsum)

    def test_refresh_after_deposit(self):
        rig = Rig(self, "refresh", [blinker()])
        p, sim = rig.p, rig.sim
        regs = p.regs()
        sim.command("deposit 17777570 123456")              # writes DR
        self.assertEqual(p.regs()["dr"], regs["dr"])         # not until the refresh
        sim.command("set panel refresh")
        self.assertEqual(p.regs()["dr"], 0o123456)
        sim.commands(["deposit r0 4321", "deposit pc 2000", "deposit psw 140000",
                      "deposit usp 7000", "deposit mmr3 20", "set panel refresh"])
        regs2 = p.regs()
        self.assertEqual(regs2["r"][0], 0o4321)
        self.assertEqual(regs2["r"][7], 0o2000)
        self.assertEqual(regs2["psw"], 0o140000)
        self.assertEqual(regs2["r"][6], 0o7000)              # user SP
        self.assertEqual(regs2["mmr3"], 0o20)
        self.assertGreater(regs2["seq"], regs["seq"])

    def test_detach_attach(self):
        prog = blinker()
        rig = Rig(self, "detach", [prog])
        p, sim = rig.p, rig.sim
        rig.run("go 1000")
        time.sleep(0.2)
        rig.halt()
        wr, stops, dr = p.u32("ring_wr"), p.u32("stop_count"), p.dr_time()
        self.assertGreater(dr[1], 0)
        self.assertTrue(all(dr[0]))                     # BLINKER rotates a bit through DR
        p.set_sr(0o12345)
        p.set_target_rate(20000)
        p.w32("halt_req", p.u32("halt_ack") + 3)            # stale request
        self.assertIn("not attached", sim.command("detach panel") + sim.command("show panel"))
        sim.start("cont")                                   # detached: hooks are no-ops
        time.sleep(0.3)
        self.assertEqual((p.u32("ring_wr"), p.u32("stop_count")), (wr, stops))
        sim.interrupt()
        self.assertEqual(sim.wait_stop(2.0).why, "Simulation stopped")
        self.assertEqual(sim.command("attach panel %s" % rig.shm), "")
        self.assertTrue(p.header_ok())
        self.assertEqual((p.u32("sr"), p.u32("sr_valid"), p.u32("target_rate")), (0o12345, 1, 20000))
        self.assertEqual(p.u32("halt_ack"), p.u32("halt_req"), "ATTACH voids old requests")
        self.assertEqual((p.u32("ring_wr"), p.u32("stop_count"), p.dr_time()), (wr, stops, dr))
        sim.start("cont")                                   # no stale stop
        time.sleep(0.3)
        self.assertTrue(sim.out.size() == sim.run_start)
        self.assertGreater(p.u32("ring_wr"), wr)
        rig.halt()
        self.assertEqual(p.u32("stop_count"), stops + 1)
        # a file that is not a version 3 export is zeroed: garbage, and a
        # valid header of another version
        for version in (None, 2):
            sim.command("detach panel")
            with open(rig.shm, "r+b") as f:
                f.write(os.urandom(panel.SIZE))
            if version is not None:
                p.w32("magic", panel.MAGIC)
                p.w32("version", version)
                p.w32("size", panel.SIZE)
                p.w32("ring_size", panel.RING)
            sim.command("attach panel %s" % rig.shm)
            self.assertTrue(p.header_ok())
            self.assertEqual((p.u32("sr_valid"), p.u32("ring_wr"), p.u32("stop_count")), (0, 0, 0))
            self.assertEqual(p.dr_time(), ([0] * 16, 0))

    def test_stop_count(self):
        rig = Rig(self, "stops", [progs.program("HALTER")])
        p, sim = rig.p, rig.sim
        n = p.u32("stop_count")
        sim.command("deposit pc 6000")
        rig.run("step 10")
        self.assertEqual(sim.wait_stop().why, "Step expired")
        self.assertEqual(p.u32("stop_count"), n + 1)
        rig.run("cont")
        self.assertEqual(sim.wait_stop().why, "HALT instruction")
        self.assertEqual(p.u32("stop_count"), n + 2)
        rig.run("cont")
        rig.halt()
        self.assertEqual(p.u32("stop_count"), n + 3)

    def test_instruction_count(self):
        prog = progs.program("SOB_LOOP")
        rig = Rig(self, "icount", [prog], extra=["deposit pc 4000"])
        p, sim = rig.p, rig.sim
        for n in (1, 1000, 100000, 3000000):
            ic = p.regs()["instr_count"]
            rig.run("step %d" % n)
            self.assertEqual(sim.wait_stop(30).why, "Step expired")
            self.assertEqual(p.regs()["instr_count"] - ic, n)
        ic, t = p.regs()["instr_count"], time.monotonic()
        rig.run("cont")
        time.sleep(0.5)
        stop, _ = rig.halt()
        mips = (p.regs()["instr_count"] - ic) / (stop.time - t) / 1e6
        self.assertTrue(10 < mips < 2000, mips)
        config.report("instr_count: exact after STEP 1/1000/100000/3000000; %.0f MIPS SOB loop" % mips)

    def test_rate_adaptation(self):
        rig = Rig(self, "rate", [blinker()])
        p = rig.p
        rig.run("go 1000")
        got = []
        for target in (0, 10000, 200000, 0):
            p.set_target_rate(target)
            time.sleep(0.6)
            wr, t = p.u32("ring_wr"), time.monotonic()
            time.sleep(0.5)
            rate = (p.u32("ring_wr") - wr) / (time.monotonic() - t)
            want = target or 50000
            regs = p.regs()
            self.assertLessEqual(abs(rate - want), 0.2 * want, (target, rate))
            self.assertLessEqual(abs(regs["sps"] - want), 0.2 * want, (target, regs["sps"]))
            got.append("%d -> %.0f/s (interval %d)" % (want, rate, regs["interval"]))
        rig.halt()
        config.report("rate adaptation: " + ", ".join(got))

    # ---- sample content -------------------------------------------------------

    def test_sample_content(self):
        """Instruction samples: rebuilt fetches (PA, opcode, IR) in a SOB loop;
        reads, writes and stack cycles of a memory-copy loop."""
        sob, mix = progs.program("SOB_LOOP"), progs.program("COPY_MIX")
        pattern = {0o14000 + 2 * i: (0o1111 * (i + 1)) & 0o177777 for i in range(16)}
        rig = Rig(self, "content", [sob, mix],
                  extra=["deposit %o %o" % kv for kv in pattern.items()])
        p = rig.p
        rig.run("go 4000")
        time.sleep(0.1)
        samples = p.reader().collect(0.3)
        rig.halt()
        hot = [s for s in samples if s.addr & IFETCH and s.va == sob["SOB1"]]
        self.assertGreater(len(hot), 0.95 * len(samples))
        for s in hot:
            self.assertEqual((pa(s.addr), s.data, s.ir), (sob["SOB1"], 0o077301, 0o077301))
            # pc is R7 at the sample (the next instruction): SOB1 again, or
            # the BR after it when this SOB counted r3 down to 0 (one pass in
            # 65535, so about one run in five has such a sample)
            self.assertIn(s.pc, (sob["SOB1"], sob["SOB1"] + 2))
            self.assertEqual(mode(s.addr), 0)
            self.assertFalse(s.addr & (MMU | M22 | WR | BYTE | WAIT | DSPACE | IOPAGE))
        rig.run("go 5000")
        time.sleep(0.1)
        samples = p.reader().collect(0.3)
        rig.halt()
        # a sample shows the last cycle of an instruction: the copy's write,
        # the fetch of ADD and SOB, immediate operands, the JSR push, RTS pop
        opcodes = {w for a, w in mix.words}
        code = {a for a, w in mix.words}
        writes = [s for s in samples if s.addr & WR and 0o15000 <= s.va < 0o15040]
        push = [s for s in samples if s.addr & WR and s.va == 0o776]
        pop = [s for s in samples if not s.addr & (WR | IFETCH) and s.va == 0o776]
        imm = [s for s in samples if not s.addr & (WR | IFETCH) and s.va in code]
        fetches = [s for s in samples if s.addr & IFETCH]
        self.assertTrue(writes and push and pop and imm and fetches)
        for s in samples:
            self.assertEqual(pa(s.addr), s.va)              # no mapping
            self.assertIn(s.ir, opcodes)
            self.assertFalse(s.addr & (BYTE | WAIT | MMU | DSPACE))
        for s in writes:
            self.assertEqual(s.data, pattern[s.va - 0o1000])
        for s in push + pop:
            self.assertEqual(s.data, mix["RET"])
        for s in imm + fetches:
            self.assertEqual(s.data, mix.word_at(s.va))
        for s in fetches:
            self.assertIn(s.va, code)
        regs = p.regs()                                      # last cycle: in the program
        self.assertLess(regs["last_pa"] & panel.PA_MASK, 0o20000)

    def test_no_behaviour_change(self):
        """The same deterministic run (no interrupts) ends in the same state
        with the export attached, detached, and on the unpatched baseline."""
        mix = progs.program("COPY_MIX")
        setups = [("attached", CFG.bin, True), ("detached", CFG.bin, False)]
        if CFG.baseline:
            setups.append(("baseline", CFG.baseline, False))
        states = {}
        for name, binary, attach in setups:
            rig = Rig(self, "same-" + name, [mix], attach=attach, console=False, binary=binary,
                      extra=["deposit %o %o" % (0o14000 + 2 * i, 3 * i + 1) for i in range(16)])
            rig.sim.command("deposit pc 5000")
            rig.sim.start("step 2000000")
            self.assertEqual(rig.sim.wait_stop(30).why, "Step expired")
            regs = [rig.sim.examine(r) for r in ("R0", "R1", "R2", "R3", "R4", "R5", "SP", "PC", "PSW")]
            mem = rig.sim.command("examine 3000-3010 14000-14040 15000-15040")
            states[name] = (regs, mem)
        ref = states["detached"]
        for name, st in states.items():
            self.assertEqual(st, ref, name)
        config.report("same end state after STEP 2000000: " + ", ".join(sorted(states)))


if __name__ == "__main__":
    sys.exit("run via run_tests.py")
