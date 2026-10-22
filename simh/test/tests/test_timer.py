"""Regression tests of the KW11-L clock while idling and throttling
(simh/patches/0003-timer-idle-and-throttle-calibration.patch).

TestIdleClock: the idle clock keeps ticking at 60 Hz after a CPU burst when
the guest idles before the KW11-L completed its first calibration second
(UNIX V5 sits at "login:" right after booting).  Without the patch
sim_idle_cyc_ms freezes at the KW11-L's initial 1 MIPS tick size; a short
burst then multiplies the tick size ~40 times, every idle tick sleeps about a
second and the calibration discards each following second as "gap too big":
1-2 ticks per second for as long as the guest idles.  The simulator waits at
the prompt for a second before GO: if that time counted in the first
calibration second, the rate it measures would be as wrong.

TestSpeedSwitch: pdp11d's speed switches at run time, made as pdp11d makes
them (panel HALT, the speed commands at the prompt, CONT).  Without the
patch the clock stalls after fast -> authentic (the tick size calibrated
unthrottled is kept), stalls after authentic -> fast and a burst (the idle
rate measured while throttled is kept), and every start of the throttle
costs the guest up to a second."""

import os
import re
import time
import unittest

from simtest import config, panel, simh
from simtest.asm11 import assemble
from simtest.config import CFG

# Kernel program at 1000: KW11-L interrupts counted at 3000 and copied to the
# display register; busy until the second tick (a kernel's start-up), then
# WAIT; one burst of 5 M instructions right after the third calibration
# second (tick 181), then WAIT for good.
IDLE_CLOCK = """
TICKS = 3000
DONE = 3002
        .org 100
        .word clk, 340
        .org 1000
start:  mov #1000, sp
        clr @#TICKS
        clr @#DONE
        mov #100, @#177546      ; KW11-L interrupt enable
        clr @#177776            ; kernel, priority 0
boot:   cmp @#TICKS, #2
        blo boot
idle:   wait
        mov @#TICKS, @#177570   ; DR <- ticks
        tst @#DONE
        bne idle
        cmp @#TICKS, #181.
        blo idle
        inc @#DONE
        mov #76., r4            ; 76 * 65535 SOB
outer:  mov #177777, r3
inner:  sob r3, inner
        sob r4, outer
        br idle
clk:    inc @#TICKS
        rti
"""
MIN_RATE = 30.0                 # ticks/s (60 nominal) where the bugs give 0-5

# Kernel program at 1000 for the speed switches: KW11-L interrupts counted at
# 3000 and copied to the display register; SR<0> = 1 busy loop, 0 WAIT loop.
# SR<1> = 1 restarts the clock with RESET (the KW11-L begins a calibration
# second at tick 0), then with SR<0> = 0: WAIT until tick 61, one burst of
# 5 M instructions (IDLE_CLOCK's), WAIT for good; with SR<0> = 1: busy until
# tick 181 (one tick into the fourth calibration second), HALT, busy for good.
SWITCH = """
TICKS = 3000
        .org 100
        .word clk, 340
        .org 1000
start:  mov #1000, sp
        clr @#TICKS
        mov #100, @#177546      ; KW11-L interrupt enable
        clr @#177776            ; kernel, priority 0
loop:   bit #2, @#177570        ; SR<1>: restart the clock
        bne restart
        bit #1, @#177570        ; SR<0>: busy, otherwise WAIT
        bne loop
        wait
        br loop
restart: reset                  ; also clears the interrupt enable
        clr @#TICKS
        mov #100, @#177546
        bit #1, @#177570
        bne count
wait61: wait
        cmp @#TICKS, #61.
        blo wait61
        mov #76., r4            ; 76 * 65535 SOB
outer:  mov #177777, r3
inner:  sob r3, inner
        sob r4, outer
idle:   wait
        br idle
count:  cmp @#TICKS, #181.
        blo count
        halt
spin:   br spin
clk:    inc @#TICKS
        mov @#TICKS, @#177570   ; DR <- ticks
        rti
"""
BUSY, RESTART = 1, 2            # SR bits of SWITCH
# pdp11d's speeds (server/internal/simh/runini.go, SpeedCommands)
FAST = ["set nothrottle", "set cpu idle"]
AUTHENTIC = ["set throttle 1M"]


