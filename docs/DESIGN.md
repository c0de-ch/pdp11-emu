# PDP-11/70 web appliance: design

This file is the **contract** between components. If an implementation needs to
change a contract, change this document (and `PROTOCOL.md`, `PANEL_LOGIC.md`,
`SIMH_PATCH.md`, `UI.md`, `server/internal/api`, `server/internal/catalog`) in
the same change. Background research: `docs/research/`.

## 1. Goal

A self-contained appliance that runs a PDP-11/70 (Open SIMH) inside Docker, an
LXC container or a VM, and serves a web UI with:

* a **console terminal** (the DL11 console, xterm.js);
* a **high-fidelity, operable replica of the PDP-11/70 front panel**: 64 lamps
  with realistic duty-cycle brightness, the 22 switch register paddles, LAMP
  TEST, LOAD ADRS / EXAM / DEP / CONT / ENABLE-HALT / S INST-S BUS CYCLE /
  START, both rotary knobs and the OFF/POWER/LOCK key;
* panel and console can be popped out into **separate browser windows**, and
  any number of viewers share the one machine;
* a menu of operating systems (2.11BSD pl482 by default, 2.11BSD pl499, Unix
  V7/V6/V5, RT-11, RSX-11M-PLUS V3.0, RSTS/E V7), downloaded from their
  original sources;
* `docker compose up` on an empty volume ends with 2.11BSD at `login:` and a
  live panel, with no clicks.

Out of scope for v1 (reserved in the protocol): extra terminal windows on
DZ/DC lines (an optional SimH telnet listener exists instead, §8), guest
networking, RSX-11M-PLUS V4.6, console telnet access.

## 2. Architecture

```
 browser(s) ──HTTP + WebSocket──►  pdp11d  (Go, single static binary)
                                     │  stdin/stdout pipes ...... SCP command channel
                                     │  TCP 127.0.0.1:BASE ...... DL11 console, raw (NOTELNET)
                                     │  mmap $PDP11_SHM ......... panel export (SIMH_PATCH.md)
                                     ▼
                                  pdp11  (Open SIMH, pinned commit + our patches)
                                     └─ optional PDP11_TTY_TELNET listener (DZ/DC lines, telnet)
```

Why (research r1 and the design review, all verified by experiment):

* SimH remote-console *master mode* and the *frontpanel API* are not used: at
  the pinned commit they have a spurious-stop bug, orphan the simulator when the
  controller restarts, take ~50 ms per command and cannot DEPOSIT while running.
* SCP over **stdin/stdout pipes** is fast (0.05-0.2 ms per command, including the
  next prompt) and has the full command set. SimH reads stdin only while halted.
* With a piped stdin SimH never polls stdin for console input, so the console is
  on TCP: `SET CONSOLE -U TELNET=127.0.0.1:p;NOTELNET` + `TELNET=BUFFERED=65536`
  (no IAC bytes, no banner, no replay on reconnect; SCP output never appears
  there; `^E` typed there goes to the guest).
* Unpatched SimH can only sample registers and its sampler defeats idling. Our
  patch exports wall-clock-weighted bus samples, registers, run/WAIT state, a live
  switch register and a race-free halt handshake through a shared-memory file
  (measured cost ≤ 2.9 % of emulated speed; halt latency ≈ 0.1 ms busy and one
  `sim_idle` sleep, ≈ 10 ms, idle: `SIMH_PATCH.md` §7).

## 3. Repository layout

```
go.mod, embed.go  Go module "pdp11emu" (go 1.23) at the repo root; embed.go embeds
                  web/index.html + web/assets and systems/ into the binary
docs/             DESIGN.md, PROTOCOL.md, PANEL_LOGIC.md, SIMH_PATCH.md, UI.md (contract),
                  PROXMOX.md (deployment notes), research/
simh/             SIMH_COMMIT, patches/*.patch, panel/ (pdp11_panel.[ch], pdp11_panel_shm.h),
                  build.sh, mkpatches.sh, test/, README.md; src/ and out/ are build outputs
server/           Go code: cmd/pdp11d, cmd/catalogcheck, internal/{api,atomicfile,catalog,
                  deps,shm,simh,systems,panel,web}
systems/          one directory per OS: system.json, boot.ini, dialog.ini, LICENSE.txt; README.md
web/              static UI, no build step: index.html + assets/{js,css,fonts,vendor,img,panel}/
                  web/dev/ = development tools (mock server, UI tests, generators), not embedded
tests/            systems/ (boot matrix), e2e/ (browser tests against the real stack)
Dockerfile, .dockerignore, compose.yaml, .env.example, install.sh,
deploy/ (pdp11d.service, pdp11d.env, pdp11d-run, collect-licenses.sh, test_pdp11d_run.py),
Makefile          deployment (§13); bin/ and dist/ are build outputs
README.md, LICENSE, THIRD_PARTY.md                          user documentation, licences (§12)
```

## 4. Ownership

| Area | Owner | Purpose |
|---|---|---|
| `server/internal/api`, `internal/catalog`, `internal/deps`, `docs/` contract files | contract | shared types, catalog schema + validator, pinned modules, these documents |
| `simh/` | simh-patch | SimH patches, build script, patch tests |
| `internal/shm`, `internal/simh`, `internal/systems`, `internal/atomicfile`, `cmd/pdp11d` | server-core | panel export reader, SimH process + SCP + console stream (`api.Machine`), install/run.ini (`api.Systems`), atomic replacement of state files, CLI and wiring |
| `internal/panel`, `internal/web` | server-panel | console logic, lamp engine, power policy; HTTP, WebSocket hub, REST |
| `web/` | web-ui | browser UI (`UI.md`) |
| `systems/`, `tests/systems/` | systems | catalog entries, boot-matrix harness |
| `tests/e2e/` | integration | browser end-to-end tests against the real pdp11d + SimH |
| `Dockerfile`, `compose.yaml`, `install.sh`, `deploy/`, `Makefile`, `README.md`, `THIRD_PARTY.md`, `LICENSE`, `docs/PROXMOX.md` | deploy | §12, §13 |

`go build ./...` and `go vet ./...` must pass; `gofmt` clean. Third-party Go
modules: `github.com/coder/websocket`, `github.com/ulikunitz/xz` only. The only
external runtime tool is `gzip` (`tarZ` archives via `gzip -dc`); HTTP(S) and FTP
downloads are built in (§7).

