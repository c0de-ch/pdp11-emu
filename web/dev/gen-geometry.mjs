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
const colorOf = (note) => (/\brose\b/.test(note) ? 'rose' : /\bmag\b/.test(note) ? 'mag' : /\bblank\b/.test(note) ? 'blank' : fail(`no colour in "${note}"`));
const TILE_TOP = 161.95; // full tiles: 170.7 -/+ 8.75
const paddleEls = ofKind('paddle');
if (paddleEls.length !== 30) fail('expected 30 paddles');
const paddles = SLOTS.map((name, slot) => {
  const e = get(`paddle.${name}`);
  if (Math.abs(e.cx - col(slot)) > 0.05) fail(`paddle ${name} not on slot ${slot}`);
  const top = r2(e.cy - 18.8);
  const bottom = r2(e.cy + 18.8);
  const p = {
    slot, name, color: colorOf(e.note), cx: e.cx, w: e.w, top, crease: e.cy, bottom,
    hit: { x0: r2(e.cx - PITCH / 2), x1: r2(e.cx + PITCH / 2), y0: TILE_TOP, y1: r2(bottom + 0.4) },
  };
  if (name.startsWith('SR')) Object.assign(p, { type: 'sr', bit: Number(name.slice(2)), label: `SR ${name.slice(2)}` });
  else Object.assign(p, PADDLE_TYPE[name]);
  return p;
});

const tileEls = ofKind('tile');
if (tileEls.length !== 31) fail('expected 31 tiles');
const tiles = tileEls.map((e) => {
  const name = e.id.slice('tile.'.length);
  const half = /half/.test(e.note);
  return { name, color: colorOf(e.note), ...box(e), r: half ? 1.6 : 2.0 };
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
const WORD_SPACING = 0.12; // em
const UPM = 1000;
for (const l of labels) {
  if (l.tile || /^\d+$/.test(l.text)) {
    delete l.width; // numbers keep the font's natural width
    continue;
  }
  const lm = art.labelMetrics?.[l.text];
  const spaces = (l.text.match(/ /g) || []).length;
  if (spaces) l.wordSpacing = WORD_SPACING;
  if (lm && lm.ink > 0) {
    const extra = spaces * WORD_SPACING * UPM;
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
const boxes = ofKind('box').map((e) => {
  const id = e.id.slice('box.'.length);
  const r = +(e.note.match(/r~([\d.]+)/)?.[1] ?? 2.2);
  return { id, ...box(e), r };
});
const notchX = ofKind('notch').map((e) => e.cx);
const bars = ofKind('bar').map((e) => {
  const id = e.id.slice('bar.'.length);
  const b = box(e);
  const first = id === 'A21' || id === 'D15';
  const last = id === 'A2_0' || id === 'D2_0';
  const notches = id.startsWith('D') ? notchX.filter((x) => x > b.x0 + 3 && x < b.x1 - 3) : [];
  return { id, color: colorOf(e.note), ...b, rl: first ? 2.4 : 0, rr: last ? 2.4 : 0, notches };
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
  return { id, x0: b.x0, x1: b.x1, y0, y1: b.y1, r: 2.4, sep: r2(y0 + (isData ? 4.4 : 4.0)), cols: cols.sort((a, c) => a - c) };
});
// Status boxes (and PARITY) hold one cell per lamp with the same scalloped
// bottom; their separators start ~1 mm above the windows.
const statusCells = boxes.filter((b) => b.id.startsWith('status') || b.id === 'parity').map((b) => {
  const cols = lamps.filter((l) => (l.group === 'status' || l.group === 'parity') && l.cx > b.x0 && l.cx < b.x1 && l.cy > b.y0 && l.cy < b.y1).map((l) => l.cx);
  const cy = lamps.find((l) => l.cx === cols[0] && l.cy > b.y0 && l.cy < b.y1).cy;
  return { id: b.id, x0: b.x0, x1: b.x1, y0: b.y0, y1: b.y1, r: b.r, sep: r2(cy - 4.65 - (b.id === 'parity' ? 2.6 : 1.1)), cols: cols.sort((a, c) => a - c) };
});

// ---------------------------------------------------------------- knobs and key
const ADDR_POS = ['PROG PHY', 'CONS PHY', 'KERNEL D', 'SUPER D', 'USER D', 'USER I', 'SUPER I', 'KERNEL I'];
const ADDR_LAMP = [59, 55, 54, 53, 52, 56, 57, 58];
const DATA_POS = ['BUS REG', 'DATA PATHS', 'µADRS FPP/CPU', 'DISPLAY REGISTER'];
const DATA_LAMP = [61, 60, 62, 63];
const knobs = ['addr', 'data'].map((name) => {
  const e = get(`knob.${name}`);
  const angles = Array.from({ length: 8 }, (_, k) => 22.5 + 45 * k);
  return {
    name, label: name === 'addr' ? 'ADDRESS' : 'DATA', cx: e.cx, cy: e.cy, r: e.w / 2, angles,
    positions: name === 'addr' ? ADDR_POS : Array.from({ length: 8 }, (_, k) => DATA_POS[k % 4]),
    lamps: name === 'addr' ? ADDR_LAMP : Array.from({ length: 8 }, (_, k) => DATA_LAMP[k % 4]),
  };
});
const keyEl = get('keyswitch');
const key = {
  cx: keyEl.cx, cy: keyEl.cy, r: keyEl.w / 2, positions: ['off', 'power', 'lock'],
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
  logo.transform = { scale: Number(s.toFixed(6)), tx: r3(logoEl.cx - cxPx * s), ty: r3(logoEl.cy - cyPx * s) };
}

// ---------------------------------------------------------------- frame
const frame = {
  bezel: { x0: bx0, y0: by0, x1: bx1, y1: by1, r: 4 },
  strip: { ...box(get('logo_strip')), r: 3.5 },
  window: { ...box(get('window')), rt: 7, rb: 6 },
  bay: { x0: r2(col(0) - 6.75 - 0.8), y0: 186.0, x1: r2(col(29) + 6.75 + 0.8), y1: 224.6 },
  ledge: { y0: 224.2, y1: 245.6 },
  lip: { y0: 245.6, y1: by1 },
};

// ---------------------------------------------------------------- colours
const colors = {
  ...table.colors,
  window_unlit: '#1a1011', lens_unlit: '#241214',
  lamp_core: '#ff3b2a', lamp_mid: '#e8001a', lamp_rim: '#8a0010',
};

const geometry = {
  version: 1,
  generatedBy: 'web/dev/gen-geometry.mjs',
  source: relative(repo, TABLE),
  units: 'mm',
  viewBox, pitch: PITCH, col0: COL0,
  colors, frame, boxes, bars, cells, statusCells,
  notch: { base: 4.6, top: 1.5, h: 3.9 },
  // capScale: the scan-based cap heights are ~6 % larger than on r4's photo
  // (backlight bloom in the scan); widths stay the measured ink widths.
  labelFont: { family: 'Archivo Panel', capHeight: art.labelCapHeight ?? 0.686, capScale: 0.94, weight: 600, tileWeight: 500 },
  tiles, labels, lamps, paddles, knobs, key, logo,
  art: { tagline: art.tagline, digital: art.digital },
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
