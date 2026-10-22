// Configuration and tool discovery for the browser end-to-end tests against
// the real stack (pdp11d + patched SimH + the web UI from this checkout).
// Everything the tests start is placed in one work directory and uses its
// own listen address, console port and panel file, so several runs (or other
// pdp11d instances) can share a host.

import { execFileSync } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, readdirSync } from 'node:fs';
import { createRequire } from 'node:module';
import { createServer } from 'node:net';
import { homedir, tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = dirname(fileURLToPath(import.meta.url));
export const REPO = resolve(HERE, '..', '..', '..');

/** playwright-core from $PLAYWRIGHT_CORE (a node_modules dir or the package), tests/e2e or web/dev. */
function loadPlaywright() {
  const tries = [];
  const p = process.env.PLAYWRIGHT_CORE;
  if (p) tries.push(p.replace(/\/$/, '').endsWith('playwright-core') ? p : join(p, 'playwright-core'));
  tries.push(join(HERE, '..', 'node_modules', 'playwright-core'), join(REPO, 'web', 'dev', 'node_modules', 'playwright-core'));
  const req = createRequire(join(HERE, 'x.js'));
  for (const t of tries) {
    try {
      return req(t);
    } catch { /* next */ }
  }
  return null;
}

/** $CHROMIUM, else the newest Playwright headless shell or Chromium under ~/.cache/ms-playwright. */
function findChromium() {
  if (process.env.CHROMIUM) return existsSync(process.env.CHROMIUM) ? process.env.CHROMIUM : null;
  const root = join(homedir(), '.cache', 'ms-playwright');
  if (!existsSync(root)) return null;
  const dirs = readdirSync(root).sort().reverse();
  for (const [prefix, rel] of [['chromium_headless_shell-', 'chrome-linux/headless_shell'], ['chromium-', 'chrome-linux/chrome']]) {
    for (const d of dirs) {
      if (!d.startsWith(prefix)) continue;
      for (const sub of [rel, rel.replace('chrome-linux', 'chrome-linux64')]) {
        const f = join(root, d, sub);
        if (existsSync(f)) return f;
      }
    }
  }
  return null;
}

function findGo() {
  const go = process.env.GO || 'go';
  try {
    execFileSync(go, ['version'], { stdio: 'ignore' });
    return go;
  } catch {
    return null;
  }
}

export const pw = loadPlaywright();
export const chromiumPath = findChromium();
export const goPath = findGo();
export const simhPath = process.env.PDP11_SIMH || join(REPO, 'simh', 'out', 'pdp11');

/** Why the suite cannot run here (node:test skip reason), or false. */
export const skip = Number(process.versions.node.split('.')[0]) < 20 ? 'needs Node 20 or newer'
  : !pw ? 'playwright-core not found (set PLAYWRIGHT_CORE to a node_modules dir that has it)'
    : !chromiumPath ? 'no Chromium found (set CHROMIUM, or install a Playwright Chromium in ~/.cache/ms-playwright)'
      : !goPath ? 'go not found (set GO)'
        : !existsSync(simhPath) ? `patched SimH not found at ${simhPath} (run simh/build.sh or set PDP11_SIMH)`
          : process.platform !== 'linux' ? 'pdp11d runs on Linux only'
            : false;

/** A free TCP port on 127.0.0.1 (for defaults; explicit settings win). */
export function freePort() {
  return new Promise((res, rej) => {
    const s = createServer();
    s.once('error', rej);
    s.listen(0, '127.0.0.1', () => {
      const { port } = s.address();
      s.close(() => res(port));
    });
  });
}

/**
 * The run's settings (env overrides):
 *   E2E_WORK       work dir (binary, data, panel file, logs); default: a new temp dir
 *   E2E_SHOTS      screenshots (default $E2E_WORK/shots)
 *   E2E_LISTEN     pdp11d listen address, 127.0.0.1 only (default: a free port)
 *   E2E_PORT_BASE  SimH console port (default: a free port)
 *   E2E_SHM        panel export file (default $E2E_WORK/shm/panel)
 *   PDP11_CACHE    read-only archive cache(s), ':'-separated (default: none: download)
 *   PDP11_SIMH     the patched simulator (default simh/out/pdp11)
 */
export async function settings() {
  const work = process.env.E2E_WORK ? resolve(process.env.E2E_WORK) : mkdtempSync(join(tmpdir(), 'pdp11-e2e-'));
  mkdirSync(work, { recursive: true });
  const listen = process.env.E2E_LISTEN || `127.0.0.1:${await freePort()}`;
  if (!/^127\.0\.0\.1:\d+$/.test(listen)) throw new Error(`E2E_LISTEN must be 127.0.0.1:<port>, not ${listen}`);
  const portBase = Number(process.env.E2E_PORT_BASE || (await freePort()));
  const shm = process.env.E2E_SHM ? resolve(process.env.E2E_SHM) : join(work, 'shm', 'panel');
  const shots = process.env.E2E_SHOTS ? resolve(process.env.E2E_SHOTS) : join(work, 'shots');
  for (const d of [dirname(shm), shots]) mkdirSync(d, { recursive: true });
  return {
    work, listen, portBase, shm, shots,
    url: `http://${listen}/`,
    data: join(work, 'data'),
    bin: join(work, 'pdp11d'),
    log: join(work, 'pdp11d.log'),
    cache: process.env.PDP11_CACHE || '',
    simh: simhPath,
    web: join(REPO, 'web'),
  };
}
