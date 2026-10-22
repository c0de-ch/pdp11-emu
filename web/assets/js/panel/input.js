// Pointer, touch, pen and keyboard interaction with the front panel
// (UI.md §4). A transparent HTML hit layer over the SVG carries the ARIA
// roles and the focus ring; the visuals stay in the SVG.

const FLICK_MM = 6; // vertical movement that sets a paddle explicitly
const DRAG_MM = 2; // movement that turns a knob click into a drag
const WHEEL_STEP = 60; // px of wheel delta per knob detent
const HYSTERESIS = 10; // degrees beyond the half-way point before a knob drag snaps on
const KEY_ORDER = ['off', 'power', 'lock'];

const ADDR_KEYS = { '[': -1, ']': 1 };
const DATA_KEYS = { '{': -1, '}': 1 };
const MOMENTARY_KEYS = { l: 'LOAD_ADRS', e: 'EXAM', d: 'DEP', c: 'CONT', s: 'START', t: 'LAMP_TEST' };

const pct = (v) => `${v}%`;

export class PanelInput {
  /**
   * @param {object} o
   * @param {HTMLElement} o.layer  hit layer (absolutely positioned over the panel)
   * @param {object} o.geo         geometry.json
   * @param {object} o.model       FrontPanel: controls(), setSR(), setLatch(), press(), setKnob(), turnKey()
   * @param {() => boolean} [o.keepFocus]  true: a click must not take the keyboard focus
   *        (e.g. the console has it); otherwise the clicked control is focused so
   *        that the panel shortcuts work
   */
  constructor({ layer, geo, model, keepFocus }) {
    this.layer = layer;
    this.geo = geo;
    this.model = model;
    this.keepFocus = keepFocus ?? (() => false);
    this.hits = new Map(); // paddle name -> element
    this.knobEls = new Map();
    this.held = new Map(); // momentary name -> source ("pointer:<id>" | "key:<k>")
    this.focusIndex = 0;
    this.build();
    this.bind();
  }

  // ---------------------------------------------------------------- DOM

  box(x0, y0, x1, y1) {
    const [vx, vy, vw, vh] = this.geo.viewBox;
    return {
      left: pct(((x0 - vx) / vw) * 100), top: pct(((y0 - vy) / vh) * 100),
      width: pct(((x1 - x0) / vw) * 100), height: pct(((y1 - y0) / vh) * 100),
    };
  }

  build() {
    const frag = document.createDocumentFragment();
    for (const p of this.geo.paddles) {
      const e = document.createElement('div');
      e.className = `hit hit-paddle hit-${p.type}`;
      e.dataset.name = p.name;
      Object.assign(e.style, this.box(p.hit.x0, p.hit.y0, p.hit.x1, p.hit.y1));
      e.tabIndex = p.slot === 0 ? 0 : -1;
      if (p.type === 'sr') {
        e.setAttribute('role', 'switch');
        e.setAttribute('aria-label', `Switch register bit ${p.bit}`);
      } else if (p.type === 'latch') {
        e.setAttribute('role', 'switch');
        e.setAttribute('aria-label', p.name === 'ENABLE_HALT' ? 'HALT (ENABLE/HALT switch)' : 'S BUS CYCLE (S INST/S BUS CYCLE switch)');
      } else {
        e.setAttribute('role', 'button');
        e.setAttribute('aria-label', p.label);
        e.setAttribute('aria-pressed', 'false');
      }
      e.title = p.type === 'sr' ? `SR ${p.bit}` : p.label;
      this.hits.set(p.name, e);
      frag.append(e);
    }
    for (const kn of this.geo.knobs) {
      const r = kn.r + 1.5;
      const e = document.createElement('div');
      e.className = 'hit hit-knob';
      e.dataset.knob = kn.name;
      Object.assign(e.style, this.box(kn.cx - r, kn.cy - r, kn.cx + r, kn.cy + r));
      e.tabIndex = 0;
      e.setAttribute('role', 'slider');
      e.setAttribute('aria-label', `${kn.label} select knob`);
      e.setAttribute('aria-valuemin', '0');
      e.setAttribute('aria-valuemax', '7');
      e.title = `${kn.label} select: click right/left half to turn`;
      this.knobEls.set(kn.name, e);
      frag.append(e);
    }
    const k = this.geo.key;
    const kr = 12;
    const key = document.createElement('div');
    key.className = 'hit hit-key';
    Object.assign(key.style, this.box(k.cx - kr, k.cy - kr, k.cx + kr, k.cy + kr));
    key.tabIndex = 0;
    key.setAttribute('role', 'slider');
    key.setAttribute('aria-label', 'Key switch');
    key.setAttribute('aria-valuemin', '0');
    key.setAttribute('aria-valuemax', '2');
    key.title = 'Key switch OFF / POWER / LOCK: click right half to turn clockwise, left half anticlockwise';
    this.keyEl = key;
    frag.append(key);
    this.layer.append(frag);
  }

