// Builds the front-panel SVG from geometry.json (millimetres, r4 frame):
// bezel, window, boxes, bars, cells, tiles, labels, logo, unlit lamp lenses,
// plus the movable parts (paddles in three poses, knobs, key) that
// controls.js animates. Lamps themselves are HTML layers (lamps.js).

const NS = 'http://www.w3.org/2000/svg';

/** Creates an SVG element; attribute values that are null/undefined are skipped. */
export function s(tag, attrs = {}, ...children) {
  const e = document.createElementNS(NS, tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v !== null && v !== undefined) e.setAttribute(k, String(v));
  }
  for (const c of children.flat()) {
    if (c !== null && c !== undefined && c !== false) e.append(c);
  }
  return e;
}

const f2 = (v) => Math.round(v * 100) / 100;
const pts = (list) => list.map(([x, y]) => `${f2(x)},${f2(y)}`).join(' ');

function rrect(x0, y0, x1, y1, r, attrs = {}) {
  return s('rect', { x: f2(x0), y: f2(y0), width: f2(x1 - x0), height: f2(y1 - y0), rx: r || null, ...attrs });
}

/** Rounded rectangle path with separate top and bottom corner radii. */
function roundedPath(x0, y0, x1, y1, rt, rb) {
  const a = (r, x, y) => (r ? `A${r},${r} 0 0 1 ${f2(x)},${f2(y)}` : `L${f2(x)},${f2(y)}`);
  return `M${f2(x0)},${f2(y0 + rt)} ${a(rt, x0 + rt, y0)} L${f2(x1 - rt)},${f2(y0)} ${a(rt, x1, y0 + rt)} ` +
    `L${f2(x1)},${f2(y1 - rb)} ${a(rb, x1 - rb, y1)} L${f2(x0 + rb)},${f2(y1)} ${a(rb, x0, y1 - rb)} Z`;
}

function linear(id, stops, { x1 = 0, y1 = 0, x2 = 0, y2 = 1, units } = {}) {
  return s('linearGradient', { id, x1, y1, x2, y2, gradientUnits: units },
    stops.map(([o, c, a]) => s('stop', { offset: o, 'stop-color': c, 'stop-opacity': a ?? null })));
}

function radial(id, stops, { cx = 0.5, cy = 0.5, r = 0.5, fx, fy, units } = {}) {
  return s('radialGradient', { id, cx, cy, r, fx, fy, gradientUnits: units },
    stops.map(([o, c, a]) => s('stop', { offset: o, 'stop-color': c, 'stop-opacity': a ?? null })));
}

// Paddle palettes: UI.md §3 tokens, shading sampled from r4's BlinkenBone photo.
const PADDLE = {
  rose: {
    face: '#cf2647', faceTop: '#7a1428', faceHi: '#dd3a57', faceLow: '#c52343', crease: '#8c142b', gloss: '#ff9fb2', edge: '#ee5a78',
    facetHi: '#8e1630', facet: '#4a0816', facetDark: '#070001', lower: '#5f3f41', lowerDark: '#2e0c12',
    back: '#3a0a12', backMid: '#5a141e', lip: '#f23f61', lipLow: '#c42a49',
    fwd: '#ef3c5b', fwdTop: '#e3304f', fwdLow: '#c92745', fwdSpec: '#ffa3b3',
  },
  mag: {
    face: '#6e2a55', faceTop: '#3c1530', faceHi: '#7e3866', faceLow: '#65264f', crease: '#3a1229', gloss: '#c58aae', edge: '#93507c',
    facetHi: '#55203f', facet: '#2a0d20', facetDark: '#050104', lower: '#3f3437', lowerDark: '#1b0f11',
    back: '#1c0b14', backMid: '#2c1221', lip: '#a3477e', lipLow: '#6e2c55',
    fwd: '#814369', fwdTop: '#71375b', fwdLow: '#622d4f', fwdSpec: '#c58db1',
  },
  blank: {
    face: '#e8e7e1', faceTop: '#b9b5ab', faceHi: '#f3f2ee', faceLow: '#e1dfd8', crease: '#b5b0a5', gloss: '#ffffff', edge: '#fbfaf7',
    facetHi: '#c9c5bd', facet: '#8d877e', facetDark: '#3a352f', lower: '#85807a', lowerDark: '#5a554f',
    back: '#7f7569', backMid: '#94897c', lip: '#f2f1ec', lipLow: '#cfc9bf',
    fwd: '#f4f3ee', fwdTop: '#e9e8e2', fwdLow: '#dad7ce', fwdSpec: '#ffffff',
  },
};

