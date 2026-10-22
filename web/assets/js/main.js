// Entry point: builds the view (main, ?view=panel, ?view=console, &chrome=0
// kiosk), connects to pdp11d and routes protocol messages to the panel,
// console, toolbar and dialogs. Exposes window.pdp11Debug for tests.

import { Connection } from './ws.js';
import { Prefs } from './prefs.js';
import { ConsoleView, SEND_KEYS } from './console.js';
import { FrontPanel, loadGeometry } from './panel/panel.js';
import { Toolbar } from './ui/toolbar.js';
import { Toasts } from './ui/toasts.js';
import { confirmDialog, confirmStop, osInMemory } from './ui/dialog.js';
import { SystemChooser, installDialog, noLogin, progressText } from './ui/systems.js';
import { Help } from './ui/help.js';
import { Tutorial } from './ui/tutorial.js';
import { InsecureBanner, ReadyBanner, FirstRunOverlay } from './ui/banners.js';
import { Popouts } from './ui/popout.js';
import { Readouts, lampBits } from './ui/readouts.js';
import { computeLayout } from './ui/layout.js';
import { attachMenu } from './ui/menu.js';
import { $, Emitter, h, newReq } from './util.js';

const params = new URLSearchParams(location.search);
const VIEW = ['panel', 'console'].includes(params.get('view')) ? params.get('view') : 'main';
const CHROME = params.get('chrome') !== '0';
const VERSION = document.querySelector('meta[name="pdp11-version"]')?.content ?? '';
const RELOAD_KEY = 'pdp11-reloaded-for';

class App extends Emitter {
  constructor() {
    super();
    this.view = VIEW;
    this.prefs = new Prefs();
    this.toasts = new Toasts($('#toasts'));
    this.state = null;
    this.systems = null;
    this.hello = null;
    this.connected = false;
    this.retryIn = 0;
    this.lastFrame = null;
    this.progress = new Map();
    this.panel = null;
    this.console = null;
    this.geometry = null;
    this.away = { panel: false, console: false };
    this.pendingAfterOff = null; // { fn, until }: action once the machine is off (reboot / switch system)
    this.lastNeedsInstall = '';
    this.lastError = '';
    this.firstRunId = '';
    this.firstRunUntil = 0;
    this.logView = null; // the simulator log viewer while it is shown (sub=log)
  }

  // ---------------------------------------------------------------- start-up

