# R3: OS disk images for an Open SIMH PDP-11/70, with fully automated boot

Researcher r3, 2026-10-21. Everything marked **VERIFIED** was run or read on this host: Ubuntu 26.04 VM, 16 vCPU ("QEMU Virtual CPU version 2.5+"), uid 1000, no root. **UNVERIFIED** means memory, inference or a third-party statement I did not reproduce.

Working directory: `$SCRATCH/r3-images` (called `$R3` below).

`$SCRATCH` stands for the research session's scratch directory, an ephemeral working directory under `/tmp` that no longer exists.

| What | Where |
|---|---|
| Pristine downloads (one dir each, plus extracted `x/`) | `$R3/dl/<name>/` |
| Working disk copies (configs attach these, by absolute path) | `$R3/run/<system>/` |
| Final SimH configs | `$R3/configs/<system>/boot.ini` |
| Harness and helper scripts | `$R3/scripts/` (`simexp.py`, `oscheck.py`, `runall.sh`, `qa.py`, `explore.py`, `nettest.py`, `dztest.py`, `lineprobe.py`, `r0trace.py`, `cpumeasure.py`, `rsxrace.py`, `finaltable.py`, `rsxtt.py`, `rstshw.py`, `rstsdz.py`, `rststty.py`, `nonzero.py`) |
| Per-run JSON and console transcripts | `$R3/results/`, and the final batch in `$R3/results/final/` |

`runall.sh` copies each system's disks from `dl/` into `run/` and boots it twice, so every number in section 2 can be re-created.

---

## 0. TL;DR

* **Simulator (VERIFIED).** Open SIMH was built from `git clone --depth 1 https://github.com/open-simh/simh`, commit `87eb7d5e96f9`, using `make -j16 TESTS=0 pdp11`. The build took 18.7 s. It has the TAP and NAT (slirp) Ethernet transports, but no libpcap, VDE, SDL video or PCRE. Without PCRE, `EXPECT -r` (regular expressions) is not available. Every config below uses plain-string EXPECT.
* **What was tested (VERIFIED).** 14 configurations boot under `set cpu 11/70` to a login or command prompt with no human input:
  * Boot-time questions are answered by SimH `EXPECT`/`SEND` in the `.ini`.
  * A Python pty harness (stdlib only) then logs in, runs a command, samples DR, R0, PC and WAIT while the system is idle, measures host CPU, and shuts the OS down cleanly before quitting SimH.
  * One configuration **fails**: the SimH-kit Unix V7 RL02 image does not boot on an 11/70 (section 3.4). One is only partly done: the RSX image from the PiDP-11 bundle.
* **Automation pitfall (VERIFIED, fixed).** A `SEND` inside an `EXPECT` action fires only 1000 instructions after the match (SimH default). RSX sometimes has not posted its terminal read by then. The answer then goes to MCR ("MCR -- Illegal function"), and the boot dialogue hangs or loses a minute.
  * Seen once in the first final batch, and in 2 of 16 race-test boots with all 16 host CPUs busy (0 of 8 without the extra load).
  * With `send -t after=250000,"…"` (type the answer 0.25 s after the prompt), 24 of 24 boots on the loaded host passed. The RSX and RSTS configs now use it (section 8, item 10).
* **Default (recommended): 2.11BSD patch level 499** (`ftp://ftp.dfupdate.se/pub/pdp11/2.11BSD/pidp-pl499.dsk.gz`, 47.9 MB compressed, 1.02 GB MSCP disk).
  * It needs no boot prompts and reaches a multi-user `login:` in about 9 s. Log in as root with no password.
  * Host CPU while idle is 2–4 %.
  * DZ11 logins over telnet work, and so does TCP/IP over the unprivileged SimH NAT.
  * Its licence (BSD plus Caldera) allows the appliance to ship it.
* **Front-panel "idle patterns" (VERIFIED by source and by experiment).**
  * Only **Unix V5, V6 and V7 write the display register 17777570.** On every clock tick, `display()` copies the word whose address is set in the switch register into DR.
  * **2.11BSD, RSX-11M-PLUS V3.0 and V4.6, and RSTS/E V7 leave DR at 0.** Each one rotates its famous pattern in **R0** and then executes `WAIT`. On a real 11/70 that shows on the DATA lamps, in the DATA PATHS knob position, while the CPU waits.
  * **RT-11 never executes WAIT.** It spins, using 100 % of a host core unless SimH throttles it.
* **Licences.**
  * Unix V1–V7 and 2.11BSD can be redistributed (Caldera 2002 licence, BSD licence).
  * RT-11 ≤ V5.3, RSX-11M-PLUS ≤ V3.0, RSX-11M ≤ V4.3 and RSTS/E ≤ V9.6 fall under the 1998 Mentec hobbyist licence: personal, non-commercial use only.
  * **RSX-11M-PLUS V4.6 (the best RSX image) is not covered by any known licence.** Offer it as a user-initiated download; do not bundle it.

---

## 1. Method

### 1.0 Build (VERIFIED)
```
$ git clone --depth 1 https://github.com/open-simh/simh        # HEAD 87eb7d5e96f9ce0ee6ac183e20160e5c486b0712
$ make -j16 TESTS=0 pdp11
*** pdp11 Simulator being built with:
*** - compiler optimizations and no debugging support. GCC Version: 15.2.0.
*** - Local LAN packet transports: TAP NAT(SLiRP)
*** - Per simulator tests will be skipped.
real 0m18.696s
$ BIN/pdp11 → "PDP-11 simulator Open SIMH V4.1-0 Current  git commit id: 87eb7d5e"
  SHOW VERSION: "Threaded Ethernet Packet transports:TAP:NAT:UDP", "No RegEx support for EXPECT
  commands", "SDL Video support: No Video Support", "FrontPanel API Version 12"
```
The build warned that libpcap, VDE, SDL2/libpng and PCRE were missing. None of them is needed for the configs below.

### 1.1 Harness (VERIFIED)

`$R3/scripts/simexp.py` is a small expect library built on stdlib `pty`:

* `pty.fork()` runs `pdp11 <ini>`.
* Console output is stripped of the parity bit, logged to a file, and time-stamped one line at a time (`timeline`).
* `expect()` matches a regex against the earliest unconsumed output.
* `send()` types one character every 30 ms, so that slow console drivers do not lose typeahead.
* `wru()` sends ^E and waits for `sim>`.
* `simcmd()` runs one SCP command.
* `cpu_seconds()` reads utime+stime from `/proc/<pid>/stat`.
* `close()` sends SIGKILL to the whole process group after a timeout. Killing only the direct child once left an orphaned simulator holding ports and a disk image, when strace was used as a wrapper.

`$R3/scripts/oscheck.py SYSTEM` does the following:

1. Boots `configs/SYSTEM/boot.ini`.
2. Waits for the prompt and records the mark `login_prompt`.
3. Logs in (mark `shell`).
4. Runs a command and checks its output (`cmd_ok`).
5. Takes 8 samples. Each sample is ^E, then `examine DR`, `R0`, `PC`, `PSW` and `WAIT`, then `examine -v -m <pc>`, then `c`.
6. Measures host CPU over 10 s while the system is idle.
7. Runs the OS-specific clean shutdown until the CPU halts or the system is otherwise safe, then `quit` (mark `halted`, `exit_ok`).

All Python that touched downloaded data ran with `python3 -I`. No downloaded code was executed. Only SimH, built from source, ran the disk images.

### 1.2 Timing and CPU

* Times are wall-clock seconds from simulator start, with SimH unthrottled, `set cpu idle`, on this VM.
* `idle_cpu_pct` is the simulator's host CPU use over 10 s at the idle prompt, after the DR samples.

---

## 2. Results matrix (all on `set cpu 11/70`)

The numbers are from the final batch `$R3/scripts/runall.sh`, which boots each system twice from a fresh copy of the pristine download. Where the two boots differ, the matrix gives the range; section 2.1 lists every boot. Raw data: `$R3/results/final/*.json`.