function defs(geo, id) {
  const c = geo.colors;
  const d = s('defs');
  d.append(
    linear(`${id}-bezel`, [[0, '#e8e6df'], [0.1, '#e1ded6'], [0.75, '#d8d4cb'], [1, '#cdc9bf']]),
    linear(`${id}-strip`, [[0, '#dedbd3'], [1, '#d2cec4']]),
    linear(`${id}-bevel`, [[0, '#d9d5cb'], [0.5, '#c3beb3'], [1, '#8f897f']], { x1: 0, y1: 0, x2: 1, y2: 0 }),
    linear(`${id}-window`, [[0, '#1e151c'], [0.5, '#1b1219'], [1, '#170f15']], { x1: 0, y1: 0, x2: 1, y2: 1 }),
    linear(`${id}-glare`, [[0, '#ffffff', 0], [0.4, '#ffffff', 0.018], [0.52, '#ffffff', 0.035], [0.6, '#ffffff', 0]],
      { x1: 0, y1: 0, x2: 1, y2: 0.35 }),
    linear(`${id}-topshade`, [[0, '#000000', 0.6], [1, '#000000', 0]]),
    linear(`${id}-topwall`, [[0, '#c9c4ba'], [0.55, '#a39d92'], [1, '#6f685f']]),
    // across-the-face shading of the REST paddles (lit side / notch side)
    linear(`${id}-shadeR`, [[0, '#ffffff', 0.1], [0.45, '#ffffff', 0], [0.75, '#000000', 0.06], [1, '#000000', 0.28]], { x1: 0, y1: 0, x2: 1, y2: 0 }),
    linear(`${id}-shadeL`, [[0, '#000000', 0.28], [0.25, '#000000', 0.06], [0.55, '#ffffff', 0], [1, '#ffffff', 0.1]], { x1: 0, y1: 0, x2: 1, y2: 0 }),
    linear(`${id}-bay`, [[0, '#040203'], [0.5, '#0c0709'], [1, '#070405']]),
    linear(`${id}-baywallL`, [[0, '#000000', 0.7], [1, '#000000', 0]], { x1: 0, y1: 0, x2: 1, y2: 0 }),
    linear(`${id}-ledge`, [[0, '#29231f'], [0.08, '#4d453e'], [0.3, '#7a736a'], [0.75, '#9a948a'], [1, '#a9a398']]),
    linear(`${id}-lip`, [[0, '#b2ab9f'], [0.5, '#a39c91'], [1, '#867e74']]),
    radial(`${id}-lens`, [[0, '#2b1416'], [0.72, c.lens_unlit], [1, '#1e0f11']]),
    radial(`${id}-knob`, [[0, '#2c2c30'], [0.55, '#111113'], [0.92, '#050506'], [1, '#18181b']], { cx: 0.4, cy: 0.33, r: 0.68 }),
    linear(`${id}-knobbar`, [[0, '#3a3a40'], [0.18, '#26262b'], [0.6, '#151518'], [1, '#09090a']], { x1: 0, y1: 0, x2: 1, y2: 0 }),
    radial(`${id}-knobshadow`, [[0, '#000000', 0.65], [0.7, '#000000', 0.4], [1, '#000000', 0]]),
    radial(`${id}-chrome`, [[0, '#f2f2ef'], [0.4, '#b9b9b5'], [0.72, '#6f6f6c'], [0.86, '#b4b4b0'], [1, '#5a5a57']],
      { cx: 0.42, cy: 0.36, r: 0.7 }),
    linear(`${id}-keybow`, [[0, '#7a7a77'], [0.25, '#f2f2ef'], [0.55, '#a9a9a5'], [0.8, '#d9d9d5'], [1, '#62625f']], { x1: 0, y1: 0, x2: 1, y2: 0 }),
    radial(`${id}-keyring`, [[0, '#0b0b0d'], [0.78, '#060607'], [0.92, '#232327'], [1, '#09090b']]),
  );
  for (const [name, p] of Object.entries(PADDLE)) {
    d.append(
      linear(`${id}-${name}-upper`, [[0, p.faceTop], [0.035, p.faceHi], [0.22, p.face], [0.8, p.face], [0.93, p.faceLow], [1, p.crease]]),
      linear(`${id}-${name}-lower`, [[0, p.lowerDark], [0.06, p.lower], [0.55, p.lower], [0.88, p.lowerDark], [1, '#000000']]),
      linear(`${id}-${name}-facetR`, [[0, p.facetHi], [0.35, p.facet], [1, p.facetDark]], { x1: 0, y1: 0, x2: 1, y2: 0 }),
      linear(`${id}-${name}-facetL`, [[0, p.facetDark], [0.65, p.facet], [1, p.facetHi]], { x1: 0, y1: 0, x2: 1, y2: 0 }),
      linear(`${id}-${name}-gloss`, [[0, p.gloss, 0], [0.5, p.gloss, 0.6], [1, p.gloss, 0]]),
      linear(`${id}-${name}-back`, [[0, p.lipLow], [0.02, p.lip], [0.1, p.lip], [0.17, p.lipLow], [0.185, p.backMid], [0.32, p.back], [1, p.back]]),
      linear(`${id}-${name}-fwd`, [[0, p.fwdTop], [0.06, p.fwd], [0.75, p.fwd], [0.94, p.fwdLow], [1, '#000000']]),
    );
  }
  return d;
}

