// Drop-down menus anchored to a button (settings, send key), with
// keyboard navigation (arrows, Home/End, Esc) and click-outside to close.

import { h } from '../util.js';

/**
 * @param {HTMLElement} anchor
 * @param {() => Array<object>} items  built when opened: {title}, {sep}, or
 *        {label, onSelect, checked?: boolean, role?: 'menuitemradio'|'menuitemcheckbox', disabled?, keep?,
 *        refocus?: false (onSelect moves the focus itself: closing leaves it there)}
 */
export function attachMenu(anchor, items) {
  let menu = null;
  anchor.setAttribute('aria-haspopup', 'menu');
  anchor.setAttribute('aria-expanded', 'false');
  const close = (refocus = true) => {
    if (!menu) return;
    menu.remove();
    menu = null;
    anchor.setAttribute('aria-expanded', 'false');
    document.removeEventListener('pointerdown', outside, true);
    window.removeEventListener('resize', onResize);
    if (refocus) anchor.focus({ preventScroll: true });
  };
  const outside = (e) => {
    if (menu && !menu.contains(e.target) && !anchor.contains(e.target)) close(false);
  };
  const onResize = () => close(false);
  const open = () => {
    menu = h('div', { class: 'menu', role: 'menu' });
    for (const it of items()) {
      if (it.title) menu.append(h('div', { class: 'menu-title', role: 'presentation' }, it.title));
      else if (it.sep) menu.append(h('div', { class: 'menu-sep', role: 'separator' }));
      else {
        const role = it.role ?? (it.checked === undefined ? 'menuitem' : 'menuitemcheckbox');
        const b = h('button', {
          type: 'button', class: 'menu-item', role, disabled: !!it.disabled,
          'aria-checked': it.checked === undefined ? null : String(!!it.checked),
          onclick: () => {
            it.onSelect?.();
            if (it.keep) {
              const fresh = items().find((x) => x.label === it.label);
              if (fresh?.checked !== undefined) b.setAttribute('aria-checked', String(!!fresh.checked));
            } else {
              close(it.refocus !== false);
            }
          },
        }, it.label, it.hint ? h('span', { class: 'muted' }, it.hint) : null);
        menu.append(b);
      }
    }
    menu.addEventListener('keydown', (e) => {
      const btns = [...menu.querySelectorAll('.menu-item:not([disabled])')];
      const i = btns.indexOf(document.activeElement);
      if (e.key === 'ArrowDown') btns[(i + 1) % btns.length]?.focus();
      else if (e.key === 'ArrowUp') btns[(i - 1 + btns.length) % btns.length]?.focus();
      else if (e.key === 'Home') btns[0]?.focus();
      else if (e.key === 'End') btns[btns.length - 1]?.focus();
      else if (e.key === 'Escape' || e.key === 'Tab') close(e.key === 'Escape');
      else return;
      e.preventDefault();
    });
    document.body.append(menu);
    const r = anchor.getBoundingClientRect();
    const m = menu.getBoundingClientRect();
    menu.style.top = `${Math.min(r.bottom + 4, innerHeight - m.height - 4)}px`;
    menu.style.left = `${Math.max(4, Math.min(r.right - m.width, innerWidth - m.width - 4))}px`;
    anchor.setAttribute('aria-expanded', 'true');
    document.addEventListener('pointerdown', outside, true);
    window.addEventListener('resize', onResize);
    menu.querySelector('.menu-item:not([disabled])')?.focus();
  };
  anchor.addEventListener('click', () => (menu ? close() : open()));
  anchor.addEventListener('keydown', (e) => {
    if (e.key === 'ArrowDown' && !menu) {
      e.preventDefault();
      open();
    }
  });
  return { close, isOpen: () => !!menu };
}