  async start() {
    const app = $('#app');
    app.dataset.view = VIEW;
    app.classList.toggle('kiosk', !CHROME);
    document.title = VIEW === 'panel' ? 'PDP-11/70 front panel' : VIEW === 'console' ? 'PDP-11/70 console' : 'PDP-11/70';
    const wantPanel = VIEW !== 'console';
    const wantConsole = VIEW !== 'panel';

    this.popouts = new Popouts({ prefs: this.prefs, onAway: (v, a) => this.setAway(v, a), toast: (t, l) => this.toast(t, l) });
    this.chooser = new SystemChooser(this);
    this.helpDlg = new Help(this);
    this.tutorial = new Tutorial(this);
    this.insecure = new InsecureBanner($('#banners'));
    this.insecure.onChange = () => this.layout();
    this.ready = new ReadyBanner($('#console-area'));
    this.firstRun = new FirstRunOverlay($('#console-area'));
    if (CHROME) {
      this.toolbar = new Toolbar($('#toolbar'), this, { full: VIEW === 'main' });
    }

    const tasks = [];
    if (wantPanel) {
      tasks.push(loadGeometry().then(async (geo) => {
        this.geometry = geo;
        await document.fonts.load('600 10px "Archivo Panel"').catch(() => {});
        this.panel = new FrontPanel($('#panel-host'), geo, {
          send: (m) => this.send(m),
          confirmKeyOff: () => this.confirmKeyOff(),
          // typing goes to the PDP-11: clicking the panel keeps the console focused
          keepFocus: () => !!this.console?.term && document.activeElement === this.console.term.textarea,
        });
        this.panel.setLampPreset(this.prefs.get('lampStyle'));
        this.panel.setBranding(this.prefs.get('branding'));
      }));
    }
    if (wantConsole) {
      this.console = new ConsoleView($('#console-host'), {
        sendInput: (b) => this.conn.sendConsole(b),
        send: (m) => this.send(m),
        prefs: this.prefs,
        toast: (t, l) => this.toast(t, l),
      });
      this.ready.console = this.console;
      // the ready banner gives way to the first key typed and keeps clear of the cursor
      this.console.onTyped = () => this.ready.hide();
      this.console.onCursorMove = () => this.ready.placeSoon();
      tasks.push(this.console.init().then(() => this.buildConsoleHead()));
    }
    const results = await Promise.allSettled(tasks);
    for (const r of results) {
      if (r.status === 'rejected') {
        console.error(r.reason);
        this.toast(`Could not start the UI: ${r.reason?.message ?? r.reason}`, 'error');
      }
    }
    this.readouts = this.panel ? new Readouts($('#readouts'), this) : null;
    this.applyPrefs();
    this.prefs.on('change', (k) => this.onPref(k));

    document.querySelectorAll('[data-bring-back]').forEach((b) => b.addEventListener('click', () => this.popouts.bringBack(b.dataset.bringBack)));
    if (VIEW === 'main') this.popouts.startMain();
    else if (window.name === `pdp11-${VIEW}` || window.opener) this.popouts.startPopout(VIEW);

    this.conn = new Connection({ subs: this.subscriptions() });
    this.wireConnection();
    this.conn.connect();
    // layout changes element sizes: coalesce into one pass per frame (no ResizeObserver loops);
    // the console header can wrap onto a second line
    const ro = new ResizeObserver(() => this.scheduleLayout());
    ro.observe($('#stage'));
    ro.observe($('#console-head'));
    window.addEventListener('resize', () => this.scheduleLayout());
    this.layout();
    this.exposeDebug();
    if (this.console && VIEW !== 'panel') setTimeout(() => this.console.focus(), 0);
  }

  subscriptions() {
    const subs = [];
    if (this.panel && !this.away.panel) subs.push('panel');
    if (this.console && !this.away.console) subs.push('console');
    if (this.logView) subs.push('log');
    return subs;
  }

  /** The simulator log viewer opened (view) or closed (null): (un)subscribe the log channel. */
  setLogView(view) {
    this.logView = view;
    this.conn?.subscribe(this.subscriptions());
  }

  // ---------------------------------------------------------------- connection

  wireConnection() {
    const c = this.conn;
    c.on('open', () => {
      this.connected = true;
      this.retryIn = 0;
      this.update();
    });
    c.on('close', ({ retryIn }) => {
      this.connected = false;
      this.retryIn = retryIn;
      this.panel?.clearLamps();
      this.lastFrame = null;
      // a shutdown's final message may never come now: its toast stops waiting for it
      this.toasts.release('shutdown');
      this.update();
      clearInterval(this.countdown);
      const until = Date.now() + retryIn;
      this.countdown = setInterval(() => {
        this.retryIn = Math.max(0, until - Date.now());
        this.toolbar?.updateStatus();
        if (this.connected || !this.retryIn) clearInterval(this.countdown);
      }, 500);
    });
    c.on('subscribed', (subs, prev) => {
      const added = (ch) => subs.includes(ch) && (!prev || !prev.includes(ch));
      if (this.console && added('console')) this.console.beginHistory();
      if (this.logView && added('log')) this.logView.reset();
    });
    c.on('hello', (m) => this.onHello(m));
    c.on('state', (m) => this.onState(m));
    c.on('systems', (m) => this.onSystems(m));
    c.on('progress', (m) => this.onProgress(m));
    c.on('toast', (m) => this.onToast(m));
    c.on('frame', (lamps) => {
      this.lastFrame = Uint8Array.from(lamps);
      this.panel?.setFrame(lamps);
    });
    c.on('console', (bytes) => this.console?.write(bytes));
    c.on('log', (m) => this.logView?.add(m.lines ?? []));
  }

