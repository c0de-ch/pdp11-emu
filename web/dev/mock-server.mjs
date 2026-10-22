#!/usr/bin/env node
// Mock pdp11d for UI development and tests: serves the real web/ files and
// speaks the browser protocol (docs/PROTOCOL.md) with a fake machine:
// synthetic lamp frames (2.11BSD/RSX-like idle patterns following
// docs/PANEL_LOGIC.md §4), a fake console (boot transcript, login, a tiny
// shell), a systems list with simulated installs, and the panel's control
// echo and console-switch semantics (LOAD ADRS, EXAM, DEP, CONT, START).
//
// Usage: node web/dev/mock-server.mjs [options]
//   --port N          HTTP port (default 8080; 0 = random)
//   --host H          listen address (default 127.0.0.1)
//   --base /p/        serve under a sub-path (reverse-proxy test), default /
//   --first-run       the default system is not installed: auto-install, then boot
//   --off             start powered off (key OFF)
//   --insecure        hello.insecure = true (no-password banner)
//   --frame-hz N      lamp frame rate (default 60)
//   --cps N           console input pacing, chars per second (default 100)
//   --version V       server version reported in hello (default: index.html as served)
//   --system ID       select this system at start (installed)
//   --boot-hang       boots never reach the system's prompt (boot-timeout toast)
//   --boot-timeout N  every system's bootTimeoutSec (default: per system, 30-90 s)
//   --legacy          omit noLogin and bootTimeoutSec from the systems list (older pdp11d)
//   --no-autostart    key at POWER but not powered on at start (PDP11_AUTOSTART=false)
//   --quiet           no request log
//
// Every HTTP response carries pdp11d's security headers (CSP, X-Frame-Options;
// DESIGN.md §10), so the UI tests run under the same policy.
//
// Test hooks (development only): GET /mock/received, POST /mock/clear,
// POST /mock/console {text}, POST /mock/state {...}, POST /mock/toast
// {level,text}, POST /mock/version {version}, POST /mock/drop, POST /mock/query,
// POST /mock/log {lines}, POST /mock/hang {hang}, POST /mock/query {seq} (a
// terminal query, default DA), POST /mock/query-after-history, POST
// /mock/refuse {n} (the next n WebSocket handshakes get HTTP 401), GET
// /mock/machine (includes statusRequests, the GET api/status count, and
// guestInput, the last 256 KiB of console input the guest received).
// Requires Node 20 and "ws" (npm install in web/dev).

import { createServer } from 'node:http';
import { readFile, stat } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import { dirname, extname, join, normalize, sep } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { WebSocketServer } from 'ws';

const HERE = dirname(fileURLToPath(import.meta.url));
const WEB = join(HERE, '..');

// ------------------------------------------------------------------ constants

const L = {
  A0: 0, D0: 22, PARITY_LOW: 38, PARITY_HIGH: 39, PAR_ERR: 40, ADRS_ERR: 41, RUN: 42, PAUSE: 43, MASTER: 44,
  USER: 45, SUPER: 46, KERNEL: 47, DATA: 48, ADRS16: 49, ADRS18: 50, ADRS22: 51,
};
const ADDR_KNOB_LAMP = [59, 55, 54, 53, 52, 56, 57, 58]; // PROG PHY, CONS PHY, KERNEL D, SUPER D, USER D, USER I, SUPER I, KERNEL I
const DATA_KNOB_LAMP = [61, 60, 62, 63]; // BUS REG, DATA PATHS, uADRS, DISPLAY REGISTER
const MOMENTARY = ['LOAD_ADRS', 'EXAM', 'DEP', 'CONT', 'START', 'LAMP_TEST'];
const HISTORY_MAX = 256 * 1024;
const LOG_HISTORY = 1000; // log lines sent as the history of the "log" channel (like pdp11d)
const SECURITY_HEADERS = {
  'Content-Security-Policy': "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; "
    + "font-src 'self' data:; connect-src 'self' ws: wss:; frame-ancestors 'self'; object-src 'none'; base-uri 'self'; form-action 'self'",
  'X-Frame-Options': 'SAMEORIGIN',
  'X-Content-Type-Options': 'nosniff',
  'Referrer-Policy': 'same-origin',
};

const TYPES = {
  '.html': 'text/html; charset=utf-8', '.js': 'text/javascript; charset=utf-8', '.mjs': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8', '.json': 'application/json', '.svg': 'image/svg+xml', '.png': 'image/png',
  '.woff2': 'font/woff2', '.txt': 'text/plain; charset=utf-8', '.ico': 'image/x-icon',
};

const NOTES_BSD = `## Log in

Log in as **root** (no password). Other accounts: \`user\`, \`tektronix\`.

## Things to try

- \`ls -l /\`, \`ps ax\`, \`who\`
- Watch the idle pattern: DATA knob on **DATA PATHS** shows the rotating band in R0.
- \`cc hello.c\` builds a C program.

## Shut down

Type \`shutdown -h now\` and wait for **halting**, or use *Shut down OS*.`;

function mockSystems() {
  const sys = (o) => ({
    category: 'unix', default: false, installed: false, installing: false, credentials: [], noLogin: false, shutdown: '', canShutdown: false,
    notes: '## Log in\n\nNo login needed.\n\n## Things to try\n\n- Look around.\n\n## Shut down\n\nJust power off.',
    speed: 'fast', bootTimeoutSec: 60, console: { backspace: 'del', interrupt: 'ctrl-c' }, ...o,
  });
  return [
    sys({
      id: 'bsd211', name: '2.11BSD (patch level 482)', short: '2.11BSD', order: 10, default: true, installed: true,
      description: 'Berkeley Unix for the PDP-11: multi-user, TCP/IP, cc, vi and tcsh. Boots to login: in about 10 seconds.',
      downloadBytes: 47_946_752, diskBytes: 1_024_458_752,
      license: { name: 'BSD and Caldera licence', class: 'redistributable', summary: 'Free to use and redistribute.', text: '' },
      credentials: [{ user: 'root', password: '' }, { user: 'user', password: '' }], bootTimeoutSec: 90,
      shutdown: 'Log in as root and type shutdown -h now; wait for "halting".', canShutdown: true, notes: NOTES_BSD,
      console: { backspace: 'del', interrupt: 'ctrl-c', keyHelp: 'Backspace erases (DEL), Ctrl+C interrupts, Ctrl+U kills the line' },
    }),
    sys({
      id: 'unix-v7', name: 'Research Unix V7', short: 'Unix V7', order: 20,
      description: 'The classic Seventh Edition Unix (1979). Boots in 2 seconds; the DISPLAY REGISTER shows the word the switches select.',
      downloadBytes: 4_200_000, diskBytes: 87_960_576,
      license: { name: 'Caldera licence', class: 'redistributable', summary: 'Free to use and redistribute.', text: '' },
      credentials: [{ user: 'root', password: 'root' }], bootTimeoutSec: 30,
      shutdown: 'Type sync three times, then power off.', canShutdown: true,
      console: { backspace: '#', interrupt: 'del', keyHelp: 'Erase is #, kill is @; Ctrl+C sends DEL (interrupt)' },
    }),
    sys({
      id: 'rt11', name: 'RT-11 V5.3', short: 'RT-11', category: 'dec', order: 30, speed: 'authentic',
      description: 'DEC\'s single-user real-time system with BASIC, FOCAL and games. Never idles, so it runs at authentic speed.',
      downloadBytes: 960_000, diskBytes: 10_485_760,
      license: { name: 'Mentec hobbyist licence', class: 'hobbyist', summary: 'Personal, non-commercial use only.', text: 'MENTEC HOBBYIST LICENSE (mock text)\n\n1. You may use this software for personal, non-commercial purposes only.\n2. No warranty.\n' },
      noLogin: true, credentials: null,
      shutdown: 'RT-11 needs no shutdown: it writes directories immediately. Make sure no program is running (the "." prompt is showing) and switch off.', canShutdown: false,
      notes: '## Log in\n\nNo login: the console shows the `.` prompt.\n\n## Things to try\n\n- `DIR`, `SHOW CONFIG`\n\n## Shut down\n\nNothing to shut down.',
      console: { backspace: 'del', interrupt: 'ctrl-c', keyHelp: 'Backspace (DEL) erases · Ctrl+U deletes the line · Ctrl+C twice stops a program' },
    }),
    sys({
      id: 'rsx11mp', name: 'RSX-11M-PLUS V3.0', short: 'RSX-11M+', category: 'dec', order: 40,
      description: 'DEC\'s multi-user real-time OS with the famous idle pattern on the DATA lamps.',
      downloadBytes: 13_200_000, diskBytes: 174_000_000,
      license: { name: 'Mentec hobbyist licence', class: 'hobbyist', summary: 'Personal, non-commercial use only.', text: 'MENTEC HOBBYIST LICENSE (mock text)\n\nPersonal use only.\n' },
      noLogin: true, credentials: null,
      shutdown: 'RUN $SHUTUP, answer the questions.', canShutdown: true,
      console: { backspace: 'del', interrupt: 'ctrl-c', keyHelp: 'Ctrl+C gets MCR>, Ctrl+U kills the line' },
    }),
  ];
}

