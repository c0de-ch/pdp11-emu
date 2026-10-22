"""The test assembler against SimH's own disassembler (EXAMINE -M)."""

import re
import sys
import unittest

from simtest import config, progs, simh
from simtest.config import CFG


class TestAssembler(unittest.TestCase):

    def test_programs_disassemble_as_written(self):
        """Every instruction of every test program, deposited into SimH,
        disassembles to the text the assembler expects."""
        sim = simh.Sim(CFG.bin, config.workdir("asm"), ["set cpu 11/70", "set cpu 4M"],
                       console=False)
        self.addCleanup(sim.close)
        checked = 0
        for name in progs.ALL:
            prog = progs.program(name)
            sim.commands(progs.deposits(prog))
            for addr, nwords, text, source in prog.listing:
                out = sim.command("examine -m %o" % addr)
                m = re.match(r"^([0-7]+):\t(.*)$", out.strip())
                self.assertIsNotNone(m, out)
                self.assertEqual(int(m.group(1), 8), addr)
                self.assertEqual(m.group(2).strip(), text, "%s: %s" % (name, source))
                checked += 1
        config.report("assembler: %d instructions of %d programs match SimH's disassembly"
                      % (checked, len(progs.ALL)))


if __name__ == "__main__":
    sys.exit("run via run_tests.py")
