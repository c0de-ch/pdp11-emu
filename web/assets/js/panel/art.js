// Small toolkit for the panel's vector art: SVG elements, paths through
// measured points, gradients and colour arithmetic.

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

export const f2 = (v) => Math.round(v * 100) / 100;
export const pts = (list) => list.map(([x, y]) => `${f2(x)},${f2(y)}`).join(' ');
export const poly = (list) => `M${list.map(([x, y]) => `${f2(x)},${f2(y)}`).join(' L')} Z`;
export const line = (list) => `M${list.map(([x, y]) => `${f2(x)},${f2(y)}`).join(' L')}`;

/**
 * Cubic segments of a smooth curve through the points, without the initial move.
 * The tangents weigh the neighbouring chords (non-uniform Catmull-Rom), so that
 * traced points at very different distances do not overshoot.
 */
export function curve(p) {
  let d = '';
  const dist = (u, v) => Math.hypot(u[0] - v[0], u[1] - v[1]);
  for (let i = 0; i < p.length - 1; i++) {
    const a = p[Math.max(0, i - 1)];
    const b = p[i];
    const c = p[i + 1];
    const e = p[Math.min(p.length - 1, i + 2)];
    const here = dist(b, c);
    const k1 = here / (3 * (dist(a, b) + here));
    const k2 = here / (3 * (here + dist(c, e)));
    d += `C${f2(b[0] + (c[0] - a[0]) * k1)},${f2(b[1] + (c[1] - a[1]) * k1)} ` +
      `${f2(c[0] - (e[0] - b[0]) * k2)},${f2(c[1] - (e[1] - b[1]) * k2)} ${f2(c[0])},${f2(c[1])} `;
  }
  return d;
}

/**
 * Outline from runs of points: smooth through each run, straight between runs.
 * The default closes it.
 */
export function outline(runs, { closed = true } = {}) {
  let d = `M${f2(runs[0][0][0])},${f2(runs[0][0][1])} `;
  runs.forEach((run, i) => {
    if (i) d += `L${f2(run[0][0])},${f2(run[0][1])} `;
    if (run.length > 1) d += curve(run);
  });
  return closed ? `${d}Z` : d.trim();
}

export function rrect(x0, y0, x1, y1, r, attrs = {}) {
  return s('rect', { x: f2(x0), y: f2(y0), width: f2(x1 - x0), height: f2(y1 - y0), rx: r || null, ...attrs });
}

/** Rectangle path with its own radius per corner: [top left, top right, bottom right, bottom left]. */
export function cornersPath(x0, y0, x1, y1, [tl, tr, br, bl]) {
  const a = (r, x, y) => (r ? `A${r},${r} 0 0 1 ${f2(x)},${f2(y)}` : `L${f2(x)},${f2(y)}`);
  return `M${f2(x0)},${f2(y0 + tl)} ${a(tl, x0 + tl, y0)} L${f2(x1 - tr)},${f2(y0)} ${a(tr, x1, y0 + tr)} ` +
    `L${f2(x1)},${f2(y1 - br)} ${a(br, x1 - br, y1)} L${f2(x0 + bl)},${f2(y1)} ${a(bl, x0, y1 - bl)} Z`;
}

export const USER = 'userSpaceOnUse';

export function linear(id, stops, { x1 = 0, y1 = 0, x2 = 0, y2 = 1, units } = {}) {
  return s('linearGradient', { id, x1, y1, x2, y2, gradientUnits: units },
    stops.map(([o, c, a]) => s('stop', { offset: o, 'stop-color': c, 'stop-opacity': a ?? null })));
}

export function radial(id, stops, { cx = 0.5, cy = 0.5, r = 0.5, fx, fy, units, transform } = {}) {
  return s('radialGradient', { id, cx, cy, r, fx, fy, gradientUnits: units, gradientTransform: transform },
    stops.map(([o, c, a]) => s('stop', { offset: o, 'stop-color': c, 'stop-opacity': a ?? null })));
}

/** Stops positioned in millimetres along a gradient of the given span. */
const spanStops = (stops) => {
  const a = stops[0][0];
  const b = stops[stops.length - 1][0];
  return stops.map(([v, c, o]) => [Math.round(((v - a) / (b - a)) * 10000) / 10000, c, o]);
};

/** Horizontal gradient from stops [[x mm, colour, opacity?], ...]. */
export const acrossX = (id, stops) => linear(id, spanStops(stops), { x1: stops[0][0], y1: 0, x2: stops[stops.length - 1][0], y2: 0, units: USER });

/** Vertical gradient from stops [[y mm, colour, opacity?], ...]. */
export const acrossY = (id, stops) => linear(id, spanStops(stops), { x1: 0, y1: stops[0][0], x2: 0, y2: stops[stops.length - 1][0], units: USER });

/** The SVG's <defs>: gradients added once and referred to by name. */
export class Defs {
  constructor(id) {
    this.id = id;
    this.el = s('defs');
    this.seen = new Set();
  }

  add(...nodes) {
    this.el.append(...nodes);
  }

  url(name) {
    return `url(#${this.id}-${name})`;
  }

  /** Vertical gradient shared under a name; returns its url. */
  vertical(name, stops) {
    if (!this.seen.has(name)) {
      this.seen.add(name);
      this.add(acrossY(`${this.id}-${name}`, stops));
    }
    return this.url(name);
  }
}

const rgb = (h) => [1, 3, 5].map((i) => parseInt(h.slice(i, i + 2), 16));
const hex = (c) => `#${c.map((v) => Math.round(v).toString(16).padStart(2, '0')).join('')}`;

export function mix(a, b, t) {
  const pa = rgb(a);
  const pb = rgb(b);
  return hex(pa.map((v, i) => v + (pb[i] - v) * t));
}

/** Piecewise-linear lookup in [[x, y], ...]; constant outside the table. */
export function lookup(table, x) {
  if (x <= table[0][0]) return table[0][1];
  for (let i = 1; i < table.length; i++) {
    if (x <= table[i][0]) {
      const [x0, y0] = table[i - 1];
      const [x1, y1] = table[i];
      return y0 + ((y1 - y0) * (x - x0)) / (x1 - x0);
    }
  }
  return table[table.length - 1][1];
}

/** Colour at x along [[x, '#rrggbb'], ...]. */
export function colorAt(table, x) {
  if (x <= table[0][0]) return table[0][1];
  for (let i = 1; i < table.length; i++) {
    if (x <= table[i][0]) {
      const [x0, c0] = table[i - 1];
      const [x1, c1] = table[i];
      return mix(c0, c1, (x - x0) / (x1 - x0));
    }
  }
  return table[table.length - 1][1];
}

/** Colour list at x along [[x, 'rrggbb rrggbb ...'], ...]. */
export function rampAt(table, x) {
  const row = (t) => t.split(' ').map((c) => `#${c}`);
  if (x <= table[0][0]) return row(table[0][1]);
  for (let i = 1; i < table.length; i++) {
    if (x <= table[i][0]) {
      const a = row(table[i - 1][1]);
      const b = row(table[i][1]);
      const t = (x - table[i - 1][0]) / (table[i][0] - table[i - 1][0]);
      return a.map((c, k) => mix(c, b[k], t));
    }
  }
  return row(table[table.length - 1][1]);
}
