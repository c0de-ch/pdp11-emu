# Open SIMH PDP-11 with the front-panel export

This directory builds the `pdp11` simulator that pdp11d runs: Open SIMH at the
commit in `SIMH_COMMIT`, plus three patches and a new `PANEL` device that
exports bus-cycle samples, registers, the run/WAIT state, a live switch
register and a halt handshake through a shared-memory file. The contract is
[`docs/SIMH_PATCH.md`](../docs/SIMH_PATCH.md); the file layout is
[`panel/pdp11_panel_shm.h`](panel/pdp11_panel_shm.h).

| Path | |
|---|---|
| `SIMH_COMMIT` | pinned upstream commit (`87eb7d5e96f9…`, 2026-09-22) |
| `build.sh` | fetch, patch, build, smoke-check, install `out/pdp11` |
| `patches/0001-pdp11-front-panel-export.patch` | hooks in `PDP11/*.c`, stop code, device table, makefile and CMake entries |
| `patches/0002-scp-readline-only-on-tty.patch` | SCP prompt on a pipe when built with libedit |
| `patches/0003-timer-idle-and-throttle-calibration.patch` | the idle rate before the first calibration (UNIX V5 clock stall) and across throttle changes; run-time speed switches keep the KW11-L at 60 Hz |
| `panel/pdp11_panel.c`, `panel/pdp11_panel.h` | the `PANEL` device and the hook functions |
| `panel/pdp11_panel_shm.h` | shared-memory layout (contract; Go mirror `server/internal/shm/layout.go`) |
| `mkpatches.sh` | regenerate the patch files from an edited tree |
| `test/` | test suite (Python 3 standard library) |

`src/` (the SimH work tree) and `out/` are build outputs and are not
committed.

## Building

```sh
simh/build.sh                       # -> simh/out/pdp11
simh/build.sh --out /opt/pdp11/bin  # -> /opt/pdp11/bin/pdp11
```

`build.sh [--src DIR] [--out DIR] [--jobs N] [--repo URL] [--baseline] [--no-smoke]`

1. Fetches the pinned commit into `--src` (default `simh/src`): `git fetch
   --depth 1 <repo> <commit>`, falling back to fetching all branches. An
   existing clone that already has the commit needs no network. `--repo`
   (or `$SIMH_REPO`) can point at a mirror or a local clone.
2. Checks out the commit, `git reset --hard`, `git clean -ffdx` (the tree is
   always rebuilt from a clean state), and verifies `HEAD`.
3. `git apply`s `patches/*.patch` in order and copies `panel/*.[ch]` into
   `PDP11/`.
