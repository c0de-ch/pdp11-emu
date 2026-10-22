// Toast notifications (server toasts, hints for ignored switches, local
// messages). One polite live region; errors stay longer. A toast shown
// under a key (the OS shutdown's progress) is updated in place.

import { h } from '../util.js';

const TIMEOUT = { info: 5000, warn: 8000, error: 12000 };
const MAX = 5;

export class Toasts {
  constructor(host) {
    this.host = host;
    this.host.setAttribute('aria-live', 'polite');
    this.host.setAttribute('role', 'status');
    this.recent = new Map(); // text -> time, to fold duplicates
    this.keyed = new Map(); // key -> toast
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
   * Drops the oldest toasts beyond MAX, and while the stack is taller than
   * the window (the stack grows upwards: their buttons would be off-screen).
   */
  trim() {
    const host = this.host;
    while (host.children.length > MAX || (host.children.length > 1 && host.getBoundingClientRect().top < 0)) host.firstElementChild.remove();
  }

  /** The toast shown under key, if it is still on screen. */
  has(key) {
    const t = this.keyed.get(key);
    return !!t && t.el.isConnected && !t.leaving;
  }

  /** A sticky toast shown under key goes away after its level's usual time (its updates stopped). */
  release(key) {
    const t = this.keyed.get(key);
    if (t?.el.isConnected && !t.leaving) t.arm(TIMEOUT[t.level]);
  }
}