| ID (`configs/…`) | OS / version | Disk → SimH device | Mem | Automated prompts (in .ini) | Login | Prompt reached (s) | Idle host CPU | Clean shutdown → time |
|---|---|---|---|---|---|---|---|---|
| **bsd499** | 2.11BSD **pl 499** (Jan 2026), kernel `pidp` | 1 × RA (MSCP UDA50) `rq0 rauser=1024`, 1.02 GB | 4 MB | boot-loader countdown skipped (`expect -p "Press <CR> to boot"`) | `root`, no password | **8.7–8.8** | 2.8–3.4 % | `shutdown -h now` → "halting" + HALT, 13 s |
| bsd482 | 2.11BSD pl 482 (Chase Covello, 2025) | RA `rauser=1000`, 1.0 GB | 4 MB | countdown skipped | `root`, `user` or `tektronix`, all without password | 10.3 | 2.2–3.2 % | `shutdown -h now`, 16 s |
| bsdw11 | 2.11BSD pl 447 + fpsim fix (w11, 2019) | RP06 on RH70, 174 MB | 4 MB | `:` → CR; single-user `#` → ^D | `root` | 9.2–9.3 | 2.3–2.4 % | `shutdown -h now`, 13 s |
| **v7pidp** | Research Unix V7 (PiDP-11 bundle) | RP06, 174 MB | 1 MB (2 MB works; 4 MB fails) | silent boot block: SEND "boot"; `:` → `hp(0,0)unix`; `#` → ^D | `root` / `root` | **1.9–2.0** | 1.3–3.1 % | `sync`×3, then ^E `quit`, 4 s |
| v7w11 | Unix V7 Keith Bostic distribution (w11) | RP04, 88 MB | 2 MB | same as v7pidp | `root` / `root` | 1.9–2.1 | 0.9–2.3 % | `sync`×3, 4 s |
| v7simh | Unix V7 (simh kit uv7swre) | RL02 | – | – | – | **FAILS on 11/70** (works on 11/45) | – | – |
| **v6simh** | Research Unix V6 (simh kit) | 4 × RK05 | 1 MB (V6 uses ≤ 248 KB) | `@` → `rkunix` | `root`, no password | **1.1–1.2** | 1.7–1.8 % | `sync`×3, 4 s |
| v5simh | Research Unix V5 (simh kit) | 1 × RK05 | 1 MB | `@` → `unix` | `root`, no password | 1.1–1.2 | 0.6–0.8 % | `sync`×3, 4 s |
| **rt11pidp** | RT-11SJ V05.03 + games, BASIC, FOCAL, TECO, C (PiDP-11 bundle) | RL02 (+ RL02 DEC-11-Graphics) | 4 MB | `DATE dd-mmm-19xx` / `TIME` sent at first `.` | none (`.` prompt) | **1.2** | **100 %** (≈ 11 % with `set throttle 1M`) | none needed; ^E `quit` |
| rt11w11 | RT-11FB V05.03 working disk (w11) | RL02 | 4 MB | DATE and TIME after the startup file | none | 1.2–1.3 | 100 % | none needed |
| rt11v53 | RT-11 V5.3 **distribution** disk (simh kit) | RL02 | 4 MB | first boot: RETURN, NO, RETURN converts it to RT11FB, then DATE and TIME | none | 1.4 first boot / 1.2 | 100 % | none needed |
| rt11v4 | RT-11SJ V04.00C (simh kit) | RK05 | 4 MB | DATE and TIME | none | 1.0 | 100 % | none needed |
| **rsxbq** | **RSX-11M-PLUS V4.6 BL87** + DECnet, BQTCP/IP, RPM (J. Billquist) | RA `rauser=1024`, 1.02 GB | 4 MB | 15 first-boot set-up prompts; on every boot, time and date (4-digit year), Start CEX, Start TCP/IP, crash explanation | `SYSTEM`/`SYSTEM` [1,1] MCR, `USER`/`USER` [200,1] DCL | **19.1** (logged-off `>`; 23.6 on the first boot) | 1.2–1.6 % | `RUN $SHUTUP`: `0`, CR, `Y` → "SHUTUP operation complete" + HALT, 14 s |
| rsx30 | RSX-11M-PLUS V3.0 BL24 (w11) | RP06, 174 MB | 4 MB | `dep STOP_TRAPS 0`; `set dli enabled` (DZ at vector 310); time and date (2-digit year) | console already logged in as [200,200] | 9.0–10.7 | 1.8–2.0 % | `RUN $SHUTUP`, 12 s |
| rsxpidp | RSX-11M-PLUS V4.6 BL87 (PiDP-11 bundle, 2018) | RA autosize, 315 MB | 4 MB | partly worked out; date prompt has a 1 min timeout, LAT and TCP/IP 15 s | not completed | about 90 (DECnet and LAT time out) | – | – |
| **rsts7** | RSTS/E V7.0-07 "llama" (simh kit) | 2 × RL01 | 1 MB | `Option:` LF; `Table suboption?` LF; date (19xx); time; `Command File Name?` CR | `HELLO 1,2` / `SYSTEM`; at "Job number to attach to?" press CR | **10.6–10.7** | 1.4–1.5 % | `RUN $SHUTUP`: CR, CR, `0` → reboots to `Option:`; then ^E `quit`, 8 s |

### 2.1 Every boot of the final batch (VERIFIED)

Each row is one boot. Boot 1 starts from the pristine image; boot 2 re-boots the same image.

* **Prompt** and **Logged in** are seconds from simulator start. RT-11 has no login.
* **Command OK** means the test command's output was right.
* **Idle host CPU** is measured at the idle prompt.
* **Shutdown** is the length of the clean shutdown. **SimH exited** means SimH quit on its own after `quit`.

rsxbq, rsx30 and rsts7 were re-run after their answers got the 0.25 s delay (section 8, item 10); rsx30 also got the second DL11.
Their earlier runs are kept in `$R3/results/final-pre-after/`. One of them is the rsxbq first boot that hung and exposed the race.
The table is produced by `scripts/finaltable.py` and saved as `results/final/table.md`.

| System | Boot | Prompt (s) | Logged in (s) | Command OK | Idle host CPU % | Shutdown (s) | SimH exited | Error |
|---|---|---|---|---|---|---|---|---|
| bsd499 | 1 | 8.68 | 8.84 | yes | 3.4 | 12.9 | yes |  |
| bsd499 | 2 | 8.76 | 8.91 | yes | 2.8 | 12.9 | yes |  |
| bsd482 | 1 | 10.31 | 10.46 | yes | 3.2 | 16.1 | yes |  |
| bsd482 | 2 | 10.35 | 10.53 | yes | 2.2 | 16.1 | yes |  |
| bsdw11 | 1 | 9.3 | 9.49 | yes | 2.4 | 13 | yes |  |
| bsdw11 | 2 | 9.18 | 9.36 | yes | 2.3 | 13 | yes |  |
| v7pidp | 1 | 1.93 | 2.24 | yes | 3.1 | 3.7 | yes |  |
| v7pidp | 2 | 1.96 | 2.29 | yes | 1.3 | 3.7 | yes |  |
| v7w11 | 1 | 1.91 | 2.21 | yes | 0.9 | 3.7 | yes |  |
| v7w11 | 2 | 2.08 | 2.39 | yes | 2.3 | 3.7 | yes |  |
| v6simh | 1 | 1.09 | 1.3 | yes | 1.7 | 3.9 | yes |  |
| v6simh | 2 | 1.18 | 1.39 | yes | 1.8 | 3.9 | yes |  |
| v5simh | 1 | 1.15 | 1.39 | yes | 0.8 | 3.8 | yes |  |
| v5simh | 2 | 1.1 | 1.34 | yes | 0.6 | 3.7 | yes |  |
| rt11pidp | 1 | 1.15 | – | yes | 99.9 | 0.3 | yes |  |
| rt11pidp | 2 | 1.19 | – | yes | 100 | 0.3 | yes |  |
| rt11w11 | 1 | 1.32 | – | yes | 100 | 0.3 | yes |  |
| rt11w11 | 2 | 1.18 | – | yes | 100 | 0.3 | yes |  |
| rt11v53 | 1 | 1.37 | – | yes | 100 | 0.3 | yes |  |
| rt11v53 | 2 | 1.18 | – | yes | 100 | 0.3 | yes |  |
| rt11v4 | 1 | 0.99 | – | yes | 99.9 | 0.3 | yes |  |
| rt11v4 | 2 | 1.01 | – | yes | 100 | 0.3 | yes |  |
| rsxbq | 1 | 23.59 | 28.19 | yes | 1.2 | 14.3 | yes |  |
| rsxbq | 2 | 19.07 | 25.61 | yes | 1.6 | 14.2 | yes |  |
| rsx30 | 1 | 9 | 9 | yes | 2 | 12.2 | yes |  |
| rsx30 | 2 | 10.67 | 10.67 | yes | 1.8 | 12.2 | yes |  |
| rsts7 | 1 | 10.63 | 11.17 | yes | 1.4 | 7.9 | yes |  |
| rsts7 | 2 | 10.72 | 11.27 | yes | 1.5 | 8.1 | yes |  |

---

## 3. Per-system details

### 3.1 2.11BSD patch level 499 (recommended default), VERIFIED

**Download.** `ftp://ftp.dfupdate.se/pub/pdp11/2.11BSD/pidp-pl499.dsk.gz`
* 47,883,044 bytes, sha256 `634d0626d898bdaa14070acd8671c09f5d587c8ed96b9e970f9f0806c40a8e7b`.
* The FTP listing gives it the same size as `pidp-latest.dsk.gz` (dated Oct 18). It unpacks to `pidp-pl499.dsk`: 1,024,458,752 bytes, sha256 `8e367779…7e4eb8fe`.

**Matching distribution tape (optional, for reinstalling).** `pidp-pl499.tap.gz`
* 24,892,185 bytes, sha256 `fb4f330d…d6ab`. Unpacked: 82,107,056 bytes.

**Notes.**
* The site is Johnny Billquist's, the former `ftp.update.uu.se`. `Patches/` there runs up to patch 499, dated 13 Jan 2026.
* `/VERSION` inside the image reads "Current Patch Level: 499, Date: January 2, 2026".
* The kernel is `2.11 BSD UNIX #5: Wed Apr 15 03:49:47 GMT 2026 root@pidp.example.com:/usr/src/sys/pidp`.
* The hostname is `pidp.bqtnet.se`.

**Other 2.11BSD sources checked (listings only, nothing downloaded).**
* TUHS moved 2.11BSD to `https://www.tuhs.org/Archive/Distributions/UCB/2BSD/2.11BSD/`. The old `UCB/2.11BSD/` path returns 404.
* It holds the original pl431 tape files and a `Patches/` directory (modified 2026-10-14).
* `UCB/2BSD/2.11BSD-patch481/` has `2.11BSD-481-simh-dist.tap` (86M, 2024-09-12) and `pdp11-dist-system.ini`. That is an installation tape, not a ready-made disk, and pl481 is older than pl499.
* The w11 oskits also carry pl447 on RK, RL and RP (`211bsd_rkset`, `211bsd_rlset`, `211bsd_rpset`, `211bsd_rpethset`, `211bsd_rpminset`, `211bsd_tmset`).

