// Help (UI.md §2): the selected system's card (notes markdown, logins,
// shutdown, keys, licence), a guide labelling every panel control, the
// keyboard shortcuts, the entry to the toggle-in tutorial and the simulator
// log (the "log" channel, subscribed only while its tab is shown).

import { h, isMac } from '../util.js';
import { SEND_KEYS } from '../console.js';
import { openDialog } from './dialog.js';
import { renderMarkdown } from './markdown.js';
import { buildPanelSvg } from '../panel/svg.js';
import { LogView } from './logview.js';
import { credentialText, noLogin } from './systems.js';

const GUIDE = [
  ['Key switch', [32.2, 136], 'OFF removes power. POWER: everything works. LOCK: power on, but LOAD ADRS, EXAM, DEP, CONT, ENABLE/HALT and START are disabled (and HALT is ignored).'],
  ['ADDRESS lamps', [175, 96], '22 address bits (A21 left … A0 right): the bus address while running, the console address while halted. What they show depends on the ADDRESS knob.'],
  ['DATA lamps', [218, 137], '16 data bits, selected by the DATA knob. Idle 2.11BSD, RSX and RSTS show their rotating patterns here in DATA PATHS.'],
  ['Status lamps', [247, 60], 'PAR ERR, ADRS ERR, RUN (dim while running, bright in WAIT), PAUSE, MASTER, the CPU mode USER/SUPER/KERNEL, DATA (D space) and the mapping ADDRESSING 16/18/22.'],
  ['PARITY HIGH/LOW', [75.3, 137], 'Parity of the high and low byte of memory reads.'],
  ['ADDRESS knob', [432.3, 82.9], 'Selects what the ADDRESS lamps show and which address space EXAM/DEP use: KERNEL/SUPER/USER I or D (virtual), CONS PHY or PROG PHY (physical). The lamp next to the label shows the position.'],
  ['DATA knob', [432.3, 136.1], 'DATA PATHS (normal), BUS REG, µADRS FPP/CPU (microcode address) or DISPLAY REGISTER (the word programs write to 17777570).'],
  ['Switch register', [175, 212], 'SR21…SR0: up = 1. Programs read SR15…SR0 at 17777570; LOAD ADRS uses all 22 bits. Click to toggle, drag across to set several.'],
  ['LAMP TEST', [340.07, 212], 'The white, unlabelled switch: raise it to light every lamp.'],
  ['LOAD ADRS', [354.38, 212], 'Press: the switch register becomes the console address (CPU halted).'],
  ['EXAM', [368.69, 212], 'Press: show the word at the console address on the DATA lamps (DATA PATHS). Pressing again steps to the next word.'],
  ['DEP', [383.0, 212], 'Raise: store SR15…SR0 at the console address. Raising again steps to the next word first.'],
  ['CONT', [397.31, 212], 'Press: continue a halted CPU; with HALT set, execute a single instruction.'],
  ['ENABLE/HALT', [411.62, 212], 'Down = HALT: stops the CPU (and makes START reset only). Up = ENABLE: allows the CPU to run.'],
  ['S INST / S BUS CYCLE', [425.93, 212], 'Single-step granularity for CONT while halted. (Here S BUS CYCLE also steps a whole instruction.)'],
  ['START', [440.24, 212], 'Press (CPU halted, ENABLE): reset and run from the console address; at 165000 or 173000 (boot ROM) it boots the selected system. With HALT: console reset only.'],
];

const PANEL_KEYS = [
  ['Tab', 'Move between the switch row, the knobs and the key'],
  ['← →', 'Previous / next switch (Home, End: first / last)'],
  ['↑ / ↓', 'SR bit 1 / 0; raise DEP or LAMP TEST; press LOAD ADRS, EXAM, CONT, START; ENABLE/S INST up, HALT/S BUS down'],
  ['Space / Enter', 'Toggle the focused switch or press it (hold)'],
  ['0 … 7', 'Shift an octal digit into the switch register'],
  ['Backspace', 'Shift the switch register right by one octal digit'],
  ['Esc', 'Clear the switch register'],
  ['L / E / D / C / S', 'LOAD ADRS / EXAM / DEP / CONT / START (hold)'],
  ['T', 'LAMP TEST (hold)'],
  ['H / B', 'Toggle ENABLE/HALT / S INST/S BUS CYCLE'],
  ['[ / ]', 'ADDRESS knob anticlockwise / clockwise'],
  ['{ / }', 'DATA knob anticlockwise / clockwise'],
  ['K / Shift+K', 'Key switch clockwise / anticlockwise (OFF asks first)'],
];

