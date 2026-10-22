# Browser ↔ pdp11d protocol (version 1)

Same-origin. With `PDP11_PASSWORD` set every request needs Basic auth (any user
name) or the session cookie `pdp11d_session`, which every request
authenticated by valid Basic credentials receives (`api/status` included) and
which stays valid until pdp11d restarts (details: `DESIGN.md` §10). After a
restart a client whose WebSocket handshake is refused should fetch any URL
before reconnecting (the UI fetches `api/status` after every failed
handshake): the browser then re-sends its cached Basic credentials (or asks
the user) and receives a new cookie. Without a password every request must
name an allowed host in its `Host` header (an IP address, `localhost`, the
machine's name, `PDP11_HOSTS`: `DESIGN.md` §10), else HTTP 421 with a
plain-text explanation. **All numbers are plain binary values**; the UI
formats addresses and data in octal. Client messages are limited to 64 KiB
(the server's read limit); the UI splits pastes into ≤ 4 KiB messages.

## 1. WebSocket `GET ws?sub=<channels>` (relative URL)

`sub`: comma list (the parameter may repeat) of `panel`, `console`, `log`
(default `panel,console` when absent). Other names (`tty0`…`tty7` are reserved
for v2, like binary types `0x20`-`0x27`) are ignored with a `warn` toast.
WebSocket compression is not negotiated.

**Connect order**: `hello`, `state`, `systems`, then for each subscribed channel
(panel, console, log) its history, then live data. History is sent only when a
channel goes from unsubscribed to subscribed (on connect or by `subscribe`):
panel → the newest frame (if any); console → one history message (below); log →
one `log` message with the last ≤ 1000 lines (`LogBuffer.SubscribeHistory`, so
no line is lost or duplicated between history and live lines). A client
**resets its terminal** whenever it (re)subscribes to `console`, before writing
the history.

**Console history.** After every (re)subscription to `console` the first binary
`0x10` message the client receives is the complete history: always exactly one
message, possibly only the type byte, never merged with live output. It is the
end-of-history marker; every later `0x10` message is live output (consecutive
live output may be merged, ≤ 64 KiB per message).

**Reconnect**: on close the client reconnects with backoff 0.5 s → 5 s (×2, reset
by a `hello`) and re-sends its subscription via the query string. It reloads
the page if `hello.server` differs from the version the page was served with
(`<meta name="pdp11-version">`), at most once per version per browser session
(otherwise it toasts "reload the page"), so a stale cached page cannot loop. The
server pings (WebSocket ping frames) every 20 s and drops connections that miss
2 pings; the client sends `ping` every 20 s and closes (code 4000) when no `pong`
arrives within 10 s.

**Close codes** (server): `1001` going away (pdp11d stopping; new handshakes get
HTTP 503 meanwhile); `1008` "client too slow" when a client's send queue exceeds
4 MiB (best effort: a fully stalled peer may not receive the close frame, the
connection ends anyway; nothing is skipped silently, the client catches up
from the histories when it reconnects); `1009` message too big (> 64 KiB).
Handshake refusals: HTTP 401 (no or wrong credentials), 403 (foreign `Origin`,
`DESIGN.md` §10), 421 (host name not allowed while no password is set,
`DESIGN.md` §10), 429 (too many failed logins).

### 1.1 Binary messages (first byte = type)

| Dir | Type | Layout | Meaning |
|---|---|---|---|
| S→C | `0x01` | `01 VV SSSS L0 … L63` | panel frame: `VV` = 1, `SSSS` = u16 LE sequence counting delivered frames, 64 lamp bytes (linear duty 0..255). Sent at the frame rate while any lamp changed, at least once per second; per client the newest frame wins (a slow client skips frames, never console bytes). |
| S→C | `0x10` | `10 bytes…` | console output (raw guest bytes; 7-bit because of `set tto 7b`, passed through unchanged, including DEL padding); history rule above |
| C→S | `0x10` | `10 bytes…` | console input (empty messages ignored). While the machine is off the input is dropped with a `warn` toast (at most one per 2 s); a full input queue likewise. |

Other binary types from the client get an `error` toast. Lamp order: `0..21`
A0..A21 · `22..37` D0..D15 · `38` PARITY_LOW · `39` PARITY_HIGH · `40` PAR_ERR ·
`41` ADRS_ERR · `42` RUN · `43` PAUSE · `44` MASTER · `45` USER · `46` SUPER ·
`47` KERNEL · `48` DATA (D space; "DATA_SPACE" in the geometry file) · `49`
ADRS16 · `50` ADRS18 · `51` ADRS22 · `52` USER_D · `53` SUPER_D · `54` KERNEL_D ·
`55` CONS_PHY · `56` USER_I · `57` SUPER_I · `58` KERNEL_I · `59` PROG_PHY · `60`
DATA_PATHS · `61` BUS_REG · `62` UADRS · `63` DISPLAY_REG.

### 1.2 JSON server → client (`{"type": …}`)

```jsonc
{ "type": "hello", "protocol": 1, "server": "0.1.0+3f9a0c1d2e4b", "clientId": 7, "lamps": 64,
  "frameHz": 60, "auth": false, "insecure": true }   // insecure: no password on a non-loopback listener → show a banner

{ "type": "state",
  "power": "off" | "on",               // key != off
  "run": "off" | "starting" | "halted" | "running" | "stopping" | "error",
  "error": "",                         // LastError when run == "error" or the last power-on failed
  "system": "bsd211",                  // selected system id
  "running": "bsd211",                 // id the simulator runs, "" when off
  "speed": "fast" | "authentic",       // Systems.SpeedFor(selected)
  "ready": false,                      // readyPattern seen since the last boot
  "needsInstall": null,                // or { "id": "rt11", "class": "hobbyist" } when power-on needs an install the user must accept
  "controls": { "sr": 0, "enableHalt": "enable", "sinstSbus": "inst", "addrKnob": 0, "dataKnob": 1,
                "key": "power", "lampTest": false,
                "pressed": { "LOAD_ADRS": false, "EXAM": false, "DEP": false, "CONT": false, "START": false } },
  "console": { "addr": 0, "adrsErr": false, "lastStop": "HALT instruction", "pc": 2050,
               "busy": "",             // panel operation in flight (PANEL_LOGIC §1 lists the names)
               "answerer": 7,          // clientId that answers terminal queries (§1.4); 0 = none
               "inputQueued": 0 },     // console input bytes still queued (held while the CPU is not running)
  "stats": { "mips": 0.0,              // guest instructions per second / 10^6, WAIT not counted (DESIGN §6)
             "samplesPerSec": 0, "uptimeSec": 0 },
  "viewers": 1 }                       // connected WebSocket clients

{ "type": "systems", "selected": "bsd211", "list": [ SystemInfo… ] }

// SystemInfo (api.SystemInfo):
{ "id": "bsd211", "name": "…", "short": "2.11BSD", "category": "unix" | "dec", "description": "…",
  "order": 10, "default": true, "installed": true, "installing": false,
  "downloadBytes": 24201968, "diskBytes": 1000448512,
  "license": { "name": "…", "class": "redistributable" | "hobbyist", "summary": "…", "text": "…" },
  "credentials": [ { "user": "root", "password": "", "note": "…" } ],   // [] when none
  "noLogin": false,                    // true: the console needs no login (RT-11, RSX-11M-PLUS)
  "shutdown": "…", "canShutdown": true, // a shutdown script exists (shutdownOS)
  "notes": "markdown", "speed": "fast" | "authentic",
  "bootTimeoutSec": 90,                // boot-timeout warning (PANEL_LOGIC §3); catalog value, default 120
  "console": { "backspace": "del" | "bs" | "#", "interrupt": "ctrl-c" | "del", "keyHelp": "…" } }

{ "type": "progress", "task": "install" | "factoryReset" | "shutdown", "id": "rt11",
  "phase": "download" | "verify" | "extract" | "step" | "done" | "error" | "cancelled",
  "pct": 42.5, "bytes": 123, "total": 456, "text": "…", "req": "…" }

{ "type": "toast", "level": "info" | "warn" | "error", "text": "…", "req": "…" }

{ "type": "log", "lines": [ "…" ] }        // simulator output and "sim> <cmd>" lines (sub=log); history first, then live
{ "type": "pong" }
```

* `hello.server` is the pdp11d version, `+` and a build id: the first 12 hex
  digits of SHA-256 over the served UI files (`index.html` and `assets/…`,
  sorted by path; for each its path, a NUL byte and its content), computed
  at start. A version that already contains `+` gets nothing appended.
  `api/status` and the `index.html` meta carry the same string, so pages
  reload after any change of the UI files, even when the version stays the
  same (`pdp11d version` prints the version alone).
* `state` is sent on connect and whenever any field changes (coalesced, ≤ 20/s;
  `stats` changes alone ≤ 1/s; `inputQueued` is re-read 4 times a second).
* `systems` is sent on connect and whenever any entry changes (install state,
  speed override, selection), coalesced.
* `progress` goes to everyone: phase changes and final phases at once and in
  order, updates within a phase ≤ 10/s per task (newest wins). `req` (omitted
  when empty) is that of the request that started the task.
* Live `log` lines are batched (≤ 1000 per message).
* Toasts (`req` omitted when empty):

| Cause | Level | To |
|---|---|---|
| invalid message (unknown type, wrong field type or value, unknown system id) | `error` | sender, its `req` |
| ignored input — at once, or when it ran after waiting in the panel queue (`PANEL_LOGIC.md` §1) | `warn` | sender, the `req` of that input |
| `selectSystem`/`factoryReset` refused; install or reset already running | `warn` | sender, its `req` |
| install, factory reset or speed change failed (a cancel is no failure: the `cancelled` progress reports it, without a toast) | `error` | sender, its `req` |
| console input dropped (machine off, queue full); unsupported channels | `warn` | sender |
| machine errors (`EvError`: simulator failed, boot failed, …) | `error` | everyone |
| shutdown failed (`warn`) or cancelled by a HALT (`info`) | `warn`/`info` | everyone, the `shutdownOS` request's `req` |
| boot has not reached its prompt within `bootTimeoutSec` | `warn` | everyone (once per boot), the booting input's `req` |

### 1.3 JSON client → server

Every message may carry `"req": "<client correlation id>"`, echoed in resulting
`toast`/`progress`. Fields are type-checked (an integer is not a string, and
vice versa); anything invalid gets an `error` toast and is not executed.

```jsonc
{ "type": "subscribe", "sub": ["panel", "console"] }               // the complete new set
{ "type": "sr", "value": 4194303 }                                  // whole 22-bit switch register
{ "type": "momentary", "name": "LOAD_ADRS" | "EXAM" | "DEP" | "CONT" | "START" | "LAMP_TEST", "active": true }
{ "type": "latch", "name": "ENABLE_HALT", "value": "enable" | "halt" }
{ "type": "latch", "name": "SINST_SBUS", "value": "inst" | "bus" }
{ "type": "knob", "name": "addr" | "data", "pos": 0 }               // 0..7
{ "type": "key", "pos": "off" | "power" | "lock" }
{ "type": "boot" }                                                  // panel rules apply (PANEL_LOGIC §3)
{ "type": "shutdownOS" }                                            // clean OS shutdown, then power off
{ "type": "selectSystem", "id": "rt11" }                            // only while run ∈ {off, error}, key != lock, panel not busy
{ "type": "install", "id": "rt11", "acceptLicense": true }          // acceptLicense required for class hobbyist
{ "type": "factoryReset", "id": "rt11" }                            // the selected system: same guard as selectSystem
{ "type": "cancel", "task": "install" | "factoryReset", "id": "rt11" }
{ "type": "speed", "value": "fast" | "authentic" }                  // selected system; persisted (DESIGN §9)
{ "type": "inputCancel" }                                           // drop queued console input
{ "type": "ping" }
```

After `install`, `selectSystem` or `factoryReset` of the selected system
completes, the server asks the panel to `retryPower` (power on if the key is on).

### 1.4 Terminal query replies

xterm.js answers terminal queries (DA, DSR, cursor position) through the same
path as typed keys. With several viewers only one may answer: the
**answerer** is the client that most recently sent console input (while it
still shows the console), else the oldest console subscriber;
`state.console.answerer` names it (0 = none: no client shows the console).
Every other client suppresses replies (register CSI handlers for the query
final bytes `c`, `n` and the like that return true). While a client replays the
history, replies generated by it are dropped, but ordinary keys typed meanwhile
are kept and sent once the history has been parsed (`UI.md` §5). A client that
just typed may answer before the `state` naming it as the answerer arrives.

## 2. REST (JSON, `Cache-Control: no-store`)

| Method + path | Result |
|---|---|
| `GET api/status` | `{ "ok": true, "version": "…", "run": "…", "system": "…", "power": "…" }` (`version` as `hello.server`; also `HEAD`; public with `PDP11_HEALTH_PUBLIC`; `pdp11d health` uses it) |
| `GET api/systems` | the `systems` message |
| `GET api/state` | the `state` message |
| `GET api/log?lines=N` | `{ "lines": [ … ] }` (N = 1..10000, default 200; otherwise HTTP 400) |

## 3. Static UI

`/` main UI; `?view=panel` panel only; `?view=console` console only;
`&chrome=0` hides toolbars (kiosk). All URLs in the UI are relative (works under a
reverse-proxy sub-path). Only `/`, `/index.html` and `/assets/…` are served.
`index.html` is served with `Cache-Control: no-store` and carries the version
(`{{VERSION}}` replaced by the `hello.server` string, HTML-escaped); `assets/…`
with `Cache-Control: no-cache` + a strong ETag (content hash; `If-None-Match` →
304) and explicit content types (JavaScript modules as `text/javascript`). With
`PDP11_WEB` the files are read from disk on every request (the build id is
still computed once, at start). Every response carries the
`Content-Security-Policy` of `DESIGN.md` §10: the UI loads scripts only from
its own files (no inline scripts, `eval` or inline event handlers), may use
inline styles, `data:` and `blob:` images and `data:` fonts, and connects only
to its own origin and to WebSocket URLs (`ws:`, `wss:`; the UI opens only its
own `ws`); other sites may not frame it unless `PDP11_FRAME_ANCESTORS` allows
them.