**Hardware it expects.**
* MSCP controller at 172150 / vector 154, with an RA drive. SimH reports `RQ0 1024MB … RA8U`; the kernel prints `ra0: RA82 size=2000896`.
* DZ11 at 160100 / vector **300**, which is SimH's autoconfig default when no second DL11 is enabled.
* DEUNA/DELUA `de0` at 174510 / vector 120, when `xu` is enabled.
* `/etc/dtab` also lists rl, xp and tms, which are probed and then skipped.

**Boot.**
1. The new boot loader prints `Press <CR> to boot, or any other key to abort: 543210`.
2. It then auto-boots `ra(0,0,0)unix` straight into multi-user (AUTOMULTIUSER). The file systems have the clean flag set, so `fsck` is skipped.
3. A persistent EXPECT that sends CR saves the 5 s countdown.

Accounts: `root` with no password.

`$R3/configs/bsd499/boot.ini`:
```
set cpu 11/70 4M
set cpu idle
set tto 7b
set rq enabled
set rq0 rauser=1024
attach rq0 $R3/run/bsd499/pidp-pl499.dsk
set rl disabled
set rp disabled
set tq disabled
set dz enabled
set dz lines=8
set dz 7b
attach -m dz 4000
set xu disabled
expect -p "Press <CR> to boot" send "\r"; continue
boot rq0
```

Timeline (`results/bsd499.json`; the gap after autoconfig is normal):
```
 1.04  : ra(0,0,0)unix
 1.17  2.11 BSD UNIX #5: Wed Apr 15 03:49:47 GMT 2026
 1.51  dz 0 csr 160100 vector 300 attached
 1.51  ra 0 csr 172150 vector 154 vectorset attached
 6.83  Automatic reboot in progress...
 6.84  /dev/ra0a: file system is clean; not checking
 7.16  Assuming NETWORKING system ...
 7.26  Setting date via ntp / No such host: pool.ntp.org      (no network attached)
 9.34  2.11 BSD UNIX (pidp.bqtnet.se) (console)  login:
```

**Things to know.**
* With no network, the clock starts from the date in the root superblock (May 2026). The 11/70 has no time-of-year clock.
* With NAT networking, `ntp -s` set the real date during boot (section 6).
* `/etc/ttys` ships with `tty00`–`tty07` set to **off**. Enable them for DZ logins (section 5).
* `/etc/rc.conf` holds the network settings: hostname, address, netmask, gateway, `ntp`, `ntpd` and `sendmail`.

**Shutdown.** `shutdown -h now` prints "syncing disks... done / halting". SimH then shows `HALT instruction, PC: 000014`, and you `quit`.

### 3.2 2.11BSD patch level 482 (Chase Covello), VERIFIED

**Download.** `https://raw.githubusercontent.com/chasecovello/211bsd-pidp11/master/2.11BSD_rq.dsk.xz`
* 24,201,968 bytes, sha256 `b3e6b832…f1d0`.
* Unpacked: 1,000,448,512 bytes, sha256 `ddd69727…7abf`. The file already carries a SimH metadata footer (`simhPDP-11 RA8U`).
* The repository has no GitHub releases. The last commit is "Updated system to patch level 482" on 2025-04-03.

**System.**
* Kernel `#21: Fri Mar 28 07:58:13 PDT 2025 root@pdp11:/usr/src/sys/PIDP11`; `ra0: RA82 size=1954000`.
* The kernel expects the **DZ at vector 310** and a second DL11 (`cn 1 csr 176500 vector 300`).
* `set dli enabled` / `set dli lines=1` makes SimH autoconfig put the DZ at 310 without an explicit `set dz vector=310`. An explicit vector switches SimH autoconfig off for every device configured after it; SimH prints a warning.

**Accounts.** root, user and tektronix, all with no password (README). Several network services are enabled: telnet, ftp, rsh, httpd.

**First-boot caveat.** If the image was ever stopped without a clean shutdown, the first boot runs fsck, marks the file systems clean and **reboots by itself**. That took 35 s in an early test. A pristine copy boots in 10.3 s.

The config is the same as bsd499 apart from `rauser=1000`, the `dli` lines and `attach -m dz 4001`.

### 3.3 2.11BSD patch level 447 (w11 oskit), VERIFIED

**Download.** `https://www.retro11.de/data/oc_w11/oskits/211bsd_rpset.tgz`
* 35,814,978 bytes, sha256 `91737beb…f6f7`.
* Contents: `211bsd_rp.dsk` (174,158,848 bytes, an RP06 image), `Caldera-license.pdf`, `BSD-license.txt`, `README_license.txt`.
* This site returned 503 for the first directory listing (HEAD returned 200). A retry minutes later worked.

**Boot.** The old boot loader shows the `:` prompt, preceded by `\n\r\x7f` fill characters, so `expect "\r\n: "` never matches; `expect ": "` does. CR boots `xp(0,0,0)unix`. Single-user `#` → ^D → multi-user.

**Pitfall (VERIFIED).** `/etc/netstart` runs `slattach /dev/tty07 9600`. The `open()` blocks forever while the line has no carrier, so boot hangs after "Assuming NETWORKING system ...".
* The fix in SimH, without changing the image: `attach dz 4002,Line=7,Loopback`. A loopback line reports DCD once DTR is up (`sim_tmxr.c:1830-1843`).
* `/etc/ttys` already enables tty00–03.
* There is also a `test` / `test4W11a` account (w11 CHANGELOG).

### 3.4 Unix V7

**v7pidp (recommended V7), VERIFIED.**
* Source: `systems/unix7/disk0.hp` (174,423,040 bytes, RP06) from the **PiDP-11 systems bundle** `http://obsolescence.dev/pidp11/systems.tar.gz`.
  * The bundle is 122,145,185 bytes, sha256 `14bcf836…0bd9`, Last-Modified 2026-04-10, and unpacks to 1.91 GB. It is fetched by `github.com/obsolescence/pidp11/install/install.sh:252`.
  * The bundle's readme: "Freshly installed unix v7, as per http://a.papnet.eu/UNIX/v7/Installation". It also contains `dist.tap` and `AncientUnix.pdf`.
* Boot (`configs/v7pidp/boot.ini`):
  * The RP boot block prints nothing. `send after=2000000,delay=20000,"boot\r"` types the command; a SEND issued before `boot rp0` survives the boot reset.
  * `Boot` / `:` → `expect ": " send "hp(0,0)unix\r"` → `mem = 971008` → single-user `#` → `expect "# " send "\004"` → multi-user.
  * Login root / root.
* Memory: 2 MB works (`mem = 2019584`). With 4 MB the kernel prints `mem = -85760` and hangs (VERIFIED). The w11 notes report the same for 3 and 4 MB.
* Shutdown: there is no halt command. Run `sync` three times, wait about 3 s, then ^E and `quit`.
* Multi-user lines: `/usr/sys/conf/myconf` contains `4dc` (4 DC11 lines) and `/etc/ttys` enables `tty00`–`tty03`. With `set dci enabled` / `set dci lines=8` / `attach dci 4003`, a telnet to port 4003 gets `login:` on `/dev/tty00` (VERIFIED, section 5).

**v7w11, VERIFIED.**
* Source: `https://www.retro11.de/data/oc_w11/oskits/u7ed_rpset.tgz`, 4,238,767 bytes, sha256 `55fb0dad…a6cf`. Contains `u7ed_rp.dsk` (87,960,576 bytes, RP04) and `Caldera-license.pdf`.
* It is the plain Bostic distribution; w11 warns that FP-using programs core-dump on FPU-less w11 hardware. Same boot dialogue as v7pidp, with `set cpu 11/70 2M` and `rp0 rp04`.

**v7simh, FAILS on an 11/70 (VERIFIED).**
* Source: `https://simh.trailing-edge.com/kits/uv7swre.zip`, 3,204,581 bytes, sha256 `849138fb…49eb8b6`. Contains `unix_v7_rl.dsk` (RL02), `AncientUnix.pdf` and `README.txt`.
* The boot block shows `@`; typing `boot` gives `New Boot, known devices are hp ht rk rl rp tm vt` and then `:`. Asking for `rl(0,0)rl2unix` on an 11/70 gives `not a directory` / `rl2unix not found`, even though the root directory is intact. A read-only parse with `scripts/v7fs.py` shows `rl2unix`, `rkunix`, `hptmunix` and others.
* Cause: after the failure, `examine MMR3` = **000065** (UNIBUS-map enable bit set) while `e 17770200-17770217` shows all map registers = 0. The stand-alone boot turns the 11/70 UNIBUS map on without loading it, so RL DMA lands in the wrong place.
* With `set cpu 11/45 256K` the same image boots (`mem = 177856`, `#`). Use v7pidp or v7w11 for an 11/70.

### 3.5 Unix V6 and V5 (simh kits), VERIFIED

