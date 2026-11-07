// Toggle-in tutorial (UI.md §6): a light chaser on the DISPLAY REGISTER.
// A non-modal card that follows the user's progress and can perform every step
// itself ("Auto-toggle") with the real panel messages, so the paddles visibly
// move. It sits in the dock under the panel, where it hides nothing; where the
// dock has no room it floats over the console and can be dragged away.

import { h, octal } from '../util.js';
import { osInMemory } from './dialog.js';

export const PROGRAM_START = 0o1000;
// 1000 MOV #1,R0 / 1004 MOV R0,@#177570 / 1010 NOP / 1012 SOB R1,. / 1014 ROL R0 / 1016 BR 1004
// The fifth word was CLR R1 (005001) in an earlier UI.md §6; CLR also clears
// the carry, so ROL loses the bit after one pass and the display goes dark
// after about a second. NOP keeps the carry: the light keeps walking (17 steps:
// 16 lamps and the carry). SOB leaves R1 = 0, which is the next 65536-pass delay.
// Verified on the patched SimH: DR = 1, 2, 4 ... 100000, 0, 1, 2 ...
export const PROGRAM = [0o012700, 0o000001, 0o010037, 0o177570, 0o000240, 0o077101, 0o006100, 0o000772];
const STEP_MS = 380;
const MIN_DOCK_H = 150; // px, a lower dock is too small for the card

export class Tutorial {
  constructor(app) {
    this.app = app;
    this.el = null;
    this.step = 0;
    this.deposited = 0;
    this.running = false;
    this.abort = false;
    this.moved = false; // dragged: stays where the user put it
  }

  open() {
    if (this.el) {
      this.el.querySelector('button')?.focus();
      return;
    }
    this.step = 0;
    this.deposited = 0;
    this.moved = false;
    this.list = h('ol', {});
    this.live = h('div', { class: 'live', 'aria-live': 'off' });
    this.autoBtn = h('button', { type: 'button', class: 'btn btn-primary', onclick: () => (this.running ? this.stop() : this.auto()) }, 'Auto-toggle');
    const head = h('header', { title: 'Drag to move' }, h('h2', {}, 'Toggle-in tutorial'),
      h('button', { type: 'button', class: 'icon-btn', 'aria-label': 'Close tutorial', onclick: () => this.close() }, '×'));
    this.el = h('section', { class: 'tutorial', role: 'dialog', 'aria-label': 'Toggle-in tutorial' },
      head, this.list,
      h('footer', {}, this.live,
        h('span', { class: 'buttons' },
          h('button', { type: 'button', class: 'btn', onclick: () => this.advance(-1) }, 'Back'),
          h('button', { type: 'button', class: 'btn', onclick: () => this.advance(1) }, 'Next'),
          this.autoBtn)));
    this.dragging(head);
    // a non-modal card at the end of the page, behind the console (Tab goes to the
    // PDP-11 there): the keyboard goes to it when it opens, Esc gives the focus back
    this.el.addEventListener('keydown', (e) => {
      if (e.key !== 'Escape') return;
      e.stopPropagation();
      this.close();
    });
    this.prevFocus = document.activeElement;
    document.body.append(this.el);
    this.render();
    this.place();
    this.app.placeToasts?.();
    this.autoBtn.focus({ preventScroll: true });
    this.unsub = this.app.onSent((m) => this.observe(m));
  }

  /** Docks the card under the panel, or floats it over the console (on open and after a layout pass). */
  place() {
    const el = this.el;
    if (!el || this.moved) return;
    const s = el.style;
    const rect = this.app.tutorialDock?.();
    this.docked = !!rect && rect.height >= MIN_DOCK_H;
    el.classList.toggle('docked', this.docked);
    if (this.docked) {
      s.left = `${rect.left}px`;
      s.top = `${rect.top}px`;
      s.width = `${rect.width}px`;
      s.height = `${rect.height}px`;
      s.right = s.bottom = 'auto';
      s.maxHeight = '';
      return;
    }
    s.height = s.width = '';
    const over = this.app.tutorialFloat?.();
    s.top = over ? `${over.top}px` : '';
    s.right = over ? `${over.right}px` : '';
    s.maxHeight = over ? `${over.height}px` : '';
    s.left = s.bottom = '';
  }

