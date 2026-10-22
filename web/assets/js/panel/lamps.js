// The 64 lamps: absolutely positioned HTML discs over the panel SVG whose
// only animated property is opacity (compositor-only; r4 §3.4). Brightness
// is smoothed per lamp with the real frame time (PANEL_LOGIC.md §5):
//   b += (target - b) * (1 - exp(-dt / tau)),  tau = rise or fall time,
//   opacity = b^(1/2.2), and 0 below 1/64.

/** Smoothing presets (ms), PANEL_LOGIC.md §5. */
export const LAMP_PRESETS = {
  led: { label: 'LED', rise: 15, fall: 15 },
  pidp11: { label: 'PiDP-11', rise: 50, fall: 50 },
  incandescent: { label: 'Incandescent', rise: 21.5, fall: 68 },
  reduced: { label: 'Reduced motion', rise: 150, fall: 150 },
};

export function defaultLampPreset() {
  try {
    if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) return 'reduced';
  } catch { /* old browsers */ }
  return 'led';
}

const DISC = 5.2; // lit disc diameter, mm
const HALO = 1.5; // halo beyond the disc, mm
const SETTLED = 1 / 512;
const MIN_VISIBLE = 1 / 64;
const GAMMA = 1 / 2.2;

export class LampBank {
  /**
   * @param {HTMLElement} layer  absolutely positioned layer covering the panel
   * @param {object} geo         geometry.json
   */
  constructor(layer, geo) {
    const [vx, vy, vw, vh] = geo.viewBox;
    const size = DISC + 2 * HALO;
    this.count = geo.lamps.length;
    this.els = [];
    this.target = new Float32Array(this.count);
    this.b = new Float32Array(this.count);
    this.shown = new Int16Array(this.count).fill(-1); // last written opacity, 1/256 steps
    this.preset = LAMP_PRESETS.led;
    this.running = false;
    this.last = 0;
    const frag = document.createDocumentFragment();
    for (const l of geo.lamps) {
      const e = document.createElement('div');
      e.className = 'lamp';
      e.dataset.lamp = l.id;
      e.style.left = `${(((l.cx - size / 2) - vx) / vw) * 100}%`;
      e.style.top = `${(((l.cy - size / 2) - vy) / vh) * 100}%`;
      e.style.width = `${(size / vw) * 100}%`;
      e.style.height = `${(size / vh) * 100}%`;
      frag.append(e);
      this.els.push(e);
    }
    layer.append(frag);
    this.layer = layer;
    this.tick = this.tick.bind(this);
  }

  setPreset(name) {
    this.preset = LAMP_PRESETS[name] ?? LAMP_PRESETS.led;
  }

  /** New lamp frame (linear duty bytes 0..255); only the newest one matters. */
  setFrame(bytes) {
    const n = Math.min(this.count, bytes.length);
    for (let i = 0; i < n; i++) this.target[i] = bytes[i] / 255;
    this.kick();
  }

  /** All lamps dark (e.g. disconnected). */
  clear() {
    this.target.fill(0);
    this.kick();
  }

  kick() {
    if (this.running) return;
    this.running = true;
    this.last = 0;
    requestAnimationFrame(this.tick);
  }

  tick(now) {
    // dt from the previous frame; a long gap (hidden tab) jumps to the target
    const dt = this.last ? Math.min(now - this.last, 1000) : 16.7;
    this.last = now;
    const kr = 1 - Math.exp(-dt / this.preset.rise);
    const kf = 1 - Math.exp(-dt / this.preset.fall);
    let busy = false;
    for (let i = 0; i < this.count; i++) {
      const t = this.target[i];
      let b = this.b[i];
      if (b !== t) {
        b += (t - b) * (t > b ? kr : kf);
        if (Math.abs(t - b) < SETTLED) b = t;
        else busy = true;
        this.b[i] = b;
      }
      const q = b < MIN_VISIBLE ? 0 : Math.round(Math.pow(b, GAMMA) * 256);
      if (q !== this.shown[i]) {
        this.shown[i] = q;
        this.els[i].style.opacity = q >= 256 ? '1' : String(q / 256);
      }
    }
    if (busy) {
      requestAnimationFrame(this.tick);
    } else {
      this.running = false;
    }
  }

  /** Smoothed linear brightness (0..1) per lamp, for readouts and tests. */
  smoothed() {
    return Float32Array.from(this.b);
  }
}