**V6.** `https://simh.trailing-edge.com/kits/uv6swre.zip`
* 1,549,757 bytes, sha256 `7bb16343…6b91`.
* Contents: `unix0_v6_rk.dsk` (root, 2,077,696 bytes) and `unix1`/`unix2`/`unix3_v6_rk.dsk` (2,440,704 bytes each), mounted by `/etc/rc` on `/usr`, `/usr/source` and `/mnt`.
* Boot: `@` → `rkunix` (the image also holds `unix` and `rkunix.40`). It comes up straight in multi-user mode: `login:` root, no password, `# `. The date is 1994.
* The kernel source tree (`/usr/sys`) is not on these packs.

**V5.** `https://simh.trailing-edge.com/kits/uv5swre.zip`
* 806,708 bytes, sha256 `875fe94a…12d1`.
* `unix_v5_rk.dsk`: `@` → `unix` → login root.

Both kernels are 18-bit (they never set 22-bit mapping), so they use at most 248 KB of the 1 MB they are given. Shutdown is `sync`×3.

### 3.6 RT-11, VERIFIED (4 images)

**rt11pidp** (PiDP bundle `systems/rt11/rt11V.dsk`, 10,478,080 bytes, RL02)
* Monitor: `RT-11SJ (S) V05.03`, SYSGENed for a **50 Hz** clock (`SHOW CONFIG` prints "50 Cycle System Clock"), so the config uses `set clk 50hz`.
* Contents: 423 files, including BASIC, FOCAL, TECO, Fortran, a small C compiler and many games.
* RL1 holds the `DEC-11-Graphics.dsk` VT11 demos. VT11 output needs an SDL build of SimH; this build has no video.
* The bundle also has `rt11games.rl2`.

**rt11w11** (`https://www.retro11.de/data/oc_w11/oskits/rt11-53_rlset.tgz`, 961,195 bytes, sha256 `c9c897c1…6acc`)
* `RT11_V5.3_SYSTEM.dsk`: `RT-11FB V05.03`, a clean installed working volume. `STARTF.COM` types `V5USER.TXT`.

**rt11v53** (`https://simh.trailing-edge.com/kits/rtv53swre.tar.Z`, 1,373,083 bytes, sha256 `9fdad109…07bc`)
* `Disks/rtv53_rl.dsk` is the **distribution** disk; it boots the RT11AI auto-install monitor.
* The config answers RETURN, then NO ("use the automatic installation procedure?"), then RETURN. RT-11 writes the RT11FB bootstrap and reboots into `RT-11FB V05.03`, the same state as rt11w11.
* This first boot changes the disk image.

**rt11v4** (`https://simh.trailing-edge.com/kits/rt11swre.tar.Z`, 858,994 bytes, sha256 `f4338a7d…821d`)
* `Disks/rtv4_rk.dsk`: `RT-11SJ V04.00C` on RK05.

**Common to all four.**
* No boot questions and no login.
* RT-11 V5.3 and earlier accept dates only in 1972–1999. Each config sends `DATE %DATE_DD%-%DATE_MMM%-%DATE_19XX_YY%` and `TIME %TIME_HH%:%TIME_MM%:%TIME_SS%` at the first interactive `.`.
  * `%DATE_19XX_YY%` is the 19xx year whose calendar matches today: 2026 → `98`, so the system shows `21-Oct-98`.
  * The matching strings are image-specific: `"\r\n."` (rt11pidp), `"TYPED or PRINTED.\r\n\r\n\r\n."` (rt11w11 and rt11v53), `"D 56=0\r\n\r\n."` (rt11v4).
* **Idle:** RT-11 never executes WAIT. PC samples land all over the monitor, with WAIT=0.
  * Host CPU is 100 % unthrottled.
  * Measured with `set throttle` (`scripts/cpumeasure.py`): 1M → **10.7 %**, 3M → 15.6 %, 10M → 34.5 %.
  * **Recommendation:** `set throttle 1M` for RT-11. That is roughly real 11/70 speed and gives a lively panel.
* Shutdown: none exists. Directory writes are synchronous. Halt when no program is running.
* Extra terminals: none. The distributed SJ/FB monitors have no multi-terminal SYSGEN option (UNVERIFIED that no image here has it; `SHOW CONFIG` lists "No SYSGEN options enabled" for SJ).

### 3.7 RSX-11M-PLUS

**rsxbq, recommended RSX, VERIFIED.**

*Download.* `ftp://ftp.dfupdate.se/pub/pdp11/rsx/pidp/pidp.dsk.gz` (anonymous FTP)
* 28,688,613 bytes, sha256 `09384cfa…ead0`. The listing dates it Sep 30.
* Unpacks to `pidp.dsk`, 1,024,458,752 bytes. SimH identifies it: `Contains an ODS1 File system … Volume Name: RSX11MPBL87`.
* Optional `pidp.tap.gz` (28,254,126 bytes, sha256 `92a3d0e4…a480`) unpacks to an 89 MB tape image. SimH scans it as SIMH/E11 format with 37,808 trailing bytes it cannot account for. I did not use it.
* The PiDP-11 `install.sh` (lines 399-447; `ftp_url` at line 428) fetches exactly these files and ships `install/boot.ini.bilquist`.

*System.*
* Banner: `RSX-11M-PLUS V4.6 BL87 1920.KW System:"PIDP"`.
* `CON DIS CONT ATTR` shows:
  * YZA (DZ11) at 160100 / 300, the SimH default.
  * YVA (DHV11) at 160500 / 310.
  * WEA (DEUNA) at 174510 / 120.
  * DUA (MSCP) at 172150 / 154, and MUA (TMSCP) at 174500.
  * Clock KW11-L with $TKPS=50, so the config uses `set clk 50hz`.

*Boot dialogue* (every string verified):
* **First boot only.**
  * 2× "Press enter to continue [S]:"
  * DECnet enabled? `N`; node address; node name `PIDP`; LAT `N`; configure TCP/IP now? `N`; 50 Hz clock? `Y`
  * 4 time-zone questions, all left at the UTC defaults
  * summer time `N`, EU rules `N`, USA rules `N`
* **Every boot:**
  * `Please enter time and date (HH:MM DD-MMM-YYYY) [S T:1M]:`. The config sends `%TIME_HH%:%TIME_MM% %DATE_DD%-%DATE_MMM%-%DATE_YYYY%`; the system is Y2K-capable and showed 21-OCT-26.
  * `Start CEX? [Y/N D:Y T:1M]` → Y
  * `Start TCP/IP? [Y/N D:Y T:1M]` → Y
  * `Do you have a crash explanation to enter? [Y/N D:N T:1M]` → N. This rule uses `expect -c`, which clears every leftover rule.
* Each prompt also times out after 1 minute, so an unattended boot would finish without help, about 4 minutes later.
* All 15 first-boot answers (2 × enter + 13 questions) and the 4 per-boot answers sit in `configs/rsxbq/boot.ini`. Every answer is sent with `send -t after=250000,"…"` (section 8, item 10). With that delay, a first boot from the pristine image reaches `logged off PIDP` in 23.6 s and later boots in 19.1 s. Without it, 18.4 s, but the dialogue sometimes hangs.

*Login.*
* `HELLO SYSTEM` / `SYSTEM` gives [1,1] with an MCR `>` prompt. `HELLO USER` / `USER` gives [200,1] with DCL `$`.
* `SYSLOGIN.CMD` sends `ESC Z` and `ESC [ c` (SET /INQUIRE). The harness answers `ESC [ ? 1 ; 2 c`; otherwise it waits about 5 s and prints "Inquire cannot determine terminal type". xterm.js answers DA1 by itself.

*Networking (VERIFIED, unprivileged).*
* With `set xu enabled` / `set xu type=delua` / `attach xu nat:tcp=2323:10.0.2.15:23`:
  * RSX DHCP gets `10.0.2.15`.
  * NTP corrected the clock to the true time through NAT.
  * `PING 10.0.2.2` replies.
  * A host telnet to `127.0.0.1:2323` gets `Welcome to PIDP, an RSX-11M-PLUS system!` and then `>`.
* Without NAT, boot still works, but IPLOG and DHCP complain on the console.

*Shutdown.* `RUN $SHUTUP`:
1. "Enter minutes to wait before shutdown:" `0`
2. "Reason for shutdown (<CR> for none):" CR
3. "OK to shutdown? [Y/N]:" `Y`
4. The disk is dismounted and the system prints "SHUTUP operation complete". SimH then shows `HALT instruction, PC: 126130`.

**rsx30, licence-clean alternative, VERIFIED.**
* Download: `https://www.retro11.de/data/oc_w11/oskits/rsx11mp-30_rpset.tgz`, 13,170,097 bytes, sha256 `4fa5d3a9…b5fd`.
* Contents: `rsx11mp-30.dsk` (174,416,896 bytes, RP06, volume `RSX11MPBL24`), `Mentec-license.txt`, `README_license.txt`.
* Boot: `RSX-11M-PLUS V3.0 BL24 1920.KW System:"RSXMPL"`.
  * The secondary boot takes an 11/70 red-stack trap; `dep STOP_TRAPS 0` is needed (w11 notes and `pdp11_cpu.c:604`).
  * One prompt: `Please enter time and date (HH:MM DD-MMM-YY) [S]:`. It is not Y2K-ready, so the config uses `%DATE_19XX_YY%` (21-OCT-98).