// ---------------------------------------------------------------- frame

/** Outline of the dark window plus the paddle bay below it. */
function openingPath(geo) {
  const w = geo.frame.window;
  const b = geo.frame.bay;
  const rt = w.rt;
  const rbl = f2(b.x0 - w.x0); // the window's bottom corners curve into the bay walls
  const rbr = f2(w.x1 - b.x1);
  return `M${w.x0},${f2(w.y0 + rt)} A${rt},${rt} 0 0 1 ${f2(w.x0 + rt)},${w.y0} L${f2(w.x1 - rt)},${w.y0} ` +
    `A${rt},${rt} 0 0 1 ${w.x1},${f2(w.y0 + rt)} L${w.x1},${f2(w.y1 - rbr)} A${rbr},${rbr} 0 0 1 ${b.x1},${w.y1} ` +
    `L${b.x1},${b.y1} L${b.x0},${b.y1} L${b.x0},${w.y1} A${rbl},${rbl} 0 0 1 ${w.x0},${f2(w.y1 - rbl)} Z`;
}

function bezel(geo, id) {
  const f = geo.frame;
  const g = s('g', { class: 'bezel' });
  g.append(
    rrect(f.bezel.x0, f.bezel.y0, f.bezel.x1, f.bezel.y1, f.bezel.r, { fill: `url(#${id}-bezel)` }),
    rrect(f.bezel.x0 + 0.6, f.bezel.y0 + 0.35, f.bezel.x1 - 0.6, f.bezel.y0 + 1.2, 0.5, { fill: '#f4f3ee', opacity: 0.75 }),
    // raised logo strip: shadow below, highlight above, face, thin outline
    rrect(f.strip.x0 + 0.2, f.strip.y0 + 0.6, f.strip.x1 + 0.25, f.strip.y1 + 0.6, f.strip.r, { fill: '#aaa59a', opacity: 0.85 }),
    rrect(f.strip.x0, f.strip.y0 - 0.25, f.strip.x1, f.strip.y1, f.strip.r, { fill: '#f3f1ec' }),
    rrect(f.strip.x0 + 0.25, f.strip.y0 + 0.1, f.strip.x1 - 0.2, f.strip.y1 - 0.2, f.strip.r - 0.2, {
      fill: `url(#${id}-strip)`, stroke: '#bdb8ad', 'stroke-width': 0.22,
    }),
    // the moulded wall around the opening (light on the left, in shadow on the right)
    s('path', { d: openingPath(geo), fill: 'none', stroke: `url(#${id}-bevel)`, 'stroke-width': 5.2, 'stroke-linejoin': 'round' }),
    s('path', { d: openingPath(geo), fill: 'none', stroke: '#8a8479', 'stroke-width': 1.6, 'stroke-linejoin': 'round', opacity: 0.55 }),
    // the top inner wall is in the shadow of the logo band
    s('path', {
      d: `M${f2(f.window.x0 + 2)},${f2(f.window.y0 - 2.6)} L${f2(f.window.x1 - 2)},${f2(f.window.y0 - 2.6)} ` +
        `L${f2(f.window.x1 - f.window.rt)},${f.window.y0} L${f2(f.window.x0 + f.window.rt)},${f.window.y0} Z`,
      fill: `url(#${id}-topwall)`, opacity: 0.9,
    }),
  );
  return g;
}

function branding(geo) {
  const a = geo.art;
  const g = s('g', { class: 'branding', 'aria-hidden': 'true' });
  // debossed: a light edge below/right, the groove shadow, the floor
  const deboss = (d, dx = 0) => [
    s('path', { d, transform: `translate(${f2(dx + 0.16)},0.2)`, fill: '#fbfaf6' }),
    s('path', { d, transform: dx ? `translate(${f2(dx)},0)` : null, fill: '#9a958a' }),
    s('path', { d, transform: `translate(${f2(dx + 0.07)},0.09)`, fill: '#bbb6ab' }),
  ];
  for (const b of a.digital.boxes) {
    g.append(
      s('rect', { x: b.x + 0.15, y: b.y + 0.18, width: b.w, height: b.h, rx: 0.25, fill: 'none', stroke: '#fbfaf6', 'stroke-width': 0.35 }),
      s('rect', { x: b.x, y: b.y, width: b.w, height: b.h, rx: 0.25, fill: 'none', stroke: '#a39e93', 'stroke-width': 0.35 }),
    );
  }
  for (const l of a.digital.letters) g.append(...deboss(l.d, l.dx));
  g.append(...deboss(a.tagline.d));
  return g;
}

