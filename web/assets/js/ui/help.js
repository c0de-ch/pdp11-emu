// Help (UI.md §2): the selected system's card (notes markdown, logins,
// shutdown, keys, licence), a guide labelling every panel control, the
// keyboard shortcuts, the entry to the toggle-in tutorial and the simulator
// log (the "log" channel, subscribed only while its tab is shown).

import { h, isMac, octal } from '../util.js';
import { SEND_KEYS } from '../console.js';
import { openDialog } from './dialog.js';
import { renderMarkdown } from './markdown.js';
import { ControlsView } from '../panel/controls.js';
import { LampBank } from '../panel/lamps.js';
import { DEFAULT_CONTROLS } from '../panel/panel.js';
import { buildPanelSvg, s } from '../panel/svg.js';
import { LogView } from './logview.js';
import { credentialText, noLogin } from './systems.js';
import { PROGRAM, PROGRAM_START } from './tutorial.js';

// The paddles' markers sit on the shelf below them, the others in the free
// black space beside what they explain: a marker never covers a label or a lamp.
const SHELF = 236.6;
// name, marker position (mm), leader lines from the marker to the control (mm), text
const GUIDE = [
  ['Key switch', [9.4, 136], [[15.8, 136, 22.2, 136]], 'OFF removes power. POWER: everything works. LOCK: power on, but LOAD ADRS, EXAM, DEP, CONT, ENABLE/HALT and START are disabled (and HALT is ignored).'],
  ['ADDRESS lamps', [8.2, 110.4], [[14.2, 112.6, 332.36, 112.6], [332.36, 112.6, 110.2], [18.65, 112.6, 110.2]], '22 address bits (A21 left … A0 right): the bus address while running, the console address while halted. What they show depends on the ADDRESS knob.'],
  ['DATA lamps', [96.6, 152.9], [[103, 152.9, 332.36, 152.9], [332.36, 152.9, 150.8], [104.51, 152.9, 150.8]], '16 data bits, selected by the DATA knob. Idle 2.11BSD, RSX and RSTS show their rotating patterns here in DATA PATHS.'],
  ['Status lamps', [153.7, 50.6], [[160.1, 50.6, 332.36, 50.6], [332.36, 50.6, 53.4], [161.75, 50.6, 53.4]], 'PAR ERR, ADRS ERR, RUN (dim while running, bright in WAIT), PAUSE, MASTER, the CPU mode USER/SUPER/KERNEL, DATA (D space) and the mapping ADDRESSING 16/18/22.'],
  ['PARITY HIGH/LOW', [50.4, 136], [], 'Parity of the high and low byte of memory reads.'],
  ['ADDRESS knob', [456.2, 83.3], [[449.6, 83.3, 446.4, 83.3]], 'Selects what the ADDRESS lamps show and which address space EXAM/DEP use: KERNEL/SUPER/USER I or D (virtual), CONS PHY or PROG PHY (physical). The lamp next to the label shows the position.'],
  ['DATA knob', [456.2, 134.95], [[449.6, 134.95, 446.4, 134.95]], 'DATA PATHS (normal), BUS REG, µADRS FPP/CPU (microcode address) or DISPLAY REGISTER (the word programs write to 17777570).'],
  ['Switch register', [175.5, SHELF], [[18.75, 227.4, 332.26, 227.4], [18.75, 227.4, 225], [332.26, 227.4, 225], [175.5, 227.4, 230.2]], 'SR21…SR0: up = 1. Programs read SR15…SR0 at 17777570; LOAD ADRS uses all 22 bits. Click to toggle, drag across to set several.'],
  ['LAMP TEST', [340.07, SHELF], [[340.07, 225, 230.2]], 'The white, unlabelled switch: raise it to light every lamp.'],
  ['LOAD ADRS', [354.38, SHELF], [[354.38, 225, 230.2]], 'Press: the switch register becomes the console address (CPU halted).'],
  ['EXAM', [368.69, SHELF], [[368.69, 225, 230.2]], 'Press: show the word at the console address on the DATA lamps (DATA PATHS). Pressing again steps to the next word.'],
  ['DEP', [383.0, SHELF], [[383.0, 225, 230.2]], 'Raise: store SR15…SR0 at the console address. Raising again steps to the next word first.'],
  ['CONT', [397.31, SHELF], [[397.31, 225, 230.2]], 'Press: continue a halted CPU; with HALT set, execute a single instruction.'],
  ['ENABLE/HALT', [411.62, SHELF], [[411.62, 225, 230.2]], 'Down = HALT: stops the CPU (and makes START reset only). Up = ENABLE: allows the CPU to run.'],
  ['S INST / S BUS CYCLE', [425.93, SHELF], [[425.93, 225, 230.2]], 'Single-step granularity for CONT while halted. (Here S BUS CYCLE also steps a whole instruction.)'],
  ['START', [440.24, SHELF], [[440.24, 225, 230.2]], 'Press (CPU halted, ENABLE): reset and run from the console address; at 165000 or 173000 (boot ROM) it boots the selected system. With HALT: console reset only.'],
];

