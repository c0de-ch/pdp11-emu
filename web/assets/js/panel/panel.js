// The operable front panel: SVG art + 64 HTML lamps + hit layer.
// Control changes are rendered optimistically and sent to the server; the
// server's state.controls echo is authoritative (UI.md §4). The switch
// register is a shared 22-bit word: this window's own toggles are kept as
// bits to set and clear on top of the server's value, so that switches
// another viewer flips meanwhile are not lost.

import { buildPanelSvg } from './svg.js';
import { LampBank, defaultLampPreset } from './lamps.js';
import { ControlsView } from './controls.js';
import { PanelInput } from './input.js';

const PENDING_MS = 1500; // how long an unconfirmed local change wins over the server
const SR_MASK = 0o17777777;

export const DEFAULT_CONTROLS = Object.freeze({
  sr: 0, enableHalt: 'enable', sinstSbus: 'inst', addrKnob: 0, dataKnob: 1, key: 'power', lampTest: false,
  pressed: { LOAD_ADRS: false, EXAM: false, DEP: false, CONT: false, START: false },
});

let instances = 0;

export class FrontPanel {
  /**
   * @param {HTMLElement} host  element the panel is built into (sized by the layout)
   * @param {object} geo        geometry.json
   * @param {object} o
   * @param {(msg: object) => void} o.send            sends a protocol message
   * @param {() => Promise<string>} [o.confirmKeyOff] resolves 'shutdown' | 'plug' | 'cancel'
   * @param {() => boolean} [o.keepFocus]           clicks must not take the focus (console has it)
   */
  constructor(host, geo, { send, confirmKeyOff, keepFocus } = {}) {
    this.geo = geo;
    this.send = send ?? (() => {});
    this.confirmKeyOff = confirmKeyOff ?? (async () => 'plug');
    this.server = structuredClone(DEFAULT_CONTROLS);
    this.pending = new Map(); // control key -> { value, t, hold }
    this.sr = null; // unconfirmed SR toggles: { set, clear, t, sent: [values sent] }
    this.held = new Set(); // momentary names held locally

    const frame = document.createElement('div');
    frame.className = 'panel-frame';
    const parts = buildPanelSvg(geo, { id: `pnl${instances++}` });
    const lampLayer = document.createElement('div');
    lampLayer.className = 'lamp-layer';
    lampLayer.setAttribute('aria-hidden', 'true');
    const hitLayer = document.createElement('div');
    hitLayer.className = 'hit-layer';
    hitLayer.setAttribute('role', 'group');
    hitLayer.setAttribute('aria-label', 'PDP-11/70 front panel controls');
    frame.append(parts.svg, lampLayer, hitLayer);
    host.append(frame);
    this.frame = frame;
    this.svg = parts.svg;

    this.lamps = new LampBank(lampLayer, geo);
    this.lamps.setPreset(defaultLampPreset());
    this.view = new ControlsView(parts, geo);
    this.input = new PanelInput({ layer: hitLayer, geo, model: this, keepFocus });
    this.render();
  }

  // ---------------------------------------------------------------- state

  /** Effective controls: server state with unconfirmed local changes on top. */
  controls() {
    const now = performance.now();
    const c = structuredClone(this.server);
    for (const [k, p] of this.pending) {
      if (!p.hold && now - p.t > PENDING_MS) {
        this.pending.delete(k);
        continue;
      }
      if (k.startsWith('pressed.')) c.pressed[k.slice(8)] = p.value;
      else c[k] = p.value;
    }
    if (this.sr && now - this.sr.t > PENDING_MS) this.sr = null;
    if (this.sr) c.sr = ((c.sr & ~this.sr.clear) | this.sr.set) >>> 0;
    return c;
  }

  /** Server state.controls arrived. */
  setServerControls(sc) {
    if (!sc) return;
    const prevSR = this.server.sr;
    this.server = {
      ...structuredClone(DEFAULT_CONTROLS), ...sc,
      pressed: { ...DEFAULT_CONTROLS.pressed, ...(sc.pressed ?? {}) },
    };
    for (const [k, p] of this.pending) {
      if (p.hold) continue;
      const v = k.startsWith('pressed.') ? this.server.pressed[k.slice(8)] : this.server[k];
      if (v === p.value) this.pending.delete(k);
    }
    this.mergeSR(prevSR);
    this.render();
  }

