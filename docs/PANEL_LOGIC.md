# PDP-11/70 console: behaviour, power policy and lamp engine (`internal/panel`)

Sources: PDP-11/70 Processor Handbook ch. 11, KB11-C manual §III, the console
schematic, REALCONS (BlinkenBone) and PiDP-11 code (re-implemented, not copied),
experiments on Open SIMH (research r1-r3, design review). Deviations from the
real machine are marked **[DEV]**.

## 1. Controls (persisted in `$DATA/panel.json`)

| Control | Values | Type |
|---|---|---|
| `sr` | 22-bit integer (SR21..SR0), paddle up = 1 | latching toggles |
| `lampTest` | bool (paddle raised) | momentary (up) |
| `LOAD_ADRS`, `EXAM`, `CONT`, `START` | pressed bool | momentary (down) |
| `DEP` | pressed bool | momentary (**up**) |
| `enableHalt` | `enable` (up) / `halt` (down) | latching |
| `sinstSbus` | `inst` (up) / `bus` (down) | latching |
| `addrKnob` | 0..7 clockwise: 0 PROG PHY, 1 CONS PHY, 2 KERNEL D, 3 SUPER D, 4 USER D, 5 USER I, 6 SUPER I, 7 KERNEL I (pointer 22.5° + 45°·k from 12 o'clock; wraps) | rotary |
| `dataKnob` | 0..7, function = k mod 4: 0 BUS REG, 1 DATA PATHS, 2 µADRS FPP/CPU, 3 DISPLAY REGISTER (22.5° + 45°·k; wraps) | rotary |
| `key` | `off` / `power` / `lock` (225° / 315° / 45°; no wrap) | key switch |

Defaults (first start): sr 0, enable, inst, addrKnob 0 (PROG PHY), dataKnob 1
(DATA PATHS), key power. `New()` calls `Machine.SetSwitchRegister(sr & 0177777)`.
The latching controls are written to `panel.json` (`version` 1) atomically,
about 250 ms after a change (debounced) and when `Run` returns; a damaged file
is renamed to `panel.json.bad` and replaced by the defaults, an invalid field
by its default. Momentary switches are not persisted.

Momentary actions fire on the **rising edge** (the first client to press the
switch); releases only update the visual state; a switch stays down while any
client holds it, and a client disconnecting while holding one releases it.

**Queue.** The panel never blocks its input path on the Machine: Machine
operations run on one worker goroutine, in order (≤ 1024 queued). When
nothing is queued or in flight an input is judged against the current state at
once (its hint is the synchronous answer to the sender); otherwise it queues
behind the operation in flight. A queued console action uses the **switch
positions from the moment it was pressed** (SR, ENABLE/HALT, S INST/S BUS,
ADDRESS knob) and the **CPU state from the moment it runs**; if it is ignored
then, its hint goes to its sender asynchronously (§6 `SubscribeHints`). After a
Step or Halt the worker waits (≤ 1 s) for the EvStop, after a PowerOn for the
panel to have seen the resulting state, so the next queued action sees the
console state those events set. `ConsoleInfo.Busy` names the operation in
flight: `power on`, `power off`, `boot`, `shutdown`, `halt`, `step`,
`continue`, `start`, `reset`, `examine`, `deposit`, `load address`, or "".
Two inputs bypass the queue: the key at OFF (§3 Key) and the ENABLE/HALT level
behind a long operation (§3).

`effectiveHalt` = `enableHalt == halt && key != lock` (LOCK forces the switch's
*effect* to ENABLE without moving the paddle).

## 2. Console state

* `consAddr` (22 bits) = SWR<21:16> (latched at LOAD ADRS) + SR<15:0> (LOAD ADRS
  value, stepped value, or PC after a stop).
* `lastOp`: `none` / `exam` / `dep` (stepping); `adrsErr` (static console error);
  `dataPaths` (DATA PATHS value while halted); `consPA` (physical address of the
  last console EXAM/DEP, for PROG PHY while halted; cleared at the next start);
  `lastStop`, `pc`.
* **At every power-on and every Boot restart** (Machine state `starting`):
  consAddr 0, dataPaths 0, lastOp none, adrsErr false, consPA none, lastStop
  "", pc 0.

## 3. Rules

Console switches (LOAD ADRS, EXAM, DEP, CONT, START) act **only when** Machine
state is `halted` and key is not `lock`; otherwise they are ignored (the paddle
still moves; the input returns a hint such as "Works only when the CPU is
halted — set ENABLE/HALT to HALT." for the UI). In `sinstSbus = bus`, LOAD ADRS,
EXAM and DEP do nothing.

* **SR toggles**: always live: `Machine.SetSwitchRegister(sr & 0177777)`. Bits
  21:16 are not program-visible.
* **LOAD ADRS**: `consAddr = sr`; `lastOp = none`; `adrsErr = false`; `dataPaths = 0`.
* **EXAM**: if `adrsErr` → ignored (needs a new LOAD ADRS). If `lastOp == exam` →
  `consAddr = step(consAddr)` first. `Machine.Examine(loc(consAddr))`. Success:
  `dataPaths = value`, `lastOp = exam`, `consPA = Translate(loc)` (physical
  locations: the address itself; registers: none). `ErrAddress`: `adrsErr = true`,
  `lastOp = none`; if this EXAM was not a step, `consAddr &^= 0177777` (observed on
  a real 11/70). Other errors (e.g. ErrNotHalted because another viewer resumed
  the CPU): ignored with a hint, no ADRS ERR, the step is undone. A stop or
  power-on that rewrote the console state meanwhile wins.
* **DEP**: as EXAM, but deposits `sr & 0177777`; repeat of DEP steps first.
  Success: `dataPaths = value`, `lastOp = dep`. (EXAM→DEP and DEP→EXAM work on the
  same location; only a repeat of the *same* operation steps.)
* **step(a)**: 17777700..17777717 → `17777700 + ((a - 17777700 + 1) & 017)` (wraps
  717→700). `a == 17777676` → error: `adrsErr = true`, `consAddr = sr`. Otherwise
  add 2 to the low 16 bits only (wraps within the 64 KB block).
* **loc(a)**: 17777700..17777717 → general register **regardless of the knob**:
  700-705 `R00`..`R05`, 706 `KSP`, 707 `PC`, 710-715 `R10`..`R15`, 716 `SSP`, 717
  `USP`. Knob PROG PHY or CONS PHY → physical `a &^ 1`. The six virtual positions
  → virtual `a & 0177776`, mode K/S/U, I or D (D space = SimH `-t`, not `-d`).
* **CONT**: `effectiveHalt` → single step (`Machine.Step`; **[DEV]** S BUS CYCLE also
  steps one whole instruction). Else `Machine.Continue`. A step, like a halt
  from the panel (below), ends the CPU's WAIT state, as the KB11-C console
  halt ends the WAIT microloop (`DESIGN.md` §6): after a halt in WAIT the
  next step executes the instruction after the WAIT, each step one
  instruction, and CONT after a DEP to PC runs the new code at once (also at
  IPL 7).
* **ENABLE/HALT, LOCK** (level): whenever `effectiveHalt` becomes true (paddle to
  HALT, or LOCK → POWER with the paddle at HALT), and whenever the Machine
  reports `running` while it is true (a power-on or boot already in flight), the
  CPU is halted: behind a long operation (power on, power off, boot, shutdown) at
  once from its own goroutine, otherwise queued ahead of the queued console
  actions; either way only if the CPU runs when it executes (`Machine.Halt`,
  which ends the WAIT state). Becoming false does nothing.
* **START**: `effectiveHalt` → console reset (`Machine.ConsoleReset`; stays halted).
  Else, if `consAddr & 0177777` is 165000 or 173000 (the M9312 boot-ROM entry
  points) → **[DEV]** `Machine.Boot()` (boots the selected system); otherwise
  `Machine.Start(consAddr & 0177777)` (reset + run; mapping is off after reset, so
  the address is a 16-bit VA).
* **After every stop** delivered as EvStop: `consAddr = (consAddr & 017600000) | PC`,
  `lastOp = none`, `dataPaths = R0` (from the next snapshot; **[DEV]** for HALT
  switch and step: the real value is microcode dependent), `lastStop`, `pc`.
  `adrsErr` and `consPA` are cleared whenever the CPU is started (CONT, START,
  boot, step).
* **Key**: `off` pulls the plug **at once** (lamps need power): queued operations
  are dropped, the one in flight is cancelled (e.g. a long power-on or an OS
  shutdown), `Machine.PowerOff` runs from its own goroutine, and later inputs
  queue behind it (the UI confirms first, offering "Shut down the OS first").
  `off → power|lock` → `Machine.PowerOn(Boot: !effectiveHalt)` (`effectiveHalt`
  as it is when the power-on runs). `power ↔ lock` while on: no power change. If
  PowerOn returns `ErrNoSystem`, the key stays where it is and `PowerStatus`
  reports the system, so the UI offers the install/licence dialog
  (`state.needsInstall`).
* **Autostart** (`PDP11_AUTOSTART`, at `Run`): if the persisted key is POWER or
  LOCK, power on exactly as for `off → power`. With autostart off nothing
  happens, also with the key at POWER: the state shows power `on`, run `off`
  until a Boot, a `retryPower`, or turning the key OFF and back.
* **`retryPower`** input (sent by web after an install, selection or factory reset
  of the selected system completes): if key != off, Machine state ∈ {off,
  error} and no power-on or boot is queued → PowerOn as above.
* **`boot`** input (toolbar): refused with a hint if key == lock ("unlock the
  console"), `effectiveHalt` ("set ENABLE/HALT to ENABLE to boot"), an OS
  shutdown is queued or running, or a boot is queued or the machine is starting.
  Key OFF → the key turns to `power` (persisted). Then, when it runs: Machine state
  off/error → PowerOn(Boot: true); halted/running → `Machine.Boot()` (the UI
  confirms first when the CPU is running).
* **Boot timeout**: every boot the panel starts — PowerOn with Boot (key,
  autostart, retryPower, toolbar), `Machine.Boot` (toolbar, START at
  165000/173000) — is watched for the selected system's `bootTimeoutSec`
  (`SystemInfo`; 0 = not watched), counted from the moment the panel sees the CPU
  running after the call (a simulator restart for the boot pauses it). If the
  system has not reached `Ready()` by then, ONE toast goes to everyone, level
  `warn`, with the `req` of the input that booted: "<name> has not reached its
  prompt after N s — check the console. If it hangs, power off and boot again."
  EvReady, any stop, power off (any other state change) and a new boot end the
  watch; a failed boot call drops it.
* **`shutdownOS`** input: refused with a hint if the CPU is not running, the
  system has no `shutdownScript`, or a shutdown is already queued or running.
  Types the script on the console (`DESIGN.md` §7), progress task `shutdown`
  (phases `step`, then `done`, `error` or `cancelled`): each step waits ≤
  `timeoutSec` (default 30 s) for its Expect — a literal, case-sensitive
  substring of the output after the previous Send (the first step also sees the
  text after the last newline of the history) — then **waits 250 ms** (RSX and
  RSTS post their terminal read late) and types Send. The `step` texts are
  readable: `Waiting for “<expect>”`, `Typing “<send>”` (`Pressing Return` for
  a bare line end) and `Waiting for the system to halt…`; line ends are
  dropped, other control characters shown as `^X`, blanks around them trimmed
  (the raw strings go to the debug log). Then:
  * last step has a Send → wait ≤ 30 s for the CPU to stop (the OS halts
    itself, `HALT instruction`); on timeout toast (`warn`, to everyone, with the
    request's `req`) the reason plus "Pull the plug (turn the key to OFF) only if
    you are sure the OS is idle." and **leave the power on**;
  * last step is expect-only (the safe point: Unix V5-V7 after `sync`, RSTS at
    `Option:`) → wait 3 s more;
  * then key = `off` (persisted) and PowerOff.
  An Expect that does not appear in time fails the same way (warn toast, power
  stays on). A stop that the panel did not request, during the script or the
  waits, counts as the OS having halted (→ power off). A HALT from the panel
  (`Front panel HALT`, requested) cancels the shutdown (`info` toast "Shut down OS
  cancelled: the CPU was halted from the panel."), the machine going off or into
  error fails it (`warn`), and key OFF cancels it and pulls the plug. Without a
  script (RT-11, `canShutdown` false) the UI
  offers only "Pull the plug" and "Cancel".
* Selection of another system (web) is allowed only while Machine state ∈ {off,
  error}, key != lock and no panel operation is in flight (`Busy` empty);
  factory reset likewise for the selected system. Behind that check the system
  manager refuses a selection while a simulator runs, and a reset of the
  images a simulator uses (`DESIGN.md` §7).

## 4. Lamp frame

64 linear duty values 0..255, computed every 1/`PDP11_FRAME_HZ`; order in
`PROTOCOL.md`. A frame is delivered when any lamp changed and at least once per
second; its `seq` counts delivered frames.

Precedence: (1) key `off` → all 0 (no power). (2) LAMP TEST raised → all 255,
the knob lamps included, in every Machine state (also off, starting, stopping or
in error: the lamps need only console power). (3) Knob lamps (52-63), in every
Machine state (the switches drive them directly): the selected ADDRESS position
and the selected DATA function 255, others 0. (4) Machine state ∉ {halted,
running} → the CPU section (lamps 0-51) 0, e.g. for about a second during every
Boot restart. (5) §4.1 if Machine state is `running`, §4.2 if `halted`
(Machine.State() is authoritative; the snapshot's state only distinguishes
RUNNING from WAIT). Every power cycle starts a new baseline (no ADRS ERR flash
from the previous run's trap counter, no repeated old frame, no DISPLAY
REGISTER difference across two exports).

### 4.1 Running

Consume the new samples (`PanelSource.ReadFrame`, the newest ≤ 8192 per frame).
For each sample with weight `w` (the wall µs it stands for, `SIMH_PATCH.md` §3)
accumulate `w` into every lamp whose bit is set; intensity = `255 · Σw·bit / Σw`
(weights, never sample counts: an idle CPU has few, heavy WAIT samples). Per
sample: `pa = addr & PnlPAMask`, `va`, `mode = (addr >> 25) & 3` (the mode of
the bus cycle), `wait = addr & PnlWait`.

* **ADDRESS**: PROG PHY → `pa`. CONS PHY → `va | (consAddr & 017600000)`. Virtual
  positions → `va` (bits 21:16 dark).
* **DATA**: DATA PATHS → `data` (WAIT samples carry R0: the RSX-11M-PLUS, 2.11BSD,
  IAS and RSTS/E idle patterns). BUS REG → `data`, but WAIT samples use the
  snapshot's `LastData`. µADRS → **[DEV]** WAIT 0x3bf; else
  `{0x3bf,0x32a,0x3bd,0x32c,0x3f5}[(pc ^ ir) % 5]`. DISPLAY REGISTER →
  time-weighted from the snapshot's accumulators: bit i = `255 · ΔDROn[i] /
  ΔWeightSum`, the differences (modulo 2^32) since the previous frame's snapshot
  of the export's sums of the sample weights with DR bit i set and of all
  sample weights, so a DR that changes faster than the frame rate glows in every
  visited bit instead of flashing one; in a frame without samples (ΔWeightSum =
  0) the snapshot's `DR`.
* **USER/SUPER/KERNEL**: mode 3/1/0 (2 lights none). **DATA**: `PnlDSpace`.
* **ADDRESSING**: `!PnlMMU` → 16; `PnlMMU && !PnlM22` → 18; both → 22.
* **PARITY HIGH/LOW**: non-WAIT reads (`!PnlWR`) of memory (`!PnlIOPage`): lamp on
  when that byte of `data` has an even number of 1 bits (a byte read lights only
  its own byte's lamp); writes and I/O-page cycles → off; WAIT samples repeat the
  previous non-WAIT sample's parity bits.
* **RUN**: WAIT samples 1.0, others 0.92. **MASTER**: non-WAIT `PnlIOPage` samples
  → 1. **PAUSE**: 0.5 × MASTER's per-sample value.
* **ADRS ERR**: 255 in a frame where `TrapCount` changed since the previous frame,
  else 0 (the browser's decay makes the flash visible).
* **PAR ERR**: 0 **[DEV]**.
* **A frame without new samples** repeats the previous frame's intensities
  (except DISPLAY REGISTER, which follows the snapshot).

### 4.2 Halted

* **ADDRESS**: `consAddr` in 17777700..17777717 → forced pattern: bits 21:06 on,
  05:04 off, 03:00 = `consAddr & 017` (any knob). Else virtual positions →
  `consAddr & 0177777`; CONS PHY → `consAddr`; PROG PHY → `consPA` if set, else
  snapshot `LastPA & PnlPAMask`.
* **DATA**: DATA PATHS → `dataPaths`; BUS REG → `sr & 0177777` (live); µADRS → 0170
  (CON.00); DISPLAY REGISTER → snapshot `DR`.
* RUN 0, PAUSE 0, MASTER 255, mode lamps from snapshot PSW<15:14>, DATA 0,
  ADDRESSING from snapshot MMR0/MMR3, PARITY 0, PAR ERR 0, ADRS ERR = `adrsErr`.

## 5. Client-side rendering (informative; `UI.md`)

The browser smooths per lamp with real `dt`: `b += (target − b)·(1 − e^(−dt/τ))`,
τ = τ_rise if rising else τ_fall. Presets: **LED** (default, 15/15 ms),
**PiDP-11** (50/50 ms), **incandescent** (21.5/68 ms), **reduced motion** (150 ms;
default with `prefers-reduced-motion`). Displayed opacity = `b^(1/2.2)`, and 0
while the linear (smoothed) brightness `b` < 1/64.

## 6. Panel API (Go, used by `internal/web` and `cmd/pdp11d`)

* `New(machine, source, systems, Options{StatePath, FrameHz, Autostart,
  Logger})` loads `panel.json`; `Run(ctx)` runs the frame loop, the worker and
  persistence and flushes `panel.json` when it returns.
* `Input(api.PanelInput) string` never blocks: "" = accepted, else the reason it
  was ignored (web toasts it to the sender, `warn`). `ReleaseClient(id)`.
* `Controls()`, `Console()` (`api.ConsoleInfo`), `PowerStatus()` →
  `{Failed, NeedsInstall}`: the last power-on attempt failed while the key is on
  (feeds `state.error`), or was refused with `ErrNoSystem` for that system
  (feeds `state.needsInstall`); reset by key OFF and by a successful power-on.
* Subscriptions (callbacks must not block): `SubscribeFrames(fn(frame, seq))`;
  `SubscribeChanges(fn)` (Controls, Console or PowerStatus changed; coalesced);
  `Toasts(fn(level, text, req))` (for everyone: shutdown results, boot timeout);
  `SubscribeHints(fn(client, req, text))` (a queued input judged ignored when it
  ran; for that client only, `warn`, with its `req`);
  `SubscribeProgress(fn(api.Progress))` (task `shutdown`; for everyone).
* `ShutdownOS(ctx)`: for `PDP11_SHUTDOWN_ON_STOP` (`DESIGN.md` §6). Runs the
  shutdown script and waits for the halt (or the 3 s settle) as above, but
  neither powers off nor changes the key; waits for the panel operation in
  flight (`Busy` = `shutdown` meanwhile); works before and after `Run`
  returned; returns nil at once when the CPU is not running.