  /** Mirror the effective controls into ARIA state. */
  updateAria(c) {
    for (const p of this.geo.paddles) {
      const e = this.hits.get(p.name);
      if (p.type === 'sr') e.setAttribute('aria-checked', String(((c.sr >>> p.bit) & 1) === 1));
      else if (p.type === 'latch') e.setAttribute('aria-checked', String(p.name === 'ENABLE_HALT' ? c.enableHalt === 'halt' : c.sinstSbus === 'bus'));
      else e.setAttribute('aria-pressed', String(p.name === 'LAMP_TEST' ? !!c.lampTest : !!c.pressed?.[p.name]));
    }
    for (const kn of this.geo.knobs) {
      const pos = kn.name === 'addr' ? c.addrKnob : c.dataKnob;
      const e = this.knobEls.get(kn.name);
      e.setAttribute('aria-valuenow', String(pos));
      e.setAttribute('aria-valuetext', kn.positions[pos] ?? String(pos));
    }
    this.keyEl.setAttribute('aria-valuenow', String(Math.max(0, KEY_ORDER.indexOf(c.key))));
    this.keyEl.setAttribute('aria-valuetext', (c.key || 'power').toUpperCase());
  }

  // ---------------------------------------------------------------- helpers

  toMM(ev) {
    const r = this.layer.getBoundingClientRect();
    const [vx, vy, vw, vh] = this.geo.viewBox;
    return { x: vx + ((ev.clientX - r.left) / r.width) * vw, y: vy + ((ev.clientY - r.top) / r.height) * vh };
  }

  slotAt(xmm) {
    const k = Math.round((xmm - this.geo.col0) / this.geo.pitch);
    return Math.max(0, Math.min(this.geo.paddles.length - 1, k));
  }

  paddleByName(name) {
    return this.geo.paddles.find((p) => p.name === name);
  }

  // ---------------------------------------------------------------- binding

  bind() {
    const L = this.layer;
    // Pointer handlers call preventDefault (no text selection, no compatibility
    // mouse events), which also stops the browser from moving the focus:
    // focus the control explicitly, unless the console must keep it.
    L.addEventListener('mousedown', (e) => { if (e.target.closest('.hit')) e.preventDefault(); });
    L.addEventListener('contextmenu', (e) => { if (e.target.closest('.hit')) e.preventDefault(); });
    L.addEventListener('pointerdown', (e) => this.onPointerDown(e));
    L.addEventListener('wheel', (e) => this.onWheel(e), { passive: false });
    L.addEventListener('pointerover', (e) => {
      const h = e.target.closest('.hit-paddle');
      if (h) this.model.view.setHover(h.dataset.name, true);
    });
    L.addEventListener('pointerout', (e) => {
      const h = e.target.closest('.hit-paddle');
      if (h) this.model.view.setHover(h.dataset.name, false);
    });
    L.addEventListener('focusin', (e) => {
      const h = e.target.closest('.hit-paddle');
      if (h) {
        this.focusIndex = this.paddleByName(h.dataset.name).slot;
        this.model.view.setFocus(h.dataset.name, true);
      }
    });
    L.addEventListener('focusout', (e) => {
      const h = e.target.closest('.hit-paddle');
      if (h) this.model.view.setFocus(h.dataset.name, false);
      if (!L.contains(e.relatedTarget)) this.releaseAll('key');
    });
    L.addEventListener('keydown', (e) => this.onKeyDown(e));
    L.addEventListener('keyup', (e) => this.onKeyUp(e));
    this.onBlur = () => this.releaseAll();
    this.onVisibility = () => { if (document.visibilityState !== 'visible') this.releaseAll(); };
    window.addEventListener('blur', this.onBlur);
    document.addEventListener('visibilitychange', this.onVisibility);
  }