class TestIdleClock(unittest.TestCase):

    def setUp(self):
        if CFG.quick:
            self.skipTest("--quick")

    def test_clock_after_burst(self):
        """SET CPU IDLE (no stability wait), a second at the prompt before
        GO: 60 Hz ticks after a short burst."""
        wd = config.workdir("idle-clock")
        shm = os.path.join(wd, "panel.shm")
        prog = assemble(IDLE_CLOCK)
        setup = ["set cpu 11/70", "set cpu 1M", "set nothrottle", "set cpu idle"]
        setup += ["deposit %o %o" % (a, w) for a, w in prog.words]
        sim = simh.Sim(CFG.bin, wd, setup, console=False, panel=shm, name="idle-clock")
        self.addCleanup(sim.close)
        p = panel.Panel(shm)
        self.addCleanup(p.close)
        p.cancel_halt()
        time.sleep(1.0)                         # at the prompt, as before a boot
        sim.start("go 1000")
        t0 = time.monotonic()
        samples, burst = [], None
        while True:
            t, ticks = time.monotonic() - t0, p.regs()["dr"]
            samples.append((t, ticks))
            if burst is None and ticks >= 181:
                burst = t
            if (burst is not None and t >= burst + 9.5) or t >= 25.0:
                break
            time.sleep(0.25)
        p.request_halt()
        self.assertEqual(sim.wait_stop(5).why, "Front panel HALT")
        self.assertTrue(burst is not None and burst < 15.0, "no burst (tick 181) within 15 s")

        def at(t):
            return next(s for s in samples if s[0] >= t)

        rates = []
        for k in (1, 3, 5, 7):                  # 2 s windows from 1 to 9 s after the burst
            (ta, na), (tb, nb) = at(burst + k), at(burst + k + 2)
            rates.append((nb - na) / (tb - ta))
        config.report("idle clock after a burst: %s ticks/s in 2 s windows 1-9 s after it (%.1f s after GO)"
                      % (" ".join("%.0f" % r for r in rates), burst))
        self.assertGreaterEqual(min(rates), MIN_RATE, "the clock stalled after the burst")