Exported beyond `internal/api` (additive, kept): `panel.(*Panel).PowerStatus`,
`SubscribeHints`, `SubscribeProgress`, `ShutdownOS(ctx)` (`PANEL_LOGIC.md` §6);
`web.(*Server).Close`, `Serve` (on a listener the caller bound);
`simh.RunHeader`, `SpeedCommands`, `TerminalsAttach`, `CheckPath` (the single
definition of the generated run.ini parts), `RunConfig.Release` and
`(*Machine).Close`, `Log`, `PanelSource`; `systems.(*Manager).Close`,
`SystemDir`, `Fetch`, `ErrLicense`; `atomicfile.WriteFile`;
`shm.(*Reader).Snapshot`, `HaltPending`, `Path`, `Dropped` (samples skipped by
a slow consumer; a gap also shows in `RingWr`), `shm.ErrFormat`.
`api.LogBuffer.SubscribeHistory` and the `api.SystemInfo` fields `noLogin` and
`bootTimeoutSec` are part of the contract types.

## 5. Configuration (env vars; flags of the same name in lower-case-dash form)

| Env | Default | Meaning |
|---|---|---|
| `PDP11_LISTEN` | `:8080` | HTTP listen address |
| `PDP11_DATA` | `/data` in a container (`/.dockerenv` or `/run/.containerenv` exists), else `/var/lib/pdp11` | data directory |
| `PDP11_SIMH` | `/opt/pdp11/bin/pdp11` | patched simulator binary |
| `PDP11_CACHE` | `/opt/pdp11/cache` | read-only archive caches searched before downloading; several directories separated by `:` (`fetch` writes into the first) |
| `PDP11_SYSTEMS` | (embedded) | catalog directory that **replaces** the embedded catalog (validated at start) |
| `PDP11_WEB` | (embedded) | serve the UI from this directory (development; read on every request) |
| `PDP11_PASSWORD` | empty | HTTP Basic auth (any user name) for UI, API, WebSocket; session cookie (§10) |
| `PDP11_ALLOW_ORIGINS` | empty | comma list of extra allowed WebSocket `Origin` patterns (§10) |
| `PDP11_HOSTS` | empty | comma list of extra host names (without ports) the UI may be opened by while no password is set (host check, §10); `*.example.com` = every subdomain of example.com, not example.com itself; `*` = no host check |
| `PDP11_FRAME_ANCESTORS` | `'self'` | CSP `frame-ancestors` source list: the pages that may show the UI in a frame (§10), e.g. `'self' https://dashboard.example.com`; `'none'` = none |
| `PDP11_TRUSTED_PROXIES` | empty | comma list of IP addresses or CIDRs of reverse proxies whose `X-Forwarded-For` names the client (failed-login limit, log lines; §10) |
| `PDP11_HEALTH_PUBLIC` | `false` | serve `GET`/`HEAD /api/status` without auth (health checks) |
| `PDP11_SYSTEM` | catalog default | system selected on the **first start only** (then persisted in `systems.json`; later values are ignored) |
| `PDP11_ACCEPT_LICENSES` | empty | comma list of system ids whose licences are accepted for unattended install |
| `PDP11_AUTOSTART` | `true` | at startup power on if the persisted key is POWER/LOCK (first start: POWER); `false`: no power-on, the key is left as it is (`PANEL_LOGIC.md` §3) |
| `PDP11_SHUTDOWN_ON_STOP` | `false` (the Docker and systemd files set `1`, §13) | on SIGTERM run the OS shutdown script before powering off (§6) |
| `PDP11_TTY_TELNET` | empty | `[host]:port` or a port, e.g. `:4000`: SimH telnet listener for the system's extra terminal lines (unauthenticated, §8) |
| `PDP11_PORT_BASE` | `23400` | localhost console port |
| `PDP11_SHM` | `/dev/shm/pdp11d-panel` (systemd unit: `/run/pdp11/panel`) | panel export file: a regular file of this user with mode 0600 on a POSIX file system (tmpfs); `ATTACH PANEL` and `shm.Open` refuse anything else |
| `PDP11_FRAME_HZ` | `60` | lamp frame rate (30-120) |
| `PDP11_LOG` | `info` | log level: `debug`, `info`, `warn`, `error` (slog text on stderr) |

Booleans accept `1/true/yes/on` and `0/false/no/off`/empty. Every sub-command
accepts all flags; flags override the environment. An invalid
`PDP11_ALLOW_ORIGINS`, `PDP11_HOSTS`, `PDP11_FRAME_ANCESTORS` or
`PDP11_TRUSTED_PROXIES` value stops `serve` before the machine starts (exit
status 1, with a message naming the setting). Paths SimH receives as
command arguments must not contain blanks, quotes or `;`: such a `PDP11_SHM`,
`PDP11_DATA` (the disk images live below it) or `PDP11_TTY_TELNET` is rejected
at start, and `PrepareRun` checks every image path again. The simulator
inherits pdp11d's environment without any `PDP11_*` variable (the password
never reaches it), with `HOME` = `$DATA/run`; it runs as pdp11d's user, which
must be able to open the export file the simulator creates with mode 0600.

Data directory:

```
$DATA/.pdp11d.lock      locked by the pdp11d that uses the directory (serve, install); its pid
$DATA/panel.json        persisted panel controls (internal/panel; a damaged file is kept as panel.json.bad)
$DATA/systems.json      selected system + per-system speed overrides (internal/systems)
$DATA/cache/<sha256>    downloaded archives (verified); <sha256>.part while downloading
$DATA/import/           drop archives here manually: files matching an artifact's
                        size+sha256 are moved to the cache
$DATA/systems/<id>/     working disk images (at their archive member paths) + .installed
                        (JSON: id, time, artifact and file sha256s); <path>.part while installing
$DATA/run/              SimH cwd and HOME: run.ini, dialog.do, pdp11.pid, SimH temp files
```

pdp11d creates the directories with mode 0750 and the disk images 0640 (both
less what the umask removes), the state files (`panel.json`, `systems.json`,
`.installed`, `run.ini`, `dialog.do`, `pdp11.pid`; replaced atomically) exactly
0640, the lock file 0600 and the archives 0644 (a download less what the umask
removes; archive caches may be shared).

Sub-commands (exit status 2 for usage and configuration errors):

* `serve` (default): run the appliance.
* `fetch [--cache DIR] [--accept-license] [id...]`: download and verify
  archives into `DIR` (default: the first `PDP11_CACHE` entry) without
  extracting; an archive that another `PDP11_CACHE` directory, `$DATA/cache`
  or `$DATA/import` holds is copied instead; no ids = every redistributable
  system; other ids need `--accept-license` or `PDP11_ACCEPT_LICENSES`. Used
  at image build.
* `install [--accept-license] id...`: install into `$DATA` (prints the licence
  text when it must be accepted first).
* `check [dir]`: validate a catalog (default `PDP11_SYSTEMS` or the embedded
  one) and list it; `go run ./server/cmd/catalogcheck [dir]` does the same.
* `health [--url URL] [--timeout D]`: `GET api/status` on the `PDP11_LISTEN`
  address (an unspecified host means loopback; default timeout 5 s), with
  `PDP11_PASSWORD` as Basic auth; exit 0 iff the answer is `{"ok": true}`, else 1.
  The container `HEALTHCHECK` (no curl needed).
