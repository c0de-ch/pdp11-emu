// UI preferences: persisted per browser (localStorage) and kept in sync
// between the main window and pop-outs over BroadcastChannel('pdp11-ui').
// The same channel carries the pop-out coordination messages (popout.js).

import { Emitter } from './util.js';
import { defaultLampPreset, LAMP_PRESETS } from './panel/lamps.js';

const KEY = 'pdp11-ui-prefs';
export const CHANNEL = 'pdp11-ui';

export const THEMES = {
  green: { label: 'Green (P1)', fg: '#33ff66', bg: '#041208' },
  amber: { label: 'Amber (P3)', fg: '#ffb000', bg: '#140b00' },
  white: { label: 'White (P4)', fg: '#e6edf3', bg: '#0b0d10' },
};

const DEFAULTS = {
  lampStyle: defaultLampPreset(),
  theme: 'green',
  crt: true,
  cols: 80,
  readouts: false,
  branding: true,
  authenticKeys: false,
  screenReader: false,
};

function sanitize(p) {
  const out = { ...DEFAULTS };
  if (p && typeof p === 'object') {
    if (LAMP_PRESETS[p.lampStyle]) out.lampStyle = p.lampStyle;
    if (THEMES[p.theme]) out.theme = p.theme;
    if (p.cols === 80 || p.cols === 132) out.cols = p.cols;
    for (const k of ['crt', 'readouts', 'branding', 'authenticKeys', 'screenReader']) if (typeof p[k] === 'boolean') out[k] = p[k];
  }
  return out;
}

export class Prefs extends Emitter {
  constructor() {
    super();
    let stored = null;
    try {
      stored = JSON.parse(localStorage.getItem(KEY) || 'null');
    } catch { /* private mode or corrupt */ }
    this.values = sanitize(stored);
    try {
      this.channel = new BroadcastChannel(CHANNEL);
      this.channel.addEventListener('message', (ev) => {
        const m = ev.data;
        if (m?.type === 'prefs') this.apply(sanitize(m.prefs), false);
        else this.emit('message', m);
      });
    } catch {
      this.channel = null; // very old browsers: pop-outs just do not sync
    }
  }

  get(k) {
    return this.values[k];
  }

  all() {
    return { ...this.values };
  }

  set(k, v) {
    this.apply(sanitize({ ...this.values, [k]: v }), true);
  }

  apply(next, broadcast) {
    const changed = Object.keys(next).filter((k) => next[k] !== this.values[k]);
    if (!changed.length) return;
    this.values = next;
    try {
      localStorage.setItem(KEY, JSON.stringify(next));
    } catch { /* quota or private mode */ }
    if (broadcast) this.post({ type: 'prefs', prefs: next });
    for (const k of changed) this.emit('change', k, next[k]);
  }

  /** Posts a coordination message to the other windows of this browser. */
  post(msg) {
    try {
      this.channel?.postMessage(msg);
    } catch { /* closed */ }
  }
}
