# Systems catalog

One directory per operating system. The schema is `server/internal/catalog/catalog.go`
(`docs/DESIGN.md` §7); validate with

```
go run ./server/cmd/catalogcheck systems
```

and boot-test every entry with the boot matrix (`tests/systems/README.md`):

```
python3 -I tests/systems/bootmatrix.py --boots 2 --second-boot
```

| id | System | Disks | Memory | Speed | Licence | Login | Extra lines |
|---|---|---|---|---|---|---|---|
| `bsd211` (default) | 2.11BSD patch level 482 (Chase Covello, 2025) | RA82 1 GB | 4M | fast | redistributable | root, no password | DZ11 8 lines, modem control, on |
| `bsd211-pl499` | 2.11BSD patch level 499 (Johnny Billquist, 2026) | RA82 1 GB | 4M | fast | redistributable | root, no password | DZ11 8 lines, modem control, off in /etc/ttys |
| `unixv7` | Research UNIX V7 (Bostic distribution, w11 kit) | RP04 | 2M | fast | redistributable | root / root | - |
| `unixv6` | Research UNIX V6 (SimH kit) | 4 x RK05 | 1M | fast | redistributable | root, no password | - |
| `unixv5` | Research UNIX V5 (SimH kit) | RK05 | 1M | fast | redistributable | root, no password | - |
| `rt11` | RT-11 V5.3 (SimH kit distribution disk) | RL02 | 4M | authentic | hobbyist | none | - |
| `rsx11mp` | RSX-11M-PLUS V3.0 BL24 (w11 kit) | RP06 | 4M | fast | hobbyist | none (console is logged in) | DZ11 8 lines (TT2:-TT11:) |
| `rsts` | RSTS/E V7.0-07 (SimH kit, Chuck Cranor) | 2 x RL01 | 1M | fast | hobbyist | HELLO 1,2 / SYSTEM | - |

## Files

* `system.json`: every field of `catalog.System`. `notes` is Markdown with the sections
  `## Log in`, `## Things to try` and `## Shut down`; everything stated there (commands,
  addresses, programs) was tried on the image.
* `boot.ini`: device setup only, appended to pdp11d's generated header (CPU 11/70, memory,
  speed, console, `set tto 7b`, panel export). Allowed: `set <device>`, `attach -e <unit>
  ${DISKn}`, `deposit`, `echo`, comments. pdp11d appends `attach -u [-m] <device> <listener>`
  for `terminals` itself when `PDP11_TTY_TELNET` is set.