const BSD_BOOT = [
  [300, '\r\n73Boot from ra(0,0,0) at 0172150\r\n: '],
  [500, 'ra(0,0,0)unix\r\nBoot: bootdev=02400 bootcsr=0172150\r\n\r\n'],
  [400, '2.11 BSD UNIX #19: Thu Jan  1 12:00:00 PST 2026\r\n    root@pidp11:/usr/src/sys/PIDP\r\n\r\n'],
  [200, 'phys mem  = 4186112\r\navail mem = 3717888\r\nuser mem  = 307200\r\n\r\n'],
  [300, 'ra0: Ver 3 mod 3\r\nra0: RA82  size=2027520\r\n\r\n'],
  [600, 'Automatic reboot in progress...\r\n/dev/ra0a: 1039 files, 9452 used, 4547 free\r\n'],
  [400, 'Starting network daemons: routed inetd.\r\nstarting system logger\r\nchecking quotas: done.\r\n'],
  [300, 'starting local daemons: sendmail.\r\nThu Jan  1 12:00:30 PST 2026\r\n\r\n\r\n'],
  [200, '2.11 BSD UNIX (pdp11) (console)\r\n\r\nlogin: '],
];

const RT11_BOOT = [
  [300, '\r\nRT-11FB  V05.03  \r\n\r\n'],
  [300, '.TIME \r\n12:00:00\r\n\r\n'],
  [200, '.'],
];

const RSX_BOOT = [
  [300, '\r\nRSX-11M-PLUS V3.0  BL24   1920K   MAPPED\r\n'],
  [300, '>RED DU0:=SY:\r\n>RED DU0:=LB:\r\n>MOU DU0:/NOSHARE\r\n'],
  [300, '>@DU0:[1,2]STARTUP\r\n>; ... startup ...\r\n>@ <EOF>\r\n'],
  [200, '>'],
];

const V7_BOOT = [
  [200, '\r\nboot\r\n: hp(0,0)unix\r\nmem = 2020544\r\n'],
  [300, '# \r\nRESTRICTED RIGHTS\r\n\r\n'],
  [200, 'login: '],
];

/** Per system: boot transcript (the last entry is the prompt), state after it, shell prompt. */
const PROFILES = {
  bsd211: { boot: BSD_BOOT, after: 'login', prompt: '# ' },
  'unix-v7': { boot: V7_BOOT, after: 'login', prompt: '# ' },
  rt11: { boot: RT11_BOOT, after: 'shell', prompt: '.' },
  rsx11mp: { boot: RSX_BOOT, after: 'shell', prompt: '>' },
};

// ------------------------------------------------------------------ fake console OS

class FakeOS {
  constructor(m) {
    this.m = m;
    this.state = 'down'; // down | booting | login | password | shell | query
    this.line = '';
    this.timers = [];
  }

  out(s) {
    this.m.consoleOut(s);
  }

  stop() {
    for (const t of this.timers) clearTimeout(t);
    this.timers = [];
    this.state = 'down';
  }

  /** Boots system id; with hang the transcript stops before the prompt (never ready). */
  boot(id, { hang = false } = {}) {
    this.stop();
    this.profile = PROFILES[id] ?? PROFILES.bsd211;
    this.state = 'booting';
    const steps = hang ? this.profile.boot.slice(0, -1) : this.profile.boot;
    let t = 0;
    steps.forEach(([dt, text], i) => {
      t += dt;
      this.timers.push(setTimeout(() => {
        this.out(text);
        if (!hang && i === steps.length - 1) {
          this.state = this.profile.after;
          this.m.setReady();
        }
      }, t));
    });
    if (hang) this.timers.push(setTimeout(() => this.out('panic: mock boot hang\r\n'), t + 200));
  }

  prompt() {
    this.out(this.state === 'shell' ? this.profile?.prompt ?? '# ' : 'login: ');
  }