function consoleKeys() {
  return [
    [isMac ? '⌘C' : 'Ctrl+Shift+C', 'Copy the selection (also on the right-click menu)'],
    [isMac ? '⌘V' : 'Ctrl+Shift+V', 'Paste (sent at the console speed; Cancel in the console header). Keys typed meanwhile follow the pasted text; the interrupt key cancels the rest of the paste'],
    ['Ctrl+C', 'Always goes to the PDP-11 (interrupt), never copies'],
    ['Backspace / Delete', `Send what the system expects (DEL, BS or #), also with ${isMac ? 'Option, ⌘, Ctrl or Shift' : 'Ctrl, Alt or Shift'}, unless "Authentic keys" is on`],
    ['Shift+Tab', 'Leaves the console (Tab itself goes to the PDP-11). F6 moves the focus to the browser\'s address bar'],
  ];
}

const TABS = [['system', 'System'], ['panel', 'Panel guide'], ['keys', 'Keyboard'], ['tutorial', 'Toggle-in tutorial'], ['log', 'Simulator log']];

/** The "Log in" entry of the system card: accounts, or why none is needed. */
function loginText(sys) {
  // a note replaces "(no password)": it usually says so itself
  const one = (c) => (c.note ? `${c.user}${c.password ? ` / ${c.password}` : ''} (${c.note})` : credentialText(c));
  const accounts = (sys.credentials ?? []).map(one).join(', ');
  if (noLogin(sys)) return accounts ? `No login needed on the console. Accounts: ${accounts}` : 'No login needed: the console is ready to use';
  return accounts || 'No login needed';
}

function table(rows) {
  return h('table', { class: 'shortcut-table' }, h('tbody', {}, ...rows.map(([k, v]) => h('tr', {},
    h('td', {}, ...String(k).split(' / ').flatMap((x, i) => [i ? ' / ' : '', h('kbd', {}, x)])), h('td', {}, v)))));
}

export class Help {
  constructor(app) {
    this.app = app;
  }

  open(tab = 'system') {
    if (this.close) {
      // already open: switch to the tab
      this.show?.(TABS.some(([id]) => id === tab) ? tab : 'system');
      return;
    }
    const body = h('div', { class: 'help-body' });
    const tabBar = h('div', { class: 'tabs', role: 'tablist' });
    const show = (id) => {
      for (const b of tabBar.children) b.setAttribute('aria-selected', String(b.dataset.tab === id));
      this.cleanup?.();
      this.cleanup = null;
      body.textContent = '';
      body.dataset.tab = id;
      body.append(this[`tab_${id}`]());
    };
    this.show = show;
    for (const [id, label] of TABS) {
      tabBar.append(h('button', { type: 'button', role: 'tab', 'data-tab': id, onclick: () => show(id) }, label));
    }
    tabBar.addEventListener('keydown', (e) => {
      if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
      const btns = [...tabBar.children];
      const i = btns.indexOf(document.activeElement);
      const j = (i + (e.key === 'ArrowRight' ? 1 : -1) + btns.length) % btns.length;
      btns[j].focus();
      btns[j].click();
    });
    const { dialog, close, promise } = openDialog({ title: 'Help', body, className: 'wide tall', actions: [{ label: 'Close', value: 'close' }], dismiss: 'close' });
    dialog.querySelector('.dialog-head').after(tabBar);
    this.close = close;
    promise.then(() => {
      this.cleanup?.();
      this.cleanup = null;
      this.close = null;
      this.show = null;
    });
    show(TABS.some(([id]) => id === tab) ? tab : 'system');
  }

