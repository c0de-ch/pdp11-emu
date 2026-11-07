// Banners and overlays: "no password" warning, the ready banner under the
// console (over it where there is no room) and the first-run install/boot
// overlay (UI.md §2).

import { h } from '../util.js';
import { noLogin, progressBar } from './systems.js';

const INSECURE_KEY = 'pdp11-insecure-dismissed';
const READY_MS = 15000;
const GAP = 6; // px between the ready banner and the console's header bar, frame or text edge

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
 * The places for a card that should not cover the console's text, best first:
 * under the console's header bar, where that holds nothing but what the card
 * repeats, and below the frame.
 */
function placesBy(area, frame) {
  const stage = area.parentElement;
  const bottom = Math.min(stage.getBoundingClientRect().bottom - parseFloat(getComputedStyle(stage).paddingBottom), innerHeight);
  const bar = area.querySelector('.head-bar')?.getBoundingClientRect();
  if (!bar?.height) return [{ top: frame.bottom, bottom }];
  if (bar.top >= frame.bottom) return [{ top: bar.bottom, bottom }];
  return [{ top: bar.bottom, bottom: frame.top }, { top: frame.bottom, bottom }]; // the bar above the frame
}

/**
 * What the console's header keeps showing once the ready banner has gone:
 * "2.11BSD — log in as root (no password)", "RT-11 — no login needed".
 */
export function loginMessage(sys) {
  const name = sys?.short || sys?.name;
  if (!name) return [];
  const c = sys.credentials ?? [];
  const parts = [h('strong', {}, name)];
  if (noLogin(sys) || !c.length) {
    parts.push(' — no login needed');
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
 * it sits under the console's header bar when the page has room there, over
 * the same text in the header, else over the half of the terminal away from
 * the cursor (the prompt), and it gives way to the first key typed.
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
    el.classList.remove('over');
    const place = placesBy(this.host, frame).find((p) => p.top + GAP + el.offsetHeight <= p.bottom);
    let top;
    if (place) {
      top = place.top + GAP; // covers nothing it does not repeat
    } else {
      el.classList.add('over');
      const screen = con.screen.getBoundingClientRect();
      const c = con.cursorRow();
      const upper = !c || c.row >= c.rows / 2; // the prompt is in the lower half
      top = upper ? screen.top + GAP : screen.bottom - GAP - el.offsetHeight;
    }
    el.style.top = `${Math.round(top - area.top)}px`;
    this.onPlace?.();
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

/**
 * First-run overlay: install progress (centred: the console has nothing to
 * show yet), then a compact "Booting…" that keeps clear of the boot messages.
 * When a system cannot start, a card says why and what to do next.
 */
export class FirstRunOverlay {
  constructor(host) {
    this.host = host;
    this.console = null; // ConsoleView (set by main.js): the booting card keeps clear of its text
    this.el = null;
    this.raf = 0;
    this.dismissed = false;
    this.said = '';
    this.cardKey = '';
    // the card is not a live region (the download text changes up to ten
    // times a second): screen readers hear the title and every new phase
    // once. The region stays in the page, so the first phase is read too (a
    // live region added together with its text is not read by every one).
    this.live = h('p', { class: 'sr-only', role: 'status' });
    host.append(this.live);
  }

  /** @param {{title: string, text: string, progress?: object, compact?: boolean}} v */
  show(v) {
    if (this.cardKey) this.hide();
    if (this.dismissed) return;
    if (!this.el) {
      this.title = h('h2', {});
      this.text = h('p', {});
      this.barBox = h('div', {});
      this.el = h('div', { class: 'overlay-card' },
        this.title, this.text, this.barBox,
        h('div', { class: 'row' }, h('button', { type: 'button', class: 'btn', onclick: () => { this.dismissed = true; this.hide(); } }, 'Hide')));
      // before the terminal in the document: Tab stops there, Shift+Tab from it reaches the card
      this.host.prepend(this.el);
    }
    this.title.textContent = v.title;
    this.text.textContent = v.text;
    this.barBox.textContent = '';
    this.barBox.append(progressBar(v.progress ?? null));
    this.el.classList.toggle('compact', !!v.compact);
    this.place();
    const said = v.progress ? `${v.title}: ${PHASE[v.progress.phase] ?? v.progress.phase}` : `${v.title}${/[.…!?:]$/.test(v.title) ? '' : '.'} ${v.text}`;
    this.say(said);
  }

  /**
   * The system cannot start: this card stays, also after a Hide of the
   * progress, until the user deals with it.
   * @param {string} key   what it says: the card is rebuilt only when it changes
   * @param {string} said  for screen readers
   * @param {() => Node} build
   * @param {string} [className]
   */
  showCard(key, said, build, className = '') {
    if (this.cardKey === key && this.el) return;
    this.hide();
    this.dismissed = false; // "Hide" was about the progress before this: the next attempt shows its own
    this.cardKey = key;
    this.el = h('div', { class: `overlay-card ${className}` }, build());
    this.host.prepend(this.el);
    this.place();
    // on a phone the console can be below the fold
    this.el.scrollIntoView({ block: 'nearest' });
    // nothing runs to type to, and the terminal would keep Tab from reaching the card
    const active = document.activeElement;
    if (!active || active === document.body || active.closest('.console-screen')) this.el.querySelector('.btn-primary')?.focus({ preventScroll: true });
    this.say(said);
  }

  say(said) {
    if (said !== this.said) {
      this.said = said;
      this.live.textContent = said;
    }
  }

  /**
   * The install's card sits in the middle of the terminal. The booting card
   * keeps the boot messages readable: under the console's header bar when the
   * page has room there, else over the half of the terminal away from the cursor.
   */
  place() {
    const el = this.el;
    const con = this.console;
    if (!el || !con?.frame) return;
    const area = this.host.getBoundingClientRect();
    const frame = con.frame.getBoundingClientRect();
    el.classList.remove('below');
    if (!el.classList.contains('compact')) {
      el.style.top = `${Math.round(frame.top + frame.height / 2 - area.top)}px`;
      return;
    }
    el.classList.add('below'); // its width there decides its height
    const place = placesBy(this.host, frame).find((p) => p.top + GAP + el.offsetHeight <= p.bottom);
    let top;
    if (place) {
      top = place.top + GAP;
    } else {
      el.classList.remove('below');
      const screen = con.screen.getBoundingClientRect();
      const c = con.cursorRow();
      top = !c || c.row >= c.rows / 2 ? screen.top + GAP : screen.bottom - GAP - el.offsetHeight;
    }
    el.style.top = `${Math.round(top - area.top)}px`;
  }

  placeSoon() {
    if (!this.el || this.raf) return;
    this.raf = requestAnimationFrame(() => {
      this.raf = 0;
      this.place();
    });
  }

  hide() {
    cancelAnimationFrame(this.raf);
    this.raf = 0;
    // the keyboard goes back to the console with the card it was in
    const inside = this.el?.contains(document.activeElement);
    this.el?.remove();
    this.el = null;
    if (inside) this.console?.focus();
    this.cardKey = '';
    this.said = '';
    this.live.textContent = '';
  }

  isShown() {
    return !!this.el;
  }
}