* TT0: stays logged in as [200,200]. `HEL` means HELP here, not HELLO.
* Terminals (`DEV`): TT0: console, TT1: a second DL11 at 176500/300, and TT2:–TT11: a DZ11 at 160100 with vector **310**.
  * The config therefore has `set dli enabled`, `set dli lines=1`, `set dlo0 7b` and `attach dli 4007`. SimH autoconfig then moves the DZ to 310.
  * Without the DLI, RSX sets the DZ's master scan enable and SimH accepts the telnet. Typed characters stay in RBUF with RDONE=1 and are never read, because the DZ interrupts at 300, where RSX has the missing DL11.
  * `set dlo 7b` is rejected ("Invalid argument"). The unit form `set dlo0 7b` works; without it the line runs in SimH's default UC mode.
* `RUN $SHUTUP` works as for V4.6 and ends in a HALT.

**rsxpidp (PiDP bundle `systems/rsx11mp/PiDP11_DU0.dsk`, 314,880,000 bytes): boots, not finished.**
* `RSX-11M-PLUS V4.6 BL87 System:"PIDP11"`, a personal 2018 system.
* The DZ is SYSGENed at 160030 / 330, so it needs `set dz address=17760030` and `set dz vec=330`, which turn SimH autoconfig off. There are also extra DL11-style YL lines at 160010 and 160020.
* Prompts: `(HH:MM DD-MMM-YYYY) [S D:"09:30 30-DEC-2018" T:1M]`, `Load LAT? [T:15S]`, `Load TCP/IP? [T:15S]`.
* About 76 s go to DECnet, LAT and UNA errors.
* It printed `PIP -- Failed to read attributes … File ID, sequence number check`.
* Its boot.ini attaches `PiDP11_DU1.dsk`, a 1.7 GB DECUS disk that is not in the bundle.
* Not recommended. Login was not attempted.

### 3.8 RSTS/E V7.0, VERIFIED

**Download.** `https://simh.trailing-edge.com/kits/rstsv7swre.tar.Z`
* 4,940,043 bytes, sha256 `23e6ff60…436069`.
* Contents: `rsts_full_rl.dsk`, `rsts_swap_rl.dsk`, `rsts_min_rl.dsk` (5,235,200 bytes each, RL01) and `rsts_readme.txt` (Chuck Cranor, 1998).

**Boot.** `RSTS V7.0-07 llama (DL0)`:
* `Option:` → **LF** (^J, not CR).
* With any memory size the saved memory table does not match, so RSTS asks `Table suboption?`. **LF** (= EXIT) accepts it. CR only prints help.
* `DD-MMM-YY?` → 19xx-style date. `12:00 PM?` → `HH:MM`; 24-hour input was accepted.
* `Command File Name?` → CR. The default start-up runs and ends with `Detaching . . .`.
* The console then sits logged out, with **no prompt**.

**Login.** `HELLO 1,2` / `SYSTEM` → "Job number to attach to?" → CR (start a new job) → `Ready`. `SYSTAT` shows 5 detached jobs (ERRCPY, OPSRUN, QUMRUN, SPLIDL, BATIDL).

**Shutdown.** `RUN $SHUTUP`:
1. "Use 'OPSER' for utilities shutdown <YES>" CR
2. "Allow utilities to reach logical end point <YES>?" CR
3. "Minutes until system shutdown (0-99) <5>?" `0`
4. Shutdown phases run, then "Please wait for system to re-boot itself". RSTS reboots to `Option:`, which is safe to halt.

**Notes.**
* The answers use `send -t after=250000` as on RSX. This is a precaution: RSTS never failed with the default timing.
* INIT's `HARDWR LIST` (`scripts/rstshw.py`, no expect rules, `configs/rsts7/explore-hw.ini`) prints the monitor's device table:
  * TT: 177560/060; RK; RL ×4; RM (RK06) ×8; TM; PR0; PP0; LP0; RX0; DZ0 160100/300; DZ1 160110/310; KW11L 177546/100
  * `SR 177570`, `DR 177570`, "Hertz = 60."
  * "Other: FPU, SL, 22-Bit Addressing, Cache w/address, System ID = 4660"
* The DZs are listed, but the monitor never turns them on (section 5). That experiment is `configs/rsts7dz/boot.ini`.
* INIT also has a `HARDWR SWITCH` suboption, "Set console switch register characteristics". DR still stayed 0 in every sample (section 4).

---

## 4. Console display register (17777570) and idle patterns

In SimH, DR is register `DR` of the SYSTEM device. `examine DR` finds it; writes go through `DR_wr()` (`PDP11/pdp11_cpumod.c:76, 285, 367`). WAIT sets `wait_state` (`pdp11_cpu.c:1031`), and the register `WAIT` (`pdp11_cpu.c:603`) shows it.

| OS | Writes 177570? | Source evidence | Empirical (VERIFIED, 8 samples at ^E) | What a panel should show |
|---|---|---|---|---|
| Unix V7 | **Yes**, every clock tick: the word at the address in the switch register (SR even = kernel D-space address, SR odd = user address SR−1) | `/usr/sys/sys/clock.c:18,44-47` "copy *switches to display", `display();`; `/usr/sys/conf/mch.s:430-450` `_display: … mov CSW,r1 … jsr pc,fuword; mov r0,CSW`; `mch.s:907 CSW = 177570`. Idle is a plain `wait` (`_idle:` spl 0; wait) | DR=000656 constant with SR=0. With `d sr 4460` (`_lbolt`, from `nm /unix`): DR=031, 012, 010, 012 (tick counter). `d sr 4464` (low word of `_time`): DR=5512, 5513, 5514, one per second. `d sr 1`: DR=170011 (`setd`, word 0 of the user program) | DR = "memory monitor": toggle an address into SR and watch the word. R0 during WAIT is a constant, so no pattern |
| Unix V6 | **Yes**, but only the 11/45-and-11/70 kernel | `usr/sys/conf/m45.s:305-325` (`_display`, the same code as V7), `m45.s:792 CSW = 177570`, `ken/clock.c:30-38`. On the 11/40 kernel `m40.s:64-67` `_display: rts pc` is a no-op (unix-history-repo, `Research-V6` branch) | DR=000417 (kernel word 0) constant, so the 11/45 kernel is in use | same as V7 |
| Unix V5 | Yes (empirically) | not read | DR=000417 constant | same as V7 |
| 2.11BSD (pl447, 482, 499) | **No** | `/usr/src/sys/pdp/iopage.h:30` defines `CSW ((u_short *) 0177570)`, but a grep over `sys/{sys,pdp,pdpuba,pdpmba,conf,pidp}` finds no other use. `/usr/src/sys/pdp/mch_xxx.s:54-75`: "If you have a console display it's amusing to see a slowly rotating sequence of lights…", `INT(LOCAL, rdisply, 0377)`, every 2nd `idle()` call `clc; rol rdisply; bpl 1f; bis $1,rdisply`, then `mov rdisply,r0 / wait displays contents of r0`, `SPLLOW`, `wait` | DR=000000 always; WAIT=1, PC=005360 (pl499). R0 = 001774, 177003, 007760, 077600, 000776, 017740, 140177… A breakpoint at the WAIT (`break 5356;ex r0;c`) shows R0 **changes every 2nd WAIT** (about 30 steps/s at 60 Hz). Simulating the source gives the same sequence: a band of 8 lit bits rotating left with wrap-around, 15 states | DATA PATHS during WAIT shows R0: the rotating 8-bit band |
| RSX-11M-PLUS V3.0 and V4.6 | **No** (DR=0 in both) | no source here. The QProbe article (retrocmp.com, "353 … more idle patterns in rsx11m and 211bsd") says the stock idle loop is in `[11,10]SYSXT.MAC`; QProbe users patch in `MOV R0,@#177570` to get it into DR | Code at the WAIT (V3.0): `25520 MOV (R1),R0; 25522 MOV R0,-(R1); 25524 WAIT; 25526 BR 25474`. Breakpoint trace: R0 changes every **3rd** WAIT through the 8-state RSX pattern: `*....******....*`, `**....****....**`, `***....**....***`, `****........****`, `.****......****.`, `..****....****..`, `...****..****...`, `....********....` (octal 103741, 141703, 160607, 170017, 074036, 036074, 017170, 007760). The same values appear on V4.6 | DATA PATHS during WAIT shows R0: the classic "two groups of bits meet in the middle" |
| RSTS/E V7.0 | **No** | no source | Null job: `42534 WAIT; 42536 SOB R1,42534; 42540 ROL R0`. R0 steps every 2nd WAIT; a ROL through carry gives a band of 5–6 lit bits rotating left (000037, 000077, 000176, 000374 … 174000, 170001 …) | DATA PATHS during WAIT shows R0 |
| RT-11 V4 and V5.3 | **No** | – | DR=0, WAIT=0, PC spread over the monitor (busy loop) | nothing specific; the lamps follow the busy loop |

Notes:
* The idle samples come from ^E / `examine` / `c` cycles (`oscheck.dr_sample`, `scripts/r0trace.py`) and from SimH breakpoints with actions (`break <wait-pc>;ex r0;c`).
* Stopping and restarting the CPU does not upset these OSes.
* The real-11/70 behaviour, that the DATA lamps show R0 in DATA PATHS during WAIT, is UNVERIFIED by me on hardware. It rests on the 2.11BSD source comment, the QProbe article, and the fact that all three OSes load R0 right before WAIT.
* Practical rule for the web panel: **if CPU `WAIT`=1, DATA PATHS = R0; DISPLAY REGISTER = DR.**
* PiDP-11 `boot.ini.bilquist` warns that throttling disturbs the idle pattern; I did not check that.

---

## 5. Extra login lines, verified with real telnet sessions from the host