  input(c) {
    if (this.state === 'down' || this.state === 'booting') return;
    if (this.state === 'query') {
      this.reply += String.fromCharCode(c);
      if (/\x1b\[[?>]?[\d;]*c$/.test(this.reply)) this.finishQuery();
      return;
    }
    const echo = this.state !== 'password';
    if (c === 0x7f || c === 0x08) {
      if (this.line.length) {
        this.line = this.line.slice(0, -1);
        if (echo) this.out('\b \b');
      }
      return;
    }
    if (c === 0x15) { // ^U
      this.line = '';
      this.out('^U\r\n');
      this.prompt();
      return;
    }
    if (c === 0x03) { // ^C
      this.line = '';
      this.out('^C\r\n');
      this.prompt();
      return;
    }
    if (c === 0x0d || c === 0x0a) {
      this.out('\r\n');
      const line = this.line;
      this.line = '';
      this.enter(line.trim());
      return;
    }
    if (c >= 0x20 && c < 0x7f) {
      this.line += String.fromCharCode(c);
      if (echo) this.out(String.fromCharCode(c));
    }
  }

  enter(line) {
    if (this.state === 'login') {
      if (!line) return this.prompt();
      if (line === 'root' || line === 'user') {
        this.user = line;
        this.out('\r\n2.11 BSD UNIX #19: mock session\r\n\r\n');
        this.state = 'shell';
      } else {
        this.out('Password:');
        this.state = 'password';
        return;
      }
      return this.prompt();
    }
    if (this.state === 'password') {
      this.out('\r\nLogin incorrect\r\n');
      this.state = 'login';
      return this.prompt();
    }
    // shell
    const [cmd, ...args] = line.split(/\s+/);
    switch (cmd) {
      case '':
        break;
      case 'ls':
        this.out('bin     boot    dev     etc     home    lib     mdec    sys     tmp     unix    usr\r\n');
        break;
      case 'date':
        this.out(`${new Date().toUTCString()}\r\n`);
        break;
      case 'uname':
        this.out('2.11BSD pdp11 mock\r\n');
        break;
      case 'who':
        this.out(`${this.user ?? 'root'}     console Jan  1 12:01\r\n`);
        break;
      case 'echo':
        this.out(`${args.join(' ')}\r\n`);
        break;
      case 'bell':
        this.out('\x07');
        break;
      case 'tset':
        // terminal query: only the answering browser may reply (PROTOCOL §1.4)
        this.state = 'query';
        this.reply = '';
        this.out('\x1b[c');
        this.timers.push(setTimeout(() => this.finishQuery(), 1500));
        return;
      case 'yes': {
        let n = 0;
        const spam = () => {
          this.out('y\r\n');
          if (++n < 200) this.timers.push(setTimeout(spam, 5));
          else this.prompt();
        };
        spam();
        return;
      }
      case 'shutdown':
        this.shutdown();
        return;
      case 'exit':
      case 'logout':
        this.state = 'login';
        this.out('\r\n\r\n2.11 BSD UNIX (pdp11) (console)\r\n\r\n');
        return this.prompt();
      default:
        this.out(`${cmd}: Command not found.\r\n`);
    }
    this.prompt();
  }

  finishQuery() {
    if (this.state !== 'query') return;
    this.state = 'shell';
    const r = this.reply ?? '';
    this.out(r ? `Terminal answered ${JSON.stringify(r).replace(/\\u001b/g, 'ESC')}\r\n` : 'No answer from the terminal\r\n');
    this.m.log(`tset reply: ${JSON.stringify(r)}`);
    this.prompt();
  }

  shutdown(cb) {
    this.out('Shutdown at 12:05 (in 0 minutes) [pid 113]\r\n\r\n\x07*** FINAL System shutdown message from root@pdp11 ***\r\nSystem going down IMMEDIATELY\r\n\r\n');
    this.timers.push(setTimeout(() => {
      this.out('syncing disks... done\r\nhalting\r\n');
      this.state = 'down';
      this.m.osHalted();
      cb?.();
    }, 1200));
  }
}

// ------------------------------------------------------------------ machine

class Machine {
  constructor(opts, events) {
    this.opts = opts;
    this.ev = events; // { state(), systems(), toast(level, text), progress(p), console(bytes), log(line) }
    this.systems = mockSystems();
    if (opts.firstRun) this.systems[0].installed = false;
    if (opts.bootTimeoutSec > 0) for (const x of this.systems) x.bootTimeoutSec = opts.bootTimeoutSec;
    this.selected = 'bsd211';
    if (opts.system && this.sys(opts.system)) {
      this.selected = opts.system;
      this.sys(opts.system).installed = true;
    }
    this.hang = !!opts.bootHang; // boots never reach the prompt
    this.bootTimer = 0;
    this.speedOverride = {};
    this.run = 'off';
    this.running = '';
    this.ready = false;
    this.error = '';
    this.needsInstall = null;
    this.controls = {
      sr: 0, enableHalt: 'enable', sinstSbus: 'inst', addrKnob: 0, dataKnob: 1, key: opts.off ? 'off' : 'power',
      lampTest: false, pressed: { LOAD_ADRS: false, EXAM: false, DEP: false, CONT: false, START: false },
    };
    this.cons = { addr: 0, adrsErr: false, lastStop: '', pc: 0, busy: '' };
    this.cpu = { mem: new Map(), R: new Array(8).fill(0), psw: 0, dr: 0, mmu: false, program: null, lastPA: 0 };
    this.dataPaths = 0;
    this.lastOp = 'none';
    this.consPA = null;
    this.os = new FakeOS(this);
    this.installs = new Map();
    this.uptime0 = 0;
    this.idle = { bsd: 0o377, rsx: 0, rsxCount: 0, t: 0 };
    this.trapFlash = 0;
  }

  sys(id = this.selected) {
    return this.systems.find((s) => s.id === id);
  }

  speed() {
    return this.speedOverride[this.selected] ?? this.sys()?.speed ?? 'fast';
  }

  effectiveHalt() {
    return this.controls.enableHalt === 'halt' && this.controls.key !== 'lock';
  }

  changed() {
    this.ev.state();
  }

  log(...lines) {
    for (const line of lines) this.ev.log(line);
  }

  consoleOut(s) {
    this.ev.console(Buffer.from(s, 'latin1'));
  }

  setReady() {
    if (this.run === 'running' && !this.ready) {
      this.ready = true;
      this.cancelBootTimer();
      this.changed();
    }
  }

  /**
   * Boot timeout (like internal/panel): one toast to everyone when a boot has
   * not reached the system's prompt within its bootTimeoutSec; cancelled by
   * ready, any stop, power off and a new boot.
   */
  armBootTimer() {
    this.cancelBootTimer();
    const sys = this.sys();
    const n = sys?.bootTimeoutSec > 0 ? sys.bootTimeoutSec : 60;
    this.bootTimer = setTimeout(() => {
      this.bootTimer = 0;
      if (this.run !== 'running' || this.ready) return;
      this.ev.toast('warn', `${sys.short || sys.name} has not reached its prompt after ${n} s — check the console. If it hangs, power off and boot again.`);
    }, n * 1000);
    this.bootTimer.unref?.(); // never keeps a closed mock alive
  }

  cancelBootTimer() {
    clearTimeout(this.bootTimer);
    this.bootTimer = 0;
  }

  // ---- power

  powerOn(boot) {
    if (this.run !== 'off' && this.run !== 'error') return;
    const sys = this.sys();
    if (!sys.installed) {
      if (sys.license.class === 'redistributable') {
        this.log(`installing ${sys.id} before power-on`);
        this.install(sys.id, false, '', () => {
          this.run = 'off';
          this.powerOn(boot);
        });
        this.run = 'starting';
        this.changed();
      } else {
        this.needsInstall = { id: sys.id, class: sys.license.class };
        this.error = `${sys.name} is not installed: accept its licence to install it`;
        this.changed();
      }
      return;
    }
    this.needsInstall = null;
    this.error = '';
    this.run = 'starting';
    this.changed();
    setTimeout(() => {
      if (this.run !== 'starting' || this.controls.key === 'off') return;
      this.uptime0 = Date.now();
      this.cons = { addr: 0, adrsErr: false, lastStop: '', pc: 0, busy: '' };
      this.dataPaths = 0;
      this.lastOp = 'none';
      this.consPA = null;
      this.cpu.mmu = false;
      this.cpu.program = null;
      this.running = this.selected;
      this.run = 'halted';
      this.log(
        'PDP-11 simulator Open SIMH V4.1-0 Current        git commit id: 1a2b3c4d',
        '/data/run/run.ini-2> set cpu 11/70',
        '/data/run/run.ini-4> set nothrottle',
        '/data/run/run.ini-6> set console -u telnet=127.0.0.1:23400;notelnet',
        '%SIM-INFO: Listening on port 23400',
        '/data/run/run.ini-9> attach panel /dev/shm/pdp11d-panel',
        `/data/run/run.ini-11> attach -e rq0 /data/systems/${this.selected}/disk0.dsk`,
        'sim> set panel refresh',
      );
      this.changed();
      if (boot) this.boot();
    }, 500);
  }

  powerOff() {
    if (this.run === 'off') return;
    this.os.stop();
    this.cancelBootTimer();
    this.run = 'stopping';
    this.changed();
    if (this.running) this.log('sim> quit', 'Goodbye');
    setTimeout(() => {
      this.run = 'off';
      this.running = '';
      this.ready = false;
      this.cpu.program = null;
      this.cons.busy = '';
      this.changed();
    }, 300);
  }

  boot() {
    if (this.run === 'off' || this.run === 'error') {
      this.controls.key = 'power';
      this.powerOn(true);
      return;
    }
    this.os.stop();
    this.cpu.program = 'os';
    this.cpu.mmu = true;
    this.cpu.psw = 0;
    this.running = this.selected;
    this.ready = false;
    this.cons.adrsErr = false;
    this.consPA = null;
    this.run = 'running';
    this.log('sim> do dialog.do', `sim> boot ${this.selected === 'bsd211' ? 'rq0' : 'rp0'}`);
    this.changed();
    this.os.boot(this.selected, { hang: this.hang });
    this.armBootTimer();
  }

  /** CPU stopped (HALT switch, HALT instruction, step). */
  stop(reason, pc) {
    this.cancelBootTimer();
    this.log(`${reason}, PC: ${(pc & 0o177777).toString(8).padStart(6, '0')} (MOV R0,R1)`);
    this.run = 'halted';
    this.cons.lastStop = reason;
    this.cons.pc = pc & 0o177777;
    this.cons.addr = (this.cons.addr & 0o17600000) | this.cons.pc;
    this.lastOp = 'none';
    this.dataPaths = this.cpu.R[0];
    this.changed();
  }