  isDocked() {
    return !!this.el && !!this.docked && !this.moved;
  }

  close() {
    this.stop();
    this.unsub?.();
    const inside = this.el?.contains(document.activeElement);
    this.el?.remove();
    this.el = null;
    this.docked = false;
    this.app.placeToasts?.();
    if (inside) {
      const back = this.prevFocus;
      if (back && back !== document.body && document.contains(back)) back.focus({ preventScroll: true });
      else this.app.console?.focus();
    }
  }

  dragging(handle) {
    handle.addEventListener('pointerdown', (e) => {
      if (e.target.closest('button')) return;
      const r = this.el.getBoundingClientRect();
      const dx = e.clientX - r.left;
      const dy = e.clientY - r.top;
      handle.setPointerCapture(e.pointerId);
      const move = (ev) => {
        this.moved = true;
        this.docked = false;
        this.el.classList.remove('docked');
        const top = Math.max(0, Math.min(innerHeight - 40, ev.clientY - dy));
        this.el.style.height = this.el.style.width = '';
        this.el.style.left = `${Math.max(0, Math.min(innerWidth - this.el.offsetWidth, ev.clientX - dx))}px`;
        this.el.style.top = `${top}px`;
        this.el.style.right = 'auto';
        this.el.style.bottom = 'auto';
        this.el.style.maxHeight = `${Math.max(160, innerHeight - top - 8)}px`;
      };
      const up = () => {
        handle.removeEventListener('pointermove', move);
        handle.removeEventListener('pointerup', up);
      };
      handle.addEventListener('pointermove', move);
      handle.addEventListener('pointerup', up);
    });
  }

  steps() {
    const words = h('div', { class: 'words' }, ...PROGRAM.map((w, i) => h('code', { class: i < this.deposited ? 'done' : '' }, octal(w, 6))));
    return [
      [h('span', {}, 'Turn the key to ', h('b', {}, 'OFF'), ', set ', h('b', {}, 'ENABLE/HALT'), ' down to HALT, turn the key to ', h('b', {}, 'POWER'), ': the CPU powers up halted. Set the speed to ', h('b', {}, 'Authentic'), '.')],
      [h('span', {}, 'Set the switch register to ', h('b', {}, '001000'), ' (switch 9 up) and press ', h('b', {}, 'LOAD ADRS'), '.')],
      [h('span', {}, 'For each word: set the switches to it and raise ', h('b', {}, 'DEP'), '. Each DEP after the first steps the address by 2.'), words],
      [h('span', {}, 'Set the switches to ', h('b', {}, '001000'), ', press ', h('b', {}, 'LOAD ADRS'), ', set ', h('b', {}, 'ENABLE/HALT'), ' up to ENABLE and press ', h('b', {}, 'START'), '.')],
      [h('span', {}, 'Turn the ', h('b', {}, 'DATA'), ' knob to ', h('b', {}, 'DISPLAY REGISTER'), ': a single light walks across the DATA lamps (for the first 2-3 s the CPU runs flat out and all of them glow dimly).')],
    ];
  }

  render() {
    if (!this.el) return;
    this.list.textContent = '';
    this.steps().forEach((content, i) => {
      this.list.append(h('li', { class: i < this.step ? 'done' : i === this.step ? 'current' : '', 'aria-current': i === this.step ? 'step' : null }, ...content));
    });
    // a list too long for its place scrolls to the current step
    const cur = this.list.querySelector('.current');
    if (cur) {
      const l = this.list.getBoundingClientRect();
      const c = cur.getBoundingClientRect();
      if (c.top < l.top) this.list.scrollTop += c.top - l.top;
      else if (c.bottom > l.bottom) this.list.scrollTop += c.bottom - l.bottom;
    }
    this.updateLive();
  }

  updateLive() {
    if (!this.el) return;
    const c = this.app.controls();
    const st = this.app.state;
    const lamps = this.app.lampBits();
    this.live.textContent = '';
    this.live.append(
      'SR ', h('b', {}, octal(c?.sr ?? 0, 8)),
      '  ADDRESS ', h('b', {}, lamps ? octal(lamps.address, 8) : '--------'),
      '  DATA ', h('b', {}, lamps ? octal(lamps.data, 6) : '------'),
      '  CPU ', h('b', {}, st?.run ?? '?'),
    );
  }

