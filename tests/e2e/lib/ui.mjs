// Browser helpers for the e2e tests: headless Chromium (playwright-core),
// the UI's window.pdp11Debug hooks, typing into the xterm.js console, real
// mouse clicks on the front panel, lamp frame decoding and the record of
// Content-Security-Policy violations.

import { join } from 'node:path';
import { chromiumPath, pw } from './env.mjs';

export const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

/** Lamp indexes (PROTOCOL.md §1.1). */
export const LAMP = { A0: 0, D0: 22, RUN: 42, KNOBS: 52 };

let browserPromise = null;
export function browser() {
  browserPromise ??= pw.chromium.launch({ executablePath: chromiumPath, headless: true, args: ['--disable-gpu'] });
  return browserPromise;
}

export async function closeBrowser() {
  if (browserPromise) await (await browserPromise).close().catch(() => {});
  browserPromise = null;
}

export async function waitFor(fn, { timeout = 10000, interval = 50, message = 'condition' } = {}) {
  const t0 = Date.now();
  let last;
  for (;;) {
    try {
      const v = await fn();
      if (v) return v;
    } catch (err) {
      last = err;
    }
    if (Date.now() - t0 > timeout) throw new Error(`timed out after ${timeout} ms waiting for ${message}${last ? ` (last error: ${last.message})` : ''}`);
    await sleep(interval);
  }
}

/** Collects console errors and page errors of a page into page.errors. */
export function watchErrors(page) {
  page.errors = [];
  page.on('console', (m) => {
    if (m.type() === 'error') page.errors.push(`console.error: ${m.text()}`);
  });
  page.on('pageerror', (e) => page.errors.push(`pageerror: ${e.message}`));
  return page;
}

/**
 * Records every Content-Security-Policy violation in the pages of ctx
 * (securitypolicyviolation events and CSP console errors); returns the list.
 */
export async function watchCSP(ctx) {
  const seen = [];
  await ctx.exposeBinding('__pdp11CSP', ({ page }, v) => seen.push({ page: page?.url(), ...v }));
  await ctx.addInitScript(() => {
    document.addEventListener('securitypolicyviolation', (e) => {
      window.__pdp11CSP({ directive: e.violatedDirective, blocked: e.blockedURI, source: `${e.sourceFile}:${e.lineNumber}`, sample: e.sample });
    });
  });
  ctx.on('console', (m) => {
    if (m.type() === 'error' && /Content.Security.Policy/i.test(m.text())) seen.push({ page: m.page()?.url(), console: m.text() });
  });
  return seen;
}

// A network change on the host (a Docker container or a VPN starting) makes
// Chromium abort the requests in flight, also to 127.0.0.1, with
// net::ERR_NETWORK_CHANGED: a page that loads while it happens is loaded again.
const NETWORK_CHANGED = /net::ERR_NETWORK_CHANGED/;

/** Opens url in a new page of ctx; waits for pdp11Debug unless wait is false. */
export async function openPage(ctx, url, { wait = true } = {}) {
  const page = watchErrors(await ctx.newPage());
  for (let attempt = 1; ; attempt++) {
    page.errors.length = 0;
    try {
      await page.goto(url);
      for (const t0 = Date.now(); wait; await sleep(50)) {
        if (page.errors.some((e) => NETWORK_CHANGED.test(e))) throw new Error('net::ERR_NETWORK_CHANGED while loading');
        if (await page.evaluate(() => !!(window.pdp11Debug?.connected && window.pdp11Debug.state)).catch(() => false)) break;
        if (Date.now() - t0 > 30000) throw new Error(`${url}: not connected with state after 30 s`);
      }
      return page;
    } catch (err) {
      const changed = NETWORK_CHANGED.test(err.message) || page.errors.some((e) => NETWORK_CHANGED.test(e));
      if (!changed || attempt === 3) throw err;
    }
  }
}

export const state = (page) => page.evaluate(() => window.pdp11Debug.state);
export const consoleText = (page) => page.evaluate(() => window.pdp11Debug.consoleText());
export const lastFrame = (page) => page.evaluate(() => {
  const f = window.pdp11Debug.lastFrame;
  return f ? Array.from(f) : null;
});

/** ADDRESS and DATA lamp values of a frame (a lamp counts as lit at >= 50 % duty). */
export function lampBits(f) {
  let address = 0;
  let data = 0;
  for (let i = 0; i < 22; i++) if (f[LAMP.A0 + i] >= 128) address += 2 ** i;
  for (let i = 0; i < 16; i++) if (f[LAMP.D0 + i] >= 128) data |= 1 << i;
  return { address, data };
}