  /** The OS halted itself (shutdown); ready stays true until power off or the next boot (like pdp11d). */
  osHalted() {
    this.stop('HALT instruction', 0o157262);
  }

  // ---- console switches (PANEL_LOGIC §3)

  loc(a) {
    return a & 0o17777776;
  }

  step(a) {
    if (a >= 0o17777700 && a <= 0o17777717) return 0o17777700 + ((a - 0o17777700 + 1) & 0o17);
    if (a === 0o17777676) return null;
    return (a & 0o17600000) | ((a + 2) & 0o177777);
  }

  exists(a) {
    // 4 MB of memory and the I/O page; the gap between is non-existent
    return a < 4 * 1024 * 1024 || a >= 0o17760000;
  }

  consoleSwitch(name) {
    const c = this.controls;
    if (this.run !== 'halted' || c.key === 'lock') {
      return this.run === 'running' ? 'works only when the CPU is halted — set ENABLE/HALT to HALT'
        : c.key === 'lock' ? 'the console is locked (key switch at LOCK)' : 'the machine is not powered';
    }
    if (c.sinstSbus === 'bus' && ['LOAD_ADRS', 'EXAM', 'DEP'].includes(name)) return 'does nothing in S BUS CYCLE';
    const cs = this.cons;
    switch (name) {
      case 'LOAD_ADRS':
        cs.addr = c.sr;
        this.lastOp = 'none';
        cs.adrsErr = false;
        this.dataPaths = 0;
        break;
      case 'EXAM':
      case 'DEP': {
        if (cs.adrsErr) return 'ADRS ERR: load a new address first';
        const op = name === 'EXAM' ? 'exam' : 'dep';
        const stepping = this.lastOp === op;
        if (stepping) {
          const n = this.step(cs.addr);
          if (n === null) {
            cs.adrsErr = true;
            cs.addr = c.sr;
            break;
          }
          cs.addr = n;
        }
        const a = cs.addr;
        if (a >= 0o17777700 && a <= 0o17777717) {
          const r = (a - 0o17777700) & 7;
          if (op === 'dep') this.cpu.R[r] = c.sr & 0o177777;
          this.dataPaths = this.cpu.R[r];
        } else if (!this.exists(a)) {
          cs.adrsErr = true;
          this.lastOp = 'none';
          if (!stepping) cs.addr &= ~0o177777;
          break;
        } else if (op === 'dep') {
          this.cpu.mem.set(this.loc(a), c.sr & 0o177777);
          if (this.loc(a) === 0o17777570) this.cpu.dr = c.sr & 0o177777;
          this.dataPaths = c.sr & 0o177777;
        } else {
          this.dataPaths = this.cpu.mem.get(this.loc(a)) ?? 0;
        }
        this.consPA = this.loc(a);
        this.lastOp = op;
        break;
      }
      case 'CONT':
        if (this.effectiveHalt()) {
          this.cons.adrsErr = false;
          this.stop('Step expired', (this.cons.pc + 2) & 0o177777);
        } else {
          this.resume();
        }
        break;
      case 'START': {
        if (this.effectiveHalt()) {
          this.cpu.mmu = false;
          this.cpu.psw = 0;
          this.log('sim> reset');
          break;
        }
        const a = cs.addr & 0o177777;
        if (a === 0o165000 || a === 0o173000) {
          this.boot();
          break;
        }
        this.cpu.mmu = false;
        this.cpu.psw = 0;
        this.cpu.R[7] = a;
        this.cpu.chaser = this.chaserAt(a);
        this.cpu.program = this.cpu.chaser ? 'chaser' : 'loop';
        this.cpu.R[0] = 1;
        this.cpu.carry = 0;
        this.cons.adrsErr = false;
        this.consPA = null;
        this.run = 'running';
        break;
      }
      default:
        break;
    }
    this.changed();
    return '';
  }

  /**
   * The tutorial's light chaser at a (MOV #1,R0; MOV R0,@#177570; NOP or CLR R1;
   * SOB R1,.; ROL R0; BR .-6). Returns 'nop' or 'clr' (CLR R1 clears the carry,
   * so that variant loses its bit after one pass), or null.
   */
  chaserAt(a) {
    const w = (i) => this.cpu.mem.get(a + 2 * i);
    const fixed = [0o012700, 0o000001, 0o010037, 0o177570, null, 0o077101, 0o006100, 0o000772];
    if (!fixed.every((x, i) => x === null || w(i) === x)) return null;
    return w(4) === 0o000240 ? 'nop' : w(4) === 0o005001 ? 'clr' : null;
  }

  resume() {
    if (!this.cpu.program) this.cpu.program = 'loop';
    this.cons.adrsErr = false;
    this.consPA = null;
    this.run = 'running';
    if (this.cpu.program === 'os' && this.os.state !== 'down') this.ready = this.os.state !== 'booting';
    this.changed();
  }

  // ---- inputs from browsers

  /** Returns a hint for the sender when the input was ignored. */
  input(m) {
    const c = this.controls;
    switch (m.type) {
      case 'sr':
        if (!Number.isInteger(m.value) || m.value < 0 || m.value > 0o17777777) return 'invalid switch register value';
        c.sr = m.value;
        break;
      case 'momentary': {
        if (!MOMENTARY.includes(m.name) || typeof m.active !== 'boolean') return 'invalid momentary switch';
        if (m.name === 'LAMP_TEST') {
          c.lampTest = m.active;
          break;
        }
        const was = c.pressed[m.name];
        c.pressed[m.name] = m.active;
        this.changed();
        if (m.active && !was) return this.consoleSwitch(m.name);
        return '';
      }
      case 'latch':
        if (m.name === 'ENABLE_HALT' && (m.value === 'enable' || m.value === 'halt')) {
          c.enableHalt = m.value;
          if (this.effectiveHalt() && this.run === 'running') this.stop('Front panel HALT', this.cpu.R[7] || 0o5360);
        } else if (m.name === 'SINST_SBUS' && (m.value === 'inst' || m.value === 'bus')) {
          c.sinstSbus = m.value;
        } else {
          return 'invalid latch';
        }
        break;
      case 'knob':
        if (!['addr', 'data'].includes(m.name) || !Number.isInteger(m.pos) || m.pos < 0 || m.pos > 7) return 'invalid knob position';
        if (m.name === 'addr') c.addrKnob = m.pos;
        else c.dataKnob = m.pos;
        break;
      case 'key': {
        if (!['off', 'power', 'lock'].includes(m.pos)) return 'invalid key position';
        const prev = c.key;
        c.key = m.pos;
        if (m.pos === 'off') this.powerOff();
        else if (prev === 'off') this.powerOn(!this.effectiveHalt());
        if (this.effectiveHalt() && this.run === 'running') this.stop('Front panel HALT', 0o5360);
        break;
      }
      case 'boot':
        if (c.key === 'lock') return 'unlock the console (key switch) to boot';
        if (this.effectiveHalt()) return 'set ENABLE/HALT to ENABLE to boot';
        this.boot();
        break;
      case 'shutdownOS':
        return this.shutdownOS(m.req);
      case 'retryPower':
        break;
      default:
        return `unknown message type ${m.type}`;
    }
    this.changed();
    return '';
  }