  onHello(m) {
    this.hello = m;
    this.console?.setClient(m.clientId);
    if (VERSION && !VERSION.includes('{{') && m.server && m.server !== VERSION) {
      let done = '';
      try {
        done = sessionStorage.getItem(RELOAD_KEY) ?? '';
      } catch { /* storage blocked */ }
      if (done !== m.server) {
        try {
          sessionStorage.setItem(RELOAD_KEY, m.server);
        } catch { /* storage blocked */ }
        this.conn.close();
        location.reload();
        return;
      }
      this.toast(`pdp11d was updated to ${m.server}; reload the page to get the new UI`, 'warn');
    }
    if (m.insecure && CHROME) this.insecure.show();
    this.update();
  }

  onState(st) {
    const prev = this.state;
    this.state = st;
    if (st.controls) this.panel?.setServerControls(st.controls);
    if (this.console && st.console) {
      this.console.setAnswerer(st.console.answerer);
      this.console.setServerQueued(st.console.inputQueued ?? 0);
    }
    // ready banner when readyPattern appears (not when connecting to a machine that is already up)
    if (prev && st.ready && !prev.ready) this.showReadyBanner(false);
    if (!st.ready && prev?.ready) this.ready.hide();
    this.checkNeedsInstall();
    if (st.run === 'error' && st.error && st.error !== this.lastError) {
      // the simulator's own output usually says what went wrong
      this.toasts.show(st.error, 'error', { action: { label: 'Simulator log', onClick: () => this.openHelp('log') } });
    }
    this.lastError = st.run === 'error' ? st.error : '';
    if (this.pendingAfterOff && st.run === 'off') {
      const { fn, until } = this.pendingAfterOff;
      this.pendingAfterOff = null;
      if (Date.now() < until) fn();
    }
    this.updateFirstRun();
    this.tutorial.onState();
    this.update();
  }

  /** Power-on needs an install the user must accept: open the licence dialog once. */
  checkNeedsInstall() {
    const ni = this.state?.needsInstall?.id ?? '';
    if (!ni) {
      this.lastNeedsInstall = '';
      return;
    }
    if (ni === this.lastNeedsInstall || VIEW !== 'main' || !CHROME) return;
    const sys = this.systems?.list?.find((x) => x.id === ni);
    if (!sys) return; // the systems list follows the first state message
    this.lastNeedsInstall = ni;
    if (!sys.installed && !sys.installing) installDialog(this, sys);
  }

  onSystems(m) {
    this.systems = m;
    this.checkNeedsInstall();
    this.console?.setKeys(this.selectedSystem()?.console);
    this.updateConsoleHead();
    this.updateFirstRun();
    this.update();
  }

  onProgress(p) {
    const key = `${p.task}:${p.id}`;
    this.progress.set(key, p);
    const sys = this.systems?.list?.find((s) => s.id === p.id);
    if (p.task === 'shutdown') this.shutdownProgress(p, sys);
    else if (p.phase === 'error') this.toast(progressText(p, sys), 'error');
    else if (p.phase === 'done' && !this.firstRun.isShown()) this.toast(progressText(p, sys), 'info');
    if (['done', 'error', 'cancelled'].includes(p.phase)) {
      setTimeout(() => {
        if (this.progress.get(key) === p) {
          this.progress.delete(key);
          this.chooser.refresh();
        }
      }, 6000);
    }
    if (p.task === 'shutdown' && (p.phase === 'error' || p.phase === 'cancelled')) this.pendingAfterOff = null;
    this.updateFirstRun();
    this.chooser.refresh();
  }

  /**
   * One toast covers an OS shutdown: each step updates its text in place, the
   * final message replaces it (R26). The server's toast about a failed or
   * cancelled shutdown carries the request's req and takes its place too.
   */
  shutdownProgress(p, sys) {
    this.shutdownReq = p.req ?? '';
    const text = p.phase === 'cancelled' ? p.text || 'Shut down OS cancelled' : p.text || progressText(p, sys);
    if (p.phase === 'step') this.toasts.show(text, 'info', { key: 'shutdown', sticky: true });
    else this.toasts.show(text, p.phase === 'error' ? 'warn' : 'info', { key: 'shutdown' });
  }