  /**
   * The server's SR changed: bits it now has as this window set them are
   * confirmed. A value this window never sent means another viewer wrote
   * the register (from its own, older view): this window's unconfirmed bits
   * are sent again on top of it.
   */
  mergeSR(prevSR) {
    const L = this.sr;
    if (!L) return;
    if (performance.now() - L.t > PENDING_MS) {
      this.sr = null; // expired: the server's value stands
      return;
    }
    const s = this.server.sr >>> 0;
    L.set = (L.set & ~s) >>> 0;
    L.clear = (L.clear & s) >>> 0;
    if (!L.set && !L.clear) {
      this.sr = null;
      return;
    }
    if (s !== prevSR && !L.sent.includes(s)) this.sendSR(((s & ~L.clear) | L.set) >>> 0);
  }

  sendSR(v) {
    const L = this.sr;
    L.sent.push(v);
    if (L.sent.length > 32) L.sent.shift();
    this.send({ type: 'sr', value: v });
  }

  setLocal(key, value, hold = false) {
    this.pending.set(key, { value, t: performance.now(), hold });
    this.render();
    this.armPendingTimer();
  }

  armPendingTimer() {
    if (!this.pendingTimer) {
      // re-render when unconfirmed changes expire so the server value shows
      this.pendingTimer = setTimeout(() => {
        this.pendingTimer = 0;
        this.render();
      }, PENDING_MS + 50);
    }
  }

  render() {
    const c = this.controls();
    this.view.render(c);
    this.input.updateAria(c);
  }

  // ---------------------------------------------------------------- model API used by PanelInput

  setSR(value) {
    const v = (value & SR_MASK) >>> 0;
    const changed = (v ^ this.controls().sr) >>> 0;
    const L = this.sr ?? { set: 0, clear: 0, sent: [] };
    L.set = ((L.set & ~changed) | (v & changed)) >>> 0;
    L.clear = ((L.clear & ~changed) | (~v & changed & SR_MASK)) >>> 0;
    L.t = performance.now();
    this.sr = L;
    this.render();
    this.armPendingTimer();
    this.sendSR(v);
  }

  setLatch(name, value) {
    this.setLocal(name === 'ENABLE_HALT' ? 'enableHalt' : 'sinstSbus', value);
    this.send({ type: 'latch', name, value });
  }

  press(name, active) {
    if (active) this.held.add(name);
    else this.held.delete(name);
    const key = name === 'LAMP_TEST' ? 'lampTest' : `pressed.${name}`;
    this.setLocal(key, active, active);
    this.send({ type: 'momentary', name, active });
  }

  setKnob(name, pos) {
    this.setLocal(name === 'addr' ? 'addrKnob' : 'dataKnob', pos);
    this.send({ type: 'knob', name, pos });
  }

  async turnKey(pos) {
    if (pos === 'off') {
      const choice = await this.confirmKeyOff();
      if (choice === 'shutdown') {
        this.send({ type: 'shutdownOS' });
        return;
      }
      if (choice !== 'plug') return;
    }
    this.setLocal('key', pos);
    this.send({ type: 'key', pos });
  }

  /** Releases every momentary switch held by this window. */
  releaseAll() {
    this.input.releaseAll();
  }

  // ---------------------------------------------------------------- lamps and options

  setFrame(bytes) {
    this.lamps.setFrame(bytes);
  }

  clearLamps() {
    this.lamps.clear();
  }

  setLampPreset(name) {
    this.lamps.setPreset(name);
  }

  setBranding(on) {
    this.frame.classList.toggle('no-branding', !on);
  }

  focus() {
    this.input.focusPaddle(this.input.focusIndex);
  }

  destroy() {
    this.input.destroy();
    clearTimeout(this.pendingTimer);
    this.frame.remove();
  }
}

/** Loads geometry.json (relative URL, works under a reverse-proxy sub-path). */
export async function loadGeometry(url = 'assets/panel/geometry.json') {
  const r = await fetch(url, { cache: 'no-cache' });
  if (!r.ok) throw new Error(`geometry.json: HTTP ${r.status}`);
  return r.json();
}
