# Open SIMH PDP-11 patches: front-panel export (+ pipe-safe prompt, timer calibration)

Pinned upstream: `https://github.com/open-simh/simh` commit
`87eb7d5e96f9ce0ee6ac183e20160e5c486b0712` (2026-09-22), recorded in
`simh/SIMH_COMMIT`. Build: §6. Implementation notes and the full measurement
tables: `simh/README.md`.

## 1. Files

1. `simh/patches/0001-pdp11-front-panel-export.patch`: the hooks of §3 in
   `PDP11/pdp11_cpu.c`, `pdp11_cpumod.c` and `pdp11_fp.c`, `STOP_PANEL`
   (`TRAP_V_MAX + 9`, message "Front panel HALT") in `pdp11_defs.h`/
   `pdp11_sys.c`, `&panel_dev` in `sim_devices[]`, `pdp11_panel.c` in the
   makefile and `PDP11/CMakeLists.txt` for `pdp11` and `uc15` (UC15 shares
   `pdp11_cpu.c`).
2. `simh/patches/0002-scp-readline-only-on-tty.patch`: in `read_line_p`
   (scp.c), use libedit's `readline()` only when `sim_ttisatty()`; otherwise
   `printf` the prompt, `fflush`, `fgets`. (Without it a host with
   `libedit-dev` produces a binary that never prints `sim> ` on a pipe.)
