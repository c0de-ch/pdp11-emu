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
import { SystemChooser, failureView, installDialog, noLogin, progressText, retryInstall } from './ui/systems.js';
import { Help } from './ui/help.js';
import { Tutorial } from './ui/tutorial.js';
import { InsecureBanner, ReadyBanner, FirstRunOverlay, loginMessage } from './ui/banners.js';
import { Popouts } from './ui/popout.js';
import { Readouts, lampBits } from './ui/readouts.js';
import { computeLayout, GAP } from './ui/layout.js';
import { attachMenu } from './ui/menu.js';
import { renderMarkdown } from './ui/markdown.js';
import { $, Emitter, h, newReq } from './util.js';

const params = new URLSearchParams(location.search);
const VIEW = ['panel', 'console'].includes(params.get('view')) ? params.get('view') : 'main';
const CHROME = params.get('chrome') !== '0';
const VERSION = document.querySelector('meta[name="pdp11-version"]')?.content ?? '';
const RELOAD_KEY = 'pdp11-reloaded-for';
const FINAL_PHASES = ['done', 'error', 'cancelled'];
const NOT_CONNECTED = 'Not connected to pdp11d: that was not sent. It reconnects by itself.';
const STAGE_PAD = CHROME ? 8 : VIEW === 'panel' ? 12 : 8; // px around the stage: a kiosk panel keeps a small margin
const FRAME_BORDER = 1; // px, console.css
const FRAME_PAD = 10; // px, the console frame's usual margins around the terminal
const BAR_MARGIN = 8; // px above the console's header bar
const MIN_NOTES_H = 64; // px, lower than this the longer notes of the dock are not worth showing
const NARROW_BAR = 480; // px, a narrower console header bar shortens its message
const READOUTS_H = 30; // px, the compact octal readouts under a panel
const READOUTS_DOCK_H = 52; // px, the large octal readouts in the dock
const TOAST_MARGIN = 10; // px between the toasts and the window or a column's edge

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
    this.licenceAccepted = new Set(); // systems whose licence was accepted in this page
    this.dismissedFailures = new Map(); // system id -> the failure's text the user closed
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
          // typing goes to the PDP-11: a mouse click on the panel leaves (or puts) the keyboard
          // in the console; the panel's shortcuts take Tab or Shift+Tab (focus ring). No
          // console in this window, or a tap (no on-screen keyboard): the control gets the focus
          keepFocus: (e) => {
            const term = this.console?.term;
            if (!term || this.away.console) return false;
            if (document.activeElement === term.textarea) return true;
            if (e?.pointerType !== 'mouse' || document.activeElement?.closest?.('#panel-host')) return false;
            term.focus();
            return true;
          },
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
      this.firstRun.console = this.console;
      this.console.notConnected = () => this.notConnected();
      this.console.onFontsChanged = () => this.layout();
      this.ready.onPlace = () => this.placeToasts();
      // the ready banner gives way to the first key typed; it and the booting card keep clear of the cursor
      this.console.onTyped = () => this.ready.hide();
      this.console.onCursorMove = () => {
        this.ready.placeSoon();
        this.firstRun.placeSoon();
      };
      // a console that cannot keep up drops its backlog and reconnects: the
      // server replays its history instead (PROTOCOL §1)
      this.console.onBacklog = () => this.conn?.reconnect('console backlog');
      tasks.push(this.console.init().then(() => this.buildConsoleHead()));
    }
    if (wantPanel && CHROME && VIEW === 'main') this.buildPanelDock();
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

    document.querySelectorAll('[data-bring-back]').forEach((b) => b.addEventListener('click', (e) => {
      const view = b.dataset.bringBack;
      this.popouts.bringBack(view);
      // the button is gone: from the keyboard (detail 0) the focus goes to the view
      // that came back; a mouse click on the panel's leaves typing in the console
      (view === 'console' || !e.detail ? this[view] : this.console)?.focus();
    }));
    if (VIEW === 'main') this.popouts.startMain();
    else if (window.name === `pdp11-${VIEW}` || window.opener) this.popouts.startPopout(VIEW);

    this.conn = new Connection({ subs: this.subscriptions() });
    this.wireConnection();
    this.conn.connect();
    // layout changes element sizes: coalesce into one pass per frame (no ResizeObserver loops);
    // the toolbar and the banners above the stage can change their height
    const ro = new ResizeObserver(() => this.scheduleLayout());
    ro.observe($('#stage'));
    ro.observe($('#toolbar'));
    ro.observe($('#banners'));
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
      this.toasts.dismiss('offline');
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
    // a failed install has its own report (taskFailed, the system's entry): not twice
    if (st.run === 'error' && st.error && st.error !== this.lastError && !st.error.startsWith('installing ')) {
      // the simulator's own output usually says what went wrong
      this.toasts.show(sentence(st.error), 'error', { action: { label: 'Simulator log', onClick: () => this.openHelp('log') } });
    }
    this.lastError = st.run === 'error' ? st.error : '';
    if (this.pendingAfterOff && st.run === 'off') {
      const { fn, until } = this.pendingAfterOff;
      this.pendingAfterOff = null;
      if (Date.now() < until) fn();
    }
    this.updateFirstRun();
    this.updateFocusChip();
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
    // a message the user closed was about the attempt before this one
    if (p.task !== 'shutdown') this.dismissedFailures.delete(p.id);
    if (p.task === 'shutdown') this.shutdownProgress(p, sys);
    else if (p.phase === 'error') this.taskFailed(p, sys);
    // the first-run overlay says when an install is done, the chooser's card when it is open; a factory reset gets its toast
    else if (p.phase === 'done' && !this.chooser.isOpen() && (p.task !== 'install' || !this.firstRun.isShown())) this.toast(progressText(p, sys), 'info');
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
   * A failed install or factory reset. The chooser's card and the first-run
   * card say what happened and what to do; when neither is in view, a short
   * toast leads to the chooser.
   */
  taskFailed(p, sys) {
    const name = sys?.short || sys?.name || p.id;
    const verb = p.task === 'factoryReset' ? `The factory reset of ${name} failed.` : `${name} could not be installed.`;
    const summary = p.failure?.summary ?? '';
    if (this.chooser.isOpen()) {
      this.chooser.announce(`${verb} ${summary}`);
      return;
    }
    if (p.task === 'install' && p.id === this.selectedSystem()?.id && !sys?.installed && !this.away.console && VIEW !== 'panel' && CHROME) return;
    this.toasts.show(`${verb} ${summary || sentence(p.text ?? '')}`.trim(), 'error', { action: { label: 'What to do', onClick: () => this.openSystems(p.id) } });
  }

  /** Why the last install or reset of sys failed, unless the user closed that message. */
  failureOf(sys) {
    const last = this.progressFor('install', sys.id);
    const reset = this.progressFor('factoryReset', sys.id);
    const p = [reset, last].find((x) => x?.phase === 'error');
    const f = sys.failure ?? p?.failure ?? (p ? { task: p.task, summary: '', hint: '', detail: p.text ?? '' } : null);
    return f && this.dismissedFailures.get(sys.id) !== f.detail ? f : null;
  }

  dismissFailure(sys) {
    const f = this.failureOf(sys);
    if (f) this.dismissedFailures.set(sys.id, f.detail);
    this.updateFirstRun();
    this.chooser.refresh();
  }

  /**
   * One toast covers an OS shutdown: each step updates its text in place, the
   * final message replaces it. The server's toast about a failed or
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
    const running = (p) => (p && !FINAL_PHASES.includes(p.phase) ? p : null);
    const resetting = sel.task === 'factoryReset' || !!running(this.progressFor('factoryReset', sel.id));
    const prog = running(this.progressFor(resetting ? 'factoryReset' : 'install', sel.id));
    const installing = sel.installing || !!prog;
    if (installing && !sel.installed) {
      this.firstRunId = sel.id;
      this.firstRunUntil = 0;
      this.firstRun.show({ title: `${resetting ? 'Restoring' : 'Setting up'} ${sel.name}`, text: prog ? progressText(prog, sel) : `Preparing ${sel.name}…`, progress: prog });
      return;
    }
    const failure = this.failureOf(sel);
    if (failure && !sel.installed && !this.away.console) {
      const title = `${sel.short || sel.name} could not be installed`;
      this.firstRun.showCard(failure.detail, `${title}. ${failure.summary}`, () => failureView(failure, {
        title,
        onDismiss: () => this.dismissFailure(sel),
        actions: [
          h('button', { type: 'button', class: 'btn btn-primary', onclick: () => retryInstall(this, sel) }, 'Try again'),
          h('button', { type: 'button', class: 'btn', onclick: () => this.openSystems(sel.id) }, 'Other systems…'),
        ],
      }), 'failure-card');
      return;
    }
    // its licence dialog was cancelled: the way forward stays in view
    if (st.needsInstall?.id === sel.id && !sel.installed && !this.away.console) {
      const title = `${sel.short || sel.name} is not installed`;
      this.firstRun.showCard(`needs ${sel.id}`, `${title}.`, () => h('div', {},
        h('h2', {}, title),
        h('p', {}, 'Its licence must be accepted before it can be installed. You can also choose another system.'),
        h('div', { class: 'row' },
          h('button', { type: 'button', class: 'btn', onclick: () => this.openSystems(sel.id) }, 'Other systems…'),
          h('button', { type: 'button', class: 'btn btn-primary', onclick: () => installDialog(this, sel) }, 'Licence and install'))));
      return;
    }
    if (this.firstRunId === sel.id && sel.installed) {
      // after the system's boot timeout the server warns (toast); the overlay
      // then gets out of the way of the console
      const now = Date.now();
      if (!this.firstRunUntil) {
        this.firstRunUntil = now + 1000 * (sel.bootTimeoutSec > 0 ? sel.bootTimeoutSec : 120);
        // the power-on follows a finished install: a moment may pass until the state says so
        const done = [this.progressFor('install', sel.id), this.progressFor('factoryReset', sel.id)].some((p) => p?.phase === 'done');
        this.firstRunPowering = st.power === 'on' && done ? now + 3000 : 0;
        this.firstRunOldError = st.error;
      }
      const booting = ['starting', 'running', 'halted'].includes(st.run);
      // a new error ends the wait: the simulator failed to start (the old one is the install's, until the power-on)
      const failed = st.run === 'error' && st.error && st.error !== this.firstRunOldError;
      if (!st.ready && (booting || (now < this.firstRunPowering && !failed)) && now < this.firstRunUntil) {
        this.firstRun.show({
          title: `Booting ${sel.name}…`,
          text: noLogin(sel)
            ? 'Watch the console and the lamps: the console is ready when the system prompt appears.'
            : 'Watch the console and the lamps: the login prompt appears when the system is up.',
          compact: true,
        });
        clearTimeout(this.firstRunTimer);
        this.firstRunTimer = setTimeout(() => this.updateFirstRun(), Math.max(0, (booting ? this.firstRunUntil : this.firstRunPowering) - now) + 50);
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
    if (!ok && msg.type !== 'ping') this.notConnected();
    return ok;
  }

  /** Something could not be sent: one message for all of it, gone when the connection is back. */
  notConnected() {
    this.toasts.show(NOT_CONNECTED, 'warn', { key: 'offline' });
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

  openSystems(focusId) {
    this.chooser.open(focusId);
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
    // the licence was declined: the server cannot start the system, so ask again
    const missing = this.systems?.list?.find((x) => x.id === st.needsInstall?.id);
    if (missing && !missing.installed && !missing.installing) {
      installDialog(this, missing);
      return;
    }
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
      this.systemChosen();
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
    this.systemChosen();
  }

  /** The chooser has done its job: what comes next happens on the console. */
  systemChosen() {
    this.chooser.close();
    if (!this.away.console) this.console?.focus();
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
      $('#panel-dock').hidden = away && VIEW === 'main';
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
    // the bar keeps its height and the frame its place whatever the message is
    this.focusEl = h('span', { class: 'focus-chip', onclick: () => this.console.focus() }, h('span', {}, 'Click to type'));
    this.console.onFocusChange = (on) => {
      this.consoleFocused = on;
      this.updateFocusChip();
    };
    this.pasteEl = h('span', { class: 'paste-progress', hidden: true },
      h('span', { class: 'paste-text' }, ''),
      h('button', { type: 'button', class: 'btn', onclick: () => this.cancelPaste() }, 'Cancel'));
    const authentic = h('input', { type: 'checkbox', checked: this.prefs.get('authenticKeys') });
    authentic.addEventListener('change', () => this.prefs.set('authenticKeys', authentic.checked));
    this.authenticBox = authentic;
    const authenticLabel = h('label', { class: 'toggle', title: 'Raw xterm keys: Backspace sends DEL, Ctrl+C sends ^C' }, authentic, 'Authentic keys');
    this.keepTyping(authenticLabel);
    const sendKey = h('button', { type: 'button', class: 'btn', title: 'Send a control key the browser would otherwise keep' }, 'Send key ▾');
    // sendKey focuses the terminal: closing the menu must not take the focus back
    attachMenu(sendKey, () => [
      { title: 'Usual meaning; it depends on the system' },
      ...SEND_KEYS.map(([label, seq, hint]) => ({ label, hint: this.sendKeyHint(seq, hint), refocus: false, onSelect: () => this.console.sendKey(seq) })),
    ], { className: 'key-menu' });
    // where the header has no room for the key help, it opens from the bar
    this.keysBtn = h('button', { type: 'button', class: 'btn keys-btn', title: 'The keys of this system', hidden: true }, 'Keys ▾');
    attachMenu(this.keysBtn, () => this.keyHelpItems().map((label) => ({ label, refocus: false, onSelect: () => this.console.focus() })));
    this.loginEl = h('p', { class: 'login-line' });
    this.keyHelpEl = h('ul', { class: 'keyhelp hint-list' });
    // a scroller, not a Tab stop: Shift+Tab goes from the terminal to its header's buttons (Help has the same text)
    this.tryEl = h('div', { class: 'try-notes', tabindex: -1 });
    head.append(
      h('div', { class: 'head-bar' },
        h('span', { class: 'title' }, 'Console'),
        h('span', { class: 'head-msg' }, this.focusEl, this.pasteEl),
        authenticLabel,
        this.keysBtn,
        sendKey),
      h('div', { class: 'head-info' }, this.loginEl, this.keyHelpEl),
      this.tryEl,
    );
    this.console.onPasteProgress = () => this.updatePasteStatus();
    this.updateConsoleHead();
  }

  /** What a Send key entry does here: the erase and interrupt keys are the system's own. */
  sendKeyHint(seq, hint) {
    const k = this.selectedSystem()?.console;
    if (seq === '\x7f') return k?.interrupt === 'del' ? 'Interrupt a program' : k?.backspace === 'del' ? 'Erase a character' : hint;
    if (seq === '\x03' && k?.interrupt === 'del') return 'Nothing special here (DEL interrupts)';
    return hint;
  }

  /** A mouse click on a switch in the header or the dock leaves the keyboard in the console; Space on it keeps the focus there. */
  keepTyping(label) {
    label.addEventListener('click', (e) => {
      if (e.detail > 0) setTimeout(() => this.console?.focus());
    });
  }

  /** The dock under the panel: its switches and how to work it. */
  buildPanelDock() {
    const bar = $('#panel-bar');
    const readouts = h('input', { type: 'checkbox', checked: this.prefs.get('readouts') });
    readouts.addEventListener('change', () => this.prefs.set('readouts', readouts.checked));
    this.readoutsBox = readouts;
    const readoutsLabel = h('label', { class: 'toggle grow', title: 'SR, ADDRESS and DATA as octal numbers' }, readouts, 'Octal readouts');
    this.keepTyping(readoutsLabel);
    bar.append(
      h('span', { class: 'title' }, 'Front panel'),
      readoutsLabel,
      h('button', { type: 'button', class: 'btn', title: 'What every control does', onclick: () => this.openHelp('panel') }, 'Panel guide'),
      h('button', { type: 'button', class: 'btn', title: 'Toggle a program in with the switches', onclick: () => this.openTutorial() }, 'Toggle-in tutorial'),
    );
    const key = (...keys) => keys.map((k) => h('kbd', {}, k));
    const hint = (...parts) => h('li', {}, ...parts);
    $('#panel-notes').append(
      hint('Click a switch to flip it; drag across several to set them all'),
      hint('Click LOAD ADRS, EXAM, DEP, CONT or START to press it'),
      hint('Click a knob or the key on its right or left half to turn it'),
      hint(...key('Shift+Tab'), ' from the console reaches the panel: a ring shows the control'),
    );
    const shortcut = (keys, text) => h('li', {}, ...key(...keys), ` ${text}`);
    $('#panel-keys').append(
      h('h3', {}, 'With the keyboard on the panel'),
      h('ul', { class: 'hint-list shortcuts' },
        shortcut(['0–7'], 'shift a digit into the switches'), shortcut(['Esc'], 'clears them'),
        shortcut(['L'], 'LOAD ADRS'), shortcut(['E'], 'EXAM'), shortcut(['D'], 'DEP'), shortcut(['C'], 'CONT'), shortcut(['S'], 'START'),
        shortcut(['H'], 'ENABLE/HALT'), shortcut(['B'], 'S INST/S BUS CYCLE'), shortcut(['T'], 'LAMP TEST (hold)'),
        shortcut(['[', ']'], 'ADDRESS knob'), shortcut(['{', '}'], 'DATA knob'), shortcut(['K'], 'key switch')),
    );
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

  /** The selected system's key help, one entry per key. */
  keyHelpItems() {
    const k = this.selectedSystem()?.console;
    if (!k) return [];
    if (k.keyHelp) return k.keyHelp.split(/\s+·\s+/);
    return [`Backspace = ${k.backspace === 'bs' ? 'BS' : k.backspace === '#' ? '#' : 'DEL'}`, `Ctrl+C = ${k.interrupt === 'del' ? 'DEL' : '^C'}`];
  }

  /** What the header's slot says: with the machine off, typing would reach nothing. */
  updateFocusChip() {
    if (!this.focusEl) return;
    const off = ['off', 'error'].includes(this.state?.run);
    const typing = this.consoleFocused && !off;
    this.focusEl.classList.toggle('on', typing);
    const text = this.focusEl.firstChild;
    text.textContent = '';
    if (typing) text.append('Typing', h('span', { class: 'long' }, ' to the PDP-11'));
    else text.append(this.consoleFocused ? 'Machine off' : 'Click to type');
  }

  updateConsoleHead() {
    if (!this.keyHelpEl) return;
    const sys = this.selectedSystem();
    // the systems list comes often during installs: the notes keep their scroll position
    const sig = JSON.stringify([sys?.id, sys?.console, sys?.credentials, sys?.notes, sys?.description]);
    if (sig === this.headSig) return;
    this.headSig = sig;
    const items = this.keyHelpItems();
    // a key help that starts with the login says it better than the accounts can
    const login = items.find((t) => /^log in/i.test(t));
    this.keyHelpEl.textContent = '';
    this.keyHelpEl.append(...items.filter((t) => t !== login).map((text) => {
      // the key that starts an entry stands out: "Ctrl+U kills the line"
      const m = text.match(/^(Backspace|Delete|Ctrl\+\S+|@)(\s.*)$/);
      return h('li', {}, ...(m ? [h('kbd', {}, m[1]), m[2]] : [text]));
    }));
    this.loginEl.textContent = '';
    if (login) this.loginEl.append(h('strong', {}, sys?.short || sys?.name || ''), ` — ${login[0].toLowerCase()}${login.slice(1)}`);
    else this.loginEl.append(...loginMessage(sys));
    // the room in the dock is for what to do with this system
    this.tryEl.textContent = '';
    const tries = sys?.notes?.match(/^## Things to try[^\n]*\n([\s\S]*?)(?=^## |(?![\s\S]))/m)?.[1];
    if (tries) this.tryEl.append(h('h3', {}, 'Things to try'), renderMarkdown(tries));
    const about = $('#panel-about');
    about.textContent = '';
    if (sys?.description) about.append(h('h3', {}, sys.name), sys.description);
    this.scheduleLayout();
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
      if (this.readoutsBox) this.readoutsBox.checked = !!v;
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
    const above = ($('#toolbar')?.offsetHeight ?? 0) + ($('#banners')?.offsetHeight ?? 0);
    const W = document.documentElement.clientWidth - 2 * STAGE_PAD;
    const pageH = window.innerHeight - 2 * STAGE_PAD;
    const H = pageH - above;
    const showPanel = !!this.panel && !this.away.panel;
    const showConsole = !!this.console?.frame && !this.away.console;
    // a view in another window leaves its "Bring back" notice in the row
    const notice = showPanel !== showConsole ? [$('#panel-away'), $('#console-away')].find((e) => !e.hidden) : null;
    const noticeW = notice ? notice.offsetWidth + GAP : 0;
    const readouts = showPanel && !$('#readouts').hidden;
    const border = 2 * FRAME_BORDER;
    const chrome = {
      x: border + 2 * FRAME_PAD, y: border + 2 * FRAME_PAD, minY: border + 2, mx: border + 2 * 3, border,
      bar: showConsole && CHROME ? this.barHeight() + BAR_MARGIN : 0,
    };
    const L = computeLayout({
      W: W - noticeW, H, pageH, showPanel, showConsole, chrome,
      panelExtraH: readouts ? READOUTS_H : 0, dockExtraH: readouts ? READOUTS_DOCK_H : 0,
      fonts: this.console?.fontChoices() ?? [],
    });
    // both views are popped out: the two "bring back" notices side by side
    stage.dataset.layout = L.layout === 'none' && VIEW === 'main' ? 'away' : L.layout;
    document.body.classList.toggle('scroll', L.layout === 'mobile');
    if (L.panel && this.panel) {
      $('#panel-host').style.width = `${L.panel.w}px`;
      $('#panel-area').style.width = `${L.panel.w}px`;
      $('#landscape-hint').hidden = !(L.layout === 'mobile' && innerHeight > innerWidth);
      if (L.layout === 'wide') {
        stage.style.setProperty('--panel-w', `${L.panel.w}px`);
        stage.style.setProperty('--panel-h', `${L.panel.h + (readouts ? READOUTS_H : 0)}px`);
        stage.style.setProperty('--beside-panel', `${L.frame.w - L.panel.w}px`);
      }
    } else {
      $('#panel-area').style.width = ''; // in another window: the notice sizes the area
    }
    if (L.font && this.console) {
      this.console.fit(L.font);
      const frame = this.console.frame;
      // grows if a browser's terminal is wider than planned: the frame, not the text, gives way
      frame.style.width = 'max-content';
      frame.style.minWidth = `${L.frame.w}px`;
      frame.style.height = `${L.frame.h}px`;
      $('#console-area').style.width = `${Math.min(L.frame.w, W)}px`;
    } else if (this.console) {
      $('#console-area').style.width = ''; // in another window: the notice sizes the area
    }
    this.lastLayout = { ...L, W, H };
    this.layoutDock(this.lastLayout, stage, notice, readouts ? READOUTS_H : 0);
    if (L.font) this.centreTerminal(L);
    // xterm.js applies a new font size on its next render
    requestAnimationFrame(() => {
      this.ready.place();
      this.firstRun.place();
      this.tutorial.place();
    });
  }

  /**
   * The terminal in the middle of its frame, on whole device pixels: a font on
   * the pixel grid stays on it. Runs once the frame has its place on the page.
   */
  centreTerminal(L) {
    const frame = this.console.frame;
    const dpr = window.devicePixelRatio || 1;
    const at = frame.getBoundingClientRect();
    const padding = (start, free) => {
      const first = Math.round((start + FRAME_BORDER + free / 2) * dpr) / dpr - start - FRAME_BORDER;
      return [Math.max(0, first), Math.max(0, free - first)];
    };
    const [top, bottom] = padding(at.top, L.frame.h - L.font.h - 2 * FRAME_BORDER);
    const [left, right] = padding(at.left, L.frame.w - L.font.w - 2 * FRAME_BORDER);
    frame.style.padding = `${top}px ${right}px ${bottom}px ${left}px`;
  }

  barHeight() {
    return $('#console-head .head-bar')?.offsetHeight || 34;
  }

  /**
   * The zones of the page: the machine (panel and console) in the dark well
   * under the toolbar, the dock with its notes below it, reaching the window's
   * bottom. Text in the dock gives way when it has no room.
   */
  layoutDock(L, stage, notice, readoutsH) {
    // a notice beside the remaining view shares its height
    for (const n of [$('#panel-away'), $('#console-away')]) n.style.height = n === notice ? `${L.frame?.h ?? L.panel?.h}px` : '';
    const hasHead = CHROME && this.keyHelpEl && ['side', 'stack', 'console', 'wide'].includes(L.layout);
    const docked = CHROME && ['side', 'stack', 'console'].includes(L.layout);
    stage.classList.toggle('docked', docked);
    // the machine's margin grows with the height to spare (the dock takes the rest)
    const machineH = L.layout === 'stack' ? L.panel.h + readoutsH + GAP + L.frame.h : L.layout === 'side' ? L.panel.h : L.frame?.h ?? 0;
    let margin = docked ? Math.round(Math.max(0, Math.min(16, (L.H - machineH - this.barHeight() - BAR_MARGIN) * 0.05))) : 0;
    // a kiosk has no dock: the machine in the middle of the page, with its edges in line
    if (!CHROME && L.layout === 'side') margin = Math.max(0, Math.floor((L.H - machineH - (readoutsH ? READOUTS_DOCK_H : 0)) / 2));
    stage.style.padding = `${STAGE_PAD}px`;
    stage.style.paddingTop = `${STAGE_PAD + margin}px`;
    stage.style.setProperty('--dock-gap', `${margin}px`);
    if (docked) {
      const dockTop = (L.font ? this.console.frame : $('#panel-host')).getBoundingClientRect().bottom + margin + BAR_MARGIN;
      stage.style.setProperty('--dock-top', `${Math.round(dockTop - stage.getBoundingClientRect().top)}px`);
    }
    const head = $('#console-head');
    const bar = head.querySelector('.head-bar');
    bar?.classList.toggle('narrow', bar.offsetWidth < NARROW_BAR);
    // what does not fit under the bars gives way: the key help opens from its button then
    const bottom = L.layout === 'wide' ? head.getBoundingClientRect().bottom - 6 : window.innerHeight - STAGE_PAD;
    const fits = (el) => {
      el.classList.remove('gone');
      return el.getBoundingClientRect().bottom <= bottom;
    };
    if (hasHead) {
      const info = head.querySelector('.head-info');
      this.loginEl.classList.remove('gone');
      let ok = fits(info);
      if (!ok) {
        this.loginEl.classList.add('gone');
        ok = fits(info);
      }
      info.classList.toggle('gone', !ok);
      this.keysBtn.hidden = ok;
      this.tryEl.classList.remove('gone');
      this.tryEl.classList.toggle('gone', !ok || !this.tryEl.childElementCount || bottom - this.tryEl.getBoundingClientRect().top < MIN_NOTES_H);
    } else if (this.keysBtn) {
      // the page scrolls, or the view is alone: nothing needs to give way
      this.keysBtn.hidden = true;
      for (const el of [head.querySelector('.head-info'), this.loginEl, this.tryEl]) el.classList.remove('gone');
    }
    if (L.layout === 'side') {
      // in the order of importance: each stays only if it still fits under the ones before it
      for (const el of [$('#panel-notes'), $('#panel-keys'), $('#panel-about')]) el.classList.toggle('gone', !el.childNodes.length || !fits(el));
    }
    this.placeToasts();
  }

  /** The tutorial card's place: the dock under the panel, below its bar and readouts, or the card beside the panel. */
  tutorialDock() {
    const L = this.lastLayout;
    if (L?.layout === 'wide') {
      const head = $('#console-head').getBoundingClientRect();
      const top = $('#console-head .head-bar').getBoundingClientRect().bottom;
      return { left: head.left + 1, top, width: head.width - 2, height: head.bottom - top - 1 };
    }
    if (L?.layout !== 'side' || !$('#stage').classList.contains('docked')) return null;
    const panel = $('#panel-host').getBoundingClientRect();
    const r = $('#readouts');
    const top = (r.hidden ? $('#panel-bar') : r).getBoundingClientRect().bottom + 4;
    return { left: panel.left, top, width: panel.width, height: window.innerHeight - STAGE_PAD - top };
  }

  /** Where the tutorial card floats without a dock: over the console, which it may hide, as the panel is what it is about. */
  tutorialFloat() {
    const L = this.lastLayout;
    if (!L?.font || L.layout === 'mobile') return null;
    const f = this.console.frame.getBoundingClientRect();
    const top = Math.max(STAGE_PAD, f.top + 6);
    return { top, right: Math.max(STAGE_PAD, document.documentElement.clientWidth - f.right + 6), height: Math.min(f.bottom, window.innerHeight) - 6 - top };
  }

  /** Where toasts go: free space in the dock first, then places that hide the least (UI.md §2). */
  placeToasts() {
    const L = this.lastLayout;
    if (!L) return;
    const iw = document.documentElement.clientWidth;
    const ih = window.innerHeight;
    const m = TOAST_MARGIN;
    if (L.layout === 'mobile') {
      // the page scrolls and an on-screen keyboard covers the bottom
      this.toasts.setZones([{ top: m, right: m, width: iw - 2 * m, maxH: ih - 2 * m }]);
      return;
    }
    const zones = [];
    const rect = (e) => (e?.getClientRects().length ? e.getBoundingClientRect() : null);
    const frame = L.font ? rect(this.console.frame) : null;
    const panel = L.panel ? rect($('#panel-host')) : null;
    const under = (col, ...above) => ({
      bottom: m, right: Math.max(m, iw - col.right), width: col.width,
      maxH: ih - m - 6 - Math.max(col.bottom, ...above.filter(Boolean).map((r) => r.bottom)),
    });
    const beside = (col) => ({ top: col.top, right: m, width: iw - col.right - 2 * m, maxH: col.height });
    const banner = rect($('.ready-banner'));
    const headBar = rect($('#console-head .head-bar'));
    if (L.layout === 'wide') {
      // in the card beside the panel, over the things to try
      const card = rect($('#console-head'));
      const top = Math.max(headBar.bottom, rect($('#console-head .head-info'))?.bottom ?? 0, banner && banner.top < card.bottom ? banner.bottom : 0) + 6;
      if (!this.tutorial.isDocked()) zones.push({ top, right: iw - card.right + 10, width: card.width - 20, maxH: card.bottom - top - 6 });
    }
    if ($('#stage').classList.contains('docked')) {
      const dockLeft = panel && L.layout === 'side' && !this.tutorial.isDocked();
      // the dock's free space, then over its notes (they can wait, the toasts go away)
      if (dockLeft) zones.push(under(panel, rect($('#panel-bar')), rect($('#readouts')), rect($('#panel-notes')), rect($('#panel-keys')), rect($('#panel-about'))));
      if (frame) zones.push(under(frame, headBar, rect($('#console-head .head-info')), banner, rect($('.try-notes'))));
      if (dockLeft) zones.push(under(panel, rect($('#panel-bar')), rect($('#readouts'))));
      if (frame) zones.push(under(frame, headBar, rect($('#console-head .head-info')), banner));
    }
    // a margin beside the machine in a stacked layout
    if (panel && L.layout === 'stack') zones.push(beside(panel));
    if (frame && (L.layout === 'stack' || L.layout === 'wide')) zones.push(beside(frame));
    // the blank top right of the panel's bezel; the terminal's top, under a ready banner there
    if (panel) zones.push({ top: panel.top + 6, right: iw - panel.right + 6, width: panel.width * 0.34, maxH: panel.height * 0.12 });
    if (frame) {
      // (a kiosk has its "Click to type" pill at the frame's top)
      const top = Math.max(frame.top + (CHROME ? 0 : 28), banner && banner.top < frame.bottom ? banner.bottom : 0) + 6;
      zones.push({ top, right: iw - frame.right + 16, width: frame.width - 32, maxH: frame.bottom - top });
    }
    if (!frame && panel) zones.push({ top: panel.top + 6, right: iw - panel.right + 6, width: panel.width - 12, maxH: panel.height * 0.7 });
    if (!zones.length) zones.push({ bottom: m, right: m, width: iw - 2 * m, maxH: ih - 2 * m });
    this.toasts.setZones(zones);
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

/** A server's error message as a sentence: "The simulator…" for "the simulator…". */
function sentence(text) {
  return text.charAt(0).toUpperCase() + text.slice(1);
}

const app = new App();
app.start().catch((err) => {
  console.error(err);
  document.body.prepend(h('p', { class: 'banner banner-warn' }, `The UI failed to start: ${err.message}`));
});
