"""Build smoke checks: SCP prompt on a pipe (also with libedit), the PANEL
device and its commands, the export layout against the C header."""

import os
import pty
import re
import select
import shutil
import subprocess
import sys
import time
import unittest

from simtest import config, panel
from simtest.config import CFG

SYSTEM_LIBDIRS = ["/usr/lib/x86_64-linux-gnu", "/usr/lib/aarch64-linux-gnu",
                  "/lib/x86_64-linux-gnu", "/lib/aarch64-linux-gnu", "/usr/lib64", "/usr/lib"]


def run_piped(binary, script, timeout=30):
    """Run the simulator with `script` on a pipe, in an empty directory."""
    wd = config.workdir("pipe")
    r = subprocess.run([binary], input=script.encode(), cwd=wd, env=dict(os.environ, HOME=wd),
                       stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=timeout)
    return r.returncode, r.stdout.decode("latin-1")


def run_on_pty(binary, keys, timeout=10):
    """Run the simulator on a pseudo terminal, typing `keys` (list of byte
    strings, one per prompt); return everything it printed."""
    wd = config.workdir("pty")
    pid, fd = pty.fork()
    if pid == 0:                                # child
        os.chdir(wd)
        os.environ["HOME"] = wd
        os.execv(binary, [binary])
    out = b""
    deadline = time.monotonic() + timeout
    pending = list(keys)
    try:
        while time.monotonic() < deadline:
            r, _, _ = select.select([fd], [], [], 0.05)
            if r:
                try:
                    d = os.read(fd, 4096)
                except OSError:
                    break
                if not d:
                    break
                out += d
            elif pending and out.endswith(b"sim> "):
                os.write(fd, pending.pop(0))
            elif not pending and b"Goodbye" in out:
                break
    finally:
        try:
            os.kill(pid, 9)
        except OSError:
            pass
        os.waitpid(pid, 0)
        os.close(fd)
    return out.decode("latin-1")


