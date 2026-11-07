# Web UI contract (`web/`)

Plain ES modules, no build step, no external network requests (xterm.js and
fonts are vendored under `web/assets/vendor` and `web/assets/fonts` with their
licences). All URLs are relative. `index.html` carries
`<meta name="pdp11-version" content="…">` (pdp11d substitutes `{{VERSION}}`; the
UI reloads when `hello.server` differs, at most once per version per browser
session, then it shows a toast). Visual reference: research r4
(`docs/research/r4-visual-spec.md`, element table
`docs/research/r4-assets/panel1170_elements.json`; the photos and screenshots
it cites stayed in the research session's scratch directory and are not in
the repository). Protocol: `PROTOCOL.md`.

## 1. Views and layout

* `/` main view: toolbar, front panel, console. `?view=panel`, `?view=console`
  (pop-outs), `&chrome=0` hides toolbars (kiosk; a kiosk panel keeps a 12 px margin).
* The main view fits the viewport **without page scrolling** at 1366×768 and
  larger, also at device pixel ratios of 1.25, 1.5 and 2. It has two zones under
  the toolbar: the **machine** (panel and console) in a dark well, and the **dock**
  below it, a lighter surface that reaches the window's bottom.
  * Side by side (panel left, console right) when both fit at a useful size:
    the columns have the same height and share their top and bottom edges
    (within 1 px): the console frame takes the panel's height and the terminal
    sits in the middle of it. The console's font is chosen so that it fits that
    height (§5): `web/assets/js/ui/layout.js` scores every font of the console in
    each arrangement on the panel and terminal areas. The panel stays the larger
    column (at least 54 % of the width where a font allows it), so free height
    goes to the dock, not to a larger console that would shrink the panel.
  * Otherwise stacked (panel above, scaled to fit, console below), centred, and
    the frame takes the panel's width when that is wider than the terminal. The
    console's rows are at least 13 px high (less only where the window is too
    narrow for that, e.g. 132 columns in a 720 px window). A console much wider
    than the panel (132 columns on most desktops) keeps the panel at its left
    edge and has its header as a card beside the panel, reaching its right edge,
    so the panel gains the height of the header bar and all of it lines up.
  * The dock holds the views' headers and what helps to use them, in the columns
    of the machine above. Under the console: its header bar (§5), the login, the
    full key help and "Things to try" from the system's notes, which scrolls.
    Under the panel: a bar with the octal readouts' switch, the panel guide and
    the tutorial, how to work the panel with the mouse, its keyboard shortcuts,
    the system's description. What does not fit gives way, least important
    first; the bars always stay, and the key help then opens from a "Keys" button
    in the header bar. In a stacked layout the dock holds the console's header
    only. On very large windows (2200×1100 and up) the toolbar and the dock are
    25 % larger.
  * The free part of the dock is where transient things go, so they cover nothing
    that matters: see the toasts, the ready banner and the tutorial (§2, §6).
  * Phones, and windows where a stacked panel would be narrower than 360 px:
    stacked, scrolling allowed, with the header bar above the terminal and the
    notes below it, the landscape hint for the panel on a portrait screen. In
    landscape the panel fits the viewport height (the toolbar scrolls away), so all
    of it can be seen at once, and so do the console's 24 rows. All columns fit
    the width where rows of at least 12 px allow it, else the terminal scrolls
    sideways inside its frame. With a touch screen, toolbar and header buttons are
    36 px high, and on a small panel the knobs and the key take targets of about
    44 px (as far as the other knob and the paddles allow, without changing the
    art); the switches keep their slots, their neighbours touch.