function opening(geo, id) {
  const w = geo.frame.window;
  const b = geo.frame.bay;
  const d = openingPath(geo);
  return s('g', { class: 'window' },
    s('path', { d, fill: `url(#${id}-window)` }),
    s('path', { d, fill: `url(#${id}-glare)` }),
    // shadow cast by the bezel's top edge onto the acrylic
    s('rect', { x: w.x0 + 4, y: w.y0, width: f2(w.x1 - w.x0 - 8), height: 2.4, fill: `url(#${id}-topshade)`, opacity: 0.85 }),
    // the paddle bay: a dark recess with shaded side walls
    rrect(b.x0, w.y1, b.x1, b.y1, 0, { fill: `url(#${id}-bay)` }),
    rrect(b.x0, w.y1, b.x0 + 2.5, b.y1, 0, { fill: `url(#${id}-baywallL)` }),
  );
}

function bayFront(geo, id) {
  const f = geo.frame;
  return s('g', { class: 'ledge' },
    rrect(f.bezel.x0, f.ledge.y0, f.bezel.x1, f.ledge.y1, 0, { fill: `url(#${id}-ledge)` }),
    s('path', { d: roundedPath(f.bezel.x0, f.lip.y0, f.bezel.x1, f.lip.y1, 0, f.bezel.r), fill: `url(#${id}-lip)` }),
    rrect(f.bezel.x0, f.lip.y0, f.bezel.x1, f.lip.y0 + 0.5, 0, { fill: '#c9c3b8', opacity: 0.8 }),
  );
}

// ---------------------------------------------------------------- printed panel

/**
 * Black lamp cells as one path: fused at the top (rounded top corners for
 * the status boxes), separated by ~0.95 mm gaps with rounded upper ends from
 * `sep` down, rounded bottom corners per cell (the scalloped bottom edge).
 * The gaps are real holes, so the window shows through them.
 */
function cellPath(c, { rt = 0, top = c.y0 } = {}) {
  const gap = 0.95;
  const n = c.cols.length;
  const rb = c.r;
  const ys = c.sep + gap / 2;
  const cells = [];
  for (let i = 0; i < n; i++) {
    const l = i === 0 ? c.x0 : (c.cols[i - 1] + c.cols[i]) / 2 + gap / 2;
    const r = i === n - 1 ? c.x1 : (c.cols[i] + c.cols[i + 1]) / 2 - gap / 2;
    cells.push([l, r]);
  }
  const { x0, x1, y1 } = c;
  const p = [`M${f2(x0)},${f2(top + rt)}`];
  if (rt) p.push(`A${rt},${rt} 0 0 1 ${f2(x0 + rt)},${f2(top)}`);
  p.push(`L${f2(x1 - rt)},${f2(top)}`);
  if (rt) p.push(`A${rt},${rt} 0 0 1 ${f2(x1)},${f2(top + rt)}`);
  for (let i = n - 1; i >= 0; i--) {
    const [l, r] = cells[i];
    if (i < n - 1) p.push(`L${f2(r)},${f2(ys)}`);
    p.push(`L${f2(r)},${f2(y1 - rb)}`, `A${rb},${rb} 0 0 1 ${f2(r - rb)},${f2(y1)}`,
      `L${f2(l + rb)},${f2(y1)}`, `A${rb},${rb} 0 0 1 ${f2(l)},${f2(y1 - rb)}`);
    if (i > 0) p.push(`L${f2(l)},${f2(ys)}`, `A${gap / 2},${gap / 2} 0 0 0 ${f2(cells[i - 1][1])},${f2(ys)}`);
  }
  p.push('Z');
  return p.join(' ');
}

/** Colour bar with optional rounded top corners and the DATA notches. */
function barPath(b, notch) {
  const { x0, x1, y0, y1, rl, rr } = b;
  const p = [`M${f2(x0)},${f2(y1)}`, `L${f2(x0)},${f2(y0 + rl)}`];
  if (rl) p.push(`A${rl},${rl} 0 0 1 ${f2(x0 + rl)},${f2(y0)}`);
  p.push(`L${f2(x1 - rr)},${f2(y0)}`);
  if (rr) p.push(`A${rr},${rr} 0 0 1 ${f2(x1)},${f2(y0 + rr)}`);
  p.push(`L${f2(x1)},${f2(y1)}`);
  // black tabs rising into the bar from its bottom edge: concave fillets at
  // the base, straight flanks, a round top (r4 §2.2, scan zoom)
  const ht = notch.top / 2;
  const fr = (notch.base - notch.top) / 2;
  for (const nx of [...b.notches].sort((a, c) => c - a)) {
    p.push(
      `L${f2(nx + ht + fr)},${f2(y1)}`,
      `Q${f2(nx + ht)},${f2(y1)} ${f2(nx + ht)},${f2(y1 - fr)}`,
      `L${f2(nx + ht)},${f2(y1 - notch.h + ht)}`,
      `A${ht},${ht} 0 0 0 ${f2(nx - ht)},${f2(y1 - notch.h + ht)}`,
      `L${f2(nx - ht)},${f2(y1 - fr)}`,
      `Q${f2(nx - ht)},${f2(y1)} ${f2(nx - ht - fr)},${f2(y1)}`,
    );
  }
  p.push('Z');
  return p.join(' ');
}