/**
 * Waits until pred(state, arg) holds in the page. pred runs in the page, so it
 * cannot use closure variables: pass them as opts.arg.
 */
export async function waitState(page, pred, { arg = null, ...opts } = {}) {
  const src = pred.toString();
  return waitFor(() => page.evaluate(([s, a]) => {
    // eslint-disable-next-line no-new-func
    const f = new Function('st', 'arg', `return (${s})(st, arg)`);
    const st = window.pdp11Debug.state;
    return !!st && f(st, a);
  }, [src, arg]), opts);
}

/** Console lines without trailing blanks (scrollback included). */
export async function consoleLines(page) {
  return (await consoleText(page)).split('\n');
}

export async function waitConsole(page, re, opts = {}) {
  return waitFor(async () => re.test(await consoleText(page)), { message: `console text ${re}`, ...opts });
}

/** The last non-empty console line is a 2.11BSD root prompt ("# "). */
export async function waitPrompt(page, opts = {}) {
  return waitFor(async () => {
    const lines = (await consoleLines(page)).filter((l) => l.trim() !== '');
    return lines.at(-1)?.trimEnd() === '#';
  }, { message: 'the # prompt', ...opts });
}

/** Clicks into the terminal (keyboard focus) and types a line, Enter included. */
export async function typeLine(page, text) {
  await page.click('.console-screen');
  await page.waitForFunction(() => document.activeElement?.classList.contains('xterm-helper-textarea'));
  await page.keyboard.type(text, { delay: 15 });
  await page.keyboard.press('Enter');
}

/** Output lines of the last "# cmd" in the console, up to the next prompt. */
export async function outputOf(page, cmd) {
  const lines = await consoleLines(page);
  let i = -1;
  for (let k = lines.length - 1; k >= 0; k--) {
    if (lines[k].trimEnd() === `# ${cmd}`) {
      i = k;
      break;
    }
  }
  if (i < 0) return null;
  const out = [];
  for (let k = i + 1; k < lines.length; k++) {
    if (lines[k].trimEnd() === '#' || lines[k].startsWith('# ')) break;
    out.push(lines[k]);
  }
  return out;
}

/** Centre of a hit element, at fractions of its box. */
export async function point(page, selector, fx = 0.5, fy = 0.5) {
  const box = await page.locator(selector).first().boundingBox();
  if (!box) throw new Error(`no box for ${selector}`);
  return { x: box.x + box.width * fx, y: box.y + box.height * fy };
}

/** A real mouse click on a paddle (SR, latch) of the main panel. */
export async function clickPaddle(page, name) {
  const p = await point(page, `#panel-host .hit-paddle[data-name="${name}"]`, 0.5, 0.75);
  await page.mouse.click(p.x, p.y);
}

/** Presses and releases a momentary paddle with the mouse (held ms). */
export async function pressPaddle(page, name, ms = 150) {
  const p = await point(page, `#panel-host .hit-paddle[data-name="${name}"]`, 0.5, 0.75);
  await page.mouse.move(p.x, p.y);
  await page.mouse.down();
  await sleep(ms);
  await page.mouse.up();
}

/** Turns the key with a click: 'ccw' (left half) or 'cw' (right half). */
export async function clickKey(page, dir) {
  const p = await point(page, '#panel-host .hit-key', dir === 'ccw' ? 0.2 : 0.8, 0.5);
  await page.mouse.click(p.x, p.y);
}

/** Sets the switch register with the panel's keyboard shortcuts (Esc, then octal digits). */
export async function typeSR(page, value) {
  await page.focus('#panel-host .hit-paddle[data-name="SR0"]');
  await page.keyboard.press('Escape');
  for (const d of value.toString(8)) await page.keyboard.press(d);
  await waitState(page, (st, v) => st.controls.sr === v, { arg: value, message: `SR = ${value.toString(8)}` });
}

/** Saves a screenshot to the run's shots dir; returns its path. */
export async function shot(page, dir, name, opts = {}) {
  const path = join(dir, `${name}.png`);
  if (opts.selector) await page.locator(opts.selector).first().screenshot({ path, animations: 'disabled' });
  else await page.screenshot({ path, fullPage: false, animations: 'disabled', ...opts });
  return path;
}
