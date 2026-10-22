# System boot matrix

`bootmatrix.py` boots every catalog entry (`systems/<id>/`) the way pdp11d does and checks
that it comes up, works and shuts down cleanly. Python 3 standard library only; run it
with `python3 -I`.

```
# all systems, simulator from simh/out/pdp11, archives from a cache directory
python3 -I tests/systems/bootmatrix.py --cache /opt/pdp11/cache --work /tmp/matrix

# twice each, and boot every result a second time in a new simulator process
python3 -I tests/systems/bootmatrix.py --boots 2 --second-boot

# one system, plus a telnet login on its extra terminal lines
python3 -I tests/systems/bootmatrix.py --system bsd211 --tty

# the Unix systems, and whether their clocks keep time
python3 -I tests/systems/bootmatrix.py --system bsd211 --system bsd211-pl499 \
    --system unixv7 --system unixv6 --system unixv5 --clock
```

## What one run does

1. **Images.** Each artifact is looked up by size and sha256 in the `--cache` directories
   (searched recursively, so a pdp11d `$DATA/cache`, `PDP11_CACHE` or any download tree
   works) and in `<work>/cache`; only missing archives are downloaded (url, then mirrors,
   3 attempts each). The listed files are extracted once into `<work>/pristine/<sha256>/`
   (sha256 checked) and copied sparsely into a fresh working directory for every run (sha256
   checked again).
2. **run.ini** exactly as in `docs/DESIGN.md` §6: the generated header (`set cpu 11/70`,
   memory, `set nothrottle` + `set cpu idle` or `set throttle 1M`, raw telnet console on a
   free localhost port, `set console telnet=buffered=65536`, `set tto 7b`, and
   `attach panel <file>` when the simulator has the PANEL device), then `boot.ini` with
   `${DISKn}` replaced by absolute paths, then, with `--tty`, the terminal line pdp11d
   generates: `attach -u [-m] <device> <listener>`.
3. **Simulator.** `pdp11 run.ini` with cwd and `HOME` = the run directory, stdin/stdout pipes
   (stderr merged), its own process group, SIGTERM on harness death. The harness waits for the
   first `sim> `, maps every diagnostic to its boot.ini line and treats anything but pdp11d's
   informational lines (`%SIM-INFO`, the reset notices, `Modem control activated`, `Idling
   disabled`, `Disabling …`, `Sockets: …`; `docs/DESIGN.md` §6 step 4) as a power-on error
   (`--allow-diag REGEX` to tolerate one).
4. **Panel and console.** With the PANEL device the export is mapped and checked (magic,
   layout version 2 or 3, size), the switch register is written (`--sr`, default 0), halts use the
   `halt_req`/`halt_ack` handshake and every run command is preceded by a cancel. Without it
   (stock SimH) halts fall back to SIGINT, which pdp11d never uses. The console is a raw TCP
   connection (retried every 200 ms for 3 s); input is paced like pdp11d's console stream
   (32 bytes at once, then 100 characters per second).
5. **Boot.** `dialog.do` = `noexpect`, `nosend`, the dialog.ini lines; `do dialog.do` (its
   reply must be empty), then the boot command. The run fails if the CPU stops before
   `readyPattern` appears.
6. **Session.** `tests.session` from system.json (log in, compile or run something, check the
   output). Each step waits for its `expect` (a literal substring of output produced after the
   previous step's send), then types `send`. With `--clock`, the Unix systems then type `date`,
   wait 20 s and type `date` again: the guest clock must have advanced the wall time within 10 %
   plus 1 s (a kernel built for a 50 Hz line clock under SimH's default 60 Hz gains 20 %).
7. **Idle CPU.** Host CPU of the simulator process over `--idle-secs` (default 10) while the
   system sits at its prompt.
8. **Shutdown.** `shutdownScript` as pdp11d's "Shut down OS" types it (with its 250 ms pause
   between an `expect` match and the next `send`). If the script ends with a `send`, the CPU
   must stop with `HALT instruction` within 30 s; if it ends with an `expect`, the harness
   waits 3 s and halts the CPU from the front panel. Then `quit`, and the simulator must exit
   with status 0.
9. **Terminals** (`--tty`, systems with `terminals`): the OS-side steps from `TTY_PROCEDURES`
   in the script, then a telnet connection to the listener: login, a command, logout; for
   lines with modem control also the hang-up (close the connection while logged in,
   reconnect, expect a fresh `login:`).

`--no-session` skips step 6, so the shutdown script starts from the state right after the
boot (for example at `login:`). `--second-boot` boots the same disks again in a new
simulator process after each run (pdp11d restarts the simulator for a Boot after the CPU
has run, so every boot is a fresh process). Each run leaves `run.ini`, `dialog.do` and
`transcript.log` (SCP and console output) in `<work>/runs/<id>/r<n>/run<boot>/`.

The table at the end lists every run (boot time from the boot command to `readyPattern`,
session time, idle host CPU, shutdown time, stop reason, exit status, telnet line and guest
clock results), then a per-system summary. `--json FILE` writes the results; the exit status
is 0 only if every run passed.

## Through pdp11d

`server/cmd/pdp11d/matrix_test.go` (build tag `matrix`) runs the same catalog through the real
daemon: the test binary re-executes itself as `pdp11d serve` with a fresh, empty `PDP11_DATA`,
`PDP11_SYSTEM=<id>`, `PDP11_ACCEPT_LICENSES=<id>` and the default autostart, and a WebSocket
client (`ws?sub=panel,console,log`) waits for `state.ready`, types `tests.session` like a typist
(one character per message), checks the panel frames (RUN lit while running), sends
`shutdownOS` (RT-11: key OFF) and requires run off with the key at OFF within the script's
budget; SIGTERM must then end pdp11d with status 0. 2.11BSD also gets a second start (key to
POWER: no fsck, ready in < 20 s; a telnet login on a DZ line with `PDP11_TTY_TELNET`; SIGTERM
with `PDP11_SHUTDOWN_ON_STOP=1` must shut the OS down cleanly) and a third (autostart, the key
stayed at POWER; no fsck).

```
go test -tags matrix -run TestMatrix -timeout 3h -v ./server/cmd/pdp11d/ -args \
    -cache /opt/pdp11/cache -work /tmp/pdmatrix [-systems bsd211,rt11] [-rounds 3] \
    [-listen-port 18080] [-port-base 23500] [-shm DIR]
```

It prints a table (install s, ready s from process start or key POWER, boot → ready s,
session, panel frames in 2 s, shutdown s, SIGTERM → exit s) and writes `matrix.json`,
`pdp11d-<n>.log`, `console-<n>.txt` and `events-<n>.txt` (timestamped WebSocket messages and
typed input) under `-work`. `-clock` also checks, for the Unix systems after the session,
that the guest clock keeps time while the system idles (`date`, 20 s, `date`).
