// The toolbar (UI.md §2): system chooser, power (mirrors the key), Boot,
// Shut down OS, speed, view settings, pop-outs, help and the status pill.

import { h } from '../util.js';
import { attachMenu } from './menu.js';
import { LAMP_PRESETS } from '../panel/lamps.js';
import { THEMES } from '../prefs.js';

const ICON = {
  power: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 3v8"/><path d="M6.3 6.6a8 8 0 1 0 11.4 0"/></svg>',
  gear: '<svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="3.2"/><path d="M12 2.8v2.4M12 18.8v2.4M2.8 12h2.4M18.8 12h2.4M5.5 5.5l1.7 1.7M16.8 16.8l1.7 1.7M5.5 18.5l1.7-1.7M16.8 7.2l1.7-1.7"/></svg>',
  popout: '<svg viewBox="0 0 24 24" aria-hidden="true"><rect x="3.5" y="7.5" width="12" height="12" rx="1.5"/><path d="M10 4.5h9.5V14M19.5 4.5 11 13"/></svg>',
  help: '<svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="M9.4 9.3a2.7 2.7 0 1 1 3.7 2.5c-.7.3-1.1.9-1.1 1.6v.6"/><circle cx="12" cy="17" r=".6" fill="currentColor"/></svg>',
};

const RUN_TEXT = {
  off: 'Off', starting: 'Starting', halted: 'Halted', running: 'Running', stopping: 'Stopping', error: 'Error',
};

const IPS_HELP = 'Instructions the emulated PDP-11 executed per second. Time the CPU spends idling in WAIT '
  + 'is not counted, so an idle system shows a low rate. This is not a measure of the host\'s speed.';

/**
 * Guest instruction rate for the status pill, from state.stats.mips (guest
 * instructions per second / 10^6): "12.4 M instr/s", "980 k instr/s".
 */
export function fmtInstrRate(mips) {
  const ips = mips * 1e6;
  if (!Number.isFinite(ips) || ips <= 0) return '';
  if (ips >= 1e6) return `${(ips / 1e6).toFixed(ips < 1e7 ? 2 : 1)} M instr/s`;
  if (ips >= 1e3) return `${Math.round(ips / 1e3)} k instr/s`;
  return `${Math.round(ips)} instr/s`;
}

function icon(name) {
  const span = h('span', { class: 'ico' });
  span.innerHTML = ICON[name]; // static, trusted markup
  return span.firstChild;
}

