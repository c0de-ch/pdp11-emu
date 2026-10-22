"""Regression: boot Unix V6 (simh kit uv6swre, 4 x RK05) to the shell, with
and without the export attached, the console on TCP as in pdp11d.  The kit is
the catalog's archive (systems/unixv6/system.json) from $PDP11_TEST_CACHE, a
directory of archives named by their sha256 as "pdp11d fetch --cache DIR"
stores them; the tests are skipped without it."""

import hashlib
import json
import os
import re
import shutil
import sys
import time
import unittest
import zipfile

from simtest import config, panel, simh
from simtest.config import CFG
from simtest.panel import IFETCH, WAIT, mode

CATALOG = os.path.join(os.path.dirname(config.SIMH_DIR), "systems", "unixv6", "system.json")
DISKS = ["unix0_v6_rk.dsk", "unix1_v6_rk.dsk", "unix2_v6_rk.dsk", "unix3_v6_rk.dsk"]
TYPE = 0.02                                     # seconds per typed character
_kit = []                                       # the extracted images' directory


def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for block in iter(lambda: f.read(1 << 20), b""):
            h.update(block)
    return h.hexdigest()


def v6_kit(test):
    """Directory with the pristine RK05 images, extracted (once per run) from
    the catalog archive in the test cache; skips the test without it."""
    if _kit:
        return _kit[0]
    with open(CATALOG) as f:
        art = json.load(f)["artifacts"][0]
    archive = os.path.join(CFG.test_cache or "", art["sha256"])
    if not CFG.test_cache or not os.path.isfile(archive):
        test.skipTest("the Unix V6 kit (%s, sha256 %s) is not in PDP11_TEST_CACHE (%s)"
                      % (os.path.basename(art["url"]), art["sha256"], CFG.test_cache or "unset"))
    test.assertEqual(art["format"], "zip")
    test.assertEqual(sha256(archive), art["sha256"], archive)
    files = {f["path"]: f["sha256"] for f in art["files"]}
    kit = config.workdir("v6-kit")
    with zipfile.ZipFile(archive) as z:
        for d in DISKS:
            out = os.path.join(kit, d)
            with z.open(d) as src, open(out, "wb") as dst:
                shutil.copyfileobj(src, dst)
            test.assertEqual(sha256(out), files[d], d)
    _kit.append(kit)
    return kit


class TestBootV6(unittest.TestCase):

    def setUp(self):
        if CFG.quick:
            self.skipTest("--quick")
        self.kit = v6_kit(self)

    def boot(self, attach):
        wd = config.workdir("v6-" + ("panel" if attach else "nopanel"))
        for d in DISKS:                                 # work on copies
            shutil.copyfile(os.path.join(self.kit, d), os.path.join(wd, d))
        setup = ["set cpu 11/70", "set cpu 1M", "set cpu idle", "set tto 7b",
                 "set rq disabled", "set tq disabled", "set rk enabled"]
        setup += ["attach rk%d %s" % (i, os.path.join(wd, d)) for i, d in enumerate(DISKS)]
        shm = os.path.join(wd, "panel.shm")
        sim = simh.Sim(CFG.bin, wd, setup, panel=shm if attach else None, name="v6")
        self.addCleanup(sim.close)
        p = None
        if attach:
            p = panel.Panel(shm)
            self.addCleanup(p.close)
            p.cancel_halt()
        out = sim.console.out
        t0 = time.monotonic()
        sim.start("boot rk0")
        pos = out.wait(rb"@", 0, 30).end()
        sim.console.send("rkunix\r", TYPE)
        pos = out.wait(rb"login: ", pos, 60).end()
        t_login = time.monotonic() - t0
        sim.console.send("root\r", TYPE)
        pos = out.wait(rb"# ", pos, 30).end()
        sim.console.send("ls /\r", TYPE)
        m = out.wait(rb"ls /\r?\n(.*?)# ", pos, 30)
        listing = m.group(1).decode("latin-1")
        pos = m.end()
        self.assertIn("rkunix", listing)
        self.assertIn("usr", listing)
        for i in range(3):
            sim.console.send("sync\r", TYPE)
            pos = out.wait(rb"# ", pos, 30).end()
        time.sleep(2.0)                                 # the shell idles
        note = ""
        if p:
            self.assertTrue(sim.running)
            self.assertGreater(p.u32("ring_wr"), 1000)
            samples = p.reader().collect(1.0)
            waits = [s for s in samples if s.addr & WAIT]
            self.assertTrue(waits, "no WAIT samples while V6 idles")
            self.assertTrue(all(mode(s.addr) == 0 for s in waits))     # the kernel idles
            self.assertGreater(sum(s.weight for s in waits), 0.5e6)    # mostly idle
            note = ", %d WAIT and %d other samples in 1 s idle, DR %06o" % (
                len(waits), len(samples) - len(waits), p.regs()["dr"])
        c0, t1 = sim.cpu_seconds(), time.monotonic()
        time.sleep(3.0)
        cpu = (sim.cpu_seconds() - c0) / (time.monotonic() - t1)
        if p:
            p.request_halt()
            stop = sim.wait_stop(2)
            self.assertEqual(stop.why, "Front panel HALT", stop.text)
        else:
            sim.interrupt()
            stop = sim.wait_stop(2)
            self.assertEqual(stop.why, "Simulation stopped", stop.text)
        config.report("Unix V6 %s: login: after %.2f s, ls / ok, idle host CPU %.1f%%%s"
                      % ("with PANEL" if attach else "without PANEL", t_login, 100 * cpu, note))

    def test_v6_with_panel(self):
        self.boot(True)

    def test_v6_without_panel(self):
        self.boot(False)


if __name__ == "__main__":
    sys.exit("run via run_tests.py")
