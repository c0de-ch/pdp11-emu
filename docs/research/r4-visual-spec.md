# r4 — Visual / UX reference: browser PDP‑11/70 front panel (SVG+CSS) + console

Researcher r4 (visual/appearance/interaction). Research phase only; nothing was written to the project repo.
Legend: **[V]** = VERIFIED (I measured, ran or read it, source cited) · **[U]** = UNVERIFIED (memory/inference).

`$SCRATCH` stands for the research session's scratch directory, an ephemeral working directory under `/tmp` that no longer exists.

Paths: `R4=$SCRATCH/r4-visual`
(downloads in `$R4/dl/*`, my scripts in `$R4/scripts`, screenshots in `$R4/shots`, machine-readable element
table `$R4/panel1170_elements.json`, traced logo `$R4/work/logo_pdp1170_path.svgfrag`, prototype SVG `$R4/work/proto_panel.svg`).

---------------------------------------------------------------------------------------------------------------

## 0. Key numbers (TL;DR)

| item | value |
|---|---|
| Coordinate frame | millimetres, origin = top‑left of the 464 × 186 mm acrylic indicator panel (DEC drawing via Chiappa) [V] |
| Full console (bezel) | x −10.1 … 474.3, y −9.9 … 250.5 → **484.4 × 260.4 mm, aspect 1.860** (19" rack ≈ 482.6 mm wide) [V] |
| SVG viewBox (full console) | `viewBox="-10.1 -9.9 484.4 260.4"` ; panel+paddles only: `viewBox="0 22 464 204"` |
| Column grid | 30 equal slots, **pitch 14.31 mm**, slot k centre `x = 25.25 + 14.31·k` (k=0 SR21 … 21 SR0, 22 LAMP TEST, 23 LOAD ADRS, 24 EXAM, 25 DEP, 26 CONT, 27 ENABLE/HALT, 28 S INST/S BUS CYCLE, 29 START) [V] |
| Row centres | status lamps y 65.75 · ADDRESS lamps 103.55 · DATA lamps 144.02 · switch tiles 170.70 · paddles 186.6–224.2 (crease 205.4) [V] |
| Counts | **64 lamps** (12 status + 22 ADDRESS + 16 DATA + 2 PARITY + 8 ADDRESS‑select + 4 DATA‑select), **30 paddles** (22 SR + unlabeled LAMP TEST + 7 control), 2 knobs (8 detents each), 1 key switch [V] |
| Colours (display palette) | panel `#1f141c`, black boxes `#0f0f13`, rose bar `#7e2639` / tile `#86293f`, magenta bar `#503049` / tile `#58304f`, ink `#ece6da`, rose paddle `#cf2647`, magenta paddle `#6e2a55`, blank paddle `#e8e7e1`, bezel `#dcd9d0` [V measured, values chosen] |
| Lamps | 11/70 consoles used **red LEDs** (early units bulbs) [V, two first-hand list posts]; lit core `#ffd2c0→#ff5a3c→#f0101e→#8a0012`, unlit lens `#241214→#120a0b` |
| Brightness model | duty‑cycle per lamp from backend (0…255 @30–60 Hz), client dt‑correct exponential smoothing; LED τ≈15 ms, bulb mode τ_rise 21.5 ms / τ_fall 68 ms (PiDP‑8/I ILS2) ; alpha = duty^(1/2.2) |
| Fast glow | static SVG art + **64 HTML lamp layers with `will-change:opacity`** → raster ≈0.4 ms per second vs 373 (SVG child opacity), 1559 (CSS drop-shadow), 199+164 (canvas) [V benchmark] |
| Console | xterm.js **@xterm/xterm 6.0.0** + addon‑fit 0.11.0 (+ addon‑webgl 0.19.0, addon‑attach 0.12.0), MIT; 80×24 default, 132×24 option; Glass TTY VT220 (Unlicense) @20 px with `lineHeight: 20/19` [V] |

---------------------------------------------------------------------------------------------------------------

## 1. Sources and method

| # | Source | What I used it for | Licence |
|---|---|---|---|
| S1 | J. Noel Chiappa, 300 dpi flat scan of an original 11/70 indicator panel `consoles/1170OrigCenter.jpg` (5144×2539 px, ruler) from `http://ana-3.lcs.mit.edu/~jnc/tech/pdp11/PDP-11_Stuff.html` ("older one, in magenta and rose colours") | **primary geometry + printed colours** (orthographic, calibrated: 11.811 px/mm) | none stated |
| S2 | Chiappa mechanical drawing `consoles/11'70FPanel.jpg` | panel outline 46.4 × 18.6 cm, key hole Ø1.98 cm, knob holes Ø1.35 cm, hole positions → frame origin | none stated |
| S3 | BlinkenBone (MIT), `git clone --depth 1 https://github.com/j-hoppe/BlinkenBone` @1ce5c06 (2019‑02‑03): `projects/09_javapanelsim/resources/blinkenbone/panelsim/panelsim1170/images/*` (photo layers 800…2950 px + `coordinates.csv`, source photo 5424 px wide) and `src/.../panelsim1170/Panel1170.java` | bezel/paddles/knob/key geometry, paddle poses, lit-lamp look, interaction logic, LED averaging | MIT |
| S4 | PiDP‑11 software `https://github.com/obsolescence/pidp11` @26d20be (2026‑04‑10); PiDP‑11 manual PDF (obsolescence.dev) | LED low-pass/PWM levels, SimH frontpanel API, UX conventions. **No panel artwork in the repo** (only icons `install/*.svg`, `IDLED_LIST.pdf`) [V] | — |
| S5 | Paul Nankervis JS emulator `https://skn.noip.me/pdp11/pdp11.html` (HTML/CSS read, page rendered) | how a web panel is drawn (CSS divs) | liberal (per author) |
| S6 | yaPDP `https://github.com/amesk/yaPDP` @dbdd5be (2026‑10‑21), MIT, live demo amesk.github.io/yaPDP | modern fork of S5: LED palette, lamp update strategy | MIT |
| S7 | PCjs 11/70 panel `https://www.pcjs.org/machines/dec/pdp11/1170/panel/` | stylised panel reference only | MIT |
| S8 | Wikimedia Commons: `File:Pdp-11-70-panel.jpg` (RCS/RI, CC BY‑SA 3.0), `File:PDP-11 70 Blinkenlights Console (15455641231).jpg` (W. Stief, CC0), `File:PiDP-11 (30280195895).jpg` (CC0) | colour/hue cross‑check | as listed |
| S9 | DEC *PDP‑11/70 Processor Handbook 1977‑78* (bitsavers), ch. 11 "Console Operation" (pdftotext lines ≈9600–9990) | switch semantics, LAMP TEST location, knob functions | — |
| S10 | PiDP‑8/I wiki "Incandescent Lamp Simulator" (tangentsoft.com) | incandescent rise/decay constants | — |
| S11 | npm registry (`npm view`, `npm pack`), GitHub API (xterm.js 6.0.0 release notes), svofski/glasstty, google/fonts | console stack versions + font licences | — |

Method [V]: (1) colour-class segmentation of S1 (numpy/scipy, `scripts/segment.py`, `segment2.py`, `build_table.py`) → 64
lamp windows, 27+4 tiles, 14 bars, text word boxes, black boxes; scan de‑rotated (slope −0.0050) and mapped to the panel frame
with `X = x + M·y + 17.5`, `Y = y − M·x − 1.4` (offsets from key hole, knob holes, centre screw hole and bottom edge;
uncertainty ±0.6 mm in X, ±0.3 mm in Y). (2) Affine fit BlinkenBone photo px → panel mm on all 64 lamps
(`scripts/bbfit.py`): `X = 0.170202x − 0.000427y − 19.013`, `Y = −0.000395x + 0.169181y − 24.500` (5.875 px/mm at 2950 px),
**residual mean 0.37 mm, max 0.71 mm** — two independent sources agree to sub‑mm. (3) Drawing vs scan: knob‑to‑key
horizontal distance 400.1 mm (scan) vs 399.25 mm (drawing); knob‑to‑knob 53.2 vs 53.3 mm. (4) A prototype SVG generated from
the table (`scripts/gen_panel_svg.py`) was rendered in Chromium and overlaid on the photo at 50 % (`shots/proto_overlay_on_photo_1200.png`):
lamps, bars, cells, tiles, boxes and labels coincide (labels after `textLength` fitting). Knob faces and outer paddles in the
photo are shifted ≈2–3 mm outward by parallax (they stand proud of the panel); the orthographic replica should use the hole/tile
centres given here.

---------------------------------------------------------------------------------------------------------------

## 2. Visual specification of the 11/70 front panel

### 2.1 Structure, top to bottom [V]
1. **Bezel** (off‑white "almond" moulded plastic), 484.4 × 260.4 mm, small outer radius (~4 mm).
2. **Top band** y −9.9…23.5 with a **raised rounded strip** x −3.4…300.1, y −2.7…16.5 (r≈3.5) carrying, tone‑on‑tone
   (debossed, no colour): the 7‑box `|d|i|g|i|t|a|l|` logo at x 15.9…62.3, y 0.2…13.7 (boxes ≈6.1 × 12.6 mm, pitch 6.63) and
   the tagline "digital equipment corporation • maynard, massachusetts" (geometric rounded lowercase, x 65.2…279.4).
   The top 23.5 mm of the acrylic panel (with its mounting holes) is hidden behind this band.
3. **Dark window** (smoked acrylic) x 0.4…463.6, y 23.5…186.0, top corners r≈6–8 mm, bottom corners r≈6 mm. Right inner bezel
   wall x 463.6…468.8 is in shadow in the photo (dark).
4. **Paddle bay** y 186.0…224.6, paddles x 18.5…447.0 (30 × 13.5 mm on 14.31 pitch, ≈0.8 mm gaps); cream cheeks at both ends.
5. **Ledge** y 224.2…245.6 (cream in shadow, photo `#46392f…#53473d`) and **front lip** y 245.6…250.5 (`#958c81…#aba398`).

### 2.2 Element order and grouping (left → right) [V]
* **Logo** "pdp11/70": x 18.5…80.0, y 53.6…72.2 (61.6 × 18.4 mm), left of the status row, above A21–A18. Outline letters
  "pdp" and "70" (white ≈0.5 mm stroke, hollow), **solid** bold "11" with flags, thin "/" slash rising above the cap line.
  Traced vector: `$R4/work/logo_pdp1170_path.svgfrag` (14.5 KB, fill‑rule evenodd; good stopgap, redraw cleanly from circles/rects for production).
* **Status row** (lamp centres y 65.75) occupies columns 10–21 (above A11…A0), 4 black boxes (y 54.4…72.3, r≈2.2):
  `[PAR ERR · ADRS ERR]  [RUN · PAUSE · MASTER]  [USER · SUPER · KERNEL]  [DATA · 16 · 18 · 22]`; "ADDRESSING" spans 16/18/22
  (centred over 18), the numerals are on label line 2. Labels: line‑1 baseline 57.3, line‑2 baseline 60.0, cap 2.5 mm, centred on the column.
  Boxes are 0.9–1.3 mm apart; inside a box each lamp sits in its own cell, cells separated by ≈0.8 mm gaps near the bottom (scalloped bottom edge).
* **ADDRESS row**: colour bars y 80.2…86.7 above black lamp cells y 86.7…110.1 (cells fused for ≈4 mm under the bar, then
  separated by ≈0.9–1.0 mm gaps, bottom corners r≈2.4); lamp windows 9.2 × 9.3 mm (r≈1.3) centred y 103.55 — i.e. in the
  *lower* part of a tall black "chimney". Bar groups (octal): `21 | 20‑18 | 17‑15 | 14‑12 | 11‑9 | 8‑6 | 5‑3 | 2‑0` coloured
  `rose | mag | rose | mag | rose | mag | rose | mag`; bar = columns ±6.47 mm (12.9 or 41.6 mm long); first bar has a rounded
  top‑left corner, last bar rounded top‑right, and carries **"ADDRESS"** right‑aligned (right edge x 329.6, baseline 84.8, cap 3.6).
  No bit numbers above the lamps.
* **DATA row**: bars y 120.6…127.2, cells 127.5…150.3, lamps y 144.02 under A15…A0 columns: `15 | 14‑12 | 11‑9 | 8‑6 | 5‑3 | 2‑0`
  (rose | mag | rose | mag | rose | mag), "DATA" right‑aligned in the last bar (right edge 329.5, baseline 125.5).
  **Three notches** cut up into the DATA bars at the D14|D13, D11|D10, D8|D7 boundaries, x = 132.9 / 175.7 / 218.6:
  a black tab rising 3.9 mm into the 6.6 mm bar, 4.6 mm wide at the base, 1.5 mm flat top, concave fillets
  (`shots/scan_zoom_data_notch_and_cells.png`). (D8|D7 separates FPP µ‑address bits 15‑8 from CPU µ‑address bits 7‑0, Handbook 11.8.7.)
* **PARITY box** x 61.4…88.9, y 120.4…150.3 (r≈2.4): "PARITY" (cap 3.6, centre x 75.3, baseline 125.4), "HIGH"/"LOW" (cap 3.2,
  baseline 129.9) over 2 lamps in the A18 / A17 columns (x 68.18 / 82.49, y 144.02).
* **Key switch** centre (32.2, 136.0): black bezel ring Ø≈19.2 (hole Ø19.8), chrome cylinder Ø≈11, flat chrome key bow
  ≈23 × 3.8 mm seen edge‑on. Labels (cap 2.6): POWER upper‑left (centre 25.6, baseline 122.9), LOCK upper‑right (42.2, 122.9),
  OFF lower‑left (23.4, 150.0). Key angle (blade direction, cw from 12 o'clock): **OFF 225°, POWER 315°, LOCK 45°**
  (`shots/bb_keyswitch_off_power_lock.png`).
* **ADDRESS‑select box** x 348.2…446.8, y 52.3…110.3 (r≈3) with 2 lamp columns x 358.7 / 390.5, rows y 65.70 / 77.15 / 88.50 / 100.15;
  labels left‑aligned at x 363.9 and 395.6 (cap 3.1): left `USER D, SUPER D, KERNEL D, CONS PHY`, right `USER I, SUPER I, KERNEL I, PROG PHY`.
  **Knob** centre (432.3, 82.9).
* **DATA‑select box** x 348.1…446.6, y 120.4…150.8: lamps rows y 130.55 / 141.80; labels `DATA PATHS` (2 lines), `BUS REG`,
  `µ ADRS FPP/CPU` (2 lines), `DISPLAY REGISTER` (2 lines). **Knob** centre (432.3, 136.1).
  The knob positions are **not printed around the knob**; the lamp next to the selected label lights (hard‑wired).
* **Switch tiles** (printed labels, 13.0 × 17.5 mm, r≈2, centre y 170.7): numbers 21…0 (cap 3.5, medium weight) coloured
  `21 rose | 20‑18 mag | 17‑15 rose | 14‑12 mag | 11‑9 rose | 8‑6 mag | 5‑3 rose | 2‑0 mag`; slot 22 has **no tile**;
  `LOAD ADRS rose, EXAM mag, DEP rose, CONT mag, ENABLE/HALT rose (two half tiles y 162.4…170.2 / 171.6…179.5), S INST/S BUS CYCLE mag (two halves), START rose`; control text cap 2.5.
* **Paddles** directly under the panel edge, colour = tile colour; slot 22 is the **cream unlabeled LAMP TEST paddle**
  ("located between the Switch Register and the LOAD ADRS switch … when raised, all indicator lights go on", Handbook 11.8.4) [V].

### 2.3 Colour palette (hex) [V measured; "display" column = my recommendation]
Measurement boxes: medians of 13×13 px (photo) or 17×17 px (scan). The scan has controlled light; the photo is room light;
the two flash/office photos (S8) are over‑exposed and shifted (rose → orange 5° under flash) — use them for hue only.

| element | scan S1 | photo S3 | other photos S8 | **display token** | HSL of token |
|---|---|---|---|---|---|
| window background | `#1d121b` | `#171216…#281a25` | — | `--panel #1f141c` (+ sheen `#2a1a26→#1a1118`) | 316°, 22 %, 10 % |
| black boxes / cells | `#0e0f16…#15161c` | `#0e0e0e…#100f10` | — | `--box #0f0f13` | |
| rose bar | `#681b2c…#6c1b2b` | `#792439…#81283d` | `#da6360…#e36667` (flash) | `--bar-rose #7e2639` | 347°, 54 %, 32 % |
| magenta bar | `#4a1933…#4d1832` | `#4b2b43…#56334f` | `#b85d70…#bb5e75` | `--bar-mag #503049` | 313°, 25 %, 25 % |
| rose tile | `#6e2234…#741f30` | `#7b2137…#933249` | `#ad4d63…#c25974` (Stief) | `--tile-rose #86293f` | 346°, 53 %, 34 % |
| magenta tile | `#511c37…#531e39` | `#4e2a44…#653e5d` | `#8b557b…#9b668d` | `--tile-mag #58304f` | 314°, 29 %, 27 % |
| label ink | `#d4d0c1…#ebdbd0` | `#f0e5e1…#f7f8f4` | — | `--ink #ece6da` | |
| lamp unlit | (backlit filter `#3f1115…#4a1519`) | `#140c0c…#180f0e` | — | lens `#241214 → #120a0b` | |
| lamp lit | — | clipped `#ff001d`, rim `#840012…#6e000d` | RUN `#c1282b` | radial `#ffd2c0 0 %, #ff5a3c 18 %, #f0101e 50 %, #8a0012 100 %` + halo `rgba(255,42,26,.55)→0` r≈9 mm | |
| rose paddle REST upper / lower | — | `#c52243…#ec4964` / `#5d3e3f…#765052` | `#ff699c…#ff76ac` (Stief, overexposed) | `#cf2647` / grad `#6a3438→#2e0c12` | 348°, 70 %, 48 % |
| rose paddle FWD (pressed down) | — | `#ee3c5a…#f43d5e` | | `#ef3c5b` | |
| rose paddle BACK (up) | — | lip `#ca2948`, face `#31080e…#3f0711` | | `#3a0a12` + lip `#cf2647` | |
| magenta paddle REST / FWD / BACK | — | `#5d2346…#884b71` / `#864770…#9a668b` / `#16090e…#310a18` (lip `#682c50`) | `#d173b3…#de7bbf` | `#6e2a55` / `#8f5079` / `#1c0b14` | 322°, 45 %, 30 % |
| blank (LAMP TEST) paddle | — | `#e9eae5…#ebece7`, lower `#817d74`, raised `#958072…#998677` | | `#e8e7e1` / `#85807a` | |
| bezel | — | top `#edece7`, frame `#dfdcd4`, strip face `#d6d3c8`, shaded side `#c4c1b9` | `#ecf5f4` (Stief) | `#dcd9d0`, hi `#ebe9e3`, lo/deboss `#b9b5ab` | |
| knob / key | — | knob black `#0b0b0c`; chrome key | | knob `#050506→#2e2e31`, pointer `#ece6da`; chrome `#f4f4f2→#9a9a97→#e2e2df` | |

Hue summary: rose = crimson/raspberry **345–349°**, magenta = plum/mauve **312–330°**, background = aubergine **311–316°**.
Nankervis uses CSS `crimson→firebrick` / `darkmagenta→purple` (300°, too violet) [V `pdp11.html:505-520`]; yaPDP lamp palette
`#ff3a00 → #8f0f00`, off `#0e0500` [V `yaPDP/css/pdp11.css:9-10`]. Noel Chiappa calls the original scheme "magenta and rose";
later 11/70s had dark‑blue/light‑blue switches (`decdatasystem 570`) and an "Industrial" blue/red panel [V S1 page].

### 2.4 Typography [V]
* Panel labels: Helvetica‑like grotesque, bold, all caps; measured ink widths are ≈87 % of Helvetica/Arial Bold.
  Cap heights: 2.5 mm (status row, control tiles), 2.6 (POWER/LOCK/OFF), 3.1 (select labels), 3.2 (HIGH/LOW), 3.5 (tile numbers, medium),
  3.6 (ADDRESS, DATA, PARITY). In SVG `font-size = cap / 0.717` (Helvetica) or `/0.69` (Archivo, Arimo).
* Best OFL match (measured in Chromium, `scripts/fontfit.mjs`): **Archivo wght 600, `font-stretch: 85%`** (variable wdth 62–125,
  OFL, google/fonts `ofl/archivo`) — widths within ≈0.3–0.7 mm of the panel incl. side bearings; Arimo Bold is ≈18 % too wide.
  Robust trick: give every label `textLength` = measured ink width + `lengthAdjust="spacingAndGlyphs"` (all widths are in the
  element table), so any fallback font keeps the exact footprint (verified in the overlay).
* Bezel tagline/digital logo: geometric rounded lowercase (Avant‑Garde‑like); OFL stand‑ins: Questrial or Didact Gothic
  (both google/fonts `ofl/`) [V licence dir exists]. Render debossed: no fill, 0.3 mm stroke `#b9b5ab` or a 1 px light/dark offset pair.
* Trademark note [U]: "digital" and "pdp" marks belong to DEC's successors (HP). Hobby replicas (PiDP‑11) carry "pdp11/70";
  make the bezel branding optional (CSS class) to be safe.

### 2.5 Lamp look [V]
Square‑ish windows 9.2 × 9.3 mm, r≈1.3, set low in tall black cells. Unlit: almost black with a faint red tint (photo `#140c0c`).
Lit (red LED behind a red diffuser): saturated red disc with white‑hot centre, soft halo spilling ≈1–2 mm over the cell
(`shots/bb_comp_lamps_zoom.png`, PiDP‑11 `shots/pidp11_manual_cover_panel.png`). BlinkenBone shows a dim lamp as the lit
image at reduced alpha over the unlit background (`DimmableLedControlSliceVisualization.java:83-93`).

### 2.6 Element table (normalized) — summary
`u = (X + 10.1) / 484.4`, `v = (Y + 9.9) / 260.4` (full console 0…1); `pu = X/464`, `pv = Y/186` (indicator panel).
Grid in u: slot pitch 0.02954, slot k centre `u = 0.07298 + 0.02954·k`. Full 248‑row table (every lamp, paddle, tile, bar,
box, notch, label with its measured ink width) in **Appendix A** and as JSON `$R4/panel1170_elements.json`
(each lamp row also carries the raw scan measurement — grid vs measured ≤0.35 mm).

| element | X mm | Y mm | w × h mm | u | v |
|---|---:|---:|---|---:|---:|
| lamp A21 … A0 (k = 0…21) | 25.25 … 325.76 | 103.55 | 9.2 × 9.3 | 0.0730 … 0.6934 | 0.4357 |
| lamp D15 … D0 (k = 6…21) | 111.11 … 325.76 | 144.02 | 9.2 × 9.3 | 0.2502 … 0.6934 | 0.5911 |
| lamp PARITY HIGH / LOW (k = 3, 4) | 68.18 / 82.49 | 144.02 | 9.2 × 9.3 | 0.1616 / 0.1911 | 0.5911 |
| status lamps PAR ERR … 22 (k = 10…21) | 168.35 … 325.76 | 65.75 | 9.2 × 9.3 | 0.3684 … 0.6934 | 0.2905 |
| ADDRESS‑select lamps (2 cols × 4) | 358.7 / 390.5 | 65.70, 77.15, 88.50, 100.15 | 9.2 × 9.3 | 0.7614 / 0.8270 | 0.2903 … 0.4226 |
| DATA‑select lamps (2 × 2) | 358.7 / 390.5 | 130.55, 141.80 | 9.2 × 9.3 | 0.7614 / 0.8270 | 0.5394 / 0.5826 |
| knob ADDRESS / DATA | 432.3 | 82.9 / 136.1 | Ø 25 | 0.9133 | 0.3564 / 0.5607 |
| key switch | 32.2 | 136.0 | Ø 19.2 | 0.0873 | 0.5603 |
| tiles SR21 … SR0 | 25.25 … 325.76 | 170.70 | 13.0 × 17.5 | 0.0730 … 0.6934 | 0.6935 |
| tiles LOAD ADRS … START (k = 23…29) | 354.38 … 440.24 | 170.70 | 13.0 × 17.5 (halves 13 × 7.8) | 0.7524 … 0.9297 | 0.6935 |
| paddles k = 0…29 | 25.25 … 440.24 | 205.4 | 13.5 × 37.6 | 0.0730 … 0.9297 | 0.8268 |
| logo pdp11/70 | 49.3 | 62.9 | 61.6 × 18.4 | 0.1226 | 0.2796 |
| ADDRESS bars | groups | 83.45 | h 6.5 | | 0.3585 |
| DATA bars | groups | 123.9 | h 6.6 | | 0.5138 |

### 2.7 How existing projects draw it [V]
* **BlinkenBone (Java Swing)**: one photo per state per control, cut from a 5424 px photo, scaled to 8 widths (800…2950 px);
  `coordinates.csv` = top‑left per layer; painted as a stack with `AlphaComposite` (`Panel1170.java:747-813`). Accurate but bitmap.
* **Nankervis / yaPDP**: HTML divs on a 4 × 5 px unit grid (`.frame` 272 × 93 units, `pdp11.html:10-17`), lamps are divs with a
  radial‑gradient background toggled by class, paddles are CSS‑border trapezoids whose top/bottom border widths are animated
  (`moveSwitch`, `pdp11.html:2847-2852`, `transition: 0.33s` line 463), knobs rotate with `transform` (0.5 s). Binary lamps only.
* **PCjs**: stylised (green switch blocks, red dot LEDs) — not a fidelity reference (`shots/pcjs_1170_panel_page.png`).
* **PiDP‑11**: physical 6:10 replica; same layout, LEDs with 32‑level PWM.

---------------------------------------------------------------------------------------------------------------

## 3. Lamp realism (brightness, persistence, rendering)

### 3.1 What the real machine had
* 11/70 consoles: "all 11/70 machines I've seen or worked on had LEDs … might have had bulbs initially" (J. Billquist,
  cctalk 2015‑06‑01) and "I know 11/70's had LEDs, but all the /45's I worked with had bulbs" (J. Elson, same day) [V]. LEDs
  switch in well under 1 µs [V PiDP‑8/I wiki], so what the eye sees is the **duty cycle** of each bit averaged over the eye's
  integration time (tens of ms) — the famous dim/"ghost" bits and idle‑loop patterns. Bulbs (11/45, early 11/70) add
  thermal lag: faster warm‑up than cool‑down.

### 3.2 How existing projects compute brightness [V]
| project | method | constants (file:line) |
|---|---|---|
| BlinkenBone Java panel | time‑weighted **box average** of the state history over the last 100 ms; 16 levels mapped to alpha `0.1 + 0.9·s/15`; repaint timer 50 ms (20 Hz) | `ControlSliceVisualization.java:76,111-127`; `Panel1170.java:413`; `DimmableLedControlSliceVisualization.java:41,87`; `Panelsim1170_app.java:89` |
| PiDP‑11 server | per‑bit time‑weighted average over `1/fmax` = **100 ms** (`fmax = 10`, "Joerg's setting"; a commented alternative `fmax = 4` "lamps are slow light bulbs"); LED refresh thread every **50 ms**; **32 brightness levels** → 31‑phase PWM with a "logarithmic" duty table | `src/11_pidp_server/pidp11/main.c:563-564`; `gpiopattern.h:34-41`; `gpiopattern.c:206-245, 476-560`; `07.0_blinkenlight_api/historybuffer.c:277` |
| SimH front‑panel API | `sim_panel_set_sampling_parameters(panel, sample_frequency /*instr between samples*/, sample_depth)` + `sim_panel_add_register_bits()` return per‑bit counts 0…depth (rolling average) | `02.3_simh/4.x+realcons/src/sim_frontpanel.h:132-167, 230-257` |
| PiDP‑8/I ILS2 (incandescent) | exponential rise/fall per 7 ms PWM cycle: rise 0.2775, fall 0.0975, rescaled `f = 1 − (1 − f₀)^(T/7ms)` → **τ_rise ≈ 21.5 ms, τ_fall ≈ 68 ms** (10–90 % ≈ 47 ms up, ≈150 ms down); 33‑step ramp | S10 (my τ arithmetic) |
| Nankervis / yaPDP | binary: `updateLights()` diffs old/new masks and toggles `.lit` only on changed bits; no persistence | `yaPDP/src/pdp11.js:2417-2480` |

Physical bulb data I could find is weak [U]: a patent quotes ≈50 ms heat‑up / ≈5 s cool‑down for a 21 W automotive bulb
(not representative of a tiny indicator lamp).

### 3.3 Recommended model (UI side)
1. **Backend → UI**: one brightness byte per lamp (0…255 = duty cycle over the last ~33–50 ms, e.g. from SimH bit sampling)
   at **30–60 Hz**; 64 B/frame ≈ 3.8 KB/s per viewer at 60 Hz; send only when something changed; keep only the newest frame
   on the client. Suggested lamp order: A0…A21, D0…D15, PARITY_LOW, PARITY_HIGH, PAR_ERR, ADRS_ERR, RUN, PAUSE, MASTER, USER,
   SUPER, KERNEL, DATA, ADRS16, ADRS18, ADRS22, ADDR‑select ×8, DATA‑select ×4 (final order is the behaviour researcher's call).
2. **Client smoothing** each rAF with real `dt`: `b += (target − b)·(1 − exp(−dt/τ))`, τ = τ_rise if target > b else τ_fall.
   Presets: **LED** τ 12–15 ms both ways (hides frame jitter, keeps crisp patterns); **bulb/11‑45** τ_rise 21.5 / τ_fall 68 ms;
   **"PiDP‑11 look"** = backend 100 ms box average + LED preset; **reduced‑motion** τ 150 ms (honour `prefers-reduced-motion`; the
   panel can exceed 3 flashes/s — WCAG 2.3.1 [U]).
3. **Perception**: alpha = duty^(1/2.2) (browser blends in gamma space, so this makes emitted light ∝ duty); zero below 1/64.
4. **Frame rate**: drive from `requestAnimationFrame`, dt‑based (so 120/144 Hz displays look the same), optionally skip
   frames with dt < 14 ms; stop the loop when all lamps are settled (|target − b| < 1/128) and restart on the next message;
   rAF pauses in hidden tabs — never use it for switch timing.

### 3.4 Rendering technique — benchmark [V]
`$R4/work/bench.html` + `scripts/bench.mjs`, `bench_trace.mjs`: prototype panel (1600 px wide), 64 lamps animated each frame
with the exponential model, writes skipped when the 1/64‑quantised value is unchanged (≈14 writes/frame). Chromium 141
headless shell (SwiftShader, no GPU, shared 16‑core host), trace `RasterTask` time per wall‑second:

| technique | raster ms/s | other main costs | fps |
|---|---:|---|---:|
| `style.opacity` on SVG child `<rect>`/`<circle>` (lit lens + halo) | 373 | DrawAndSwap 945 | 47–60 |
| **64 HTML `<div>` layers over the static SVG, `will-change: opacity`, radial‑gradient lens+halo** | **0.44** | DrawAndSwap 94 | **59.7** |
| `<canvas>` overlay, 64 `drawImage` glow sprites (`lighter`) | 199 | Commit 164, DrawAndSwap 865 | 36–60 |
| CSS `filter: drop-shadow()` ×2 on lit SVG rects | 1559 | — | 43–60 |
| nothing changing | 0 (no frames) | — | — |

Recommendation: draw everything static (bezel, window, boxes, bars, notches, tiles, labels, logo, **unlit** lenses) as one SVG
(or an `<img src=panel.svg>`), then put 64 absolutely positioned HTML lamp elements on top (left/top/width in % from the table,
so resizing needs no JS) whose only animated property is `opacity` → compositor‑only, no re‑raster, no layout. Never use
`filter`/`feGaussianBlur` per lamp, never read layout in the loop, batch writes in one rAF. Paddles/knobs/key change rarely:
keep them as SVG (pose class swap; knob `transform: rotate()` on its own group).

---------------------------------------------------------------------------------------------------------------

## 4. Switches, knobs, key: look and interaction

### 4.1 Physical behaviour [V Handbook ch. 11 + BlinkenBone images]
| control | type | actuation | visual pose at rest → actuated |
|---|---|---|---|
| SR21…SR0 | latching toggle | up = 1, down = 0 | 0 = REST, 1 = BACK |
| LAMP TEST (slot 22, cream, unlabeled) | momentary | **raised** → all lamps on | REST → BACK |
| LOAD ADRS, EXAM, CONT, START | momentary | **depress** | REST → FWD |
| DEP | momentary | **raise** | REST → BACK |
| ENABLE/HALT | 2‑position | up ENABLE, down HALT | ENABLE = REST, HALT = FWD |
| S INST / S BUS CYCLE | 2‑position | up S INST, down S BUS CYCLE | REST / FWD |
| key OFF/POWER/LOCK | 3‑position key, no wrap | LOCK = power on but the 7 control switches LOAD ADRS…START disabled | 225° / 315° / 45° |
| ADDRESS select | rotary, 8 detents, continuous (BlinkenBone wraps) | USER I/D, SUPER I/D, KERNEL I/D, CONS PHY, PROG PHY | see angles |
| DATA select | rotary, 8 detents = 4 functions ×2 | DATA PATHS, BUS REG, µADRS FPP/CPU, DISPLAY REGISTER | see angles |

Knob pointer angles measured from the BlinkenBone layers (cw from 12 o'clock, `scripts/knobangles.py`), all on 22.5° + 45°·k:
ADDRESS `PROG PHY 22.5, CONS PHY 67.5, KERNEL D 112.5, SUPER D 157.5, USER D 202.5, USER I 247.5, SUPER I 292.5, KERNEL I 337.5`;
DATA `BUS REG 22.5/202.5, DATA PATHS 67.5/247.5, µADRS 112.5/292.5, DISPLAY REG 157.5/337.5`. Knob = black round skirt
Ø≈25 mm with a raised bar (≈26 × 9 mm) carrying a white pointer line from a white centre dot (`shots/bb_knob_*_states.png`).
BlinkenBone's knob order/encoding table: `Panel1170.java:567-594, 1026-1117`.

### 4.2 Paddle poses (what to draw) [V `shots/bb_switch_poses_rest_vs_pressed.png`, `pidp11_manual_sr107_switches.png`]
* **REST** ("bow‑tie"): bright saturated upper face (top ≈50 %), crease at y 205.4 with dark side notches (waist ≈1.6 mm each
  side), darker lower face fading to near black.
* **BACK** (top tilted away: SR = 1, DEP/LAMP TEST raised): almost uniformly dark face (rose `#3a0a12`, magenta `#1c0b14`),
  thin bright lip along the top edge, straight silhouette.
* **FWD** (pressed down, HALT, S BUS CYCLE): whole face bright and flat (rose `#ef3c5b`, magenta `#8f5079`), small specular at the bottom.
* Motion: real toggles snap; animate pose changes 60–90 ms ease‑out (Nankervis uses 330 ms — feels sluggish [U]); hold a
  momentary visual ≥120 ms even for very short clicks.

### 4.3 Pointer / touch interaction (recommended)
* Use **Pointer Events** for mouse, touch and pen; `touch-action: none` on the controls layer only (pinch‑zoom elsewhere),
  `user-select: none`, `-webkit-touch-callout: none`, images `draggable="false"`.
* **Hit areas**: whole slot (14.31 mm wide) × paddle + its tile (y 162…224, ≈62 mm) → at a 1280 px wide console 1 mm = 2.64 px
  → 38 × 164 px targets; at 390 px (phone) a slot is 11.5 px — below the 24 px WCAG 2.2 AA minimum [U] → offer zoom/pan, a
  landscape hint and an optional large "switch register strip" (22 toggles + 7 controls).
* SR toggles: click/tap toggles; **drag‑paint**: pointerdown fixes target = opposite of the first paddle, dragging across sets every
  paddle passed to that target (BlinkenBone generates down/up per crossed control while dragging, `Panel1170.java:968-990`);
  a vertical flick (> 6 mm) sets 1 (up) / 0 (down) explicitly.
* Momentary: `pointerdown` → press (+`setPointerCapture`), release on `pointerup`/`pointercancel`/`lostpointercapture`/window
  `blur`/`visibilitychange` (BlinkenBone notes momentary switches "can hang sometimes", `Panel1170.java:1002-1004`); send both
  edges to the backend and let it latch short presses.
* Knob: click right half = cw, left half = ccw, wrap (BlinkenBone `Panel1170.java:894-909`; Nankervis: click = cw, Shift‑click = ccw,
  `pdp11.html:864,890`); wheel over the knob (`{passive:false}`, accumulate deltaY ≥ 60 px per detent for trackpads); drag = follow
  `atan2` around the centre, snap to the nearest 45° detent with ±10° hysteresis; animate 80–120 ms.
* Key: click right/left half = cw/ccw, no wrap (`Panel1170.java:923-933`); require confirmation (or long‑press) for **OFF**.
* Optional sound (switch clack, knob click) and `navigator.vibrate(8)` on Android — only after a user gesture (see 6.5).

### 4.4 Keyboard and accessibility (recommended)
* Panel is one focusable region with a visible focus ring (double ring: outer `#0b0d10`, inner `#66b3ff`, works on plum and cream).
  Roving `tabindex` over the 30 paddles: ←/→ move, Home/End, ↑ = set 1 / raise (DEP, LAMP TEST), ↓ = set 0 / depress,
  Space/Enter = toggle or press (keydown press, keyup release).
* Panel shortcuts (**only while the panel has focus**, no modifiers): `0`–`7` shift an octal digit into SR
  (`SR = ((SR<<3)|d) & 0o17777777`), Backspace = SR >>= 3, Esc/Delete = SR 0, `L` LOAD ADRS, `E`/`X` EXAM, `D` DEP, `C` CONT,
  `S` START, `H` ENABLE/HALT, `B` S INST/S BUS, `T` LAMP TEST (hold), `[`/`]` ADDRESS knob, `{`/`}` DATA knob, `K` key (confirm OFF).
  PiDP‑11 convention worth copying as an optional shortcut: set SR to an octal "system number" and push the ADDRESS knob to
  (re)boot (PiDP‑11 manual p. 12) — e.g. Enter on the knob.
* ARIA: SR `role="switch"` + `aria-checked` + `aria-label="Switch register bit 17"`; momentary `role="button"` (+ `aria-pressed`
  while held); knobs `role="slider"` `aria-valuemin=0 aria-valuemax=7 aria-valuetext="CONS PHY"`; key `role="slider"` with 3
  values or a radiogroup; lamps `aria-hidden`, plus an optional readout "ADDRESS 17777707 DATA 000000" updated ≤1 Hz.

---------------------------------------------------------------------------------------------------------------

## 5. Console terminal

### 5.1 xterm.js [V npm + GitHub API, 2026‑10‑21]
| package | version (date) | files to vendor | UMD global |
|---|---|---|---|
| `@xterm/xterm` | **6.0.0** (2025‑12‑22), MIT, dist‑tag beta 6.1.0‑beta.315 | `lib/xterm.js` (488,663 B UMD) or `lib/xterm.mjs` (344,970 B ESM), `css/xterm.css` (7,112 B), types `typings/xterm.d.ts` | `Terminal` |
| `@xterm/addon-fit` | 0.11.0 | `lib/addon-fit.js` / `.mjs` (1.5 / 2.0 KB) | `FitAddon.FitAddon` |
| `@xterm/addon-webgl` | 0.19.0 | `lib/addon-webgl.js` / `.mjs` (247 / 127 KB) | `WebglAddon.WebglAddon` |
| `@xterm/addon-attach` | 0.12.0 | `lib/addon-attach.js` / `.mjs` (1.7 / 2.1 KB) | `AttachAddon.AttachAddon` |
| others (same release) | web‑links 0.12.0, unicode11 0.9.0, search 0.16.0, serialize 0.14.0, clipboard 0.2.0, image 0.9.0 | | |

6.0.0 notes: **canvas renderer addon removed** (#5105) — use the default DOM renderer or WebGL (`@xterm/addon-canvas` 0.7.0 is
stale, peer `^5`); ESM builds added; `overviewRulerWidth` → `overviewRuler.*`; alt→ctrl‑arrow hack removed; `windowsMode`,
`fastScrollModifier` removed; synchronized output (DEC 2026) added. Option names verified in `typings/xterm.d.ts`
(`fontFamily, fontSize, lineHeight, letterSpacing, cursorStyle, cursorBlink, scrollback, convertEol, customGlyphs,
minimumContrastRatio, screenReaderMode, …`).
Renderer choice: DOM renderer for 80×24 is fast enough and is the only one where CSS `text-shadow` glow applies; WebGL for heavy
scrolling (`webgl.onContextLoss(() => webgl.dispose())`). Wire with `AttachAddon(ws)` or `term.onData(d => ws.send(d))` +
`term.write(new Uint8Array(msg))`.

### 5.2 Fonts [V licences]
| font | look | licence | notes |
|---|---|---|---|
| **Glass TTY VT220** (svofski/glasstty, `Glass_TTY_VT220.ttf` 88 KB) | traced VT220 ROM, includes scan‑line gaps | **Unlicense / public domain** | author: use size 20 (Mac/Linux) / 15 (Windows) or multiples; 10×20 px cell |
| VT323 (Peter Hull) | VT320 bitmap look | OFL (google/fonts `ofl/vt323`) | crisp at 24 px multiples |
| IBM Plex Mono | modern, readable | OFL‑1.1 | "clean" mode, 132 columns |
| fritzm/vt52 (`vt52.otf`, used by yaPDP) | VT52 | MIT | if a VT52 skin is wanted |
| Share Tech Mono, Courier Prime | alt. mono | OFL | |
| DEC Terminal Modern | VT220 | **unclear** ("free for personal use" on a font site, no licence file) | avoid for distribution |

### 5.3 Phosphor themes and CRT effect [V rendered: `shots/console_themes_xterm6.png`]
* P1 green fg `#33ff66` on `#041208`; P3 amber `#ffb000` on `#140b00`; P4 white `#e6edf3` on `#0b0d10`
  (selection = fg at 30 % alpha, cursorAccent = bg). DEC VT100s were white (P4); VT220s came in white/green/amber [U].
* Effects as overlays with `pointer-events:none` (selection keeps working): vignette `radial-gradient(ellipse, transparent 62%,
  rgba(0,0,0,.42))`, inset shadow, optional scan lines `repeating-linear-gradient(#0000 0 2px, #00000038 2px 3px)` (not with
  Glass TTY — it has its own gaps), glow `text-shadow: 0 0 1px fg, 0 0 5px fg@45%` (DOM renderer only). No curvature
  transforms (breaks mouse mapping, blurs), no flicker. Offer a "clean" toggle.
* **Pitfall found**: Glass TTY at `fontSize 20, lineHeight 1` gives 19 px rows in xterm 6 (800×456 for 80×24) and every other
  row looks dimmer because the font's built‑in scan lines beat against the row grid; `lineHeight: 20/19` gives exact 20 px rows
  (800×480) and uniform text [V]. **Pitfall found**: a transparent `theme.background` (with `allowTransparency`) rendered as black;
  use the opaque phosphor background colour [V].

### 5.4 Geometry
80×24 default (VT100/VT220); 132×24 option (VT220 wide; RSX/RSTS `SET TERM /WIDTH:132`) [U]. Keep cols/rows fixed (the guest
does not learn about window resizes over a DL11) and scale the font instead: Glass TTY 20 px → 800×480, 40 px → 1600×960;
non‑integer scales with VT323/Plex. FitAddon only if a "fill window" mode is wanted.

### 5.5 Console input pitfalls [U unless noted]
Backspace: xterm sends DEL (0x7F) — right for RT‑11/RSX/RSTS; Unix V6/V7 default erase is `#`, kill `@`; offer "Backspace sends
^H". Browser‑reserved keys (Ctrl+W/T/N, Ctrl+Tab) never reach the page except via Keyboard Lock in fullscreen (Chromium) →
provide a "send control key" menu (^C ^O ^Q ^S ^U ^W ^Z, BREAK). SimH's WRU character (default ^E) returns SimH to `sim>` on a
local console — coordinate with the backend researcher. Bell → visual flash of the bezel.

---------------------------------------------------------------------------------------------------------------

## 6. Overall UI layout

```
+------------------------------------------------------------------------------------+
| [System: 2.11BSD v]  [Boot]  [Power ●]   ● RUNNING  2.4 MIPS   [⧉ Panel] [⧉ Console] |
|                                           [Phosphor v] [CRT] [Sound] [⛶] [?]         |
+------------------------------------------------------------------------------------+
|                         FRONT PANEL  (aspect 484.4 : 260.4)                         |
|        static SVG art + 64 opacity layers + SVG paddles/knobs/key (all % units)     |
+------------------------------------------------------------------------------------+
|                 CONSOLE  80×24  (Glass TTY 20/40 px, phosphor theme)                |
+------------------------------------------------------------------------------------+
```
* **Toolbar**: system selector (OS disk images: RT‑11, RSX‑11M+, RSTS/E, 2.11BSD, Unix V6/V7, Ultrix‑11, XXDP …), Boot, Power
  (mirrors the key: OFF/POWER/LOCK), status pill (connected / running / halted, MIPS, disk activity), pop‑out buttons, theme,
  CRT, sound, fullscreen, help (shortcut sheet).
* **Dark theme tokens**: page `#0d0e10`, surface `#16181b`, border `#2a2d31`, text `#d6d9de`, muted `#8b9098`, focus `#66b3ff`,
  danger `#ff5a4a`, accent = current phosphor colour. The panel keeps its own physical colours in every theme.
* **Responsive**: panel `width: min(100%, 1600px); aspect-ratio: 484.4/260.4` with `preserveAspectRatio="xMidYMid meet"`;
  compact mode hides the bezel (`viewBox="0 22 464 204"`, +4 % size); below ≈700 px allow pinch/pan or the SR strip; console
  scales by font size (integer multiples for Glass TTY).
* **Pop‑out**: `window.open('/panel?popout=1', 'pdp11-panel', 'popup,width=1300,height=720')` from a click handler (else blocked
  → show a hint); each window opens **its own WebSocket** (server fans out lamp frames, accepts switch events from any window);
  UI prefs via `BroadcastChannel('pdp11-ui')`; the main window shows "Panel is in another window [Bring back]" and polls
  `win.closed`. Optional Chromium‑only always‑on‑top mode: Document Picture‑in‑Picture `documentPictureInPicture.requestWindow()` [U].
* **Pitfalls**: (1) audio autoplay — create/resume the `AudioContext` inside the first user gesture (power key / boot);
  (2) focus — keys typed while the terminal is focused belong to the PDP‑11; panel shortcuts only when the panel is focused;
  clicking the panel must not steal focus permanently (return focus to the terminal after a mouse action if it had it);
  show "click to type" on the console; (3) stuck momentary switches on focus loss (release all on `blur`/`visibilitychange`);
  (4) background tabs throttle rAF/timers — drop stale lamp frames, keep switch events event‑driven; (5) Hi‑DPI: SVG is crisp,
  bitmap‑style terminal fonts need integer sizes; (6) long‑press context menu and text selection on touch; (7) photosensitivity
  (reduced‑motion preset).

---------------------------------------------------------------------------------------------------------------

## 7. Screenshots and artefacts (`$R4/shots/`)
| file | content |
|---|---|
| `bb_composite_1340.png`, `bb_composite_2950.png` | BlinkenBone photo layers composited (lamps lit, SR20/18/14/2 up, EXAM down, DEP up) — best overall photo reference |
| `bb_crop_logo_key.png`, `bb_crop_status_row(_enh).png`, `bb_crop_addr_row_left/right.png`, `bb_crop_knobs.png`, `bb_crop_switches_left/right.png`, `bb_crop_data_bar_notches.png` | detail crops of the real panel |
| `bb_comp_knobs.png`, `bb_comp_lamps_zoom.png`, `bb_comp_keyswitch.png`, `bb_comp_switches_mid.png` | lit lamps, knobs, key, paddles (composited) |
| `bb_knob_addr_states.png`, `bb_knob_data_states.png`, `bb_keyswitch_off_power_lock.png`, `bb_keyswitch_states.png` | all detent images |
| `bb_switch_poses_rest_vs_pressed.png` | REST vs BACK vs FWD paddle poses |
| `scan_zoom_data_notch_and_cells.png`, `scan_zoom_status_and_addr_cells.png`, `scan_zoom_logo_pdp1170.png` | 300 dpi scan details (notch, cell separators, logo) |
| `pidp11_manual_cover_panel.png`, `pidp11_manual_sr107_switches.png` | PiDP‑11 lit panel; SR = 107 octal showing up/down look |
| `nankervis_pdp1170_page.png`, `pcjs_1170_panel_page.png` | existing web panels |
| `proto_svg_panel_1200.png`, `proto_overlay_on_photo_1200.png`, `proto_overlay_detail_right.png`, `proto_logo_traced.png` | my SVG prototype from the table, its 50 % overlay on the photo, traced logo |
| `console_themes_xterm6.png` | xterm.js 6.0.0 with Glass TTY/P1, VT323/P3, Plex Mono/P4 |

Other artefacts: `$R4/panel1170_elements.json` (248 elements, mm + normalized + measured), `$R4/work/proto_panel.svg`
(prototype, 64 lamp groups `L_*`, 30 paddles `sw_*`, knobs, key), `$R4/work/logo_pdp1170_path.svgfrag`,
`$R4/work/console_proto.html`, `$R4/work/bench.html`, generators `$R4/scripts/gen_panel_svg.py`, `gen_table.py`, `trace_logo.py`.
Raw downloads (untrusted data; no downloaded code was run on the host — live web pages and CDN scripts (xterm.js, fonts) ran only inside headless Chromium; Python that read downloads ran with `-I`): `$R4/dl/{blinkenbone,pidp11,yapdp,skn,jnc*,commons-*,pidp11-manual,handbook1170,npm-xterm,fontinfo}`.

## 8. Open points / caveats
* Frame offset X has ±0.6 mm uncertainty (drawing dims vs scan); bezel height ±3 mm (photo edge); knob Ø (≈25 mm) and key bow
  size estimated from shadowed photo layers.
* The scan is one physical panel (early magenta/rose); later blue panels exist — not covered.
* Lamp window corner radius, cell‑gap start height and notch fillets are eyeballed from 300 dpi zooms (±0.3 mm).
* Benchmarks ran on software GL in a shared container; relative ranking is what matters — re‑check on target GPUs/Safari.
* [U] items: VT220 phosphor options, WCAG target numbers, Document PiP / Keyboard Lock specifics, WRU handling over telnet.

---------------------------------------------------------------------------------------------------------------

## Appendix A — full element table (panel mm; u,v normalized to the 484.4 × 260.4 mm console)
(`pu = cx/464`, `pv = cy/186` are in the JSON; lamp rows show the raw 300 dpi scan measurement in parentheses.)

| id | kind | cx mm | cy mm | w | h | u | v | notes |
|---|---|---:|---:|---:|---:|---:|---:|---|
| bezel | frame | 232.1 | 120.3 | 484.4 | 260.4 | 0.5 | 0.5 | almond/cream moulded bezel, r~4mm |
| top_band | frame | 232.1 | 6.8 | 484.4 | 33.4 | 0.5 | 0.0641 | cream band above the dark window |
| logo_strip | frame | 148.35 | 6.9 | 303.5 | 19.2 | 0.3271 | 0.0645 | raised rounded strip r~3.5; tone-on-tone digital logo + tagline |
| digital_logo | art | 39.1 | 6.95 | 46.4 | 13.5 | 0.1016 | 0.0647 | 7 boxed lowercase letters d-i-g-i-t-a-l, debossed, no colour |
| tagline | art | 172.3 | 7.5 | 214.2 | 6.0 | 0.3765 | 0.0668 | "digital equipment corporation • maynard, massachusetts", debossed |
| window | frame | 232.0 | 104.75 | 463.2 | 162.5 | 0.4998 | 0.4403 | dark smoked acrylic; top corners r~6-8, bottom-right r~6 |
| indicator_panel | frame | 232.0 | 93.0 | 464.0 | 186.0 | 0.4998 | 0.3952 | acrylic panel per Chiappa drawing; top ~23.5 mm hidden behind bezel band |
| logo_pdp1170 | art | 49.3 | 62.9 | 61.6 | 18.4 | 0.1226 | 0.2796 | outline "pdp" + solid "11" + thin "/" + outline "70"; traced path in work/logo_pdp1170_path.svgfrag |
| paddle_bay | frame | 232.6 | 205.4 | 429.5 | 38.6 | 0.501 | 0.8268 | recess holding 30 paddles |
| ledge | frame | 232.1 | 234.9 | 484.4 | 21.4 | 0.5 | 0.9401 | shadowed cream ledge below paddles |
| lip | frame | 232.1 | 248.05 | 484.4 | 4.9 | 0.5 | 0.9906 | bright front lip |
| box.status1 | box | 175.5 | 63.35 | 27.51 | 17.9 | 0.3832 | 0.2813 | black r~2.2: PAR_ERR,ADRS_ERR |
| box.status2 | box | 211.28 | 63.35 | 41.82 | 17.9 | 0.457 | 0.2813 | black r~2.2: RUN,PAUSE,MASTER |
| box.status3 | box | 254.21 | 63.35 | 41.82 | 17.9 | 0.5456 | 0.2813 | black r~2.2: USER,SUPER,KERNEL |
| box.status4 | box | 304.29 | 63.35 | 56.13 | 17.9 | 0.649 | 0.2813 | black r~2.2: DATA_SPACE,ADRS16,ADRS18,ADRS22 |
| box.parity | box | 75.15 | 135.35 | 27.5 | 29.9 | 0.176 | 0.5578 | black r~2.4, holds PARITY/HIGH/LOW + 2 lamps |
| box.knob_addr | box | 397.5 | 81.3 | 98.6 | 58.0 | 0.8415 | 0.3502 | black r~3 |
| box.knob_data | box | 397.35 | 135.6 | 98.5 | 30.4 | 0.8411 | 0.5588 | black r~3 |
| bar.A21 | bar | 25.25 | 83.45 | 12.94 | 6.5 | 0.073 | 0.3585 | rose |
| cells.A21_21 | cells | 25.25 | 98.4 | 13.2 | 23.4 | 0.073 | 0.4159 | black lamp cells, bottom corners r~2.4, ~0.5mm separators |
| bar.A20_18 | bar | 53.87 | 83.45 | 41.56 | 6.5 | 0.1321 | 0.3585 | mag |
| cells.A20_18 | cells | 53.87 | 98.4 | 41.82 | 23.4 | 0.1321 | 0.4159 | black lamp cells, bottom corners r~2.4, ~0.5mm separators |
| bar.A17_15 | bar | 96.8 | 83.45 | 41.56 | 6.5 | 0.2207 | 0.3585 | rose |
| cells.A17_15 | cells | 96.8 | 98.4 | 41.82 | 23.4 | 0.2207 | 0.4159 | black lamp cells, bottom corners r~2.4, ~0.5mm separators |
| bar.A14_12 | bar | 139.73 | 83.45 | 41.56 | 6.5 | 0.3093 | 0.3585 | mag |
| cells.A14_12 | cells | 139.73 | 98.4 | 41.82 | 23.4 | 0.3093 | 0.4159 | black lamp cells, bottom corners r~2.4, ~0.5mm separators |
| bar.A11_9 | bar | 182.66 | 83.45 | 41.56 | 6.5 | 0.3979 | 0.3585 | rose |
| cells.A11_9 | cells | 182.66 | 98.4 | 41.82 | 23.4 | 0.3979 | 0.4159 | black lamp cells, bottom corners r~2.4, ~0.5mm separators |
| bar.A8_6 | bar | 225.59 | 83.45 | 41.56 | 6.5 | 0.4866 | 0.3585 | mag |
| cells.A8_6 | cells | 225.59 | 98.4 | 41.82 | 23.4 | 0.4866 | 0.4159 | black lamp cells, bottom corners r~2.4, ~0.5mm separators |
| bar.A5_3 | bar | 268.52 | 83.45 | 41.56 | 6.5 | 0.5752 | 0.3585 | rose |
| cells.A5_3 | cells | 268.52 | 98.4 | 41.82 | 23.4 | 0.5752 | 0.4159 | black lamp cells, bottom corners r~2.4, ~0.5mm separators |
| bar.A2_0 | bar | 311.45 | 83.45 | 41.56 | 6.5 | 0.6638 | 0.3585 | mag +"ADDRESS" |
| cells.A2_0 | cells | 311.45 | 98.4 | 41.82 | 23.4 | 0.6638 | 0.4159 | black lamp cells, bottom corners r~2.4, ~0.5mm separators |
| bar.D15 | bar | 111.11 | 123.9 | 12.94 | 6.6 | 0.2502 | 0.5138 | rose |
| cells.D15_15 | cells | 111.11 | 138.9 | 13.2 | 22.8 | 0.2502 | 0.5714 | black lamp cells |
| bar.D14_12 | bar | 139.73 | 123.9 | 41.56 | 6.6 | 0.3093 | 0.5138 | mag |
| cells.D14_12 | cells | 139.73 | 138.9 | 41.82 | 22.8 | 0.3093 | 0.5714 | black lamp cells |
| bar.D11_9 | bar | 182.66 | 123.9 | 41.56 | 6.6 | 0.3979 | 0.5138 | rose |
| cells.D11_9 | cells | 182.66 | 138.9 | 41.82 | 22.8 | 0.3979 | 0.5714 | black lamp cells |
| bar.D8_6 | bar | 225.59 | 123.9 | 41.56 | 6.6 | 0.4866 | 0.5138 | mag |
| cells.D8_6 | cells | 225.59 | 138.9 | 41.82 | 22.8 | 0.4866 | 0.5714 | black lamp cells |
| bar.D5_3 | bar | 268.52 | 123.9 | 41.56 | 6.6 | 0.5752 | 0.5138 | rose |
| cells.D5_3 | cells | 268.52 | 138.9 | 41.82 | 22.8 | 0.5752 | 0.5714 | black lamp cells |
| bar.D2_0 | bar | 311.45 | 123.9 | 41.56 | 6.6 | 0.6638 | 0.5138 | mag +"DATA" |
| cells.D2_0 | cells | 311.45 | 138.9 | 41.82 | 22.8 | 0.6638 | 0.5714 | black lamp cells |
| notch.D14|13 | notch | 132.9 | 125.25 | 4.6 | 3.9 | 0.2952 | 0.519 | black tab rising into DATA bar: base 4.6, top flat 1.5, height 3.9, concave fillets |
| notch.D11|10 | notch | 175.7 | 125.25 | 4.6 | 3.9 | 0.3836 | 0.519 | black tab rising into DATA bar: base 4.6, top flat 1.5, height 3.9, concave fillets |
| notch.D8|7 | notch | 218.6 | 125.25 | 4.6 | 3.9 | 0.4721 | 0.519 | black tab rising into DATA bar: base 4.6, top flat 1.5, height 3.9, concave fillets |
| lamp.PAR_ERR | lamp | 168.35 | 65.75 | 9.2 | 9.3 | 0.3684 | 0.2905 | status row (meas 168.12,65.79) |
| lamp.ADRS_ERR | lamp | 182.66 | 65.75 | 9.2 | 9.3 | 0.3979 | 0.2905 | status row (meas 182.5,65.7) |
| lamp.RUN | lamp | 196.97 | 65.75 | 9.2 | 9.3 | 0.4275 | 0.2905 | status row (meas 196.8,65.74) |
| lamp.PAUSE | lamp | 211.28 | 65.75 | 9.2 | 9.3 | 0.457 | 0.2905 | status row (meas 211.19,65.73) |
| lamp.MASTER | lamp | 225.59 | 65.75 | 9.2 | 9.3 | 0.4866 | 0.2905 | status row (meas 225.34,65.75) |
| lamp.USER | lamp | 239.9 | 65.75 | 9.2 | 9.3 | 0.5161 | 0.2905 | status row (meas 239.79,65.75) |
| lamp.SUPER | lamp | 254.21 | 65.75 | 9.2 | 9.3 | 0.5456 | 0.2905 | status row (meas 254.24,65.75) |
| lamp.KERNEL | lamp | 268.52 | 65.75 | 9.2 | 9.3 | 0.5752 | 0.2905 | status row (meas 268.68,65.81) |
| lamp.DATA_SPACE | lamp | 282.83 | 65.75 | 9.2 | 9.3 | 0.6047 | 0.2905 | status row (meas 282.82,65.81) |
| lamp.ADRS16 | lamp | 297.14 | 65.75 | 9.2 | 9.3 | 0.6343 | 0.2905 | status row (meas 297.17,65.79) |
| lamp.ADRS18 | lamp | 311.45 | 65.75 | 9.2 | 9.3 | 0.6638 | 0.2905 | status row (meas 311.56,65.71) |
| lamp.ADRS22 | lamp | 325.76 | 65.75 | 9.2 | 9.3 | 0.6934 | 0.2905 | status row (meas 325.61,65.69) |
| lamp.A21 | lamp | 25.25 | 103.55 | 9.2 | 9.3 | 0.073 | 0.4357 | ADDRESS (meas 25.14,103.65) |
| lamp.A20 | lamp | 39.56 | 103.55 | 9.2 | 9.3 | 0.1025 | 0.4357 | ADDRESS (meas 39.21,103.62) |
| lamp.A19 | lamp | 53.87 | 103.55 | 9.2 | 9.3 | 0.1321 | 0.4357 | ADDRESS (meas 53.56,103.62) |
| lamp.A18 | lamp | 68.18 | 103.55 | 9.2 | 9.3 | 0.1616 | 0.4357 | ADDRESS (meas 67.91,103.59) |
| lamp.A17 | lamp | 82.49 | 103.55 | 9.2 | 9.3 | 0.1911 | 0.4357 | ADDRESS (meas 82.14,103.47) |
| lamp.A16 | lamp | 96.8 | 103.55 | 9.2 | 9.3 | 0.2207 | 0.4357 | ADDRESS (meas 96.47,103.47) |
| lamp.A15 | lamp | 111.11 | 103.55 | 9.2 | 9.3 | 0.2502 | 0.4357 | ADDRESS (meas 110.9,103.41) |
| lamp.A14 | lamp | 125.42 | 103.55 | 9.2 | 9.3 | 0.2798 | 0.4357 | ADDRESS (meas 125.14,103.47) |
| lamp.A13 | lamp | 139.73 | 103.55 | 9.2 | 9.3 | 0.3093 | 0.4357 | ADDRESS (meas 139.44,103.45) |
| lamp.A12 | lamp | 154.04 | 103.55 | 9.2 | 9.3 | 0.3389 | 0.4357 | ADDRESS (meas 153.82,103.52) |
| lamp.A11 | lamp | 168.35 | 103.55 | 9.2 | 9.3 | 0.3684 | 0.4357 | ADDRESS (meas 168.05,103.55) |
| lamp.A10 | lamp | 182.66 | 103.55 | 9.2 | 9.3 | 0.3979 | 0.4357 | ADDRESS (meas 182.47,103.52) |
| lamp.A9 | lamp | 196.97 | 103.55 | 9.2 | 9.3 | 0.4275 | 0.4357 | ADDRESS (meas 196.71,103.57) |
| lamp.A8 | lamp | 211.28 | 103.55 | 9.2 | 9.3 | 0.457 | 0.4357 | ADDRESS (meas 211.04,103.54) |
| lamp.A7 | lamp | 225.59 | 103.55 | 9.2 | 9.3 | 0.4866 | 0.4357 | ADDRESS (meas 225.35,103.54) |
| lamp.A6 | lamp | 239.9 | 103.55 | 9.2 | 9.3 | 0.5161 | 0.4357 | ADDRESS (meas 239.67,103.5) |
| lamp.A5 | lamp | 254.21 | 103.55 | 9.2 | 9.3 | 0.5456 | 0.4357 | ADDRESS (meas 254.13,103.54) |
| lamp.A4 | lamp | 268.52 | 103.55 | 9.2 | 9.3 | 0.5752 | 0.4357 | ADDRESS (meas 268.49,103.72) |
| lamp.A3 | lamp | 282.83 | 103.55 | 9.2 | 9.3 | 0.6047 | 0.4357 | ADDRESS (meas 282.93,103.55) |
| lamp.A2 | lamp | 297.14 | 103.55 | 9.2 | 9.3 | 0.6343 | 0.4357 | ADDRESS (meas 297.12,103.52) |
| lamp.A1 | lamp | 311.45 | 103.55 | 9.2 | 9.3 | 0.6638 | 0.4357 | ADDRESS (meas 311.57,103.56) |
| lamp.A0 | lamp | 325.76 | 103.55 | 9.2 | 9.3 | 0.6934 | 0.4357 | ADDRESS (meas 325.69,103.45) |
| lamp.PARITY_HIGH | lamp | 68.18 | 144.02 | 9.2 | 9.3 | 0.1616 | 0.5911 | parity high byte (meas 67.83,144.02) |
| lamp.PARITY_LOW | lamp | 82.49 | 144.02 | 9.2 | 9.3 | 0.1911 | 0.5911 | parity low byte (meas 82.03,143.96) |
| lamp.D15 | lamp | 111.11 | 144.02 | 9.2 | 9.3 | 0.2502 | 0.5911 | DATA (meas 110.83,143.99) |
| lamp.D14 | lamp | 125.42 | 144.02 | 9.2 | 9.3 | 0.2798 | 0.5911 | DATA (meas 124.88,144.03) |
| lamp.D13 | lamp | 139.73 | 144.02 | 9.2 | 9.3 | 0.3093 | 0.5911 | DATA (meas 139.16,144.07) |
| lamp.D12 | lamp | 154.04 | 144.02 | 9.2 | 9.3 | 0.3389 | 0.5911 | DATA (meas 153.57,144.01) |
| lamp.D11 | lamp | 168.35 | 144.02 | 9.2 | 9.3 | 0.3684 | 0.5911 | DATA (meas 167.9,144.04) |
| lamp.D10 | lamp | 182.66 | 144.02 | 9.2 | 9.3 | 0.3979 | 0.5911 | DATA (meas 182.38,144.0) |
| lamp.D9 | lamp | 196.97 | 144.02 | 9.2 | 9.3 | 0.4275 | 0.5911 | DATA (meas 196.52,143.98) |
| lamp.D8 | lamp | 211.28 | 144.02 | 9.2 | 9.3 | 0.457 | 0.5911 | DATA (meas 210.86,143.9) |
| lamp.D7 | lamp | 225.59 | 144.02 | 9.2 | 9.3 | 0.4866 | 0.5911 | DATA (meas 225.23,144.01) |
| lamp.D6 | lamp | 239.9 | 144.02 | 9.2 | 9.3 | 0.5161 | 0.5911 | DATA (meas 239.56,144.0) |
| lamp.D5 | lamp | 254.21 | 144.02 | 9.2 | 9.3 | 0.5456 | 0.5911 | DATA (meas 254.08,144.01) |
| lamp.D4 | lamp | 268.52 | 144.02 | 9.2 | 9.3 | 0.5752 | 0.5911 | DATA (meas 268.53,144.05) |
| lamp.D3 | lamp | 282.83 | 144.02 | 9.2 | 9.3 | 0.6047 | 0.5911 | DATA (meas 282.85,144.03) |
| lamp.D2 | lamp | 297.14 | 144.02 | 9.2 | 9.3 | 0.6343 | 0.5911 | DATA (meas 297.02,144.01) |
| lamp.D1 | lamp | 311.45 | 144.02 | 9.2 | 9.3 | 0.6638 | 0.5911 | DATA (meas 311.35,144.05) |
| lamp.D0 | lamp | 325.76 | 144.02 | 9.2 | 9.3 | 0.6934 | 0.5911 | DATA (meas 325.5,144.15) |
| lamp.USER_D | lamp | 358.7 | 65.7 | 9.2 | 9.3 | 0.7614 | 0.2903 | ADDRESS-select indicator (meas 358.68,65.66) |
| lamp.SUPER_D | lamp | 358.7 | 77.15 | 9.2 | 9.3 | 0.7614 | 0.3343 | ADDRESS-select indicator (meas 358.74,77.19) |
| lamp.KERNEL_D | lamp | 358.7 | 88.5 | 9.2 | 9.3 | 0.7614 | 0.3779 | ADDRESS-select indicator (meas 358.71,88.68) |
| lamp.CONS_PHY | lamp | 358.7 | 100.15 | 9.2 | 9.3 | 0.7614 | 0.4226 | ADDRESS-select indicator (meas 358.78,100.04) |
| lamp.USER_I | lamp | 390.5 | 65.7 | 9.2 | 9.3 | 0.827 | 0.2903 | ADDRESS-select indicator (meas 390.44,65.72) |
| lamp.SUPER_I | lamp | 390.5 | 77.15 | 9.2 | 9.3 | 0.827 | 0.3343 | ADDRESS-select indicator (meas 390.75,77.12) |
| lamp.KERNEL_I | lamp | 390.5 | 88.5 | 9.2 | 9.3 | 0.827 | 0.3779 | ADDRESS-select indicator (meas 391.11,88.29) |
| lamp.PROG_PHY | lamp | 390.5 | 100.15 | 9.2 | 9.3 | 0.827 | 0.4226 | ADDRESS-select indicator (meas 390.41,100.3) |
| lamp.DATA_PATHS | lamp | 358.7 | 130.55 | 9.2 | 9.3 | 0.7614 | 0.5394 | DATA-select indicator (meas 358.53,130.48) |
| lamp.BUS_REG | lamp | 358.7 | 141.8 | 9.2 | 9.3 | 0.7614 | 0.5826 | DATA-select indicator (meas 358.56,141.85) |
| lamp.UADRS | lamp | 390.5 | 130.55 | 9.2 | 9.3 | 0.827 | 0.5394 | DATA-select indicator (meas 390.44,130.6) |
| lamp.DISPLAY_REG | lamp | 390.5 | 141.8 | 9.2 | 9.3 | 0.827 | 0.5826 | DATA-select indicator (meas 390.42,141.8) |
| knob.addr | knob | 432.3 | 82.9 | 25.0 | 25.0 | 0.9133 | 0.3564 | 8 detents @22.5+45k deg cw from 12h, wraps; hole d13.5 |
| knob.data | knob | 432.3 | 136.1 | 25.0 | 25.0 | 0.9133 | 0.5607 | 8 detents, 4 functions repeated; wraps |
| keyswitch | key | 32.2 | 136.0 | 19.2 | 19.2 | 0.0873 | 0.5603 | OFF 225deg / POWER 315deg / LOCK 45deg (cw from 12h), no wrap |
| tile.SR21 | tile | 25.25 | 170.7 | 13.0 | 17.5 | 0.073 | 0.6935 | rose r~2 |
| tile.SR20 | tile | 39.56 | 170.7 | 13.0 | 17.5 | 0.1025 | 0.6935 | mag r~2 |
| tile.SR19 | tile | 53.87 | 170.7 | 13.0 | 17.5 | 0.1321 | 0.6935 | mag r~2 |
| tile.SR18 | tile | 68.18 | 170.7 | 13.0 | 17.5 | 0.1616 | 0.6935 | mag r~2 |
| tile.SR17 | tile | 82.49 | 170.7 | 13.0 | 17.5 | 0.1911 | 0.6935 | rose r~2 |
| tile.SR16 | tile | 96.8 | 170.7 | 13.0 | 17.5 | 0.2207 | 0.6935 | rose r~2 |
| tile.SR15 | tile | 111.11 | 170.7 | 13.0 | 17.5 | 0.2502 | 0.6935 | rose r~2 |
| tile.SR14 | tile | 125.42 | 170.7 | 13.0 | 17.5 | 0.2798 | 0.6935 | mag r~2 |
| tile.SR13 | tile | 139.73 | 170.7 | 13.0 | 17.5 | 0.3093 | 0.6935 | mag r~2 |
| tile.SR12 | tile | 154.04 | 170.7 | 13.0 | 17.5 | 0.3389 | 0.6935 | mag r~2 |
| tile.SR11 | tile | 168.35 | 170.7 | 13.0 | 17.5 | 0.3684 | 0.6935 | rose r~2 |
| tile.SR10 | tile | 182.66 | 170.7 | 13.0 | 17.5 | 0.3979 | 0.6935 | rose r~2 |
| tile.SR9 | tile | 196.97 | 170.7 | 13.0 | 17.5 | 0.4275 | 0.6935 | rose r~2 |
| tile.SR8 | tile | 211.28 | 170.7 | 13.0 | 17.5 | 0.457 | 0.6935 | mag r~2 |
| tile.SR7 | tile | 225.59 | 170.7 | 13.0 | 17.5 | 0.4866 | 0.6935 | mag r~2 |
| tile.SR6 | tile | 239.9 | 170.7 | 13.0 | 17.5 | 0.5161 | 0.6935 | mag r~2 |
| tile.SR5 | tile | 254.21 | 170.7 | 13.0 | 17.5 | 0.5456 | 0.6935 | rose r~2 |
| tile.SR4 | tile | 268.52 | 170.7 | 13.0 | 17.5 | 0.5752 | 0.6935 | rose r~2 |
| tile.SR3 | tile | 282.83 | 170.7 | 13.0 | 17.5 | 0.6047 | 0.6935 | rose r~2 |
| tile.SR2 | tile | 297.14 | 170.7 | 13.0 | 17.5 | 0.6343 | 0.6935 | mag r~2 |
| tile.SR1 | tile | 311.45 | 170.7 | 13.0 | 17.5 | 0.6638 | 0.6935 | mag r~2 |
| tile.SR0 | tile | 325.76 | 170.7 | 13.0 | 17.5 | 0.6934 | 0.6935 | mag r~2 |
| tile.LOAD_ADRS | tile | 354.38 | 170.7 | 13.0 | 17.5 | 0.7524 | 0.6935 | rose r~2 |
| tile.EXAM | tile | 368.69 | 170.7 | 13.0 | 17.5 | 0.782 | 0.6935 | mag r~2 |
| tile.DEP | tile | 383.0 | 170.7 | 13.0 | 17.5 | 0.8115 | 0.6935 | rose r~2 |
| tile.CONT | tile | 397.31 | 170.7 | 13.0 | 17.5 | 0.8411 | 0.6935 | mag r~2 |
| tile.ENABLE | tile | 411.62 | 166.3 | 13.0 | 7.8 | 0.8706 | 0.6767 | rose half tile r~1.6 |
| tile.HALT | tile | 411.62 | 175.55 | 13.0 | 7.9 | 0.8706 | 0.7122 | rose half tile r~1.6 |
| tile.S_INST | tile | 425.93 | 166.3 | 13.0 | 7.8 | 0.9001 | 0.6767 | mag half tile r~1.6 |
| tile.S_BUS_CYCLE | tile | 425.93 | 175.55 | 13.0 | 7.9 | 0.9001 | 0.7122 | mag half tile r~1.6 |
| tile.START | tile | 440.24 | 170.7 | 13.0 | 17.5 | 0.9297 | 0.6935 | rose r~2 |
| paddle.SR21 | paddle | 25.25 | 205.4 | 13.5 | 37.6 | 0.073 | 0.8268 | slot 0; rose; latching toggle (up=1); crease y=205.4 |
| paddle.SR20 | paddle | 39.56 | 205.4 | 13.5 | 37.6 | 0.1025 | 0.8268 | slot 1; mag; latching toggle (up=1); crease y=205.4 |
| paddle.SR19 | paddle | 53.87 | 205.4 | 13.5 | 37.6 | 0.1321 | 0.8268 | slot 2; mag; latching toggle (up=1); crease y=205.4 |
| paddle.SR18 | paddle | 68.18 | 205.4 | 13.5 | 37.6 | 0.1616 | 0.8268 | slot 3; mag; latching toggle (up=1); crease y=205.4 |
| paddle.SR17 | paddle | 82.49 | 205.4 | 13.5 | 37.6 | 0.1911 | 0.8268 | slot 4; rose; latching toggle (up=1); crease y=205.4 |
| paddle.SR16 | paddle | 96.8 | 205.4 | 13.5 | 37.6 | 0.2207 | 0.8268 | slot 5; rose; latching toggle (up=1); crease y=205.4 |
| paddle.SR15 | paddle | 111.11 | 205.4 | 13.5 | 37.6 | 0.2502 | 0.8268 | slot 6; rose; latching toggle (up=1); crease y=205.4 |
| paddle.SR14 | paddle | 125.42 | 205.4 | 13.5 | 37.6 | 0.2798 | 0.8268 | slot 7; mag; latching toggle (up=1); crease y=205.4 |
| paddle.SR13 | paddle | 139.73 | 205.4 | 13.5 | 37.6 | 0.3093 | 0.8268 | slot 8; mag; latching toggle (up=1); crease y=205.4 |
| paddle.SR12 | paddle | 154.04 | 205.4 | 13.5 | 37.6 | 0.3389 | 0.8268 | slot 9; mag; latching toggle (up=1); crease y=205.4 |
| paddle.SR11 | paddle | 168.35 | 205.4 | 13.5 | 37.6 | 0.3684 | 0.8268 | slot 10; rose; latching toggle (up=1); crease y=205.4 |
| paddle.SR10 | paddle | 182.66 | 205.4 | 13.5 | 37.6 | 0.3979 | 0.8268 | slot 11; rose; latching toggle (up=1); crease y=205.4 |
| paddle.SR9 | paddle | 196.97 | 205.4 | 13.5 | 37.6 | 0.4275 | 0.8268 | slot 12; rose; latching toggle (up=1); crease y=205.4 |
| paddle.SR8 | paddle | 211.28 | 205.4 | 13.5 | 37.6 | 0.457 | 0.8268 | slot 13; mag; latching toggle (up=1); crease y=205.4 |
| paddle.SR7 | paddle | 225.59 | 205.4 | 13.5 | 37.6 | 0.4866 | 0.8268 | slot 14; mag; latching toggle (up=1); crease y=205.4 |
| paddle.SR6 | paddle | 239.9 | 205.4 | 13.5 | 37.6 | 0.5161 | 0.8268 | slot 15; mag; latching toggle (up=1); crease y=205.4 |
| paddle.SR5 | paddle | 254.21 | 205.4 | 13.5 | 37.6 | 0.5456 | 0.8268 | slot 16; rose; latching toggle (up=1); crease y=205.4 |
| paddle.SR4 | paddle | 268.52 | 205.4 | 13.5 | 37.6 | 0.5752 | 0.8268 | slot 17; rose; latching toggle (up=1); crease y=205.4 |
| paddle.SR3 | paddle | 282.83 | 205.4 | 13.5 | 37.6 | 0.6047 | 0.8268 | slot 18; rose; latching toggle (up=1); crease y=205.4 |
| paddle.SR2 | paddle | 297.14 | 205.4 | 13.5 | 37.6 | 0.6343 | 0.8268 | slot 19; mag; latching toggle (up=1); crease y=205.4 |
| paddle.SR1 | paddle | 311.45 | 205.4 | 13.5 | 37.6 | 0.6638 | 0.8268 | slot 20; mag; latching toggle (up=1); crease y=205.4 |
| paddle.SR0 | paddle | 325.76 | 205.4 | 13.5 | 37.6 | 0.6934 | 0.8268 | slot 21; mag; latching toggle (up=1); crease y=205.4 |
| paddle.LAMP_TEST | paddle | 340.07 | 205.4 | 13.5 | 37.6 | 0.7229 | 0.8268 | slot 22; blank; momentary-up; crease y=205.4 |
| paddle.LOAD_ADRS | paddle | 354.38 | 205.4 | 13.5 | 37.6 | 0.7524 | 0.8268 | slot 23; rose; momentary-down; crease y=205.4 |
| paddle.EXAM | paddle | 368.69 | 205.4 | 13.5 | 37.6 | 0.782 | 0.8268 | slot 24; mag; momentary-down; crease y=205.4 |
| paddle.DEP | paddle | 383.0 | 205.4 | 13.5 | 37.6 | 0.8115 | 0.8268 | slot 25; rose; momentary-up; crease y=205.4 |
| paddle.CONT | paddle | 397.31 | 205.4 | 13.5 | 37.6 | 0.8411 | 0.8268 | slot 26; mag; momentary-down; crease y=205.4 |
| paddle.ENABLE_HALT | paddle | 411.62 | 205.4 | 13.5 | 37.6 | 0.8706 | 0.8268 | slot 27; rose; latching (up=ENABLE, down=HALT); crease y=205.4 |
| paddle.SINST_SBUS | paddle | 425.93 | 205.4 | 13.5 | 37.6 | 0.9001 | 0.8268 | slot 28; mag; latching (up=S INST, down=S BUS CYCLE); crease y=205.4 |
| paddle.START | paddle | 440.24 | 205.4 | 13.5 | 37.6 | 0.9297 | 0.8268 | slot 29; rose; momentary-down; crease y=205.4 |
| label."PAR" | label | 168.3 | 56.05 | 6.7 | 2.5 | 0.3683 | 0.2533 | baseline y=57.3, cap 2.5 mm, anchor middle @x=168.3 |
| label."ERR" | label | 168.3 | 58.75 | 6.9 | 2.5 | 0.3683 | 0.2636 | baseline y=60.0, cap 2.5 mm, anchor middle @x=168.3 |
| label."ADRS" | label | 182.7 | 56.05 | 9.0 | 2.5 | 0.398 | 0.2533 | baseline y=57.3, cap 2.5 mm, anchor middle @x=182.7 |
| label."ERR" | label | 182.6 | 58.75 | 6.9 | 2.5 | 0.3978 | 0.2636 | baseline y=60.0, cap 2.5 mm, anchor middle @x=182.6 |
| label."RUN" | label | 197.0 | 56.05 | 7.3 | 2.5 | 0.4275 | 0.2533 | baseline y=57.3, cap 2.5 mm, anchor middle @x=197.0 |
| label."PAUSE" | label | 211.4 | 56.05 | 10.4 | 2.5 | 0.4573 | 0.2533 | baseline y=57.3, cap 2.5 mm, anchor middle @x=211.4 |
| label."MASTER" | label | 225.8 | 56.05 | 12.8 | 2.5 | 0.487 | 0.2533 | baseline y=57.3, cap 2.5 mm, anchor middle @x=225.8 |
| label."USER" | label | 240.1 | 56.05 | 9.0 | 2.5 | 0.5165 | 0.2533 | baseline y=57.3, cap 2.5 mm, anchor middle @x=240.1 |
| label."SUPER" | label | 254.2 | 56.05 | 10.8 | 2.5 | 0.5456 | 0.2533 | baseline y=57.3, cap 2.5 mm, anchor middle @x=254.2 |
| label."KERNEL" | label | 268.5 | 56.05 | 12.5 | 2.5 | 0.5751 | 0.2533 | baseline y=57.3, cap 2.5 mm, anchor middle @x=268.5 |
| label."DATA" | label | 282.6 | 56.05 | 8.0 | 2.5 | 0.6043 | 0.2533 | baseline y=57.3, cap 2.5 mm, anchor middle @x=282.6 |
| label."ADDRESSING" | label | 311.4 | 56.05 | 20.1 | 2.5 | 0.6637 | 0.2533 | baseline y=57.3, cap 2.5 mm, anchor middle @x=311.4 |
| label."16" | label | 297.2 | 58.75 | 3.7 | 2.5 | 0.6344 | 0.2636 | baseline y=60.0, cap 2.5 mm, anchor middle @x=297.2 |
| label."18" | label | 311.5 | 58.75 | 3.7 | 2.5 | 0.6639 | 0.2636 | baseline y=60.0, cap 2.5 mm, anchor middle @x=311.5 |
| label."22" | label | 325.3 | 58.75 | 4.3 | 2.5 | 0.6924 | 0.2636 | baseline y=60.0, cap 2.5 mm, anchor middle @x=325.3 |
| label."ADDRESS" | label | 318.7 | 83.0 | 21.8 | 3.6 | 0.6788 | 0.3568 | baseline y=84.8, cap 3.6 mm, anchor end @x=329.6 |
| label."DATA" | label | 323.85 | 123.7 | 11.3 | 3.6 | 0.6894 | 0.5131 | baseline y=125.5, cap 3.6 mm, anchor end @x=329.5 |
| label."PARITY" | label | 75.3 | 123.6 | 15.9 | 3.6 | 0.1763 | 0.5127 | baseline y=125.4, cap 3.6 mm, anchor middle @x=75.3 |
| label."HIGH" | label | 68.2 | 128.3 | 10.2 | 3.2 | 0.1616 | 0.5307 | baseline y=129.9, cap 3.2 mm, anchor middle @x=68.2 |
| label."LOW" | label | 82.4 | 128.3 | 9.1 | 3.2 | 0.191 | 0.5307 | baseline y=129.9, cap 3.2 mm, anchor middle @x=82.4 |
| label."POWER" | label | 25.6 | 121.6 | 11.3 | 2.6 | 0.0737 | 0.505 | baseline y=122.9, cap 2.6 mm, anchor middle @x=25.6 |
| label."LOCK" | label | 42.2 | 121.6 | 8.6 | 2.6 | 0.108 | 0.505 | baseline y=122.9, cap 2.6 mm, anchor middle @x=42.2 |
| label."OFF" | label | 23.4 | 148.7 | 6.8 | 2.6 | 0.0692 | 0.6091 | baseline y=150.0, cap 2.6 mm, anchor middle @x=23.4 |
| label."USER D" | label | 371.7 | 65.35 | 15.6 | 3.1 | 0.7882 | 0.289 | baseline y=66.9, cap 3.1 mm, anchor start @x=363.9 |
| label."USER I" | label | 402.75 | 65.35 | 13.7 | 3.1 | 0.8523 | 0.289 | baseline y=66.9, cap 3.1 mm, anchor start @x=395.9 |
| label."SUPER D" | label | 372.95 | 76.75 | 18.1 | 3.1 | 0.7908 | 0.3328 | baseline y=78.3, cap 3.1 mm, anchor start @x=363.9 |
| label."SUPER I" | label | 403.75 | 76.75 | 16.1 | 3.1 | 0.8544 | 0.3328 | baseline y=78.3, cap 3.1 mm, anchor start @x=395.7 |
| label."KERNEL D" | label | 374.15 | 88.25 | 20.5 | 3.1 | 0.7932 | 0.3769 | baseline y=89.8, cap 3.1 mm, anchor start @x=363.9 |
| label."KERNEL I" | label | 404.95 | 88.25 | 18.7 | 3.1 | 0.8568 | 0.3769 | baseline y=89.8, cap 3.1 mm, anchor start @x=395.6 |
| label."CONS PHY" | label | 374.4 | 99.55 | 21.0 | 3.1 | 0.7938 | 0.4203 | baseline y=101.1, cap 3.1 mm, anchor start @x=363.9 |
| label."PROG PHY" | label | 405.9 | 99.55 | 20.6 | 3.1 | 0.8588 | 0.4203 | baseline y=101.1, cap 3.1 mm, anchor start @x=395.6 |
| label."DATA" | label | 368.65 | 128.15 | 9.7 | 3.1 | 0.7819 | 0.5301 | baseline y=129.7, cap 3.1 mm, anchor start @x=363.8 |
| label."PATHS" | label | 370.2 | 131.95 | 12.8 | 3.1 | 0.7851 | 0.5447 | baseline y=133.5, cap 3.1 mm, anchor start @x=363.8 |
| label."μ ADRS" | label | 403.45 | 128.25 | 15.5 | 3.1 | 0.8537 | 0.5305 | baseline y=129.8, cap 3.1 mm, anchor start @x=395.7 |
| label."FPP/CPU" | label | 404.45 | 132.35 | 17.5 | 3.1 | 0.8558 | 0.5463 | baseline y=133.9, cap 3.1 mm, anchor start @x=395.7 |
| label."BUS REG" | label | 373.15 | 141.55 | 18.5 | 3.1 | 0.7912 | 0.5816 | baseline y=143.1, cap 3.1 mm, anchor start @x=363.9 |
| label."DISPLAY" | label | 404.05 | 139.65 | 16.9 | 3.1 | 0.855 | 0.5743 | baseline y=141.2, cap 3.1 mm, anchor start @x=395.6 |
| label."REGISTER" | label | 405.45 | 143.75 | 19.7 | 3.1 | 0.8579 | 0.5901 | baseline y=145.3, cap 3.1 mm, anchor start @x=395.6 |
| label."LOAD" | label | 354.1 | 169.25 | 9.0 | 2.5 | 0.7519 | 0.688 | baseline y=170.5, cap 2.5 mm, anchor middle @x=354.1 |
| label."ADRS" | label | 354.1 | 172.05 | 8.9 | 2.5 | 0.7519 | 0.6987 | baseline y=173.3, cap 2.5 mm, anchor middle @x=354.1 |
| label."EXAM" | label | 368.5 | 170.6 | 9.1 | 2.5 | 0.7816 | 0.6932 | baseline y=171.85, cap 2.5 mm, anchor middle @x=368.5 |
| label."DEP" | label | 382.7 | 170.6 | 6.9 | 2.5 | 0.8109 | 0.6932 | baseline y=171.85, cap 2.5 mm, anchor middle @x=382.7 |
| label."CONT" | label | 397.2 | 170.7 | 9.1 | 2.5 | 0.8408 | 0.6935 | baseline y=171.95, cap 2.5 mm, anchor middle @x=397.2 |
| label."START" | label | 440.2 | 170.65 | 10.0 | 2.5 | 0.9296 | 0.6934 | baseline y=171.9, cap 2.5 mm, anchor middle @x=440.2 |
| label."ENABLE" | label | 411.8 | 166.05 | 12.6 | 2.5 | 0.871 | 0.6757 | baseline y=167.3, cap 2.5 mm, anchor middle @x=411.8 |
| label."HALT" | label | 411.6 | 175.35 | 8.0 | 2.5 | 0.8706 | 0.7114 | baseline y=176.6, cap 2.5 mm, anchor middle @x=411.6 |
| label."S INST" | label | 425.9 | 166.05 | 11.6 | 2.5 | 0.9001 | 0.6757 | baseline y=167.3, cap 2.5 mm, anchor middle @x=425.9 |
| label."S BUS" | label | 425.95 | 174.05 | 10.5 | 2.5 | 0.9002 | 0.7064 | baseline y=175.3, cap 2.5 mm, anchor middle @x=425.95 |
| label."CYCLE" | label | 425.8 | 176.95 | 10.2 | 2.5 | 0.8999 | 0.7175 | baseline y=178.2, cap 2.5 mm, anchor middle @x=425.8 |
| label."21" | label | 25.25 | 170.6 | 5.1 | 3.5 | 0.073 | 0.6932 | tile number, baseline 172.35, cap 3.5 |
| label."20" | label | 39.56 | 170.6 | 5.1 | 3.5 | 0.1025 | 0.6932 | tile number, baseline 172.35, cap 3.5 |
| label."19" | label | 53.87 | 170.6 | 5.1 | 3.5 | 0.1321 | 0.6932 | tile number, baseline 172.35, cap 3.5 |
| label."18" | label | 68.18 | 170.6 | 5.1 | 3.5 | 0.1616 | 0.6932 | tile number, baseline 172.35, cap 3.5 |
| label."17" | label | 82.49 | 170.6 | 5.1 | 3.5 | 0.1911 | 0.6932 | tile number, baseline 172.35, cap 3.5 |
| label."16" | label | 96.8 | 170.6 | 5.1 | 3.5 | 0.2207 | 0.6932 | tile number, baseline 172.35, cap 3.5 |
| label."15" | label | 111.11 | 170.6 | 5.1 | 3.5 | 0.2502 | 0.6932 | tile number, baseline 172.35, cap 3.5 |
| label."14" | label | 125.42 | 170.6 | 5.1 | 3.5 | 0.2798 | 0.6932 | tile number, baseline 172.35, cap 3.5 |
| label."13" | label | 139.73 | 170.6 | 5.1 | 3.5 | 0.3093 | 0.6932 | tile number, baseline 172.35, cap 3.5 |
| label."12" | label | 154.04 | 170.6 | 5.1 | 3.5 | 0.3389 | 0.6932 | tile number, baseline 172.35, cap 3.5 |
| label."11" | label | 168.35 | 170.6 | 5.1 | 3.5 | 0.3684 | 0.6932 | tile number, baseline 172.35, cap 3.5 |
| label."10" | label | 182.66 | 170.6 | 5.1 | 3.5 | 0.3979 | 0.6932 | tile number, baseline 172.35, cap 3.5 |
| label."9" | label | 196.97 | 170.6 | 3.5 | 3.5 | 0.4275 | 0.6932 | tile number, baseline 172.35, cap 3.5 |
| label."8" | label | 211.28 | 170.6 | 3.5 | 3.5 | 0.457 | 0.6932 | tile number, baseline 172.35, cap 3.5 |
| label."7" | label | 225.59 | 170.6 | 3.5 | 3.5 | 0.4866 | 0.6932 | tile number, baseline 172.35, cap 3.5 |
| label."6" | label | 239.9 | 170.6 | 3.5 | 3.5 | 0.5161 | 0.6932 | tile number, baseline 172.35, cap 3.5 |
| label."5" | label | 254.21 | 170.6 | 3.5 | 3.5 | 0.5456 | 0.6932 | tile number, baseline 172.35, cap 3.5 |
| label."4" | label | 268.52 | 170.6 | 3.5 | 3.5 | 0.5752 | 0.6932 | tile number, baseline 172.35, cap 3.5 |
| label."3" | label | 282.83 | 170.6 | 3.5 | 3.5 | 0.6047 | 0.6932 | tile number, baseline 172.35, cap 3.5 |
| label."2" | label | 297.14 | 170.6 | 3.5 | 3.5 | 0.6343 | 0.6932 | tile number, baseline 172.35, cap 3.5 |
| label."1" | label | 311.45 | 170.6 | 3.5 | 3.5 | 0.6638 | 0.6932 | tile number, baseline 172.35, cap 3.5 |
| label."0" | label | 325.76 | 170.6 | 3.5 | 3.5 | 0.6934 | 0.6932 | tile number, baseline 172.35, cap 3.5 |

## Appendix B — regenerate / reuse
```
R4=$SCRATCH/r4-visual
python3 -I $R4/scripts/gen_panel_svg.py out.svg '{"lit":{"L_RUN":1},"poses":{"SR0":"back"},"logo_path":"'$R4'/work/logo_pdp1170_path.svgfrag"}'   # prototype SVG (state JSON optional)
python3 -I $R4/scripts/gen_table.py $R4/scripts $R4/work/panel_elements_mm.json out.json out.md
```
State JSON keys: lit {lampId: 0..1}, poses {SRn|LOAD_ADRS|EXAM|DEP|CONT|ENABLE_HALT|SINST_SBUS|START|LAMP_TEST: rest|back|fwd}, key off|power|lock, addr_knob, data_knob, logo_path.