* Pop-out buttons open `window.open('?view=panel', 'pdp11-panel', 'popup,width=…')`
  from the click handler (show a hint if blocked). Each window has its own
  WebSocket; UI preferences sync over `BroadcastChannel('pdp11-ui')`; the main
  window shows "The front panel is in another window." (or "The console is in
  another window.") with a [Bring back] button in its place: a
  200 px wide notice of the same height beside the remaining view, which is laid
  out in the rest of the width (every frame and hit area stays inside the
  window); the row is centred as a whole. With both views away the two notices
  sit side by side in the middle of the page.
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
  the CPU runs and the system has `canShutdown`), speed (fast/authentic; a radio
  group: one Tab stop, the arrow keys choose), lamp
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
  per outage, not at every reconnect attempt). It keeps to one row down to a
  760 px window: the brand and the pop-out buttons' labels go at 1100 px, the
  status pill keeps only the run state at 900 px, the separators and the system
  button's arrow go at 860 px, and the pill gives way before anything wraps. A
  phone has two tidy rows: the machine, then the view, with the pill taking what
  is left. The two pop-out icons differ: lamps and a prompt.
* **Power off / boot while running / switching systems** confirm with: "Shut
  down the OS first" (sends `shutdownOS`; recommended by the system's shutdown
  text above it, "Clean shutdown (recommended): …"), "Pull the plug", "Cancel".
  For a system without a shutdown script (`canShutdown` false, RT-11) only "Pull
  the plug" and "Cancel", with the system's `shutdown` text (what to do on the
  console before switching off).
  The same applies while the CPU is **halted with the OS still in memory**
  (`state.ready` and a last stop other than the OS's own `HALT instruction`,
  e.g. a HALT or a single step from the panel): the dialog says the OS is still
  in memory and how to shut it down (ENABLE, CONT, then Shut down OS), Cancel
  has the focus, and the clean way is "Continue and shut down" (latch ENABLE,
  CONT, wait until the CPU runs, then `shutdownOS`; not at LOCK, not without
  `canShutdown`). A CPU that powered up halted, or an OS that halted itself,
  gets the plain confirmation. The text fits the system: pulling the plug risks
  disk repairs on a system with a clean shutdown; one without it (RT-11 writes
  its directories at once) only risks the files a program is writing.
* **Dialogs**: a dialog body with more below its fold shows a fade over its
  lower edge and a "More below ↓" button that pages down (pointer only; the
  keyboard moves the focus or uses Page Down). A dialog over another one dims the
  page less. Closing gives the focus back to the button that opened it; its
  focus ring shows only for someone who navigates with the keyboard, not after a
  click (also for menus).
