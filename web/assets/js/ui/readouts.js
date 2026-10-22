// Optional octal readouts under the panel (accessibility, UI.md §2):
// switch register, ADDRESS and DATA lamps. Visible digits update a few times
// a second; the screen-reader text at most once a second (UI.md §4).

import { h, octal } from '../util.js';

/** ADDRESS/DATA values from a lamp frame (a lamp counts as lit at >= 50 % duty). */
export function lampBits(frame) {
  if (!frame) return null;
  let address = 0;
  let data = 0;
  for (let i = 0; i < 22; i++) if (frame[i] >= 128) address += 2 ** i;
  for (let i = 0; i < 16; i++) if (frame[22 + i] >= 128) data |= 1 << i;
  return { address, data };
}

export class Readouts {
  constructor(host, app) {
    this.host = host;
    this.app = app;
    this.sr = h('b', {}, '--------');
    this.addr = h('b', {}, '--------');
    this.data = h('b', {}, '------');
    this.sronly = h('span', { class: 'sr-only', 'aria-live': 'off' });
    host.append(
      h('span', { 'aria-hidden': 'true' }, 'SR ', this.sr),
      h('span', { 'aria-hidden': 'true' }, 'ADDRESS ', this.addr),
      h('span', { 'aria-hidden': 'true' }, 'DATA ', this.data),
      this.sronly,
    );
    this.timer = 0;
    this.lastSr = 0;
  }

  start() {
    if (this.timer) return;
    this.timer = setInterval(() => this.update(), 250);
    this.update();
  }

  stop() {
    clearInterval(this.timer);
    this.timer = 0;
  }

  update() {
    const c = this.app.controls();
    const bits = lampBits(this.app.lastFrame);
    const sr = octal(c?.sr ?? 0, 8);
    const addr = bits ? octal(bits.address, 8) : '--------';
    const data = bits ? octal(bits.data, 6) : '------';
    this.sr.textContent = sr;
    this.addr.textContent = addr;
    this.data.textContent = data;
    const now = Date.now();
    if (now - this.lastSr >= 1000) {
      this.lastSr = now;
      this.sronly.textContent = `Switch register ${sr}, address ${addr}, data ${data}`;
    }
  }
}
