// Shared test harness: mock server + headless Chromium via playwright-core.
//
// playwright-core is resolved from web/dev/node_modules, or from the
// directory in $PLAYWRIGHT_CORE (a node_modules dir or the package itself).
// The browser is $CHROMIUM, else the newest Playwright headless shell or
// Chromium under ~/.cache/ms-playwright. Tests are skipped when either is
// missing.
//
// The mock sends pdp11d's Content-Security-Policy; every page of a context
// made by newContext() reports violations (securitypolicyviolation events
// and CSP console errors), and closeBrowser() fails when there were any.

import { existsSync, readdirSync } from 'node:fs';
import { createRequire } from 'node:module';
import { homedir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { startMockServer } from '../mock-server.mjs';

const HERE = dirname(fileURLToPath(import.meta.url));

function loadPlaywright() {
  const tries = [];
  if (process.env.PLAYWRIGHT_CORE) {
    const p = process.env.PLAYWRIGHT_CORE;
    tries.push(p.endsWith('playwright-core') ? join(p, 'index.js') : join(p, 'playwright-core'));
  }
  tries.push(join(HERE, '..', 'node_modules', 'playwright-core'));
  for (const t of tries) {
    try {
      return createRequire(join(HERE, 'x.js'))(t);
    } catch { /* next */ }
  }
  return null;
}

function findChromium() {
  if (process.env.CHROMIUM && existsSync(process.env.CHROMIUM)) return process.env.CHROMIUM;
  const root = join(homedir(), '.cache', 'ms-playwright');
  if (!existsSync(root)) return null;
  const dirs = readdirSync(root).sort().reverse();
  for (const [prefix, rel] of [['chromium_headless_shell-', 'chrome-linux/headless_shell'], ['chromium-', 'chrome-linux/chrome']]) {
    for (const d of dirs) {
      if (!d.startsWith(prefix)) continue;
      for (const sub of [rel, rel.replace('chrome-linux', 'chrome-linux64')]) {
        const p = join(root, d, sub);
        if (existsSync(p)) return p;
      }
    }
  }
  return null;
}

export const pw = loadPlaywright();
export const chromiumPath = findChromium();
export const skip = !pw ? 'playwright-core not found (npm install in web/dev or set PLAYWRIGHT_CORE)'
  : !chromiumPath ? 'no Chromium found (set CHROMIUM)' : false;

let browserPromise = null;
export function browser() {
  browserPromise ??= pw.chromium.launch({ executablePath: chromiumPath, headless: true, args: ['--disable-gpu'] });
  return browserPromise;
}

const cspViolations = [];

/** A browser context whose pages report CSP violations (see closeBrowser). */
export async function newContext(opts = {}) {
  const ctx = await (await browser()).newContext(opts);
  await ctx.exposeBinding('__pdp11CSP', ({ page }, v) => cspViolations.push({ page: page?.url(), ...v }));
  await ctx.addInitScript(() => {
    document.addEventListener('securitypolicyviolation', (e) => {
      window.__pdp11CSP({ directive: e.violatedDirective, blocked: e.blockedURI, source: `${e.sourceFile}:${e.lineNumber}`, sample: e.sample });
    });
  });
  ctx.on('console', (m) => {
    if (m.type() === 'error' && /Content.Security.Policy/i.test(m.text())) cspViolations.push({ page: m.page()?.url(), console: m.text() });
  });
  return ctx;
}

/**
 * Closes the browser and any mock server a failed test left open (it would
 * keep node alive). Throws when a page violated the Content-Security-Policy.
 */
export async function closeBrowser() {
  const b = browserPromise;
  browserPromise = null;
  if (b) await (await b).close().catch(() => {});
  await Promise.all([...servers].map((srv) => srv.close().catch(() => {})));
  const seen = cspViolations.splice(0);
  if (seen.length) throw new Error(`Content-Security-Policy violations: ${JSON.stringify(seen)}`);
}

const servers = new Set();

/** Starts a mock server tuned for tests (fast installs and console pacing). */
export async function mock(opts = {}) {
  const srv = await startMockServer({ port: 0, quiet: true, cps: 5000, installMs: 600, ...opts });
  servers.add(srv);
  const close = srv.close.bind(srv);
  srv.close = async () => {
    if (!servers.delete(srv)) return;
    await close();
  };
  return srv;
}

// A network change on the host (a Docker container or a VPN starting)
// makes Chromium abort the requests in flight, also to 127.0.0.1, with
// net::ERR_NETWORK_CHANGED: a page that loads while it happens is loaded again.
const NETWORK_CHANGED = /net::ERR_NETWORK_CHANGED/;

/** Waits until the page is connected and has state: '' when it is, else why not. */
async function connected(page, timeout = 30000) {
  for (const t0 = Date.now(); ; await sleep(25)) {
    if (page.errors.some((e) => NETWORK_CHANGED.test(e))) return 'network changed';
    if (await page.evaluate(() => !!(window.pdp11Debug?.connected && window.pdp11Debug.state)).catch(() => false)) return '';
    if (Date.now() - t0 > timeout) return `not connected with state after ${timeout} ms`;
  }
}

/**
 * Opens a page on the mock and waits until it is connected and has state.
 * Collects console errors and page errors into page.errors.
 */
export async function openPage(srv, { path = '', width = 1600, height = 900, context } = {}) {
  const ctx = context ?? (await newContext({ viewport: { width, height } }));
  const page = await ctx.newPage();
  page.on('console', (m) => {
    if (m.type() === 'error') page.errors.push(`console.error: ${m.text()}`);
  });
  page.on('pageerror', (e) => page.errors.push(`pageerror: ${e.message}`));
  page.ctx = ctx;
  for (let attempt = 1; ; attempt++) {
    page.errors = [];
    const failed = await page.goto(srv.url + path).then(() => '', (err) => err.message);
    const why = NETWORK_CHANGED.test(failed) ? 'network changed' : failed || await connected(page);
    if (!why) return page;
    if (why === 'network changed' && attempt < 3) continue;
    // say why: no UI (start-up failed), no connection, or no state
    const state = await page.evaluate(() => ({
      ui: !!window.pdp11Debug, connected: window.pdp11Debug?.connected ?? null, state: !!window.pdp11Debug?.state,
      sent: window.pdp11Debug?.sent?.length ?? null, banner: document.querySelector('.banner')?.textContent ?? '',
    })).catch((e) => ({ evaluate: e.message }));
    if (!context) await ctx.close().catch(() => {});
    throw new Error(`${why} (attempt ${attempt}); page ${JSON.stringify(state)}; errors ${JSON.stringify(page.errors)}`);
  }
}

/** Messages the mock received from browsers (optionally filtered). */
export function received(srv, pred = () => true) {
  return srv.received.filter((r) => r.msg && pred(r.msg)).map((r) => r.msg);
}

export async function waitFor(fn, { timeout = 5000, interval = 25, message = 'condition' } = {}) {
  const t0 = Date.now();
  for (;;) {
    const v = await fn();
    if (v) return v;
    if (Date.now() - t0 > timeout) throw new Error(`timed out waiting for ${message}`);
    await new Promise((r) => setTimeout(r, interval));
  }
}

/** Centre of an element (page coordinates), optionally offset in fractions of its size. */
export async function centre(page, selector, fx = 0.5, fy = 0.5) {
  const box = await page.locator(selector).first().boundingBox();
  if (!box) throw new Error(`no box for ${selector}`);
  return { x: box.x + box.width * fx, y: box.y + box.height * fy, box };
}

export const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
