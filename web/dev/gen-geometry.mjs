#!/usr/bin/env node
// Generates web/assets/panel/geometry.json, the single source of panel
// geometry for the browser renderer, from research r4's element table
// (docs/research/r4-assets/panel1170_elements.json) and the bezel artwork
// produced by build-fonts.py (web/dev/panel-art.json).
//
// Usage: node web/dev/gen-geometry.mjs [--check]
//   --check  exit 1 if geometry.json is not up to date (no write)
//
// All coordinates are millimetres in the r4 frame (origin = top-left of the
// 464 x 186 mm indicator panel); the SVG viewBox is the whole console bezel.

import { readFileSync, writeFileSync } from 'node:fs';
import { dirname, join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const repo = join(here, '..', '..');
const TABLE = join(repo, 'docs', 'research', 'r4-assets', 'panel1170_elements.json');
const ART = join(here, 'panel-art.json');
const OUT = join(repo, 'web', 'assets', 'panel', 'geometry.json');

const table = JSON.parse(readFileSync(TABLE, 'utf8'));
const art = JSON.parse(readFileSync(ART, 'utf8'));
const els = table.elements;

function fail(msg) {
  console.error(`gen-geometry: ${msg}`);
  process.exit(2);
}
const r2 = (v) => Math.round(v * 100) / 100;
const r3 = (v) => Math.round(v * 1000) / 1000;
const byId = new Map(els.map((e) => [e.id, e]));
function get(id) {
  const e = byId.get(id);
  if (!e) fail(`element ${id} missing from the table`);
  return e;
}
const ofKind = (k) => els.filter((e) => e.kind === k);
const box = (e) => ({ x0: r2(e.cx - e.w / 2), y0: r2(e.cy - e.h / 2), x1: r2(e.cx + e.w / 2), y1: r2(e.cy + e.h / 2) });

const PITCH = table.frame_mm.pitch;
const COL0 = table.frame_mm.col0;
const col = (k) => r2(COL0 + PITCH * k);
const [bx0, by0, bx1, by1] = table.frame_mm.bezel;
const viewBox = [bx0, by0, r2(bx1 - bx0), r2(by1 - by0)];

// ---------------------------------------------------------------- lamps
// Protocol order (docs/PROTOCOL.md §1.1); r4 calls lamp 48 "DATA_SPACE".
const LAMP_IDS = [
  ...Array.from({ length: 22 }, (_, i) => `A${i}`),
  ...Array.from({ length: 16 }, (_, i) => `D${i}`),
  'PARITY_LOW', 'PARITY_HIGH', 'PAR_ERR', 'ADRS_ERR', 'RUN', 'PAUSE', 'MASTER', 'USER', 'SUPER', 'KERNEL',
  'DATA_SPACE', 'ADRS16', 'ADRS18', 'ADRS22', 'USER_D', 'SUPER_D', 'KERNEL_D', 'CONS_PHY', 'USER_I', 'SUPER_I',
  'KERNEL_I', 'PROG_PHY', 'DATA_PATHS', 'BUS_REG', 'UADRS', 'DISPLAY_REG',
];
const LAMP_NAMES = { DATA_SPACE: 'DATA', UADRS: 'µADRS FPP/CPU', DISPLAY_REG: 'DISPLAY REGISTER' };
if (LAMP_IDS.length !== 64) fail('lamp id list must have 64 entries');
if (ofKind('lamp').length !== 64) fail(`expected 64 lamps in the table, found ${ofKind('lamp').length}`);
const lamps = LAMP_IDS.map((id, i) => {
  const e = get(`lamp.${id}`);
  return {
    i, id, name: LAMP_NAMES[id] ?? id.replace(/_/g, ' '),
    cx: e.cx, cy: e.cy, w: e.w, h: e.h,
    group: /^A\d/.test(id) ? 'address' : /^D\d/.test(id) ? 'data' : i >= 52 ? 'select' : i <= 39 ? 'parity' : 'status',
  };
});

// ---------------------------------------------------------------- paddles and tiles
const SLOTS = [...Array.from({ length: 22 }, (_, i) => `SR${21 - i}`),
  'LAMP_TEST', 'LOAD_ADRS', 'EXAM', 'DEP', 'CONT', 'ENABLE_HALT', 'SINST_SBUS', 'START'];
const PADDLE_TYPE = {
  LAMP_TEST: { type: 'momentary', dir: 'up', label: 'LAMP TEST' },
  LOAD_ADRS: { type: 'momentary', dir: 'down', label: 'LOAD ADRS' },
  EXAM: { type: 'momentary', dir: 'down', label: 'EXAM' },
  DEP: { type: 'momentary', dir: 'up', label: 'DEP' },
  CONT: { type: 'momentary', dir: 'down', label: 'CONT' },
  ENABLE_HALT: { type: 'latch', dir: 'down', label: 'ENABLE/HALT', up: 'enable', down: 'halt' },
  SINST_SBUS: { type: 'latch', dir: 'down', label: 'S INST/S BUS CYCLE', up: 'inst', down: 'bus' },
  START: { type: 'momentary', dir: 'down', label: 'START' },
};
// The photographed paddle row runs a little wider than the printed columns
// above it (the paddles stand proud of the sheet): slot k spans 14.375 mm from
// 16.25 + 14.375 k. Measured per paddle on the photo layers, in slot order:
// how far the face's edges move in at the waist (left, right), the height of
// the crease, of the streak on the rounded bend above it, and that streak's
// strength (0 = only a soft band).
const ROW_X0 = 16.25;
const ROW_PITCH = 14.375;
const PADDLE_ART = [
  [0.25, 4.9, 207.05, 202.5, 0], [1.05, 4.1, 206.15, 202, 0], [1.2, 3.95, 207.95, 202.5, 0], [0.95, 4.2, 208.35, 203.5, 0], [1.1, 4.05, 207.65, 203, 0],
  [1.7, 3.45, 207.65, 203.2, 0], [1.8, 3.35, 207.45, 203, 0], [1.6, 3.55, 207.95, 203.5, 0], [1.9, 3.25, 207.75, 203, 0], [1.7, 3.45, 207.75, 203.5, 0],
  [2.15, 3, 207.75, 203, 0], [1.95, 3.2, 208.05, 203.7, 0], [1.8, 3.35, 207.95, 203.5, 0], [2.6, 2.55, 207.75, 203, 0], [3.1, 2.05, 207.35, 203.5, 0],
  [3.1, 2.05, 207.55, 203.5, 0], [3.3, 1.85, 207.25, 203, 0], [4, 1.15, 207.15, 203.2, 0], [3.9, 1.25, 207.65, 203.5, 0], [4.2, 0.95, 206.65, 203, 0.15],
  [3.9, 1.25, 206.45, 202.7, 0.13], [4, 1.15, 208.05, 204.2, 0.16], [4.85, 0.3, 207.25, 203, 0], [5.05, 0.1, 208.15, 204, 0.5], [4.65, 0.5, 208.05, 204.2, 0.5],
  [4.75, 0.4, 206.65, 203, 0.34], [4.95, 0.2, 209.55, 206.2, 0.66], [5.05, 0.1, 207.65, 203.6, 0.72], [4.85, 0.3, 209.05, 205, 0.92], [4.65, 0.5, 208.05, 204.2, 0.55],
];
const colorOf = (note) => (/\brose\b/.test(note) ? 'rose' : /\bmag\b/.test(note) ? 'mag' : /\bblank\b/.test(note) ? 'blank' : fail(`no colour in "${note}"`));
const TILE_TOP = 161.95; // full tiles: 170.7 -/+ 8.75
const paddleEls = ofKind('paddle');
if (paddleEls.length !== 30) fail('expected 30 paddles');
const paddles = SLOTS.map((name, slot) => {
  const e = get(`paddle.${name}`);
  if (Math.abs(e.cx - col(slot)) > 0.05) fail(`paddle ${name} not on slot ${slot}`);
  const bottom = r2(e.cy + 18.8);
  const p = {
    slot, name, color: colorOf(e.note), cx: e.cx,
    hit: { x0: r2(ROW_X0 + ROW_PITCH * slot), x1: r2(ROW_X0 + ROW_PITCH * (slot + 1)), y0: TILE_TOP, y1: r2(bottom + 0.4) },
    art: {
      x0: r2(ROW_X0 + ROW_PITCH * slot), x1: r2(ROW_X0 + ROW_PITCH * (slot + 1)),
      insetL: PADDLE_ART[slot][0], insetR: PADDLE_ART[slot][1], crease: PADDLE_ART[slot][2], streak: PADDLE_ART[slot][3], gain: PADDLE_ART[slot][4],
    },
  };
  if (name.startsWith('SR')) Object.assign(p, { type: 'sr', bit: Number(name.slice(2)), label: `SR ${name.slice(2)}` });
  else Object.assign(p, PADDLE_TYPE[name]);
  return p;
});

const tileEls = ofKind('tile');
if (tileEls.length !== 31) fail('expected 31 tiles');
// The printed row as on the photo: tiles in groups, each group rounded only at
// its outer corners. Radii are [top left, top right, bottom right, bottom left].
const R_TILE = 2.6;
const R_SMALL = 0.4;
const TILE_CORNERS = {
  LOAD_ADRS: [R_TILE, R_TILE, R_SMALL, R_TILE], EXAM: [R_TILE, R_TILE, R_SMALL, R_SMALL],
  DEP: [R_TILE, R_TILE, R_SMALL, R_SMALL], CONT: [R_TILE, R_TILE, R_SMALL, R_SMALL],
  ENABLE: [R_TILE, R_TILE, R_SMALL, R_SMALL], HALT: [R_SMALL, R_SMALL, R_SMALL, R_SMALL],
  S_INST: [R_SMALL, R_TILE, R_SMALL, R_SMALL], S_BUS_CYCLE: [R_SMALL, R_SMALL, R_TILE, R_SMALL],
  START: [R_TILE, R_TILE, R_TILE, R_TILE],
};
const tiles = tileEls.map((e) => {
  const name = e.id.slice('tile.'.length);
  const b = box(e);
  if (b.y0 < 162) b.y0 = TILE_TOP - 0.2;
  if (name === 'ENABLE' || name === 'S_INST') b.y0 = 162.1;
  let corners = TILE_CORNERS[name];
  const m = /^SR(\d+)$/.exec(name);
  if (m) {
    const bit = +m[1];
    if (bit === 21) corners = [R_TILE, R_TILE, R_TILE, R_TILE];
    else if (bit % 3 === 2) corners = [R_TILE, R_SMALL, R_SMALL, R_TILE]; // first of a group: 20, 17, 14, ...
    else if (bit % 3 === 0) corners = [R_SMALL, R_TILE, R_TILE, R_SMALL]; // last of a group: 18, 15, 12, ...
    else corners = [R_SMALL, R_SMALL, R_SMALL, R_SMALL];
  }
  return { name, color: colorOf(e.note), ...b, corners };
});

// ---------------------------------------------------------------- labels
// Label notes look like: 'baseline y=57.3, cap 2.5 mm, anchor middle @x=168.3'
// or (tile numbers) 'tile number, baseline 172.35, cap 3.5'.
const labels = ofKind('label').map((e) => {
  const text = e.id.match(/^label\."(.*)"$/)?.[1] ?? fail(`bad label id ${e.id}`);
  let m;
  if ((m = e.note.match(/baseline y=([\d.]+), cap ([\d.]+) mm, anchor (start|middle|end) @x=([\d.]+)/))) {
    return { text, x: +m[4], y: +m[1], cap: +m[2], width: e.w, anchor: m[3] };
  }
  if ((m = e.note.match(/tile number, baseline ([\d.]+), cap ([\d.]+)/))) {
    return { text, x: e.cx, y: +m[1], cap: +m[2], width: e.w, anchor: 'middle', tile: true };
  }
  return fail(`cannot parse label note "${e.note}"`);
});
if (labels.length !== 71) fail('expected 71 labels');
// Ink width -> SVG textLength (advance width incl. side bearings) and anchor
// offset, from the Archivo instance's real metrics (build-fonts.py).
// Multi-word labels get extra word spacing: the panel's word gaps are wider
// than Archivo's space. Tile numbers keep their natural width: their table
// widths are placeholders (every single digit is listed as 3.5 mm).
const WORD_SPACING = 0.45; // em
const UPM = 1000;
// The table's sizes come from a scan whose backlight bloom widens every glyph:
// compared with the ink on the photo each label is 1.25 mm too wide and 0.4 mm
// too tall, whatever its size.
const BLOOM_WIDTH = 1.25;
const BLOOM_CAP = 0.4;
// Where the print sits on the photo, by group, on top of the sheet's transform.
function labelNudge(l) {
  const { text, x, y } = l;
  if (l.tile) return [-0.3, -0.22];
  if (y < 58) return [0.58, 0.25]; // status row
  if (y < 61) return [0.58, 0.16];
  if (text === 'ADDRESS') return [-0.28, 0.36];
  if (text === 'DATA' && y < 127 && x < 340) return [-0.3, 0.08];
  if (text === 'PARITY') return [0, -0.32];
  if (text === 'HIGH' || text === 'LOW') return [-0.1, -0.27];
  if (text === 'POWER' || text === 'LOCK') return [-0.1, -0.3];
  if (text === 'OFF') return [-0.42, -0.44];
  if (y < 102) return [1.1, 0.32]; // ADDRESS knob's box
  if (y < 150) return [0.9, 0.14]; // DATA knob's box
  return [0.08, 0.05]; // the words on the control tiles
}
// The upper line of a two-line label, which moves away from the lower one when
// labels are enlarged on small panels.
const UPPER_LINES = new Set(['PAR', 'ADDRESSING', 'LOAD', 'S BUS', 'μ ADRS', 'DISPLAY']);
for (const l of labels) {
  if (UPPER_LINES.has(l.text) || (l.text === 'ADRS' && l.y < 100) || (l.text === 'DATA' && l.x > 340)) l.upper = true;
  const [dx, dy] = labelNudge(l);
  l.x = r3(l.x + dx);
  l.y = r2(l.y + dy);
  l.cap = r2(l.cap - BLOOM_CAP);
  if (l.tile || /^\d+$/.test(l.text)) {
    delete l.width; // numbers keep the font's natural width
    continue;
  }
  l.width = r2(l.width - BLOOM_WIDTH);
  const lm = art.labelMetrics?.[l.text];
  // the gap beside the µ is a narrow one
  const gap = l.text.startsWith('μ') ? 0.12 : WORD_SPACING;
  const spaces = (l.text.match(/ /g) || []).length;
  if (spaces) l.wordSpacing = gap;
  if (lm && lm.ink > 0) {
    const extra = spaces * gap * UPM;
    const k = l.width / (lm.ink + extra); // mm per font unit, horizontally
    l.textLength = r3((lm.adv + extra) * k);
    if (l.anchor === 'start') l.x = r3(l.x - lm.lsb * k);
    else if (l.anchor === 'end') l.x = r3(l.x + lm.rsb * k);
    else l.x = r3(l.x - ((lm.lsb - lm.rsb) / 2) * k);
  } else {
    l.textLength = r3(l.width + 0.12 * l.cap);
  }
}

// ---------------------------------------------------------------- boxes, bars, cells, notches
// Box edges as measured on the photo where the table's scan-based ones differ.
// The status row is one long bar cut in four: no gaps between the lamps of a group,
// only the ends of the row are round. Radii: [top left, top right, bottom right, bottom left].
const R_END = 2.8;
const R_CUT = 0.6;
const BOX_EDGES = {
  status1: { x0: 162.68, x1: 189.56, y0: 54.3, y1: 71.9, corners: [R_END, R_CUT, R_CUT, R_END] },
  status2: { x0: 190.75, x1: 232.87, y0: 54.3, y1: 71.9, corners: [R_CUT, R_CUT, R_CUT, R_CUT] },
  status3: { x0: 233.62, x1: 275.47, y0: 54.3, y1: 71.9, corners: [R_CUT, R_CUT, R_CUT, R_CUT] },
  status4: { x0: 276.77, x1: 331.55, y0: 54.3, y1: 71.9, corners: [R_CUT, R_END, R_END, R_CUT] },
  parity: { x0: 61.5, x1: 88.9, y0: 120.8, y1: 150, r: 2.9 },
  knob_addr: { x0: 348.3, x1: 446.9, y0: 54.65, y1: 109.75 },
  knob_data: { x0: 348.2, x1: 446.8, y0: 121.05, y1: 150.55 },
};
const boxes = ofKind('box').map((e) => {
  const id = e.id.slice('box.'.length);
  const r = +(e.note.match(/r~([\d.]+)/)?.[1] ?? 2.2);
  return { id, ...box(e), r, ...BOX_EDGES[id] };
});
const notchX = ofKind('notch').map((e) => r2(e.cx - 0.35));
const bars = ofKind('bar').map((e) => {
  const id = e.id.slice('bar.'.length);
  const b = box(e);
  const first = id === 'A21' || id === 'D15';
  const last = id === 'A2_0' || id === 'D2_0';
  const notches = id.startsWith('D') ? notchX.filter((x) => x > b.x0 + 3 && x < b.x1 - 3) : [];
  // the bars' heights as measured on the photo
  const edges = id.startsWith('D') ? { y0: 120.4, y1: 127.1 } : { y0: 80.1, y1: 86.4 };
  return { id, color: colorOf(e.note), ...b, ...edges, rl: first ? 2.4 : 0, rr: last ? 2.4 : 0, notches };
});
// Lamp cells under the ADDRESS/DATA bars: black, fused under the bar, then
// separated by ~0.95 mm gaps, rounded bottom corners. "sep" is where the
// gaps begin.
const cells = ofKind('cells').map((e) => {
  const id = e.id.slice('cells.'.length);
  const b = box(e);
  const isData = id.startsWith('D');
  const bar = bars.find((x) => Math.abs((x.x0 + x.x1) / 2 - e.cx) < 0.5 && x.id.startsWith(isData ? 'D' : 'A'));
  const y0 = bar ? bar.y1 : b.y0;
  const cols = lamps.filter((l) => l.group === (isData ? 'data' : 'address') && l.cx > b.x0 && l.cx < b.x1).map((l) => l.cx);
  return { id, x0: b.x0, x1: b.x1, y0, y1: isData ? 150.1 : 109.5, r: 2.4, sep: r2(y0 + (isData ? 4.4 : 4.0)), cols: cols.sort((a, c) => a - c) };
});
// The PARITY box is split below its labels, one cell per lamp.
const parityBox = boxes.find((b) => b.id === 'parity');
const parityCell = {
  ...parityBox, sep: 130.4,
  cols: lamps.filter((l) => l.group === 'parity' && l.cx > parityBox.x0 && l.cx < parityBox.x1).map((l) => l.cx).sort((a, c) => a - c),
};

// ---------------------------------------------------------------- knobs and key
const ADDR_POS = ['PROG PHY', 'CONS PHY', 'KERNEL D', 'SUPER D', 'USER D', 'USER I', 'SUPER I', 'KERNEL I'];
const ADDR_LAMP = [59, 55, 54, 53, 52, 56, 57, 58];
const DATA_POS = ['BUS REG', 'DATA PATHS', 'µADRS FPP/CPU', 'DISPLAY REGISTER'];
const DATA_LAMP = [61, 60, 62, 63];
const knobs = ['addr', 'data'].map((name) => {
  const e = get(`knob.${name}`);
  const angles = Array.from({ length: 8 }, (_, k) => 22.5 + 45 * k);
  return {
    name, label: name === 'addr' ? 'ADDRESS' : 'DATA', r: e.w / 2, angles,
    // Measured on the photo: the dome's centre, and the pointer's pivot on the
    // knob's top, which stands proud of the panel and so shows up and to the right.
    cx: name === 'addr' ? 434 : 433.95, cy: name === 'addr' ? 85 : 136.65,
    px: name === 'addr' ? 435.6 : 435.55, py: name === 'addr' ? 83.75 : 135.4,
    positions: name === 'addr' ? ADDR_POS : Array.from({ length: 8 }, (_, k) => DATA_POS[k % 4]),
    lamps: name === 'addr' ? ADDR_LAMP : Array.from({ length: 8 }, (_, k) => DATA_LAMP[k % 4]),
  };
});
const keyEl = get('keyswitch');
const key = {
  cx: 31.3, cy: 136, r: keyEl.w / 2, positions: ['off', 'power', 'lock'], // centre as measured on the photo
  angles: { off: 225, power: 315, lock: 405 }, // LOCK = 45 deg, written as 405 so turns never wrap
};

// ---------------------------------------------------------------- pdp11/70 logo
// Measured on the 300 dpi scan (r4 shots/scan_zoom_logo_pdp1170.png, 11.811 px/mm,
// de-rotated): letterform stroke W = 21.5 px; bowls/0 are rings with outer
// radius 57.5 and inner radius 36.0 px, all centred on y = 143.4 px; the outline
// stroke is ~5 px. "pdp" and "70" are outlined (union outline), "11" is solid,
// "/" is a single thin line. Coordinates below are scan px; the transform maps
// them so that the logo is centred on the r4 element (49.3, 62.9).
const LOGO_PX = 1 / 11.811;
const logoEl = get('logo_pdp1170');
const L = {
  cy: 143.4, R: 57.5, r: 36.0, t: 5.0,
  top: 86.0, bottom: 200.8, asc: 42.0, desc: 247.0,
  bbox: { x0: 22, y0: 40, x1: 737, y1: 249.5 },
};
const logoPx = {
  t: L.t,
  rings: [
    { cx: 84.0, cy: L.cy, R: L.R, r: L.r }, // p
    { cx: 199.0, cy: L.cy, R: L.R, r: L.r }, // d
    { cx: 314.5, cy: L.cy, R: L.R, r: L.r }, // p
    { cx: 675.5, cy: L.cy, R: L.R, r: L.r }, // 0
  ],
  stems: [
    { x0: 84.0 - L.R, x1: 84.0 - L.r, y0: L.top, y1: L.desc }, // p descender (left)
    { x0: 199.0 + L.r, x1: 199.0 + L.R, y0: L.asc, y1: L.bottom }, // d ascender (right)
    { x0: 314.5 - L.R, x1: 314.5 - L.r, y0: L.top, y1: L.desc }, // p descender
  ],
  // "7": top bar + diagonal, outline centre lines (union outline like the rings)
  seven: [[536.5, 86.0], [630.0, 86.0], [630.0, 100.0], [581.5, 200.8], [555.5, 200.8], [601.0, 106.5], [536.5, 106.5]],
  // "11": solid; stem 28 px wide with the flag's top edge curving down to the left
  ones: [400, 464].map((x) => ({
    d: `M${x + 4},84.6 L${x + 28},84.6 L${x + 28},201.4 L${x},201.4 L${x},123.4 L${x - 32},123.4 L${x - 32},104.2 ` +
       `L${x - 22},103.6 Q${x - 6},101.5 ${x},91.5 Z`,
  })),
  slash: { x0: 482.5, y0: 247.0, x1: 544.5, y1: 40.5 },
};
const logo = {
  transform: { scale: r3(LOGO_PX * 1), tx: 0, ty: 0 },
  ...logoPx,
};
{
  const s = LOGO_PX;
  const cxPx = (L.bbox.x0 + L.bbox.x1) / 2;
  const cyPx = (L.bbox.y0 + L.bbox.y1) / 2;
  // the small offsets bring it onto the photo, together with the sheet's transform
  logo.transform = { scale: Number(s.toFixed(6)), tx: r3(logoEl.cx - cxPx * s + 0.1), ty: r3(logoEl.cy - cyPx * s + 0.37) };
  logo.ink = '#dfe1da'; // the backlit logo reads a little dimmer than the labels
}

// ---------------------------------------------------------------- frame
// The moulding as measured on the photo. Its outline stays 1 mm inside the
// viewBox: the object keeps an edge of its own where a window cuts it off.
const frame = {
  bezel: { x0: r2(bx0 + 1), y0: r2(by0 + 0.7), x1: r2(bx1 - 1), y1: r2(by1 - 0.7), r: 0.9, taper: { y0: 208, y1: 230, dx: 1.5 } },
  chamfer: { y: -7.7 },
  strip: { x0: -3.6, y0: -2.6, x1: 300.6, y1: 16.35, r: 3.2 },
  // The dark panel's edge and the top and right walls' outer edge, traced on the
  // photo: nothing on the object is quite straight.
  window: {
    top: [[12, 27.8], [20, 27.8], [40, 27.95], [60, 28.1], [100, 28.2], [150, 28.35], [200, 28.35], [250, 28.35], [300, 28.5], [350, 28.45], [420, 28.3], [445, 28.25], [455, 28.25], [457.8, 28.9]],
    right: [[459.7, 30], [460.9, 31], [461.7, 32], [462.3, 33], [462.9, 34], [463.4, 35], [463.8, 36], [464, 40], [463.9, 60], [463.6, 100], [463.4, 140], [463.3, 170],
      [463, 175], [462.8, 180], [462.2, 182], [460.8, 184], [459.5, 185.2], [458, 186.2], [455.5, 187.1], [452, 187.3]],
    left: [[12, 187.5], [8, 187.3], [6, 187], [4, 186], [2.3, 184.5], [1.6, 183], [1, 181.8], [0.5, 180], [0.1, 178], [0.1, 150], [0.3, 120], [0.5, 100], [0.7, 70], [0.9, 50], [1.05, 40], [1.2, 37.2], [1.55, 34.9], [2.2, 32.9], [3.2, 31.1], [4.6, 29.6], [6.4, 28.5], [8.6, 27.95]],
  },
  wall: {
    top: [[5, 22.95], [60, 23.05], [100, 23.2], [200, 23.3], [300, 23.4], [420, 23.3], [455, 23.2], [460, 23.45]],
    right: [[463, 24.05], [464.7, 25], [465.9, 26], [466.8, 27], [467.6, 28], [468.1, 29], [468.5, 30], [468.6, 32], [468.7, 40], [469, 60], [469.1, 100], [469.3, 140], [469.5, 170], [469.7, 186], [469.8, 204.6]],
  },
  // the recess the paddles stand in; its top edge is the dark panel's lower edge
  bay: { x0: 15.5, x1: 448.3, y0L: 186.5, y0R: 185.2 },
  // the fold between the pockets and the shelf
  ledge: { y0: 204.4, y1: 207.7 },
};

// ---------------------------------------------------------------- colours
const colors = {
  ...table.colors,
  panel: '#161115', box: '#0d0d0d', ink: '#f0efe9',
  window_unlit: '#170f0d',
  lamp_core: '#ff3b2a', lamp_mid: '#e8001a', lamp_rim: '#8a0010',
};

// The logo's seven letters as strokes, x from their tile's left edge (mm), on
// the photo's baseline 10: "g" and "a" are double-storeyed, unlike any font here.
// The tagline's words stand a little differently on the photo: [from, to] in mm.
const TAG_WORDS = [[65.2, 66.1], [87.14, 87.6], [90.83, 91.24], [131.65, 131.1], [134.4, 134.5], [176.65, 177.24], [179.95, 180.86], [184.17, 184.5], [223.65, 222.36],
  [226.17, 224.86], [279.4, 279.76]];
const wordX = (x) => {
  const i = TAG_WORDS.findIndex(([a], k) => k > 0 && x <= a);
  const [a0, b0] = TAG_WORDS[(i < 0 ? TAG_WORDS.length - 1 : i) - 1];
  const [a1, b1] = TAG_WORDS[i < 0 ? TAG_WORDS.length - 1 : i];
  return b0 + ((x - a0) * (b1 - b0)) / (a1 - a0);
};
/** A path with absolute commands only (what build-fonts.py writes), its x coordinates mapped. */
function mapPathX(d, f) {
  return d.replace(/([MLHQC])([^MLHVQCZ]*)/g, (_, cmd, args) => {
    const n = args.trim().split(/[\s,]+/).map(Number);
    const every = cmd === 'H' ? 1 : 2;
    return cmd + n.map((v, i) => (i % every === 0 ? r2(f(v)) : v)).join(' ');
  });
}
const DIGITAL_LETTERS = [
  'M4.8,7.25 C4.35,6.75 3.75,6.55 3.25,6.58 C2.35,6.62 1.85,7.4 1.85,8.3 C1.85,9.2 2.4,9.98 3.25,9.98 C3.85,9.98 4.4,9.7 4.8,9.15 M4.95,3.4 L4.95,10.1',
  'M3.25,6.55 L3.25,10.1 M3.25,4 L3.25,4.8',
  'M4.1,7.85 A1.15,1.3 0 1 1 1.8,7.85 A1.15,1.3 0 1 1 4.1,7.85 M3.85,7 L5.15,6.5 M2.25,9.05 C1.85,9.4 2,9.9 2.65,9.95 L3.75,10 C4.55,10.05 4.9,10.35 4.9,10.75 C4.9,11.3 4.05,11.55 3,11.55 C1.95,11.55 1.25,11.3 1.25,10.8 C1.25,10.45 1.65,10.2 2.25,10.1',
  'M3.25,6.55 L3.25,10.1 M3.25,4 L3.25,4.8',
  'M3.2,4.5 L3.2,9.1 C3.2,9.75 3.55,9.98 4,9.98 L4.6,9.92 M2.05,6.62 L4.5,6.62',
  'M1.95,7.05 C2.25,6.7 2.7,6.5 3.2,6.5 C4.05,6.5 4.45,6.95 4.45,7.7 L4.45,10.1 M4.45,8.15 L3.25,8.3 C2.35,8.4 1.8,8.7 1.8,9.2 C1.8,9.72 2.25,9.98 2.8,9.98 C3.45,9.98 4.1,9.68 4.45,9.2',
  'M3.45,3.4 L3.45,10.1',
];
// The printed sheet lies a little rotated and stretched against the table's
// frame on the photo the bezel was measured on: x' = a x + c y + e, y' = b x + d y + f.
const sheet = [1.00225, -0.003, 0.003, 1, -1.38, 0.85];
const geometry = {
  version: 1,
  generatedBy: 'web/dev/gen-geometry.mjs',
  source: relative(repo, TABLE),
  units: 'mm',
  viewBox, pitch: PITCH, col0: COL0,
  colors, frame, sheet, boxes, bars, cells, parityCell,
  notch: { base: 6.85, top: 1.65, h: 3.95 },
  labelFont: { family: 'Archivo Panel', capHeight: art.labelCapHeight ?? 0.686, weight: 600, tileWeight: 500 },
  tiles, labels, lamps, paddles, knobs, key, logo,
  art: {
    // Measured on the photo: the tagline's x-height is 4.4 mm on baseline 10.1
    // (the source lettering is 3.8 mm on 9.9), the "digital" letters are narrow
    // and stand on baseline 10 in contiguous tiles.
    tagline: { d: mapPathX(art.tagline.d, wordX), sy: 1.1, ty: r3(10.1 - 9.9 * 1.1), stroke: 0.34 },
    digital: {
      tiles: { x0: 16.04, pitch: 6.652, y0: 0, y1: 13.28, stroke: 0.66 },
      letters: DIGITAL_LETTERS,
    },
  },
};

const text = JSON.stringify(geometry, null, 1) + '\n';
if (process.argv.includes('--check')) {
  let cur = '';
  try { cur = readFileSync(OUT, 'utf8'); } catch { /* missing */ }
  if (cur !== text) {
    console.error('geometry.json is out of date: run node web/dev/gen-geometry.mjs');
    process.exit(1);
  }
  console.log('geometry.json is up to date');
} else {
  writeFileSync(OUT, text);
  console.log(`wrote ${relative(repo, OUT)} (${text.length} bytes): ${lamps.length} lamps, ${paddles.length} paddles, ${tiles.length} tiles, ${labels.length} labels`);
}