function boxesAndCells(geo) {
  const c = geo.colors;
  const g = s('g', { class: 'boxes' });
  for (const b of geo.boxes) {
    if (b.id.startsWith('status') || b.id === 'parity') continue; // drawn as cells
    g.append(rrect(b.x0, b.y0, b.x1, b.y1, b.r, { fill: c.box }));
  }
  for (const sc of geo.statusCells) g.append(s('path', { d: cellPath(sc, { rt: sc.r }), fill: c.box }));
  // cells under the bars start inside the bar (no seam; the black shows in the notches)
  for (const cc of geo.cells) g.append(s('path', { d: cellPath(cc, { top: cc.y0 - 4.3 }), fill: c.box }));
  for (const b of geo.bars) {
    g.append(s('path', { d: barPath(b, geo.notch), fill: b.color === 'rose' ? c.bar_rose : c.bar_mag }));
  }
  return g;
}

function lampWindows(geo, id) {
  const g = s('g', { class: 'lamp-windows' });
  for (const l of geo.lamps) {
    g.append(
      rrect(l.cx - l.w / 2, l.cy - l.h / 2, l.cx + l.w / 2, l.cy + l.h / 2, 1.3, { fill: geo.colors.window_unlit }),
      s('circle', { cx: l.cx, cy: l.cy, r: 2.6, fill: `url(#${id}-lens)` }),
    );
  }
  return g;
}

function tiles(geo) {
  const c = geo.colors;
  const g = s('g', { class: 'tiles' });
  for (const t of geo.tiles) {
    g.append(rrect(t.x0, t.y0, t.x1, t.y1, t.r, { fill: t.color === 'rose' ? c.tile_rose : c.tile_mag }));
  }
  return g;
}

function labels(geo) {
  const lf = { capHeight: 0.686, capScale: 1, weight: 600, tileWeight: 500, ...geo.labelFont };
  const g = s('g', { class: 'labels', fill: geo.colors.ink, 'aria-hidden': 'true' });
  for (const l of geo.labels) {
    const size = (l.cap * lf.capScale) / lf.capHeight;
    const t = s('text', {
      x: l.x, y: l.y, 'font-size': f2(size), 'text-anchor': l.anchor,
      'font-weight': l.tile ? lf.tileWeight : null,
      'word-spacing': l.wordSpacing ? f2(l.wordSpacing * size) : null,
      textLength: l.textLength ?? null, lengthAdjust: l.textLength ? 'spacingAndGlyphs' : null,
    });
    t.textContent = l.text;
    g.append(t);
  }
  return g;
}

/** pdp11/70 badge: outlined "pdp" and "70" (union outline via a mask), solid "11", thin "/". */
function logo(geo, id) {
  const L = geo.logo;
  const ink = geo.colors.ink;
  const t = L.t;
  const tr = L.transform;
  const mask = s('mask', { id: `${id}-logo-mask`, maskUnits: 'userSpaceOnUse', x: 0, y: 0, width: 800, height: 300 });
  const grow = s('g', { fill: 'white', stroke: 'white' });
  const shrink = s('g', { fill: 'black', stroke: 'black' });
  for (const r of L.rings) {
    const mid = (r.R + r.r) / 2;
    grow.append(s('circle', { cx: r.cx, cy: r.cy, r: mid, fill: 'none', 'stroke-width': r.R - r.r + t }));
    shrink.append(s('circle', { cx: r.cx, cy: r.cy, r: mid, fill: 'none', 'stroke-width': r.R - r.r - t }));
  }
  for (const st of L.stems) {
    grow.append(s('rect', { x: st.x0 - t / 2, y: st.y0 - t / 2, width: st.x1 - st.x0 + t, height: st.y1 - st.y0 + t, stroke: 'none' }));
    shrink.append(s('rect', { x: st.x0 + t / 2, y: st.y0 + t / 2, width: st.x1 - st.x0 - t, height: st.y1 - st.y0 - t, stroke: 'none' }));
  }
  grow.append(s('polygon', { points: pts(L.seven), 'stroke-width': t, 'stroke-linejoin': 'miter' }));
  shrink.append(s('polygon', { points: pts(insetPolygon(L.seven, t / 2)), stroke: 'none' }));
  mask.append(grow, shrink);
  return s('g', { class: 'logo', transform: `translate(${tr.tx},${tr.ty}) scale(${tr.scale})`, 'aria-hidden': 'true' },
    s('defs', {}, mask),
    s('rect', { x: 0, y: 0, width: 800, height: 300, fill: ink, mask: `url(#${id}-logo-mask)` }),
    ...L.ones.map((o) => s('path', { d: o.d, fill: ink })),
    s('line', { x1: L.slash.x0, y1: L.slash.y0, x2: L.slash.x1, y2: L.slash.y1, stroke: ink, 'stroke-width': t }),
  );
}

