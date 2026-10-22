// Renders control positions onto the panel SVG: paddle poses (REST, BACK,
// FWD; UI.md §3), knob and key rotation. Momentary poses are held at least
// MIN_HOLD ms so that very short clicks remain visible.

export const MIN_HOLD = 120;

export const KEY_ANGLE = { off: 225, power: 315, lock: 405 };

/** Pose of a paddle for a given (effective) controls object. */
export function poseFor(p, c) {
  switch (p.type) {
    case 'sr':
      return ((c.sr >>> p.bit) & 1) ? 'back' : 'rest';
    case 'latch':
      return (p.name === 'ENABLE_HALT' ? c.enableHalt === 'halt' : c.sinstSbus === 'bus') ? 'fwd' : 'rest';
    case 'momentary': {
      const on = p.name === 'LAMP_TEST' ? !!c.lampTest : !!c.pressed?.[p.name];
      if (!on) return 'rest';
      return p.dir === 'up' ? 'back' : 'fwd';
    }
    default:
      return 'rest';
  }
}

export class ControlsView {
  /**
   * @param {object} parts  result of buildPanelSvg (paddles, knobs, key)
   * @param {object} geo    geometry.json
   */
  constructor(parts, geo) {
    this.parts = parts;
    this.geo = geo;
    this.knobAngle = new Map(); // cumulative angle per knob (no backwards spin on wrap)
    this.activeSince = new Map(); // momentary name -> time its active pose was first shown
    this.holdTimer = 0;
    this.last = null;
  }

  render(c) {
    this.last = c;
    const now = performance.now();
    let pendingRelease = Infinity;
    for (const p of this.geo.paddles) {
      const g = this.parts.paddles.get(p.name);
      let pose = poseFor(p, c);
      if (p.type === 'momentary') {
        if (pose !== 'rest') {
          if (!this.activeSince.has(p.name)) this.activeSince.set(p.name, now);
        } else if (this.activeSince.has(p.name)) {
          const left = this.activeSince.get(p.name) + MIN_HOLD - now;
          if (left > 0) {
            pose = p.dir === 'up' ? 'back' : 'fwd';
            pendingRelease = Math.min(pendingRelease, left);
          } else {
            this.activeSince.delete(p.name);
          }
        }
      }
      if (g.dataset.pose !== pose) g.dataset.pose = pose;
    }
    if (pendingRelease !== Infinity && !this.holdTimer) {
      this.holdTimer = setTimeout(() => {
        this.holdTimer = 0;
        if (this.last) this.render(this.last);
      }, pendingRelease + 1);
    }
    this.rotateKnob('addr', c.addrKnob);
    this.rotateKnob('data', c.dataKnob);
    const ka = KEY_ANGLE[c.key] ?? KEY_ANGLE.power;
    const key = this.parts.key;
    if (key.dataset.angle !== String(ka)) {
      key.dataset.angle = String(ka);
      key.style.transform = `rotate(${ka}deg)`;
    }
  }

  rotateKnob(name, pos) {
    const g = this.parts.knobs.get(name);
    const target = 22.5 + 45 * (((pos | 0) % 8 + 8) % 8);
    let a = this.knobAngle.get(name);
    if (a === undefined) {
      a = target;
    } else {
      const cur = ((a % 360) + 360) % 360;
      let d = target - cur;
      if (d > 180) d -= 360;
      if (d < -180) d += 360;
      a += d;
    }
    if (a !== this.knobAngle.get(name)) {
      this.knobAngle.set(name, a);
      g.style.transform = `rotate(${a}deg)`;
    }
  }

  setHover(name, on) {
    this.parts.paddles.get(name)?.classList.toggle('hover', on);
  }

  setFocus(name, on) {
    this.parts.paddles.get(name)?.classList.toggle('focus', on);
  }
}