  tab_system() {
    const app = this.app;
    const sys = app.selectedSystem();
    if (!sys) return h('p', { class: 'muted' }, 'No system information yet (not connected).');
    const lic = sys.license ?? {};
    const out = h('div', {});
    out.append(
      h('h3', {}, sys.name),
      h('p', {}, sys.description || ''),
      h('dl', { class: 'kv' },
        h('dt', {}, 'Log in'), h('dd', {}, loginText(sys)),
        h('dt', {}, 'Keys'), h('dd', {}, sys.console?.keyHelp || `Backspace sends ${sys.console?.backspace === 'bs' ? 'BS' : sys.console?.backspace === '#' ? '#' : 'DEL'}; Ctrl+C sends ${sys.console?.interrupt === 'del' ? 'DEL' : '^C'}`),
        h('dt', {}, 'Shut down'), h('dd', {}, sys.shutdown || '—'),
        h('dt', {}, 'Licence'), h('dd', {}, `${lic.name || '?'}${lic.summary ? ` — ${lic.summary}` : ''}`),
        h('dt', {}, 'Speed'), h('dd', {}, sys.speed === 'authentic' ? 'Authentic (≈1 MIPS)' : 'Fast'),
      ),
      h('p', {}, h('button', { type: 'button', class: 'btn', onclick: () => { this.close?.('close'); app.showReadyBanner(true); } },
        noLogin(sys) ? 'Show the ready message' : 'Show the login hint')),
    );
    if (sys.notes) out.append(h('div', { class: 'notes' }, renderMarkdown(sys.notes, { headingOffset: 2 })));
    if (lic.text) {
      out.append(h('details', {}, h('summary', {}, 'Licence text'), h('div', { class: 'licence-text' }, lic.text)));
    }
    return out;
  }

  tab_panel() {
    const app = this.app;
    const out = h('div', {});
    if (app.geometry) {
      const fig = h('div', { class: 'guide-figure' });
      const pf = h('div', { class: 'panel-frame' });
      const { svg } = buildPanelSvg(app.geometry, { id: 'guide', title: 'Front panel with numbered controls' });
      pf.append(svg);
      fig.append(pf);
      const [vx, vy, vw, vh] = app.geometry.viewBox;
      GUIDE.forEach(([, [x, y]], i) => {
        fig.append(h('span', { class: 'guide-marker', style: { left: `${((x - vx) / vw) * 100}%`, top: `${((y - vy) / vh) * 100}%` }, 'aria-hidden': 'true' }, String(i + 1)));
      });
      out.append(fig);
    }
    out.append(h('ol', { class: 'guide-list' }, ...GUIDE.map(([name, , text]) => h('li', {}, h('strong', {}, name), ': ', text))));
    return out;
  }

  tab_keys() {
    return h('div', {},
      h('h3', {}, 'Front panel'),
      h('p', { class: 'muted' }, 'These work while the panel has the keyboard focus. While the console has it, clicking the panel leaves the focus there (typing goes to the PDP-11): press Shift+Tab or click outside the console first, then click a switch, or press Tab until the focus ring shows.'),
      table(PANEL_KEYS),
      h('h3', {}, 'Console'),
      h('p', { class: 'muted' }, `Keys typed while the console has the focus go to the PDP-11. Browser shortcuts such as Ctrl+W and Ctrl+T cannot be typed: the "Send key" menu in the console header sends ${SEND_KEYS.map(([label]) => label).join(' ')}. With a screen reader, turn on "Screen reader mode" in the view settings to read the console text.`),
      table(consoleKeys()),
    );
  }

  tab_log() {
    const view = new LogView(this.app);
    const el = view.open();
    this.cleanup = () => view.close();
    return el;
  }

  tab_tutorial() {
    const app = this.app;
    return h('div', {},
      h('h3', {}, 'Toggle in a program'),
      h('p', {}, 'Enter an eight-word program with the switches, the way PDP-11 programs were loaded before boot ROMs: a light that walks across the DATA lamps on the DISPLAY REGISTER.'),
      h('p', { class: 'muted' }, 'The tutorial opens next to the panel and shows every step with the current switch, address and data values. "Auto-toggle" performs the steps for you with the real panel controls.'),
      h('p', {}, h('button', { type: 'button', class: 'btn btn-primary', onclick: () => { this.close?.('close'); app.openTutorial(); } }, 'Open the tutorial')),
    );
  }
}