  shutdownOS(req) {
    const sys = this.sys(this.running || this.selected);
    if (this.run !== 'running' || this.cpu.program !== 'os') return 'works only while the operating system is running';
    if (!sys.canShutdown) return `${sys.name} has no automatic shutdown`;
    this.cons.busy = 'shutdown';
    this.changed();
    // the step texts of pdp11d's shutdown (power.go)
    const p = (phase, text, pct) => this.ev.progress({ task: 'shutdown', id: sys.id, phase, pct, bytes: 0, total: 0, text, req });
    p('step', 'Typing “root”', 20);
    this.os.state = 'shell';
    setTimeout(() => {
      p('step', 'Typing “shutdown -h now”', 50);
      this.consoleOut('shutdown -h now\r\n');
      this.os.shutdown(() => {
        p('step', 'Waiting for the system to halt…', 90);
        setTimeout(() => {
          this.controls.key = 'off';
          this.cons.busy = '';
          this.powerOff();
          p('done', 'The OS has shut down; power is off.', 100);
        }, 500);
      });
    }, 500);
    return '';
  }

  /** Why a system switch or factory reset is refused now: pdp11d's texts (handlers.go changeGuard). */
  changeGuard() {
    if (this.controls.key === 'lock') return 'The console is locked: turn the key to POWER or OFF first.';
    if (!['off', 'error'].includes(this.run)) return 'Power the machine off first (shut down the OS, then turn the key to OFF).';
    return '';
  }

  selectSystem(id) {
    if (!this.sys(id)) return `unknown system ${id}`;
    const guard = this.changeGuard();
    if (guard) return guard;
    this.selected = id;
    this.needsInstall = null;
    this.error = '';
    this.ev.systems();
    this.changed();
    this.retryPower();
    return '';
  }

  retryPower() {
    if (this.controls.key !== 'off' && ['off', 'error'].includes(this.run)) this.powerOn(!this.effectiveHalt());
  }

  install(id, accept, req, then) {
    const sys = this.sys(id);
    if (!sys) return `unknown system ${id}`;
    if (sys.installed) {
      this.ev.progress({ task: 'install', id, phase: 'done', pct: 100, bytes: sys.downloadBytes, total: sys.downloadBytes, text: '', req });
      return '';
    }
    if (sys.license.class !== 'redistributable' && !accept) return `${sys.name}: the licence must be accepted (acceptLicense)`;
    if (this.installs.has(id)) return `${sys.name} is already being installed`;
    sys.installing = true;
    this.ev.systems();
    const total = sys.downloadBytes;
    const job = { cancelled: false, timer: 0 };
    this.installs.set(id, job);
    const start = Date.now();
    const DL = this.opts.installMs ?? 4000;
    const send = (phase, pct, bytes, text = '') => this.ev.progress({ task: 'install', id, phase, pct, bytes, total, text, req });
    const tick = () => {
      if (job.cancelled) return;
      const t = Date.now() - start;
      if (t < DL) {
        const f = t / DL;
        send('download', Math.round(f * 900) / 10, Math.round(total * f));
      } else if (t < DL + 600) {
        send('verify', 92, total);
      } else if (t < DL + 1600) {
        send('extract', 96, total);
      } else {
        this.installs.delete(id);
        sys.installing = false;
        sys.installed = true;
        send('done', 100, total);
        this.ev.systems();
        if (then) then();
        else if (id === this.selected) this.retryPower();
        return;
      }
      job.timer = setTimeout(tick, 200);
    };
    tick();
    return '';
  }

  cancel(task, id) {
    const job = this.installs.get(id);
    if (!job) return 'nothing to cancel';
    job.cancelled = true;
    clearTimeout(job.timer);
    this.installs.delete(id);
    const sys = this.sys(id);
    sys.installing = false;
    if (this.run === 'starting') {
      this.run = 'off';
      this.changed();
    }
    this.ev.progress({ task, id, phase: 'cancelled', pct: 0, bytes: 0, total: sys.downloadBytes, text: '' });
    this.ev.systems();
    return '';
  }

  factoryReset(id, req) {
    const sys = this.sys(id);
    if (!sys || id !== this.selected) return 'factory reset works on the selected system';
    const guard = this.changeGuard();
    if (guard) return guard;
    if (!sys.installed) return `${sys.name} is not installed`;
    this.ev.progress({ task: 'factoryReset', id, phase: 'extract', pct: 50, bytes: 0, total: 0, text: '', req });
    setTimeout(() => {
      this.ev.progress({ task: 'factoryReset', id, phase: 'done', pct: 100, bytes: 0, total: 0, text: '', req });
      this.retryPower();
    }, 800);
    return '';
  }

  // ---- lamps (PANEL_LOGIC §4)

  frame(now) {
    const f = new Uint8Array(64);
    const c = this.controls;
    // key OFF: no power, all dark; LAMP TEST lights everything, even while the
    // machine is off, starting or in error; the knob lamps show the selected
    // positions whenever the key is on; then the state rules (PANEL_LOGIC §3-4)
    if (c.key === 'off') return f;
    if (c.lampTest) return f.fill(255);
    f[ADDR_KNOB_LAMP[c.addrKnob]] = 255;
    f[DATA_KNOB_LAMP[c.dataKnob % 4]] = 255;
    if (!['halted', 'running'].includes(this.run)) return f;
    if (this.run === 'running') this.runningLamps(f, now);
    else this.haltedLamps(f);
    return f;
  }

  setBits(f, base, n, v, duty = 255) {
    for (let i = 0; i < n; i++) if ((v / 2 ** i) & 1) f[base + i] = Math.max(f[base + i], duty);
  }

  haltedLamps(f) {
    const c = this.controls;
    const a = this.cons.addr;
    let addr;
    if (a >= 0o17777700 && a <= 0o17777717) addr = 0o17777700 | (a & 0o17);
    else if (c.addrKnob >= 2) addr = a & 0o177777;
    else if (c.addrKnob === 1) addr = a;
    else addr = this.consPA ?? this.cpu.lastPA;
    this.setBits(f, L.A0, 22, addr);
    const fn = c.dataKnob % 4;
    const data = fn === 1 ? this.dataPaths : fn === 0 ? c.sr & 0o177777 : fn === 2 ? 0o170 : this.cpu.dr;
    this.setBits(f, L.D0, 16, data);
    f[L.MASTER] = 255;
    f[L.KERNEL] = 255;
    f[this.cpu.mmu ? L.ADRS22 : L.ADRS16] = 255;
    if (this.cons.adrsErr) f[L.ADRS_ERR] = 255;
  }