/** Offsets a simple polygon's edges inwards by d (miter joins). */
export function insetPolygon(list, d) {
  const n = list.length;
  let area = 0;
  for (let i = 0; i < n; i++) {
    const [x1, y1] = list[i];
    const [x2, y2] = list[(i + 1) % n];
    area += x1 * y2 - x2 * y1;
  }
  const sign = area > 0 ? 1 : -1;
  const lines = [];
  for (let i = 0; i < n; i++) {
    const [x1, y1] = list[i];
    const [x2, y2] = list[(i + 1) % n];
    const len = Math.hypot(x2 - x1, y2 - y1);
    const nx = (sign * -(y2 - y1)) / len;
    const ny = (sign * (x2 - x1)) / len;
    lines.push([x1 + nx * d, y1 + ny * d, x2 + nx * d, y2 + ny * d]);
  }
  const out = [];
  for (let i = 0; i < n; i++) {
    const a = lines[(i + n - 1) % n];
    const b = lines[i];
    const den = (a[0] - a[2]) * (b[1] - b[3]) - (a[1] - a[3]) * (b[0] - b[2]);
    if (Math.abs(den) < 1e-9) {
      out.push([b[0], b[1]]);
      continue;
    }
    const ta = ((a[0] - b[0]) * (b[1] - b[3]) - (a[1] - b[1]) * (b[0] - b[2])) / den;
    out.push([a[0] + ta * (a[2] - a[0]), a[1] + ta * (a[3] - a[1])]);
  }
  return out;
}

function keyStatic(geo, id) {
  const k = geo.key;
  const g = s('g', { class: 'key-static', transform: `translate(${k.cx},${k.cy})` });
  // faint printed guide lines from the key towards POWER, LOCK and OFF
  for (const a of [315, 45, 225]) {
    const rad = ((a - 90) * Math.PI) / 180;
    g.append(s('line', {
      x1: f2(Math.cos(rad) * 10.3), y1: f2(Math.sin(rad) * 10.3), x2: f2(Math.cos(rad) * 12.8), y2: f2(Math.sin(rad) * 12.8),
      stroke: '#3b2d35', 'stroke-width': 0.35,
    }));
  }
  g.append(
    s('circle', { r: 9.9, fill: '#070609' }),
    s('circle', { r: 9.6, fill: `url(#${id}-keyring)` }),
    s('circle', { r: 9.4, fill: 'none', stroke: '#2c2b30', 'stroke-width': 0.3 }),
    s('circle', { r: 5.8, fill: '#141416' }),
    s('circle', { r: 5.4, fill: `url(#${id}-chrome)` }),
    s('circle', { r: 3.6, fill: '#58524d', opacity: 0.55 }),
    s('circle', { r: 3.6, fill: 'none', stroke: '#d8d8d4', 'stroke-width': 0.25, opacity: 0.5 }),
  );
  return g;
}

// ---------------------------------------------------------------- movable parts

/** The key bow seen edge-on, rotated by controls.js about the key centre. */
function keyMovable(geo, id) {
  const k = geo.key;
  // the end pointing at the selected position is a little wider and carries
  // an index notch, so OFF (225 deg) and LOCK (45 deg) can be told apart
  const bow = 'M-1.8,-11.5 Q0,-12.2 1.8,-11.5 L1.35,6.5 Q0,7.1 -1.35,6.5 Z';
  const rot = s('g', { class: 'key-rot' },
    s('path', { d: bow, fill: '#000', opacity: 0.5, transform: 'translate(0.8,1.0)' }),
    s('path', { d: bow, fill: `url(#${id}-keybow)`, stroke: '#3e3e3b', 'stroke-width': 0.22 }),
    s('rect', { x: -0.32, y: -11.1, width: 0.64, height: 1.7, rx: 0.3, fill: '#3c3c39' }),
    s('circle', { r: 1.2, fill: '#6d6d69' }),
  );
  return s('g', { class: 'key', transform: `translate(${k.cx},${k.cy})` }, rot);
}