// The guide's lamps, a still picture of a running system: kernel mode with
// 22-bit mapping, the idle loop's address and the rotating band on DATA PATHS.
const LAMP_BITS = { address: 0o5360, data: 0o3760 };
const LAMPS_ON = ['PARITY_LOW', 'RUN', 'MASTER', 'KERNEL', 'ADRS22', 'PROG_PHY', 'DATA_PATHS'];

// The tutorial's program, UI.md §6: where, how many words, instruction, what it does
const LISTING = [
  [0o1000, 2, 'MOV #1,R0', 'One lit bit'],
  [0o1004, 2, 'MOV R0,@#177570', 'Show R0 on the DISPLAY REGISTER'],
  [0o1010, 1, 'NOP', 'Nothing: R1 is 0 already, and the carry stays'],
  [0o1012, 1, 'SOB R1,.', 'Wait: 65536 passes, about 65 ms at authentic speed'],
  [0o1014, 1, 'ROL R0', 'Move the bit on, through the carry'],
  [0o1016, 1, 'BR 001004', 'And again'],
];

const TUTORIAL_STEPS = [
  'Turn the key to OFF, set ENABLE/HALT to HALT and turn the key to POWER: the CPU powers up halted. Set the speed to Authentic.',
  'Set the switches to 001000 and press LOAD ADRS.',
  'For each of the eight words: set the switches to it and raise DEP.',
  'Set 001000 again, press LOAD ADRS, set ENABLE/HALT to ENABLE and press START.',
  'Turn the DATA knob to DISPLAY REGISTER and watch the light walk across the DATA lamps.',
];

