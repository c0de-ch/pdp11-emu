// Toast notifications (server toasts, hints for ignored switches, local
// messages). One polite live region; errors stay longer. A toast shown
// under a key (the OS shutdown's progress) is updated in place. Where the stack
// goes is the layout's choice (setZones): places where it covers nothing first.

import { h } from '../util.js';

const TIMEOUT = { info: 5000, warn: 8000, error: 12000 };
const MAX = 5;
const MIN_ZONE_W = 200; // px, narrower than this a toast is mostly line breaks
const WIDE_ZONE_W = 340; // px, narrower than this an action button goes under the text (app.css)
const ENTRANCE = 8; // px a new toast is lower during its entrance animation (app.css) than where it ends up

export class Toasts {
  constructor(host) {
    this.host = host;
    this.host.setAttribute('aria-live', 'polite');
    this.host.setAttribute('role', 'status');
    this.recent = new Map(); // text -> time, to fold duplicates
    this.keyed = new Map(); // key -> toast
    this.zones = [];
  }

  /**
   * The places for the stack, best first, in viewport px: {right, width,
   * maxH} and either bottom (the stack grows upwards) or top (downwards).
   */
  setZones(zones) {
    this.zones = zones;
    this.trim();
  }

  /** Puts the stack into the first zone it fits, else into the last one; returns that zone. */
  place() {
    const host = this.host;
    let zone = null;
    if (!host.children.length) return zone;
    for (const z of this.zones) {
      if (z.width < MIN_ZONE_W) continue;
      zone = z;
      host.style.right = `${z.right}px`;
      host.style.bottom = z.bottom === undefined ? 'auto' : `${z.bottom}px`;
      host.style.top = z.top === undefined ? 'auto' : `${z.top}px`;
      host.style.setProperty('--toast-w', `${z.width}px`);
      host.classList.toggle('narrow', z.width < WIDE_ZONE_W);
      if (host.offsetHeight <= z.maxH) break;
    }
    return zone;
  }

  /**
   * @param {string} text
   * @param {string} [level]  info | warn | error
   * @param {object} [o]
   * @param {number} [o.timeout]  ms
   * @param {{label: string, onClick: () => void}} [o.action]  a button in the toast
   * @param {string} [o.key]  a toast already shown under this key gets the new text and level
   * @param {boolean} [o.sticky]  stays until updated or dismissed
   */
  show(text, level = 'info', { timeout, action, key, sticky = false } = {}) {
    if (!text) return null;
    const lvl = TIMEOUT[level] ? level : 'info';
    const now = Date.now();
    const shown = key ? this.keyed.get(key) : null;
    if (shown?.el.isConnected && !shown.leaving) {
      shown.el.className = `toast toast-${lvl}`;
      if (lvl === 'error') shown.el.setAttribute('role', 'alert');
      else shown.el.removeAttribute('role');
      shown.el.querySelector('.toast-text').textContent = text;
      shown.level = lvl;
      shown.arm(sticky ? 0 : timeout ?? TIMEOUT[lvl]);
      this.trim();
      return shown.el;
    }
    const prev = key ? null : this.recent.get(text);
    if (prev && now - prev.t < 1500 && prev.el.isConnected) {
      prev.t = now;
      prev.count++;
      prev.el.querySelector('.toast-count').textContent = `×${prev.count}`;
      return prev.el;
    }
    const el = h('div', { class: `toast toast-${lvl}`, role: lvl === 'error' ? 'alert' : null },
      h('span', { class: 'toast-text' }, text),
      h('span', { class: 'toast-count' }),
      action ? h('button', { type: 'button', class: 'btn toast-action', onclick: () => { t.remove(); action.onClick(); } }, action.label) : null,
      h('button', { type: 'button', class: 'icon-btn toast-x', 'aria-label': 'Dismiss', onclick: () => t.remove() }, '×'),
    );
    const t = {
      el,
      level: lvl,
      leaving: false,
      timer: 0,
      remove: () => {
        clearTimeout(t.timer);
        t.leaving = true;
        el.classList.add('leaving');
        setTimeout(() => el.remove(), 160);
        if (key && this.keyed.get(key) === t) this.keyed.delete(key);
      },
      arm: (ms) => {
        clearTimeout(t.timer);
        if (ms) t.timer = setTimeout(t.remove, ms);
      },
    };
    this.host.append(el);
    this.trim();
    if (key) this.keyed.set(key, t);
    else this.recent.set(text, { t: now, el, count: 1 });
    t.arm(sticky ? 0 : timeout ?? TIMEOUT[lvl]);
    return el;
  }

  /**
   * Drops the oldest toasts beyond MAX, and while the stack is larger than the
   * last of its places (it would run over what lies below it) or leaves the
   * window (its buttons would be off-screen); the stack then has its place.
   */
  trim() {
    const host = this.host;
    while (host.children.length > MAX) host.firstElementChild.remove();
    let zone = this.place();
    const tooLarge = () => {
      const r = host.getBoundingClientRect();
      return r.top < 0 || r.bottom > innerHeight - ENTRANCE || (zone && host.offsetHeight > zone.maxH);
    };
    while (host.children.length > 1 && tooLarge()) {
      host.firstElementChild.remove();
      zone = this.place();
    }
  }

  /** The toast shown under key, if it is still on screen. */
  has(key) {
    const t = this.keyed.get(key);
    return !!t && t.el.isConnected && !t.leaving;
  }

  /** The toast shown under key goes away now: what it says is no longer true. */
  dismiss(key) {
    this.keyed.get(key)?.remove();
  }

  /** A sticky toast shown under key goes away after its level's usual time (its updates stopped). */
  release(key) {
    const t = this.keyed.get(key);
    if (t?.el.isConnected && !t.leaving) t.arm(TIMEOUT[t.level]);
  }
}