* `version`, `help`.

One data directory serves one instance. `serve` and `install` lock it
(`flock` of `$DATA/.pdp11d.lock`, exclusive, not waiting; this also works
between containers that share a volume) before they write anything there,
and keep the lock until they exit; the descriptor is close-on-exec, so the
simulator never holds it. If another pdp11d holds the lock they exit with
status 2 and `pdp11d: another pdp11d (pid <N>) is using the data directory
<dir>; one data directory serves one instance` (`<N>` as the holder recorded
it, `unknown` if it cannot be read). `fetch`, `check`, `health` and
`version` do not lock. `serve` then binds its listener, before the panel
starts: a busy or forbidden port ends it with status 1 before anything is
powered on.

## 6. Simulator lifecycle (`internal/simh`)

**Start (power on).** `PowerOn(opts)`:

1. If the selected system is not installed: if its class is `redistributable`
   or it is in `PDP11_ACCEPT_LICENSES`, install it now (state `starting`,
   progress via `Systems.SubscribeProgress`; an install of it that another
   client started is waited for); else return `ErrNoSystem` (no state change,
   `LastError` explains; the UI offers the licence dialog).
2. `PrepareRun` writes `$DATA/run/run.ini` = generated header + the system's
   `boot.ini` with `${DISKn}` replaced by absolute paths of the working copies
   (all of which must exist; "a factory reset restores it"). It fails while the
   system is being installed or reset (`ErrBusy`), and leases the system's
   images until `RunConfig.Release`, which the Machine calls once the
   simulator's process has exited or could not be started (§7). Header
   (`simh.RunHeader`; 9 lines for fast, 8 for authentic;
   `RunConfig.HeaderLines` tells §6 step 4 where boot.ini starts):
   ```
   ; generated by pdp11d - do not edit
   set cpu 11/70
   set cpu ${MEMORY}
   ${SPEED}                      ; simh.SpeedCommands (§9): fast "set nothrottle" + "set cpu idle"; authentic "set throttle 1M"
   set console -u telnet=127.0.0.1:${CONSOLE_PORT};notelnet
   set console telnet=buffered=65536
   set tto 7b
   attach panel ${PANEL_SHM}
   ```
   If the system has `terminals` and `PDP11_TTY_TELNET` is set, a line
   `attach -u [-m] <device> <PDP11_TTY_TELNET>` (`simh.TerminalsAttach`; `-m`
   only for `dz` with `terminals.modem`) is appended after boot.ini.
   `server/internal/systems/testdata/*.ini` are the golden files.
3. Refuse to start if `$DATA/run/simh.ini` exists. Kill a stale simulator
   recorded in `$DATA/run/pdp11.pid` (only if `/proc/<pid>/exe` is the
   configured binary; `serve` holds the data directory lock, §5, so it is
   an orphan of a pdp11d that died). Spawn `pdp11 run.ini` with cwd and
   `HOME` = `$DATA/run`, stdin/stdout pipes (stderr merged),
   `SysProcAttr{Setpgid: true, Pdeathsig: SIGTERM}` from a goroutine that
   holds `runtime.LockOSThread()` for the child's lifetime. Write the pid
   file.
4. **Start-up diagnostics.** Read stdout until the first `sim> ` (≤ 15 s; an
   exit or a timeout is a power-on error quoting the last output). SimH echoes a
   command of the file that printed something as `<abs path>/run.ini-<n>>
   <cmd>`; every following line up to the next echo or the prompt is a
   diagnostic of line n. Not diagnostics: blank lines; output before the first
   echo (banner, CPU notices); informational lines — `%SIM-INFO…`, `Resetting
   all devices...`, `The GO and CONTINUE commands do not reset devices.`,
   `Modem control activated`/`deactivated` (printed by `attach -m dz`),
   `Idling disabled`, `Throttling disabled`, lines starting with `Disabling `,
   and `Sockets: …` (SimH prints it before the `%SIM-ERROR` of a failed listener,
   which still counts); the output of boot.ini `echo` commands (SimH prints
   ECHO output without an echo line of its own; it is recognised by matching
   the text of the file's later `echo` lines, `%VAR%` as wildcards). Any other
   diagnostic is a **power-on error** (state `error`, no boot); `LastError`
   names its origin: `run.ini line n` (header), `boot.ini line n`, or the
   `PDP11_TTY_TELNET` listener.
5. Map the shm (`shm.Open`: magic, version 3, size; else `ErrFormat`; a
   symbolic link, a non-regular file, another user's file or one open to
   group/others is refused too) → a bad export is a power-on error (the
   appliance never runs without the panel export; there is no SIGINT
   fallback). Write the remembered switch register (sr, sr_valid), cancel
   any halt request, connect the console client (retry every 200 ms for
   ≤ 3 s; SimH accepts once per second).
6. State `halted`; a speed requested while starting is applied now. If
   `opts.Boot`: run the **boot sequence** (below) → `running`.

**Boot sequence** (only in a simulator that has executed nothing since it
started — SEND timing is relative to sim time, which BOOT resets, and one-shot
EXPECT rules are consumed): write `$DATA/run/dialog.do` containing `noexpect`,
`nosend`, then the system's dialog.ini lines; send `do dialog.do` (SimH
substitutes `%DATE_…%` etc. at this moment; any diagnostic in its reply fails
the boot: `EvError`, the CPU stays halted), arm the `readyPattern` matcher,
then cancel any halt request (below) and send the boot command.
`Machine.Boot()` on a simulator that has already executed (any
boot/run/go/cont/step) restarts the process (state `starting`, ~1 s; a running
CPU is first halted silently) and then runs the boot sequence. `Start`,
`Continue` and `Step` never arm the dialogue.

**stdout framing.** stdout is a byte stream; lines end in `\n` or `\r\n`. The
prompt is the 5 bytes `sim> ` without newline, recognised at the start of a
line (also when output follows it, `sim> Goodbye`). After a run command
(`boot`, `cont`, `run`, `go`, `step`) the CPU is halted **exactly when** the
stream tail ends with `sim> `. The stop reason is the last line since the run
command that matches `^(?P<why>.*), PC: (?P<pc>[0-7]+)(?: \((?P<insn>.*)\))?$`
(greedy, end anchored; e.g. `HALT instruction`, `Front panel HALT`, `Step
expired`, `Simulation stopped`, `Breakpoint`, `Unit not attached`, trap names);
if none, the reason is `""` (EXPECT stop; the PC comes from the register block)
and any non-informational lines (step 4) are reported as an error. Never write
to stdin while running (SimH would execute it after the next stop). Output
produced while running may arrive late (not flushed).