  onToast(m) {
    if (m.req && m.req === this.shutdownReq && this.toasts.has('shutdown')) {
      this.toasts.show(m.text, m.level, { key: 'shutdown' });
      return;
    }
    this.toast(m.text, m.level);
  }

  progressFor(task, id) {
    return this.progress.get(`${task}:${id}`) ?? null;
  }

  /** First run: the default system installs automatically, then boots (UI.md §2). */
  updateFirstRun() {
    if (VIEW === 'panel' || !CHROME) return;
    const sel = this.selectedSystem();
    const st = this.state;
    if (!sel || !st) return;
    const prog = this.progressFor('install', sel.id);
    const installing = sel.installing || (prog && !['done', 'error', 'cancelled'].includes(prog.phase));
    if (installing && !sel.installed) {
      this.firstRunId = sel.id;
      this.firstRunUntil = 0;
      this.firstRun.show({ title: `Setting up ${sel.name}`, text: prog ? progressText(prog, sel) : `Preparing ${sel.name}…`, progress: prog });
      return;
    }
    if (this.firstRunId === sel.id && sel.installed) {
      // after the system's boot timeout the server warns (toast); the overlay
      // then gets out of the way of the console
      if (!this.firstRunUntil) this.firstRunUntil = Date.now() + 1000 * (sel.bootTimeoutSec > 0 ? sel.bootTimeoutSec : 120);
      if (!st.ready && ['starting', 'running', 'halted'].includes(st.run) && Date.now() < this.firstRunUntil) {
        this.firstRun.show({
          title: `Booting ${sel.name}…`,
          text: noLogin(sel)
            ? 'Watch the console and the lamps: the console is ready when the system prompt appears.'
            : 'Watch the console and the lamps: the login prompt appears when the system is up.',
        });
        clearTimeout(this.firstRunTimer);
        this.firstRunTimer = setTimeout(() => this.updateFirstRun(), Math.max(0, this.firstRunUntil - Date.now()) + 50);
        return;
      }
      this.firstRunId = '';
    }
    this.firstRun.hide();
  }

  // ---------------------------------------------------------------- helpers for UI modules

  send(msg) {
    const m = msg.req || msg.type === 'ping' ? msg : { ...msg, req: newReq() };
    const ok = this.conn?.send(m) ?? false;
    this.emit('sent', m);
    if (!ok && msg.type !== 'ping') this.toast('Not connected to pdp11d', 'warn');
    return ok;
  }

  onSent(fn) {
    return this.on('sent', fn);
  }

  toast(text, level = 'info') {
    this.toasts.show(text, level);
  }

  selectedSystem() {
    const id = this.systems?.selected ?? this.state?.system;
    return this.systems?.list?.find((s) => s.id === id) ?? null;
  }

  runningSystem() {
    const id = this.state?.running || this.state?.system;
    return this.systems?.list?.find((s) => s.id === id) ?? this.selectedSystem();
  }

  controls() {
    return this.panel ? this.panel.controls() : this.state?.controls ?? null;
  }

  lampBits() {
    return lampBits(this.lastFrame);
  }

  /** Panel messages, through the local panel (optimistic rendering) when there is one. */
  panelActions() {
    const p = this.panel && !this.away.panel ? this.panel : null;
    return {
      sr: (v) => (p ? p.setSR(v) : this.send({ type: 'sr', value: v })),
      press: (name, active) => (p ? p.press(name, active) : this.send({ type: 'momentary', name, active })),
      latch: (name, value) => (p ? p.setLatch(name, value) : this.send({ type: 'latch', name, value })),
      knob: (name, pos) => (p ? p.setKnob(name, pos) : this.send({ type: 'knob', name, pos })),
      key: (pos) => this.send({ type: 'key', pos }),
    };
  }

