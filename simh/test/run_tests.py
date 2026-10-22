#!/usr/bin/env python3
"""Tests of the patched Open SIMH PDP-11 simulator (docs/SIMH_PATCH.md).

    python3 -I simh/test/run_tests.py [--bin simh/out/pdp11] [options] [pattern ...]

Groups: build (smoke check, libedit, layout), asm (the test assembler against
SimH's disassembler), panel (functional), perf (overhead, idle CPU), boot
(Unix V6 regression, the kit from $PDP11_TEST_CACHE) and timer (the clock
while idling and throttling, patch 0003).  Patterns select tests whose id
contains one of them, e.g. "panel" or "test_halt".  Python 3 standard
library only.
"""

import os
import sys

sys.dont_write_bytecode = True
HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)

import argparse      # noqa: E402
import atexit        # noqa: E402
import shutil        # noqa: E402
import subprocess    # noqa: E402
import tempfile      # noqa: E402
import time          # noqa: E402
import unittest      # noqa: E402

from simtest import config, simh   # noqa: E402

SIMH = config.SIMH_DIR
MODULES = ["tests.test_build", "tests.test_asm", "tests.test_panel",
           "tests.test_perf", "tests.test_boot", "tests.test_timer"]


def build_baseline(cfg):
    """Build the pinned commit with only the non-panel patches (the CPU
    without hooks) for the overhead comparison."""
    src = os.path.join(cfg.work, "baseline-src")
    out = os.path.join(cfg.work, "baseline")
    repo = os.path.join(SIMH, "src")
    cmd = [os.path.join(SIMH, "build.sh"), "--baseline", "--src", src, "--out", out]
    if os.path.isdir(os.path.join(repo, ".git")):
        cmd += ["--repo", repo]                 # no network needed
    print("building the baseline simulator: " + " ".join(cmd), flush=True)
    t = time.monotonic()
    r = subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    if r.returncode != 0:
        cfg.baseline_note = "baseline build failed: " + r.stdout.decode("latin-1")[-500:]
        print(cfg.baseline_note)
        return None
    print("baseline built in %.0f s" % (time.monotonic() - t), flush=True)
    return os.path.join(out, "pdp11")


def iter_tests(suite):
    for t in suite:
        if isinstance(t, unittest.TestSuite):
            yield from iter_tests(t)
        else:
            yield t


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("--bin", default=os.path.join(SIMH, "out", "pdp11"),
                    help="simulator under test (default simh/out/pdp11)")
    ap.add_argument("--baseline-bin", help="unpatched simulator for the overhead "
                    "test (default: build one with build.sh --baseline)")
    ap.add_argument("--no-baseline", action="store_true",
                    help="do not build a baseline; overhead only attached vs detached")
    ap.add_argument("--test-cache", default=os.environ.get("PDP11_TEST_CACHE"),
                    help="catalog archives named by their sha256, as 'pdp11d fetch --cache DIR' "
                    "stores them (default $PDP11_TEST_CACHE); the boot tests take the "
                    "Unix V6 kit from it")
    ap.add_argument("--work", help="work directory (default: a temporary directory)")
    ap.add_argument("--keep", action="store_true", help="keep the work directory")
    ap.add_argument("--quick", action="store_true",
                    help="skip the performance, boot, timer and libedit build tests")
    ap.add_argument("--no-libedit-build", action="store_true",
                    help="skip building the libedit variant")
    ap.add_argument("--rounds", type=int, default=7, help="benchmark rounds (default 7)")
    ap.add_argument("patterns", nargs="*")
    a = ap.parse_args()

    cfg = config.CFG
    cfg.bin = os.path.abspath(a.bin)
    if not os.access(cfg.bin, os.X_OK):
        ap.error("%s is not an executable (run simh/build.sh first)" % cfg.bin)
    if a.work:
        cfg.work = os.path.abspath(a.work)
        os.makedirs(cfg.work, exist_ok=True)
    else:
        cfg.work = tempfile.mkdtemp(prefix="pdp11-simh-test-")
        if not a.keep:
            atexit.register(shutil.rmtree, cfg.work, True)
    atexit.register(simh.kill_all)
    cfg.test_cache = a.test_cache
    cfg.quick = a.quick
    cfg.rounds = max(4, a.rounds)
    cfg.libedit_build = not (a.no_libedit_build or a.quick)

    loader = unittest.TestLoader()
    suite = unittest.TestSuite()
    for name in MODULES:
        suite.addTests(loader.loadTestsFromName(name))
    if a.patterns:
        suite = unittest.TestSuite(t for t in iter_tests(suite)
                                   if any(p in t.id() for p in a.patterns))
    wants_perf = any("test_perf" in t.id() for t in iter_tests(suite))
    if a.baseline_bin:
        cfg.baseline = os.path.abspath(a.baseline_bin)
    elif a.no_baseline or cfg.quick:
        cfg.baseline_note = "no baseline simulator (--no-baseline)"
    elif wants_perf:
        cfg.baseline = build_baseline(cfg)

    print("simulator: %s\nwork directory: %s" % (cfg.bin, cfg.work), flush=True)
    result = unittest.TextTestRunner(verbosity=2).run(suite)
    lines = config.report_lines()
    if lines:
        print("\nMeasurements")
        for line in lines:
            print("  " + line)
    if a.keep or a.work:
        print("work directory kept: " + cfg.work)
    return 0 if result.wasSuccessful() else 1


if __name__ == "__main__":
    sys.exit(main())