export class Toolbar {
  /**
   * @param {HTMLElement} host
   * @param {object} app  controller (main.js): state, systems, prefs, actions
   * @param {object} o
   * @param {boolean} o.full  main view (all controls) or a pop-out (status + settings)
   */
  constructor(host, app, { full }) {
    this.app = app;
    this.full = full;
    host.textContent = '';
    this.el = {};
    const E = this.el;
    host.append(h('span', { class: 'brand' }, 'PDP-11/70'));
    if (full) {
      E.system = h('button', { type: 'button', class: 'btn system-btn', title: 'Choose the operating system', 'aria-haspopup': 'dialog', onclick: () => app.openSystems() },
        h('span', { class: 'sys-name' }, '…'), h('span', { class: 'caret', 'aria-hidden': 'true' }, '▾'));
      E.power = h('button', {
        type: 'button', class: 'btn power-btn', role: 'switch', 'aria-checked': 'false',
        title: 'Power (turns the key switch OFF / POWER)', onclick: () => app.togglePower(),
      }, h('span', { class: 'dot', 'aria-hidden': 'true' }), h('span', { class: 'label' }, 'Power'));
      E.boot = h('button', { type: 'button', class: 'btn', title: 'Boot the selected system', onclick: () => app.boot() },
        h('span', {}, 'Boot'));
      E.shutdown = h('button', { type: 'button', class: 'btn', title: "Shut the operating system down cleanly, then power off", onclick: () => app.shutdownOS() },
        h('span', {}, 'Shut down'), h('span', { class: 'label-long' }, 'OS'));
      // a disabled button shows no tooltip in some browsers: the wrapper carries it too
      E.shutdownWrap = h('span', { class: 'tip-wrap' }, E.shutdown);
      E.fast = h('button', { type: 'button', role: 'radio', 'aria-checked': 'false', onclick: () => app.setSpeed('fast'), title: 'Unthrottled; idles in WAIT' }, 'Fast');
      E.authentic = h('button', { type: 'button', role: 'radio', 'aria-checked': 'false', onclick: () => app.setSpeed('authentic'), title: 'About 1 MIPS, like a real 11/70' }, 'Authentic');
      E.speed = h('div', { class: 'seg', role: 'radiogroup', 'aria-label': 'CPU speed' }, E.fast, E.authentic);
      host.append(E.system, h('span', { class: 'sep' }), E.power, E.boot, E.shutdownWrap, h('span', { class: 'sep' }), E.speed);
    }
    host.append(h('span', { class: 'spacer' }));
    // the pill changes about once a second (instruction rate): not a live
    // region; screen readers hear only the changes of E.announce
    E.status = h('span', { class: 'status-pill', title: 'Machine status' },
      h('span', { class: 'dot', 'aria-hidden': 'true' }), h('span', { class: 'status-text' }, 'Connecting…'));
    E.announce = h('span', { class: 'sr-only', role: 'status' });
    host.append(E.status, E.announce);
    this.statusSig = '';
    E.settings = h('button', { type: 'button', class: 'icon-btn', title: 'View settings', 'aria-label': 'View settings' }, icon('gear'));
    attachMenu(E.settings, () => this.settingsItems());
    host.append(E.settings);
    if (full) {
      E.popPanel = h('button', { type: 'button', class: 'icon-btn pop-panel', title: 'Open the front panel in its own window', 'aria-label': 'Pop out the front panel', onclick: () => app.popOut('panel') }, icon('popout'), h('span', { class: 'label label-long' }, ' Panel'));
      E.popConsole = h('button', { type: 'button', class: 'icon-btn pop-console', title: 'Open the console in its own window', 'aria-label': 'Pop out the console', onclick: () => app.popOut('console') }, icon('popout'), h('span', { class: 'label label-long' }, ' Console'));
      host.append(E.popPanel, E.popConsole);
    }
    E.help = h('button', { type: 'button', class: 'icon-btn', title: 'Help', 'aria-label': 'Help', 'aria-haspopup': 'dialog', onclick: () => app.openHelp() }, icon('help'));
    host.append(E.help);
  }

  settingsItems() {
    const p = this.app.prefs;
    const radio = (key, value, label) => ({
      label, role: 'menuitemradio', checked: p.get(key) === value, keep: true, onSelect: () => p.set(key, value),
    });
    const check = (key, label, hint) => ({ label, hint, checked: !!p.get(key), keep: true, onSelect: () => p.set(key, !p.get(key)) });
    return [
      { title: 'Lamps' },
      ...Object.entries(LAMP_PRESETS).map(([k, v]) => radio('lampStyle', k, v.label)),
      { sep: true },
      { title: 'Console' },
      ...Object.entries(THEMES).map(([k, v]) => radio('theme', k, v.label)),
      check('crt', 'CRT effect'),
      { label: '132 columns', checked: p.get('cols') === 132, keep: true, onSelect: () => p.set('cols', p.get('cols') === 132 ? 80 : 132) },
      check('authenticKeys', 'Authentic keys', 'raw xterm keys'),
      check('screenReader', 'Screen reader mode', 'console text for screen readers'),
      { sep: true },
      { title: 'Panel' },
      check('readouts', 'Octal readouts'),
      check('branding', 'Branding', 'logos'),
    ];
  }