def links_libedit(binary):
    try:
        r = subprocess.run(["ldd", binary], stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
    except OSError:
        return False
    return b"libedit" in r.stdout


class TestBuild(unittest.TestCase):

    def test_prompt_on_pipe(self):
        """SCP prints "sim> " when stdin is a pipe; the PANEL device exists."""
        rc, out = run_piped(CFG.bin, "show version\nshow panel\nexit\n")
        self.assertEqual(rc, 0, out)
        self.assertIn("sim> ", out)
        self.assertRegex(out, r"PDP-11 simulator Open SIMH")
        self.assertRegex(out, r"PANEL\s+export not attached")

    def test_panel_commands(self):
        """ATTACH/SHOW/SET REFRESH/DETACH, and ATTACH errors (also a file
        that other users could open; another user's file is refused the
        same way, but cannot be made here)."""
        wd = config.workdir("cmds")
        shm = os.path.join(wd, "panel.shm")
        link = os.path.join(wd, "link.shm")
        os.symlink(shm, link)
        fifo = os.path.join(wd, "fifo")
        os.mkfifo(fifo)
        shared = os.path.join(wd, "shared.shm")
        with open(shared, "w"):
            pass
        os.chmod(shared, 0o640)
        rc, out = run_piped(CFG.bin, "\n".join([
            "set panel refresh",                    # not attached
            "attach panel %s" % fifo,               # not a regular file
            "attach panel %s" % link,               # a symbolic link
            "attach panel %s" % wd,                 # a directory
            "attach panel %s" % shared,             # open to the group
            "attach panel %s" % shm,
            "show panel",
            "set panel refresh",
            "examine panel interval,samples,rate",
            "detach panel",
            "detach panel",
            "exit"]) + "\n")
        self.assertEqual(rc, 0, out)
        chunks = out.split("sim> ")
        self.assertIn("Unit not attached", chunks[1])
        self.assertIn("is not a regular file", chunks[2])
        self.assertIn("can't open %s" % link, chunks[3])
        self.assertIn("can't open %s" % wd, chunks[4])
        self.assertIn("%s is open to other users (mode 0640; it must be 0600)" % shared, chunks[5])
        self.assertEqual(os.stat(shared).st_size, 0, "a refused file was changed")
        self.assertEqual(chunks[6].strip(), "")
        self.assertRegex(chunks[7], r"export to %s, \d+ samples/sec \(target 50000\), "
                         r"interval 1000, 0 samples, halted" % re.escape(shm))
        self.assertEqual(chunks[8].strip(), "")
        self.assertRegex(chunks[9], r"INTERVAL:\s+0*1000\s+SAMPLES:\s+0+\s+RATE:\s+0+")
        self.assertEqual(chunks[10].strip(), "")
        self.assertIn("Unit not attached", chunks[11])
        st = os.stat(shm)
        self.assertEqual(st.st_size, panel.SIZE)
        # one simulator per file: a second one cannot attach while the first has it
        first = subprocess.Popen([CFG.bin], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                 stderr=subprocess.STDOUT, cwd=wd, env=dict(os.environ, HOME=wd))
        try:
            first.stdin.write(b"attach panel %s\necho attached\n" % shm.encode())
            first.stdin.flush()
            line = b""
            while b"attached" not in line:
                line = first.stdout.readline()
                self.assertTrue(line, "first simulator exited")
            rc2, out2 = run_piped(CFG.bin, "attach panel %s\nexit\n" % shm)
            self.assertIn("in use by another simulator", out2)
        finally:
            first.stdin.close()
            first.wait(10)
        self.assertEqual(st.st_mode & 0o777, 0o600)
        p = panel.Panel(shm)
        try:
            self.assertTrue(p.header_ok())
            self.assertEqual(p.u32("state"), panel.ST_HALTED)
        finally:
            p.close()

    def test_layout_matches_header(self):
        """simtest.panel's offsets equal the C header's (host C compiler)."""
        cc = shutil.which(os.environ.get("CC", "cc")) or shutil.which("gcc")
        if not cc:
            self.skipTest("no C compiler")
        wd = config.workdir("layout")
        src = os.path.join(wd, "offsets.c")
        fields = [f for f in panel.OFF]
        with open(src, "w") as f:
            f.write('#include <stdio.h>\n#include "pdp11_panel_shm.h"\nint main (void) {\n')
            for name in fields:
                f.write('printf ("%%s %%u\\n", "%s", (unsigned) offsetof (PANEL_SHM, %s));\n'
                        % (name, name))
            f.write('printf ("sizeof %u\\n", (unsigned) sizeof (PANEL_SHM));\n'
                    'printf ("sample %u\\n", (unsigned) sizeof (PANEL_SAMPLE));\n'
                    'printf ("constants %u %u %u\\n", PANEL_MAGIC, PANEL_VERSION, PANEL_RING);\n'
                    'return 0; }\n')
        exe = os.path.join(wd, "offsets")
        for std in ("-std=c11", "-std=gnu99"):      # C11 also runs the _Static_asserts
            subprocess.run([cc, std, "-Wall", "-Werror", "-I", os.path.join(config.SIMH_DIR, "panel"),
                            "-o", exe, src], check=True)
        out = subprocess.run([exe], stdout=subprocess.PIPE, check=True).stdout.decode()
        got = dict(l.split(" ", 1) for l in out.splitlines() if l and not l.startswith("constants"))
        for name in fields:
            self.assertEqual(int(got[name]), panel.OFF[name], name)
        self.assertEqual(int(got["sizeof"]), panel.SIZE)
        self.assertEqual(int(got["sample"]), 16)
        self.assertIn("constants %u %u %u" % (panel.MAGIC, panel.VERSION, panel.RING), out)

    def test_libedit(self):
        """With libedit, SCP still prompts on a pipe and uses line editing
        (history) on a terminal.  Uses the binary under test if it links
        libedit, else builds a variant against the host's libedit.so.2."""
        binary = CFG.bin
        if not links_libedit(binary):
            lib = next((os.path.join(d, "libedit.so.2") for d in SYSTEM_LIBDIRS
                        if os.path.exists(os.path.join(d, "libedit.so.2"))), None)
            if lib is None:
                self.skipTest("the host has no libedit")
            if not CFG.libedit_build:
                self.skipTest("libedit build disabled (--quick / --no-libedit-build)")
            binary = self.build_libedit_variant(lib)
        self.assertTrue(links_libedit(binary))
        rc, out = run_piped(binary, "show panel\nexit\n")
        self.assertEqual(rc, 0, out)
        self.assertIn("sim> ", out)
        self.assertRegex(out, r"PANEL\s+export not attached")
        # on a terminal libedit is used: up-arrow recalls the previous command
        out = run_on_pty(binary, [b"show panel\r", b"\x1b[A\r", b"exit\r"])
        self.assertEqual(out.count("export not attached"), 2, out)
        self.assertNotIn("Unknown command", out)

    def build_libedit_variant(self, lib):
        """build.sh with a two-line <editline/readline.h> and a libedit.so
        link to the system library (SimH uses only readline/add_history)."""
        wd = config.workdir("libedit")
        inc = os.path.join(wd, "include", "editline")
        os.makedirs(inc)
        with open(os.path.join(inc, "readline.h"), "w") as f:
            f.write("char *readline (const char *prompt);\nint add_history (const char *line);\n")
        libdir = os.path.join(wd, "lib")
        os.makedirs(libdir)
        os.symlink(lib, os.path.join(libdir, "libedit.so"))
        env = dict(os.environ, CC="%s -isystem %s" % (shutil.which("cc") or "gcc",
                                                       os.path.join(wd, "include")),
                   LDFLAGS="-L" + libdir)
        cmd = [os.path.join(config.SIMH_DIR, "build.sh"), "--src", os.path.join(wd, "src"),
               "--out", os.path.join(wd, "out")]
        repo = os.path.join(config.SIMH_DIR, "src")
        if os.path.isdir(os.path.join(repo, ".git")):
            cmd += ["--repo", repo]
        r = subprocess.run(cmd, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        log = r.stdout.decode("latin-1")
        self.assertEqual(r.returncode, 0, log[-2000:])
        self.assertIn("using libedit", log)
        config.report("libedit build: %s (prompt on a pipe and line editing on a pty checked)"
                      % os.path.join(wd, "out", "pdp11"))
        return os.path.join(wd, "out", "pdp11")


if __name__ == "__main__":
    sys.exit("run via run_tests.py")
