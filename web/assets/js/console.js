// The DL11 console: xterm.js 6 (DOM renderer) with Glass TTY VT220 at
// integer multiples of 20 px, phosphor themes, the system's key mapping,
// copy/paste that works over plain HTTP, paced pastes, bell flash, history
// replay on (re)subscribe, output flow control and the terminal-query
// answerer rule (docs/PROTOCOL.md §1.4, docs/UI.md §5).

import { Terminal } from '../vendor/xterm/xterm.mjs';
import { THEMES } from './prefs.js';
import { copyText, h, isMac } from './util.js';

const ROWS = 24;
const GLASS = '"Glass TTY VT220"';
const SMOOTH = '"VT323", "IBM Plex Mono", ui-monospace, monospace';
const SCROLLBAR = 14; // xterm.js 6 overlays its scrollbar; console.css gives it a gutter
const CHUNK = 4096; // max bytes per console message for pastes
const PASTE_WINDOW = 8192; // keep at most this much queued at the server
// state.console.inputQueued may not count input sent this recently (the
// server re-reads its queue 4 times a second and coalesces state messages)
const STATE_LAG = 500;
// Console output handed to xterm.js and not parsed yet. xterm.js cannot drop
// what it has queued, so a reset (reconnect) waits for this much at most and
// everything behind it is dropped instead of being replayed ahead of the history.
const WRITE_AHEAD = 65536;

export const SEND_KEYS = [
  ['^C', '\x03'], ['^O', '\x0f'], ['^Q', '\x11'], ['^S', '\x13'], ['^T', '\x14'], ['^U', '\x15'], ['^W', '\x17'], ['^Z', '\x1a'],
  ['DEL', '\x7f'], ['ESC', '\x1b'],
];

const reducedMotion = matchMedia('(prefers-reduced-motion: reduce)');

const TYPOGRAPHIC = {
  '‘': "'", '’': "'", '“': '"', '”': '"', '–': '-', '—': '-', '−': '-',
  ' ': ' ', ' ': ' ', '…': '...',
};

/**
 * Text to 7-bit bytes for the guest: typographic quotes, dashes and spaces
 * folded to ASCII, anything else non-ASCII as '?'. With `paste`, line ends
 * (CR LF, LF) become CR, the Return key.
 */
export function encodeForGuest(text, { paste = true } = {}) {
  const t = paste ? text.replace(/\r\n?|\n/g, '\r') : text;
  const out = new Uint8Array(t.length * 3);
  let n = 0;
  for (const ch of t) {
    const c = ch.codePointAt(0);
    if (c < 0x80) out[n++] = c;
    else for (const r of TYPOGRAPHIC[ch] ?? '?') out[n++] = r.charCodeAt(0);
  }
  return out.slice(0, n);
}

