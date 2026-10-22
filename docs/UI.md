# Web UI contract (`web/`)

Plain ES modules, no build step, no external network requests (xterm.js and
fonts are vendored under `web/assets/vendor` and `web/assets/fonts` with their
licences). All URLs are relative. `index.html` carries
`<meta name="pdp11-version" content="…">` (pdp11d substitutes `{{VERSION}}`; the
UI reloads when `hello.server` differs, at most once per version per browser
session, then it shows a toast). Visual reference: research r4
(`docs/research/r4-visual-spec.md`, element table
`docs/research/r4-assets/panel1170_elements.json`, photos under the research
scratch dir `r4-visual/shots/`). Protocol: `PROTOCOL.md`.

## 1. Views and layout

* `/` main view: toolbar, front panel, console. `?view=panel`, `?view=console`
  (pop-outs), `&chrome=0` hides toolbars (kiosk).
* The main view fits the viewport **without page scrolling** at 1366×768,
  1920×1080 and 2560×1440: side-by-side (panel left, console right) when the
  viewport is wide enough for both at a useful size, otherwise stacked (panel
  above, scaled to fit, console below). The console's rows are at least 13 px
  high (less only where the window is too narrow for that, e.g. 132 columns in
  a 720 px window): when no arrangement scores, the stack keeps 24 such rows for
  the console (the font's cell height is rounded to device pixels, so at a
  device pixel ratio of 1.25 or 1.5 that takes a few px more than 24 × 13) and
  gives the panel the rest. Phones, and windows where that panel
  would be narrower than 360 px: stacked, scrolling allowed, console rows of at
  least 12 px, landscape hint for the panel; in landscape the panel fits the
  viewport height (the toolbar scrolls away), so all of it can be seen at once.
* Pop-out buttons open `window.open('?view=panel', 'pdp11-panel', 'popup,width=…')`
  from the click handler (show a hint if blocked). Each window has its own
  WebSocket; UI preferences sync over `BroadcastChannel('pdp11-ui')`; the main
  window shows "Panel is in another window — [Bring back]" in its place: a
  200 px wide notice beside the remaining view, which is laid out in the rest
  of the width (every frame and hit area stays inside the window).
  Without `BroadcastChannel` pop-outs work but neither sync nor coordinate
  "Bring back".
* Dark theme tokens (r4 §6): page `#0d0e10`, surface `#16181b`, border `#2a2d31`,
  text `#d6d9de`, muted `#8b9098`, focus `#66b3ff`, danger `#ff5a4a`. The panel keeps
  its physical colours.
* `prefers-reduced-motion`: the "reduced" lamp style by default, no paddle,
  knob or key transitions, no toast or ready-banner entrance animation, no
  pulsing status dot, a still (full, dimmed) bar for indeterminate progress,
  and a steady console cursor.

## 2. Toolbar and dialogs

* Toolbar: current system (opens the system chooser), power (mirrors the key:
  OFF ↔ POWER, with confirmation), **Boot**, **Shut down OS** (disabled unless
  the CPU runs and the system has `canShutdown`), speed (fast/authentic), lamp
  style (LED, PiDP-11, incandescent, reduced motion), console theme, pop-out
  panel, pop-out console, help, status pill (run state, busy operation, ready,
  guest instruction rate, viewers, connection; "not started" while the key is
  at POWER but nothing runs, e.g. `PDP11_AUTOSTART=false`). The rate comes from
  `stats.mips` ("12.4 M instr/s", "980 k instr/s") and has a tooltip: the
  instructions the emulated PDP-11 executed per second, time idling in WAIT not
  counted, so an idle system shows a low rate; not a measure of the host's speed.
  The pill is not a live region (the rate changes about once a second); a
  visually hidden one announces changes of the run state, busy operation,
  readiness and connection ("Running, ready"; "Disconnected from pdp11d" once
  per outage, not at every reconnect attempt).