  destroy() {
    this.releaseAll();
    window.removeEventListener('blur', this.onBlur);
    document.removeEventListener('visibilitychange', this.onVisibility);
  }

  // ---------------------------------------------------------------- momentary bookkeeping

  pressMomentary(name, source) {
    if (this.held.has(name)) return;
    this.held.set(name, source);
    this.model.press(name, true);
  }

  releaseMomentary(name, source) {
    const s = this.held.get(name);
    if (s === undefined || (source && s !== source)) return;
    this.held.delete(name);
    this.model.press(name, false);
  }

  /** Release every held momentary switch (optionally only those held by keys or pointers). */
  releaseAll(kind) {
    for (const [name, src] of [...this.held]) {
      if (!kind || src.startsWith(kind)) this.releaseMomentary(name);
    }
  }

  // ---------------------------------------------------------------- pointer

  onPointerDown(e) {
    const hit = e.target.closest('.hit');
    if (!hit || e.button > 0) return;
    if (!this.keepFocus() && document.activeElement !== hit) {
      if (hit.classList.contains('hit-paddle')) this.focusPaddle(this.paddleByName(hit.dataset.name).slot, { visible: false });
      else hit.focus({ preventScroll: true, focusVisible: false });
    }
    if (hit.classList.contains('hit-paddle')) this.paddlePointer(e, hit);
    else if (hit.classList.contains('hit-knob')) this.knobPointer(e, hit);
    else if (hit.classList.contains('hit-key')) this.keyPointer(e);
  }

  capture(e, el, onMove, onEnd) {
    try { el.setPointerCapture(e.pointerId); } catch { /* synthetic events */ }
    const move = (ev) => { if (ev.pointerId === e.pointerId) onMove(ev); };
    let done = false;
    const end = (ev) => {
      if (ev.pointerId !== e.pointerId || done) return;
      done = true;
      el.removeEventListener('pointermove', move);
      el.removeEventListener('pointerup', end);
      el.removeEventListener('pointercancel', end);
      el.removeEventListener('lostpointercapture', end);
      onEnd(ev);
    };
    el.addEventListener('pointermove', move);
    el.addEventListener('pointerup', end);
    el.addEventListener('pointercancel', end);
    el.addEventListener('lostpointercapture', end);
  }

  paddlePointer(e, hit) {
    const p = this.paddleByName(hit.dataset.name);
    e.preventDefault();
    const start = this.toMM(e);
    if (p.type === 'momentary') {
      const src = `pointer:${e.pointerId}`;
      this.pressMomentary(p.name, src);
      this.capture(e, hit, () => {}, () => this.releaseMomentary(p.name, src));
      return;
    }
    if (p.type === 'latch') {
      let flicked = false;
      this.capture(e, hit, (ev) => {
        const dy = this.toMM(ev).y - start.y;
        if (!flicked && Math.abs(dy) >= FLICK_MM) {
          flicked = true;
          this.setLatchDown(p, dy > 0);
        }
      }, (ev) => {
        if (!flicked && ev.type === 'pointerup') this.toggleLatch(p);
      });
      return;
    }
    // SR paddle: click toggles; drag across paddles paints; vertical flick sets explicitly
    let paint = null; // value being painted, once dragging across slots
    let flicked = null;
    const startSlot = p.slot;
    let lastSlot = startSlot;
    this.capture(e, hit, (ev) => {
      const m = this.toMM(ev);
      const dy = m.y - start.y;
      if (paint === null && flicked === null && Math.abs(dy) >= FLICK_MM && Math.abs(dy) > Math.abs(m.x - start.x)) {
        flicked = dy < 0 ? 1 : 0;
        this.setBit(p.bit, flicked);
      }
      const slot = this.slotAt(m.x);
      if (slot === lastSlot) return;
      if (paint === null) {
        paint = flicked ?? (1 - this.bitOf(p.bit));
        this.setBit(p.bit, paint);
      }
      // every slot passed since the last move (fast drags skip some)
      const dir = Math.sign(slot - lastSlot);
      let value = this.model.controls().sr >>> 0;
      for (let k = lastSlot + dir; k !== slot + dir; k += dir) {
        const q = this.geo.paddles[k];
        if (q.type === 'sr') value = paint ? (value | (1 << q.bit)) >>> 0 : (value & ~(1 << q.bit)) >>> 0;
      }
      lastSlot = slot;
      if (value !== this.model.controls().sr >>> 0) this.model.setSR(value);
    }, (ev) => {
      if (paint === null && flicked === null && ev.type === 'pointerup') this.setBit(p.bit, 1 - this.bitOf(p.bit));
    });
  }

