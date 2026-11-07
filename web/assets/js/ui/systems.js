// System chooser and licence/install dialogs (UI.md §2).

import { copyText, fmtBytes, h } from '../util.js';
import { confirmDialog, openDialog } from './dialog.js';
import { inline } from './markdown.js';

const CLASS_LABEL = { redistributable: 'Free to use', hobbyist: 'Hobbyist licence' };
const FINAL = ['done', 'error', 'cancelled'];

/** "Downloading 2.11BSD… 12 / 24 MB" style text for a progress message. */
export function progressText(p, sys) {
  const name = sys?.short || sys?.name || p.id;
  switch (p.phase) {
    case 'download':
      // no bytes yet: the server says what the task waits for
      if (!p.bytes && p.text) return p.text;
      if (!p.bytes) return `Downloading ${name}…`;
      return p.total > 0
        ? `Downloading ${name}… ${fmtBytes(p.bytes)} / ${fmtBytes(p.total)}`
        : `Downloading ${name}… ${fmtBytes(p.bytes)}`;
    case 'verify':
      return `Verifying ${name}…`;
    case 'extract':
      return `Unpacking ${name}…`;
    case 'step':
      return p.text || `${p.task === 'shutdown' ? 'Shutting down' : 'Working on'} ${name}…`;
    case 'done':
      return p.task === 'factoryReset' ? `${name} restored to factory state` : p.task === 'shutdown' ? `${name} shut down` : `${name} installed`;
    case 'cancelled':
      return 'Cancelled';
    case 'error':
      return p.failure?.summary || p.text || 'Failed';
    default:
      return p.text || p.phase;
  }
}

export function progressBar(p) {
  const pct = Number.isFinite(p?.pct) && p.pct > 0 ? Math.min(100, p.pct) : null;
  const bar = h('div', { class: `progress${pct === null ? ' indeterminate' : ''}`, role: 'progressbar', 'aria-valuemin': 0, 'aria-valuemax': 100, 'aria-valuenow': pct ?? null },
    h('span', { style: pct === null ? {} : { width: `${pct}%` } }));
  return bar;
}

/**
 * The console needs no login (RT-11, RSX-11M-PLUS V3.0): the catalog's
 * noLogin, or no credentials when the server does not send noLogin yet.
 */
export function noLogin(sys) {
  if (typeof sys?.noLogin === 'boolean') return sys.noLogin;
  return !sys?.credentials?.length;
}

/** "root (no password)", "1,2 / SYSTEM": one credential as text. */
export function credentialText(c) {
  return `${c.user}${c.password ? ` / ${c.password}` : ' (no password)'}`;
}

function credentialsText(sys) {
  if (noLogin(sys) || !sys.credentials?.length) return h('span', {}, 'No login needed');
  return h('span', {}, 'Log in as ', ...sys.credentials.flatMap((c, i) => [
    i ? ', ' : '', h('code', {}, c.user), c.password ? [' / ', h('code', {}, c.password)] : ' (no password)',
  ].flat()));
}

/**
 * What happened, what to do and the error itself, for a failed install or
 * factory reset (the chooser's card and the first-run card).
 *
 * @param {object} failure  TaskFailure (PROTOCOL.md §1.2)
 * @param {object} o
 * @param {string} o.title
 * @param {(Node|null)[]} [o.actions]  buttons under the text
 * @param {(() => void)} [o.onDismiss] puts a close button in the title row
 */
export function failureView(failure, { title, actions = [], onDismiss }) {
  const detail = failure.detail ?? '';
  const copy = h('button', {
    type: 'button', class: 'link-btn',
    onclick: () => { copy.textContent = copyText(detail) ? 'Copied' : 'Copy failed'; },
  }, 'Copy');
  return h('div', { class: 'failure', role: 'group', 'aria-label': title },
    h('div', { class: 'failure-title' }, h('strong', {}, title),
      onDismiss ? h('button', { type: 'button', class: 'icon-btn failure-x', 'aria-label': 'Dismiss', onclick: onDismiss }, '×') : null),
    failure.summary ? h('p', { class: 'failure-what' }, failure.summary) : null,
    failure.hint ? h('p', { class: 'failure-do' }, ...inline(failure.hint)) : null,
    actions.some(Boolean) ? h('div', { class: 'actions' }, ...actions) : null,
    detail ? h('details', { class: 'failure-detail' },
      h('summary', {}, 'Technical details'),
      h('pre', {}, detail),
      copy) : null);
}