  // ---------------------------------------------------------------- actions (toolbar)

  openSystems() {
    this.chooser.open();
  }

  openHelp(tab) {
    this.helpDlg.open(tab);
  }

  openTutorial() {
    this.tutorial.open();
  }

  showReadyBanner(sticky) {
    if (!this.console || this.away.console) return;
    this.ready.show(this.runningSystem(), { sticky });
  }

  /**
   * Asks before stopping the OS (UI.md §2): 'shutdown' | 'plug' | 'cancel'.
   * "Continue and shut down" (an OS halted from the panel) resumes the CPU
   * first and then answers 'shutdown'.
   */
  async confirmStop(what) {
    const v = await confirmStop({ what, system: this.runningSystem(), state: this.state });
    if (v !== 'resume') return v;
    return (await this.resumeOS()) ? 'shutdown' : 'cancel';
  }

  async confirmKeyOff() {
    return this.confirmStop('Power off');
  }

  /** ENABLE, CONT, and wait until the CPU runs again (the OS can then shut down). */
  async resumeOS() {
    const p = this.panelActions();
    const pause = (ms) => new Promise((r) => setTimeout(r, ms));
    p.latch('ENABLE_HALT', 'enable');
    p.press('CONT', true);
    await pause(160);
    p.press('CONT', false);
    for (const t0 = Date.now(); this.state?.run !== 'running'; await pause(100)) {
      if (!this.connected || Date.now() - t0 > 5000) {
        this.toast('The CPU did not continue: set ENABLE/HALT to ENABLE, press CONT, then use Shut down OS', 'warn');
        return false;
      }
    }
    return true;
  }

  async togglePower() {
    const st = this.state;
    if (!st) return;
    if (st.power !== 'on') {
      this.send({ type: 'key', pos: 'power' });
      return;
    }
    const v = await this.confirmKeyOff();
    if (v === 'shutdown') this.send({ type: 'shutdownOS' });
    else if (v === 'plug') this.send({ type: 'key', pos: 'off' });
  }

  async boot() {
    const st = this.state;
    if (!st) return;
    if (st.run === 'running' || osInMemory(st)) {
      const v = await this.confirmStop('Reboot');
      if (v === 'cancel') return;
      if (v === 'shutdown') {
        this.afterOff(() => this.send({ type: 'boot' }));
        this.send({ type: 'shutdownOS' });
        return;
      }
    }
    this.send({ type: 'boot' });
  }

  shutdownOS() {
    this.send({ type: 'shutdownOS' });
  }

  setSpeed(value) {
    this.send({ type: 'speed', value });
  }

  async selectSystem(id) {
    const st = this.state;
    const sys = this.systems?.list?.find((s) => s.id === id);
    if (!st || !sys) return;
    if (st.run === 'off' || st.run === 'error') {
      this.send({ type: 'selectSystem', id });
      return;
    }
    const v = await this.confirmStop(`Switch to ${sys.name}`);
    if (v === 'cancel') return;
    // power off (cleanly or not), select, then turn the key back on
    this.afterOff(() => {
      this.send({ type: 'selectSystem', id });
      setTimeout(() => this.send({ type: 'key', pos: 'power' }), 300);
    });
    if (v === 'shutdown') this.send({ type: 'shutdownOS' });
    else this.send({ type: 'key', pos: 'off' });
  }

  /** Runs fn once the machine is off (after a clean shutdown or pulling the plug), within 2 minutes. */
  afterOff(fn) {
    this.pendingAfterOff = { fn, until: Date.now() + 120000 };
  }

  popOut(view) {
    this.popouts.open(view);
  }

  setAway(view, away) {
    this.away[view] = away;
    $(`#${view}-away`).hidden = !away;
    if (view === 'panel') {
      $('#panel-host').hidden = away;
      if (away) this.panel?.clearLamps();
    } else {
      $('#console-host').hidden = away;
      $('#console-head').hidden = away;
      if (away) this.ready.hide();
    }
    this.conn?.subscribe(this.subscriptions());
    this.layout();
  }

