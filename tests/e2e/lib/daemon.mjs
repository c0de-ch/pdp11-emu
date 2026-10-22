// Builds pdp11d from this checkout and runs it on the e2e work directory:
// the UI is served from web/ on disk (PDP11_WEB), archives come from
// PDP11_CACHE when set, and the console port, panel file and listen address
// are the run's own. stop() sends SIGTERM and makes sure the simulator is
// gone too.

import { execFile, spawn } from 'node:child_process';
import { closeSync, existsSync, openSync, readFileSync, readlinkSync, writeSync } from 'node:fs';
import { join } from 'node:path';
import { promisify } from 'node:util';
import { REPO, goPath } from './env.mjs';

const run = promisify(execFile);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

/**
 * go build -o <bin> ./server/cmd/pdp11d. Other work on the same tree may
 * leave it briefly uncompilable: retries (E2E_BUILD_RETRIES, default 3,
 * 30 s apart) before giving up.
 */
export async function buildDaemon(bin) {
  const retries = Number(process.env.E2E_BUILD_RETRIES ?? 3);
  let last;
  for (let i = 0; i <= retries; i++) {
    try {
      const t0 = Date.now();
      await run(goPath, ['build', '-o', bin, './server/cmd/pdp11d'], { cwd: REPO, maxBuffer: 16 << 20 });
      return Date.now() - t0;
    } catch (err) {
      last = err;
      if (i < retries) await sleep(30000);
    }
  }
  throw new Error(`go build ./server/cmd/pdp11d failed: ${last.stderr || last.message}`);
}

/** Environment of the daemon: only what the run sets (no PDP11_* from the caller). */
function daemonEnv(cfg, extra) {
  const env = {};
  for (const [k, v] of Object.entries(process.env)) if (!k.startsWith('PDP11_')) env[k] = v;
  return {
    ...env,
    PDP11_LISTEN: cfg.listen,
    PDP11_DATA: cfg.data,
    PDP11_SIMH: cfg.simh,
    PDP11_CACHE: cfg.cache,
    PDP11_WEB: cfg.web,
    PDP11_PORT_BASE: String(cfg.portBase),
    PDP11_SHM: cfg.shm,
    PDP11_SHUTDOWN_ON_STOP: '0',
    PDP11_AUTOSTART: 'true',
    PDP11_LOG: 'debug',
    ...extra,
  };
}

export class Daemon {
  constructor(cfg) {
    this.cfg = cfg;
    this.proc = null;
    this.exited = null;
    this.starts = 0;
  }

  /** Starts pdp11d serve; resolves when api/status answers. */
  async start(extraEnv = {}) {
    if (this.proc) throw new Error('pdp11d is already running');
    const fd = openSync(this.cfg.log, 'a');
    writeSync(fd, `\n===== e2e: pdp11d start #${++this.starts} at ${new Date().toISOString()}\n`);
    const proc = spawn(this.cfg.bin, ['serve'], { env: daemonEnv(this.cfg, extraEnv), stdio: ['ignore', fd, fd], cwd: this.cfg.work });
    closeSync(fd);
    this.proc = proc;
    this.exited = new Promise((res) => proc.once('exit', (code, signal) => res({ code, signal })));
    this.exited.then(() => { if (this.proc === proc) this.proc = null; });
    const t0 = Date.now();
    for (;;) {
      if (!this.proc) throw new Error(`pdp11d exited at start; see ${this.cfg.log}`);
      try {
        const r = await fetch(`${this.cfg.url}api/status`);
        if (r.ok) return Date.now() - t0;
      } catch { /* not listening yet */ }
      if (Date.now() - t0 > 15000) throw new Error(`pdp11d does not answer on ${this.cfg.url}; see ${this.cfg.log}`);
      await sleep(50);
    }
  }

  /** SIGTERM, wait for the exit (SIGKILL after timeoutMs); the simulator must be gone too. */
  async stop(timeoutMs = 60000) {
    const proc = this.proc;
    if (proc) {
      proc.kill('SIGTERM');
      const t = setTimeout(() => proc.kill('SIGKILL'), timeoutMs);
      const ex = await this.exited;
      clearTimeout(t);
      this.lastExit = ex;
    }
    await this.reapSimulator();
    return this.lastExit;
  }

  /** The simulator of this data dir, if one is still running: pid, else 0. */
  simulatorPid() {
    const pidFile = join(this.cfg.data, 'run', 'pdp11.pid');
    if (!existsSync(pidFile)) return 0;
    const pid = Number(readFileSync(pidFile, 'utf8').trim());
    if (!pid) return 0;
    try {
      return readlinkSync(`/proc/${pid}/exe`).replace(/ \(deleted\)$/, '') === this.cfg.simh ? pid : 0;
    } catch {
      return 0;
    }
  }

  /** Kills a simulator pdp11d left behind (it should never). Returns its pid or 0. */
  async reapSimulator() {
    const pid = this.simulatorPid();
    if (!pid) return 0;
    for (let i = 0; i < 30 && this.simulatorPid(); i++) await sleep(100); // pdp11d may still be stopping it
    if (!this.simulatorPid()) return 0;
    try {
      process.kill(pid, 'SIGKILL');
    } catch { /* gone */ }
    this.leftover = pid;
    return pid;
  }

  async json(path) {
    const r = await fetch(`${this.cfg.url}${path}`);
    if (!r.ok) throw new Error(`${path}: HTTP ${r.status}`);
    return r.json();
  }
}