/** Installs again without asking again for what was accepted (the licence, in this page). */
export function retryInstall(app, sys) {
  if (sys.license?.class === 'redistributable' || app.licenceAccepted.has(sys.id)) {
    app.send({ type: 'install', id: sys.id, ...(app.licenceAccepted.has(sys.id) ? { acceptLicense: true } : {}) });
    return;
  }
  installDialog(app, sys);
}

/**
 * Licence acceptance and install. Resolves true when an install request was sent.
 */
export async function installDialog(app, sys) {
  const hobby = sys.license?.class !== 'redistributable';
  const body = [
    h('p', {}, `${sys.name}: ${sys.description || ''}`),
    h('dl', { class: 'kv' },
      h('dt', {}, 'Download'), h('dd', {}, sys.downloadBytes ? fmtBytes(sys.downloadBytes) : 'unknown'),
      h('dt', {}, 'Disk space'), h('dd', {}, sys.diskBytes ? fmtBytes(sys.diskBytes) : 'unknown'),
      h('dt', {}, 'Licence'), h('dd', {}, `${sys.license?.name || '?'} (${CLASS_LABEL[sys.license?.class] ?? sys.license?.class ?? '?'})`),
    ),
  ];
  if (sys.license?.summary) body.push(h('p', { class: 'muted' }, sys.license.summary));
  let accept = null;
  if (hobby) {
    body.push(h('div', { class: 'licence-text', tabindex: 0, 'aria-label': 'Licence text' }, sys.license?.text || 'Licence text not available.'));
    accept = h('input', { type: 'checkbox', id: 'accept-licence' });
    body.push(h('label', { class: 'check', for: 'accept-licence' }, accept,
      h('span', {}, `I have read and accept the ${sys.license?.name || 'licence'} (personal, non-commercial use).`)));
  }
  const { promise, dialog } = openDialog({
    title: `Install ${sys.name}`,
    body,
    className: hobby ? 'wide' : '',
    actions: [
      { label: 'Cancel', value: 'cancel' },
      { label: hobby ? 'Accept and install' : 'Install', value: 'install', kind: 'primary', disabled: hobby },
    ],
  });
  if (accept) {
    const btn = dialog.querySelector('[data-value="install"]');
    accept.addEventListener('change', () => { btn.disabled = !accept.checked; });
  }
  const v = await promise;
  if (v !== 'install') return false;
  if (hobby) app.licenceAccepted.add(sys.id);
  app.send({ type: 'install', id: sys.id, ...(hobby ? { acceptLicense: true } : {}) });
  return true;
}

export class SystemChooser {
  constructor(app) {
    this.app = app;
    this.dlg = null;
    this.expanded = new Set(); // cards whose whole description shows
    this.failed = new Set(); // cards that showed a failure at the last rebuild
  }

  /** @param {string} [focusId] system whose card gets the view (a failure's toast leads there) */
  open(focusId) {
    if (!this.dlg) {
      this.list = h('div', { class: 'sys-list' });
      this.notice = h('p', { class: 'notice', hidden: true });
      this.live = h('p', { class: 'sr-only', role: 'status' });
      const { promise, dialog, close } = openDialog({
        title: 'Operating systems',
        body: [this.notice, this.live, this.list],
        note: 'Disk images are downloaded from their original sources the first time a system is installed.',
        className: 'chooser',
        actions: [{ label: 'Close', value: 'close' }],
        dismiss: 'close',
      });
      this.dlg = dialog;
      this.closeDialog = close;
      this.sig = '';
      this.measure = new ResizeObserver(() => this.markClamped());
      this.measure.observe(this.list);
      promise.then(() => {
        this.dlg = null;
        this.measure.disconnect();
      });
      this.refresh();
    }
    if (focusId) {
      const card = this.list.querySelector(`.sys-card[data-id="${CSS.escape(focusId)}"]`);
      card?.scrollIntoView({ block: 'nearest' });
      card?.querySelector('.actions .btn:not([disabled])')?.focus({ preventScroll: true });
    }
  }

  close() {
    this.closeDialog?.('close');
  }

  isOpen() {
    return !!this.dlg;
  }

  /** Tells screen readers what just changed in a card. */
  announce(text) {
    if (this.live) this.live.textContent = text;
  }

  /**
   * Re-renders the cards. Cards are rebuilt only when something that changes
   * their structure changed (a rebuild under the pointer would swallow
   * clicks); progress alone is updated in place.
   */
  refresh() {
    if (!this.dlg) return;
    const app = this.app;
    const st = app.state;
    const sig = JSON.stringify({
      c: app.connected, sel: app.systems?.selected, run: st?.run, key: st?.controls?.key,
      list: (app.systems?.list ?? []).map((x) => [x.id, x.installed, x.installing, x.task, x.speed, x.name,
        !!this.progressOf(x), this.isBusy(x, this.progressOf(x)), app.failureOf(x)?.detail]),
    });
    if (sig === this.sig) {
      for (const sys of app.systems?.list ?? []) this.updateProgress(sys);
      return;
    }
    this.sig = sig;
    this.rebuild();
  }

