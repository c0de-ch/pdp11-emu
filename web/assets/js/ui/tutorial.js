// Toggle-in tutorial (UI.md §6): a light chaser on the DISPLAY REGISTER.
// A floating, non-modal card next to the panel that follows the user's
// progress and can perform every step itself ("Auto-toggle") with the real
// panel messages, so the paddles visibly move.

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

export class Tutorial {
  constructor(app) {
    this.app = app;
    this.el = null;
    this.step = 0;
    this.deposited = 0;
    this.running = false;
    this.abort = false;
  }

  open() {
    if (this.el) {
      this.el.querySelector('button')?.focus();
      return;
    }
    this.step = 0;
    this.deposited = 0;
    this.list = h('ol', {});
    this.live = h('div', { class: 'live', 'aria-live': 'off' });
    this.autoBtn = h('button', { type: 'button', class: 'btn btn-primary', onclick: () => (this.running ? this.stop() : this.auto()) }, 'Auto-toggle');
    const head = h('header', {}, h('h2', {}, 'Toggle-in tutorial'),
      h('button', { type: 'button', class: 'icon-btn', 'aria-label': 'Close tutorial', onclick: () => this.close() }, '×'));
    this.el = h('section', { class: 'tutorial', role: 'dialog', 'aria-label': 'Toggle-in tutorial' },
      head, this.list, this.live,
      h('footer', {},
        h('button', { type: 'button', class: 'btn', onclick: () => this.advance(-1) }, 'Back'),
        h('button', { type: 'button', class: 'btn', onclick: () => this.advance(1) }, 'Next'),
        this.autoBtn));
    this.dragging(head);
    document.body.append(this.el);
    this.render();
    this.unsub = this.app.onSent((m) => this.observe(m));
  }

  close() {
    this.stop();
    this.unsub?.();
    this.el?.remove();
    this.el = null;
  }

  dragging(handle) {
    handle.addEventListener('pointerdown', (e) => {
      if (e.target.closest('button')) return;
      const r = this.el.getBoundingClientRect();
      const dx = e.clientX - r.left;
      const dy = e.clientY - r.top;
      handle.setPointerCapture(e.pointerId);
      const move = (ev) => {
        this.el.style.left = `${Math.max(0, Math.min(innerWidth - r.width, ev.clientX - dx))}px`;
        this.el.style.top = `${Math.max(0, Math.min(innerHeight - 40, ev.clientY - dy))}px`;
        this.el.style.right = 'auto';
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
      [h('span', {}, 'Turn the ', h('b', {}, 'DATA'), ' knob to ', h('b', {}, 'DISPLAY REGISTER'), ': a single light walks across the DATA lamps.')],
    ];
  }

  render() {
    if (!this.el) return;
    this.list.textContent = '';
    this.steps().forEach((content, i) => {
      this.list.append(h('li', { class: i < this.step ? 'done' : i === this.step ? 'current' : '', 'aria-current': i === this.step ? 'step' : null }, ...content));
    });
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
