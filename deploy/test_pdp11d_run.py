#!/usr/bin/env python3
"""Tests for deploy/pdp11d-run (no root and no systemd needed).

    python3 -I deploy/test_pdp11d_run.py

pdp11d-run reads /etc/pdp11/pdp11d.env with the rules of systemd's
EnvironmentFile=. Every case below is a settings file and the variables
systemd 259 makes of it (checked with systemd-run --user -p
EnvironmentFile=FILE env). The script runs under each POSIX shell found
(dash, bash, busybox sh) as a non-root user, with pdp11d replaced by a
program that prints its environment.
"""

import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest

sys.dont_write_bytecode = True          # keep the source tree clean
HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(HERE, "pdp11d-run")
BIN_LINE = "bin=/opt/pdp11/bin/pdp11d\n"

CASES = {
    "plain": ("T_A=plain\nT_B=  spaced value  \nT_C=\n#T_D=commented\n; T_E=semi\n   T_F = x\n",
              {"T_A": "plain", "T_B": "spaced value", "T_C": "", "T_F": "x"}),
    "quotes": ("T_A='single $x \\\\ \"d\"'\nT_B=\"double \\\" \\\\ \\$ \\` \\n \\x\"\nT_C='a'b\nT_D='a' 'b'\n"
               "T_E=a'b'c\nT_F=\"it's\"\nT_G='trail'   \nT_H=\"x\"  y  \n",
               {"T_A": "single $x \\\\ \"d\"", "T_B": "double \" \\ $ ` \\n \\x", "T_C": "ab", "T_D": "ab",
                "T_E": "a'b'c", "T_F": "it's", "T_G": "trail", "T_H": "xy"}),
    "backslashes": ("T_A=pa\\ss\nT_B=a\\\\b\nT_C=a\\ \nT_D=a \\ \nT_E=back\\\nslash\nT_F=end\\\n",
                    {"T_A": "pass", "T_B": "a\\b", "T_C": "a ", "T_D": "a  ", "T_E": "backslash", "T_F": "end"}),
    "multi-line": ("T_A='line1\nline2'\nT_B=\"l1\nl2\\\nl3\"\nT_C=after\n",
                   {"T_A": "line1\nline2", "T_B": "l1\nl2l3", "T_C": "after"}),
    "invalid names": ("1T=x\nT-X=y\nT X=z\nexport T_EX=e\nT_OK=ok\n=v\nT_NOEQ\nT_LAST=last",
                      {"T_OK": "ok", "T_LAST": "last"}),
    "CRLF": ("T_A=crlf\r\nT_B='q'\r\n# c\r\nT_C=x y \r\n", {"T_A": "crlf", "T_B": "q", "T_C": "x y"}),
    "comments": ("# comment \\\nT_A=after-comment-backslash\nT_B=x # not a comment\n",
                 {"T_A": "after-comment-backslash", "T_B": "x # not a comment"}),
    "dollar": ("PDP11_PASSWORD=$ecretPass\nT_A=$HOME\nT_B=\"$HOME\"\nT_C='$HOME'\n",
               {"PDP11_PASSWORD": "$ecretPass", "T_A": "$HOME", "T_B": "$HOME", "T_C": "$HOME"}),
    "last wins": ("T_A=1\nT_A=2\n", {"T_A": "2"}),
    "README example": ("PDP11_PASSWORD='a$b\\c'\nT_FA=\"'self' https://dashboard.example.com\"\n",
                       {"PDP11_PASSWORD": "a$b\\c", "T_FA": "'self' https://dashboard.example.com"}),
}

SHELLS = [sh for sh in (["dash"], ["bash"], ["busybox", "sh"]) if shutil.which(sh[0])]


@unittest.skipIf(os.geteuid() == 0, "run as a non-root user (as root pdp11d-run changes to the user pdp11)")
class EnvFileTest(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp(prefix="pdp11d-run-test.")
        self.addCleanup(shutil.rmtree, self.dir)
        stub = os.path.join(self.dir, "pdp11d")
        with open(stub, "w") as f:
            f.write("#!%s -I\nimport json, os, sys\njson.dump(dict(os.environ), sys.stdout)\n" % sys.executable)
        os.chmod(stub, 0o755)
        with open(SCRIPT) as f:
            text = f.read()
        self.assertIn(BIN_LINE, text)
        self.script = os.path.join(self.dir, "pdp11d-run")
        with open(self.script, "w") as f:
            f.write(text.replace(BIN_LINE, "bin=%s\n" % stub, 1))

    def environment(self, shell: list, settings: str) -> dict:
        path = os.path.join(self.dir, "pdp11d.env")
        with open(path, "w", newline="") as f:
            f.write(settings)
        env = {"PATH": os.environ.get("PATH", "/usr/bin:/bin"), "PDP11_ENV_FILE": path}
        r = subprocess.run(shell + [self.script], env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                           timeout=30)
        self.assertEqual(r.returncode, 0, r.stderr.decode())
        return json.loads(r.stdout)

    def test_systemd_rules(self):
        self.assertTrue(SHELLS, "no POSIX shell found")
        for shell in SHELLS:
            for name, (settings, want) in CASES.items():
                with self.subTest(shell=shell[0], case=name):
                    got = self.environment(shell, settings)
                    self.assertEqual({k: v for k, v in got.items() if k.startswith("T_") or k == "PDP11_PASSWORD"},
                                     want)

    def test_defaults_and_shipped_file(self):
        """The unit's defaults, then deploy/pdp11d.env (only PDP11_LISTEN and an empty password are active)."""
        with open(os.path.join(HERE, "pdp11d.env")) as f:
            shipped = f.read()
        got = self.environment(SHELLS[0], shipped)
        self.assertEqual({k: v for k, v in got.items() if k.startswith("PDP11_") and k != "PDP11_ENV_FILE"}, {
            "PDP11_DATA": "/var/lib/pdp11", "PDP11_SHM": "/run/pdp11/panel", "PDP11_SHUTDOWN_ON_STOP": "1",
            "PDP11_LISTEN": ":8080", "PDP11_PASSWORD": ""})
        got = self.environment(SHELLS[0], "PDP11_DATA=/srv/pdp11\nPDP11_SHUTDOWN_ON_STOP=0\n")
        self.assertEqual((got["PDP11_DATA"], got["PDP11_SHUTDOWN_ON_STOP"]), ("/srv/pdp11", "0"))


if __name__ == "__main__":
    unittest.main()