  /** Synthetic bus samples for the running program, accumulated into duties. */
  runningLamps(f, now) {
    const c = this.controls;
    const acc = new Float64Array(64);
    let total = 0;
    const N = 48;
    const prog = this.cpu.program;
    // idle patterns advance with wall time
    const dt = now - this.idle.t;
    if (dt >= 33) {
      this.idle.t = now;
      let v = (this.idle.bsd << 1) & 0xffff; // 2.11BSD: clc; rol rdisply; bpl 1f; bis $1
      if (v & 0x8000) v |= 1;
      this.idle.bsd = v;
      if (++this.idle.rsxCount % 3 === 0) this.idle.rsx = (this.idle.rsx + 1) % 8;
      if (prog === 'chaser') {
        // one pass of the loop: MOV R0,@#177570; NOP or CLR R1 (clears C); SOB; ROL R0
        const r0 = this.cpu.R[0];
        this.cpu.dr = r0;
        const carry = this.cpu.chaser === 'clr' ? 0 : this.cpu.carry ?? 0;
        this.cpu.carry = (r0 >> 15) & 1;
        this.cpu.R[0] = ((r0 << 1) & 0xffff) | carry;
      }
    }
    const RSX = [0o103741, 0o141703, 0o160607, 0o170017, 0o074036, 0o036074, 0o017170, 0o007760];
    const sysId = this.running || this.selected;
    const r0 = prog === 'os' ? (sysId.startsWith('rsx') ? RSX[this.idle.rsx] : this.idle.bsd) : this.cpu.R[0];
    const waitPC = prog === 'os' ? 0o5360 : (this.cpu.R[7] + 8) & 0o177777;
    const idleShare = prog === 'os' ? (this.ready ? 0.9 : 0.35) : prog === 'chaser' ? 0 : 0.2;
    const fn = c.dataKnob % 4;
    let lastParity = [0, 0];
    for (let s = 0; s < N; s++) {
      const w = 1;
      total += w;
      const wait = Math.random() < idleShare;
      let va;
      let pa;
      let data;
      let mode = 0;
      let io = false;
      let write = false;
      if (wait) {
        va = waitPC;
        pa = waitPC;
        data = r0;
      } else if (prog === 'chaser') {
        const pcs = [0o1004, 0o1006, 0o1010, 0o1012, 0o1012, 0o1012, 0o1012, 0o1014];
        va = pcs[s % pcs.length];
        pa = va;
        data = this.cpu.mem.get(va) ?? 0;
        io = s % 16 === 0;
        if (io) {
          pa = 0o17777570;
          data = this.cpu.R[0];
          write = true;
        }
      } else {
        const user = prog === 'os' && this.ready && Math.random() < 0.15;
        mode = user ? 3 : 0;
        va = user ? (Math.random() * 0o40000) & 0o177776 : (0o4000 + Math.random() * 0o60000) & 0o177776;
        pa = user ? 0o1200000 + va : va;
        io = Math.random() < 0.06;
        if (io) pa = 0o17772000 + ((Math.random() * 0o4000) & 0o7776);
        data = (Math.random() * 0x10000) | 0;
        write = Math.random() < 0.3;
      }
      let addr;
      if (c.addrKnob === 0) addr = pa;
      else if (c.addrKnob === 1) addr = va | (this.cons.addr & 0o17600000);
      else addr = va;
      for (let i = 0; i < 22; i++) if ((addr / 2 ** i) & 1) acc[L.A0 + i] += w;
      let d = data;
      if (fn === 0 && wait) d = this.cpu.lastData ?? 0;
      else if (fn === 2) d = wait ? 0x3bf : [0x3bf, 0x32a, 0x3bd, 0x32c, 0x3f5][s % 5];
      else if (fn === 3) d = this.cpu.dr;
      if (!wait) this.cpu.lastData = data;
      for (let i = 0; i < 16; i++) if ((d >> i) & 1) acc[L.D0 + i] += w;
      acc[mode === 3 ? L.USER : mode === 1 ? L.SUPER : L.KERNEL] += w;
      if (this.cpu.mmu) acc[L.ADRS22] += w;
      else acc[L.ADRS16] += w;
      acc[L.RUN] += wait ? w : 0.92 * w;
      if (!wait && io) {
        acc[L.MASTER] += w;
        acc[L.PAUSE] += 0.5 * w;
      }
      if (!wait && !io && !write) {
        const even = (b) => (b.toString(2).split('1').length - 1) % 2 === 0;
        lastParity = [even(data & 0xff) ? 1 : 0, even((data >> 8) & 0xff) ? 1 : 0];
      }
      if (!wait && (io || write)) lastParity = [0, 0];
      acc[L.PARITY_LOW] += lastParity[0] * w;
      acc[L.PARITY_HIGH] += lastParity[1] * w;
      this.cpu.lastPA = pa;
    }
    for (let i = 0; i < 52; i++) f[i] = Math.round((255 * acc[i]) / total);
    if (this.trapFlash > now) f[L.ADRS_ERR] = 255;
  }