  // ---------------------------------------------------------------- console header

  buildConsoleHead() {
    const head = $('#console-head');
    head.textContent = '';
    if (!CHROME) return;
    this.keyHelpEl = h('span', { class: 'keyhelp', title: '' });
    this.pasteEl = h('span', { class: 'paste-progress', hidden: true },
      h('span', { class: 'paste-text' }, ''),
      h('button', { type: 'button', class: 'btn', onclick: () => this.cancelPaste() }, 'Cancel'));
    const authentic = h('input', { type: 'checkbox', checked: this.prefs.get('authenticKeys') });
    authentic.addEventListener('change', () => this.prefs.set('authenticKeys', authentic.checked));
    this.authenticBox = authentic;
    const sendKey = h('button', { type: 'button', class: 'btn', title: 'Send a control key the browser would otherwise keep' }, 'Send key ▾');
    // sendKey focuses the terminal: closing the menu must not take the focus back
    attachMenu(sendKey, () => SEND_KEYS.map(([label, seq]) => ({ label, refocus: false, onSelect: () => this.console.sendKey(seq) })));
    head.append(
      h('span', { class: 'title' }, 'Console'),
      this.keyHelpEl,
      this.pasteEl,
      h('label', { class: 'toggle', title: 'Raw xterm keys: Backspace sends DEL, Ctrl+C sends ^C' }, authentic, 'Authentic keys'),
      sendKey,
    );
    this.console.onPasteProgress = () => this.updatePasteStatus();
    this.updateConsoleHead();
  }

  /**
   * The paste line of the console header (UI.md §5): this window's own paste,
   * with Cancel; input the server holds while the CPU is not running (typed
   * here or by another viewer) as a hint without Cancel; nothing for another
   * viewer's paste.
   */
  updatePasteStatus() {
    if (!this.pasteEl) return;
    const own = this.console.pasteRemaining();
    const held = this.state && this.state.run !== 'running' ? this.console.otherQueued() : 0;
    const text = own > 0 ? `Pasting… ${own} bytes` : held > 0 ? `${held} characters waiting until the CPU runs` : '';
    this.pasteEl.hidden = !text;
    this.pasteEl.querySelector('.paste-text').textContent = text;
    this.pasteEl.querySelector('button').hidden = own <= 0;
  }

  /** inputCancel drops the server's whole input queue: ask first when it holds other input too. */
  async cancelPaste() {
    const others = this.console.otherQueued();
    if (others > 0 && !(await confirmDialog({
      title: 'Cancel the paste',
      text: `${others} more characters are queued that are not part of this paste (typed while the CPU was halted, or sent by another viewer). Cancelling drops them too.`,
      ok: 'Cancel everything', cancel: 'Keep pasting', danger: true,
    }))) return;
    this.console.cancelPaste();
  }

  updateConsoleHead() {
    if (!this.keyHelpEl) return;
    const sys = this.selectedSystem();
    const k = sys?.console;
    const text = k?.keyHelp || (k ? `Backspace = ${k.backspace === 'bs' ? 'BS' : k.backspace === '#' ? '#' : 'DEL'}, Ctrl+C = ${k.interrupt === 'del' ? 'DEL' : '^C'}` : '');
    this.keyHelpEl.textContent = text;
    this.keyHelpEl.title = text;
  }

  // ---------------------------------------------------------------- prefs

  applyPrefs() {
    for (const k of ['lampStyle', 'branding', 'readouts', 'authenticKeys']) this.onPref(k);
  }

  onPref(k) {
    const v = this.prefs.get(k);
    if (k === 'lampStyle') this.panel?.setLampPreset(v);
    else if (k === 'branding') this.panel?.setBranding(v);
    else if (k === 'readouts') {
      const show = !!v && !!this.panel;
      $('#readouts').hidden = !show;
      if (show) this.readouts?.start();
      else this.readouts?.stop();
      this.layout();
    } else if (k === 'authenticKeys' && this.authenticBox) this.authenticBox.checked = !!v;
    if (k === 'cols') this.layout();
  }