class TestSpeedSwitch(unittest.TestCase):

    def setUp(self):
        if CFG.quick:
            self.skipTest("--quick")

    def start(self, name, speed, sr):
        """SWITCH running at the given speed (run.ini's header line)."""
        wd = config.workdir(name)
        shm = os.path.join(wd, "panel.shm")
        prog = assemble(SWITCH)
        setup = ["set cpu 11/70", "set cpu 1M"] + speed
        setup += ["deposit %o %o" % (a, w) for a, w in prog.words]
        sim = simh.Sim(CFG.bin, wd, setup, console=False, panel=shm, name=name)
        self.addCleanup(sim.close)
        p = panel.Panel(shm)
        self.addCleanup(p.close)
        p.set_sr(sr)
        p.cancel_halt()
        sim.start("go 1000")
        return sim, p

    def halt(self, sim, p):
        p.request_halt()
        stop = sim.wait_stop(5)
        p.cancel_halt()
        self.assertEqual(stop.why, "Front panel HALT", stop.text)
        return stop

    def set_speed(self, sim, speed):
        for line in speed:
            r = sim.command(line)
            self.assertNotIn("%SIM-ERROR", r, line)

    def switch(self, sim, p, speed):
        """A speed change while running: HALT, the commands, CONT."""
        self.halt(sim, p)
        self.set_speed(sim, speed)
        sim.start("cont")

    def wait_throttled(self, p, timeout=15.0):
        """Until the throttle runs (SimH defers it for the first seconds):
        SimH's time, WAIT included, advances at about 1 M/s."""
        deadline = time.monotonic() + timeout
        while True:
            t0, s0 = time.monotonic(), p.regs()["sim_time"]
            time.sleep(0.25)
            rate = (p.regs()["sim_time"] - s0) / (time.monotonic() - t0)
            if rate < 3e6:
                return
            if time.monotonic() > deadline:
                self.fail("the throttle did not start: %.3g instructions/s" % rate)

    def rate(self, p, t0, start, end):
        """KW11-L ticks/s from t0 + start to t0 + end."""
        time.sleep(max(0.0, t0 + start - time.monotonic()))
        a, ta = p.regs()["dr"], time.monotonic()
        time.sleep(max(0.0, t0 + end - time.monotonic()))
        b, tb = p.regs()["dr"], time.monotonic()
        return ((b - a) & 0xFFFF) / (tb - ta)

    def test_authentic_after_fast(self):
        """Authentic, fast (busy), authentic: the clock keeps ticking (the
        catch-up of the switch to fast holds it near 40 ticks/s at first)."""
        sim, p = self.start("auth-fast-auth", AUTHENTIC, BUSY)
        self.wait_throttled(p)
        self.switch(sim, p, FAST)
        time.sleep(3.0)                         # the tick size calibrates to the fast rate
        self.switch(sim, p, AUTHENTIC)
        rate = self.rate(p, time.monotonic(), 1.0, 4.0)
        self.halt(sim, p)
        config.report("authentic, fast, authentic: %.1f ticks/s 1-4 s after the switch" % rate)
        self.assertGreaterEqual(rate, MIN_RATE, "the clock stalled after fast -> authentic")

    def test_fast_after_authentic_burst(self):
        """Authentic (idle), fast set at the prompt, a burst: 60 Hz ticks."""
        sim, p = self.start("auth-fast-burst", AUTHENTIC, 0)
        self.wait_throttled(p)
        time.sleep(1.5)                         # throttled calibration seconds
        self.halt(sim, p)
        time.sleep(1.0)                         # halted, then switched: pdp11d's halted path
        self.set_speed(sim, FAST)
        cyc_ms = int(re.search(r"(\d+)\s*$", sim.command("examine int-clock IDLE_CYC_MS")).group(1))
        time.sleep(1.0)
        sim.start("cont")
        time.sleep(1.0)
        p.set_sr(RESTART)                       # WAIT to tick 61, the burst, WAIT
        rate = self.rate(p, time.monotonic(), 3.0, 7.0)
        self.halt(sim, p)
        config.report("authentic, fast, a burst: idle rate %d cycles/ms, %.1f ticks/s 3-7 s after the burst"
                      % (cyc_ms, rate))
        self.assertGreaterEqual(rate, MIN_RATE, "the clock stalled after authentic -> fast and a burst")
        self.assertTrue(cyc_ms == 0 or cyc_ms >= 5000,
                        "the idle rate is the throttled one: %d cycles/ms" % cyc_ms)

    def test_throttle_start_keeps_time(self):
        """Fast, authentic one tick into a calibration second: no time lost."""
        sim, p = self.start("fast-auth-time", FAST, BUSY)
        time.sleep(2.0)                         # the tick size calibrates to the fast rate
        t_restart = time.monotonic()
        p.set_sr(BUSY | RESTART)                # busy to tick 181, HALT
        stop = sim.wait_stop(15)
        self.assertEqual(stop.why, "HALT instruction", stop.text)
        self.set_speed(sim, AUTHENTIC)
        t_cont = time.monotonic()
        sim.start("cont")
        time.sleep(6.0)                         # SimH's catch-up settles
        ticks, t_end = p.regs()["dr"], time.monotonic()
        auth = self.halt(sim, p)
        error = ticks / 60.0 - ((stop.time - t_restart) + (t_end - t_cont))
        config.report("fast, authentic at tick 1 of a calibration second: guest clock %+.2f s after %.1f s"
                      % (error, (stop.time - t_restart) + (t_end - t_cont)))
        self.assertGreater(error, -0.4, "the start of the throttle cost the guest time")
        self.assertNotIn("WARNING", auth.text)  # "Host CPU could be too slow": a stale measurement
