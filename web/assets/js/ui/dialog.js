// Modal dialogs on top of <dialog> (focus trap, Esc, inert background),
// plus the standard confirmations of UI.md §2.

import { h } from '../util.js';

/**
 * Opens a modal dialog and resolves with the value of the action chosen
 * (or `dismiss` when closed with Esc / the close button).
 *
 * @param {object} o
 * @param {string} o.title
 * @param {Node|string|(Node|string)[]} [o.body]
 * @param {{label: string, value: string, kind?: string, disabled?: boolean}[]} [o.actions]
 * @param {string} [o.dismiss]  value for Esc (default 'cancel')
 * @param {string} [o.focus]    value of the action that gets the focus (default: the primary one, else the first)
 * @param {string} [o.className]
 * @param {(dlg: HTMLDialogElement, close: (v: string) => void) => void} [o.onOpen]
 * @returns {{ promise: Promise<string>, dialog: HTMLDialogElement, close: (v: string) => void }}
 */
export function openDialog({ title, body, actions = [], dismiss = 'cancel', focus, className = '', onOpen } = {}) {
  const prevFocus = document.activeElement;
  let resolve;
  const promise = new Promise((r) => { resolve = r; });
  const titleId = `dlg-${Math.random().toString(36).slice(2)}`;
  const dlg = h('dialog', { class: `dialog ${className}`, 'aria-labelledby': titleId });
  const close = (v) => {
    if (!dlg.open) return;
    dlg.close();
    dlg.remove();
    resolve(v);
    if (prevFocus && document.contains(prevFocus)) prevFocus.focus?.({ preventScroll: true });
  };
  const header = h('header', { class: 'dialog-head' },
    h('h2', { id: titleId }, title),
    h('button', { type: 'button', class: 'icon-btn dialog-x', 'aria-label': 'Close', onclick: () => close(dismiss) }, '×'),
  );
  const content = h('div', { class: 'dialog-body' });
  if (body !== undefined && body !== null) content.append(...[body].flat());
  dlg.append(header, content);
  if (actions.length) {
    const foot = h('footer', { class: 'dialog-actions' });
    for (const a of actions) {
      foot.append(h('button', {
        type: 'button', class: `btn ${a.kind ? `btn-${a.kind}` : ''}`, disabled: !!a.disabled,
        'data-value': a.value, onclick: () => close(a.value),
      }, a.label));
    }
    dlg.append(foot);
  }
  dlg.addEventListener('cancel', (e) => {
    e.preventDefault();
    close(dismiss);
  });
  document.body.append(dlg);
  dlg.showModal();
  const first = (focus && dlg.querySelector(`.dialog-actions [data-value="${CSS.escape(focus)}"]:not([disabled])`))
    ?? dlg.querySelector('.dialog-actions .btn-primary:not([disabled])') ?? dlg.querySelector('.dialog-actions .btn:not([disabled])');
  first?.focus();
  onOpen?.(dlg, close);
  return { promise, dialog: dlg, close };
}

/** Simple yes/no confirmation. */
export function confirmDialog({ title, text, ok = 'OK', cancel = 'Cancel', danger = false }) {
  return openDialog({
    title,
    body: h('p', {}, text),
    actions: [
      { label: cancel, value: 'cancel' },
      { label: ok, value: 'ok', kind: danger ? 'danger' : 'primary' },
    ],
  }).promise.then((v) => v === 'ok');
}

/**
 * The OS is halted but still in memory: it reached its prompt and the CPU
 * stopped for a reason other than the OS halting itself (a HALT from the
 * panel, a single step). Powering off now loses what it has not written.
 */
export function osInMemory(st) {
  return st?.run === 'halted' && !!st.ready && st.console?.lastStop !== 'HALT instruction';
}

/**
 * "Power off / boot while running / switch systems" (UI.md §2): offers a
 * clean OS shutdown first. A system without a shutdown script (canShutdown
 * false, e.g. RT-11) gets only "Pull the plug" and "Cancel", with its
 * shutdown text as advice. An OS halted from the panel (osInMemory) gets
 * "Continue and shut down" (resolves 'resume'), with Cancel as the default.
 * Resolves 'shutdown' | 'plug' | 'cancel' | 'resume'.
 *
 * @param {object} o
 * @param {string} o.what        e.g. "Power off", "Boot", "Switch to RT-11"
 * @param {object} [o.system]    SystemInfo of the running system
 * @param {object} [o.state]     the state message (CPU running, halted with the OS in memory, ...)
 */
export function confirmStop({ what, system, state }) {
  const name = system?.name ?? 'the operating system';
  if (osInMemory(state)) {
    const locked = state.controls?.key === 'lock';
    const how = `${locked ? 'turn the key to POWER, ' : ''}set ENABLE/HALT to ENABLE and press CONT`;
    const body = [
      h('p', {}, `${name} is still in memory: the CPU was halted, not shut down. Turning it off now is like pulling the plug: the disks may need repair (fsck) at the next boot, and recent changes can be lost.`),
      h('p', {}, system?.canShutdown ? `To shut it down cleanly, ${how}, then use Shut down OS.` : `To stop it cleanly, ${how} and do what it needs on the console first.`),
    ];
    if (system?.shutdown) body.push(h('div', { class: 'shutdown-text' }, h('strong', {}, system.canShutdown ? 'Clean shutdown: ' : 'Before you switch off: '), system.shutdown));
    const actions = [
      { label: 'Cancel', value: 'cancel' },
      { label: 'Pull the plug', value: 'plug', kind: 'danger' },
    ];
    if (system?.canShutdown && !locked) actions.push({ label: 'Continue and shut down', value: 'resume', kind: 'primary' });
    return openDialog({ title: what, body, actions, focus: 'cancel' }).promise;
  }
  if (state?.run !== 'running') {
    return openDialog({
      title: what,
      body: h('p', {}, `${what}? The CPU is not running, so no operating system needs to be shut down.`),
      actions: [{ label: 'Cancel', value: 'cancel' }, { label: what, value: 'plug', kind: 'primary' }],
    }).promise;
  }
  if (!system?.canShutdown) {
    const body = [h('p', {}, `${name} is running and has no automatic shutdown: do what it needs on the console first, then pull the plug.`)];
    if (system?.shutdown) body.push(h('div', { class: 'shutdown-text' }, h('strong', {}, 'Before you switch off: '), system.shutdown));
    else body.push(h('p', {}, 'Switching off while a program writes to a disk can damage its files.'));
    return openDialog({
      title: what,
      body,
      actions: [
        { label: 'Cancel', value: 'cancel' },
        { label: 'Pull the plug', value: 'plug', kind: 'danger' },
      ],
    }).promise;
  }
  const body = [
    h('p', {}, `${name} is running. Turning it off without a clean shutdown is like pulling the plug: the disks may need repair (fsck) at the next boot, and recent changes can be lost.`),
  ];
  if (system.shutdown) body.push(h('div', { class: 'shutdown-text' }, h('strong', {}, 'Clean shutdown: '), system.shutdown));
  return openDialog({
    title: what,
    body,
    actions: [
      { label: 'Cancel', value: 'cancel' },
      { label: 'Pull the plug', value: 'plug', kind: 'danger' },
      { label: 'Shut down the OS first (recommended)', value: 'shutdown', kind: 'primary' },
    ],
  }).promise;
}