  stats() {
    // guest instructions per second / 10^6; time idling in WAIT is not counted
    const idle = this.cpu.program === 'os' && this.ready;
    const fast = this.speed() !== 'authentic';
    return {
      mips: this.run === 'running' ? (idle ? (fast ? 0.021 : 0.018) : fast ? 12.4 : 0.98) : 0,
      samplesPerSec: this.run === 'running' ? 6000 : 0,
      uptimeSec: this.uptime0 && this.run !== 'off' ? Math.round((Date.now() - this.uptime0) / 1000) : 0,
    };
  }
}

// ------------------------------------------------------------------ server

export async function startMockServer(options = {}) {
  const opts = {
    port: 8080, host: '127.0.0.1', base: '/', frameHz: 60, cps: 100, quiet: false, ...options,
  };
  if (!opts.base.endsWith('/')) opts.base += '/';
  let version = opts.version ?? '0.0.0-mock';
  const clients = new Map(); // id -> client
  let nextId = 1;
  const received = [];
  const history = [];
  let historyBytes = 0;
  const logLines = [];
  let answerer = null;
  const input = { queue: [], burst: 0, lastAt: 0 };
  let insecure = !!opts.insecure;
  let refuse = 0; // test hook: WebSocket handshakes still to refuse
  let statusRequests = 0;
  let guestInput = ''; // test hook: console input as the guest received it

  const send = (cl, data) => {
    if (cl.ws.readyState === 1) cl.ws.send(data);
  };
  const json = (cl, obj) => send(cl, JSON.stringify(obj));
  const everyone = (obj) => {
    const s = JSON.stringify(obj);
    for (const cl of clients.values()) send(cl, s);
  };
  const subscribers = (ch) => [...clients.values()].filter((c) => c.subs.has(ch));

  // state coalescing: <= 20/s, stats-only changes <= 1/s
  let stateDirty = false;
  let lastStateJSON = '';
  const stateBody = () => ({
    power: m.controls.key === 'off' ? 'off' : 'on', run: m.run, error: m.error, system: m.selected, running: m.running,
    speed: m.speed(), ready: m.ready, needsInstall: m.needsInstall, controls: structuredClone(m.controls),
    console: { ...m.cons, answerer: currentAnswerer(), inputQueued: input.queue.length },
    stats: m.stats(), viewers: clients.size,
  });
  const systemsBody = () => ({
    selected: m.selected,
    list: m.systems.map((s) => {
      const x = { ...s, speed: m.speedOverride[s.id] ?? s.speed, license: { ...s.license } };
      if (opts.legacy) {
        // a pdp11d from before SystemInfo carried these
        delete x.noLogin;
        delete x.bootTimeoutSec;
      }
      return x;
    }),
  });
  const currentAnswerer = () => {
    if (answerer !== null && clients.get(answerer)?.subs.has('console')) return answerer;
    const subs = subscribers('console').sort((a, b) => a.joined - b.joined || a.id - b.id);
    return subs[0]?.id ?? null;
  };

  let queryAfterHistory = false; // test hook: a live query right behind the next history
  const m = new Machine(opts, {
    state: () => { stateDirty = true; },
    systems: () => everyone({ type: 'systems', ...systemsBody() }),
    toast: (level, text) => everyone({ type: 'toast', level, text }),
    progress: (p) => everyone({ type: 'progress', ...p }),
    console: (bytes) => {
      history.push(bytes);
      historyBytes += bytes.length;
      while (historyBytes > HISTORY_MAX && history.length > 1) historyBytes -= history.shift().length;
      const msg = Buffer.concat([Buffer.from([0x10]), bytes]);
      for (const cl of subscribers('console')) send(cl, msg);
    },
    log: (line) => {
      logLines.push(line);
      if (logLines.length > 1000) logLines.shift();
      for (const cl of subscribers('log')) json(cl, { type: 'log', lines: [line] });
    },
  });

  const flushState = (force = false) => {
    if (!stateDirty && !force) return;
    stateDirty = false;
    const body = stateBody();
    const s = JSON.stringify({ type: 'state', ...body });
    if (s === lastStateJSON) return;
    lastStateJSON = s;
    for (const cl of clients.values()) send(cl, s);
  };
  const stateTimer = setInterval(() => flushState(), 50);
  const statsTimer = setInterval(() => { stateDirty = true; }, 1000);

  // lamp frames at frameHz while anything changed, at least once a second
  let seq = 0;
  let lastFrame = Buffer.alloc(64);
  let lastSent = 0;
  const frameTimer = setInterval(() => {
    const now = Date.now();
    const f = Buffer.from(m.frame(now));
    if (!f.equals(lastFrame) || now - lastSent >= 1000) {
      lastFrame = f;
      lastSent = now;
      seq = (seq + 1) & 0xffff;
      const msg = Buffer.concat([Buffer.from([0x01, 1, seq & 0xff, seq >> 8]), f]);
      for (const cl of subscribers('panel')) send(cl, msg);
    }
  }, Math.round(1000 / opts.frameHz));

  // console input pacing: first 32 bytes of a burst at once, then opts.cps;
  // held while the CPU is not running (like pdp11d)
  const inputTimer = setInterval(() => {
    if (!input.queue.length || m.run !== 'running') return;
    const now = Date.now();
    let n = 0;
    if (input.burst > 0) {
      n = Math.min(input.burst, input.queue.length);
      input.burst = 0;
    } else {
      const due = Math.floor(((now - input.lastAt) * opts.cps) / 1000);
      if (due < 1) return;
      n = Math.min(due, input.queue.length);
    }
    input.lastAt = now;
    const bytes = input.queue.splice(0, n);
    guestInput = (guestInput + Buffer.from(bytes).toString('latin1')).slice(-HISTORY_MAX);
    for (const b of bytes) m.os.input(b);
    stateDirty = true;
  }, 10);
  const consoleInput = (cl, bytes) => {
    if (!['halted', 'running'].includes(m.run)) {
      json(cl, { type: 'toast', level: 'warn', text: 'The machine is powered off: console input dropped' });
      return;
    }
    if (input.queue.length + bytes.length > 64 * 1024) {
      json(cl, { type: 'toast', level: 'warn', text: 'Console input queue full' });
      return;
    }
    if (!input.queue.length) {
      input.burst = Math.min(32, bytes.length);
      input.lastAt = Date.now();
    }
    input.queue.push(...bytes);
    if (answerer !== cl.id) {
      answerer = cl.id;
    }
    stateDirty = true;
  };

  const handle = (cl, msg) => {
    received.push({ client: cl.id, t: Date.now(), msg });
    if (received.length > 2000) received.shift();
    const hint = (text) => text && json(cl, { type: 'toast', level: 'warn', text, req: msg.req });
    switch (msg.type) {
      case 'ping':
        json(cl, { type: 'pong' });
        return;
      case 'subscribe': {
        if (!Array.isArray(msg.sub)) return hint('subscribe needs a list');
        const next = new Set(msg.sub.filter((s) => ['panel', 'console', 'log'].includes(s)));
        const added = [...next].filter((s) => !cl.subs.has(s));
        cl.subs = next;
        if (added.includes('console')) sendHistory(cl);
        if (added.includes('log')) json(cl, { type: 'log', lines: logLines.slice(-LOG_HISTORY) });
        if (added.includes('panel')) send(cl, Buffer.concat([Buffer.from([0x01, 1, seq & 0xff, seq >> 8]), lastFrame]));
        stateDirty = true;
        return;
      }
      case 'selectSystem':
        return hint(m.selectSystem(String(msg.id)));
      case 'install':
        return hint(m.install(String(msg.id), msg.acceptLicense === true, msg.req));
      case 'factoryReset':
        return hint(m.factoryReset(String(msg.id), msg.req));
      case 'cancel':
        return hint(m.cancel(msg.task === 'factoryReset' ? 'factoryReset' : 'install', String(msg.id)));
      case 'speed':
        if (msg.value !== 'fast' && msg.value !== 'authentic') return hint('invalid speed');
        m.speedOverride[m.selected] = msg.value;
        stateDirty = true;
        everyone({ type: 'systems', ...systemsBody() });
        return;
      case 'inputCancel':
        input.queue.length = 0;
        stateDirty = true;
        return;
      default: {
        const r = m.input({ ...msg });
        if (msg.type === 'momentary') {
          if (msg.active) cl.held.add(msg.name);
          else cl.held.delete(msg.name);
        }
        return hint(r);
      }
    }
  };

  const sendHistory = (cl) => {
    // the whole history as exactly one console message (possibly empty), never
    // merged with live output: the client treats it as the end of the history
    send(cl, Buffer.concat([Buffer.from([0x10]), ...history]));
    if (queryAfterHistory) {
      // live output that follows the history immediately (it is parsed after it)
      queryAfterHistory = false;
      m.consoleOut('\x1b[c');
    }
  };

  // ---- HTTP
  const base = opts.base;
  const etags = new Map();
  const server = createServer(async (req, res) => {
    const url = new URL(req.url, 'http://x');
    if (!opts.quiet) console.log(req.method, url.pathname);
    for (const [k, v] of Object.entries(SECURITY_HEADERS)) res.setHeader(k, v);
    try {
      if (base !== '/' && url.pathname === '/') {
        res.writeHead(302, { Location: base });
        return res.end();
      }
      if (!url.pathname.startsWith(base)) {
        res.writeHead(404);
        return res.end('not found');
      }
      const rel = url.pathname.slice(base.length);
      if (rel.startsWith('api/')) return api(rel.slice(4), url, res);
      if (rel.startsWith('mock/')) return hooks(rel.slice(5), req, res);
      if (rel === '' || rel === 'index.html') {
        const html = (await readFile(join(WEB, 'index.html'), 'utf8')).replaceAll('{{VERSION}}', version);
        res.writeHead(200, { 'Content-Type': TYPES['.html'], 'Cache-Control': 'no-store' });
        return res.end(html);
      }
      if (!rel.startsWith('assets/') && !rel.startsWith('dev/')) {
        res.writeHead(404);
        return res.end('not found');
      }
      const file = normalize(join(WEB, rel));
      if (!file.startsWith(WEB + sep) || file.includes(`${sep}node_modules${sep}`)) {
        res.writeHead(403);
        return res.end('forbidden');
      }
      const st = await stat(file).catch(() => null);
      if (!st?.isFile()) {
        res.writeHead(404);
        return res.end('not found');
      }
      const body = await readFile(file);
      let tag = etags.get(file);
      if (!tag || tag.mtime !== st.mtimeMs) {
        tag = { mtime: st.mtimeMs, etag: `"${createHash('sha1').update(body).digest('hex')}"` };
        etags.set(file, tag);
      }
      if (req.headers['if-none-match'] === tag.etag) {
        res.writeHead(304, { ETag: tag.etag });
        return res.end();
      }
      res.writeHead(200, {
        'Content-Type': TYPES[extname(file)] ?? 'application/octet-stream', 'Cache-Control': 'no-cache', ETag: tag.etag,
      });
      return res.end(body);
    } catch (err) {
      console.error(err);
      res.writeHead(500);
      res.end(String(err));
    }
  });

  const api = (path, url, res) => {
    const out = (obj) => {
      res.writeHead(200, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' });
      res.end(JSON.stringify(obj));
    };
    if (path === 'status') {
      statusRequests++;
      return out({ ok: true, version, run: m.run, system: m.selected, power: m.controls.key === 'off' ? 'off' : 'on' });
    }
    if (path === 'systems') return out(systemsBody());
    if (path === 'state') return out(stateBody());
    if (path === 'log') return out({ lines: logLines.slice(-(Number(url.searchParams.get('lines')) || 200)) });
    res.writeHead(404);
    return res.end('not found');
  };

  const readBody = (req) => new Promise((resolve) => {
    let s = '';
    req.on('data', (d) => { s += d; });
    req.on('end', () => {
      try {
        resolve(s ? JSON.parse(s) : {});
      } catch {
        resolve({});
      }
    });
  });

  const hooks = async (path, req, res) => {
    const ok = (obj = { ok: true }) => {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify(obj));
    };
    if (path === 'received') return ok(received);
    const body = req.method === 'POST' ? await readBody(req) : {};
    switch (path) {
      case 'clear':
        received.length = 0;
        return ok();
      case 'console':
        m.consoleOut(String(body.text ?? ''));
        return ok();
      case 'state':
        for (const k of ['run', 'ready', 'error', 'needsInstall']) if (k in body) m[k] = body[k];
        if (body.controls) Object.assign(m.controls, body.controls);
        if ('insecure' in body) insecure = !!body.insecure;
        stateDirty = true;
        return ok();
      case 'toast':
        everyone({ type: 'toast', level: body.level ?? 'info', text: String(body.text ?? '') });
        return ok();
      case 'version':
        version = String(body.version ?? version);
        return ok({ version });
      case 'drop':
        for (const cl of clients.values()) cl.ws.terminate();
        return ok();
      case 'query':
        m.consoleOut(typeof body.seq === 'string' ? body.seq : '\x1b[c');
        return ok();
      case 'query-after-history':
        queryAfterHistory = true;
        return ok();
      case 'log':
        m.log(...(Array.isArray(body.lines) ? body.lines.map(String) : [String(body.text ?? '')]));
        return ok();
      case 'hang':
        m.hang = body.hang !== false;
        return ok({ hang: m.hang });
      case 'refuse':
        refuse = Number(body.n ?? 1);
        return ok({ refuse });
      case 'machine':
        return ok({
          run: m.run, controls: m.controls, cons: m.cons, mem: Object.fromEntries([...m.cpu.mem].map(([a, v]) => [a.toString(8), v.toString(8)])),
          answerer: currentAnswerer(), queued: input.queue.length, subs: Object.fromEntries([...clients.values()].map((c) => [c.id, [...c.subs]])),
          statusRequests, guestInput,
        });
      default:
        res.writeHead(404);
        return res.end('no such hook');
    }
  };

  // ---- WebSocket
  const wss = new WebSocketServer({ noServer: true, maxPayload: 64 * 1024 });
  server.on('upgrade', (req, socket, head) => {
    const url = new URL(req.url, 'http://x');
    if (url.pathname !== `${base}ws`) {
      socket.destroy();
      return;
    }
    const origin = req.headers.origin;
    if (origin && new URL(origin).host !== req.headers.host) {
      socket.write('HTTP/1.1 403 Forbidden\r\n\r\n');
      socket.destroy();
      return;
    }
    if (refuse > 0) {
      // like pdp11d after a restart: the old session cookie is no longer valid
      refuse--;
      socket.write('HTTP/1.1 401 Unauthorized\r\nContent-Length: 0\r\n\r\n');
      socket.destroy();
      return;
    }
    wss.handleUpgrade(req, socket, head, (ws) => {
      const subs = (url.searchParams.get('sub') ?? 'panel,console').split(',').filter((s) => ['panel', 'console', 'log'].includes(s));
      const cl = { id: nextId++, ws, subs: new Set(subs), joined: Date.now(), held: new Set(), alive: true };
      clients.set(cl.id, cl);
      json(cl, { type: 'hello', protocol: 1, server: version, clientId: cl.id, lamps: 64, frameHz: opts.frameHz, auth: false, insecure });
      json(cl, { type: 'state', ...stateBody() });
      json(cl, { type: 'systems', ...systemsBody() });
      if (cl.subs.has('console')) sendHistory(cl);
      if (cl.subs.has('log')) json(cl, { type: 'log', lines: logLines.slice(-LOG_HISTORY) });
      if (cl.subs.has('panel')) send(cl, Buffer.concat([Buffer.from([0x01, 1, seq & 0xff, seq >> 8]), lastFrame]));
      stateDirty = true;
      ws.on('pong', () => { cl.alive = true; });
      ws.on('message', (data, isBinary) => {
        if (isBinary) {
          const b = Buffer.from(data);
          received.push({ client: cl.id, t: Date.now(), binary: b[0], length: b.length - 1, text: b.length < 200 ? b.subarray(1).toString('latin1') : undefined });
          if (b[0] === 0x10) consoleInput(cl, b.subarray(1));
          return;
        }
        let msg;
        try {
          msg = JSON.parse(String(data));
        } catch {
          json(cl, { type: 'toast', level: 'error', text: 'malformed message' });
          return;
        }
        if (!msg || typeof msg.type !== 'string') return;
        handle(cl, msg);
      });
      ws.on('close', () => {
        clients.delete(cl.id);
        for (const name of cl.held) m.input({ type: 'momentary', name, active: false });
        if (answerer === cl.id) answerer = null;
        stateDirty = true;
      });
    });
  });
  const pingTimer = setInterval(() => {
    for (const cl of clients.values()) {
      if (!cl.alive) {
        cl.ws.terminate();
        continue;
      }
      cl.alive = false;
      cl.ws.ping();
    }
  }, 20000);

  await new Promise((resolve) => server.listen(opts.port, opts.host, resolve));
  const { port } = server.address();
  const url = `http://${opts.host}:${port}${base}`;
  if (!opts.quiet) console.log(`mock pdp11d ${version} on ${url}`);
  // autostart; without it the key stays at POWER and the machine off (power "on", run "off")
  if (m.controls.key !== 'off' && opts.autostart !== false) setTimeout(() => m.powerOn(!m.effectiveHalt()), 300);

  return {
    url,
    port,
    machine: m,
    received,
    setVersion: (v) => { version = v; },
    async close() {
      for (const t of [stateTimer, statsTimer, frameTimer, inputTimer, pingTimer]) clearInterval(t);
      m.os.stop();
      m.cancelBootTimer();
      for (const cl of clients.values()) cl.ws.terminate();
      await new Promise((r) => server.close(r));
    },
  };
}

const USAGE = `usage: node web/dev/mock-server.mjs [options]
  --port N          HTTP port (default 8080; 0 = random)
  --host H          listen address (default 127.0.0.1)
  --base /p/        serve under a sub-path (reverse-proxy test), default /
  --first-run       the default system is not installed: auto-install, then boot
  --off             start powered off (key OFF)
  --insecure        hello.insecure = true (no-password banner)
  --frame-hz N      lamp frame rate (default 60)
  --cps N           console input pacing, chars per second (default 100)
  --version V       server version reported in hello (default 0.0.0-mock)
  --system ID       select this system at start (installed): bsd211 unix-v7 rt11 rsx11mp
  --boot-hang       boots never reach the system's prompt (boot-timeout toast)
  --boot-timeout N  every system's bootTimeoutSec (default: per system, 30-90 s)
  --legacy          omit noLogin and bootTimeoutSec from the systems list (older pdp11d)
  --no-autostart    key at POWER but not powered on at start (PDP11_AUTOSTART=false)
  --quiet           no request log`;

function parseArgs(argv) {
  const o = {};
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    const val = () => argv[++i];
    if (a === '--port') o.port = Number(val());
    else if (a === '--host') o.host = val();
    else if (a === '--base') o.base = val();
    else if (a === '--first-run') o.firstRun = true;
    else if (a === '--off') o.off = true;
    else if (a === '--insecure') o.insecure = true;
    else if (a === '--frame-hz') o.frameHz = Number(val());
    else if (a === '--cps') o.cps = Number(val());
    else if (a === '--version') o.version = val();
    else if (a === '--system') o.system = val();
    else if (a === '--boot-hang') o.bootHang = true;
    else if (a === '--boot-timeout') o.bootTimeoutSec = Number(val());
    else if (a === '--legacy') o.legacy = true;
    else if (a === '--no-autostart') o.autostart = false;
    else if (a === '--quiet') o.quiet = true;
    else if (a === '--help' || a === '-h') {
      console.log(USAGE);
      process.exit(0);
    } else {
      console.error(`unknown option ${a}`);
      process.exit(2);
    }
  }
  return o;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const srv = await startMockServer(parseArgs(process.argv.slice(2)));
  const bye = async () => {
    await srv.close();
    process.exit(0);
  };
  process.on('SIGINT', bye);
  process.on('SIGTERM', bye);
}