4. `make -j N TESTS=0 pdp11`. `$CC` is passed as `GCC=` (SimH's makefile
   ignores `CC`), `$CFLAGS` as `OPTIMIZE=` (replacing SimH's `-O2
   -DNDEBUG=1`), `$LDFLAGS` as `LDFLAGS_O=`. The build uses SimH's `-Werror`.
5. Copies `BIN/pdp11` to a temporary file in `--out` (a directory; default
   `simh/out`), runs the smoke check on it (`show version`, `show panel`,
   `exit` on a pipe, in an empty directory: requires a `sim> ` prompt and the
   `PANEL` device), then renames it to `<out>/pdp11`. A broken build never
   replaces a working binary.

`--baseline` builds the same commit with only the non-panel patches (0002 and
0003; the CPU without hooks), for overhead measurements. `--no-smoke` is for
cross builds whose binary cannot run on the build host. Running `build.sh`
again is safe and repeats all steps (about 20 s on 16 cores; the SimH makefile
has no incremental build).

Requirements: bash, git, GNU make, a C compiler (Debian/Ubuntu:
`build-essential git`), and `ca-certificates` for the HTTPS fetch (Debian's
`git` only *recommends* it, so `apt-get install --no-install-recommends` needs it
listed). Optional libraries (SDL, pcap, PCRE, VDE) are not needed; libedit is
used when its headers are installed (patch 0002 keeps the prompt working). The
code is plain C99 with GCC/Clang `__atomic` builtins and POSIX `mmap`, no
architecture-specific code (amd64 and arm64). Hosts without `mmap` (Windows,
VMS) compile the device as stubs whose `ATTACH` fails. In a Docker build, keep
`simh/src` and `simh/out` out of the build context (`.dockerignore`).

## What the patches do

### 0001: hooks (`PDP11/pdp11_cpu.c` and friends)

| Where | Hook | Cost per event |
|---|---|---|
| `sim_instr`, entry / exit | `panel_enter ()`, `panel_exit (panel_cd)` | once per run |
| main loop, between the WAIT check and `inst_pc = PC` | `if (--panel_cd <= 0) { panel_cd = panel_sample (); if (reason) { clear T-bit trap; break; } }` then `panel_bus = 0` | a register decrement and one store per instruction |
| before the main loop (entry and every abort) | `panel_cd = panel_rearm ()` | once per run and per abort |
| WAIT state | `if ((--panel_wcountdown <= 0) && panel_wait (FALSE)) continue;` then `if (sim_idle (...)) panel_wait (TRUE);` | per WAIT iteration |
| `ReadW`, `ReadB`, `ReadCW`, `ReadMW`, `ReadMB`, `WriteW`, `WriteB`, `WriteCW`; the last word of the FP11's multi-word stores (`WriteI`, `WriteFP` in `pdp11_fp.c`) | `PANEL_VA (va)`: virtual address with the D-space and mode bits | one store |
| `PReadW`, `PReadB`, `PWriteW`, `PWriteB`, memory and I/O-page paths | `PANEL_BUS (pa, data, flags)`: one 64-bit word (PA, `PNL_WR`/`PNL_BYTE`/`PNL_IOPAGE`, data) | one store |
| trap dispatch | `panel_trap ()` for red stack, odd address, NXM, MMU (`trap_count`, ADRS ERR) | per such trap |
| `SR_rd` (`pdp11_cpumod.c`) | `SR = panel_sr (SR)`: the bridge's switch register when `sr_valid` | per read of 17777570 |

Plus `STOP_PANEL` (`TRAP_V_MAX + 9`, "Front panel HALT") in `pdp11_defs.h` and
`pdp11_sys.c`, `&panel_dev` in `sim_devices[]` (pdp11 and UC15), and
`pdp11_panel.c` in the makefile and `PDP11/CMakeLists.txt` for `pdp11` and
`uc15` (CMake lists were edited to match what `cmake/generate.py` would
produce). Without an attached file every hook is a no-op apart from the plain
stores and the countdown decrements: both countdowns start at 2^31 − 1, also
after aborts. Attached, the instruction countdown (a local, lost in the
longjmp of an abort) restarts at 1 at entry and, after an abort, at a random
point of its period, so aborts neither force samples nor change the rate.

### 0002: SCP prompt on a pipe with libedit

`read_line_p` calls libedit's `readline ()` only when `sim_ttisatty ()`;
otherwise it prints the prompt, flushes and uses `fgets`, as the build without
libedit does. Without it, a host with `libedit-dev` produces a binary that
never prints `sim> ` on a pipe (verified: an unpatched build against the
host's `libedit.so.2` prints no prompt). History is kept for terminal input
only.

### 0003: idle and throttle calibration (UNIX V5 clock stall, speed switches)

`sim_idle` sleeps `sim_interval / sim_idle_cyc_ms` milliseconds; only a
calibration second with less than 1% idle measures `sim_idle_cyc_ms`, and
until then the first `sim_idle` derived it from the calibrated timer's tick
size. UNIX V5 idles a few milliseconds after its kernel starts the KW11-L,
whose initial tick is 16667 cycles at 60Hz (1 MIPS): `sim_idle_cyc_ms` froze
at 1000 on a host running ~65 million instructions per second. Idle ticks
then calibrate to ~17000 cycles, a short busy burst (`cc`) ends a calibration
second within tens of milliseconds and multiplies the tick size up to ~90
times, every idle tick sleeps about a second, and each following calibration
second (> 30 s) is discarded as "gap too big": the guest clock stalls (`date`
+1 s per minute, tty delays of seconds). The patch uses the precalibrated
execution rate while `sim_idle_cyc_ms` is unset; the first calibration then
measures it (systems busy in their first second, `SET CPU IDLE=n` and
throttling never reach that code). `test/tests/test_timer.py` reproduces the
stall with a bare-metal program (1-2 ticks/s without the patch).

That first calibration second must measure the run alone. `BOOT`, `GO` and
`RUN` zero `sim_gtime ()` and, through the device resets, set the clocks'
calibration start to the time the simulator last stopped (at start-up:
before the 100 ms precalibration), and `sim_start_timer_services` added the
time stopped only when `sim_gtime ()` was not 0. So the first calibration
second after a boot included the time at the prompt. UNIX V5's kernel
resets the KW11-L once it runs, but a bare-metal guest started 0.3 s after
the simulator idled at 5442 instead of ~110 000 cycles/ms (a burst then
slowed its clock to 12 ticks/s for 5 s), and one started a second after it
stalled at 1 tick/s like V5 before the patch. The time stopped now always
moves the calibration start.

pdp11d switches speed at run time with a transparent stop: `set throttle 1M`
(authentic) or `set nothrottle` + `set cpu idle` (fast), then `cont`. Three
faults of the same code made the guest clock stall or lose time:

* `set nothrottle` left the throttle in its THROTTLE state, so a later `set
  throttle 1M` resumed the old wait and skipped the measurement whose end
  (`_sim_timer_adjust_cal`) sets the clocks' tick size to the throttled rate.
  The KW11-L kept the tick size calibrated at the fast rate (1.9 M
  instructions per tick) at 1 MIPS: 0-1 ticks/s, every calibration second
  longer than 30 s and discarded as "gap too big", for good. Every new
  throttle setting except the fixed `N/M` form now starts the measurement
  again, from a fresh start (`sim_throt_sched`): with the clock calibrated it
  takes about 20 ms at full speed. The old start, 0 after a fast boot, made it
  average over the time at the prompt and print "Host CPU could be too slow
  to simulate 1,000,000 instructions per second".
* While throttled, a calibration second at 0 % idle (idling is off) measured
  `sim_idle_cyc_ms` at the throttled rate, about 930 cycles/ms, because the
  throttle's sleeps are part of its wall time. After a switch to fast an idle
  guest kept that value, the V5 state: a 50 ms burst took the tick size from
  22 828 to 624 364 cycles and the clock to 1-3 ticks/s. `sim_rtcn_calb` now
  skips that measurement in a second in which the throttle slept.
* The calibration that `_sim_timer_adjust_cal` forces on the next tick counted
  a whole second of virtual time for the ticks of the current second so far;
  the catch-up then slowed the clock until the guest had lost up to a second,
  at every start of the throttle (0.5 s on average per fast → authentic
  switch). The forced second now counts only its ticks.

Measured with a bare-metal guest, the commands and stops made as pdp11d
makes them, 3 rounds per transition: after every kind of switch (fast boot →
authentic, authentic → fast → a 50 ms burst, authentic → fast → authentic,
running or halted at the prompt) the KW11-L runs at 60 ticks/s within 1-3 s
and stays there after a burst. Without this part of the patch: 0-1 ticks/s
after fast → authentic, 1-14 ticks/s for seconds after the burst. Three
authentic/fast toggles of 4-6 s change the guest clock by −0.03 to +0.04 s
over 30 s (−1.4 to −1.7 s without the forced-second fix, −15 s and growing
without this part). After a switch to fast the first calibration second still
ends within milliseconds (the tick size is the throttled one): the guest gets
up to a second of ticks at once, and SimH's catch-up holds 40 ticks/s for
2-3 s to take it back; toggling every second adds these up (40 ticks/s for
about 7 s after six switches).

### The PANEL device

```
ATTACH PANEL <file>    open/create (mode 0600, no symlinks, regular files only; an existing
                       file must be this user's and closed to group and others), flock it
                       (one simulator per file), size it to 262400 bytes, mmap MAP_SHARED
DETACH PANEL           unmap; the hooks become no-ops
SET PANEL REFRESH      rewrite the register block now (after DEPOSIT/RESET while halted)
SHOW PANEL             file, measured and target rate, interval, samples, state
EXAMINE PANEL INTERVAL,SAMPLES,RATE    read-only registers
HELP PANEL
```

`DEV_NOSAVE`, no DIB (not a bus device), present on every CPU model. ATTACH of
a file that holds a valid version 3 header keeps the bridge words (`sr`,
`sr_valid`, `halt_req`, `target_rate`) **and the counters** (`ring_wr`,
`stop_count`, `trap_count`, `instr_count`, `dr_on`, `weight_sum`, `seq`, so
they stay monotonic for a reader that keeps its mapping across simulator
restarts); anything else (another version too) is zeroed. ATTACH sets
`halt_ack = halt_req`. It refuses an existing file of another user or one that
grants group or others access (another local user could have created it
first in `/dev/shm` and map it too), and leaves it untouched.

## How the export behaves (notes for the reader side)

* **A sample shows the last bus cycle of the last instruction** (or of the
  trap sequence), taken between instructions. `mov (r1)+,(r2)+` therefore
  always appears as its write. The instruction fetch has no hook: the CPU
  clears `panel_bus` once per instruction and only data references store into
  it; if it is still clear at a sample, the sample is the fetch of the
  instruction at `inst_pc`, rebuilt by relocating `inst_pc` in the mode the
  instruction began in and reading memory (`PNL_IFETCH`, `va` = that PC,
  `data` = its opcode). In every instruction sample `ir` is the opcode at
  `inst_pc`, i.e. of the last instruction (0 for code in the I/O page), and
  `pc` is R7 at the sample (the next instruction). Immediate and absolute
  operand words are data reads in I space. The mode flags are those of the
  cycle: a data cycle has the mode of its virtual address (an MTPI write is in
  the previous mode, an RTI pop in the mode it returns from), a rebuilt fetch
  the mode its instruction began in.
* **Weights** are the wall-clock microseconds a sample stands for (≥ 1). A
  sample after an idle sleep, and the last of a run (halt request), gets the
  wall time not yet given to a sample; every other one its work (countdown
  steps since the previous sample of its kind) at the wall time per step of
  the previous 100 ms window, corrected so that what is left over is paid
  back over the next window (and never more than 4 ms). So the weights of one
  run still add up to its wall time (measured: within 0.2 %; the baseline is
  reset at `sim_instr` entry, halted time is never weighted), and a throttled
  CPU, which SimH runs at host speed for ~1000 instructions and then puts to
  sleep for a millisecond, does not give that millisecond to one sample: with
  wall time since the previous sample as the weight, the heaviest 2 % of the
  samples carried 95 % of the weight at `set throttle 1M`, and a lamp at a
  fifth varied ±25/255 from frame to frame instead of ±3.5.
* **DISPLAY REGISTER on-time** (`dr_on[16]`, `weight_sum`, version 3): every
  sample adds its weight to the accumulator of every DR bit set at the sample
  and to `weight_sum`; differences between two reads give the time-weighted
  DR (pdp11d's DISPLAY REGISTER lamps while running).
* **WAIT samples** (`PNL_WAIT`, `addr` = PA of PC relocated in the current mode,
  `va` = `pc` = PC, `data` = R0, `ir` = 000001):
  * with idling, after every `sim_idle` sleep (the sample carries the time
    slept; one or two per tick). SimH sleeps whole milliseconds and spins out
    the rest of a tick; that spin is usually not sampled (the WAIT countdown is
    at least 2^17 after a sleep; the next WAIT sample carries its work);
  * when the WAIT countdown expires: busy wait (idling off, or throttled) at
    the adaptive rate, and spins SimH does not sleep through (an event too
    close, catch-up ticks). An idle window sets the interval to a few
    iterations, so these come in bursts: an idle guest gives thousands of
    WAIT samples per second (a bare WAIT loop 10 000-18 000, 2.11BSD
    1700-4900, together 1-3 % of the weight), not one per tick;
  * at the first WAIT iteration after an instruction sample, before `sim_idle`
    can sleep, so that `state` becomes `PNL_ST_WAIT` at once.
* **Rate**: the instruction and WAIT countdowns share the interval; at least
  every 100 ms of run time it is set so that samples/s ≈ `target_rate`
  (default 50 000; `[1, 10^7]`), earlier when a window already holds twice its
  share (cuts the burst at interval ≈ 1 when a busy period follows an idle
  one). Every reload is dithered ±12.5 % with a 32-bit Galois LFSR.
  `sample_interval` and `samples_per_sec` are published in the register block.
* **`instr_count`** counts instructions only (WAIT iterations have their own
  countdown): exact after `STEP n` without aborts (tested for n = 1 … 3 000
  000); with aborts, whose countdown restarts at random, unbiased but
  approximate (an abort every 4 instructions: within 0.6 % of the instructions
  executed over a second, +1.7 % after `STEP 3000000`).
* **Register block** (seqlock): every 64 samples, at every sleep WAIT sample,
  when the interval changes, at `sim_instr` exit, on ATTACH and on `SET PANEL
  REFRESH`. Inside `sim_instr` it shows the working registers; while halted
  the state SCP sees (`saved_PC`, `REGFILE`, `STACKFILE`, `PSW`; the mode and
  register set from `PSW`), so `DEPOSIT R0/PC/PSW/USP/MMR3` + `SET PANEL REFRESH`
  are reflected. `last_pa`/`last_data` follow the sample rules above.
* **Halt handshake**: every sample compares `halt_req` with `halt_ack`; on a
  difference the simulator stores `halt_ack = halt_req` (release) and stops with
  `Front panel HALT` *before the next instruction is fetched* (so no instruction
  runs after the request is seen, nothing can overwrite the stop reason, and a
  pending T-bit trace trap for the unexecuted instruction is withdrawn). In WAIT
  the check happens before `sim_idle` sleeps. `sim_instr` entry samples at once,
  so a request pending at `cont` is honoured immediately; requests are never
  discarded. **Any** value of `halt_req` different from `halt_ack` is a request:
  the bridge must write these words with single atomic 32-bit stores (in Python
  `struct.pack_into` zeroes the bytes before writing and caused spurious halts in
  an early version of the tests).
* **`trap_count`** is incremented in the trap dispatch loop of `sim_instr`
  for trap numbers ≤ `TRAP_V_MME` (red stack, odd address, NXM/timeout, MMU
  abort or trap), i.e. once per dispatched trap of these kinds.
* **`state`**: `PNL_ST_RUNNING` at entry and at instruction samples,
  `PNL_ST_WAIT` at WAIT samples, `PNL_ST_HALTED` at exit; `stop_count` is
  incremented at every `sim_instr` exit (including the silent EXPECT exits),
  after the register block and `state` were written.

### Why the hooks are not the obvious ones

The first version followed the obvious design (a global countdown before
`ReadE`, a `panel_ir` store after it, and three stores in `ReadE`); it cost
**7.6–8.8 %** on the SOB loop with the export detached, against the ≤ 3 % limit.
The hooks now in `docs/SIMH_PATCH.md` §3 produce the same sample content more
cheaply:

| Obvious form (first version) | Implemented | Why |
|---|---|---|
| `ReadE` captures the fetch (`PNL_IFETCH`) | no hook in `ReadE`; `panel_bus = 0` per instruction, the fetch is rebuilt at the sample from `inst_pc` | ~6 % of the overhead |
| `panel_ir` stored after the fetch | IR rebuilt at the sample from memory at `inst_pc` | one store per instruction |
| `if (--panel_countdown <= 0) panel_sample ();` immediately before the fetch | a `sim_instr` local countdown (kept in a register), placed before `inst_pc = PC` (still before the fetch); re-armed at a random point of its period after aborts | global load/store per instruction |
| the sample happens, the instruction is still executed | the CPU stops before the fetch when a halt was requested | `reason = cis11 (IR)` could otherwise overwrite `STOP_PANEL` |
| WAIT samples on the instruction countdown | own WAIT countdown, checked before `sim_idle`; at least 2^17 after a sleep | `instr_count` counts instructions only; no sampling of the post-sleep spin; no idling after a halt request |
| refresh the register block on every WAIT sample | on every sleep WAIT sample (≤ one per idle wake-up), every 64 busy-wait samples | throttled CPUs busy-wait at 50 k WAIT samples/s |
| adapt once ≥ 100 ms passed | also earlier when the window holds 2× its share | bounds the burst after idle periods |

## Changing the patches

```sh
simh/build.sh                  # leaves simh/src patched (panel files copied in)
$EDITOR simh/src/PDP11/pdp11_cpu.c
simh/mkpatches.sh              # rewrites patches/*.patch, keeps their descriptions
simh/build.sh
```

`mkpatches.sh` regenerates each patch as `git diff` against the pinned commit
of the files that patch already touches. The patches contain CRLF lines
(Open SIMH stores these files with CRLF); `patches/.gitattributes` keeps git
from converting them. Edit `panel/*` in this directory, not in `simh/src`.

## Tests

```sh
python3 -I simh/test/run_tests.py [--bin simh/out/pdp11] [options] [pattern ...]
```

| Option | |
|---|---|
| `--baseline-bin PATH` | unpatched simulator for the overhead test (default: built with `build.sh --baseline` into the work directory, from `simh/src` if present) |
| `--no-baseline` | no baseline: overhead attached vs detached only |
| `--test-cache DIR` | catalog archives named by their sha256, as `pdp11d fetch --cache DIR` stores them (default `$PDP11_TEST_CACHE`); the boot tests take the Unix V6 kit from it and are skipped without it |
| `--quick` | skip performance, boot, timer and the libedit build |
| `--rounds N` | benchmark rounds (default 7, at least 4) |
| `--work DIR`, `--keep` | keep the work directory (simulator transcripts, shm files) |
| `pattern` | run tests whose id contains it (`test_panel`, `test_halt`, ...) |

Everything runs as real simulator processes driven like pdp11d: SCP on
stdin/stdout pipes, the console on a localhost TCP port (`notelnet`,
buffered), the export mapped with `mmap`. Test programs are written in a small
MACRO-11 subset (`test/simtest/asm11.py`); a test checks every instruction of
every program against SimH's own disassembler (`EXAMINE -M`).

| Group | Tests |
|---|---|
| build | prompt on a pipe; PANEL commands and ATTACH errors (FIFO, symlink, directory, a file open to the group, second simulator); layout of the header vs `test/simtest/panel.py` (host C compiler, `-std=c11` with the static asserts and `-std=gnu99`); libedit: built against the host's `libedit.so.2` when no `libedit-dev` is installed: prompt on a pipe, line editing (history recall) on a pty |
| asm | assembler vs `EXAMINE -M` |
| panel | header/registers after ATTACH; ring advances only while running; weights = wall time (busy, idle); throttled weights (the heaviest 2 % of the samples, frame-to-frame duty vs unthrottled); WAIT samples with idle on, throttled, and in user mode with 22-bit mapping and D space (PA vs VA, mode/MMU/M22/D-space flags, rebuilt fetches, current-mode `pc_pa`); mode of MTPI writes and RTI pops; FP11 store addresses; live SR within 10 ms; halt request busy (≤ 25 ms, 20 runs) and idling (≤ 1 tick); `cont` + immediate request (20/20 ≤ 25 ms); cancel protocol (deterministic, negative control, 40 random races); halt transparent to T-bit tracing; `trap_count` (odd address, NXM); `SET PANEL REFRESH` after DEPOSIT; DETACH/ATTACH (bridge words, counters, stale request, garbage file, another version); `stop_count`; exact `instr_count`; an abort every 4 instructions (rate, `instr_count`, halt); DISPLAY REGISTER accumulators; rate adaptation (50k, 10k, 200k); sample content (SOB loop, copy loop); identical end state attached/detached/baseline |
| perf | overhead of the hooks (SOB loop, copy loop; baseline / detached / attached at 50 k/s, interleaved, all on one host CPU, on-CPU time of the simulator thread from schedstat); host CPU in a WAIT loop with `SET CPU IDLE=2` |
| boot | Unix V6 to `# `, `ls /`, `sync`, with and without PANEL; WAIT samples while V6 idles; idle host CPU. The kit is the catalog archive of `systems/unixv6/system.json` from the test cache (sha256 of the archive and of each image checked, images extracted into the work directory) |
| timer | patch 0003, bare-metal programs, 60 nominal and at least 30 ticks/s required where the faults give 0-5: the idle clock after a burst (every 2 s window 1-9 s after it) when the guest idles before the first calibration and starts a second after the simulator (V5); authentic → fast (busy) → authentic; authentic (idle) → fast at the prompt → a burst one tick into a calibration second, and `IDLE_CYC_MS` not the throttled rate; fast → authentic one tick into a calibration second: the guest clock within 0.4 s, no throttle warning. About 55 s |

## Measured results

Host: QEMU VM, 16 vCPUs at 2.1 GHz, GCC 15.2, Ubuntu, with other builds running
at the same time. Two complete runs of the suite (30 tests, all pass, about
2.5 min each); where they differ the table gives both. Rows marked C2 come
from the review fixes (39 tests, all pass; the same host, loaded by other
jobs; ranges over four runs of the perf group, two of the whole suite).

| Measurement | Result | Limit |
|---|---|---|
| Overhead, export detached: SOB loop / copy loop (median of 7, on-CPU time) | −1.6 … −1.8 % / +2.8 … +3.9 % (162–171 / 113–118 MIPS baseline); C2: −2.1 … +0.9 % / +2.4 … +4.7 % | ≤ 3 % |
| Overhead, attached at 50 k samples/s | −0.6 … −2.9 % / +2.9 … +3.6 %; C2: −0.2 … −3.5 % / +2.9 … +5.5 % (old and new export side by side: 158.2 / 161.3 MIPS SOB loop) | ≤ 10 % |
| Same, first version with the contract-literal hooks, detached | −7.6 … −8.8 % / −1 … −5 % | |
| Halt request while busy → `Front panel HALT` on stdout | 0.05–0.06 ms median, ≤ 0.09 ms max (20 runs) | ≤ 25 ms |
| `cont` written, halt requested at once | 20/20, 0.02–0.03 ms median, ≤ 0.12 ms max | 20/20 ≤ 25 ms |
| Halt request while idling (sleeping in WAIT) | 8.5–11.8 ms median, 16.4–21.0 ms max | ≤ 1 tick (16.7 ms) |
| Live switch register seen by the guest | 1.3 ms median, ≤ 1.5 ms max, no halt | ≤ 10 ms |
| Sum of the weights vs wall time | busy 1.001 s / 1.001 s; idle 3.018 s / 3.015 s (≤ 0.1 %); C2: busy +0.0 … +0.1 %, idle −0.0 … +0.2 % (bursts of samples standing for less than the 1 µs they get), throttled −0.1 … +0.05 % | ±10 % |
| C2: throttled at 1 MIPS, a lamp lit at a fifth (MASTER), per 60 Hz frame | 50.4 ± 3.3 of 255, the heaviest 2 % of the samples carry 2.5 % of the weight (unthrottled 51.3 ± 3.6; before the fix ± 24.2 and 95 %) | ≤ 2 × unthrottled, < 10 % |
| C2: an abort every 4 instructions (17.7 M aborts/s) | 49 927 samples/s, `instr_count` 7.076 · 10^7/s vs 7.071 · 10^7 cycles/s, the heaviest bus cycle 25 % of the weight, halt 0.10 ms; `STEP 3000000` → 3 050 759 (before: 10.5 M samples/s, interval 10^7, the trap push 100 %, 7.5 M after `STEP 30002`) | target ± 20 %, ± 5 %, < 40 %, ≤ 25 ms |
| C2: DISPLAY REGISTER accumulators, 25.3 % / 74.7 % duty | 25.2 % / 74.8 % | ± 3 points |
| Rate adaptation (target → measured) | 50 000 → 49 952–52 019, 10 000 → 9 990–10 021, 200 000 → 199 945–200 505 /s | |
| `instr_count` after `STEP n` | exact for n = 1, 1000, 100 000, 3 000 000 (no aborts) | |
| Idle WAIT loop (`SET CPU IDLE=2`), host CPU | attached 10.7 % / 16.1 % vs unpatched 10.4 % / 15.7 %, measured together | ≤ 10 % |
| Same, throttled at 1 MIPS, attached | 4.2–5.9 % host CPU, ~45 000 WAIT samples/s | |
| WAIT samples while idling | ~40–50 sleep samples/s (95 % of the time) + busy-wait samples in ticks SimH does not sleep; C2: 44 sleep samples/s (94 % of the time) and 18 255 light busy-wait samples/s (`SET CPU IDLE=2`) | |
| Unix V6 (4 × RK05) to `login:` | 0.31 s with PANEL, 0.30–0.32 s without; `ls /` ok; idle 6.7–8.0 % vs 12.7 % host CPU | boots both ways |
| Cancel protocol, 40 random races with a HALT instruction | 12–17 Front panel HALT, 23–28 HALT instruction, no spurious stop | |
| Deterministic `STEP 2000000` | identical end state attached, detached and unpatched | |

Notes:

* The 10 % WAIT-loop limit is not met on this loaded, virtualized host by the
  **unpatched** simulator either (10–16 % in this loop); the export adds less
  than half a point. The test accepts the absolute limit, or at most 3 points
  above the unpatched simulator measured at the same time.
* Idle halt latency is bounded by SimH's own `sim_idle` sleep (the export
  checks right before and right after it). On this host SimH sometimes sleeps
  longer than a tick (median sleep 22 ms), hence maxima slightly above
  16.7 ms; the test allows one tick + 8 ms.
* The overhead varies by about ±2 % between runs; the copy loop is
  consistently a little *faster* with the patch (code layout effects).