**SCP replies.** `examine X` succeeds iff the reply before the prompt
(informational lines ignored) matches `^([0-7]+|[A-Z0-9]+):\t([0-7]{6})$`;
`deposit` succeeds iff the reply is empty. `Address space exceeded`,
`Relocation error`, `Invalid argument` → `ErrAddress` (wrapped with the text);
anything else → generic error. A command not answered within 10 s declares the
simulator failed. After every Deposit and ConsoleReset send `set panel refresh`
so the register block (DR, PSW, MMR0/3) is current while halted. `Translate`
of a virtual location (halted only) reads MMR0, MMR3 and the relevant PAR via
`examine` and computes the PA (16/18/22-bit mapping; I/O page at 160000-177777
→ 17760000+); physical locations translate to themselves in any state;
registers have no PA (error).

**Halt handshake** (shm, see `pdp11_panel_shm.h`; every bridge word is written
with a single atomic 32-bit store): to halt while running store `halt_req =
halt_ack + 1`; SimH stops with `Front panel HALT` at the next sample, before
the next instruction (even if it only just entered `sim_instr`). After any
other stop, and before writing any run command, store `halt_req = halt_ack`
(cancel). If no stop arrives within 2 s → state `error` ("simulator not
responding") and the simulator is torn down; a Step that has not stopped after
2 s is halted the same way. **SIGINT is never sent** (with a piped stdin a
SIGINT at the prompt makes SimH exit).

**Console stops end WAIT.** After the stop of a `Halt` (the panel's
ENABLE/HALT, LOCK → POWER, the HALT level stopping a boot) and after every
`Step`, pdp11d sends `deposit WAIT 0` before it reports the stop, as the
KB11-C's console halt ends the WAIT microloop: CONT resumes at the
instruction after the WAIT and a step executes it. (SimH would go back into
the WAIT: a step could never get past it, and CONT after a deposit to PC
would wait for an interrupt before running the new code, at IPL 7 for
ever.) Transparent stops (below, and the halt of a Boot restart) and power
off leave the guest's WAIT alone.

**Events.** Machine events are delivered in order on one goroutine, never under
internal locks (see `api.Machine`). `EvState` on every `State()` change except the
blips of Step and transparent stops; `EvStop` for every non-transparent stop
(also logged at info: reason, PC, instruction), before the resulting
`EvState(halted)`; `EvError` for user-visible errors;
`EvReady` when the system's `readyPattern` appears on the console after a boot.
Sequences:

* PowerOn: `starting` (installing too), `halted`; with Boot `running`, later
  `EvReady`. A failure of PowerOn itself (install, run.ini, spawn, start-up
  diagnostics, export, console) only sets `LastError` and `EvState(error)`
  (`off` when PowerOff cancelled it); no `EvError`.
* Boot of a fresh simulator: `running`, `EvReady`. Boot restart: `starting`,
  a silent maintenance halt (no `EvStop`), `halted`, `running`, `EvReady`.
* Natural stop (HALT instruction, breakpoint, trap, EXPECT without continue):
  `EvStop{Reason, PC, Insn}`, `halted`; without a stop line `EvStop{Reason:
  ""}` plus `EvError` with the output. Halt: `EvStop{"Front panel HALT",
  Requested: true}`, `halted`. Step: only `EvStop{"Step expired"}`.
* PowerOff of a running CPU: `stopping`, `EvStop{"Front panel HALT",
  Requested: true}`, `off` (no `halted`); of a halted one: `stopping`, `off`.
* Asynchronous errors — an unresponsive or exited simulator (`EvError` +
  `error`), a failed boot command or dialogue (`EvError`, stays halted).

**Busy states.** While `starting` or `stopping`: PowerOn, Boot, Halt and the
halted-only operations (Continue, Step, Start, ConsoleReset, Examine, Deposit,
virtual Translate) fail fast with `ErrBusy` instead of waiting behind a long
install; SetSpeed while `starting` is recorded and applied once the simulator
is up (returns nil), while `stopping` → `ErrBusy`. Halted-only operations while
running → `ErrNotHalted`. While off or in error every operation except
PowerOn, PowerOff, SetSwitchRegister (remembered for the next power-on) and
the Translate of a physical location → `ErrNotPowered`. Halt of a halted CPU
is a no-op.
`Speed()` without a simulator is `Systems.SpeedFor(Selected())`.

**Transparent stops.** `SetSpeed` while running: halt, `SpeedCommands`,
cancel, `cont`; no events except on failure or when a real stop raced the
request (then a real EvStop/EvState and the CPU stays halted).

**Power off** (`PowerOff`): cancels a PowerOn or Boot restart in progress; if
running, halt; `quit`; wait 3 s; close stdin, SIGTERM; wait 2 s; SIGKILL (the
process group). SimH flushes disk buffers at every stop, so a halted simulator
loses no writes. State `off`.

**Crash safety.** If pdp11d dies, Pdeathsig delivers SIGTERM: a running SimH
stops, reads stdin EOF and exits; a halted one exits immediately. The pid file
covers the rest.

**Console stream.** One TCP connection per power cycle; if SimH drops it
mid-session it is re-established (1 s delay; with NOTELNET nothing is replayed
twice). Output goes to a 256 KiB history ring (kept across power cycles) and
subscribers; `SubscribeHistory` is atomic. Input: `Write` returns
`ErrNotPowered` while not connected and `ErrInputFull` beyond 64 KiB queued;
the first 32 bytes of a burst are written at once, then paced at `InputCPS`
(default 100 cps; token bucket); input is **held in the queue while the CPU is
not running** (SimH reads the console only while executing; visible as
`Queued()`/`inputQueued`); `CancelInput` and power off drop the queue. SimH
quirk (verified): the last console character a run outputs is sent only when
the CPU runs again, and START (SimH `run`, which resets devices) drops it.
`readyPattern` detection scans the output after each boot (literal,
case-sensitive substring across reads, NUL and DEL fill ignored) → `Ready()`
and `EvReady`; reset by a boot and by power off.

**Log buffer** (`Machine.Log()`, the only source of the `log` channel): the
last 2000 simulator stdout lines plus every SCP command pdp11d sends, as
`sim> <cmd>` (invalid UTF-8 replaced). `SubscribeHistory(n, fn)` returns the
last n lines and registers fn for every later line atomically (lines are
neither lost nor duplicated between the two); `Lines(n)`, `Subscribe(fn)`.

**Stats.** `MIPS` = guest instructions executed per second (`instr_count`
deltas over ≥ 1 s windows / 10^6). WAIT iterations are not instructions, so an
idle OS shows a small figure (≈ 0.01-0.02 at fast speed); it is not a measure
of host speed. 0 unless running. `SamplesPerSec` = measured export rate,
`UptimeSec` = since the simulator started (0 when off).

**SIGTERM/SIGINT to pdp11d.** Stop the web server (≤ 10 s; clients get close
1001), stop the panel (its `Run` returns and flushes `panel.json`; controls are
not changed, so the next start autostarts again); if `PDP11_SHUTDOWN_ON_STOP`
and the CPU runs: `panel.ShutdownOS` (≤ 45 s: the shutdown script and the wait
for the halt, `PANEL_LOGIC.md` §3; no key change); `PowerOff` (≤ 20 s); exit.
The worst case is about 75 s, so service managers must allow ≥ 75 s (§13). A
second signal exits at once (Pdeathsig then stops SimH, which flushes its disks
at the stop).

## 7. Systems and the catalog

`systems/<id>/system.json` (`catalog.System` is authoritative; `catalogcheck`
validates), `boot.ini` (device setup only: `set <dev> …`, `attach -e <unit>
${DISKn}`, `deposit`, `echo`, comments — the header owns CPU, memory, speed,
console, panel, prompt; no networking; no terminal attach) and optional
`dialog.ini` (only `expect`/`send` lines; every `expect` action ends in
`; continue`; SimH `%DATE_…%` variables allowed; RSX/RSTS answers use
`send -t after=250000,"…"`). Conventions and per-system notes:
`systems/README.md`.

Fields beyond the obvious: `license.class` (`redistributable` — Unix V5-V7, 2.11BSD;
`hobbyist` — Mentec hobbyist licence, user must accept the text before install),
`credentials` or `noLogin` (the console needs no login: RT-11, RSX-11M-PLUS whose
console comes up logged in; `SystemInfo.noLogin`, `UI.md` §2), `shutdown` (text),
`shutdownScript` (expect/send steps typed on the console by "Shut down OS";
`SystemInfo.canShutdown` = a script exists), `notes` (markdown with `## Log in`,
`## Things to try`, `## Shut down`), `console` key mapping (`backspace`:
`del`|`bs`|`#`, `interrupt`: `ctrl-c`|`del`, `keyHelp`), `readyPattern`,
`bootTimeoutSec` (`SystemInfo.bootTimeoutSec`, default 120; only a warning
toast, `PANEL_LOGIC.md` §3), `terminals` (`device`, `lines`, `modem`, `enableHint`),
`artifacts` (`url`, `mirrors`, `sha256`, `size`, `format`, `files[path, role,
sha256, size, sparse]`), `tests.session` (boot matrix).

**Matching.** `readyPattern` and every `expect` (shutdown script, session
steps) are **literal, case-sensitive substrings** of the console output, like
SimH `EXPECT` without `-r` (the RSX and RSTS prompts contain `( ) [ ] ? < >`).
A step's `expect` only sees output produced after the previous step's `send`;
the first step also sees the text after the last newline of the history (a
prompt already on the screen counts).

**Shutdown scripts.** Each step waits ≤ `timeoutSec` (default 30) for its
`expect`, pauses 250 ms after the match (RSX and RSTS post their terminal read
late), then types `send`. The last step says how the shutdown ends: with a
`send` the OS halts itself (2.11BSD `shutdown -h now`, RSX `RUN $SHUTUP`; the
CPU stops with `HALT instruction`, waited for ≤ 30 s); **expect-only** marks the
safe point (Unix V5-V7 after the third `sync`, RSTS at INIT's `Option:`), after
which pdp11d waits 3 s and powers off. Unix scripts type `root` first (logs in
at `login:`, harmless in a shell). RT-11 has no script (`canShutdown` false:
the UI offers only "Pull the plug").

**Install**: for each artifact look in `$DATA/cache`, each `PDP11_CACHE`
directory (files named by sha256), `$DATA/import` (any regular, non-hidden file
whose size+sha256 match → moved into the cache), else download url then
mirrors. Downloads are built in: HTTP(S) with `net/http` (Range resume, no
transparent decompression) and FTP with an internal client
(`server/internal/systems/ftp.go`: anonymous login unless the URL names a user,
`TYPE I`, `EPSV` then `PASV` fallback — the data connection always goes to the
control connection's host —, `REST` resume, multi-line replies of at most 64
KiB and 1000 lines, at most 10 `120` replies before the greeting, progress);
per URL 3 attempts with backoff (2 s, 8 s), 20 s connect timeout, 60 s stall
timeout (every reply as a whole and every read), into `<sha256>.part` with
resume; a complete but wrong file is deleted. HTTP 4xx other than 408/429 and
FTP 5xx other than 530 are permanent for that URL (next mirror at once). Verify
size + sha256. An archive that has to be copied (an import from another file
system, `fetch` from another cache) goes through a temporary name, not
`.part`, and is renamed into place only after its size and sha256 were
checked; an import is removed only then. Check free space before changing
anything (files at their size, `sparse` files at 1/4: all new files exist next
to the copies they replace; ENOSPC still fails with a clear error). Then two
phases. First extract the listed files (formats `raw`, `gz`, `xz`, `zip`,
`tar`, `tgz`, `tarZ` via `gzip -dc`; archive member names compared after
`path.Clean` on both sides, so `./name` = `name`; regular files only) to
`$DATA/systems/<id>/<member path>.part` and verify each sha256; only files
with `sparse: true` are written with holes (all-zero 4 KiB blocks skipped), the
others fully allocated (no fragmentation, no ENOSPC at run time). A cancel or
an error in this phase deletes the `.part` files and leaves the old images and
`.installed` as they were. Then, not cancellable: remove `.installed`, rename
every file into place, write `.installed` (a crash in between leaves the system
without `.installed`, so that it is installed again). Errors name the file,
size, sha256 and every URL tried (and the import directory). Installs are
serialized (others wait, "Waiting for another installation to finish"; a
second install of the same id → `ErrBusy`); installing an installed system
reports `done` at once; progress is published to all subscribers; at the end
the memory the extraction used (up to ~200 MB for the xz kits) is returned to
the operating system. While a simulator uses a system's images (from
`PrepareRun` until its process has exited, §6) an install or factory reset of
that system and any `Select` fail with `ErrBusy`, and while an install or reset
of the selected system runs `PrepareRun` does: in one step under one lock, so a
power-on that started after a client checked "the machine is off" can never run
on images that are being replaced.
**Factory reset** re-extracts the working copies (downloading again if the cache
lost the archive).

**Selection.** `$DATA/systems.json` (written atomically) holds the selected
id and the per-system speed overrides. `PDP11_SYSTEM` (else the catalog
default) is written there on the first start only; an unknown persisted id
falls back to the default.

**Default system**: 2.11BSD patch level 482 (Chase Covello's PiDP-11 image), from
`raw.githubusercontent.com` pinned to commit `b054907c…` (HTTPS, immutable). No
byte-identical third-party mirror exists for its 24 MB archive. pl499 (newer,
FTP-only, the upstream file changes: its sha256 is re-checked before releases)
is offered as an alternative entry.

## 8. Terminals

v1 has one browser terminal: the console. Systems with `terminals` expose their
DZ/DC lines through SimH's own telnet listener when `PDP11_TTY_TELNET` is set
(§6 step 2; the `Modem control activated` notice of `-m` is informational):

* `bsd211` (pl482): DZ11, 8 lines with modem control; `tty00`-`tty07` are on
  and run gettys: telnet and log in. Closing the connection hangs up the line,
  but 2.11BSD scans the carrier only about once a second, so a reconnect
  within that second gets the previous, still logged-in session: log out
  first (the README and both `enableHint`s say so).
* `bsd211-pl499`: the same after switching the lines on in `/etc/ttys` once
  (its `enableHint`). Only pl499 needs this edit.
* `rsx11mp`: DZ11 lines TT2:-TT11:, no modem control (RSX V3.0 never raises
  DTR): `HELLO SYSTEM` or `HELLO USER`; a closed connection leaves the session
  logged in for the next caller (log out with `BYE`).
* None: Unix V5/V6 (no tty device nodes), Unix V7 (the kernel has only hp/tm
  drivers), RT-11 (no multi-terminal monitor), RSTS/E V7 (the monitor never
  sets the DZ11's master scan enable).

The listener is unauthenticated (§10). The protocol reserves binary types
`0x20-0x27` and `tty*` subscriptions for v2.

## 9. Speeds

`simh.SpeedCommands` is the single definition (run.ini header, `SetSpeed`, the
golden files, `tests/systems/bootmatrix.py` `speed_lines`):

* `fast`: `set nothrottle` + `set cpu idle` (idles in WAIT). Not `idle=2`: at the
  pinned commit SimH pre-calibrates at start-up with a stability wait of 0, and
  `idle=2` raises it back to 2 s; the first 2 s of a boot then spin through WAIT
  unthrottled, which inflates the calibrated instructions per tick about 4× and
  costs host CPU for the rest of the run (boot-matrix A/B, interleaved on one
  host: UNIX V7 at its prompt 13.8-14.8 % host CPU with `idle=2` vs 3.8-4.1 %
  without, 2.11BSD 10.7-11.3 % vs 6.3-7.5 %).
* `authentic`: `set throttle 1M` (~1 MIPS like a real 11/70). SimH runs
  unthrottled for the first 2-3 s of a boot (until the clock has completed 3
  calibration seconds) and for about 20 ms after a run-time switch to
  authentic, while it measures the host. Run-time switches
  (`Machine.SetSpeed`) depend on `simh/patches/0003` (`SIMH_PATCH.md` §1
  item 3); without it fast → authentic stalls the guest clock. After a switch
  to fast the guest gets up to a second of clock ticks at once, which SimH's
  catch-up takes back within about 3 s (40 ticks/s).

Idle host CPU figures come from the shared, loaded 16-vCPU build VM and are
indicative; RT-11 (authentic) measured 4.4-7.6 %, RSX-11M-PLUS 5.3-5.6 % and
RSTS/E 4.1-5.0 % (fast, `bootmatrix.py`, 20 s at the prompt, PANEL attached).
RT-11 never executes WAIT, so its default is `authentic`. Web handles a `speed`
request with `Systems.SetSpeedFor(Selected(), s)` and, if powered,
`Machine.SetSpeed` (transparent stop). `state.speed = Systems.SpeedFor(Selected())`.

## 10. Security

* Raw SCP is never reachable from the network: pdp11d builds every SCP command
  from validated values (octal numbers, known register names, catalog data).
  Browser input only reaches the guest console.
* `PDP11_PASSWORD` enables Basic auth (any user name) for every route — the
  UI, the API and the WebSocket — except, with `PDP11_HEALTH_PUBLIC`,
  `GET`/`HEAD /api/status`. Every request authenticated by valid Basic
  credentials rather than a valid cookie (`api/status` included) gets a new
  session cookie `pdp11d_session`: a random
  nonce + HMAC-SHA256 under a key made at start (no server-side storage; valid
  until pdp11d restarts), `HttpOnly`, `SameSite=Strict`, `Secure` when the
  request came over TLS or with `X-Forwarded-Proto: https`, `Path` `/` or a
  sanitized `X-Forwarded-Prefix`. Pop-out windows and WebSocket handshakes use
  it. At start pdp11d logs `authentication: on` or `authentication: off` (then
  with the names the host check allows). Without a password on a non-loopback
  listener it also logs a warning, and the UI shows a banner
  (`hello.insecure`).
* **Failed logins**: after 20 failed Basic logins within a minute a client gets
  HTTP 429 (`Retry-After: 60`) for further attempts, the correct password
  included (otherwise the limit would not limit guessing; valid session
  cookies keep working). A client is an IPv4 address or an IPv6 /64. Behind a
  reverse proxy every request comes from the proxy, unless the proxy is
  listed in `PDP11_TRUSTED_PROXIES`: for a request from a listed address the
  client is the rightmost `X-Forwarded-For` entry that is not listed (entries
  further left come from the client and are never believed); without the
  header, with only listed entries, or when that entry is not an IP address,
  it is the proxy itself. `Forwarded` (RFC 7239) is not parsed. Log lines name
  the same client address. The limiter tracks at most 4096 clients. When its
  table is full it drops expired entries, then unblocked ones, but never a
  blocked one before it expires; while only blocked entries remain, a new
  client counts as blocked, so an attack from many addresses blocks new
  logins instead of lifting blocks. A full table is logged as a warning, at
  most once a minute.
* **Host check** (DNS rebinding), only while no password is set: every request
  (static files, `api/…`, the WebSocket handshake) must name an allowed host
  in its `Host` header; it is checked before authentication and routing, on
  the host without its port, IPv6 brackets and one trailing dot,
  case-insensitively. Allowed: no `Host`; any IP literal; `localhost` and
  `*.localhost`; the machine's host name (`os.Hostname()`, its first label and
  `<first label>.local`); the host name of `PDP11_LISTEN`, if it names one
  (`pdp11d health` sends it); the `PDP11_HOSTS` entries (`*.example.com` matches
  every subdomain at any depth, not example.com itself; `*` alone turns the
  check off); the host part of every `PDP11_ALLOW_ORIGINS` pattern, with its
  wildcards. Anything else gets HTTP 421 (`text/plain`: `pdp11d: the host name
  "<host>" is not allowed while no password is set. Add it to PDP11_HOSTS, or
  set PDP11_PASSWORD.`, which the browser shows instead of the UI) and a
  warning in the log, at most once a minute per host. Why: a web page the user
  opens can make its own domain resolve to pdp11d's address; it is then
  same-origin with itself, so the `Origin` check passes, but its `Host` header
  names its own domain. IP literals are safe because a rebound page always has
  a domain origin. This also protects a loopback listener without a password:
  the network cannot reach it, but the web pages its user opens can. In Docker
  the host name is the container's id, so a LAN name such as `nas.lan` must be
  listed, and so must the public name behind a reverse proxy that keeps
  `Host`. With a password there is no host check: a rebound page has neither
  the Basic credentials nor the cookie of the real host.
* `PDP11_TTY_TELNET` listeners are **unauthenticated**; documented as such.
* WebSocket `Origin` must name the request's `Host` (default ports ignored) or
  match a `PDP11_ALLOW_ORIGINS` pattern (`path.Match`, case-insensitive, on the
  host, or on `scheme://host` when the pattern contains `://`); else HTTP 403.
  Handshakes without `Origin` are accepted: browsers always send one, so they
  are not cross-site (tools and test harnesses); the password still applies.
* Limits: client messages ≤ 64 KiB (the UI splits pastes into ≤ 4 KiB
  messages); a client whose send queue exceeds 4 MiB is disconnected; the
  kernel send buffer of every connection is capped at 256 KiB (otherwise a
  stalled browser absorbs megabytes before its queue grows); request headers
  ≤ 64 KiB, read within 10 s.
* Every response carries `Content-Security-Policy: default-src 'self';
  script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:
  blob:; font-src 'self' data:; connect-src 'self' ws: wss:; frame-ancestors
  <PDP11_FRAME_ANCESTORS>; object-src 'none'; base-uri 'self'; form-action
  'self'` (styles may be inline because xterm.js's DOM renderer injects
  `<style>` elements; scripts never are), with the default `frame-ancestors
  'self'` also `X-Frame-Options: SAMEORIGIN` (with any other value none: only
  the CSP can express it), `X-Content-Type-Options: nosniff` and
  `Referrer-Policy: same-origin`. Other sites therefore cannot frame the UI
  (clickjacking of the power and factory-reset controls); a dashboard that
  shows the kiosk view (`?view=panel&chrome=0`) is allowed by listing its
  origin.
* System ids are validated against the catalog; no client-supplied paths;
  static files only from the UI file system.

## 11. Front panel and UI

Behaviour and lamps: `PANEL_LOGIC.md`. Wire format: `PROTOCOL.md`. UI contract
(layout, lamp and paddle rendering, help, keyboard): `UI.md`. Panel geometry:
`web/assets/panel/geometry.json` (from research r4's element table).

## 12. Licences

Our code: MIT, `LICENSE` ("Copyright (c) 2026 The pdp11-emu authors").
`THIRD_PARTY.md` lists everything else: Open SIMH (X11-style licence; the
patch files carry it; with the Slirp code it bundles), xterm.js 6.0.0 (MIT),
the fonts Glass TTY VT220 (Unlicense), Archivo (OFL), VT323 (OFL) and the
Montserrat Alternates outlines of the bezel lettering (OFL), the Go modules
coder/websocket (ISC) and ulikunitz/xz (BSD-3-Clause), the Go standard library,
and the licences of the OS images (`systems/*/LICENSE.txt`). The licence texts
ship next to the files (`web/assets/fonts/LICENSE-*`,
`web/assets/vendor/xterm/LICENSE.txt`) and with the binaries: one script,
`deploy/collect-licenses.sh`, collects them into `licenses/` of the image and
of native installs (`/opt/pdp11/share/doc/licenses`, next to `LICENSE` and
`THIRD_PARTY.md`) and of `make dist` tarballs. The image also has
`licenses/ca-certificates.txt` (the CA bundle's MPL-2.0 copyright file), and
its licenses label adds BSD-2-Clause (Slirp) and MPL-2.0. OS images are
never committed; they are downloaded from their original sources, and each
system's licence is shown before install. Container images may pre-fetch only
the redistributable systems; hobbyist images are downloaded by the user after
accepting their licence and are never redistributed.

## 13. Deployment

User documentation: `README.md` (Docker, compose, native install) and
`docs/PROXMOX.md` (LXC/VM). Design points:

* **Defaults for a clean stop**: the Docker image, `compose.yaml` and the
  systemd unit set `PDP11_SHUTDOWN_ON_STOP=1` and allow pdp11d ≥ 75 s to stop
  (§6): `compose.yaml` and the unit use 90 s; a plain `docker run`/`docker stop`
  needs `--stop-timeout 90`/`-t 90` (Docker's default is 10 s). A clean 2.11BSD
  shutdown takes 12-16 s; an unclean stop costs a ~30 s fsck (and a
  self-reboot) at the next boot. The code default stays `false`.
* **Docker** (`Dockerfile`, `.dockerignore` keeps `simh/src`, `simh/out`, local
  data and dev tooling out of the context): every stage that compiles runs on the
  build platform — SimH via `simh/build.sh` with a cross C compiler (`CC=…`,
  `--no-smoke`) when the target differs, pdp11d with `CGO_ENABLED=0 GOARCH=…` —
  and the runtime stage has no `RUN`, so linux/arm64 images build on amd64
  without QEMU/binfmt (and natively on a Raspberry Pi). A cache stage runs
  `pdp11d fetch` into `/opt/pdp11/cache`: `--build-arg PREFETCH=default` (the
  catalog default, 2.11BSD; the default), `all` (every redistributable system),
  `none`, or a list of ids; hobbyist systems are never baked in; a failed
  download or checksum fails the build; `--build-context archives=DIR` offers
  local archives. `# syntax=docker/dockerfile:1.27`; the base images
  (`GO_IMAGE`, `RUNTIME_IMAGE`) are tags, so that a rebuild gets their
  updates; digests can be pinned through them and `BUILDKIT_SYNTAX`. Runtime:
  `debian:trixie-slim` (whose `gzip` is used) plus the CA bundle;
  `/opt/pdp11/{bin,cache,share/doc}`; non-root user `pdp11` (10001) owning
  `/data`; `PDP11_DATA=/data`, `PDP11_SHUTDOWN_ON_STOP=1`; `VOLUME /data`;
  `EXPOSE 8080`; `HEALTHCHECK` `pdp11d health`; entry point `pdp11d serve`.
  `--target binaries` outputs `pdp11`, `pdp11d` and `licenses/`.
* **Compose** (`compose.yaml`, settings from `.env`, template `.env.example`):
  builds/uses `pdp11emu:latest` with `pull_policy: build` (never pulled; every
  `up` rebuilds from the build cache), named volume `pdp11-data:/data`,
  `${PDP11_HTTP_PORT:-8080}:8080`, `restart: unless-stopped`, `init: true`,
  `stop_grace_period: 90s`; `PDP11_PASSWORD`, `PDP11_HOSTS`,
  `PDP11_TRUSTED_PROXIES`, `PDP11_FRAME_ANCESTORS` and `TZ` from `.env` (the
  four settings as empty strings when `.env` leaves them out, which means
  their defaults, §5; `TZ` then `Etc/UTC`); commented
  `PDP11_ACCEPT_LICENSES` and `PDP11_TTY_TELNET` + port.
* **Native** (Debian 12/13, Ubuntu 22.04/24.04, amd64/arm64; VM or unprivileged
  LXC): `install.sh` (run as root from a checkout; `--port`, `--prefetch
  default|all|none`, `--archives DIR`, `--no-start`, `--no-systemd`,
  `--uninstall [--purge]`) installs the build tools (a pinned, sha256-checked Go
  release only if the system Go is too old), builds SimH and pdp11d in a work
  directory under `/opt/pdp11` (it first checks that programs can run there),
  and installs `/opt/pdp11/{bin,cache,share/doc}` (the licence texts in
  `share/doc/licenses`), the user `pdp11`, `/var/lib/pdp11`,
  `/etc/pdp11/pdp11d.env` (from `deploy/pdp11d.env`, kept if present) and
  `deploy/pdp11d.service`; re-running upgrades (settings and data kept). After
  a (re)start it checks that `pdp11d.service` stays active for 5 s, else it
  exits 1 with the `systemctl status`/`journalctl` hints. An existing login
  account `pdp11` gets a warning; `--purge` removes the account only if
  `install.sh` created it (a system uid, home `/var/lib/pdp11`, no login
  shell, the comment "PDP-11/70 appliance").
  The unit: `User=pdp11`, `PDP11_DATA=/var/lib/pdp11`,
  `PDP11_SHM=/run/pdp11/panel` (`RuntimeDirectory=pdp11`),
  `PDP11_SHUTDOWN_ON_STOP=1`, `EnvironmentFile=-/etc/pdp11/pdp11d.env`
  (overrides), `StateDirectory=pdp11`, `TimeoutStopSec=90`, `KillMode=mixed`
  (only pdp11d gets SIGTERM; it stops the simulator itself),
  `Restart=on-failure`, `NoNewPrivileges=yes`,
  `AmbientCapabilities=`/`CapabilityBoundingSet=CAP_NET_BIND_SERVICE` (ports
  below 1024); no mount-namespace sandboxing (fails in unprivileged LXC with
  226/NAMESPACE). Without systemd, `/opt/pdp11/bin/pdp11d-run` sets up the
  same environment (the settings file read with systemd's `EnvironmentFile=`
  rules) and execs pdp11d; run as root it drops to the user `pdp11` with
  `setpriv`, keeping `CAP_NET_BIND_SERVICE` as an ambient capability, and
  where that is impossible it runs without it and warns if a port below
  `ip_unprivileged_port_start` is configured.
* `Makefile`: `build` (`bin/pdp11`, `bin/pdp11d`), `test` (go vet, gofmt, go
  test, catalog check, harness unit tests, `test-deploy`
  (`deploy/test_pdp11d_run.py`), UI tests when Node is set up), `test-simh`,
  `test-e2e` (`tests/e2e`), `run` (development: own data directory, listener
  `127.0.0.1:8081`, panel file and console port), `docker`, `docker-arm64`
  (buildx, no emulation; both pass `VERSION` and `SOURCE_URL`), `dist` /
  `dist-docker` / `dist-arm64` (a tarball of prebuilt binaries, their
  `licenses/` and `install.sh`, built on the host or with the Dockerfile on
  Debian 13; owner 0:0, no group/other write, sorted, every mtime
  `SOURCE_DATE_EPOCH`, `gzip -n`), `clean`, `distclean`. `PDP11_TEST_CACHE`
  is passed through when set.
* **docs/PROXMOX.md**: prefer a native install in an unprivileged Debian CT over
  Docker-in-LXC (needs nesting/keyctl); time zone; reverse-proxy notes
  (WebSocket upgrades, `Host` with its port, `PDP11_ALLOW_ORIGINS`,
  `PDP11_HOSTS`, `PDP11_TRUSTED_PROXIES`, `X-Forwarded-Prefix`).

## 14. Platforms

linux/amd64 and linux/arm64 (Raspberry Pi 4/5). The shm protocol uses explicit
atomics on both sides (C `__atomic_*`, Go `sync/atomic`) and is little-endian
(`shm.Open` refuses big-endian hosts); the code has no other
architecture-specific parts. Tests run on amd64; arm64 is cross-compiled
(`simh/build.sh` with a cross `CC` and `--no-smoke`, Go with `GOARCH`), and
running it needs arm64 hardware or an emulator.

## 15. Tests

* `go test -race ./...`; opt-in end-to-end tests with the real simulator:
  `go test -tags e2e -run E2E ./server/internal/web/ -args -bsd211
  <2.11BSD_rq.dsk>` (toggle-in program of `UI.md` §6, 2.11BSD boot, console
  login, `shutdownOS`; the test runs in its package directory, so `-bsd211`
  needs an absolute path, and `-simh` defaults to `simh/out/pdp11`; a file
  named by a flag that does not exist fails the test). Tests that need OS
  archives (the real-simulator tests of `internal/simh` and `cmd/pdp11d`, the
  `tarZ` extraction test, the boot matrix) read catalog archives from
  `PDP11_TEST_CACHE` (named by their
  sha256, as `pdp11d fetch --cache DIR` writes them), extract what they need
  into temporary directories and skip without it; the simulator is
  `simh/out/pdp11` or `PDP11_TEST_SIMH`. The boot matrix through pdp11d itself:
  `go test -tags matrix -run TestMatrix -timeout 3h ./server/cmd/pdp11d/ -args
  -cache DIR[:DIR…]` (default `PDP11_TEST_CACHE`; every catalog system on an
  empty data directory: autostart to ready, the session typed over the
  WebSocket, panel frames, `shutdownOS`; 2.11BSD also restarts without fsck, a
  telnet login on a DZ line and a SIGTERM with `PDP11_SHUTDOWN_ON_STOP`;
  `-clock` also checks the Unix guests' clocks).
* `python3 -I simh/test/run_tests.py` (`SIMH_PATCH.md` §7).
* `python3 -I tests/systems/bootmatrix.py` (every catalog entry: boot, session,
  idle CPU, shutdown script, optionally the terminal lines and, with
  `--clock`, whether the Unix guests' clocks keep time;
  `tests/systems/README.md`) and its unit tests `tests/systems/test_bootmatrix.py`.
* `python3 -I deploy/test_pdp11d_run.py`: `pdp11d-run`'s reading of the
  settings file against systemd's rules, under dash, bash and busybox sh.
  `make test` runs it (`test-deploy`) with the Go tests, the catalog check,
  the harness unit tests and the UI tests.
* `cd web/dev && npm test`: UI tests in headless Chromium against the mock
  server (`web/dev/README.md`).
* `cd tests/e2e && npm test` (Node ≥ 20, playwright-core): headless Chromium
  against the real stack (pdp11d built from the checkout, the patched SimH,
  2.11BSD and RT-11 from an archive cache): first run to the ready banner and
  a login, panel operations, the toggle-in tutorial of `UI.md` §6, pop-out and
  "Bring back", reconnect and pdp11d restart, Shut down OS, the knob lamps and
  LAMP TEST while the machine is off, the boot timeout warning, RT-11's
  "Pull the plug" confirmations, the security headers; every page fails on a
  Content-Security-Policy violation.
