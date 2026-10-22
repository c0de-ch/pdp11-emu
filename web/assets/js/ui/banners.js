// Banners and overlays: "no password" warning, the ready banner over the
// console and the first-run install/boot overlay (UI.md §2).

import { h } from '../util.js';
import { noLogin, progressBar } from './systems.js';

const INSECURE_KEY = 'pdp11-insecure-dismissed';
const READY_MS = 15000;
const GAP = 8; // px between the ready banner and the console frame or text edge

export class InsecureBanner {
  constructor(host) {
    this.host = host;
    this.el = null;
  }

  show() {
    let dismissed = false;
    try {
      dismissed = sessionStorage.getItem(INSECURE_KEY) === '1';
    } catch { /* storage blocked */ }
    if (this.el || dismissed) return;
    this.el = h('div', { class: 'banner banner-warn', role: 'alert' },
      h('span', { 'aria-hidden': 'true' }, '⚠'),
      h('span', { class: 'grow' }, 'No password set: anyone on your network can control this machine. Set PDP11_PASSWORD to require a login.'),
      h('button', {
        type: 'button', class: 'btn', onclick: () => {
          try {
            sessionStorage.setItem(INSECURE_KEY, '1');
          } catch { /* storage blocked */ }
          this.hide();
        },
      }, 'Dismiss'));
    this.host.append(this.el);
    this.onChange?.();
  }

  hide() {
    this.el?.remove();
    this.el = null;
    this.onChange?.();
  }
}

/**
 * "2.11BSD is up — log in as root (no password)"; systems whose console needs
 * no login (noLogin: RT-11, RSX) get "RT-11 is up — the console is ready (no
 * login needed)", never a login instruction. The banner adds the key help.
 */
export function readyMessage(sys) {
  const name = sys?.short || sys?.name || 'The system';
  const parts = [h('strong', {}, `${name} is up`)];
  const c = sys?.credentials ?? [];
  if (noLogin(sys) || !c.length) {
    parts.push(' — the console is ready (no login needed)');
    return parts;
  }
  parts.push(' — log in as ');
  c.slice(0, 2).forEach((cr, i) => {
    if (i) parts.push(' or ');
    parts.push(h('strong', {}, cr.user), cr.password ? ` (password ${cr.password})` : ' (no password)');
  });
  return parts;
}

/**
 * The ready banner keeps clear of the console text where it can (UI.md §2):
 * it sits below the console when the page has room there, else over the half
 * of the terminal away from the cursor (the prompt), and it gives way to the
 * first key typed.
 */
export class ReadyBanner {
  constructor(host) {
    this.host = host; // #console-area
    this.console = null; // ConsoleView (set by main.js): the terminal and its cursor
    this.el = null;
    this.timer = 0;
    this.raf = 0;
  }

  show(sys, { sticky = false } = {}) {
    this.hide();
    const help = sys?.console?.keyHelp;
    this.el = h('div', { class: 'ready-banner', role: 'status' },
      h('div', {}, h('div', {}, ...readyMessage(sys)), help ? h('div', { class: 'help' }, help) : null),
      h('button', { type: 'button', class: 'icon-btn', 'aria-label': 'Dismiss', onclick: () => this.hide() }, '×'));
    this.host.append(this.el);
    this.place();
    if (!sticky) this.timer = setTimeout(() => this.hide(), READY_MS);
  }

  /** Positions the banner (on show, after a layout pass and when the cursor moves). */
  place() {
    const el = this.el;
    const con = this.console;
    if (!el || !con?.frame) return;
    const area = this.host.getBoundingClientRect();
    const frame = con.frame.getBoundingClientRect();
    const screen = con.screen.getBoundingClientRect();
    const stage = this.host.parentElement;
    const bottom = Math.min(stage.getBoundingClientRect().bottom - parseFloat(getComputedStyle(stage).paddingBottom), innerHeight);
    const bh = el.offsetHeight;
    let top;
    if (frame.bottom + GAP + bh <= bottom) {
      top = frame.bottom + GAP; // below the console: covers nothing
    } else {
      const c = con.cursorRow();
      const upper = !c || c.row >= c.rows / 2; // the prompt is in the lower half
      top = upper ? screen.top + GAP : screen.bottom - GAP - bh;
    }
    el.style.top = `${Math.round(top - area.top)}px`;
  }

  /** place() once in the next frame, however often the cursor moves until then. */
  placeSoon() {
    if (!this.el || this.raf) return;
    this.raf = requestAnimationFrame(() => {
      this.raf = 0;
      this.place();
    });
  }

  hide() {
    clearTimeout(this.timer);
    cancelAnimationFrame(this.raf);
    this.raf = 0;
    this.el?.remove();
    this.el = null;
  }
}

const PHASE = { download: 'downloading', verify: 'verifying', extract: 'unpacking', done: 'installed', error: 'failed', cancelled: 'cancelled' };

/** First-run overlay: install progress, then "Booting…". */
export class FirstRunOverlay {
  constructor(host) {
    this.host = host;
    this.el = null;
    this.dismissed = false;
    this.said = '';
    // the card is not a live region (the download text changes up to ten
    // times a second): screen readers hear the title and every new phase
    // once. The region stays in the page, so the first phase is read too (a
    // live region added together with its text is not read by every one).
    this.live = h('p', { class: 'sr-only', role: 'status' });
    host.append(this.live);
  }

  /** @param {{title: string, text: string, progress?: object}} v */
  show(v) {
    if (this.dismissed) return;
    if (!this.el) {
      this.title = h('h2', {});
      this.text = h('p', {});
      this.barBox = h('div', {});
      this.el = h('div', { class: 'overlay-card' },
        this.title, this.text, this.barBox,
        h('div', { class: 'row' }, h('button', { type: 'button', class: 'btn', onclick: () => { this.dismissed = true; this.hide(); } }, 'Hide')));
      this.host.append(this.el);
    }
    this.title.textContent = v.title;
    this.text.textContent = v.text;
    this.barBox.textContent = '';
    this.barBox.append(progressBar(v.progress ?? null));
    const said = v.progress ? `${v.title}: ${PHASE[v.progress.phase] ?? v.progress.phase}` : `${v.title}${/[.…!?:]$/.test(v.title) ? '' : '.'} ${v.text}`;
    if (said !== this.said) {
      this.said = said;
      this.live.textContent = said;
    }
  }

  hide() {
    this.el?.remove();
    this.el = null;
    this.said = '';
    this.live.textContent = '';
  }

  isShown() {
    return !!this.el;
  }
}