/** Data xterm.js generated that is a terminal reply, not a key. */
const REPLY = /^\x1b(\[[?>=]?[\d;]*[$]?[a-zA-Z]|\][\s\S]*(\x07|\x1b\\)|P[\s\S]*\x1b\\)$/;
const KEYLIKE = /^\x1b(\[[\d;]*[ABCDHF~]|\[1;\d+[PQRS]|O[A-Z])$/; // cursor and function keys (modifiers included)

function theme(name) {
  const t = THEMES[name] ?? THEMES.green;
  const fg = t.fg;
  // a monochrome VT220: every ANSI colour is the phosphor, bright = full intensity
  const dim = mix(fg, t.bg, 0.72);
  return {
    background: t.bg, foreground: fg, cursor: fg, cursorAccent: t.bg,
    selectionBackground: `${fg}4d`, selectionInactiveBackground: `${fg}33`,
    black: t.bg, red: dim, green: dim, yellow: dim, blue: dim, magenta: dim, cyan: dim, white: dim,
    brightBlack: mix(fg, t.bg, 0.45), brightRed: fg, brightGreen: fg, brightYellow: fg, brightBlue: fg,
    brightMagenta: fg, brightCyan: fg, brightWhite: fg,
  };
}

function mix(a, b, wa) {
  const p = (x) => [1, 3, 5].map((i) => parseInt(x.slice(i, i + 2), 16));
  const [r1, g1, b1] = p(a);
  const [r2, g2, b2] = p(b);
  const c = (x, y) => Math.round(x * wa + y * (1 - wa)).toString(16).padStart(2, '0');
  return `#${c(r1, r2)}${c(g1, g2)}${c(b1, b2)}`;
}

export class ConsoleView {
  /**
   * @param {HTMLElement} host
   * @param {object} o
   * @param {(bytes: Uint8Array) => boolean} o.sendInput  console input to the server
   * @param {(msg: object) => void} o.send                JSON messages (inputCancel)
   * @param {import('./prefs.js').Prefs} o.prefs
   * @param {(text: string, level?: string) => void} [o.toast]
   */
  constructor(host, { sendInput, send, prefs, toast }) {
    this.host = host;
    this.sendInputRaw = sendInput;
    this.send = send;
    this.prefs = prefs;
    this.toast = toast ?? (() => {});
    this.keys = { backspace: 'del', interrupt: 'ctrl-c', keyHelp: '' };
    this.answererId = undefined;
    this.clientId = undefined;
    this.lastInputAt = 0;
    this.gate = false; // closed while the history is parsed: no terminal replies (PROTOCOL §1.4)
    this.historyGen = 0; // counts non-empty histories; only the newest one opens the gate
    this.gatedKeys = []; // typed keys (strings) and pastes ({paste}) held while the gate was closed
    this.historyExpected = false; // the next console message is the history
    this.out = []; // console output not handed to xterm.js yet: {bytes, done?} or {reset: true}
    this.inFlight = 0; // bytes handed to xterm.js whose write callback has not run
    this.paste = null; // { bytes, offset } of a paced paste: what is not sent yet
    this.pasteQueued = 0; // estimate of this client's paste bytes still in the server's queue
    this.pasteSends = []; // [time, bytes] of the paste chunks sent within STATE_LAG
    this.serverQueued = 0; // state.console.inputQueued: every viewer's input
    this.fontMode = { family: GLASS, size: 20, lineHeight: 20 / 19 };
    this.box = null;
  }

  async init() {
    await Promise.allSettled([
      document.fonts.load(`20px ${GLASS}`),
      document.fonts.load('20px "VT323"'),
    ]);
    const frame = h('div', { class: 'console-frame' });
    const screen = h('div', { class: 'console-screen' });
    this.hint = h('div', { class: 'console-hint', 'aria-hidden': 'true' }, 'Click to type');
    frame.append(screen, h('div', { class: 'crt-overlay', 'aria-hidden': 'true' }), this.hint);
    this.host.append(frame);
    this.frame = frame;
    this.screen = screen;
    const term = new Terminal({
      cols: this.prefs.get('cols'), rows: ROWS,
      fontFamily: GLASS, fontSize: 20, lineHeight: 20 / 19, letterSpacing: 0,
      cursorBlink: !reducedMotion.matches, cursorStyle: 'block', cursorInactiveStyle: 'outline',
      scrollback: 2000, theme: theme(this.prefs.get('theme')),
      customGlyphs: true, drawBoldTextInBrightColors: true, minimumContrastRatio: 1,
      allowProposedApi: false, rightClickSelectsWord: false, macOptionIsMeta: false,
      screenReaderMode: !!this.prefs.get('screenReader'),
      // PDP-11 systems pad with DEL (0x7f) after line ends; xterm.js ignores it
      // but logs a "Parsing error" for every one
      logLevel: 'warn',
      logger: {
        trace() {}, debug() {}, info() {},
        warn: (msg, ...args) => console.warn(msg, ...args),
        error: (msg, ...args) => {
          if (!/Parsing error/.test(String(msg))) console.error(msg, ...args);
        },
      },
    });
    this.term = term;
    term.open(screen);
    this.applyTheme();
    this.applyCRT();

    // ---- terminal query replies: only the answerer, never during history (PROTOCOL §1.4)
    const suppress = () => this.gate || !this.isAnswerer();
    for (const id of [{ final: 'c' }, { prefix: '>', final: 'c' }, { prefix: '=', final: 'c' },
      { final: 'n' }, { prefix: '?', final: 'n' }, { intermediates: '$', final: 'p' },
      { prefix: '?', intermediates: '$', final: 'p' }, { prefix: '>', final: 'q' }]) {
      term.parser.registerCsiHandler(id, () => suppress());
    }
    // DECRQSS and the colour queries (OSC 4/10/11/12 with "?"); a colour that is
    // set, not queried, still goes to xterm.js
    term.parser.registerDcsHandler({ intermediates: '$', final: 'q' }, () => suppress());
    for (const osc of [4, 10, 11, 12]) term.parser.registerOscHandler(osc, (data) => data.includes('?') && suppress());
    term.onData((d) => {
      if (REPLY.test(d) && !KEYLIKE.test(d)) {
        // a reply xterm.js generated: dropped during the history and when another
        // viewer answers (whatever query handler xterm.js used); not typing
        if (!this.gate && this.isAnswerer()) this.input(encodeForGuest(d, { paste: false }));
        return;
      }
      this.typed(d);
    });
    term.onBell(() => this.bell());
    term.attachCustomKeyEventHandler((ev) => this.onKey(ev));
    const ta = term.textarea;
    ta?.addEventListener('focus', () => frame.classList.add('focused'));
    ta?.addEventListener('blur', () => frame.classList.remove('focused'));
    // pastes go through our paced path, not xterm's (capture phase, before the textarea)
    term.element.addEventListener('paste', (ev) => {
      ev.preventDefault();
      ev.stopImmediatePropagation();
      this.pasteText(ev.clipboardData?.getData('text/plain') ?? '');
    }, true);
    frame.addEventListener('contextmenu', (ev) => this.onContextMenu(ev));
    this.prefs.on('change', (k) => {
      if (k === 'theme') this.applyTheme();
      else if (k === 'crt') this.applyCRT();
      else if (k === 'cols') this.setCols(this.prefs.get('cols'));
      else if (k === 'screenReader') term.options.screenReaderMode = !!this.prefs.get('screenReader');
    });
    reducedMotion.addEventListener('change', () => { term.options.cursorBlink = !reducedMotion.matches; });
    term.onCursorMove(() => this.onCursorMove?.());
    return this;
  }

  // ---------------------------------------------------------------- output

  /**
   * The server is about to (re)send the history: reset first (PROTOCOL §1).
   * Output that has not reached xterm.js yet is stale now and dropped; the
   * reset waits until xterm.js has parsed what it already has, so nothing of
   * the old output appears after it.
   */
  beginHistory() {
    // a history that a quicker reconnect overtook ends here, so the gate it
    // closed does not wait for the safety timeout
    for (const o of this.out) o.done?.();
    this.out = [{ reset: true }];
    this.historyExpected = true;
    this.drain();
  }

  /**
   * Console output. The first message after (re)subscribing is the history:
   * the server always sends exactly one, possibly empty, and never merges
   * live output into it, so it marks the end of the history. Terminal replies
   * are suppressed while it is parsed; live output written after it is parsed
   * with the gate open (xterm.js parses writes in order and runs a write's
   * callback before the next one).
   */
  write(bytes) {
    if (!this.term) return;
    if (!this.historyExpected) {
      this.out.push({ bytes });
      this.drain();
      return;
    }
    this.historyExpected = false;
    if (!bytes.length) return; // an empty history: nothing to parse, nothing to gate
    const gen = ++this.historyGen;
    this.gate = true;
    let done = false;
    const opened = () => {
      if (done) return;
      done = true;
      clearTimeout(safety);
      // a newer history (quick reconnect) keeps the gate closed until it is parsed
      if (gen === this.historyGen) this.openGate();
    };
    // never keep the console closed for good
    const safety = setTimeout(opened, 15000);
    this.out.push({ bytes, done: opened });
    this.drain();
  }

  /**
   * Flow control: hands queued output to xterm.js while less than WRITE_AHEAD
   * bytes of it are unparsed (a message is never split, so the history stays
   * one write with its own callback).
   */
  drain() {
    while (this.term && this.out.length) {
      const next = this.out[0];
      if (next.reset) {
        if (this.inFlight) return;
        this.out.shift();
        this.term.reset();
        continue;
      }
      if (this.inFlight && this.inFlight + next.bytes.length > WRITE_AHEAD) return;
      this.out.shift();
      const n = next.bytes.length;
      this.inFlight += n;
      this.term.write(next.bytes, () => {
        this.inFlight -= n;
        next.done?.();
        this.drain();
      });
    }
  }

  /** The history is parsed: the held keys and pastes follow, in the order they were typed. */
  openGate() {
    this.gate = false;
    const held = this.gatedKeys;
    this.gatedKeys = [];
    for (const k of held) {
      if (typeof k === 'string') this.typed(k);
      else this.pasteText(k.paste);
    }
  }

  bell() {
    this.frame.classList.remove('bell');
    void this.frame.offsetWidth; // restart the animation
    this.frame.classList.add('bell');
    clearTimeout(this.bellTimer);
    this.bellTimer = setTimeout(() => this.frame.classList.remove('bell'), 200);
  }

  // ---------------------------------------------------------------- input

  setClient(clientId) {
    this.clientId = clientId;
  }

  setAnswerer(id) {
    this.answererId = id;
  }

  isAnswerer() {
    // answerer 0 = none: as far as the server's (lagging) state knows, nobody
    // shows the console, yet this client does; with no state yet: answer too
    if (!this.answererId || this.clientId === undefined) return true;
    if (this.answererId === this.clientId) return true;
    // we just typed: the server makes us the answerer; its state may lag behind
    return performance.now() - this.lastInputAt < 1500;
  }

  /** Sends console input; only typed input (not a terminal reply) makes this client the answerer. */
  input(bytes, { typed = false } = {}) {
    if (!bytes.length) return;
    if (typed) this.lastInputAt = performance.now();
    if (!this.sendInputRaw(bytes)) this.toast('Not connected: input dropped', 'warn');
  }

  /**
   * Everything the user types (keys, the mapped Backspace and Ctrl+C, Send
   * key) goes through here (UI.md §5): held while the history is replayed,
   * appended to a paste that is still being sent, so it stays in order, else
   * sent at once. The system's interrupt key during a paste first cancels
   * the rest of the paste.
   */
  typed(str) {
    this.onTyped?.();
    if (this.gate) {
      this.gatedKeys.push(str);
      return;
    }
    if (str === this.interruptKey() && this.pasteRemaining() > 0) this.cancelPaste();
    const bytes = encodeForGuest(str, { paste: false });
    if (this.paste) {
      this.appendPaste(bytes);
      this.pump();
      return;
    }
    this.input(bytes, { typed: true });
  }

  sendKey(seq) {
    this.typed(seq);
    this.term?.focus();
  }

  setKeys(k) {
    this.keys = { backspace: k?.backspace || 'del', interrupt: k?.interrupt || 'ctrl-c', keyHelp: k?.keyHelp || '' };
  }

  /** What Backspace sends on this system: DEL, BS or '#'. */
  eraseKey() {
    return this.keys.backspace === 'bs' ? '\x08' : this.keys.backspace === '#' ? '#' : '\x7f';
  }

  /** What interrupts a program on this system: ^C or DEL. */
  interruptKey() {
    return this.keys.interrupt === 'del' ? '\x7f' : '\x03';
  }

  /** Key mapping (UI.md §5); returns false for keys handled here. */
  onKey(ev) {
    const down = ev.type === 'keydown';
    const k = ev.key;
    const copyCombo = isMac ? ev.metaKey && !ev.ctrlKey && (k === 'c' || k === 'C') : ev.ctrlKey && ev.shiftKey && (k === 'C' || k === 'c');
    if (copyCombo) {
      if (down) {
        ev.preventDefault();
        this.copy();
      }
      return false;
    }
    const pasteCombo = isMac ? ev.metaKey && (k === 'v' || k === 'V') : (ev.ctrlKey && ev.shiftKey && (k === 'V' || k === 'v')) || (ev.shiftKey && k === 'Insert');
    if (pasteCombo) return false; // the browser's paste event follows
    // Shift+Tab leaves the console (the browser moves the focus back); Tab
    // itself goes to the PDP-11, whose terminals had no back-tab
    if (k === 'Tab' && ev.shiftKey && !ev.ctrlKey && !ev.altKey && !ev.metaKey) return false;
    if (this.prefs.get('authenticKeys')) return true;
    // Backspace with any modifier, and Delete, erase like the system expects:
    // xterm.js would send DEL (the Unix V5-V7 interrupt), ESC DEL, BS or ESC [3~
    if (k === 'Backspace' || k === 'Delete') {
      if (down) {
        ev.preventDefault();
        this.typed(this.eraseKey());
      }
      return false;
    }
    if (ev.ctrlKey && !ev.shiftKey && !ev.altKey && !ev.metaKey && (k === 'c' || k === 'C') && this.keys.interrupt === 'del') {
      if (down) {
        ev.preventDefault();
        this.typed('\x7f');
      }
      return false;
    }
    return true;
  }

  // ---------------------------------------------------------------- copy / paste

  /** Copies the selection via the DOM copy event (works without a secure context). */
  copy() {
    const text = this.term?.getSelection() ?? '';
    if (!text) return false;
    const ok = copyText(text);
    if (!ok) this.toast('Copy failed: the browser refused clipboard access', 'warn');
    return ok;
  }

  pasteText(text) {
    if (!text) return;
    if (this.gate) {
      // like a typed key, a paste waits for the history (sent in order when the gate opens)
      this.gatedKeys.push({ paste: text });
      return;
    }
    const bytes = encodeForGuest(text);
    if (!bytes.length) return;
    this.onTyped?.();
    this.appendPaste(bytes);
    this.lastInputAt = performance.now();
    this.pump();
    this.term?.focus();
  }

  /** Appends bytes to the paste in progress (or starts one). */
  appendPaste(bytes) {
    const p = this.paste;
    if (!p) {
      this.paste = { bytes, offset: 0 };
      return;
    }
    const merged = new Uint8Array(p.bytes.length - p.offset + bytes.length);
    merged.set(p.bytes.subarray(p.offset));
    merged.set(bytes, merged.length - bytes.length);
    this.paste = { bytes: merged, offset: 0 };
  }

  /** Paste bytes sent within STATE_LAG: the server's queue may not count them yet. */
  recentPaste() {
    const now = performance.now();
    this.pasteSends = this.pasteSends.filter(([t]) => now - t < STATE_LAG);
    return this.pasteSends.reduce((sum, [, n]) => sum + n, 0);
  }

  /** Sends the next chunks while the server's queue has room. */
  pump() {
    const p = this.paste;
    if (!p) return;
    let budget = PASTE_WINDOW - this.serverQueued - this.recentPaste();
    while (p.offset < p.bytes.length && budget > 0) {
      const n = Math.min(CHUNK, p.bytes.length - p.offset, Math.max(budget, 1));
      const part = p.bytes.subarray(p.offset, p.offset + n);
      if (!this.sendInputRaw(part)) {
        this.toast('Not connected: paste stopped', 'warn');
        this.paste = null;
        break;
      }
      this.lastInputAt = performance.now();
      this.pasteSends.push([this.lastInputAt, n]);
      p.offset += n;
      budget -= n;
      this.pasteQueued += n;
    }
    if (this.paste && this.paste.offset >= this.paste.bytes.length) this.paste = null;
    this.onPasteProgress?.();
  }

  /**
   * state.console.inputQueued arrived: every viewer's queued input. This
   * client's part of it is at most what it sent, and at most the whole queue
   * plus what the queue may not count yet.
   */
  setServerQueued(n) {
    this.serverQueued = Math.max(0, n | 0);
    this.pasteQueued = Math.min(this.pasteQueued, this.serverQueued + this.recentPaste());
    this.pump();
    this.onPasteProgress?.();
  }

  /** Bytes of this client's paste not yet typed into the guest (local and estimated at the server). */
  pasteRemaining() {
    const local = this.paste ? this.paste.bytes.length - this.paste.offset : 0;
    return local + this.pasteQueued;
  }

  /** Queued input that is not this client's paste (other viewers', or keys held while the CPU is halted). */
  otherQueued() {
    return Math.max(0, this.serverQueued - this.pasteQueued);
  }

  /** Drops the rest of the paste. inputCancel clears the server's whole input queue. */
  cancelPaste() {
    this.paste = null;
    this.send({ type: 'inputCancel' });
    this.serverQueued = 0;
    this.pasteQueued = 0;
    this.pasteSends = [];
    this.onPasteProgress?.();
  }

  async pasteFromMenu() {
    if (navigator.clipboard?.readText && window.isSecureContext) {
      try {
        this.pasteText(await navigator.clipboard.readText());
        return;
      } catch { /* denied: fall through to the hint */ }
    }
    this.toast(`Press ${isMac ? '⌘V' : 'Ctrl+Shift+V'} to paste into the console`, 'info');
    this.term?.focus();
  }

  onContextMenu(ev) {
    ev.preventDefault();
    this.menu?.remove();
    const sel = this.term?.getSelection() ?? '';
    const close = () => {
      this.menu?.remove();
      this.menu = null;
      document.removeEventListener('pointerdown', outside, true);
      document.removeEventListener('keydown', onEsc, true);
    };
    const outside = (e) => { if (!this.menu?.contains(e.target)) close(); };
    const onEsc = (e) => { if (e.key === 'Escape') close(); };
    const item = (label, fn, disabled = false) => h('button', {
      type: 'button', role: 'menuitem', class: 'menu-item', disabled,
      onclick: () => { close(); fn(); },
    }, label);
    const menu = h('div', { class: 'menu context-menu', role: 'menu' },
      item(`Copy${isMac ? '  ⌘C' : '  Ctrl+Shift+C'}`, () => this.copy(), !sel),
      item(`Paste${isMac ? '  ⌘V' : '  Ctrl+Shift+V'}`, () => this.pasteFromMenu()),
      item('Select all', () => this.term.selectAll()),
      h('div', { class: 'menu-sep', role: 'separator' }),
      ...SEND_KEYS.map(([label, seq]) => item(`Send ${label}`, () => this.sendKey(seq))),
      h('div', { class: 'menu-sep', role: 'separator' }),
      item('Clear scrollback', () => this.term.clear()),
    );
    document.body.append(menu);
    const r = menu.getBoundingClientRect();
    menu.style.left = `${Math.min(ev.clientX, innerWidth - r.width - 4)}px`;
    menu.style.top = `${Math.min(ev.clientY, innerHeight - r.height - 4)}px`;
    this.menu = menu;
    setTimeout(() => {
      document.addEventListener('pointerdown', outside, true);
      document.addEventListener('keydown', onEsc, true);
    });
    menu.querySelector('button:not([disabled])')?.focus();
  }

  // ---------------------------------------------------------------- presentation

  applyTheme() {
    const name = this.prefs.get('theme');
    const t = THEMES[name] ?? THEMES.green;
    if (this.term) this.term.options.theme = theme(name);
    this.host.style.setProperty('--phosphor', t.fg);
    this.host.style.setProperty('--phosphor-bg', t.bg);
  }

  applyCRT() {
    this.frame?.classList.toggle('crt', !!this.prefs.get('crt'));
  }

  setCols(cols) {
    if (!this.term) return;
    this.term.resize(cols, ROWS);
    if (this.box) this.fit(this.box.w, this.box.h, this.box.opts);
  }

  /**
   * Terminal size in CSS px for a font, computed the way xterm's DOM renderer
   * does: canvas TextMetrics for the char box, device-pixel rounding, cell
   * height = floor(char height * lineHeight).
   */
  sizeFor(mode) {
    const cols = this.term?.cols ?? this.prefs.get('cols');
    const dpr = window.devicePixelRatio || 1;
    const key = `${mode.family}|${mode.size}|${dpr}`;
    let m = this.metrics?.get(key);
    if (!m) {
      this.metrics ??= new Map();
      this.ctx ??= document.createElement('canvas').getContext('2d');
      this.ctx.font = `${mode.size}px ${mode.family}`;
      const t = this.ctx.measureText('W');
      const asc = t.fontBoundingBoxAscent ?? mode.size * 0.8;
      const desc = t.fontBoundingBoxDescent ?? mode.size * 0.2;
      m = { w: t.width, h: asc + desc };
      this.metrics.set(key, m);
    }
    const charH = Math.ceil(m.h * dpr);
    const cellH = Math.floor(charH * mode.lineHeight);
    return { w: Math.round((m.w * dpr * cols) / dpr) + SCROLLBAR, h: Math.round((cellH * ROWS) / dpr), rowH: cellH / dpr };
  }

  /**
   * Picks the font for a box of w x h px: Glass TTY at the largest integer
   * multiple of 20 px that fits, else VT323 scaled to fit (UI.md §5).
   */
  chooseFont(w, h, { minSize = 0 } = {}) {
    const cols = this.term?.cols ?? this.prefs.get('cols');
    const k = Math.floor(Math.min((w - SCROLLBAR) / (cols * 10), h / (ROWS * 20)) + 1e-6);
    if (k >= 1) return { family: GLASS, size: 20 * Math.min(k, 6), lineHeight: 20 / 19 };
    let size = Math.max(6, Math.floor(Math.min((w - SCROLLBAR) / (cols * 0.4), h / ROWS)));
    for (let i = 0; i < 40 && size > 6; i++, size--) {
      const sz = this.sizeFor({ family: SMOOTH, size, lineHeight: 1 });
      if (sz.w <= w && sz.h <= h) break;
    }
    return { family: SMOOTH, size: Math.max(size, minSize), lineHeight: 1 };
  }

  /** What fit() would produce for a box (layout candidates); {glass: k} forces Glass TTY k x. */
  predict(box) {
    const mode = box.glass ? { family: GLASS, size: 20 * box.glass, lineHeight: 20 / 19 } : this.chooseFont(box.w, box.h);
    return { ...this.sizeFor(mode), glass: mode.family === GLASS, mode };
  }

  fit(w, h, opts = {}) {
    if (!this.term) return { w: 0, h: 0 };
    this.box = { w, h, opts };
    const mode = this.chooseFont(w, h, opts);
    const m = this.fontMode;
    if (mode.family !== m.family || mode.size !== m.size) {
      this.fontMode = mode;
      this.term.options.fontFamily = mode.family;
      this.term.options.lineHeight = mode.lineHeight;
      this.term.options.fontSize = mode.size;
    }
    this.frame.dataset.font = mode.family === GLASS ? `glass-${mode.size / 20}x` : `smooth-${mode.size}`;
    return this.sizeFor(mode);
  }

  focus() {
    this.term?.focus();
  }

  /** The cursor's row on the visible screen and the number of rows (the ready banner keeps clear of it). */
  cursorRow() {
    const b = this.term?.buffer.active;
    if (!b) return null;
    return { row: b.baseY + b.cursorY - b.viewportY, rows: this.term.rows };
  }

  /** Visible screen as text (tests, pdp11Debug). */
  text() {
    const b = this.term?.buffer.active;
    if (!b) return '';
    const lines = [];
    for (let i = 0; i < b.length; i++) lines.push(b.getLine(i)?.translateToString(true) ?? '');
    while (lines.length && lines[lines.length - 1] === '') lines.pop();
    return lines.join('\n');
  }
}