function knobMovable(geo, id, kn) {
  const rot = s('g', { class: 'knob-rot' },
    // the raised bar: its side (offset) and its top face with the pointer
    s('rect', { x: -4.5, y: -12.6, width: 9.0, height: 25.6, rx: 3.6, fill: '#020203', transform: 'translate(0.55,0.75)' }),
    s('rect', { x: -4.6, y: -13.0, width: 9.2, height: 26.0, rx: 3.6, fill: '#060607' }),
    s('rect', { x: -4.25, y: -12.65, width: 8.5, height: 25.3, rx: 3.3, fill: `url(#${id}-knobbar)` }),
    s('rect', { x: -3.7, y: -12.0, width: 1.4, height: 24.0, rx: 0.7, fill: '#ffffff', opacity: 0.08 }),
    s('path', { d: 'M-4.25,-9.4 A3.3,3.3 0 0 1 -0.95,-12.65 L0.95,-12.65 A3.3,3.3 0 0 1 4.25,-9.4', fill: 'none', stroke: '#6b6b72', 'stroke-width': 0.3, opacity: 0.6 }),
    s('rect', { x: -0.42, y: -11.0, width: 0.84, height: 10.4, rx: 0.42, fill: geo.colors.ink }),
    s('circle', { r: 1.25, fill: geo.colors.ink }),
  );
  return s('g', { class: 'knob', 'data-knob': kn.name, transform: `translate(${kn.cx},${kn.cy})` },
    s('ellipse', { cx: 1.6, cy: 2.4, rx: kn.r + 2.0, ry: kn.r + 1.8, fill: `url(#${id}-knobshadow)` }),
    s('circle', { r: kn.r, fill: `url(#${id}-knob)` }),
    s('path', { d: `M${f2(-kn.r * 0.94)},${f2(-kn.r * 0.34)} A${kn.r},${kn.r} 0 0 1 ${f2(kn.r * 0.34)},${f2(-kn.r * 0.94)}`, fill: 'none', stroke: '#4a4a50', 'stroke-width': 0.4, opacity: 0.7 }),
    rot,
  );
}

/**
 * One paddle in its three poses (REST bow-tie, BACK raised, FWD pressed).
 * The face's waist (13.5 -> 10.3 mm at the crease) is split between the
 * sides according to the slot's distance from the panel centre, like the
 * real row seen from the front: the side facing the centre shows the facet.
 */