* **Shut down OS** progress (`progress` task `shutdown`): one info toast that
  updates its text in place with each step (the server's texts, e.g. "Typing
  “root”", "Waiting for the system to halt…"), no ×N counter; the final
  message replaces it ("The OS has shut down; power is off.", or the server's
  `warn`/`info` toast that carries the request's `req`). If the connection
  drops first, the toast goes away like any info toast.
* **Toasts**: long words (URLs, sha256 sums, paths) wrap and a long text
  scrolls (at most 30 % of the window height), so the action and dismiss
  buttons stay inside the window; at most five are shown, and the oldest give
  way while the stack would leave the window. They go to the first place the
  stack fits: the free part of the dock under the panel, then under the
  console, then over the dock's notes; in a wide stack the card beside the panel;
  in a stacked layout the margin beside the panel or the console; then the blank
  top right of the panel's bezel; then the terminal's top, below a ready banner
  there. On a phone: the top of the window
  (an on-screen keyboard covers the bottom). They never cover the ready banner
  or the header bar where one of these places fits. A server's error text
  shows as a sentence (first letter capitalised). Whatever cannot be sent because
  the connection is down (keys, a paste, a button) gets one toast, "Not connected
  to pdp11d: that was not sent. It reconnects by itself.", which goes away when
  the connection is back.
* **System chooser**: a wide dialog of cards in rows of equal height, which line
  up: name, licence class and state badges (Installed, Not installed,
  Installing…, Resetting…, Install failed, Selected), a description cut at six
  lines with "More", download/disk size, credentials and speed, buttons. Eight
  systems fit a 1366×768 window without scrolling. The main action comes first
  and looks like it: "Install" ("Licence and install" for class `hobbyist`, whose
  licence text must be accepted) on a system that is not installed, with a plain
  "Select" beside it (it is then installed when the machine powers on), "Select"
  on one that is. Choosing a system closes the chooser and leaves the keyboard in
  the console. "Factory reset" (confirm: discards changes on that system's disks)
  stands apart at the right; every installed system has it, and for the selected
  one, which may run, it is disabled with its reason beside it ("Power off first",
  "Turn the key first"). A disabled button keeps a dashed outline. At key LOCK a
  notice above the cards says why Select is disabled; Install still works. While
  a task runs its bar and text take the place of the sizes, so the card keeps its
  height, with a "Cancel" button; the text says what the task waits for
  (`PROTOCOL.md` §1.2).
* **A failed install or factory reset** (`TaskFailure`, `PROTOCOL.md` §1.2): the
  card moves to the top of the list, takes the whole row and says what happened
  and what to do, in plain words,
  with "Try again" and "Technical details" (the raw error, with Copy); no progress
  bar is left and the badge says "Install failed". This stays until the next
  attempt or until the user closes it. If the failed system is the selected one
  and not installed, the first-run card says the same in the console area ("Try
  again", "Other systems…"), without a toast; it comes into view on a phone and
  takes the focus from the empty console (the card stands before the terminal in
  the document, so Shift+Tab from the terminal reaches it; the focus goes back
  when it leaves). Otherwise, unless the chooser is open, a short toast ("UNIX V6
  could not be installed. …") with "What to do" leads to the card.
* **First run**: while the default system installs and boots, an overlay shows
  progress ("Downloading 2.11BSD… 12 / 24 MB", "Booting…") until the system is
  ready or its `bootTimeoutSec` has passed (the server then warns with a toast,
  `PANEL_LOGIC.md` §3). The install's card is centred on the empty terminal. The
  booting card is small and keeps clear of the boot messages like the ready
  banner: under the console's header bar when the page has room there, else over
  the half of the terminal away from the cursor. Screen readers hear its title
  and each new phase, not the byte counts.
* **Ready banner**: when `state.ready` turns true, show for ~15 s (dismissable,
  re-openable from help): "2.11BSD is up — log in as **root** (no password)" +
  `console.keyHelp`. It keeps clear of the console text where it can: under
  the console's header bar when the page has room there, in place of the login
  and key help it repeats (which come back when it goes), else over the half of
  the terminal away from the cursor, so never over the prompt; it gives way to
  the first key typed into the console.
  Systems with `noLogin` (RT-11,
  RSX-11M-PLUS) get "RT-11 is up — the console is ready (no login needed)": the
  UI never tells the user to log in on them. `needsInstall` opens the
  licence/install dialog; after a Cancel a card in the console area says the
  system is not installed ("Licence and install", "Other systems…"), and Boot
  opens the dialog again.
  `hello.insecure` shows a persistent dismissable banner: "No password set:
  anyone on your network can control this machine".
* **Help**: a wide dialog of the same size on every tab. System: notes markdown
  (headings, lists, code) beside the facts (credentials or "No login needed",
  key help, shutdown, licence, speed), one above the other on a narrow screen; a
  **panel guide**: the panel as on a running system (its lamps lit, a still
  picture), with a numbered marker for every control, in free space beside what it
  explains or on the shelf below the paddles, never on a label or lamp, with
  leader lines; the figure stays in view while the legend beside or under it
  scrolls, and pointing at a number or an entry marks its partner; keyboard
  shortcuts; a **toggle-in tutorial** tab (§6) with the steps, the program listing
  and the button that opens the tutorial; a
  **Simulator log** tab: the `log` channel (subscribed only while the tab is
  shown: history, then live lines), read-only, following the end unless the
  user scrolled up, with Copy. The toast for `state.run` = `error` offers a
  "Simulator log" button that opens it. Server hints for ignored switches
  arrive as toasts.
* Optional **octal readouts** under the panel: SR, ADDRESS, DATA (accessibility),
  as labelled numbers in the red of the lamps, large in the dock, a compact row
  directly under the panel in the other layouts. The dock's "Octal readouts"
  switch and the view settings set the same preference.
* Branding (digital logo, pdp11/70 badge) on by default; setting to hide it.

## 3. Front panel rendering (normative)

* One static SVG (`viewBox="-10.1 -9.9 484.4 260.4"`, millimetres, r4 frame) for
  the moulded bezel, logo plate, dark panel, the printed sheet on it (boxes, colour
  bars with the three DATA notches, tiles, labels, logo, unlit lamp windows) and
  the movable parts. Geometry comes from `web/assets/panel/geometry.json` (generated
  by `web/dev/gen-geometry.mjs` from the r4 element table and from measurements on
  the photo; lamp id `DATA_SPACE` = protocol lamp 48 `DATA`). The art is in
  `web/assets/js/panel/` (`svg.js`, `paddles.js`, `art.js`): vector shapes and
  gradients only, no bitmap and **no SVG or CSS filter**, so that nothing has to
  repaint with the lamps.
* The colours and shapes are sampled from the BlinkenBone photo of a real panel
  (`bb_composite_2950.png`, see Acceptance). Its light comes from the upper right:
  the cream goes from `#c8c5b9` on the left to `#f0efeb` on the right, shadows
  fall to the lower left, the top and right walls around the dark panel are in
  shade.
* **Bezel**: the moulding's outline stays 1 mm inside the viewBox with a darker
  rounded edge, so that it stays an object where a window cuts the view off
  (`?view=panel&chrome=0`). A chamfer along the top; a pebbled surface (a tile of
  small light and dark dots, fine on the lit cream, barely visible in the shade); a
  soft ridge down the left wall; the top and right walls, traced on the photo, in
  brown shade (`#4e443c` … `#2b231f` … `#544338`), lightening around the top left
  corner into the lit left wall; the dark panel (`#161115`, sheen towards the upper
  right) with its outline traced too. Below it, two lit **pockets**, cream slopes
  from the panel's rounded lower corners down to a **fold** (y 204.4 to 207.7) that
  runs out to the moulding's sides; the left one has a diagonal ridge. The paddle
  **recess** flares between them: on the left a lit slope and a dark wall, on the right
  a dark gap and a triangle reflecting the last paddle. The **shelf** below is the
  same cream in its own shadow (`#4e473e` … `#3f352b`), catching the light towards
  its lower edge (`#a59e92`) and in the lower right corner, with a small contact
  shadow under the paddles.
* **Logo plate**: no fill of its own, an edge pressed into the moulding: a dark line
  with a light one beside it on the sides away from the light, faint towards the
  light. Tone on tone lettering, each shape drawn twice: a dark edge to the lower
  left, the face a little lighter than the cream. "digital" stands in seven joined
  square tiles (pitch 6.65 mm), its letters are strokes drawn in `gen-geometry.mjs`
  (the double-storey g and a are in no font used here); the tagline is Montserrat
  Alternates converted to SVG paths at authoring time (`web/dev/build-fonts.py`),
  its words placed as measured on the photo. With branding off the plate, the
  lettering and the pdp11/70 badge are gone and the plain moulding remains: its top
  band is simply the cream face, as on a panel whose plate was never fitted, not an
  empty strip.
* **Printed sheet**: on the photo it lies a little rotated and stretched against the
  table's frame (`sheet`, an affine transform; up to 1.3 mm at the left end of the
  panel, where the lamps behind it stay on their grid). The black print
  takes half of the panel's sheen. The status row is one long box cut in four, without
  gaps between the lamps of a group, round only at the row's ends; the ADDRESS and
  DATA cells and PARITY have gaps and rounded bottoms per lamp. The tiles come in groups, each group
  rounded only at its outer corners. The DATA notches are black tabs with a flat top
  1.65 mm wide, straight flanks and wide flared bases (6.85 mm). The colours brighten
  to the right: rose `#7a273b` → `#96344c`, magenta `#4a2841` → `#633d5a` on the tiles.
* **Labels**: Archivo at 85 % width (instanced at weight 600 for labels, 500 for
  the tile numbers, subset), every label sized with `textLength` = measured ink
  width. The table's sizes come from a scan whose backlight bloom widens every
  glyph: labels are drawn 1.25 mm narrower and with a cap height 0.4 mm lower, which
  is what the photo measures. Positions per group as on the photo, which also opens
  the two-line labels (PAR ERR, ADRS ERR: line pitch 2.7 mm, cap 2.1 mm). The single-digit
  switch-tile numbers (all listed as 3.5 mm wide) keep the font's natural width. On
  **small panels** the labels up to 2.8 mm cap height grow around their own baseline
  and centre (left end beside the lamps of the knob boxes), a little bolder: × 1.2
  up to 1000 px of panel width, × 1.3 up to 640 px (container queries in `panel.css`).
  A wide label grows less sideways, so that it stays on its tile and off its
  neighbours, and the upper line of a two-line label moves up.
* **Lamps**: 64 absolutely positioned HTML elements over the SVG (positions in %),
  `border-radius: 50%`, only `opacity` animates (`will-change: opacity`). Lit: as on
  the photo a saturated disc Ø 5.2 mm centred on the lamp centre, `#ff001d` going to
  `#c80019` at the rim, a crisp edge; glow at most 1.5 mm beyond the disc, alpha ≤ 0.14. Unlit (in the SVG): just the flat window `#170f0d`, as on the
  photo. Never light the whole square window; no CSS filters per lamp.
* **Brightness**: per-lamp exponential smoothing with real `dt` in a
  `requestAnimationFrame` loop (`PANEL_LOGIC.md` §5 presets), opacity = `b^(1/2.2)`,
  0 while the linear brightness `b` < 1/64; stop the loop when settled; keep only
  the newest frame.
* **Paddles** (3 colours — rose, magenta, cream — × 3 poses), measured per paddle on
  the photo layers of all poses; on the photo the row runs a little wider than the
  printed columns above it (slot k spans 14.375 mm from x = 16.25 + 14.375 k), and
  the row brightens to the right. Between neighbours, black gaps.
  REST, the moulded bow-tie: the side edges run in to a waist at y ≈ 204 and out
  again. The camera looks into the side facing the middle of the row, so that side is
  cut deep (the insets add up to 5.15 mm: 0.25 and 4.9 mm at slot 0, 4.65 and 0.5 mm at
  slot 29), its wall a dark blade, up to the whole cut on the right of the row, and a
  gap of 0.24 mm stays between neighbours. A saturated upper face (rose
  `#b91e3d` … `#f65b73`, magenta `#5c1f44` … `#91597e`, cream `#eaebe6`) with a soft
  reflection, a rounded bend above the crease that ends in dark, on the right of the
  row with a white specular streak. The crease curves softly. The lower face goes from
  a grey-pink (rose `#5a3b3d` … `#765657`, magenta `#3c3233` … `#5f5558`, cream
  `#817d74`) to its dark rounded bottom end.
  BACK (SR = 1, DEP or LAMP TEST raised): seen from below, a roof, a bright band
  (rose `#fd3a5c`, magenta `#8c3b6a`), then a dark face (rose `#450912`, magenta `#17080f`)
  that narrows along a diagonal from the top corner and widens again, with a lit wedge
  and a lit fold beside the band. DEP has its own bright top and a red face, LAMP TEST
  a white top and a tan face (`#958172`).
  FWD (pressed LOAD ADRS, EXAM, CONT, START, HALT, S BUS CYCLE): the whole face comes
  forward into the light (rose `#ef3c5b`, magenta `#884a71`, `#9a6186` towards the
  right of the row), narrowing to a rolled lower end with a white streak, darker
  towards its right edge, with the recess's floor showing on the left.
  Pose changes animate 60-90 ms; momentary poses are held ≥ 120 ms. Hover and
  focus highlight on each paddle, shaped like its pose (white at alpha 0.2 and 0.12,
  on the cream paddle a dark tint).
* **Knobs**: a black dome Ø 22 mm, of which only the part towards the light stands
  out from its large soft shadow to the lower left; a raised bar with a thin white
  pointer, which rotates by `transform` (80-120 ms) about the pointer's pivot (`px`, `py`:
  the knob stands proud of the panel, so its top shows up and to the right of the
  dome's centre `cx`, `cy`, which is also the centre of its hit area);
  detents at 22.5° + 45°·k. The light depends on the position (`data-pos`, `panel.css`):
  the bar's long edge facing the upper right catches it, strongly when its axis is at
  112.5° (KERNEL D, SUPER I), which also shows a broad reflection on the dome.
  **Key**: a worn cylinder in a black escutcheon with a raised ring, OFF 225°, POWER
  315°, LOCK 45°. Standing proud of a panel seen from its middle it looks different by
  position (`data-angle`): at POWER the camera looks along the blade, a thin edge with
  a tan glint and the wide flat head; at OFF and LOCK at its flat side, stamped with
  lettering, bright towards the light, beside the cylinder. Its long end points at the
  position.
* Acceptance: a 1340 px wide screenshot overlaid on r4's BlinkenBone composite
  photo (`bb_composite_1340.png`, not in the repository;
  `web/dev/panel-preview.html?photo=<url>` draws a photo under the panel)
  matches positions (lamps within about 0.5 mm, as measured: mean 0.37 mm, the 15 of
  64 over it at most 0.93 mm, where lens reflections hide part of the disc; the labels
  within 0.2 mm); lamps read as small round red LEDs; paddles read as the real bow-tie
  row in all three poses.

## 4. Panel interaction

* Pointer Events (mouse/touch/pen), `touch-action: none` on controls only, no text
  selection or context menu on controls. Hit area of a paddle = its slot
  as photographed (14.375 mm, §3) × paddle + tile.
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
  shift an octal digit into SR, Backspace shifts right, Esc or Delete clears SR, `L` LOAD
  ADRS, `E` EXAM, `D` DEP, `C` CONT, `S` START, `H` ENABLE/HALT, `B` S INST/S BUS,
  `T` LAMP TEST (hold), `[`/`]` ADDRESS knob, `{`/`}` DATA knob, `K`/Shift+`K` key
  cw/ccw (OFF asks first).
* ARIA: SR paddles `role="switch"` + `aria-checked`; momentary `role="button"`;
  knobs/key `role="slider"` with `aria-valuetext`; lamps `aria-hidden`; an optional
  ≤ 1 Hz text readout.
* The UI renders the server's `state.controls` (optimistic rendering until the echo).

## 5. Console

* xterm.js 6.0.0, DOM renderer, fixed 80×24 (option 132 columns); no fill mode,
  so `addon-fit` is not vendored. One look at every size: font **Glass TTY VT220**,
  with `lineHeight: 20/19`, in the size the layout chooses (§1), from 14 px up.
  Multiples of 20 device px (20 CSS px at a device pixel ratio of 1, 16 at 1.25,
  20 at 2) put its strokes and scan-line gaps exactly on the pixel grid and
  multiples of 30 repeat them evenly (15 CSS px at 2, 20 at 1.5), and the layout
  prefers these among whole CSS sizes; between them the gaps are antialiased
  unevenly, where some of them come out dark (a "1" then reads as an "i"). As in a
  real tube the phosphor partly fills them: a dim copy of the text one scan line
  (1/20 em) lower (`text-shadow`), which also keeps the text about equally bright at
  every size and at every device pixel ratio. That copy and the glow take the
  colour of the text, so dim text stays dim and inverse video stays sharp.
  The terminal sits on whole device pixels. In a laptop's window (1366×768: 592 px
  for the console) 15 and 16 px sizes with one column pixel less (`letterSpacing:
  -1`, only up to a device pixel ratio of 1.25) stay taller than the 14 px size.
  VT323 (vendored), narrower and without gaps, remains only where Glass TTY cannot
  do it: windows too small for 14 px rows next to the panel (about 1060-1240 px wide
  and lower than about 700 px), 132 columns in a narrow window, and a phone's 80
  columns in about 370 px (VT323 12 px with tightened columns: rows of 12 px where
  Glass TTY would have 9 px), then a local IBM Plex Mono / `ui-monospace`. xterm.js 6 draws its scrollbar over the text area, which would
  hide the last column of a fixed-width screen: the console reserves a gutter for it
  (10 px, 4 px on a touch screen) with a slim thumb. Phosphor themes: green
  `#33ff66` on `#041208`, amber `#ffb000` on `#140b00`, white `#e6edf3` on
  `#0b0d10` (opaque backgrounds). Optional CRT effect (a light vignette that leaves
  the corners readable, text glow), never curvature or flicker.
* **Header** (in the dock, under the terminal; above it on a phone, where an
  on-screen keyboard covers what is below): a bar of fixed height with the title,
  the message slot, "Authentic keys" and "Send key", then the login and the full
  key help, each key in a `kbd`. Transient messages (paste progress, characters
  waiting) take the message slot and never move the frame or wrap the bar; in a
  narrow bar "Authentic keys" makes way for them. A mouse click on "Authentic keys"
  or on the octal readouts' switch leaves the keyboard in the console.
* Focus: the frame's border and glow light up in the phosphor's colour while
  it has the keyboard, and the header's slot says "Click to type", "Typing to the
  PDP-11" or, focused with the machine off or failed, "Machine off" (nothing covers the text; a kiosk without a header shows a "Click to
  type" pill at the frame's top right); keys typed while focused go to
  the PDP-11 only; a mouse click on the panel leaves the keyboard in the console or
  puts it there (typing goes to the PDP-11; the panel's shortcuts take Tab or
  Shift+Tab, a tap and a panel window without a console focus the control).
  **Shift+Tab leaves the console** (the
  browser moves the focus back to the console header, which stays before the
  terminal in the document though it is shown below it, and on to the panel's
  controls; the panel's bar in the dock comes before the panel in the same way; Tab
  itself goes to the PDP-11, whose terminals had no back-tab); F6 moves it to the browser's address
  bar. Help says both. A **screen reader mode** preference (view settings) sets
  xterm.js's `screenReaderMode` (the console text in an accessibility tree).
  With `prefers-reduced-motion` the cursor does not blink. Closing the right-click
  menu from the keyboard or choosing an entry leaves the focus in the terminal;
  in forced-colours mode (Windows high contrast) the console uses VT323 at any size
  (Glass TTY needs a text-shadow to fill its scan-line gaps, and that mode drops shadows)
  with a steady cursor in the system's colours; the lamps and status dots keep
  their colours (`forced-color-adjust: none`), since their colour is the message.
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
  ^Z DEL ESC (Chrome keeps Ctrl+T and Ctrl+W), each with its usual meaning (DEL and
  ^C by the system's key mapping; the menu says that the guest decides); choosing
  one leaves the focus in the terminal.
* Bell → brief visual flash of the console frame.
* **Output flow control**: console output is handed to xterm.js while less than
  64 KiB of it is unparsed (write callbacks); the rest waits in the browser,
  merged into 4 KiB blocks. xterm.js cannot drop what it has queued, so this
  bounds what a slow client still has to parse when the connection drops. More
  than 4 MiB waiting (a client that cannot keep up, e.g. a hidden tab under an
  output flood) is dropped: the client reconnects (close code 4001) and the
  server's history replaces it.
* **A system switch starts on a clear console** (`PROTOCOL.md` §1): the old
  system's text would stand around the new system's first messages, so the server
  drops it for every viewer; a reboot of the same system keeps it.
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
panel messages (visibly animating the paddles). The card sits in the dock under
the panel (a wide stack: under the header bar beside the panel) when that has room
(at least 150 px), its steps in columns; else it floats over the console, inside
its frame (on a phone: a sheet over the window's bottom), so the panel stays in view. It can be dragged by its header (a handle says so) and then stays
where it was put. The card takes the keyboard focus
when it opens; Esc closes it and gives the focus back. SimH throttles only about
2-3 s after START: until then the CPU runs flat out and every DATA lamp glows
dimly, then the single light walks; the last step says so. The end-to-end tests
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
