// Pop-out windows (UI.md §1): the panel or the console in a window of its
// own (each with its own WebSocket). The windows coordinate over the
// BroadcastChannel of prefs.js:
//   {type:'popout-open', view}    a pop-out exists (sent on load and on 'who')
//   {type:'popout-closed', view}  it went away
//   {type:'who'}                  main window (re)loaded: who is out there?
//   {type:'bring-back', view}     main window wants the view back

const FEATURES = {
  panel: 'popup,width=1300,height=760',
  console: 'popup,width=880,height=660',
};

export class Popouts {
  /**
   * @param {object} o
   * @param {import('../prefs.js').Prefs} o.prefs
   * @param {(view: string, away: boolean) => void} o.onAway  main window: show/hide the view
   * @param {(text: string, level?: string) => void} o.toast
   */
  constructor({ prefs, onAway, toast }) {
    this.prefs = prefs;
    this.onAway = onAway;
    this.toast = toast;
    this.wins = { panel: null, console: null };
    this.away = { panel: false, console: false };
    this.poll = 0;
  }

  /** Main window: listen for pop-outs, including ones that outlived a reload. */
  startMain() {
    this.prefs.on('message', (m) => {
      if (!m || (m.view !== 'panel' && m.view !== 'console')) return;
      if (m.type === 'popout-open') this.setAway(m.view, true);
      else if (m.type === 'popout-closed') this.setAway(m.view, false);
    });
    this.prefs.post({ type: 'who' });
    this.poll = setInterval(() => {
      for (const v of ['panel', 'console']) {
        if (this.wins[v] && this.wins[v].closed) {
          this.wins[v] = null;
          this.setAway(v, false);
        }
      }
    }, 1000);
  }

  setAway(view, away) {
    if (this.away[view] === away) return;
    this.away[view] = away;
    this.onAway(view, away);
  }

  /** Must run inside the click handler (pop-up blockers). */
  open(view) {
    let win = null;
    try {
      win = window.open(`?view=${view}`, `pdp11-${view}`, FEATURES[view]);
    } catch { /* blocked */ }
    if (!win) {
      this.toast('The browser blocked the pop-up window. Allow pop-ups for this site and try again.', 'warn');
      return false;
    }
    this.wins[view] = win;
    this.setAway(view, true);
    try {
      win.focus();
    } catch { /* cross-window focus can be refused */ }
    return true;
  }

  bringBack(view) {
    const win = this.wins[view];
    if (win && !win.closed) {
      try {
        win.close();
      } catch { /* not ours */ }
    }
    this.prefs.post({ type: 'bring-back', view });
    this.wins[view] = null;
    this.setAway(view, false);
  }

  /** Pop-out window: announce, answer 'who', close on 'bring-back'. */
  startPopout(view) {
    const announce = () => this.prefs.post({ type: 'popout-open', view });
    this.prefs.on('message', (m) => {
      if (m?.type === 'who') announce();
      else if (m?.type === 'bring-back' && m.view === view) window.close();
    });
    announce();
    window.addEventListener('pagehide', () => this.prefs.post({ type: 'popout-closed', view }));
  }
}