  knobPointer(e, hit) {
    const kn = this.geo.knobs.find((k) => k.name === hit.dataset.knob);
    e.preventDefault();
    const start = this.toMM(e);
    let dragging = false;
    this.capture(e, hit, (ev) => {
      const m = this.toMM(ev);
      if (!dragging && Math.hypot(m.x - start.x, m.y - start.y) >= DRAG_MM) dragging = true;
      if (!dragging) return;
      const dx = m.x - kn.cx;
      const dy = m.y - kn.cy;
      if (Math.hypot(dx, dy) < 2) return;
      const theta = ((Math.atan2(dx, -dy) * 180) / Math.PI + 360) % 360;
      const pos = this.knobPos(kn.name);
      let d = theta - (22.5 + 45 * pos);
      d = ((d % 360) + 540) % 360 - 180;
      if (Math.abs(d) > 22.5 + HYSTERESIS) {
        const k = ((Math.round((theta - 22.5) / 45) % 8) + 8) % 8;
        if (k !== pos) this.model.setKnob(kn.name, k);
      }
    }, (ev) => {
      if (!dragging && ev.type === 'pointerup') this.turnKnob(kn.name, start.x >= kn.cx ? 1 : -1);
    });
  }

  keyPointer(e) {
    e.preventDefault();
    const start = this.toMM(e);
    const hit = this.keyEl;
    this.capture(e, hit, () => {}, (ev) => {
      if (ev.type === 'pointerup') this.turnKey(start.x >= this.geo.key.cx ? 1 : -1);
    });
  }

  onWheel(e) {
    const hit = e.target.closest('.hit-knob');
    if (!hit) return;
    e.preventDefault();
    const unit = e.deltaMode === 1 ? 16 : e.deltaMode === 2 ? 400 : 1;
    const delta = (Math.abs(e.deltaY) >= Math.abs(e.deltaX) ? e.deltaY : e.deltaX) * unit;
    const name = hit.dataset.knob;
    this.wheelAcc = this.wheelAcc && this.wheelAcc.name === name ? this.wheelAcc : { name, v: 0 };
    this.wheelAcc.v += delta;
    while (Math.abs(this.wheelAcc.v) >= WHEEL_STEP) {
      const dir = Math.sign(this.wheelAcc.v);
      this.wheelAcc.v -= dir * WHEEL_STEP;
      this.turnKnob(name, dir);
    }
  }

  // ---------------------------------------------------------------- actions

  bitOf(bit) {
    return (this.model.controls().sr >>> bit) & 1;
  }

  setBit(bit, v) {
    const sr = this.model.controls().sr >>> 0;
    const next = v ? (sr | (1 << bit)) >>> 0 : (sr & ~(1 << bit)) >>> 0;
    if (next !== sr) this.model.setSR(next);
  }

  toggleLatch(p) {
    const c = this.model.controls();
    const down = p.name === 'ENABLE_HALT' ? c.enableHalt === 'halt' : c.sinstSbus === 'bus';
    this.setLatchDown(p, !down);
  }

  setLatchDown(p, down) {
    this.model.setLatch(p.name, down ? p.down : p.up);
  }

  knobPos(name) {
    const c = this.model.controls();
    return name === 'addr' ? c.addrKnob : c.dataKnob;
  }

  turnKnob(name, dir) {
    this.model.setKnob(name, (((this.knobPos(name) + dir) % 8) + 8) % 8);
  }

  turnKey(dir) {
    const i = KEY_ORDER.indexOf(this.model.controls().key);
    const j = Math.max(0, Math.min(KEY_ORDER.length - 1, (i < 0 ? 1 : i) + dir));
    if (j !== i) this.model.turnKey(KEY_ORDER[j]);
  }

  // ---------------------------------------------------------------- keyboard