* **Power off / boot while running / switching systems** confirm with: "Shut
  down the OS first (recommended)" (sends `shutdownOS`; shows the system's
  shutdown text), "Pull the plug", "Cancel". For a system without a shutdown
  script (`canShutdown` false, RT-11) only "Pull the plug" and "Cancel", with the
  system's `shutdown` text (what to do on the console before switching off).
  The same applies while the CPU is **halted with the OS still in memory**
  (`state.ready` and a last stop other than the OS's own `HALT instruction`,
  e.g. a HALT or a single step from the panel): the dialog says the OS is still
  in memory and how to shut it down (ENABLE, CONT, then Shut down OS), Cancel
  has the focus, and the clean way is "Continue and shut down" (latch ENABLE,
  CONT, wait until the CPU runs, then `shutdownOS`; not at LOCK, not without
  `canShutdown`). A CPU that powered up halted, or an OS that halted itself,
  gets the plain confirmation.
* **Shut down OS** progress (`progress` task `shutdown`): one info toast that
  updates its text in place with each step (the server's texts, e.g. "Typing
  “root”", "Waiting for the system to halt…"), no ×N counter; the final
  message replaces it ("The OS has shut down; power is off.", or the server's
  `warn`/`info` toast that carries the request's `req`). If the connection
  drops first, the toast goes away like any info toast.
* **Toasts**: long words (URLs, sha256 sums, paths) wrap and a long text
  scrolls (at most 30 % of the window height), so the action and dismiss
  buttons stay inside the window; at most five are shown, and the oldest give
  way while the stack would be taller than the window.
* **System chooser**: cards (name, description, download/disk size, licence class
  badge, installed state, credentials). Actions: select, install (licence text
  must be accepted for class `hobbyist`), factory reset (confirm: discards
  changes on that system's disks), cancel install. Progress bars from `progress`.
* **First run**: while the default system installs and boots, an overlay shows
  progress ("Downloading 2.11BSD… 12 / 24 MB", "Booting…") until the system is
  ready or its `bootTimeoutSec` has passed (the server then warns with a toast,
  `PANEL_LOGIC.md` §3). Screen readers hear its title and each new phase, not
  the byte counts.
* **Ready banner**: when `state.ready` turns true, show for ~15 s (dismissable,
  re-openable from help): "2.11BSD is up — log in as **root** (no password)" +
  `console.keyHelp`. It keeps clear of the console text where it can: below
  the console frame when the page has room there (desktop side-by-side
  layouts), else over the half of the terminal away from the cursor, so never
  over the prompt; it gives way to the first key typed into the console.
  Systems with `noLogin` (RT-11,
  RSX-11M-PLUS) get "RT-11 is up — the console is ready (no login needed)": the
  UI never tells the user to log in on them. `needsInstall` opens the
  licence/install dialog.
  `hello.insecure` shows a persistent dismissable banner: "No password set:
  anyone on your network can control this machine".
* **Help**: system card (notes markdown — headings, lists, code — credentials or
  "No login needed", shutdown, key help, licence); a panel guide numbering and
  labelling every control; keyboard shortcuts; a **toggle-in tutorial** (§6); a
  **Simulator log** tab: the `log` channel (subscribed only while the tab is
  shown: history, then live lines), read-only, following the end unless the
  user scrolled up, with Copy. The toast for `state.run` = `error` offers a
  "Simulator log" button that opens it. Server hints for ignored switches
  arrive as toasts.
* Optional **octal readouts** under the panel: SR, ADDRESS, DATA (accessibility).
* Branding (digital logo, pdp11/70 badge) on by default; setting to hide it.

## 3. Front panel rendering (normative)

* One static SVG (`viewBox="-10.1 -9.9 484.4 260.4"`, millimetres, r4 frame) for
  bezel, window, boxes, colour bars with the three DATA notches, tiles, labels,
  logo, unlit lamp lenses. Geometry comes from `web/assets/panel/geometry.json`
  (generated by `web/dev/gen-geometry.mjs` from the r4 element table; lamp id
  `DATA_SPACE` = protocol lamp 48 `DATA`).
* **Labels**: Archivo at 85 % width (instanced at weight 600 for labels, 500 for
  the tile numbers, subset), every label sized with `textLength` = measured ink
  width from the element table. The table's scan-based cap heights are ~6 %
  larger than in the photo, so labels are drawn at 0.94 of the listed cap height
  (`labelFont.capScale` in geometry.json). The single-digit switch-tile numbers
  (all listed as 3.5 mm wide) keep the font's natural width. The bezel
  lettering ("digital" and the tagline) is Montserrat Alternates converted to SVG
  paths at authoring time (`web/dev/build-fonts.py`).
* **Lamps**: 64 absolutely positioned HTML elements over the SVG (positions in %),
  `border-radius: 50%`, only `opacity` animates (`will-change: opacity`). Lit: disc
  Ø 5.2 mm centred on the lamp centre, radial gradient `#ff3b2a` (centre) →
  `#e8001a` (60 %) → `#8a0010` (rim), specular dot Ø 1 mm at (−1.2, −1.2) mm, 40 %
  white; halo at most 1.5 mm beyond the disc, alpha ≤ 0.3. Unlit (in the SVG): window
  `#1a1011` and a dim lens disc `#241214` Ø 5.2 mm. Never light the whole square
  window; no CSS filters per lamp.
* **Brightness**: per-lamp exponential smoothing with real `dt` in a
  `requestAnimationFrame` loop (`PANEL_LOGIC.md` §5 presets), opacity = `b^(1/2.2)`,
  0 while the linear brightness `b` < 1/64; stop the loop when settled; keep only
  the newest frame.
* **Paddles** (SVG paths, 3 colours — rose `#cf2647`, magenta `#6e2a55`, cream
  `#e8e7e1` — × 3 poses): REST "bow-tie": the upper face narrows from 13.5 mm at the
  top to 9.9 mm at the crease (y 205.4), the lower face widens back to 13.5 mm; the
  waist is formed by lens-shaped darker side facets running from 20 % to 78 % of
  the paddle height, deepest 1.5 mm above the crease, the deeper facet on the side
  facing the panel centre (as on the real row; this reads like the photo better
  than r4's ~10.3 mm); V-notches between neighbours; a gloss stripe on the bright
  upper face; darker lower face. BACK (SR = 1, DEP or LAMP TEST raised): almost
  uniformly dark face (rose `#3a0a12`, magenta `#1c0b14`) with a 1.2 mm bright lip
  at the top. FWD (pressed LOAD ADRS/EXAM/CONT/START, HALT, S BUS CYCLE): flat
  bright face (rose `#ef3c5b`, magenta `#8f5079`) with a small bottom specular.
  Pose changes animate 60-90 ms; momentary poses are held ≥ 120 ms. Hover/focus
  highlight on each paddle.
* **Knobs**: black skirt Ø 25 mm, raised bar with white pointer, rotate by
  `transform` (80-120 ms); detents at 22.5° + 45°·k. **Key**: chrome cylinder + bow;
  OFF 225°, POWER 315°, LOCK 45°.
* Acceptance: a 1340 px wide screenshot overlaid on r4's `bb_composite_1340.png`
  matches positions (lamps within about 0.5 mm); lamps read as small round red
  LEDs; paddles read as the real bow-tie row.

## 4. Panel interaction

* Pointer Events (mouse/touch/pen), `touch-action: none` on controls only, no text
  selection or context menu on controls. Hit area of a paddle = its slot
  (14.31 mm) × paddle + tile.
* SR paddle: click toggles; drag across paddles paints the value of the first
  paddle's new state; vertical flick sets up/down explicitly. Send the whole `sr`.
  The switch register is shared: a window keeps its own unconfirmed toggles
  (≤ 1.5 s) as bits to set and clear on top of the server's `sr`, so switches
  another viewer flips meanwhile show at once; when the server reports a value
  this window never sent (another viewer wrote the register from an older
  view), it sends its unconfirmed bits again on top of it.
* Momentary paddles: pointerdown → `active: true` (pointer capture), release on
  pointerup/cancel/lostpointercapture, window blur and `visibilitychange`.
* Knobs: click right half = clockwise, left half = counter-clockwise; wheel
  (accumulate ≥ 60 px per detent); drag around the centre snaps to detents.
  Key: click right/left = cw/ccw, no wrap; OFF needs confirmation.
* Keyboard (only while the panel has focus; Tab moves between the switch row,
  the knobs and the key, each with a visible focus ring; roving focus over the
  paddles): ←/→ move (Home/End: first/last), ↑ = 1/raise, ↓ = 0/press,
  Space/Enter toggle or press; arrows turn a focused knob or the key; `0`-`7`
  shift an octal digit into SR, Backspace shifts right, Esc clears SR, `L` LOAD
  ADRS, `E` EXAM, `D` DEP, `C` CONT, `S` START, `H` ENABLE/HALT, `B` S INST/S BUS,
  `T` LAMP TEST (hold), `[`/`]` ADDRESS knob, `{`/`}` DATA knob, `K`/Shift+`K` key
  cw/ccw (OFF asks first).
* ARIA: SR paddles `role="switch"` + `aria-checked`; momentary `role="button"`;
  knobs/key `role="slider"` with `aria-valuetext`; lamps `aria-hidden`; an optional
  ≤ 1 Hz text readout.
* The UI renders the server's `state.controls` (optimistic rendering until the echo).

## 5. Console

* xterm.js 6.0.0, DOM renderer, fixed 80×24 (option 132 columns); no fill mode,
  so `addon-fit` is not vendored. Font **Glass TTY VT220** at the largest integer
  multiple of 20 px that fits, with `lineHeight: 20/19`; otherwise VT323 (vendored)
  for non-integer scaling, then a local IBM Plex Mono / `ui-monospace`. xterm.js 6
  draws its scrollbar over the text area, which would hide the last column of a
  fixed-width screen: the console reserves a 14 px gutter for it. Phosphor themes:
  green `#33ff66` on `#041208`, amber `#ffb000` on `#140b00`, white `#e6edf3` on
  `#0b0d10` (opaque backgrounds). Optional CRT effect (vignette overlay, text
  glow), never curvature or flicker.
* Focus: "click to type" overlay when unfocused; keys typed while focused go to
  the PDP-11 only; clicking the panel does not steal focus permanently (a click
  on the panel leaves it in the console). **Shift+Tab leaves the console** (the
  browser moves the focus back to the console header; Tab itself goes to the
  PDP-11, whose terminals had no back-tab); F6 moves it to the browser's address
  bar. Help says both. A **screen reader mode** preference (view settings) sets
  xterm.js's `screenReaderMode` (the console text in an accessibility tree).
  With `prefers-reduced-motion` the cursor does not blink.
* Key mapping from the selected system's `console` (`backspace`: send DEL `\x7f`,
  BS `\x08` or `#`; `interrupt`: Ctrl+C sends `\x03` or DEL) with a visible
  "authentic keys" toggle (raw xterm behaviour) and `keyHelp` in the console header.
  Backspace with any modifier (Ctrl, Alt/Option, Shift, Cmd) and Delete send the
  erase character too, never DEL on a system whose interrupt it is.
* Copy: selection + Ctrl+Shift+C (Cmd+C on macOS) and a right-click menu, using the
  DOM copy event / `document.execCommand('copy')` (works over plain HTTP; never rely
  on `navigator.clipboard`). Paste: Ctrl+Shift+V / browser paste, split into
  ≤ 4 KiB messages, at most 8 KiB queued at the server, the rest held in the
  browser. Keys typed during a paste (mapped keys and Send key included) are
  appended to it, so they reach the guest after the pasted text; the system's
  interrupt key (Ctrl+C sending ^C or DEL) instead cancels the rest of the paste
  (`inputCancel`) and is then sent. The console header shows "Pasting… n bytes
  [Cancel]" only in the window that pastes: its held bytes plus an estimate of
  its bytes in `state.console.inputQueued` (at most what it sent, at most the
  queue plus what it sent in the last 0.5 s, which the queue may not count
  yet). Input the server holds while the CPU is not running shows as "n
  characters waiting until the CPU runs" without Cancel. `inputCancel` drops the
  whole queue, so Cancel asks first when the queue holds input that is not this
  paste. Ctrl+C always goes to the guest. "Send key" menu: ^C ^O ^Q ^S ^T ^U ^W
  ^Z DEL ESC (Chrome keeps Ctrl+T and Ctrl+W); choosing one leaves the focus in
  the terminal.
* Bell → brief visual flash of the console frame.
* **Output flow control**: console output is handed to xterm.js while less than
  64 KiB of it is unparsed (write callbacks); the rest waits in the browser.
  xterm.js cannot drop what it has queued, so this bounds what a slow client
  still has to parse when the connection drops.
* **History** (`PROTOCOL.md` §1): on every (re)subscribe `reset()` the terminal;
  the first `0x10` message after it is the history. Output still waiting in the
  browser is dropped, and the reset waits until xterm.js has parsed what it
  already had, so no old output appears after it. While xterm.js parses the
  history the reply gate is closed: reply-like sequences xterm.js generates are
  dropped, ordinary keys typed meanwhile (cursor and function keys included, the
  mapped Backspace and Ctrl+C, Send key and pastes too) are kept and sent in the
  order typed when the gate opens — when the history's write callback runs, at
  the latest after 15 s; a newer history (quick reconnect) keeps it closed until
  that one is parsed. Live output written after the history is parsed with the
  gate open (xterm.js parses writes in order). An empty history does not close
  the gate.
* **Terminal query replies** (`PROTOCOL.md` §1.4): a client answers only when it
  is the answerer, when the answerer is unknown (no `state` yet, or 0), or when
  it typed within the last 1.5 s (the server makes it the answerer; its state may
  lag); never while the gate is closed. This covers DA, DSR, DECRQM, XTVERSION,
  DECRQSS and the colour queries (OSC 4/10/11/12 with `?`; setting a colour is
  not suppressed), and any other reply-like data xterm.js sends; replies do not
  count as typing. Guests pad lines with DEL (2.11BSD); xterm.js's log message
  for each one is suppressed.

## 6. Toggle-in tutorial (help)

Verified program (on the patched SimH, reading DR from the export): a light
chaser on the DISPLAY REGISTER.

1. Key OFF (confirm), ENABLE/HALT → HALT, key POWER (the CPU powers up halted).
   Speed → authentic.
2. SR = 001000, LOAD ADRS.
3. For each word `012700 000001 010037 177570 000240 077101 006100 000772`: set SR to
   it and raise DEP (successive DEPs step +2).
4. SR = 001000, LOAD ADRS, ENABLE/HALT → ENABLE, START.
5. DATA knob → DISPLAY REGISTER: a single light walks across DATA.

At 001000: `MOV #1,R0`; `MOV R0,@#177570`; `NOP`; `SOB R1,.` (a 65536-pass
delay, R1 is 0 afterwards); `ROL R0`; `BR 001004`. The bit rotates through the
carry, so the display cycles through 17 states (DR 1, 2, 4 … 100000, 0) for good,
about 65 ms per step at authentic speed. (The NOP holds the place of an earlier
`CLR R1`, which cleared the carry: the light then walked across once and the
display stayed dark.) The tutorial shows each step with the current octal values
and may offer an "auto-toggle" button that performs the steps by sending the real
panel messages (visibly animating the paddles). The end-to-end tests
(`server/internal/web/e2e_test.go`, `tests/e2e/`) toggle in exactly this program.

## 7. Testing hooks

`window.pdp11Debug` exposes the last `state`, the last raw lamp frame, the
smoothed lamp values and a log of sent messages (for Playwright tests).
`web/dev/mock-server.mjs` (Node 20 + `ws`, dependencies under `web/dev/`) speaks
the protocol with synthetic lamps and a fake console for UI development, and
sends pdp11d's `Content-Security-Policy` and `X-Frame-Options` (`DESIGN.md` §10)
with every response; `web/dev/tests/` runs the UI tests in headless Chromium
against it (`npm test`). The UI runs under that policy: no inline scripts and
no eval (xterm.js's DOM renderer injects `<style>` elements, which is why it
allows `style-src 'unsafe-inline'`). Any CSP violation fails the UI tests and
the end-to-end tests.
