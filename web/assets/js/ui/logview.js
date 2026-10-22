// Simulator log viewer (a help tab): the WebSocket "log" channel
// (PROTOCOL.md §1), i.e. SimH's own output and the "sim> …" commands pdp11d
// sends it. Useful when a power-on or a boot fails. The channel is
// subscribed only while the viewer is shown: on every (re)subscribe the
// server sends the history as the first "log" message, then live lines.
// The view follows the end unless the user scrolled up.

import { copyText, h } from '../util.js';

const MAX_LINES = 5000; // kept in the view; older lines are dropped
const SLACK = 6000; // trim only when this many have accumulated
const AT_END = 4; // px from the bottom that still counts as "at the end"

export class LogView {
  /**
   * @param {object} app  controller (main.js): setLogView(), toast()
   */
  constructor(app) {
    this.app = app;
    this.lines = [];
    this.follow = true;
    this.waiting = true; // until the history arrived
    this.el = null;
  }

  /** Builds the view and subscribes to the log channel; close() undoes both. */
  open() {
    this.pre = h('pre', { class: 'log-lines', tabindex: 0, role: 'log', 'aria-label': 'Simulator log', 'aria-live': 'off' });
    this.count = h('span', { class: 'muted log-count' }, 'Loading…');
    this.jump = h('button', { type: 'button', class: 'btn', hidden: true, onclick: () => this.toEnd() }, 'Follow the end');
    const copy = h('button', {
      type: 'button', class: 'btn', title: 'Copy the whole log to the clipboard',
      onclick: () => {
        if (!copyText(this.lines.join('\n'))) this.app.toast('Copy failed: the browser refused clipboard access', 'warn');
        else this.app.toast(`Copied ${this.lines.length} log lines`, 'info');
      },
    }, 'Copy');
    this.pre.addEventListener('scroll', () => {
      const atEnd = this.pre.scrollTop + this.pre.clientHeight >= this.pre.scrollHeight - AT_END;
      this.follow = atEnd;
      this.jump.hidden = atEnd;
    });
    this.el = h('div', { class: 'log-view' },
      h('p', { class: 'muted' }, 'What the simulator printed and the commands pdp11d sent it (lines starting with "sim>"). '
        + 'When a system does not power on or boot, the reason is usually here.'),
      h('div', { class: 'log-bar' }, this.count, h('span', { class: 'grow' }), this.jump, copy),
      this.pre);
    this.app.setLogView(this);
    return this.el;
  }

  close() {
    if (!this.el) return;
    this.el = null;
    this.app.setLogView(null);
  }

  /** The channel was (re)subscribed: the next "log" message is the history. */
  reset() {
    this.lines = [];
    this.waiting = true;
    if (!this.pre) return;
    this.pre.textContent = '';
    this.count.textContent = 'Loading…';
  }

  /** A "log" message: the history first, then live lines. */
  add(lines) {
    if (!this.pre || !Array.isArray(lines)) return;
    this.waiting = false;
    const fresh = lines.map((l) => String(l));
    this.lines.push(...fresh);
    if (this.lines.length > SLACK) {
      this.lines = this.lines.slice(-MAX_LINES);
      this.pre.textContent = this.lines.length ? `${this.lines.join('\n')}\n` : '';
    } else if (fresh.length) {
      this.pre.append(document.createTextNode(`${fresh.join('\n')}\n`));
    }
    this.count.textContent = this.lines.length === 1 ? '1 line' : `${this.lines.length} lines`;
    if (!this.lines.length) this.count.textContent = 'No simulator output yet';
    if (this.follow) this.toEnd();
  }

  toEnd() {
    if (!this.pre) return;
    this.pre.scrollTop = this.pre.scrollHeight;
    this.follow = true;
    this.jump.hidden = true;
  }

  text() {
    return this.lines.join('\n');
  }
}
