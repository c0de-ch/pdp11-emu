# PDP-11/70 appliance

A PDP-11/70 that runs in Docker, an LXC container or a VM and is used from a web
browser: a console terminal and a high-fidelity, operable replica of the
PDP-11/70 front panel, with lamps that glow like the real ones, switches that
work like the real ones, and a menu of historic operating systems. 2.11BSD
boots by default; UNIX V5, V6 and V7, a newer 2.11BSD, RT-11, RSX-11M-PLUS and
RSTS/E are a click away.

<!-- Screenshots (to be added; tests/e2e takes them on every run): docs/img/main-view.png
     (panel and console side by side, the e2e shot f-main-1920x1080.png),
     docs/img/front-panel.png (the panel while 2.11BSD idles, f-panel-1340.png),
     docs/img/toggle-in.png (the light chaser, c-light-chaser.png). -->

Under the hood it is [Open SIMH](https://github.com/open-simh/simh) with a small
patch that exports what the real front panel shows (bus cycles weighted by
wall-clock time, registers, the run and WAIT state, a live switch register),
supervised by `pdp11d`, a single Go binary that also serves the UI. Any number
of browsers can watch and operate the one machine at the same time, and the
panel and the console can each have a window of their own.

* [Quick start: Docker](#quick-start-docker)
* [Install in a VM or LXC container](#install-in-a-vm-or-lxc-container) (and [Proxmox](docs/PROXMOX.md))
* [Using it](#using-it): front panel, console, windows, systems, toggle-in tutorial, keys
* [Configuration](#configuration)
* [Security](#security) and [reverse proxies](#behind-a-reverse-proxy)
* [Troubleshooting](#troubleshooting)
* [Development](#development) and [architecture](#architecture)

## Quick start: Docker

It needs Docker Engine 23 or newer with BuildKit (the buildx plugin) and
Compose v2 (`docker compose`); the health check's 2-s start interval needs
Engine 25. Debian 12's own packages (docker.io 20.10, docker-compose 1.29)
are too old: install `docker-ce`, `docker-buildx-plugin` and
`docker-compose-plugin` from Docker's repository there. Debian 13: `apt install
docker.io docker-buildx docker-compose`; Ubuntu 22.04/24.04: `apt install
docker.io docker-buildx docker-compose-v2` (without the buildx package,
`docker build` falls back to the legacy builder, which rejects the
Dockerfile).

```sh
git clone <this repository> pdp11-emu && cd pdp11-emu
cp .env.example .env          # optional: set PDP11_PASSWORD (and TZ) in .env
docker compose up -d          # builds the image pdp11emu:latest from the checkout
```

Compose expands `$` in `.env`, also inside double quotes: write a password
that contains `$` in single quotes (`PDP11_PASSWORD='pa$w0rd'`) or double
every `$` (`pa$$w0rd`); unquoted, `PDP11_PASSWORD=$ecret` becomes an empty
password, which turns authentication off.

Open `http://<host>:8080/`. On an empty volume pdp11d installs 2.11BSD from the
archive baked into the image (about 6 s), powers the machine on and boots
(about 10 s): the console shows `login:`. Log in as **root** (no password). No
click is needed; the panel lamps come alive while it boots.

`docker compose down` (or `stop`) shuts 2.11BSD down cleanly first (about 16 s;
the image sets `PDP11_SHUTDOWN_ON_STOP=1` and compose.yaml allows 90 s), so the
next boot finds clean file systems. `docker compose down -v` also deletes the
volume with the disks.

Upgrade with

```sh
git pull && docker compose up -d --build
```

It rebuilds the image from the checkout (compose.yaml never pulls it) and
replaces the container; the old one shuts its guest OS down cleanly first, and
the disks stay in the volume. Open pages reload themselves when the new
version brings a changed UI.

Without compose:

```sh
docker build -t pdp11emu:latest .
docker run -d --name pdp11 -p 8080:8080 -v pdp11-data:/data \
    --stop-timeout 90 -e PDP11_PASSWORD=change-me pdp11emu:latest
docker stop -t 90 pdp11       # the default 10 s is too short for a clean shutdown
```

| | |
|---|---|
| Volume | `/data`: disks of the installed systems, panel and system settings (owned by uid 10001; a bind-mounted directory must be writable for it: `chown 10001:10001 DIR`) |
| Port | 8080 (HTTP and WebSocket); compose.yaml publishes `${PDP11_HTTP_PORT:-8080}`, on all interfaces by default (see [Security](#security)) |
| Health | `HEALTHCHECK` runs `pdp11d health` (GET `api/status`, with the password if one is set); `docker ps` shows `healthy` when pdp11d answers, whether or not the guest OS is up |
| User | 10001 (`pdp11`), no root inside the container |
| Size | about 59 MB compressed (Debian trixie-slim, the two binaries, the 24 MB 2.11BSD archive) |

Build arguments and contexts:

* `--build-arg PREFETCH=default|all|none|<ids>`: archives baked into
  `/opt/pdp11/cache` (`default`: 2.11BSD, 24 MB; `all`: every redistributable
  system, 79 MB). Hobbyist-licensed systems are never baked in.
* `--build-context archives=DIR`: take the archives from a local directory
  (any file names, matched by size and sha256) instead of downloading them,
  e.g. a cache written by `pdp11d fetch --cache DIR`.
* `--build-arg VERSION=...`: the version pdp11d reports; `SOURCE_URL=...`:
  the image's source label (`make docker` sets both from git).
* `--build-arg GO_IMAGE=... RUNTIME_IMAGE=...`: the base images. They are tags
  (`golang:1.27-trixie`, `debian:trixie-slim`), so that a rebuild picks up
  their security updates; pin digests for a reproducible build, e.g.
  `RUNTIME_IMAGE=debian:trixie-slim@sha256:...` (`docker buildx imagetools
  inspect debian:trixie-slim` shows it), and the Dockerfile frontend with
  `BUILDKIT_SYNTAX=docker/dockerfile:1.27@sha256:...`.
* **arm64** (Raspberry Pi 4/5): `docker buildx build --platform linux/arm64 -t
  pdp11emu:arm64 --load .` works on an amd64 host without QEMU (everything is
  cross-compiled; the runtime stage has no `RUN` step), and the same Dockerfile
  builds natively on the Pi. `--target binaries --output type=local,dest=DIR`
  extracts just `pdp11`, `pdp11d` and their licence texts (`licenses/`).

## Install in a VM or LXC container

On Debian 12/13 or Ubuntu 22.04/24.04 (amd64 or arm64), in a VM or an
unprivileged LXC container, as root (or with `sudo`):

```sh
git clone <this repository> pdp11-emu && cd pdp11-emu
./install.sh                  # or: ./install.sh --port 8080 --prefetch all
```

`install.sh` installs the build tools (`build-essential git ca-certificates`),
uses the system's Go if it is new enough for `go.mod` or else downloads the
pinned official Go release (sha256-checked), builds the patched SimH and
pdp11d from the checkout in a work directory under `/opt/pdp11` (not in
`/tmp` or `/var/tmp`, which hardened hosts mount `noexec`), prefetches the
default system's archive, creates the system user `pdp11` and enables and
starts `pdp11d.service`, checks that it keeps running and prints the URL. It
takes about a minute on a fast machine.

It needs access to these hosts (HTTPS unless noted; the usual proxy
variables such as `https_proxy` work, except for FTP):

* the Debian or Ubuntu package mirrors;
* `github.com` (the Open SIMH commit) and `proxy.golang.org` (Go modules);
* `go.dev` and `dl.google.com`, only when the system's Go is older than
  `go.mod` needs;
* `raw.githubusercontent.com`: the default system's archive;
* for `--prefetch all`, and for systems installed later from the UI:
  `simh.trailing-edge.com`, `www.retro11.de`, `web.archive.org` (mirrors) and
  passive FTP to `ftp.dfupdate.se` (2.11BSD pl499).

| Path | |
|---|---|
| `/opt/pdp11/bin/` | `pdp11` (simulator), `pdp11d`, `pdp11d-run` (start without systemd) |
| `/opt/pdp11/cache/` | prefetched archives (a read-only cache, `PDP11_CACHE`) |
| `/opt/pdp11/share/doc/` | `README.md`, `LICENSE`, `THIRD_PARTY.md` and the third-party licence texts (`licenses/`) |
| `/etc/pdp11/pdp11d.env` | settings ([Configuration](#configuration)); written once, never overwritten |
| `/var/lib/pdp11/` | data: disks, panel state (`PDP11_DATA`, home of the user `pdp11`) |
| `/etc/systemd/system/pdp11d.service` | the service (`journalctl -u pdp11d` for its log) |

| Option | |
|---|---|
| `--port N` | listen on port N (sets `PDP11_LISTEN=:N` in the settings file; ports below 1024 work, the service has `CAP_NET_BIND_SERVICE`) |
| `--no-start` | install and enable the service, but do not (re)start it |
| `--no-systemd` | install the files only (containers without systemd); start `/opt/pdp11/bin/pdp11d-run` as root, stop it with SIGTERM |
| `--prefetch default\|all\|none` | archives for `/opt/pdp11/cache` |
| `--archives DIR` | take the archives from DIR instead of downloading them |
| `--uninstall [--purge]` | remove the service and `/opt/pdp11`; `--purge` also removes the settings, the data and the user `pdp11` if `install.sh` created it |

An existing account named `pdp11` is used as it is; if it is a login account,
`install.sh` warns, since the service then runs with all its groups.

Re-running `install.sh` (after `git pull`) upgrades: it rebuilds, replaces the
binaries and restarts the service; a running guest OS is shut down cleanly
first (`PDP11_SHUTDOWN_ON_STOP=1` in the unit, `TimeoutStopSec=90`). The unit
has no mount-namespace sandboxing on purpose: options such as `ProtectSystem=`
or `PrivateTmp=` fail in unprivileged LXC containers (status 226/NAMESPACE).

`make dist` builds a tarball with prebuilt binaries and their licence texts;
`install.sh` from an unpacked tarball installs them without any build tools.
The simulator needs the glibc of the machine it was built on or a newer one
(`install.sh` checks): `make dist-docker DIST_PLATFORM=linux/amd64` and `make
dist-arm64` build the binaries with the Dockerfile on Debian 13, which suits
Debian 13 and Ubuntu 24.04.

**Proxmox VE**: see [docs/PROXMOX.md](docs/PROXMOX.md); an unprivileged Debian
13 container with 1 core, 512 MB and 4 GB is enough.

## Using it

### The screen

The toolbar holds the current system (click it for the system chooser),
**Power** (the key switch: OFF ↔ POWER), **Boot**, **Shut down OS**, the speed
(**Fast** or **Authentic**), view settings (lamp style: LED, PiDP-11,
incandescent or reduced motion; console colour: green, amber or white; 132
columns; Authentic keys; Screen reader mode; …), the pop-out buttons,
**Help** and a status pill (run state, ready, executed instructions per
second, the number of viewers, the connection). The panel and the console sit
side by side on a wide screen and are stacked on a narrow one; no page
scrolling at 1366×768 and larger; small windows scroll rather than shrink the
console below a readable size.

Powering off, booting while the CPU runs or switching systems asks first and
offers **Shut down the OS first (recommended)**, **Pull the plug** or
**Cancel**. "Shut down OS" types the system's shutdown commands on the console
(2.11BSD: `sync`, `shutdown -h now`), waits for the OS to halt the CPU and
then powers off. RT-11 has nothing to shut down, so it only offers "Pull the
plug". The same confirmations cover an OS halted from the panel: it is still
in memory, and **Continue and shut down** lets it run again and shuts it down.

### Front panel

Every control works: the 22 switch-register paddles, LAMP TEST, LOAD ADRS,
EXAM, DEP, CONT, ENABLE/HALT, S INST/S BUS CYCLE, START, both knobs and the
OFF/POWER/LOCK key. Click a paddle to toggle it, drag across paddles to set
several, hold a momentary switch to keep it pressed; turn a knob by clicking
its left or right half, with the mouse wheel or by dragging. The lamps show
duty-cycle brightness from the simulated bus: idle 2.11BSD, RSX and RSTS show
their rotating patterns on the DATA lamps in DATA PATHS, a busy CPU flickers.

As on the real machine, LOAD ADRS, EXAM, DEP, CONT and START act only while the
CPU is halted (ENABLE/HALT down) and the key is not at LOCK; the panel tells
you when a switch is ignored. START at 165000 or 173000 (the boot ROM entry
points) boots the selected system. Help → Panel guide labels every control.

### Console

The DL11 console in xterm.js (80×24, optionally 132 columns), in the Glass TTY
VT220 font. Click it to type; everything typed goes to the PDP-11, Ctrl+C and
Tab included; Shift+Tab leaves the console (F6 goes to the address bar).
Backspace and the interrupt key follow what the selected system expects (DEL,
BS or `#`): Backspace and Delete with any modifier (Ctrl, Alt or Shift;
Option or ⌘ on a Mac) send the system's erase character, unless **Authentic
keys** (view settings) is on, which sends the raw keys. Copy with
Ctrl+Shift+C (⌘C), paste with Ctrl+Shift+V (⌘V) or the browser's paste;
pastes are typed at the console's speed, with a Cancel button. Keys typed
during a paste follow it, and the interrupt key (Ctrl+C) cancels the rest of
the paste. The "Send key" menu sends ^C ^O ^Q ^S ^T ^U ^W ^Z DEL ESC. With a
screen reader, turn on **Screen reader mode** (view settings) to read the
console text.

### Windows and viewers

The two pop-out buttons move the panel or the console into a window of its
own (allow pop-ups for the site); the main window then shows "Bring back".
The views also have URLs of their own: `?view=panel`, `?view=console`, and
`&chrome=0` hides the toolbar (kiosk). A page of another site may show them in
a frame only if `PDP11_FRAME_ANCESTORS` lists it. Every browser window is a
viewer of the same machine; the status pill shows how many there are.

### Systems and licences

| System | id | Download → disk | Licence | Console login |
|---|---|---|---|---|
| 2.11BSD patch level 482 (default) | `bsd211` | 24 MB → 1 GB RA82 | redistributable | root, no password |
| 2.11BSD patch level 499 | `bsd211-pl499` | 48 MB → 1 GB RA82 (FTP) | redistributable | root, no password |
| UNIX Seventh Edition | `unixv7` | 4 MB → 88 MB RP04 | redistributable | root / root |
| UNIX Sixth Edition | `unixv6` | 1.5 MB → 4 × RK05 | redistributable | root, no password |
| UNIX Fifth Edition | `unixv5` | 0.8 MB → RK05 | redistributable | root, no password |
| RT-11 V5.3 | `rt11` | 1.4 MB → RL02 | hobbyist | none needed |
| RSX-11M-PLUS V3.0 | `rsx11mp` | 13 MB → 174 MB RP06 | hobbyist | none needed (logged in) |
| RSTS/E V7.0 | `rsts` | 4.9 MB → 2 × RL01 | hobbyist | HELLO 1,2 / SYSTEM |

The system chooser shows every system with its description, sizes, licence
and install state. Systems are downloaded from their original sources when you
install them (sha256-verified, with mirrors); the large disk images are
written as sparse files, so 2.11BSD's 1 GB disk takes about 170 MB. **Redistributable** systems (UNIX
V5-V7, 2.11BSD) install without questions. RT-11, RSX-11M-PLUS and RSTS/E are
under the **Mentec PDP-11 hobbyist licence**: the UI shows the licence text and
installs only after you accept it (or list the ids in `PDP11_ACCEPT_LICENSES`
for unattended installs); they are never included in images built from this
repository. "Factory reset" re-creates a system's disks from the archive.
Help → System shows the notes for the selected system: how to log in, things
to try, how to shut down. [THIRD_PARTY.md](THIRD_PARTY.md) lists all sources
and licences.

2.11BSD and RSX-11M-PLUS also have terminal lines (a DZ11 with eight lines)
for more users at once, reachable over telnet: set `PDP11_TTY_TELNET=:4000`
and restart pdp11d (compose: uncomment it and its port mapping in
compose.yaml, then `docker compose up -d`; native: in `/etc/pdp11/pdp11d.env`,
then `systemctl restart pdp11d`), then `telnet <host> 4000`. Each connection
gets the next free line. These lines are unauthenticated: see
[Security](#security).

* 2.11BSD patch level 482 runs a `getty` on all eight lines: log in as root.
  Log out (Ctrl+D) before you close the connection: closing it hangs up the
  line, but 2.11BSD notices that only about once a second, and a new
  connection within that time gets the old, still logged-in session.
* 2.11BSD patch level 499 ships with the lines switched off. Switch them on
  once as root: `sed -e '/^tty0[0-7]/s/off/on/' /etc/ttys > /tmp/ttys && cp
  /tmp/ttys /etc/ttys && kill -HUP 1`. Log out before closing a connection, as
  with patch level 482.
* RSX-11M-PLUS (lines TT2: to TT11:): press Return at the `>` prompt and log in
  with `HELLO SYSTEM` (password `SYSTEM`) or `HELLO USER` (`USER`); log out with
  `BYE`, since closing the connection leaves the session logged in.

### Toggle-in tutorial

Help → Toggle-in tutorial enters a program with the switches, the way programs
were loaded before boot ROMs, and shows every step with the current values;
"Auto-toggle" performs the steps with the real panel controls. The program is
a light that walks across the DATA lamps on the DISPLAY REGISTER:

| Address | Word | |
|---|---|---|
| 001000 | 012700 000001 | MOV #1,R0 |
| 001004 | 010037 177570 | MOV R0,@#177570 (display register) |
| 001010 | 000240 | NOP |
| 001012 | 077101 | SOB R1,. (delay) |
| 001014 | 006100 | ROL R0 |
| 001016 | 000772 | BR 001004 |

1. Key to OFF, ENABLE/HALT down (HALT), key to POWER: the CPU powers up halted.
   Set the speed to Authentic.
2. Set SR to 001000 and press LOAD ADRS.
3. For each of the eight words: set SR to it and raise DEP (each DEP after the
   first steps the address by 2).
4. SR to 001000, LOAD ADRS, ENABLE/HALT up (ENABLE), press START.
5. Turn the DATA knob to DISPLAY REGISTER: a single light walks across DATA.

### Keyboard shortcuts

Front panel (while it has the focus; Tab moves between the switch row, the
knobs and the key):

| Keys | |
|---|---|
| ← → (Home, End) | previous / next switch |
| ↑ / ↓ | SR bit 1 / 0; raise DEP or LAMP TEST; press LOAD ADRS, EXAM, CONT, START; ENABLE/S INST up, HALT/S BUS down |
| Space, Enter | toggle or press (hold) the focused switch |
| 0 … 7 | shift an octal digit into the switch register; Backspace shifts right; Esc clears it |
| L, E, D, C, S | LOAD ADRS, EXAM, DEP, CONT, START (hold) |
| T | LAMP TEST (hold) |
| H, B | toggle ENABLE/HALT, S INST/S BUS CYCLE |
| [ ], { } | ADDRESS knob, DATA knob anticlockwise / clockwise |
| K, Shift+K | key switch clockwise / anticlockwise (OFF asks first) |

Console: Ctrl+Shift+C / Ctrl+Shift+V (⌘C / ⌘V) copy and paste; Ctrl+C always
goes to the PDP-11. Shift+Tab leaves the console (Tab goes to the PDP-11, F6
to the address bar). Browser shortcuts such as Ctrl+W cannot be sent; use the
"Send key" menu.

### Speed

**Fast** runs the CPU unthrottled and idles the host when the guest executes
WAIT (`set nothrottle`, `set cpu idle`): a few per cent of one host core while
the OS idles. **Authentic** throttles to about 1 MIPS like a real 11/70 (`set
throttle 1M`). RT-11 never executes WAIT, so it defaults to Authentic. The
status pill's "instr/s" counts executed instructions only, so it is low while
a fast system idles in WAIT.

## Configuration

pdp11d reads `PDP11_*` environment variables (Docker: `environment:` in
compose.yaml, which takes `PDP11_PASSWORD`, `PDP11_HOSTS`,
`PDP11_TRUSTED_PROXIES`, `PDP11_FRAME_ANCESTORS` and `TZ` from `.env`, or
`docker run -e`; native installs: `/etc/pdp11/pdp11d.env`). `pdp11d serve`
also takes each as a flag in lower-case-dash form (`--listen :9090`), which
wins over the environment. An empty variable means its default.

| Variable | Default | Meaning |
|---|---|---|
| `PDP11_LISTEN` | `:8080` | HTTP listen address, `[host]:port` |
| `PDP11_PASSWORD` | empty | password for the UI, API and WebSocket (HTTP Basic auth, any user name); empty: no authentication |
| `PDP11_DATA` | `/data` in a container, else `/var/lib/pdp11` | data directory (disks, `panel.json`, `systems.json`, `cache/`, `import/`, `run/`) |
| `PDP11_SIMH` | `/opt/pdp11/bin/pdp11` | the patched simulator |
| `PDP11_CACHE` | `/opt/pdp11/cache` | read-only archive caches searched before downloading; several directories separated by `:` |
| `PDP11_SYSTEM` | the catalog default (`bsd211`) | system selected on the **first** start only (then remembered in `systems.json`) |
| `PDP11_ACCEPT_LICENSES` | empty | comma list of hobbyist system ids whose licence you accept, for installs without the dialog |
| `PDP11_AUTOSTART` | `true` | at start, power on if the key switch was left at POWER or LOCK |
| `PDP11_SHUTDOWN_ON_STOP` | `false` (image and service: `1`) | on SIGTERM, run the guest OS's shutdown script before powering off (needs a stop timeout of 75 s or more) |
| `PDP11_TTY_TELNET` | empty | e.g. `:4000`: a telnet listener for the system's extra terminal lines (2.11BSD, RSX); **unauthenticated** |
| `PDP11_ALLOW_ORIGINS` | empty | extra allowed WebSocket `Origin` hosts, comma list of patterns such as `pdp11.example.com` or `*.example.com:8443` |
| `PDP11_HOSTS` | empty | while no password is set: host names (no ports) under which the UI may be opened, besides IP addresses, `localhost`, the machine's own name, the host name in `PDP11_LISTEN` and the `PDP11_ALLOW_ORIGINS` hosts; comma list, `*.example.com` matches every subdomain, `*` turns the check off ([Security](#security)) |
| `PDP11_TRUSTED_PROXIES` | empty | reverse proxies (comma list of addresses or CIDRs) whose `X-Forwarded-For` is trusted: the failed-login limit and the log use the client's address |
| `PDP11_FRAME_ANCESTORS` | `'self'` | pages that may show the UI in a frame: a CSP `frame-ancestors` source list, e.g. `'self' https://dashboard.example.com` (kiosk views) |
| `PDP11_HEALTH_PUBLIC` | `false` | answer `GET api/status` without the password |
| `PDP11_PORT_BASE` | `23400` | localhost TCP port of the simulator's console |
| `PDP11_SHM` | `/dev/shm/pdp11d-panel` (service: `/run/pdp11/panel`) | the panel export file shared with the simulator: a regular file of pdp11d's user with mode 0600 ([Troubleshooting](#troubleshooting)) |
| `PDP11_FRAME_HZ` | `60` | lamp frame rate, 30-120 |
| `PDP11_LOG` | `info` | log level: `debug`, `info`, `warn`, `error` |
| `PDP11_SYSTEMS` | (embedded) | a catalog directory that replaces the built-in one |
| `PDP11_WEB` | (embedded) | serve the UI from this directory (development) |

Paths and the telnet listener must not contain blanks, quotes or `;` (they
are passed to SimH). Archives that cannot be downloaded can be dropped into
`$PDP11_DATA/import/`: any file whose size and sha256 match an archive of the
catalog is moved into the cache.

`/etc/pdp11/pdp11d.env` is read with the rules of systemd's
`EnvironmentFile=`, also by `pdp11d-run`: put a value with spaces, quotes,
backslashes or `$` in single quotes (`PDP11_PASSWORD='a$b\c'`; unquoted or
in double quotes, systemd treats a backslash as an escape). In `.env`,
Compose's rules apply ([Quick start](#quick-start-docker)).

The date and time that RT-11, RSX, RSTS and UNIX V7 get typed in at boot
come from the local time of pdp11d. Docker uses `TZ` (`.env`, default UTC); a
native install uses the system's time zone (`timedatectl set-timezone
Europe/Zurich`; a Proxmox container's: [docs/PROXMOX.md](docs/PROXMOX.md)) or
`TZ=Europe/Zurich` in `/etc/pdp11/pdp11d.env`.

`pdp11d` sub-commands: `serve` (default), `fetch [--cache DIR] [id...]`
(download and verify archives; default: every redistributable system),
`install [--accept-license] id...` (it refuses with exit status 2 while a
pdp11d serves the same `PDP11_DATA`: stop the service, or install from the
UI), `check [dir]` (validate a catalog),
`health [--url URL] [--timeout D]` (exit 0 if the local pdp11d answers),
`version`.

HTTP API (same authentication as the UI): `GET api/status` (`{"ok": true,
"version", "run", "system", "power"}`; `version` is `<version>+<12 hex
digits>`, the version that `pdp11d version` prints plus a build id of the UI
files), `api/state`, `api/systems`, `api/log?lines=N` (simulator log). The
WebSocket protocol is [docs/PROTOCOL.md](docs/PROTOCOL.md).

## Security

* **Set `PDP11_PASSWORD`** whenever anyone else can reach the port. Without
  it, anyone who can open the page controls the machine; pdp11d logs a
  warning and the UI shows "No password set: anyone on your network can
  control this machine" whenever it listens on a non-loopback address (in
  Docker always, since pdp11d cannot see how the port is published). With a
  password every request needs HTTP Basic auth (any user name) or the session
  cookie set after it (HttpOnly, SameSite=Strict, Secure behind HTTPS; valid
  until pdp11d restarts). After 20 failed logins within a minute an address
  (an IPv6 address: its /64) gets HTTP 429 for the rest of that minute, even
  with the right password. Under a flood of failures from very many
  addresses, logins from new addresses are refused for a while; existing
  sessions keep working.
* **Without a password**, pdp11d answers only requests for an IP address,
  `localhost` (or a name ending in `.localhost`), the machine's host name
  (also just its first label, or that label with `.local`), the host name
  given in `PDP11_LISTEN` (`PDP11_LISTEN=pdp11.lan:8080`) and the names in
  `PDP11_HOSTS` and `PDP11_ALLOW_ORIGINS`. Any other name gets HTTP 421 with
  a message that says what to do. This keeps a web page that a user on your
  network opens from taking over the machine through DNS rebinding. In Docker
  the machine's own name is the container's: list the names you open the UI
  with, e.g. `PDP11_HOSTS=nas.lan,pdp11.local`.
* **Docker-published ports bypass the host's firewall** (ufw, firewalld):
  Docker's own iptables rules let the traffic in before the firewall's input
  rules see it. The compose default (port 8080 on every interface, no
  password) is therefore open to every network that reaches the host. Set
  `PDP11_PASSWORD`, or publish on loopback only (`PDP11_HTTP_PORT=127.0.0.1:8080`
  in `.env`) behind a reverse proxy.
* pdp11d speaks plain HTTP. Put a TLS reverse proxy in front of it for
  anything beyond a trusted LAN.
* The telnet terminal lines (`PDP11_TTY_TELNET`) are **unauthenticated**, and
  2.11BSD's `root` has no password: set passwords in the guest (`passwd`)
  before you publish that port.
* The simulator's command interface is never reachable from the network:
  pdp11d builds every simulator command itself from validated values, and
  browser input only reaches the guest console.
* WebSocket connections must come from the page's own origin (the `Origin`
  host must equal `Host`) or from a host listed in `PDP11_ALLOW_ORIGINS`.
* Every response carries a Content-Security-Policy. Only the UI itself may
  show it in a frame (`X-Frame-Options: SAMEORIGIN` too), unless
  `PDP11_FRAME_ANCESTORS` lists other pages.

### Behind a reverse proxy

The UI uses relative URLs only, so it works at the root of a host or under a
sub-path. The proxy must pass WebSocket upgrades for `ws` and should keep the
`Host` header with its port (else list the public host in
`PDP11_ALLOW_ORIGINS`). For a sub-path, send `X-Forwarded-Prefix` (the session
cookie's path); send `X-Forwarded-Proto: https` so that the cookie is marked
Secure. pdp11d pings every WebSocket every 20 s, so the usual 60 s proxy
timeouts are fine. Set `PDP11_TRUSTED_PROXIES` to the proxy's address, so
that the failed-login limit and the log take the client's address from
`X-Forwarded-For`; without it every client counts as the proxy. Without a
password, list the public host name in `PDP11_HOSTS`.

nginx, at `https://example.com/pdp11/`:

```nginx
location = /pdp11 { return 301 /pdp11/; }
location /pdp11/ {
    proxy_pass http://127.0.0.1:8080/;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header Host $http_host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-Prefix /pdp11;
}
```

`$http_host` is the `Host` the browser sent, port included; nginx's `$host`
drops the port, and behind a port other than 443 the WebSocket is then
refused.

Caddy (WebSockets and `X-Forwarded-For` work out of the box):

```
pdp11.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

## Troubleshooting

* **Logs**: `docker compose logs -f` or `docker logs -f pdp11`; native:
  `journalctl -u pdp11d -f`. `PDP11_LOG=debug` logs more. The simulator's own
  output (and every command pdp11d sends it) is in Help → Simulator log and at
  `GET api/log?lines=500`; power-on errors also appear as a message with a
  link to it.
* **The page does not load**: is the port published and free (`ss -ltnp |
  grep 8080`)? Change it with `PDP11_HTTP_PORT` in `.env` (compose),
  `install.sh --port N` or `PDP11_LISTEN`. pdp11d also needs the localhost
  console port (`PDP11_PORT_BASE`, 23400) for itself.
* **Two instances**: two pdp11d on one host need different ports
  (`PDP11_LISTEN`, `PDP11_PORT_BASE`), different `PDP11_SHM` files and
  different `PDP11_DATA` directories. One data directory (or Docker volume)
  serves one instance: a second pdp11d on it stops with "another pdp11d (pid
  N) is using the data directory ...".
* **"the host name ... is not allowed while no password is set"** (HTTP 421):
  open the UI by its IP address, add the name to `PDP11_HOSTS`, or set
  `PDP11_PASSWORD` ([Security](#security)).
* **"origin not allowed"** (WebSocket refused behind a proxy): pass the `Host`
  header with its port (nginx: `$http_host`) or set `PDP11_ALLOW_ORIGINS`.
* **Boot hangs**: if a system does not reach its prompt within its boot
  timeout (30-90 s), every viewer gets a warning; check the console, then power
  off and boot again.
* **fsck after an unclean stop**: pulling the plug (or a stop timeout that is
  too short, e.g. `docker stop` without `-t 90`) leaves the guest's file
  systems unclean. 2.11BSD then repairs them with fsck at the next boot and
  reboots itself (about 20 s extra); UNIX V5-V7 may lose recent writes. Use "Shut
  down OS", `docker stop -t 90` or `systemctl stop pdp11d`. "Factory reset" in
  the system chooser restores a broken system's disks.
* **Downloads fail**: the error names every URL tried. Download the archive by
  hand and drop it into `$PDP11_DATA/import/`, or build the image with
  `--build-context archives=DIR`.
* **Permission denied on `/data`** with a bind mount: `chown -R 10001:10001 DIR`.
* **Power-on fails with "PANEL: ... is open to other users (mode 0644; it
  must be 0600)"**: the panel export (`PDP11_SHM`) must be a regular file of
  the user pdp11d runs as, with mode 0600, on a POSIX file system such as
  tmpfs. The simulator creates it that way, but refuses any other file at that
  path and leaves it untouched: "PANEL: ... belongs to uid N, not to this user
  (uid M)", "PANEL: ... is not a regular file", "PANEL: can't open ...: Too
  many levels of symbolic links" (pdp11d checks it again: the same texts after
  "shm:", or "shm: ... is a symbolic link; the panel export must be a regular
  file"). Delete the file, or point `PDP11_SHM` elsewhere.
* **Native service does not start**: `install.sh` says so when
  `pdp11d.service` fails right after the start; `journalctl -u pdp11d -n 50`
  shows why (a port in use, a data directory that another pdp11d uses, a bad
  setting). Status 226/NAMESPACE in an LXC container: sandboxing options
  (`ProtectSystem=`, `PrivateTmp=`, ...) were added in a drop-in under
  `/etc/systemd/system/pdp11d.service.d/`; remove them and run `systemctl
  daemon-reload`.

## Development

```sh
make build            # bin/pdp11 (simh/build.sh, pinned Open SIMH + patches) and bin/pdp11d
make test             # go vet, gofmt, go test ./..., catalog check, harness and pdp11d-run tests, UI tests
make run              # pdp11d on 127.0.0.1:8081, UI read live from web/, data in ./data
make test-simh        # the SimH patch test suite (quick mode)
make test-e2e         # browser tests against the real stack (Node, playwright-core)
make docker           # pdp11emu:latest; docker-arm64, dist, dist-arm64, clean: see "make help"
```

Requirements: Go (see `go.mod`), a C compiler, GNU make, git, Python 3 for the
harness tests, Node 20+ for the UI tools.

Tests that boot an operating system from its archive (the SimH suite's UNIX
V6 boot, the Go tests with the real simulator) read the archives from
`PDP11_TEST_CACHE`: a directory of catalog archives named by their sha256, as
`pdp11d fetch --cache DIR` writes it (`make test PDP11_TEST_CACHE=DIR`).
Without it, or without the archive they need, they are skipped and say so.

* **UI** (`web/`): plain ES modules, no build step; with `make run` (or
  `PDP11_WEB=web pdp11d serve`) changes show on reload. `web/dev/` has a mock
  server that speaks the protocol with a fake machine (`cd web/dev && npm
  install && node mock-server.mjs --help`) and the headless-Chromium UI tests
  (`npm test`; playwright-core from `node_modules` or `$PLAYWRIGHT_CORE`). See
  [web/dev/README.md](web/dev/README.md).
* **Simulator** (`simh/`): [simh/README.md](simh/README.md) describes the
  patches, the build and the test suite (`python3 -I simh/test/run_tests.py`).
* **Catalog** (`systems/`): one directory per OS ([systems/README.md](systems/README.md));
  `go run ./server/cmd/catalogcheck systems` validates it and
  `python3 -I tests/systems/bootmatrix.py` boots every system the way pdp11d
  does ([tests/systems/README.md](tests/systems/README.md)).
* **End-to-end tests** against the real stack: `make test-e2e` drives the UI
  in headless Chromium against pdp11d, the patched SimH and 2.11BSD
  ([tests/e2e/README.md](tests/e2e/README.md)); `go test -tags e2e -run E2E
  ./server/internal/web/ -args -bsd211 "$PWD/2.11BSD_rq.dsk"` runs the
  toggle-in and 2.11BSD scenarios at the protocol level (the test runs in its
  package directory: give `-bsd211` as an absolute path; `-simh` defaults to
  `simh/out/pdp11`).

## Architecture

```
 browser(s) ──HTTP + WebSocket──►  pdp11d  (Go, single static binary: UI, panel logic, catalog)
                                     │  stdin/stdout pipes ...... SimH command channel
                                     │  TCP 127.0.0.1:23400 ..... DL11 console (raw)
                                     │  mmap PDP11_SHM .......... front-panel export
                                     ▼
                                  pdp11  (Open SIMH at a pinned commit + our patches)
                                     └─ optional PDP11_TTY_TELNET listener (DZ11 lines)
```

* `pdp11d` starts the simulator, drives it over its command line on pipes,
  connects to the console on a localhost TCP port and maps the shared-memory
  file the patched simulator writes its panel samples to. It turns the samples
  into 64 lamp intensities per frame, implements the console switches and the
  power and boot policy, installs systems from the catalog and serves the UI,
  a REST API and one WebSocket per browser window.
* The browser renders the panel as one SVG with 64 lamps whose brightness is
  smoothed per lamp, and the console with xterm.js.

Contracts and details: [docs/DESIGN.md](docs/DESIGN.md) (architecture,
lifecycle, configuration), [docs/PROTOCOL.md](docs/PROTOCOL.md) (browser ↔
pdp11d), [docs/PANEL_LOGIC.md](docs/PANEL_LOGIC.md) (console behaviour and lamp
engine), [docs/SIMH_PATCH.md](docs/SIMH_PATCH.md) (the panel export),
[docs/UI.md](docs/UI.md) (the web UI), [docs/PROXMOX.md](docs/PROXMOX.md).

## Licence

MIT, see [LICENSE](LICENSE). Open SIMH, xterm.js, the fonts and the Go modules
keep their own licences, and the operating systems are under theirs: see
[THIRD_PARTY.md](THIRD_PARTY.md).