function paddle(geo, id, p) {
  const pal = PADDLE[p.color];
  const { cx, top: t, crease: m, bottom: b } = p;
  const hw = p.w / 2;
  const x0 = cx - hw;
  const x1 = cx + hw;
  const off = Math.max(-1, Math.min(1, (p.slot - 14.5) / 3));
  const waist = 3.6; // 13.5 -> 9.9 mm: reads like the photo's row (UI.md: ~10.3)
  const insL = (waist / 2) * (1 + 0.84 * off);
  const insR = waist - insL;
  const u = (n) => `url(#${id}-${p.color}-${n})`;
  const g = s('g', { class: 'paddle', 'data-slot': p.slot, 'data-name': p.name, 'data-pose': 'rest' });

  // The faces are waisted by lens-shaped side facets (photo zoom): a facet
  // starts ~20 % down the paddle, is deepest just above the crease and ends
  // ~78 % down; the face to its side is straight. The facet facing the
  // panel centre is the deep one.
  const H = b - t;
  const yTop = t + 0.2 * H;
  const yBot = t + 0.78 * H;
  const yW = m - 1.5;
  // facet boundary: leaves the edge at a shallow angle, vertical at its
  // deepest point (cubic segments), a pointed lens like the photo's
  const facetEdge = (side, d) => {
    const xe = side > 0 ? x1 : x0; // paddle edge
    const xi = xe - side * d; // deepest point
    const a = yW - yTop;
    const c = yBot - yW;
    return `M${f2(xe)},${f2(yTop)} C${f2(xe - side * 0.25 * d)},${f2(yTop + 0.3 * a)} ${f2(xi)},${f2(yW - 0.45 * a)} ${f2(xi)},${f2(yW)} ` +
      `C${f2(xi)},${f2(yW + 0.4 * c)} ${f2(xe - side * 0.25 * d)},${f2(yBot - 0.3 * c)} ${f2(xe)},${f2(yBot)}`;
  };
  const lens = (side, d) => (d < 0.25 ? null : `${facetEdge(side, d)} Z`);
  const rimOf = facetEdge;
  const deep = insR >= insL ? 1 : -1; // +1: the deep facet is on the right
  const shade = `url(#${id}-${deep > 0 ? 'shadeR' : 'shadeL'})`;
  const rest = s('g', { class: 'pose pose-rest' },
    s('rect', { x: f2(x0), y: t, width: p.w, height: f2(m - t), rx: 0.5, fill: u('upper') }),
    s('rect', { x: f2(x0), y: m, width: p.w, height: f2(b - m), fill: u('lower') }),
    s('rect', { x: f2(x0), y: t, width: p.w, height: f2(H), rx: 0.5, fill: shade }),
    // glossy band high on the upper face, the crease, the side facets with their glossy rims
    s('rect', { x: f2(x0 + 1.2), y: f2(t + 1.8), width: f2(p.w - 2.4), height: 1.5, rx: 0.4, fill: u('gloss'), opacity: 0.3 }),
    s('line', { x1: f2(x0), y1: f2(m - 0.15), x2: f2(x1), y2: f2(m - 0.15), stroke: pal.gloss, 'stroke-width': 0.3, opacity: 0.35 }),
    ...[[1, insR], [-1, insL]].map(([side, d]) => lens(side, d) && s('path', {
      d: lens(side, d), fill: `url(#${id}-${p.color}-facet${side > 0 ? 'R' : 'L'})`,
    })),
    ...[[1, insR], [-1, insL]].map(([side, d]) => d >= 0.25 && s('path', {
      d: rimOf(side, d), fill: 'none', stroke: pal.edge, 'stroke-width': 0.3, opacity: d > 0.8 ? 0.8 : 0.35,
    })),
  );
  const back = s('g', { class: 'pose pose-back' },
    s('rect', { x: f2(x0), y: t, width: p.w, height: f2(b - t), rx: 0.5, fill: u('back') }),
    // the raised paddle still shows a narrow facet on the side facing the centre
    s('path', { d: deep > 0 ? `M${f2(x1)},${f2(t + 7)} L${f2(x1 - 1.1)},${f2(t + 12)} L${f2(x1 - 1.1)},${f2(b - 4)} L${f2(x1)},${f2(b - 1)} Z`
      : `M${f2(x0)},${f2(t + 7)} L${f2(x0 + 1.1)},${f2(t + 12)} L${f2(x0 + 1.1)},${f2(b - 4)} L${f2(x0)},${f2(b - 1)} Z`, fill: '#000000', opacity: 0.35 }),
    s('rect', { x: f2(x0 + 0.6), y: f2(t + 9), width: f2(p.w - 1.2), height: f2(b - t - 12), rx: 0.4, fill: '#ffffff', opacity: 0.025 }),
    s('rect', { x: f2(x0), y: f2(b - 1.3), width: p.w, height: 1.3, fill: '#000', opacity: 0.4 }),
  );
  const fwd = s('g', { class: 'pose pose-fwd' },
    s('rect', { x: f2(x0), y: t, width: p.w, height: f2(b - t), rx: 0.5, fill: u('fwd') }),
    s('rect', { x: f2(x0 + 3), y: f2(b - 3.6), width: f2(p.w - 6), height: 0.7, rx: 0.35, fill: pal.fwdSpec, opacity: 0.25 }),
  );
  const hl = s('rect', { class: 'paddle-hl', x: f2(x0), y: t, width: p.w, height: f2(b - t), rx: 0.5, fill: '#ffffff' });
  g.append(rest, back, fwd, hl);
  return g;
}

/**
 * Builds the complete panel SVG. Returns { svg, paddles: Map(name -> g),
 * knobs: Map(name -> rotating g), key: rotating g }.
 */
export function buildPanelSvg(geo, { id = 'pnl', title = 'PDP-11/70 front panel' } = {}) {
  const [vx, vy, vw, vh] = geo.viewBox;
  const svg = s('svg', {
    class: 'panel-svg', viewBox: `${vx} ${vy} ${vw} ${vh}`, preserveAspectRatio: 'xMidYMid meet',
    role: 'img', 'aria-label': title, focusable: 'false',
  });
  svg.append(
    defs(geo, id),
    bezel(geo, id),
    branding(geo),
    opening(geo, id),
    boxesAndCells(geo),
    lampWindows(geo, id),
    tiles(geo),
    labels(geo),
    logo(geo, id),
    keyStatic(geo, id),
    bayFront(geo, id),
  );
  const paddles = new Map();
  const pg = s('g', { class: 'paddles' });
  for (const p of geo.paddles) {
    const g = paddle(geo, id, p);
    paddles.set(p.name, g);
    pg.append(g);
  }
  // the window's bottom edge throws a thin shadow onto the paddle tops
  const w = geo.frame.window;
  pg.append(rrect(geo.frame.bay.x0, w.y1, geo.frame.bay.x1, w.y1 + 1.4, 0, { fill: `url(#${id}-topshade)` }));
  svg.append(pg);
  const knobs = new Map();
  for (const kn of geo.knobs) {
    const g = knobMovable(geo, id, kn);
    knobs.set(kn.name, g.querySelector('.knob-rot'));
    svg.append(g);
  }
  const keyG = keyMovable(geo, id);
  svg.append(keyG);
  return { svg, paddles, knobs, key: keyG.querySelector('.key-rot') };
}