  focusPaddle(slot, { visible = true } = {}) {
    const n = this.geo.paddles.length;
    const k = Math.max(0, Math.min(n - 1, slot));
    for (const p of this.geo.paddles) this.hits.get(p.name).tabIndex = p.slot === k ? 0 : -1;
    this.focusIndex = k;
    this.hits.get(this.geo.paddles[k].name).focus({ preventScroll: true, focusVisible: visible });
  }

  onKeyDown(e) {
    if (e.ctrlKey || e.metaKey || e.altKey) return;
    const t = e.target;
    const onPaddle = t.classList?.contains('hit-paddle');
    const onKnob = t.classList?.contains('hit-knob');
    const onKey = t === this.keyEl;
    const k = e.key;
    let handled = true;
    if (onPaddle && (k === 'ArrowLeft' || k === 'ArrowRight' || k === 'Home' || k === 'End')) {
      const p = this.paddleByName(t.dataset.name);
      this.focusPaddle(k === 'Home' ? 0 : k === 'End' ? this.geo.paddles.length - 1 : p.slot + (k === 'ArrowLeft' ? -1 : 1));
    } else if (onPaddle && (k === 'ArrowUp' || k === 'ArrowDown' || k === ' ' || k === 'Enter')) {
      if (!e.repeat) this.paddleKey(this.paddleByName(t.dataset.name), k, `key:${k}`);
    } else if ((onKnob || onKey) && k.startsWith('Arrow')) {
      if (!e.repeat || onKnob) {
        const dir = k === 'ArrowRight' || k === 'ArrowUp' ? 1 : -1;
        if (onKnob) this.turnKnob(t.dataset.knob, dir);
        else this.turnKey(dir);
      }
    } else if (k >= '0' && k <= '7') {
      if (!e.repeat) this.model.setSR((((this.model.controls().sr << 3) | (k.charCodeAt(0) - 48)) & 0o17777777) >>> 0);
    } else if (k === 'Backspace') {
      this.model.setSR(this.model.controls().sr >>> 3);
    } else if (k === 'Escape' || k === 'Delete') {
      this.model.setSR(0);
    } else if (MOMENTARY_KEYS[k.toLowerCase()] && k.length === 1) {
      if (!e.repeat) this.pressMomentary(MOMENTARY_KEYS[k.toLowerCase()], `key:${k.toLowerCase()}`);
    } else if (k === 'h' || k === 'H') {
      if (!e.repeat) this.toggleLatch(this.paddleByName('ENABLE_HALT'));
    } else if (k === 'b' || k === 'B') {
      if (!e.repeat) this.toggleLatch(this.paddleByName('SINST_SBUS'));
    } else if (ADDR_KEYS[k]) {
      this.turnKnob('addr', ADDR_KEYS[k]);
    } else if (DATA_KEYS[k]) {
      this.turnKnob('data', DATA_KEYS[k]);
    } else if (k === 'k' || k === 'K') {
      if (!e.repeat) this.turnKey(e.shiftKey ? -1 : 1);
    } else {
      handled = false;
    }
    if (handled) {
      e.preventDefault();
      e.stopPropagation();
    }
  }

  paddleKey(p, k, src) {
    if (p.type === 'sr') {
      if (k === 'ArrowUp') this.setBit(p.bit, 1);
      else if (k === 'ArrowDown') this.setBit(p.bit, 0);
      else this.setBit(p.bit, 1 - this.bitOf(p.bit));
    } else if (p.type === 'latch') {
      if (k === 'ArrowUp') this.setLatchDown(p, false);
      else if (k === 'ArrowDown') this.setLatchDown(p, true);
      else this.toggleLatch(p);
    } else if (k === ' ' || k === 'Enter' || (k === 'ArrowUp' && p.dir === 'up') || (k === 'ArrowDown' && p.dir === 'down')) {
      this.pressMomentary(p.name, src);
    }
  }

  onKeyUp(e) {
    const k = e.key;
    if (k === ' ' || k === 'Enter' || k === 'ArrowUp' || k === 'ArrowDown') {
      for (const [name, src] of [...this.held]) if (src === `key:${k}`) this.releaseMomentary(name, src);
    }
    const m = MOMENTARY_KEYS[k.toLowerCase?.()];
    if (m && k.length === 1) this.releaseMomentary(m, `key:${k.toLowerCase()}`);
  }
}