  advance(d) {
    this.step = Math.max(0, Math.min(5, this.step + d));
    this.render();
  }

  /** Follows the user's progress from state changes (called by main.js). */
  onState() {
    if (!this.el) return;
    const st = this.app.state;
    const c = this.app.controls();
    if (!st || !c) return;
    if (this.step === 0 && st.run === 'halted' && c.enableHalt === 'halt' && st.speed === 'authentic') this.step = 1;
    if (this.step === 1 && st.console?.addr === PROGRAM_START && st.run === 'halted') this.step = 2;
    if (this.step === 3 && st.run === 'running') this.step = 4;
    this.render();
  }

  /** Watches this window's own panel messages for the deposits. */
  observe(m) {
    if (!this.el || this.step !== 2) return;
    if (m.type === 'momentary' && m.name === 'DEP' && m.active) {
      const sr = (this.app.controls()?.sr ?? 0) & 0o177777;
      if (sr === PROGRAM[this.deposited]) {
        this.deposited++;
        if (this.deposited === PROGRAM.length) this.step = 3;
        this.render();
      }
    }
  }

  stop() {
    this.abort = true;
    this.running = false;
    if (this.autoBtn) this.autoBtn.textContent = 'Auto-toggle';
  }

  async auto() {
    const app = this.app;
    const st = app.state;
    if (!app.connected || !st) return;
    if (st.controls?.key === 'lock') {
      app.toast('Unlock the console first (key switch to POWER)', 'warn');
      return;
    }
    if (st.power === 'on' && (st.run === 'running' || osInMemory(st))) {
      const v = await app.confirmStop('Power off for the tutorial');
      if (v === 'cancel') return;
      if (v === 'shutdown') {
        app.send({ type: 'shutdownOS' });
        app.toast('Shutting the OS down; start Auto-toggle again when the machine is off', 'info');
        return;
      }
    }
    this.abort = false;
    this.running = true;
    this.autoBtn.textContent = 'Stop';
    const p = app.panelActions();
    const pause = (ms = STEP_MS) => new Promise((r) => setTimeout(r, ms));
    const check = () => {
      if (this.abort || !app.connected) throw new Error('stopped');
    };
    const waitRun = async (want, ms) => {
      const t0 = Date.now();
      while (!want.includes(app.state?.run)) {
        check();
        if (Date.now() - t0 > ms) throw new Error(`the CPU did not reach "${want.join('/')}"`);
        await pause(100);
      }
    };
    const press = async (name) => {
      p.press(name, true);
      await pause(160);
      p.press(name, false);
      await pause();
      check();
    };
    try {
      this.step = 0;
      this.deposited = 0;
      this.render();
      if (app.state.power === 'on') {
        p.key('off');
        await waitRun(['off'], 15000);
        await pause();
      }
      p.latch('ENABLE_HALT', 'halt');
      await pause();
      p.key('power');
      await waitRun(['halted'], 30000);
      app.send({ type: 'speed', value: 'authentic' });
      await pause();
      this.step = 1;
      this.render();
      p.sr(PROGRAM_START);
      await pause();
      await press('LOAD_ADRS');
      this.step = 2;
      this.render();
      for (const w of PROGRAM) {
        p.sr(w);
        await pause();
        await press('DEP');
        this.deposited++;
        this.render();
      }
      this.step = 3;
      this.render();
      p.sr(PROGRAM_START);
      await pause();
      await press('LOAD_ADRS');
      p.latch('ENABLE_HALT', 'enable');
      await pause();
      await press('START');
      this.step = 4;
      this.render();
      await pause();
      p.knob('data', 3);
      this.step = 5;
      this.render();
    } catch (err) {
      if (err.message !== 'stopped') app.toast(`Auto-toggle stopped: ${err.message}`, 'warn');
    } finally {
      this.running = false;
      if (this.autoBtn) this.autoBtn.textContent = 'Auto-toggle';
    }
  }
}