| OS | Multiplexer the kernel/SYSGEN expects | SimH config | Verified |
|---|---|---|---|
| 2.11BSD pl499 | DZ11 160100/300 (`/etc/dtab`), tty00–07 | `set dz enabled`, `set dz lines=8`, `set dz 7b`, `attach -m dz 4000`. `-m` = modem control: carrier follows the telnet connection, so a hang-up logs the user out. | `scripts/dztest.py`: `sed -e '/^tty0[0-7]/s/off/on/'` on `/etc/ttys` + `kill -HUP 1`. Telnet to 4000 → "Connected to the PDP-11 simulator DZ device, line 0" → `2.11 BSD UNIX (pidp.bqtnet.se) (tty00) login:` → root → `tty` = `/dev/tty00` |
| 2.11BSD pl482 | DZ 160100/**310**, plus a 2nd DL11 at 176500/300 (`cn 1`) | add `set dli enabled`, `set dli lines=1` (DLI can also go to a TCP port: `attach dli <port>`) | boots with "dz 0 … vector 310 attached", "cn 1 … attached"; no telnet test |
| 2.11BSD w11 | DZ 160100/310 + `cn 1` | as pl482, plus `Line=7,Loopback` (section 3.3) | tty00–03 enabled in the image; no telnet test |
| Unix V7 (PiDP) | DC11 ×4 (`myconf: 4dc`), tty00–03 enabled | `set dci enabled`, `set dci lines=8`, `set dco 7b`, `attach dci 4003` | `scripts/lineprobe.py`: telnet to 4003 → "Connected to the PDP-11 simulator DCI device, line 0" → `login:` root/root → `/dev/tty00` |
| Unix V6 / V5 | V6 `/etc/ttys` has only console tty8 enabled (`18-`); DC11/KL11 need ttys edits | `set dci …` as for V7 (PiDP's V6 boot.ini does the same) | UNVERIFIED |
| RSX-11M-PLUS V4.6 (bq) | YZA DZ 160100/300 → TT11:–TT20:; YVA DHV 160500/310 → TT1:–TT10: (offline unless SimH `vh` is enabled at that address); TN: telnet terminals via TCP/IP | `attach dz 4004` | `lineprobe.py`: telnet to 4004 → `>` → `HELLO USER`/`USER` → "Logged on Terminal TT11: as USR1" |
| RSX-11M-PLUS V3.0 | 2nd DL11 176500/300 → TT1:; DZ11 160100/**310** → TT2:–TT11: | `set dli enabled`, `set dli lines=1`, `set dlo0 7b`, `attach dli 4007`; `set dz lines=8`, `attach dz 4005` | `scripts/rsxtt.py --probe`: telnet to 4005 (TT2:) or 4007 (TT1:) → `>`; `TIM` → "MCR -- Not logged in"; `HELLO` → "Account or name:". Login not done: no account for these lines is documented (the console comes up logged in). Without the DLI the DZ lines never answer |
| RSTS/E V7 | INIT `HARDWR LIST`: TT: 177560/060, **DZ0 160100/300, DZ1 160110/310**. That matches SimH's 11/70 default DZ (enabled, 16 lines, 160100–160117, vectors 300–314). `HARDWR ENABLE DZ0` → "Controller not disabled" | `attach dz 4006` | **Does not work as shipped.** After boot the DZ CSRs are 0: the monitor never sets master scan enable. Open SIMH accepts DZ telnet connections only when some DZ has MSE set (`pdp11_dz.c:568-570`), so the telnet gets no banner. `TTYSET` accepts `KB1:` / `SPEED 9600`, but MSE stays 0. Cause not found (maybe no DZ lines in the monitor's SYSGEN) |
| RT-11 | none | – | – |

On the 11/70, SimH enables a DZ with 16 lines by default (`show dz` → 160100–160117, vectors 300–314), along with RK, HK, RX, TM, LPT, PTR and PTP. `set dz lines=8` makes it one DZ11.

Open SIMH's DZ accepts a telnet only after the OS sets master scan enable (`pdp11_dz.c:568-570`). With `attach -m` (modem control) it also waits for the OS to answer the ring with DTR. RSX V3.0 never raises DTR, so that gave "No answer on any connection".

Every SimH multiplexer can also listen per line (`attach dz Line=n,port`). They can buffer and log, and with `-U` they reuse a port. Use distinct ports per service in the container.

---

## 6. Networking in a container (DELUA `xu`)

* **Build.** This SimH has the `NAT` (slirp, user-mode) and `TAP` transports, and no pcap (`libpcap-dev` was not installed; the warning appears in the build output). **NAT needs no privileges and no `/dev/net/tun`**, which suits Docker and LXC. TAP needs CAP_NET_ADMIN and a tun device.
* **2.11BSD pl499, VERIFIED** (`scripts/nettest.py`).
  * The kernel has the DE driver: `attaching de0 csr 174510`.
  * In a working copy, `/etc/rc.conf` was changed to `address=10.0.2.15`, `gateway=10.0.2.2`, `/etc/hosts` to `10.0.2.15 pidp pidp.bqtnet.se`, and `/etc/resolv.conf` to `nameserver 10.0.2.3`. With `set xu enabled`, `set xu type=delua`, `attach xu nat:tcp=2324:10.0.2.15:23`:
    * `ifconfig de0` shows `inet 10.0.2.15`.
    * `netstat -rn` shows a default route via 10.0.2.2.
    * `ping -c 3 10.0.2.2` loses 0 %.
    * `ntp -s -f pool.ntp.org` at boot set the real date: `offset:12898536 … Wed Oct 21 22:34:02 2026`.
    * A host telnet to `127.0.0.1:2324` gets `2.11 BSD UNIX (pidp.bqtnet.se) (ptyp0) login:`.
  * With the shipped 192.168.1.x settings, boot hung at `starting local daemons:` (sendmail waiting on DNS). The network settings must be adapted, or `xu` left disabled.
* **RSX V4.6, VERIFIED.** DHCP, NTP and telnet all work over NAT, with no image changes (section 3.7).
* **SimH NAT start-up bug, found and VERIFIED from source and one occurrence.** `slirp_glue/sim_slirp.c:339-348` creates a "doorbell" UDP socket on `localhost:<port>`, where port = (milliseconds since start) & 0xFFFF. The loop `do { … sim_connect_sock_ex(db_host, …) } while (db_chime == INVALID_SOCKET);` retries **without changing the port**.
  * Inside a container running as non-root, with `net.ipv4.ip_unprivileged_port_start=1024` (this host's value), a port below 1024 fails with EACCES forever. That is about 1.6 % of starts.
  * Seen once: the console filled with 1.4 million lines of `Sockets: bind error 13 - Permission denied` right after the `attach xu nat:` line, and the simulator never booted.
  * Fixes, any one of:
    * patch the loop to `++rnd_val` on failure (recommended; one line);
    * run the container with `--sysctl net.ipv4.ip_unprivileged_port_start=0`;
    * grant CAP_NET_BIND_SERVICE;
    * have a watchdog restart SimH when it sees that message.
* NAT forwarding: `nat:tcp=<hostport>:10.0.2.15:<guestport>[,tcp=…]`. Guest view: gateway 10.0.2.2, DNS 10.0.2.3, DHCP from 10.0.2.15.

---

## 7. CPU, idle and throttle

* PDP-11 `set cpu idle` takes only an optional stability value: `set cpu idle=N`, N from 2 to 600 s, default 20 (`sim_timer.c:1662-1665`, `sim_timer.h:84-86`). There are no OS-specific idle variants like the VAX's `idle=VMS`. The CPU idles whenever it executes `WAIT` (`pdp11_cpu.c:955-957`).
* SimH's own help (`pdp11_cpu.c:3743`) says idling "will work for RSTS/E and RSX-11M+, but not for RT-11 or UNIX". That is **out of date for UNIX**: V5, V6, V7 and 2.11BSD all idle at 0.7–4 % host CPU. It is right for RT-11 (100 %).
* `set throttle 1M` costs about 11 % of a core for RT-11 and 3.9 % for 2.11BSD. It probably gives a more realistic panel; boot is barely slower (not measured precisely).

---

## 8. Gotchas found while automating (all VERIFIED)

1. **Matching strings.** SimH EXPECT strings must match the raw byte stream.
   * The old 2.11BSD and V7 boot programs emit `\n\r\x7f\x7f: ` with DEL fill characters, and RT-11 V4 pads lines with NULs.
   * Use short tail strings (`": "`, `"# "`) and the one-shot order of rules.
   * Use `-p` for prompts that repeat, `-c` on the last rule to drop rules that never fired, and `[n]` counts where needed.
2. **SEND before boot.** `send after=…,delay=…,"…"` issued before `boot` survives the boot reset. It is the only way to type into the silent V7 boot block.
3. **Explicit addresses.** `set <dev> vector=` or `address=` disables SimH autoconfig for every later device. Prefer enabling `dli` so the DZ shifts to 310 by itself.
4. **Two-digit years.** For OSes that are not Y2K-ready (RT-11 ≤ V5.3, RSX-11M-PLUS V3.0, RSTS/E V7), use `%DATE_19XX_YY%`.
5. **No time-of-year clock.** The 11/70 has none. Every Unix in this set started with a stale or 1969 date unless NTP was available.
6. **Tape attach syntax.** `attach -r -f tq0 e11 file` (Billquist's boot.ini) works. `attach -f e11 tq0 …` fails with "Non-existent device: E11".
7. **PiDP-11 boot.ini files.** They contain `set realcons …` lines, which are BlinkenBone-only, and attach `PiDP11_DU1.dsk` or `eth0`. Remove those for plain Open SIMH.
8. **Footers and copies.** Disk sizes did not change during these runs. Open SIMH writes a metadata footer only when it creates or auto-sizes a container without one (`sim_disk.c:3307-3308`). Still, keep pristine downloads and boot copies: every boot dirties the image.
9. **Sparse copies: they help for 2.11BSD, not for RSX** (corrected after a re-measurement with `scripts/nonzero.py`, which counts 4 KiB blocks that are not all zero).
   | Image | Size | Non-zero data |
   |---|---|---|
   | 2.11BSD pl499 | 977 MiB | 188 MiB (19 %) |
   | 2.11BSD pl482 | 954 MiB | 170 MiB (18 %) |
   | RSX V4.6 bq | 977 MiB | 947 MiB (97 %) |
   * `cp --sparse=always pidp-pl499.dsk x.dsk` gives an identical file (`cmp`) that takes **189 MB** on disk.
   * `xz -d` already writes sparse output: the extracted pl482 takes 242 MB. `gzip -d` does not: the extracted pl499 takes 977 MB.
   * Ship the images compressed (gz or xz, 24–48 MB), expand them on first start into the data volume, and make them sparse there. Docker image layers are known to expand sparse files (UNVERIFIED here).
10. **Answer timing (race).** An `EXPECT` match fires while the prompt's last character is being output. Its `SEND` starts `AFTER` = `DELAY` = 1000 instructions later by default (`sim_defs.h:911` `SEND_DEFAULT_DELAY 1000`, `scp.c:5398-5399`). That is well under a millisecond.
    * The OS has not always posted its terminal read by then. With disk I/O asynchronous (`SIM_ASYNCH_IO`) and the CPU idling in `WAIT`, the gap depends on host scheduling. On RSX the early answer is treated as unsolicited input and goes to MCR: `… [Y/N D:N]: N` / `MCR -- Illegal function`.
      * A first-boot question has no timeout, so the dialogue hangs for good.
      * A per-boot question (`T:1M`) takes its default after a minute.
    * `scripts/rsxrace.py OPTS N ROUNDS` boots N fresh copies of the pristine RSX V4.6 image in parallel, with every SEND rewritten to OPTS. Results, logs in `$R3/results/rsxrace/`:
      * SimH default, 4 simulators in parallel and no other load: 8 of 8 passed.
      * SimH default with 16 busy loops on the 16 host CPUs: 14 of 16 passed. One run hung at `Summer time zone name`; another lost the answer to `Start TCP/IP?`.
      * `-t after=250000` under the same load: **24 of 24 passed**. A first boot takes 23–26 s instead of 19–21 s.
    * `-t` makes SimH read both `AFTER` and `DELAY` as microseconds, converted with the calibrated instruction rate (`scp.c:13627-13628`). The default `DELAY` of 1000 therefore becomes 1 ms between characters.
    * Rule: every `SEND` that answers an OS prompt gets `-t after=250000`. Boot-loader prompts that are polled (`:`, `@`, the `Press <CR> to boot` countdown) and RT-11's `.` did not fail in any run (each booted 2 or more times in the final batch), so their configs were left as they are.
11. **Second DL11 options are per unit.** `set dlo 7b` fails with "Invalid argument"; `set dlo0 7b` works. Without it, the extra DL11 line runs in SimH's default UC mode, so all output is upper case.
12. **Interrupt vectors must match the SYSGEN.** RSX V3.0 and 2.11BSD pl447/pl482 were built with a second DL11 at 176500/300, which puts the DZ at vector 310.
    * On SimH, enable `dli` (1 line) so that autoconfig moves the DZ to 310.
    * With the wrong vector, the DZ runs and SimH accepts telnets, but input is never read.
    * Check what the OS expects: 2.11BSD boot messages, RSX `DEV`, RSTS INIT `HARDWR LIST`.

---

## 9. Licences

* **Unix V1–V7 (and 32V): Caldera "Ancient UNIX" letter of 23 Jan 2002** (`AncientUnix.pdf` in the simh kits, the PiDP bundle and w11). It grants a fee-free right to "use, modify and distribute" source and binaries, under 4-clause BSD-style conditions, including the advertising clause "This product includes software developed or owned by Caldera International, Inc.". It **explicitly excludes System III and System V**, so the PiDP bundle's `sysiii/` and `sysv/` images are **not** covered.
* **2.11BSD:** 4-clause BSD licence (Regents of the University of California, 1992), with the Caldera licence covering code derived from V6 and V7 (w11 `README_license.txt`, `BSD-license.txt`). It can be redistributed. Keep the notices in the appliance's documentation.
* **DEC operating systems: Mentec licence text** (`Licenses/pdp11_license.txt` in the simh kits, `Mentec-license.txt` in w11). It covers "RT-11 V5.3 or prior, RSTS/E V9.6 or prior, RSX-11M V4.3 or prior, RSX-11M PLUS V3.0 or prior".
  * The grant: "use and copy the SOFTWARE TECHNOLOGY solely for personal, non-commercial uses in conjunction with the EMULATOR". EMULATOR is defined as "software owned by Digital Equipment Corporation that emulates…".
  * w11's reading: "commonly understood to cover the usage … in the PDP-11 software simulator available from the simh project". It is **not** valid on real hardware or FPGAs.
  * There is no explicit right to redistribute to third parties.
* **RSX-11M-PLUS V4.6 (Billquist image and PiDP bundle):** no licence was found in the images or on the sites. It is newer than anything the Mentec licence covers.
  * Per Wikipedia's "Mentec" article (UNVERIFIED by me beyond a web search), the rights have belonged to **XX2247 LLC** since Mentec's 2006 bankruptcy, and there is no public hobbyist programme.
  * The hobbyist world distributes it openly (the PiDP-11 installer downloads it), but treat it as legal grey.
* **Appliance policy suggestion:**
  * Bundle 2.11BSD, V7, V6 and V5.
  * Fetch the DEC OSes on first use, after the user accepts the Mentec text.
  * Label RSX V4.6 "no licence available – personal use at your own risk".

---

## 10. Recommendation

**Default: 2.11BSD pl499** (`bsd499`).
* It is the newest code (patches through January 2026) and boots with zero prompts in about 9 s.
* It has multi-user DZ lines, TCP/IP over NAT, cc, vi and tcsh.
* It idles at 2–4 % of a core.
* Its rotating R0 band is a good, if modest, panel show.
* It can be redistributed.

Ordered list for the appliance's OS menu:

| # | System (config) | Why | Download (compressed) | On disk |
|---|---|---|---|---|
| 1 | **2.11BSD pl499** (`bsd499`) | default, see above | 47.9 MB (`pidp-pl499.dsk.gz`; tape +24.9 MB optional) | 1,024 MB |
| 2 | **RSX-11M-PLUS V4.6 BL87** (`rsxbq`) | the iconic 11/70 idle pattern, DECnet and TCP/IP, very polished; download on request (licence) | 28.7 MB (`pidp.dsk.gz`) | 1,024 MB |
| 2b | RSX-11M-PLUS V3.0 (`rsx30`) | licence-clean RSX with the same idle pattern | 13.2 MB | 174 MB |
| 3 | **Unix V7** (`v7pidp`; lightweight alternative `v7w11`) | the classic Unix; boots to login in 2 s; DR "memory monitor" via the switch register | 122.1 MB for the whole PiDP bundle (also gives RT-11 with games), **or** 4.2 MB (`u7ed_rpset.tgz`) | 174 MB / 88 MB |
| 4 | **RT-11 V5.3** (`rt11pidp` with games and languages, or `rt11w11` clean FB) | single-user DEC classic; use `set throttle 1M` | in the PiDP bundle, or 0.96 MB (w11), or 1.37 MB (simh kit) | 10.5 MB (+2.5 MB graphics disk) |
| 5 | **Unix V6** (`v6simh`) | Lions-book Unix; DR display() | 1.55 MB | 9.4 MB |
| 6 | **RSTS/E V7.0** (`rsts7`) | timesharing BASIC-PLUS; R0 idle band | 4.94 MB | 10.5 MB (2 × RL01) |
| 7 (bonus) | Unix V5 (`v5simh`), RT-11 V4 (`rt11v4`) | tiny, verified | 0.81 MB + 0.86 MB | 2.5 + 1.7 MB |

Totals:
* **Minimal set without the PiDP bundle** (1, 2, 2b, V7 from w11, RT-11 from w11, V6, RSTS, V5, RT-11 V4): 47.9 + 28.7 + 13.2 + 4.2 + 0.96 + 1.55 + 4.94 + 0.81 + 0.86 = **≈ 103 MB** to download, **≈ 2.35 GB** of disk images (2,345,818,112 bytes).
* **With the PiDP bundle instead of the w11 V7 and RT-11 images:** ≈ 103 − 4.2 − 0.96 + 122.1 = **≈ 220 MB** to download. The full bundle expands to 1.91 GB, of which only `unix7/disk0.hp` (174 MB) and `rt11/*` (23 MB) are needed.
* **Redistributable-only set** (2.11BSD, V7 w11, V6, V5): ≈ 54.5 MB to download.

---

## 11. What is VERIFIED vs UNVERIFIED

**VERIFIED** (run here; transcripts in `$R3/results/`):
* all download URLs, sizes and sha256 values above;
* every boot, login, command and shutdown in section 2 except rsxpidp's login;
* the DR and R0 samples and the breakpoint traces;
* DZ logins on 2.11BSD and RSX V4.6, and the DC11 login on V7;
* the RSX V3.0 prompts on TT1: (DLI, port 4007) and TT2: (DZ, port 4005) once `dli` is enabled;
* the RSTS/E V7 DZ table (INIT `HARDWR LIST`), and that the monitor never sets master scan enable;
* NAT networking on 2.11BSD and RSX, including telnet from the host;
* throttle CPU figures;
* the non-zero block counts of the 1 GB images and the 189 MB sparse copy of pl499;
* the V7 simh-kit failure and its MMR3/UBM cause;
* the NAT doorbell bug, from source plus one observed occurrence;
* the EXPECT/SEND answer race on RSX and its `-t after=250000` fix (16 boots with the default timing and 24 with the fix, on a loaded host);
* source citations given with file:line.

**UNVERIFIED:**
* real-hardware DATA PATHS = R0 during WAIT (inferred from sources and articles);
* the XX2247 LLC ownership (Wikipedia, via web search);
* multi-terminal support on RT-11, V5 and V6;
* why RSTS/E V7 never enables its DZ, and which account works on RSX V3.0's extra lines;
* the effect of throttling on the RSX idle pattern;
* the PiDP bundle's older `211bsd/` image (superseded; not booted), and its `sysiii/`, `sysv/`, `unix1/`, `unix4/`, `dos11/` images (not booted).

---

## Appendix A: full sizes and sha256 (VERIFIED)

Paths are relative to `$R3/dl/`. Downloads:

```
211bsd-pl499/pidp-pl499.dsk.gz                  47883044  634d0626d898bdaa14070acd8671c09f5d587c8ed96b9e970f9f0806c40a8e7b
211bsd-pl499/pidp-pl499.tap.gz                  24892185  fb4f330df6324e8832255b29a5f06224f62d2380477f40fede23133689abd6ab
211bsd-pidp11/2.11BSD_rq.dsk.xz                 24201968  b3e6b8321e6e1ec28e1aebdef4282d6596bc1691ad9600fcac8072a8a119f1d0
w11-211bsd_rpset/211bsd_rpset.tgz               35814978  91737bebeb6c3c909b3b09b94741bf5b6d3042976902100d3a7d60511eb2f6f7
w11-211bsd_rpethset/211bsd_rpethset.tgz         35784008  74678c649338b10bfc470b4fec4bd75b649b4df1e3eb5a9f227ed7ac7d947b42
pidp11-systems/systems.tar.gz                  122145185  14bcf83610737c2383ffbfa411ebbd067590705fd1c42caa679d1a5a63f60bd9
w11-u7ed_rpset/u7ed_rpset.tgz                    4238767  55fb0dad3d28eb6ccdf3af7039c6b3d77904d5114ed368e443e8b3682217a6cf
uv7swre/uv7swre.zip                              3204581  849138fba585723dff7c346d4f5ab3eb3b19dfafcd073ce877cbc099349eb8b6
uv6swre/uv6swre.zip                              1549757  7bb16343d280a888bbc387719e8e7a5c5c4aef0af368b0e9ef903aa692a26b91
uv5swre/uv5swre.zip                               806708  875fe94acf3b0a54bc927c8c507cd83c1060f671ce01d38f1d0235d64f8812d1
w11-u5ed_rkset/u5ed_rkset.tgz                     800768  bad3f00f39ca2769590baacb044d3090d7e60661644ccc88c93a663b7843381e
w11-rt11-53_rlset/rt11-53_rlset.tgz               961195  c9c897c10365b8b2aca051dd82df91ce85c8a156bdab2c12ac4dbccce4916acc
rtv53swre/rtv53swre.tar.Z                        1373083  9fdad10969f1f391b13d9d97aa8fc1aa8fcb44472dac363d23eb2d31500207bc
rt11swre/rt11swre.tar.Z                           858994  f4338a7dffb0ce74e06e91219d1e95cd300ff9ea7236785f5b8362f08b06821d
rsx-billquist/pidp.dsk.gz                       28688613  09384cfa04c73285364d4bf1e70355647df731fd3677f9c158a35b1bf758ead0
rsx-billquist/pidp.tap.gz                       28254126  92a3d0e4b8a8b849ce4e18b5dda20029a0cc8a076c4b5a31e3aca7a00a02a480
w11-rsx11mp-30_rpset/rsx11mp-30_rpset.tgz       13170097  4fa5d3a9ce6a10d1fc9bf23a0289fe03e0039a58f226fc7c60ba618afe7ab5fd
w11-rsx11m-40_rkset/rsx11m-40_rkset.tgz          1151736  295e3d694b81f56ae7fa33d943709c6a24dc4a8336817b0758e12ab9aad0d604
rstsv7swre/rstsv7swre.tar.Z                      4940043  23e6ff60597ebd5946cf2eb748a4294e736e33c49033a186997697cfab436069
```

Extracted disk and tape images (what SimH attaches):

```
211bsd-pl499/x/pidp-pl499.dsk                             1024458752  8e367779b51c0d246a283fb9772450d4ed8747892e764fcc2188c34b7e4eb8fe
211bsd-pl499/x/pidp-pl499.tap                               82107056  88a6b0ddacf9aec78f08cdfdd08144287b2788d2151e9de182f059b7d4b49d52
211bsd-pidp11/x/2.11BSD_rq.dsk                            1000448512  ddd69727f4c0d379d8ecabcb62d8a39a10db287fe9721761761c4f4058117abf
w11-211bsd_rpset/x/211bsd_rp.dsk                           174158848  570ef17586bc1ee6ac72a752db9eea4f8aa944a59d4b03869c3bf71178a158d5
pidp11-systems/x/systems/unix7/disk0.hp                    174423040  fc04dab1e7f2fbf3ea4b44603ed2c9d6ba15f4f86c702d4a27a1dac725966f39
w11-u7ed_rpset/x/u7ed_rp.dsk                                87960576  894d8755c9fe1f7a3626025812eeb4c2148c17cad4d111b5c5bdf08955f8f704
uv7swre/x/unix_v7_rl.dsk                                    10485760  235426852d2fdc2b7b3432f46bb2174d579a6e730f84d66f1464a2ae564a1c81
uv6swre/x/unix0_v6_rk.dsk                                    2077696  26d8885f4b0500f12174f5b9e5b484a230585593e8ad1767d82ce9db8b955afa
uv6swre/x/unix1_v6_rk.dsk                                    2440704  7a730aedc5f78f4f7b910994a0b1bec4bd05eb22e0fde043b08efd4439b75e17
uv6swre/x/unix2_v6_rk.dsk                                    2440704  8b6efcbd8aaaca0c997d0f158d7275069c3a38f8892f039f7831b9a4d4f13271
uv6swre/x/unix3_v6_rk.dsk                                    2440704  fa57755b83d8f3d051c5fb8c94f5c884fb9db0ea23b6ff82f92d990c72e29231
uv5swre/x/unix_v5_rk.dsk                                     2494464  af573c130bc4aadb2ec3d67983e6f1687bd49b994a0fe7c5101b407dd85eec16
pidp11-systems/x/systems/rt11/rt11V.dsk                     10478080  543277ee81fb4c024d19242bf6d6a04c81a257c48fe4cece256a42de3fe8dd98
pidp11-systems/x/systems/rt11/DEC-11-Graphics.dsk            2457600  2c278e15fc6cc24454320499e1e8bf3d04d1faf07e4d387d66535ea39d1b7f89
w11-rt11-53_rlset/x/RT11_V5.3_SYSTEM.dsk                    10485760  dfa187cf8fdc314bbdfb4af68e0a0b519d1b38f16bfc1fbd0ca7f6f472687a19
rtv53swre/x/Disks/rtv53_rl.dsk                              10475520  ac4770cbdaf809cb3099241086947b56a5e93b2c9ae1c4908b5c30d2f2479e69
rt11swre/x/Disks/rtv4_rk.dsk                                 1672704  ebf0da98feffe3cd82aad95c3f4143757be512199058f996fc15e7e96c173978
rsx-billquist/x/pidp.dsk                                  1024458752  8fd191a6c8ddd7cb988105a4821ea8b3bc80603e81ae48644af4acda5efbcd5c
rsx-billquist/x/pidp.tap                                    89062636  b0befc9a6ea3e927fecb74fdcd515f54c0f1c31edbb56efaa8efb763d1ce2a2b
w11-rsx11mp-30_rpset/x/rsx11mp-30.dsk                      174416896  b1c02788d76348a40fca9672eba4e6d20e95ea81d50a80a00a128b71227f1750
pidp11-systems/x/systems/rsx11mp/PiDP11_DU0.dsk            314880000  516eea6dd72e89ac4c99453fbe203ece9fc801e902cb118257d6a650112b05bb
rstsv7swre/x/Disks/rsts_full_rl.dsk                          5235200  686291135714664706df06691b6768cd7c6feb575d9ff73d7bc196a1b136298d
rstsv7swre/x/Disks/rsts_swap_rl.dsk                          5235200  009ee782fce3727734f2dbe3cfadb370295e489b4c45ad84647e18d46c230bac
rstsv7swre/x/Disks/rsts_min_rl.dsk                           5235200  bf17d27c76ff9a3607296c34f1c61223c4050f82b9ada7ce1189a4ec3c8a7c58
```
