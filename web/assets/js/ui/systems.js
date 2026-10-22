// System chooser and licence/install dialogs (UI.md §2).

import { fmtBytes, h } from '../util.js';
import { confirmDialog, openDialog } from './dialog.js';

const CLASS_LABEL = { redistributable: 'Free to use', hobbyist: 'Hobbyist licence' };

/** "Downloading 2.11BSD… 12 / 24 MB" style text for a progress message. */
export function progressText(p, sys) {
  const name = sys?.short || sys?.name || p.id;
  switch (p.phase) {
    case 'download':
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
      return p.text || 'Failed';
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
  app.send({ type: 'install', id: sys.id, ...(hobby ? { acceptLicense: true } : {}) });
  return true;
}

export class SystemChooser {
  constructor(app) {
    this.app = app;
    this.dlg = null;
  }

  open() {
    if (this.dlg) return;
    this.list = h('div', { class: 'sys-list' });
    const { promise, dialog } = openDialog({
      title: 'Operating systems',
      body: [h('p', { class: 'muted' }, 'Disk images are downloaded from their original sources the first time a system is installed.'), this.list],
      className: 'wide',
      actions: [{ label: 'Close', value: 'close' }],
      dismiss: 'close',
    });
    this.dlg = dialog;
    this.sig = '';
    promise.then(() => { this.dlg = null; });
    this.refresh();
  }

  close() {
    this.dlg?.querySelector('[data-value="close"]')?.click();
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
    const active = (p) => p && !['done', 'error', 'cancelled'].includes(p.phase);
    const sig = JSON.stringify({
      c: app.connected, sel: app.systems?.selected, run: st?.run, key: st?.controls?.key,
      list: (app.systems?.list ?? []).map((x) => [x.id, x.installed, x.installing, x.speed, x.name,
        !!app.progressFor('install', x.id), active(app.progressFor('install', x.id)) || active(app.progressFor('factoryReset', x.id))]),
    });
    if (sig === this.sig) {
      for (const sys of app.systems?.list ?? []) this.updateProgress(sys);
      return;
    }
    this.sig = sig;
    this.rebuild();
  }

  progressOf(sys) {
    return this.app.progressFor('install', sys.id) ?? this.app.progressFor('factoryReset', sys.id);
  }

  updateProgress(sys) {
    const card = this.list.querySelector(`.sys-card[data-id="${CSS.escape(sys.id)}"]`);
    const box = card?.querySelector('.sys-progress');
    const prog = this.progressOf(sys);
    if (!box) return;
    box.textContent = '';
    if (prog) box.append(progressBar(prog), h('div', { class: 'progress-text' }, progressText(prog, sys)));
  }

  rebuild() {
    const app = this.app;
    const st = app.state;
    const selected = app.systems?.selected;
    const offOrError = !st || st.run === 'off' || st.run === 'error';
    const focusedId = document.activeElement?.closest?.('.sys-card')?.dataset.id;
    const focusedAction = document.activeElement?.dataset?.action;
    this.list.textContent = '';
    for (const sys of app.systems?.list ?? []) {
      const prog = this.progressOf(sys);
      const isSel = sys.id === selected;
      const busy = sys.installing || (prog && !['done', 'error', 'cancelled'].includes(prog.phase));
      const badges = h('div', { class: 'meta' },
        h('span', { class: `badge ${sys.license?.class === 'redistributable' ? 'badge-ok' : 'badge-warn'}` }, CLASS_LABEL[sys.license?.class] ?? sys.license?.class ?? 'licence?'),
        h('span', { class: `badge ${sys.installed ? 'badge-ok' : busy ? 'badge-info' : 'badge-muted'}` }, busy ? 'Installing…' : sys.installed ? 'Installed' : 'Not installed'),
        isSel ? h('span', { class: 'badge badge-info' }, 'Selected') : null,
        sys.default ? h('span', { class: 'badge badge-muted' }, 'Default') : null,
      );
      const actions = h('div', { class: 'actions' });
      const act = (label, action, fn, { kind = '', disabled = false, title = '' } = {}) => {
        actions.append(h('button', { type: 'button', class: `btn ${kind}`, 'data-action': action, disabled, title: title || null, onclick: fn }, label));
      };
      if (!isSel) {
        act('Select', 'select', () => app.selectSystem(sys.id), {
          kind: 'btn-primary', disabled: !app.connected || st?.controls?.key === 'lock',
          title: st?.controls?.key === 'lock' ? 'Unlock the console (key switch) first' : '',
        });
      }
      if (busy) {
        act('Cancel', 'cancel', () => app.send({ type: 'cancel', task: prog?.task === 'factoryReset' ? 'factoryReset' : 'install', id: sys.id }), { kind: 'btn-danger' });
      } else if (!sys.installed) {
        act(sys.license?.class === 'redistributable' ? 'Install' : 'Licence and install', 'install', () => installDialog(app, sys), { disabled: !app.connected });
      } else {
        act('Factory reset', 'reset', () => this.factoryReset(sys), {
          disabled: !app.connected || !isSel || !offOrError,
          title: !isSel ? 'Select the system first' : !offOrError ? 'Power off first' : 'Restore the original disk images',
        });
      }
      const card = h('div', { class: 'sys-card', 'data-id': sys.id, 'data-selected': String(isSel) },
        h('h3', {}, sys.name),
        badges,
        h('p', { class: 'desc' }, sys.description || ''),
        h('div', { class: 'meta muted' },
          sys.downloadBytes ? h('span', {}, `Download ${fmtBytes(sys.downloadBytes)}`) : null,
          sys.diskBytes ? h('span', {}, `· Disk ${fmtBytes(sys.diskBytes)}`) : null,
          sys.speed ? h('span', {}, `· ${sys.speed === 'fast' ? 'Fast' : 'Authentic'} speed`) : null),
        h('div', { class: 'creds' }, credentialsText(sys)),
        h('div', { class: 'sys-progress' }),
        actions,
      );
      this.list.append(card);
      this.updateProgress(sys);
    }
    if (focusedId) {
      const card = this.list.querySelector(`.sys-card[data-id="${CSS.escape(focusedId)}"]`);
      (card?.querySelector(`[data-action="${focusedAction}"]:not([disabled])`) ?? card?.querySelector('button:not([disabled])'))?.focus();
    }
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