  /** The newest progress of a system's install or factory reset, while it matters. */
  progressOf(sys) {
    const install = this.app.progressFor('install', sys.id);
    const reset = this.app.progressFor('factoryReset', sys.id);
    const running = (p) => p && !FINAL.includes(p.phase);
    return (running(reset) && reset) || (running(install) && install) || install || reset;
  }

  isBusy(sys, prog) {
    return !!sys.installing || (!!prog && !FINAL.includes(prog.phase));
  }

  updateProgress(sys) {
    const card = this.list.querySelector(`.sys-card[data-id="${CSS.escape(sys.id)}"]`);
    const box = card?.querySelector('.sys-progress');
    const prog = this.progressOf(sys);
    if (!box) return;
    const live = prog && !FINAL.includes(prog.phase) ? prog : null;
    const busy = this.isBusy(sys, prog);
    // a finished task leaves its result for a moment; a failure has its own block
    const text = live ? progressText(live, sys)
      : busy ? `${sys.task === 'factoryReset' ? 'Restoring' : 'Preparing'} ${sys.short || sys.name}…`
        : prog && prog.phase !== 'error' ? progressText(prog, sys) : '';
    box.textContent = '';
    if (busy) box.append(progressBar(live));
    if (text) box.append(h('div', { class: 'progress-text', title: text }, text));
    box.hidden = !text;
    card.querySelector('.sys-info').hidden = !box.hidden;
  }