* `dialog.ini`: the boot dialogue, `expect`/`send` lines only, written into `dialog.do`
  after `noexpect` and `nosend` and executed with `do dialog.do` right before every boot
  command (so SimH's `%DATE_…%`/`%TIME_…%` values are fresh). Every `expect` action ends in
  `; continue`. Answers to RSX and RSTS prompts use `send -t after=250000,"…"` (typed 0.25 s
  after the prompt; with SimH's default of 1000 instructions RSX sometimes has not posted its
  read yet and hands the line to MCR). Polled prompts (boot loaders, RT-11's `.`) keep
  instruction-based delays.
* `LICENSE.txt`: the licence text shown to users: the Mentec hobbyist licence (verbatim from
  the w11 kits) for RT-11, RSX and RSTS; Caldera's 2002 Ancient UNIX licence (transcribed from
  the kits' PDF) for V5-V7; BSD + Caldera for 2.11BSD.

## Conventions

* `readyPattern`, and the `expect` of every `shutdownScript` and `tests.session` step, are
  literal, case-sensitive substrings of the console output (like SimH `EXPECT` without `-r`).
  The ready patterns avoid regular-expression metacharacters other than `.`.
* A step's `expect` only sees console output produced after the previous step's `send`.
  pdp11d types a `shutdownScript` step's `send` 250 ms after its `expect` matched (RSX and
  RSTS post their terminal read late).
* `shutdownScript` either ends with the command that makes the OS halt itself (2.11BSD
  `shutdown -h now`, RSX `RUN $SHUTUP`; the CPU then stops with `HALT instruction`) or with
  an `expect`-only step that marks the safe point (Unix V5-V7 after the third `sync`, RSTS at
  INIT's `Option:` prompt after `SHUTUP`); pdp11d powers off 3 s after the latter. RT-11 has
  no script: it has nothing to shut down.
* `shutdownScript` runs on a console in any state, so it looks before it types; a user's work
  matters more than a clean stop, and no script sends an interrupt (`docs/DESIGN.md` §7 has the
  full rules and what each state leads to). Besides `expect`, `send` and `timeoutSec` a step can
  have:
  * `ifLineEnds`, a list of texts: the step runs only if the console output, once silent for
    300 ms, ends in one of them, and is skipped otherwise. In a text `\n` stands for a line end
    (blank lines and fill do not count) and `*` for anything on the same line.
  * `expectAny`, a list of texts: waits for the first that appears. Unlike `expect` it leaves
    what it saw for the next step's wait, so that the steps after a branch can fall through.
  * `else`: when `ifLineEnds` does not hold or the wait times out, the script fails at once with
    this text, shown after "Shut down OS: " in the toast and in the log of a daemon stop. It says
    what to do.

  The first step must have `ifLineEnds` and `else`, the last step has no `ifLineEnds`, `send` holds
  no interrupt, and other control characters (Ctrl+D, Ctrl+U) only with `ifLineEnds`. `catalogcheck`
  checks this and the texts, and `tests.session` cannot use these fields. The prompts:

  | id | Login prompt | Root or privileged | Other users | Clearing a half-typed name |
  |---|---|---|---|---|
  | `bsd211` | `login: ` | `# ` | `user[1] `, `tektronix[1] ` (tcsh), `% ` (csh), `$ ` (sh) | Ctrl+U |
  | `bsd211-pl499` | `login: ` | `# ` | `> ` (`user`, tcsh; the shutdown script refuses it, a shell's continuation prompt looks the same), `% ` (csh), `$ ` (sh) | Ctrl+U |
  | `unixv7` | `login: `, then `Password:` | `# ` | `$ ` (`dmr`, `bin`, `sys`: no password) | `@` |
  | `unixv6` | `login: `, after a failed login `Name: ` | `# ` | `% ` (`ken`, `bin`, `daemon`, `lvd`: no password) | `@` |
  | `unixv5` | `login: `, after a failed login `Name: ` | `# ` | `% ` (`bin`, `daemon`: no password) | `@` |
  | `rsx11mp` | none: `>` after `BYE`, `HELLO SYSTEM`, `Password: ` | `>` | `>`: `RUN $SHUTUP` says "Can't install privileged task" | Ctrl+U |
  | `rsts` | none: no prompt when logged out, `HELLO 1,2`, `Password:` | `Ready` | `Ready`: `RUN $SHUTUP` says "Protection violation", `HELLO 1,2` works | Ctrl+U |

  2.11BSD's syslog prints on the console where the cursor is, for instance `Mar 30 02:31:38 pdp11
  login: ROOT LOGIN ON tty00` after the prompt; `ifLineEnds` and the first wait do not count such
  lines. RSTS has no prompt while logged out: the scripts take `Detaching . . .`, `Bye`, `BYE/F` and
  `Good ...` (morning, afternoon, evening) as the last line with text for it.
* Operating systems that are not Y2K-ready get the 19xx year whose calendar matches today's
  (`%DATE_19XX_YY%`), so weekdays are right: RT-11, RSX-11M-PLUS V3.0, RSTS/E V7 and Unix V7
  (whose clock is set in single-user mode during the boot).
* Console keys: V5-V7 use `#` to erase and DEL to interrupt (`backspace: "#"`,
  `interrupt: "del"`); everything else erases with DEL and interrupts with Ctrl+C.
* Archive member paths are written exactly as stored in the archive (the w11 tar files store
  `./name`).
* Mirrors are byte-identical copies in the Internet Archive (`web.archive.org/web/<ts>id_/…`
  returns the original bytes); each was downloaded and checked against `sha256`.

## Sources and known issues

* 2.11BSD pl482 is fetched from `raw.githubusercontent.com` pinned to commit
  `b054907c6c5dd507a387c62e7e4b2dd48e284ad1` (the last commit that changed
  `2.11BSD_rq.dsk.xz`, "Updated system to patch level 482", 2025-04-03). Its kernel expects a
  second DL11 (`cn 1`), so `boot.ini` enables one DLI line, which moves the DZ11 to vector 310.
  There is no byte-identical HTTPS mirror (jsDelivr refuses files over 20 MB, the Internet
  Archive has no copy).
* 2.11BSD pl499 exists only on anonymous FTP (`ftp.dfupdate.se`). The file is regenerated
  upstream from time to time (2026-10-18, 2026-10-27; the pin is the latter, older files are
  not kept anywhere); if it changes again the checksum no longer matches and the install fails
  cleanly and the entry needs a new pin. Its image ships with `rc_configured=NO` in
  `/etc/rc.conf`, which stops `/etc/rc` at a root shell ("Automatic multiuser boot aborted"), so
  the first boot's `dialog.ini` rule sets `rc_configured=YES` and `sendmail=NO` there and
  reboots; rc.conf's other daemons are already off, and there is no network device (`xu` is
  disabled, `/etc/rc` still starts inetd, which sees only `lo0`). Its `pidp` kernel counts 50 clock ticks per second, so `boot.ini` sets the
  KW11-L to 50 Hz (at SimH's default 60 Hz the clock gained 12 s a minute;
  `tests/systems/bootmatrix.py --clock` checks it).
* After an unclean stop while the disks were being written, `fsck -p` in 2.11BSD's `/etc/rc` can
  meet an inconsistency it may not repair alone ("UNEXPECTED INCONSISTENCY; RUN fsck MANUALLY",
  "Automatic reboot failed... help!") and leave a root shell, at every later boot too (measured:
  6 of 12 SIGKILLs during a `cp -r` on `bsd211`, 2 of 12 on `bsd211-pl499`). Both entries have a
  persistent `dialog.ini` rule on that prompt: `fsck -y -t /_FSCK_TMP_FILE` (without `-t` fsck asks
  "NEED SCRATCH FILE ... ENTER FILENAME:" for `/usr` and nobody answers), then `fsck -p` as a
  check, and `reboot -n` if it passes (no sync: the kernel's copy of a repaired root is stale). On
  a disk fsck cannot repair (tried with the superblock of the last partition destroyed) the check
  ends with status 8: the console says "fsck cannot repair this disk" and stays at the root shell
  rather than rebooting for ever, the boot-timeout warning comes, and Shut down OS works from that
  shell. Factory reset restores the disk.
* `www.retro11.de` (w11 kits: V7, RSX) answered HTTP 503 for every request on 2026-10-22; the
  Internet Archive mirrors listed for those kits were verified the same day.
* The UNIX V6 kit's root file system has a damaged free list (`icheck /dev/rk0`: `3 dups in
  free`), which cross-links new files and breaks `cc`. The notes explain the one-time repair
  (`icheck -s /dev/rrk0`, then pull the plug and boot again); the image is not modified.
* The RSX-11M-PLUS kit counts 50 clock ticks per second, so `boot.ini` sets the KW11-L to 50 Hz,
  as for 2.11BSD pl499 (at SimH's default 60 Hz the clock gained 12 s a minute).
* RT-11 V5.3 comes as the distribution disk: its first boot answers the RT11AI questions
  (RETURN, NO, RETURN), which writes the RT11FB bootstrap and reboots; later boots go straight
  to RT-11FB. RT-11 never executes WAIT, hence `speed: authentic`.
* No extra terminal lines for UNIX V7 (its kernel has only the hp and tm drivers), V5/V6
  (only the console exists in `/dev`), RT-11 (no multi-terminal monitor) and RSTS/E V7 (the
  monitor never enables the DZ11's master scan, research r3 §5).
