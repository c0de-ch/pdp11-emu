// Small shared helpers: DOM construction, octal/byte formatting, events.

/**
 * Creates an HTML element. attrs: class, text, html (trusted markup only),
 * dataset, style, on<event> handlers, any other attribute; children are
 * nodes or strings (strings become text nodes).
 */
export function h(tag, attrs = {}, ...children) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs ?? {})) {
    if (v === null || v === undefined || v === false) continue;
    if (k === 'class') e.className = v;
    else if (k === 'text') e.textContent = v;
    else if (k === 'dataset') Object.assign(e.dataset, v);
    else if (k === 'style' && typeof v === 'object') Object.assign(e.style, v);
    else if (k.startsWith('on') && typeof v === 'function') e.addEventListener(k.slice(2), v);
    else if (v === true) e.setAttribute(k, '');
    else e.setAttribute(k, String(v));
  }
  for (const c of children.flat()) {
    if (c === null || c === undefined || c === false) continue;
    e.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return e;
}

export const $ = (sel, root = document) => root.querySelector(sel);

/** Zero-padded octal. */
export function octal(v, digits) {
  return (v >>> 0).toString(8).padStart(digits, '0');
}

/** Human-readable byte size (decimal units, like download sizes). */
export function fmtBytes(n) {
  if (!Number.isFinite(n) || n <= 0) return '0 B';
  const u = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  let v = n;
  while (v >= 1000 && i < u.length - 1) {
    v /= 1000;
    i++;
  }
  return `${v >= 100 || i === 0 ? Math.round(v) : v.toFixed(1)} ${u[i]}`;
}

/** Minimal typed event emitter. */
export class Emitter {
  constructor() {
    this.handlers = new Map();
  }

  on(type, fn) {
    if (!this.handlers.has(type)) this.handlers.set(type, new Set());
    this.handlers.get(type).add(fn);
    return () => this.handlers.get(type)?.delete(fn);
  }

  emit(type, ...args) {
    for (const fn of [...(this.handlers.get(type) ?? [])]) {
      try {
        fn(...args);
      } catch (err) {
        console.error(`handler for "${type}" failed`, err);
      }
    }
  }
}

/**
 * Copies text to the clipboard through the DOM copy event: works without a
 * secure context (plain HTTP), unlike navigator.clipboard. Must run in a user
 * gesture. Returns false when the browser refused.
 */
export function copyText(text) {
  if (!text) return false;
  const onCopy = (e) => {
    e.clipboardData.setData('text/plain', text);
    e.preventDefault();
    e.stopImmediatePropagation();
  };
  document.addEventListener('copy', onCopy, { capture: true, once: true });
  let ok = false;
  try {
    ok = document.execCommand('copy');
  } catch { /* not allowed */ }
  document.removeEventListener('copy', onCopy, { capture: true });
  return ok;
}

/** Correlation ids for requests ("req"), unique per page. */
let reqSeq = 0;
export function newReq(prefix = 'r') {
  reqSeq += 1;
  return `${prefix}${Date.now().toString(36)}${reqSeq}`;
}

export const isMac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);