  rebuild() {
    const app = this.app;
    const st = app.state;
    const selected = app.systems?.selected;
    const locked = st?.controls?.key === 'lock';
    const offOrError = !st || st.run === 'off' || st.run === 'error';
    const focusedId = document.activeElement?.closest?.('.sys-card')?.dataset.id;
    const focusedAction = document.activeElement?.dataset?.action;
    this.notice.hidden = app.connected && !locked;
    this.list.classList.toggle('short', !this.notice.hidden);
    this.notice.textContent = !app.connected ? 'Not connected to pdp11d: nothing can be changed until it is back.'
      : locked ? 'The key is at LOCK, so you cannot switch systems. Turn the key to POWER or OFF first.' : '';
    this.list.textContent = '';
    // a card with a failure comes first, in view: it needs the whole row and the user's attention
    const list = app.systems?.list ?? [];
    const failureOf = (s) => (this.isBusy(s, this.progressOf(s)) ? null : app.failureOf(s));
    const failed = list.filter(failureOf);
    if (failed.some((s) => !this.failed.has(s.id))) this.list.closest('.dialog-body').scrollTop = 0;
    this.failed = new Set(failed.map((s) => s.id));
    for (const sys of [...failed, ...list.filter((s) => !failureOf(s))]) {
      const prog = this.progressOf(sys);
      const isSel = sys.id === selected;
      const busy = this.isBusy(sys, prog);
      const resetting = busy && (sys.task ?? (prog?.task || 'install')) === 'factoryReset';
      const failure = failureOf(sys);
      const stateBadge = busy ? ['badge-info', resetting ? 'Resetting…' : 'Installing…']
        : failure && !sys.installed ? ['badge-danger', 'Install failed']
          : sys.installed ? ['badge-ok', 'Installed'] : ['badge-muted', 'Not installed'];
      const badges = h('div', { class: 'meta badges' },
        h('span', { class: `badge ${sys.license?.class === 'redistributable' ? 'badge-ok' : 'badge-warn'}` }, CLASS_LABEL[sys.license?.class] ?? sys.license?.class ?? 'licence?'),
        h('span', { class: `badge ${stateBadge[0]}` }, stateBadge[1]),
        isSel ? h('span', { class: 'badge badge-info' }, 'Selected') : null,
      );
      const btn = (label, action, fn, { kind = '', disabled = false, title = '' } = {}) => h('button', {
        type: 'button', class: `btn ${kind}`, 'data-action': action, disabled, title: title || null, onclick: fn,
      }, label);
      const select = (kind) => (isSel ? null : btn('Select', 'select', () => app.selectSystem(sys.id), {
        kind, disabled: !app.connected || locked,
        title: locked ? 'Unlock the console (key switch) first' : !sys.installed ? 'Make this the current system; it is installed when the machine powers on' : '',
      }));
      const install = () => btn(sys.license?.class === 'redistributable' ? 'Install' : 'Licence and install', 'install', () => installDialog(app, sys), { kind: 'btn-primary', disabled: !app.connected });
      const main = h('div', { class: 'actions' });
      if (busy) {
        main.append(btn('Cancel', 'cancel', () => app.send({ type: 'cancel', task: resetting ? 'factoryReset' : 'install', id: sys.id }), { kind: 'btn-danger' }));
      } else if (!sys.installed) {
        if (!failure) main.append(install());
        const s = select('');
        if (s && !failure) main.append(s);
      } else {
        const s = select('btn-primary');
        if (s) main.append(s);
        // only the selected system can run: a system that is not selected can always be reset
        const why = !isSel ? '' : !offOrError ? 'Power off first' : locked ? 'Turn the key first' : '';
        const reset = h('span', { class: 'reset' },
          why ? h('span', { class: 'why' }, why) : null,
          btn('Factory reset', 'reset', () => this.factoryReset(sys), {
            kind: 'btn-quiet', disabled: !app.connected || !!why, title: why || 'Restore the original disk images',
          }));
        main.append(reset);
      }
      const expanded = this.expanded.has(sys.id);
      const more = h('button', {
        type: 'button', class: 'link-btn more', 'data-action': 'more', 'aria-expanded': String(expanded), hidden: !expanded,
        onclick: () => this.toggleMore(sys.id),
      }, expanded ? 'Less' : 'More');
      // a group with the system's name: every card has its Select and Install
      const card = h('div', { class: `sys-card${expanded ? ' expanded' : ''}${failure ? ' failed' : ''}`, role: 'group', 'aria-label': sys.name, 'data-id': sys.id, 'data-selected': String(isSel) },
        h('h3', {}, sys.name),
        badges,
        h('div', { class: 'desc-wrap' }, h('div', { class: 'desc-box' }, h('p', { class: 'desc' }, sys.description || ''), more)),
        h('div', { class: 'sys-slot' },
          h('div', { class: 'sys-info' },
            h('div', { class: 'meta muted' }, ...[
              sys.downloadBytes ? `Download ${fmtBytes(sys.downloadBytes)}` : null,
              sys.diskBytes ? `Disk ${fmtBytes(sys.diskBytes)}` : null,
            ].filter(Boolean).map((t) => h('span', {}, t))),
            h('div', { class: 'meta muted creds' }, credentialsText(sys),
              sys.speed ? h('span', {}, `${sys.speed === 'fast' ? 'Fast' : 'Authentic'} speed`) : null)),
          h('div', { class: 'sys-progress', hidden: true })),
        failure ? failureView(failure, {
          title: failure.task === 'factoryReset' ? 'The factory reset failed' : 'The installation failed',
          onDismiss: () => { app.dismissFailure(sys); },
          actions: sys.installed ? [] : [
            btn('Try again', 'retry', () => retryInstall(app, sys), { kind: 'btn-primary', disabled: !app.connected }),
            select(''),
          ],
        }) : null,
        main.childElementCount ? main : null,
      );
      this.list.append(card);
      this.updateProgress(sys);
    }
    if (focusedId) {
      const card = this.list.querySelector(`.sys-card[data-id="${CSS.escape(focusedId)}"]`);
      (card?.querySelector(`[data-action="${focusedAction}"]:not([disabled])`) ?? card?.querySelector('button:not([disabled])'))?.focus();
    }
    this.markClamped();
  }

  /** "More" only on descriptions the card cuts off. */
  markClamped() {
    for (const card of this.list.querySelectorAll('.sys-card:not(.expanded)')) {
      const desc = card.querySelector('.desc');
      card.querySelector('.more').hidden = desc.scrollHeight <= desc.clientHeight + 1;
    }
  }

  toggleMore(id) {
    if (this.expanded.has(id)) this.expanded.delete(id);
    else this.expanded.add(id);
    const card = this.list.querySelector(`.sys-card[data-id="${CSS.escape(id)}"]`);
    const on = this.expanded.has(id);
    card.classList.toggle('expanded', on);
    const more = card.querySelector('.more');
    more.textContent = on ? 'Less' : 'More';
    more.setAttribute('aria-expanded', String(on));
    more.hidden = false;
    this.markClamped();
  }

  async factoryReset(sys) {
    const ok = await confirmDialog({
      title: `Factory reset ${sys.name}`,
      text: `This discards every change made on ${sys.name}'s disks (files, accounts, configuration) and restores the original images.`,
      ok: 'Discard changes and reset',
      danger: true,
    });
    if (ok) this.app.send({ type: 'factoryReset', id: sys.id });
  }
}