3. `simh/patches/0003-timer-idle-and-throttle-calibration.patch`
   (sim_timer.c):
   * in `sim_idle`, while no calibration has measured `sim_idle_cyc_ms` yet,
     derive it from the precalibrated execution rate instead of the timer's
     tick size. (Without it a guest that idles before the KW11-L completed its
     first calibration second, UNIX V5, freezes the KW11-L's 1 MIPS initial
     tick into it; after a short CPU burst the clock can then stall at about
     one tick per second for as long as the guest idles.) The first
     calibration second, which measures it, starts with the run: BOOT, GO and
     RUN no longer leave the time at the prompt in it (a bare-metal guest
     started a second after the simulator stalled the same way);
   * speed switches at run time (`set throttle 1M` and `set nothrottle` +
     `set cpu idle`, DESIGN §9): every new throttle setting measures the host
     again, from a fresh start, and ends by setting the clocks' tick size to
     the throttled rate; a calibration second in which the throttle slept no
     longer sets `sim_idle_cyc_ms`; the calibration forced when the throttle
     starts counts only the ticks it delivered. (Without it fast → authentic
     stalls the clock for good, authentic → fast stalls it after a short burst
     while the guest idles, every start of the throttle costs the guest up to
     a second, and authentic after a fast boot prints a bogus "Host CPU could
     be too slow" warning.) After a switch to fast the first calibration
     second still ends within milliseconds: the guest gets up to a second of
     ticks at once, which SimH's catch-up takes back within about 3 s.
4. `simh/panel/pdp11_panel.c`, `pdp11_panel.h`: the `PANEL` device and the
   hook functions; copied into `PDP11/` by `build.sh`, not part of a patch.
5. `simh/panel/pdp11_panel_shm.h`: the canonical shared-memory layout and
   protocol (Go mirror `server/internal/shm/layout.go`). Its `_Static_assert`s
   compile only under C11; SimH builds with `-std=gnu99`, so
   `pdp11_panel.c` repeats every offset/size check as a C89 typedef check.

The patches are `git diff`s with CRLF lines (Open SIMH stores these files with
CRLF; `simh/patches/.gitattributes` stops git converting them). The changed
SimH files keep Open SIMH's X11-style licence; our new files are MIT.

## 2. Device `PANEL`

* `ATTACH PANEL <file>`: `open(O_RDWR|O_CREAT|O_NOFOLLOW|O_CLOEXEC, 0600)`;
  refuses symbolic links and non-regular files, and leaves an existing file
  untouched that belongs to another user ("belongs to uid N, not to this user
  (uid M)") or grants group or others any access ("is open to other users
  (mode 0644; it must be 0600)"): the file is reused, and another local user
  who created it first in a world-writable directory such as `/dev/shm` could
  otherwise map it too (bus samples carry typed characters; halt requests and
  the switch register are writable). Takes an exclusive `flock(LOCK_NB)` held
  while attached (a second simulator fails with "in use by another
  simulator"); `ftruncate` to `sizeof(PANEL_SHM)` (262400); `mmap(MAP_SHARED)`.
  A file that already holds a valid version-3 header (magic, version, size,
  ring_size) keeps the bridge words (`sr`, `sr_valid`, `halt_req`,
  `target_rate`) **and the counters** (`ring_wr`, `stop_count`, `trap_count`,
  `instr_count`, `dr_on`, `weight_sum`; `seq` rounded up to even), so they stay
  monotonic for a reader that keeps its mapping across simulator restarts;
  anything else, a header of another version too, is zeroed. Then `halt_ack =
  halt_req` (old requests are void), `state = PNL_ST_HALTED`, header written
  (magic last), interval 1000, register block written. The reader
  (`shm.Open`) refuses any other version ("layout version 2 (the simulator and
  pdp11d come from different builds)") and makes the same file checks
  (`O_NOFOLLOW`, regular, owner, mode).
* `DETACH PANEL`: state HALTED, unmap, close (releases the lock); every hook
  becomes a no-op.
* `SET PANEL REFRESH`: rewrite the register block now (pdp11d uses it while
  halted after DEPOSIT and RESET so DR/PSW/MMR lamps are current).
* `SHOW PANEL`: file, measured and target rate, interval, samples, state.
  Read-only registers `INTERVAL`, `SAMPLES` (since ATTACH), `RATE`. `HELP PANEL`.
* `DEV_NOSAVE`, no DIB (not a bus device), present on every CPU model. Hosts
  without `mmap` (Windows, VMS, or `-DPANEL_NO_SHM`) compile stubs: ATTACH fails,
  hooks are no-ops. Compiles under SimH's `-Werror` flags.

## 3. Hooks

The ≤ 3 % detached budget (§7) rules out per-instruction work beyond a
register decrement and one store; the hooks below produce the sample content
of §4 with that cost (a first version with a hook in `ReadE`, a global
countdown and an IR store per instruction measured 7.6-8.8 % slower).

1. **Bus capture** (plain stores, no branches, **no hook in `ReadE`**): the
   virtual data entry points `ReadW`, `ReadB`, `ReadCW`, `ReadMW`, `ReadMB`,
   `WriteW`, `WriteB`, `WriteCW` store `va` including the `VA_DS` bit
   (`PANEL_VA`); the physical funnel `PReadW`, `PReadB`, `PWriteW`, `PWriteB`
   stores one 64-bit word on both the memory and the I/O-page paths
   (`PANEL_BUS`): PA | `PNL_WR`/`PNL_BYTE`/`PNL_IOPAGE` | a "cycle present" bit,
   data in bits 47:32 (byte cycles: the byte value). DMA and console
   EXAMINE/DEPOSIT are not captured.
2. **Instruction marker**: `panel_bus = 0` once per instruction, right before
   `inst_pc = PC`. If it is still 0 at a sample, the last instruction made no
   data reference after its fetch, and the sample is that fetch, **rebuilt** from
   `inst_pc`: PA = `relocC(inst_pc)` in the mode the instruction began in
   (`inst_psw`; unmappable → the last bus PA), `PNL_IFETCH` (+ `PNL_IOPAGE`),
   `va` = `inst_pc`, `data` = the word there. The `ir` of every instruction
   sample is rebuilt the same way (memory word at `inst_pc`; 0 for code in the
   I/O page); there is no IR store.
3. **Instruction sampler**: a `sim_instr` local countdown `panel_cd` (kept in a
   register) set to `panel_rearm ()` before the main loop, at entry and after
   every abort (the setjmp return path), and in the loop after the WAIT check,
   before the marker of hook 2: `if (--panel_cd <= 0) { panel_cd = panel_sample
   (); if (reason != 0) { trap_req &= ~TRAP_TRC; break; } }`. `panel_rearm`
   returns 2^31−1 detached, 1 before the first sample of a run (a pending halt
   request is honoured at once), and otherwise a uniformly random point of the
   current period (its own xorshift generator): the local does not survive the
   longjmp, and at a random moment the countdown is about evenly spread over
   the period, so an abort neither forces a sample (restarting at 1 sampled
   the trap push of every abort, inflated `instr_count` and ran the interval
   away to 10^7 above ~50 000 aborts/s) nor starves sampling (restarting with a
   whole period would never expire with an abort every few instructions).
   `panel_sample` writes one ring entry (§4) weighted with the wall-clock time
   it stands for (`panel_weight`, below), adds the elapsed countdown to
   `instr_count`, reloads the countdown with the interval ±12.5 % (32-bit
   Galois LFSR), and sets a WAIT countdown > 1 to 1 (the first WAIT iteration
   after an instruction sample is sampled).

   **Weights** (µs, ≥ 1, ≤ 2^32−1). A baseline advances by the weights given,
   so the weights of a run add up to its wall time (`CLOCK_MONOTONIC`; reset at
   `sim_instr` entry, so halted time is never weighted): exactly for a run that
   ends with a halt request, else less the residual at its end, the wall time
   not given yet (within ±4 ms). A sample after an idle sleep and the last
   sample of a run (a halt request) get the whole residual. Every other sample,
   of either sampler, stands for the work since the previous sample of its
   kind, its countdown steps (instructions, or WAIT iterations), at the wall
   time per step of the previous adaptation window (hook 5; sleep samples
   excluded), corrected so that the residual is paid back over the next 100 ms.
   Throttled (`set throttle 1M`) SimH executes ~1000 instructions at host speed
   and then sleeps 1 ms in its throttle event: weighted with the wall time
   since the previous sample, the first sample after a sleep got the whole
   millisecond and the ~50 others 1 µs each (the heaviest 2 % of the samples
   carried 95 % of the weight, ~17 effective samples per 60 Hz frame instead of
   ~830), so a partly lit lamp flickered ±50 % from frame to frame. The
   residual stays within ±4 ms (a sample that would leave more gets the
   difference, and before the first adaptation every sample gets the residual,
   i.e. its wall time); a sample that stands for less than 1 µs borrows its
   microsecond from the next 4 ms at most (beyond that such samples count
   extra: bursts at an interval of a few steps after an idle window, e.g.
   +0.2 % in a 3 s idle run).
4. **WAIT sampler**: `if (wait_state) { if ((--panel_wcountdown <= 0) &&
   panel_wait (FALSE)) continue; if (sim_idle (TMR_CLK, TRUE)) panel_wait
   (TRUE); continue; }`. WAIT iterations have their own countdown (so
   `instr_count` counts instructions only); it is checked **before**
   `sim_idle`, so a halt request never idles and `state` turns WAIT before the
   first sleep; `panel_wait (TRUE)` samples every sleep, and that sample carries
   the time slept (an idle CPU: one per `sim_idle` sleep, 60-120/s at 60 Hz as
   SimH may sleep twice in a tick, ≥ 95 % of the weight). After a sleep the
   WAIT countdown is ≥ 2^17, so the sub-millisecond spin SimH does after each
   sleep (the rest of the tick) is usually not sampled; the next WAIT sample
   carries its work. Where SimH spins longer or does not sleep in a tick (its
   next event too close, catch-up ticks), the countdown runs at the adaptive
   interval, which an idle window sets to a few iterations: such spins come
   out as bursts of light busy-wait samples, so an idle guest still gives
   thousands of WAIT samples per second (a bare WAIT loop 10 000-18 000/s,
   2.11BSD 1700-4900/s, with 1-3 % of the weight). Idle off or throttled,
   every WAIT iteration is busy-wait (~45 000 samples/s). `sim_gtime()` is not
   called per iteration.
5. **Adaptive rate**: from either sampler, once a window holds ≥ 100 ms of run
   time (halted time excluded), or earlier once it holds twice its share
   (`target/5 + 64` samples; this cuts the burst at interval ≈ 1 when a busy
   period follows an idle one), set the interval so samples/s ≈ target
   (`target_rate`, **0 = 50 000**; interval clamped to [1, 10^7]); publish
   `sample_interval` and the measured `samples_per_sec`. The same window
   measures the wall time per countdown step for the weights (hook 3).
6. **Halt handshake** (protocol in the header): every sample of either sampler
   compares `halt_req` (acquire) with `halt_ack`; if they differ it stores
   `halt_ack = halt_req` (release) and sets `reason = STOP_PANEL` (a VM stop
   code: SCP does not sleep 250 ms as after `SCPE_STOP`). The CPU stops
   **before the next instruction is fetched**: no instruction runs after the
   request is seen, so `reason = cis11 (IR)` and the like cannot overwrite
   `STOP_PANEL`, and the trace trap the T-bit line just requested for the
   unexecuted instruction is withdrawn (else T-bit tracing guests see a spurious
   trap per halt). In WAIT the check precedes `sim_idle`. `sim_instr` entry sets
   both countdowns to 1, so a request pending at entry (e.g. `cont` written just
   after the request) is honoured at once. A request is never discarded.
7. **Run state**: entry (`panel_enter`): `state = PNL_ST_RUNNING`, weight
   baseline reset; exit (`panel_exit (panel_cd)`, after the registers were
   saved): instructions since the last sample added to `instr_count`, register
   block written, `state = PNL_ST_HALTED`, `stop_count++`. (EXPECT actions exit
   and re-enter `sim_instr` silently, so these are not "halts" for pdp11d; it
   takes halted/running from the SCP prompt.)
8. **Switch register**: `SR = panel_sr (SR)` in `SR_rd` (`pdp11_cpumod.c`): if
   attached and `sr_valid` (acquire), `sr & 0177777`.
9. **ADRS ERR counter**: `trap_count++` in the trap dispatch loop of `sim_instr`
   for trap numbers ≤ `TRAP_V_MME` (red stack, odd address, NXM/timeout, MMU
   abort or trap), once per dispatched trap.
10. **Register block** (seqlock: `seq` odd → write → even, release fences):
    `r[0..7]` (inside `sim_instr` the working registers, current-mode SP; while
    halted the state SCP sees: `REGFILE` of the PSW's register set,
    `STACKFILE[mode]`, `saved_PC`, so DEPOSIT + `SET PANEL REFRESH` is
    reflected), `psw`, `dr`, `mmr0`, `mmr3`, `last_pa`/`last_data` (the most
    recent bus cycle as in hooks 1-2, with the sample flags), `pc_pa` (PA of PC
    in the current mode as for WAIT samples, no flags), `sim_time`,
    `sample_interval`, `samples_per_sec`, `instr_count`. Rewritten every 64
    samples, at every WAIT sample after a sleep (≤ one per idle wake-up), when
    the interval changes, at `sim_instr` exit, on ATTACH and on `SET PANEL
    REFRESH`; not at busy-wait WAIT samples otherwise (a throttled CPU takes
    ~45 000 of them per second).
11. **DISPLAY REGISTER on-time** (layout version 3, header bytes 144-211,
    outside the register block): every sample adds its weight to
    `dr_on[i]` for every bit i set in `DR` at the sample and to `weight_sum`
    (µs, modulo 2^32, relaxed stores before the sample's `ring_wr`). A reader
    takes differences: bit i was on for `Δdr_on[i] / Δweight_sum` of the time,
    weighted like the samples. The 17 words are not one snapshot: a sample
    stored while they are read can count in the next difference instead. (`dr` in the register block is the latch at the
    last refresh: a DR that a program changes every few instructions, e.g. a
    light chaser, shows one random value per 60 Hz frame there.) A wall-clock
    time stamp per DR write would cost too much (a counter demo writes DR
    every 3 instructions, ~50 million times per second).

Without an attached file every hook is a no-op apart from the plain stores and
the countdown decrements (both countdowns start at 2^31−1, also after aborts).

## 4. Sample semantics (for the reader)

* A sample is taken between instructions and shows the **last bus cycle of the
  last instruction** (or of its trap sequence): `MOV (R1)+,(R2)+` always shows
  its write. Immediate and absolute operand words are data reads in I space
  (not `PNL_IFETCH`). Only an instruction without data references shows its
  (rebuilt) fetch.
* `pc` is R7 at the sample (the next instruction); `ir`, `va`, `data` describe
  the last one. The mode flags are the mode of the cycle: for a data cycle the
  mode bits <18:17> of its virtual address (MTPI/MTPD write in the previous
  mode; RTI/RTT pop the stack in the mode they return from), for a rebuilt
  fetch (and the rebuilt `ir`) the mode the instruction began in
  (`inst_psw`). The FP11's multi-word stores go through `PWriteW`; their last
  word's virtual address is stored explicitly (`pdp11_fp.c`). `PNL_MMU` =
  MMR0<0>, `PNL_M22` = MMR3<4>.
* WAIT samples: `addr` = PA of PC relocated in the current mode (`relocC (PC,
  K/S/U)`; unmappable → the last bus PA) | `PNL_WAIT` | mode/MMU/M22 |
  `PNL_IOPAGE` when that PA is in the I/O page; `va` = `pc` = PC, `data` = R0,
  `ir` = 000001; mode flags of the current mode.
* A weight is the wall time the sample stands for (hook 3); the weights add up
  to the run's wall time. Duty cycles must be computed from the weights, not
  from sample counts (an idle CPU has few, heavy samples).
* `state` distinguishes RUNNING from WAIT only while the CPU executes; whether
  it is halted is decided by the SCP prompt.

## 5. Atomics

Simulator: `ring_wr`, `seq`, `state`, `stop_count`, `trap_count`, `halt_ack`
and `magic` with `__atomic_store_n(..., __ATOMIC_RELEASE)`; the register block
between release fences; `dr_on` and `weight_sum` relaxed; `halt_req` and
`sr_valid` loaded with `__ATOMIC_ACQUIRE`, `sr` and `target_rate` relaxed. A
release fence precedes the stores of every ring entry: a release store orders
only what precedes it, so without the fence the entry written into a slot
could become visible before the `ring_wr` that allows the overwrite, and a
reader lapped during its copy could take the torn entry for an intact one
(its check reloads `ring_wr` after the copy). Bridge: every bridge word (`sr`,
`sr_valid`, `halt_req`, `target_rate`) is written with a **single atomic 32-bit
store** (Go `sync/atomic`, whose loads are sequentially consistent). Any
`halt_req` ≠ `halt_ack` is a request, so a writer that clears a word before
writing it (Python's `struct.pack_into` does) causes spurious halts. On x86
(TSO) the fences cost nothing and the ordering holds by construction; for
arm64 the protocol is correct by the ARMv8 memory model (GCC emits `dmb ish`
for the fences, `stlr` for the release stores, and Go `ldar` for the loads),
but it has only been checked by reasoning, not on arm64 hardware or with a
litmus test.

## 6. Build (`simh/build.sh`)

`simh/build.sh [--src DIR] [--out DIR] [--jobs N] [--repo URL] [--baseline]
[--no-smoke]` (idempotent; every run repeats all steps, ~20-26 s):

1. `--src` (default `simh/src`): fetch the pinned commit (`git fetch --depth 1
   <repo> <commit>`, falling back to fetching all branches; an existing clone
   that has it needs no network). `--repo` (default `$SIMH_REPO` or the GitHub
   URL) may be a mirror or a local clone.
2. Check out, `git reset --hard`, `git clean -ffdx`, verify `HEAD`.
3. `git apply` `simh/patches/*.patch` in order (`--baseline`: only the
   non-panel patches 0002 and 0003, i.e. the unpatched CPU for overhead
   measurements); copy `simh/panel/*.[ch]` into `PDP11/`.
4. `make -j N TESTS=0 pdp11` (`--jobs`, default: number of CPUs). `$CC` is
   passed as `GCC=`, `$CFLAGS` as `OPTIMIZE=` (replacing `-O2 -DNDEBUG=1`),
   `$LDFLAGS` as `LDFLAGS_O=`.
5. Copy `BIN/pdp11` to a temporary file in `--out`, smoke-check it
   (`show version`, `show panel`, `exit` on a pipe in an empty directory with
   `HOME` there: requires a `sim> ` prompt and the PANEL device; `--no-smoke`
   for cross builds whose binary cannot run), then rename it to
   **`<--out>/pdp11`**. `--out` is a directory (default `simh/out`; the
   appliance uses `--out /opt/pdp11/bin`). A broken build never replaces a
   working binary.

Cross builds: `CC=<cross gcc> DONT_USE_ROMS=1 simh/build.sh --no-smoke` (the
environment reaches SimH's makefile; `DONT_USE_ROMS` skips its BuildROMs
helper, which would have to run on the build host; the PDP-11 needs no ROM it
generates). Requirements: bash, git, GNU make, a C compiler, `ca-certificates`
for the HTTPS fetch (`apt-get install --no-install-recommends` drops it). Optional
libraries (SDL, pcap, PCRE, VDE) are not needed; libedit is used if its headers
are installed. Plain C99 + GCC/Clang `__atomic` builtins + POSIX `mmap`/`flock`/
`clock_gettime`, no architecture-specific code. `simh/mkpatches.sh [--src DIR]`
regenerates the patch files from an edited tree (each patch keeps its
description and the files it touches). Not provided: Visual Studio and VMS
build entries (not targets); the CMake lists are edited but not exercised.
Research r1's `sim_timer.c` master-mode fix is not included (master mode is not
used).

## 7. Tests (`simh/test/`, Python 3 stdlib, `python3 -I`)

`python3 -I simh/test/run_tests.py [--bin simh/out/pdp11] [--quick]
[--baseline-bin PATH | --no-baseline] [--test-cache DIR] [--rounds N]
[--work DIR --keep] [pattern...]`. Simulators are driven like pdp11d (SCP on
pipes, console on localhost TCP, export mapped). Groups:

* **build**: prompt on a pipe (and with the host's libedit); PANEL commands and
  ATTACH errors (FIFO, symlink, directory, a file open to the group, second
  simulator); header layout vs the Python mirror (`-std=c11` and
  `-std=gnu99`).
* **asm**: the test programs' MACRO-11 subset assembler against SimH's
  `EXAMINE -M`.
* **panel**: header after ATTACH; ring advances only while running; weights ≈
  wall time (±10 %); throttled weights: the heaviest 2 % of the samples carry
  < 10 % of the weight and a lamp at a fifth (MASTER) varies per 60 Hz frame
  at most twice as much as unthrottled; WAIT samples idle, throttled and in
  user mode with 22-bit mapping and D space (PA vs VA, flags, rebuilt fetches,
  current-mode `pc_pa`); the mode of MTPI writes and RTI pops; the virtual
  address of FP11 multi-word stores;
  live `sr` seen by the guest within 10 ms without halting; halt request busy →
  `Front panel HALT, PC: …` within 25 ms and idling within one clock tick;
  **`cont` written and halt requested at once → stop within 25 ms (20/20)**;
  cancel protocol (a request that races a HALT instruction does not stop the
  next `cont`; negative control; 40 random races); halt transparent to T-bit
  tracing; `trap_count` (odd address, NXM); `set panel refresh` after `deposit
  17777570 x`; DETACH/ATTACH (bridge words, counters, stale request, garbage
  file, another version); `stop_count`; exact `instr_count` after `STEP n`; an
  abort every 4 instructions (samples/s at the target, no bus cycle with more
  than 40 % of the weight, `instr_count` within 5 % of the instructions
  executed, within 10 % after `STEP 3000000`, halt within 25 ms); the
  DISPLAY REGISTER accumulators (a 25 %/75 % duty within 3 points); rate
  adaptation (50k, 10k, 200k); identical end state attached/detached/unpatched.
* **perf**: hook overhead (SOB loop and memory-copy loop, median of ≥ 4 rounds of
  on-CPU time, baseline/detached/attached interleaved) and host CPU in a WAIT
  loop (`SET CPU IDLE=2`, PANEL attached).
* **boot**: Unix V6 to the prompt, with and without PANEL. The kit is the
  catalog's archive (`systems/unixv6/system.json`) from `$PDP11_TEST_CACHE`
  (or `--test-cache DIR`): a directory of archives named by their sha256, as
  `pdp11d fetch --cache DIR` stores them. The tests check the sha256 of the
  archive and of each image, extract the images into the work directory, and
  are skipped, naming `PDP11_TEST_CACHE`, without the archive.
* **timer** (patch 0003): the idle clock after a burst when the guest idles
  before the first calibration and is started a second after the simulator
  (the UNIX V5 stall; every 2 s window 1-9 s after the burst), and speed
  switches made as pdp11d makes them (panel HALT, the commands at the prompt,
  `cont`): authentic → fast (busy) → authentic; authentic (idle) → fast at
  the prompt → a burst one tick into a calibration second (and
  `sim_idle_cyc_ms` not the throttled rate); fast → authentic one tick into a
  calibration second (the guest loses no time, no throttle warning). At least
  30 of 60 ticks/s where the bugs give 0-5; about 55 s.

Acceptance criteria, and the Phase A results (QEMU VM, 16 vCPUs at 2.1 GHz,
GCC 15.2, other builds running; two complete runs, 30/30 tests). Rows and
values marked C2 come from the review fixes (39/39 tests; the same host,
loaded by other jobs; four runs of the perf group, two of the whole suite):

| Criterion | Limit | Measured |
|---|---|---|
| Slowdown, export detached (SOB / copy loop) | ≤ 3 % | 1.6-1.8 % / none (copy loop 2.8-3.9 % *faster*: code layout); C2: 0.4-2.1 % (once 0.9 % faster) / none (2.4-4.7 % faster) |
| Slowdown, attached at 50 000 samples/s | ≤ 10 % | 0.6-2.9 % / none (2.9-3.6 % faster); C2: 0.2-3.5 % / none (2.9-5.5 % faster) |
| Halt request while busy → stop line | ≤ 25 ms | 0.05-0.06 ms median, ≤ 0.09 ms max |
| `cont` + immediate request | 20/20 ≤ 25 ms | 20/20, ≤ 0.12 ms |
| Halt request while idling | ≤ 1 tick (16.7 ms) + 8 ms | 8.5-11.8 ms median, 16.4-21.0 ms max |
| Live switch register seen by the guest | ≤ 10 ms | 1.3 ms median, ≤ 1.5 ms |
| Sum of weights vs wall time | ±10 % | ≤ 0.1 % (busy and idle); C2: ≤ 0.1 % busy and throttled, ≤ 0.2 % idle |
| C2: throttled 1 MIPS, lamp at a fifth, per 60 Hz frame | sd ≤ 2 × unthrottled; heaviest 2 % of samples < 10 % of the weight | ± 3.3 vs ± 3.6 unthrottled; 2.5 % (before: ± 24.2, 95 %) |
| C2: an abort every 4 instructions | samples/s = target ± 20 %; `instr_count` ± 5 %; no bus cycle > 40 % | 49 927/s; +0.07 %; 25 % (before: 10.5 M/s, interval 10^7) |
| C2: DISPLAY REGISTER accumulators | ± 3 points | 25.2 % / 74.8 % for 25.3 % / 74.7 % |
| Idle WAIT loop host CPU (`IDLE=2`, attached) | ≤ 10 %, or ≤ unpatched + 3 points | 10.7 % / 16.1 % vs unpatched 10.4 % / 15.7 %, measured together |
| Unix V6 boot to `login:` | boots with and without PANEL | 0.31 s / 0.30-0.32 s |

The idle criteria are tolerant on purpose: idle halt latency is bounded by
SimH's own `sim_idle` sleep, which on a loaded host sometimes exceeds a tick
(the export checks right before and right after it), and the unpatched
simulator misses the absolute 10 % idle limit on such a host too (the export
adds < 0.5 points). Throttled at 1 MIPS (attached) the WAIT loop costs 4.2-5.9 %.
Not run in Phase A: arm64 and Docker builds, the CMake build.