  /** Re-renders from the app's current state. */
  update() {
    const { state: st, connected } = this.app;
    const E = this.el;
    const sys = this.app.selectedSystem();
    const run = st?.run ?? 'off';
    if (this.full) {
      E.system.querySelector('.sys-name').textContent = sys?.short || sys?.name || st?.system || 'System';
      E.system.disabled = !connected;
      const on = st?.power === 'on';
      E.power.setAttribute('aria-checked', String(on));
      E.power.title = on ? 'Power off (key switch to OFF)' : 'Power on (key switch to POWER)';
      E.power.disabled = !connected || !st;
      E.boot.disabled = !connected || !st || st.controls?.key === 'lock';
      // the system that is running is the one to shut down (it is the selected one
      // unless the selection changes while off)
      const rs = this.app.runningSystem();
      const rsName = rs?.short || rs?.name || 'This system';
      E.shutdown.disabled = !connected || run !== 'running' || !rs?.canShutdown;
      const tip = !rs?.canShutdown ? `${rsName} has no automatic shutdown.${rs?.shutdown ? ` ${rs.shutdown}` : ''}`
        : run !== 'running' ? 'Shuts the operating system down cleanly; works while the CPU is running'
          : `Shut ${rsName} down cleanly, then power off`;
      E.shutdown.title = tip;
      E.shutdownWrap.title = tip;
      const speed = st?.speed ?? sys?.speed;
      E.fast.setAttribute('aria-checked', String(speed === 'fast'));
      E.authentic.setAttribute('aria-checked', String(speed === 'authentic'));
      E.fast.disabled = E.authentic.disabled = !connected;
    }
    this.updateStatus();
  }

  updateStatus() {
    const { state: st, connected, retryIn, hello } = this.app;
    const pill = this.el.status;
    pill.dataset.conn = connected ? 'up' : 'down';
    pill.dataset.run = st?.run ?? 'off';
    // parts: [text, title?, class?]; announced: the run state, busy operation,
    // readiness and connection, never the instruction rate or the viewers
    let parts;
    let title = 'Machine status';
    let announce = '';
    if (!connected) {
      parts = [[retryIn ? `Disconnected, retrying in ${Math.ceil(retryIn / 1000)} s` : 'Connecting…']];
      title = 'No connection to pdp11d';
      // said once per outage: the countdown reaches 0 at every new attempt
      announce = retryIn || this.el.announce.textContent ? 'Disconnected from pdp11d' : '';
    } else if (!st) {
      parts = [['Connected']];
    } else {
      const run = RUN_TEXT[st.run] ?? st.run;
      parts = [[run]];
      if (st.console?.busy) parts.push([st.console.busy]);
      // the key is at POWER but nothing was started (PDP11_AUTOSTART=false)
      const notStarted = st.power === 'on' && st.run === 'off' && !st.console?.busy && !st.needsInstall;
      if (notStarted) parts.push(['not started', 'The key is at POWER, but the simulator has not been started: press Boot']);
      if (st.ready && st.run === 'running') parts.push(['ready']);
      announce = parts.map(([t]) => t).join(', ');
      const ips = st.run === 'running' && st.stats?.mips > 0 ? fmtInstrRate(st.stats.mips) : '';
      if (ips) parts.push([ips, IPS_HELP, 'ips']);
      if (st.viewers > 1) parts.push([`${st.viewers} viewers`]);
      title = [
        `CPU: ${run}`,
        st.error ? `Error: ${st.error}` : '',
        st.console?.lastStop ? `Last stop: ${st.console.lastStop}` : '',
        ips ? `Guest instructions: ${ips} (time idling in WAIT is not counted; not the host's speed)` : '',
        `Viewers: ${st.viewers ?? 1}`,
        hello?.server ? `pdp11d ${hello.server}` : '',
      ].filter(Boolean).join('\n');
    }
    pill.title = title;
    // rebuilt only when it changed (every state message calls this)
    const sig = JSON.stringify(parts);
    if (sig !== this.statusSig) {
      this.statusSig = sig;
      const text = pill.querySelector('.status-text');
      text.textContent = '';
      for (const [t, tip, cls] of parts) text.append(h('span', { class: cls ? `part ${cls}` : 'part', title: tip ?? null }, t));
    }
    if (announce !== this.el.announce.textContent) this.el.announce.textContent = announce;
  }
}
