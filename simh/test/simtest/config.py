"""Options shared by the test modules (set by run_tests.py)."""

import os
import threading

SIMH_DIR = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))


class Config:
    bin = None              # simulator under test
    baseline = None         # unpatched simulator for the overhead test, or None
    baseline_note = ""      # why there is none
    work = None             # work directory
    test_cache = None       # catalog archives named by their sha256 (PDP11_TEST_CACHE)
    quick = False           # skip the slow tests
    rounds = 7              # benchmark rounds
    libedit_build = True    # build a libedit variant when the host allows it


CFG = Config()
_report = []
_lock = threading.Lock()
_counter = [0]


def workdir(name):
    """A new, empty directory below the work directory (a reused work
    directory keeps the directories of earlier runs)."""
    with _lock:
        while True:
            _counter[0] += 1
            d = os.path.join(CFG.work, "%03d-%s" % (_counter[0], name))
            try:
                os.makedirs(d)
                return d
            except FileExistsError:
                continue


def report(line):
    """Record a measurement for the summary printed at the end."""
    with _lock:
        _report.append(line)
    print("    # " + line, flush=True)


def report_lines():
    return list(_report)