  // ---------------------------------------------------------------- rendering

  update() {
    this.toolbar?.update();
    this.chooser?.refresh();
    this.tutorial?.updateLive();
  }

  scheduleLayout() {
    if (this.layoutFrame) return;
    this.layoutFrame = requestAnimationFrame(() => {
      this.layoutFrame = 0;
      this.layout();
    });
  }

  layout() {
    const stage = $('#stage');
    if (!stage) return;
    // measured from the viewport, not the stage: in the scrolling (mobile)
    // layout the stage grows with its content
    const cs = getComputedStyle(stage);
    const above = ($('#toolbar')?.offsetHeight ?? 0) + ($('#banners')?.offsetHeight ?? 0);
    const W = document.documentElement.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight);
    const pageH = window.innerHeight - parseFloat(cs.paddingTop) - parseFloat(cs.paddingBottom);
    const H = pageH - above;
    const showPanel = !!this.panel && !this.away.panel;
    const showConsole = !!this.console && !this.away.console;
    // a view in another window leaves its "Bring back" notice in the row
    const notice = showPanel !== showConsole ? [$('#panel-away'), $('#console-away')].find((e) => !e.hidden) : null;
    const noticeW = notice ? notice.offsetWidth + parseFloat(cs.columnGap || 0) : 0;
    const head = $('#console-head');
    const headH = showConsole && CHROME ? Math.max(34, head.offsetHeight) : 0;
    const readoutsH = showPanel && !$('#readouts').hidden ? 24 : 0;
    const chrome = { w: 22, h: 22 + headH };
    const L = computeLayout({
      W: W - noticeW, H, pageH, showPanel, showConsole, panelExtraH: readoutsH, consoleChrome: chrome,
      consoleFit: (box) => this.console.predict(box),
    });
    if (L.layout === 'none' && VIEW === 'main') {
      // both views are popped out: show the two "bring back" notices side by side
      stage.dataset.layout = 'side';
    } else {
      stage.dataset.layout = L.layout;
    }
    document.body.classList.toggle('scroll', L.layout === 'mobile');
    if (L.panel && this.panel) {
      $('#panel-host').style.width = `${L.panel.w}px`;
      $('#landscape-hint').hidden = !(L.layout === 'mobile' && innerHeight > innerWidth);
    }
    if (L.consoleBox && this.console) {
      // phones: keep the console readable and let it scroll sideways instead
      const minSize = L.layout === 'mobile' ? 12 : 0;
      const sz = this.console.fit(Math.max(50, L.consoleBox.w), Math.max(50, L.consoleBox.h), { minSize });
      $('#console-area').style.width = L.layout === 'mobile' ? '100%' : `${sz.w + chrome.w}px`;
    } else if (this.console) {
      $('#console-area').style.width = ''; // in another window: the notice sizes the area
    }
    this.lastLayout = { ...L, W, H };
    // xterm.js applies a new font size on its next render
    requestAnimationFrame(() => this.ready.place());
  }

  exposeDebug() {
    const app = this;
    window.pdp11Debug = {
      view: VIEW,
      version: VERSION,
      get state() { return app.state; },
      get systems() { return app.systems; },
      get hello() { return app.hello; },
      get connected() { return app.connected; },
      get lastFrame() { return app.lastFrame; },
      get smoothed() { return app.panel?.lamps.smoothed() ?? null; },
      get sent() { return app.conn?.sent ?? []; },
      get layout() { return app.lastLayout; },
      logText: () => app.logView?.text() ?? null,
      controls: () => app.controls(),
      consoleText: () => app.console?.text() ?? '',
      consoleFont: () => app.console?.fontMode ?? null,
      app,
    };
  }
}

const app = new App();
app.start().catch((err) => {
  console.error(err);
  document.body.prepend(h('p', { class: 'banner banner-warn' }, `The UI failed to start: ${err.message}`));
});
