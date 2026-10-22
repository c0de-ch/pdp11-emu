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
  INIT's `Option:` prompt after `SHUTUP`); pdp11d powers off 3 s after the latter. The
  scripts work both at the login prompt and in a logged-in session (the Unix scripts type
  `root` first: it logs in at `login:` and is a harmless "not found" in a shell). RT-11 has
  no script: it has nothing to shut down.
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
* 2.11BSD pl499 exists only on anonymous FTP (`ftp.dfupdate.se`). The file was regenerated
  upstream on 2026-10-18; if it changes again the checksum no longer matches and the install
  fails cleanly. Its `pidp` kernel counts 50 clock ticks per second, so `boot.ini` sets the
  KW11-L to 50 Hz (at SimH's default 60 Hz the clock gained 12 s a minute;
  `tests/systems/bootmatrix.py --clock` checks it).
* `www.retro11.de` (w11 kits: V7, RSX) answered HTTP 503 for every request on 2026-10-22; the
  Internet Archive mirrors listed for those kits were verified the same day.
* The UNIX V6 kit's root file system has a damaged free list (`icheck /dev/rk0`: `3 dups in
  free`), which cross-links new files and breaks `cc`. The notes explain the one-time repair
  (`icheck -s /dev/rrk0`, then pull the plug and boot again); the image is not modified.
* RT-11 V5.3 comes as the distribution disk: its first boot answers the RT11AI questions
  (RETURN, NO, RETURN), which writes the RT11FB bootstrap and reboots; later boots go straight
  to RT-11FB. RT-11 never executes WAIT, hence `speed: authentic`.
* No extra terminal lines for UNIX V7 (its kernel has only the hp and tm drivers), V5/V6
  (only the console exists in `/dev`), RT-11 (no multi-terminal monitor) and RSTS/E V7 (the
  monitor never enables the DZ11's master scan, research r3 §5).