const PANEL_KEYS = [
  ['Tab', 'Move between the switch row, the knobs and the key'],
  ['← →', 'Previous / next switch (Home, End: first / last)'],
  ['↑ / ↓', 'SR bit 1 / 0; raise DEP or LAMP TEST; press LOAD ADRS, EXAM, CONT, START; ENABLE/S INST up, HALT/S BUS down'],
  ['Space / Enter', 'Toggle the focused switch or press it (hold)'],
  ['0 … 7', 'Shift an octal digit into the switch register'],
  ['Backspace', 'Shift the switch register right by one octal digit'],
  ['Esc / Delete', 'Clear the switch register'],
  ['← → ↑ ↓', 'On a knob or the key: turn it'],
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

/** The key help as lines: "Backspace erases · Ctrl+U kills the line" has one fact per " · ". */
function keyHelpLines(text) {
  return text.split(' · ').map((line) => h('div', {}, line));
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
    const body = h('div', { class: 'help-body', role: 'tabpanel', id: 'help-panel', tabindex: 0 });
    const tabBar = h('div', { class: 'tabs', role: 'tablist', 'aria-label': 'Help' });
    const show = (id) => {
      for (const b of tabBar.children) {
        b.setAttribute('aria-selected', String(b.dataset.tab === id));
        b.tabIndex = b.dataset.tab === id ? 0 : -1;
      }
      body.setAttribute('aria-labelledby', `help-tab-${id}`);
      this.cleanup?.();
      this.cleanup = null;
      body.textContent = '';
      body.dataset.tab = id;
      body.append(this[`tab_${id}`]());
    };
    this.show = show;
    for (const [id, label] of TABS) {
      tabBar.append(h('button', { type: 'button', role: 'tab', id: `help-tab-${id}`, 'aria-controls': 'help-panel', 'data-tab': id, onclick: () => show(id) }, label));
    }
    tabBar.addEventListener('keydown', (e) => {
      if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
      const btns = [...tabBar.children];
      const i = btns.indexOf(document.activeElement);
      const j = (i + (e.key === 'ArrowRight' ? 1 : -1) + btns.length) % btns.length;
      btns[j].focus();
      btns[j].click();
    });
    const { dialog, close, promise } = openDialog({ title: 'Help', body, className: 'help', actions: [{ label: 'Close', value: 'close' }], dismiss: 'close' });
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
    const keys = sys.console?.keyHelp || `Backspace sends ${sys.console?.backspace === 'bs' ? 'BS' : sys.console?.backspace === '#' ? '#' : 'DEL'}; Ctrl+C sends ${sys.console?.interrupt === 'del' ? 'DEL' : '^C'}`;
    return h('div', { class: 'sys-help' },
      h('div', { class: 'sys-intro' }, h('h3', {}, sys.name), h('p', {}, sys.description || '')),
      h('aside', { class: 'sys-facts' },
        h('dl', { class: 'kv' },
          h('dt', {}, 'Log in'), h('dd', {}, loginText(sys),
            h('button', { type: 'button', class: 'link-btn', onclick: () => { this.close?.('close'); app.showReadyBanner(true); } },
              noLogin(sys) ? 'Show the ready message' : 'Show the login hint')),
          h('dt', {}, 'Keys'), h('dd', {}, ...keyHelpLines(keys)),
          h('dt', {}, 'Shut down'), h('dd', {}, sys.shutdown || '—'),
          h('dt', {}, 'Licence'), h('dd', {}, `${lic.name || '?'}${lic.summary ? `: ${lic.summary}` : ''}`),
          h('dt', {}, 'Speed'), h('dd', {}, sys.speed === 'authentic' ? 'Authentic (≈1 MIPS)' : 'Fast'),
        )),
      h('div', { class: 'sys-text' },
        sys.notes ? h('div', { class: 'notes' }, renderMarkdown(sys.notes, { headingOffset: 2 })) : null,
        lic.text ? h('details', {}, h('summary', {}, 'Licence text'), h('div', { class: 'licence-text' }, lic.text)) : null),
    );
  }

  /** The panel as the guide shows it: lit like a running system, with the numbered markers. */
  guideFigure() {
    const app = this.app;
    const geo = app.geometry;
    const [vx, vy, vw, vh] = geo.viewBox;
    const frame = h('div', { class: `panel-frame${app.prefs.get('branding') ? '' : ' no-branding'}` });
    const parts = buildPanelSvg(geo, { id: 'guide', title: 'Front panel with numbered controls' });
    const lampLayer = h('div', { class: 'lamp-layer', 'aria-hidden': 'true' });
    frame.append(parts.svg, lampLayer);
    new ControlsView(parts, geo).render(structuredClone(DEFAULT_CONTROLS));
    const lit = new Set(LAMPS_ON);
    const bank = new LampBank(lampLayer, geo);
    geo.lamps.forEach((l, i) => {
      const bit = (v, n) => (v >> n) & 1;
      const m = /^([AD])(\d+)$/.exec(l.id);
      const on = m ? bit(m[1] === 'A' ? LAMP_BITS.address : LAMP_BITS.data, Number(m[2])) : lit.has(l.id);
      if (on) bank.els[i].style.opacity = '1';
    });
    // leader lines: from the marker to the control, or a bracket along a row
    const leaders = s('svg', { class: 'guide-leaders', viewBox: `${vx} ${vy} ${vw} ${vh}`, 'aria-hidden': 'true' });
    GUIDE.forEach(([, , lines], i) => {
      for (const l of lines) {
        // [x0, y0, x1, y1], or [x, y0, y1] for a vertical line
        const [x1, y1, x2, y2] = l.length === 3 ? [l[0], l[1], l[0], l[2]] : l;
        leaders.append(s('line', { x1, y1, x2, y2, class: y1 > 224 ? 'shelf' : null }));
      }
    });
    const art = h('div', { class: 'guide-art' }, frame, leaders);
    GUIDE.forEach(([, [x, y]], i) => {
      const onShelf = y === SHELF && i > 7;
      art.append(h('span', {
        class: `guide-marker${onShelf && i % 2 ? ' low' : ''}`, 'data-n': i + 1, 'aria-hidden': 'true',
        style: { left: `${((x - vx) / vw) * 100}%`, top: `${((y - vy) / vh) * 100}%` },
      }, String(i + 1)));
    });
    return h('figure', { class: 'guide-figure' }, art,
      h('figcaption', { class: 'muted' }, 'The lamps as on a running system. Point at a number or an entry to see which belongs to which.'));
  }

  tab_panel() {
    const out = h('div', { class: 'guide' });
    if (this.app.geometry) out.append(this.guideFigure());
    const list = h('ol', { class: 'guide-list' }, ...GUIDE.map(([name, , , text], i) => h('li', { 'data-n': i + 1 }, h('strong', {}, name), ': ', text)));
    out.append(list);
    // pointing at an entry or a marker shows its partner
    const mark = (n, on) => {
      for (const e of out.querySelectorAll(`[data-n="${n}"]`)) e.classList.toggle('active', on);
    };
    for (const type of ['pointerover', 'pointerout']) {
      out.addEventListener(type, (e) => {
        const n = e.target.closest?.('.guide-marker, .guide-list li')?.dataset.n;
        if (n) mark(n, type === 'pointerover');
      });
    }
    return out;
  }

  tab_keys() {
    return h('div', { class: 'help-cols' },
      h('div', {},
        h('h3', {}, 'Front panel'),
        h('p', { class: 'muted' }, 'These work while the panel has the keyboard focus: press Tab or Shift+Tab until the focus ring shows on a switch. A mouse click on the panel leaves the keyboard in the console (typing goes to the PDP-11).'),
        table(PANEL_KEYS)),
      h('div', {},
        h('h3', {}, 'Console'),
        h('p', { class: 'muted' }, `Keys typed while the console has the focus go to the PDP-11. Browser shortcuts such as Ctrl+W and Ctrl+T cannot be typed: the "Send key" menu in the console header sends ${SEND_KEYS.map(([label]) => label).join(' ')}. With a screen reader, turn on "Screen reader mode" in the view settings to read the console text.`),
        table(consoleKeys())),
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
    let word = 0;
    const rows = LISTING.map(([addr, n, instruction, what]) => {
      const words = PROGRAM.slice(word, word + n);
      word += n;
      return h('tr', {}, h('td', {}, octal(addr, 6)), h('td', {}, words.map((w) => octal(w, 6)).join(' ')), h('td', {}, instruction), h('td', {}, what));
    });
    return h('div', { class: 'help-cols' },
      h('div', {},
        h('h3', {}, 'Toggle in a program'),
        h('p', {}, 'Enter an eight-word program with the switches, the way PDP-11 programs were loaded before boot ROMs: a light that walks across the DATA lamps on the DISPLAY REGISTER.'),
        h('p', {}, h('button', { type: 'button', class: 'btn btn-primary', onclick: () => { this.close?.('close'); app.openTutorial(); } }, 'Open the tutorial')),
        h('p', { class: 'muted' }, 'The tutorial opens next to the panel and shows every step with the current switch, address and data values. "Auto-toggle" performs the steps for you with the real panel controls.'),
        h('h4', {}, 'What you will do'),
        h('ol', { class: 'tutorial-steps' }, ...TUTORIAL_STEPS.map((t) => h('li', {}, t)))),
      h('div', {},
        h('h3', {}, `The program, at ${octal(PROGRAM_START, 6)}`),
        h('table', { class: 'listing' },
          h('thead', {}, h('tr', {}, h('th', {}, 'Address'), h('th', {}, 'Words'), h('th', {}, 'Instruction'), h('th', {}, 'What it does'))),
          h('tbody', {}, ...rows)),
        h('p', { class: 'muted' }, 'The bit rotates through the carry, so the display cycles through 17 states for good: 1, 2, 4 … 100000, 0. For the first 2-3 s after START the CPU runs flat out and every DATA lamp glows dimly; then the single light walks.')),
    );
  }
}
