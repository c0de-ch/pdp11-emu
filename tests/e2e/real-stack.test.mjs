// Browser end-to-end tests against the real stack: pdp11d built from this
// checkout, the patched SimH, 2.11BSD installed from an archive cache (or
// downloaded) and the web UI from web/, driven in headless Chromium.
// The scenarios run in order on one machine and build on each other:
//   a. first run: install progress, "Booting…", ready banner, login, ls /
//   b. panel: RUN lamp, HALT, LOAD ADRS + EXAM of the PC, ENABLE + CONT
//   c. toggle-in tutorial on a fresh power cycle: the light keeps walking
//   d. pop-out panel window, "Bring back"
//   e. reconnect: history replayed once; pdp11d restart: reconnect, autostart
//   f. screenshots while running, then Shut down OS from the toolbar
//   g. LAMP TEST with the key OFF and with the machine off (the knob lamps
//      lit); autostart off
//   h. boot timeout: the one warning toast
//   i. RT-11 (no shutdown script): every stop confirmation is "Pull the plug" or Cancel
//   j. security headers (Content-Security-Policy, X-Frame-Options)
// No page may violate the Content-Security-Policy. Run: see README.md.
// Screenshots and timings go to the work directory.

import { after, before, describe, test } from 'node:test';
import assert from 'node:assert/strict';
import { cpSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { REPO, settings, skip } from './lib/env.mjs';
import { Daemon, buildDaemon } from './lib/daemon.mjs';
import {
  LAMP, browser, clickKey, clickPaddle, closeBrowser, consoleLines, consoleText, lampBits, lastFrame, openPage,
  outputOf, point, pressPaddle, shot, sleep, state, typeLine, typeSR, waitConsole, waitFor, waitPrompt, waitState, watchCSP, watchErrors,
} from './lib/ui.mjs';

// UI.md §6 (R7): the light chaser at 001000
const PROGRAM = [0o012700, 0o000001, 0o010037, 0o177570, 0o000240, 0o077101, 0o006100, 0o000772];
// Chromium reports refused WebSocket connections while pdp11d restarts, and
// the api/status request the UI makes before reconnecting (PROTOCOL.md)
const EXPECTED_ERRORS = /WebSocket connection to .* failed|Failed to load resource: (net::ERR_CONNECTION_REFUSED|the server responded with a status of 503)/;

describe('real stack: pdp11d + SimH + web UI in Chromium', { skip }, () => {
  let cfg;
  let daemon;
  let ctx;
  let page;
  let csp = []; // Content-Security-Policy violations in any page
  let broken = ''; // the scenario that failed: later ones depend on it
  const timings = {};
  const t0 = Date.now();

  /** Runs a scenario body, recording its time; skips when an earlier scenario failed. */
  const scenario = (name, body) => test(name, async (t) => {
    if (broken) {
      t.skip(`needs the machine state that "${broken}" should have left`);
      return;
    }
    const start = Date.now();
    try {
      await body(t);
      assert.deepEqual(csp.splice(0), [], 'no Content-Security-Policy violations');
    } catch (err) {
      broken = name;
      if (page && cfg) await shot(page, cfg.shots, `FAILED-${name.slice(0, 1)}`).catch(() => {});
      throw err;
    } finally {
      timings[name.slice(0, 1)] = { ...(timings[name.slice(0, 1)] ?? {}), totalMs: Date.now() - start };
    }
  });
  const mark = (key, ms) => {
    timings.marks = { ...(timings.marks ?? {}), [key]: ms };
  };

  before(async () => {
    cfg = await settings();
    mark('buildMs', await buildDaemon(cfg.bin));
    daemon = new Daemon(cfg);
    ctx = await (await browser()).newContext({ viewport: { width: 1920, height: 1080 } });
    csp = await watchCSP(ctx);
    // per page: a load id (a reload gets a new one) and a recorder of the
    // first-run overlay, ready banners and toasts
    await ctx.addInitScript(() => {
      window.__e2eId = Math.random().toString(36).slice(2);
      const rec = { overlay: [], banners: [], toasts: [] };
      window.__e2e = rec;
      let overlay = '';
      let banner = '';
      setInterval(() => {
        const o = document.querySelector('.overlay-card')?.textContent ?? '';
        if (o !== overlay) rec.overlay.push({ t: Date.now(), text: (overlay = o) });
        const b = document.querySelector('.ready-banner')?.textContent ?? '';
        if (b !== banner && (banner = b)) rec.banners.push({ t: Date.now(), text: b });
        for (const el of document.querySelectorAll('.toast:not([data-e2e])')) {
          el.dataset.e2e = '1';
          rec.toasts.push({ t: Date.now(), level: el.className, text: el.querySelector('.toast-text')?.textContent ?? '' });
        }
      }, 40);
    });
  });

  after(async () => {
    timings.allMs = Date.now() - t0;
    if (page) {
      const errs = page.errors.filter((e) => !EXPECTED_ERRORS.test(e));
      if (errs.length) console.log('# browser errors:', JSON.stringify(errs));
    }
    await ctx?.close().catch(() => {});
    await closeBrowser();
    if (daemon) {
      await daemon.stop();
      timings.leftoverSimulator = daemon.leftover ?? 0;
    }
    if (cfg) {
      writeFileSync(join(cfg.work, 'timings.json'), `${JSON.stringify(timings, null, 2)}\n`);
      // a temporary work dir keeps its logs, timings and screenshots, not the disks
      if (!process.env.E2E_WORK && !process.env.E2E_KEEP) {
        for (const p of [cfg.data, cfg.bin]) rmSync(p, { recursive: true, force: true });
      }
      console.log(`# work dir ${cfg.work}; screenshots in ${cfg.shots}`);
    }
    console.log(`# timings ${JSON.stringify(timings)}`);
    assert.equal(timings.leftoverSimulator ?? 0, 0, 'pdp11d left no simulator running');
  });

  scenario('a. first run: install progress, then "Booting…", the ready banner, login and ls /', async () => {
    const start = Date.now();
    await daemon.start();
    // open the page at once: from the cache the install takes only seconds
    page = await openPage(ctx, cfg.url);
    await waitState(page, (st) => st.ready && st.run === 'running', { timeout: 240000, message: '2.11BSD ready', interval: 200 });
    mark('firstRunToReadyMs', Date.now() - start);
    const rec = await page.evaluate(() => window.__e2e);
    const texts = rec.overlay.map((o) => o.text);
    const setup = texts.findIndex((x) => /Setting up 2\.11BSD/.test(x) && /(Preparing|Downloading|Verifying|Unpacking) 2\.11BSD/.test(x));
    const booting = texts.findIndex((x) => /Booting 2\.11BSD/.test(x));
    assert.ok(setup >= 0, `install progress shown (${JSON.stringify(texts)})`);
    assert.ok(booting > setup, `then "Booting…" (${JSON.stringify(texts)})`);
    assert.match(texts[booting], /the login prompt appears/);
    await page.waitForSelector('.overlay-card', { state: 'detached', timeout: 10000 });
    await page.waitForSelector('.ready-banner', { timeout: 10000 });
    assert.match(await page.textContent('.ready-banner'), /2\.11BSD is up — log in as root \(no password\)/);
    assert.match(await page.textContent('.ready-banner'), /Ctrl\+U kills the line/, 'with the key help');
    await shot(page, cfg.shots, 'a-ready-banner');
    await waitConsole(page, /login: *$/m, { timeout: 10000 });
    // log in from the browser
    const login = Date.now();
    await typeLine(page, 'root');
    await waitPrompt(page, { timeout: 60000 });
    mark('loginToPromptMs', Date.now() - login);
    await typeLine(page, 'ls /');
    const out = await waitFor(async () => {
      const o = await outputOf(page, 'ls /');
      return o && (await consoleLines(page)).filter((l) => l.trim()).at(-1).trimEnd() === '#' ? o : null;
    }, { timeout: 30000, message: 'ls / output' });
    const words = out.join(' ').split(/\s+/);
    assert.ok(words.includes('bin') && words.includes('etc'), `ls / lists bin and etc: ${JSON.stringify(out)}`);
    await shot(page, cfg.shots, 'a-logged-in');
    // the simulator log (help tab, "log" channel): SimH's banner and pdp11d's commands
    await page.click('button[aria-label="Help"]');
    await page.click('.tabs button[data-tab="log"]');
    await waitFor(() => page.evaluate(() => window.pdp11Debug.app.conn.subs.includes('log')), { message: 'log subscribed' });
    await waitFor(async () => {
      const t = await page.textContent('.log-lines');
      return /PDP-11 simulator Open SIMH/.test(t) && /sim> boot rq0/.test(t);
    }, { message: 'simulator log history' });
    await shot(page, cfg.shots, 'a-simulator-log');
    await page.keyboard.press('Escape');
    await waitFor(() => page.evaluate(() => !window.pdp11Debug.app.conn.subs.includes('log')), { message: 'log unsubscribed' });
    assert.deepEqual(page.errors, []);
  });

  scenario('b. panel: RUN lamp, HALT, LOAD ADRS + EXAM of the PC, ENABLE + CONT', async () => {
    await waitFor(async () => (await lastFrame(page))?.[LAMP.RUN] >= 128, { message: 'RUN lamp lit while running' });
    // HALT with a real click on the ENABLE/HALT paddle
    await clickPaddle(page, 'ENABLE_HALT');
    const st = await waitState(page, (s) => s.run === 'halted' && s.controls.enableHalt === 'halt', { timeout: 10000, message: 'halted' })
      .then(() => state(page));
    assert.match(st.console.lastStop, /HALT/, `stop reason ${st.console.lastStop}`);
    await waitFor(async () => (await lastFrame(page))?.[LAMP.RUN] === 0, { message: 'RUN lamp dark' });
    // the console info shows the stop: the status pill
    assert.match(await page.textContent('.status-pill'), /Halted/);
    assert.match(await page.getAttribute('.status-pill', 'title'), new RegExp(`Last stop: ${st.console.lastStop}`));
    // LOAD ADRS 17777707 (the PC register) + EXAM: DATA PATHS shows the PC of the stop
    assert.equal(st.controls.dataKnob % 4, 1, 'DATA knob at DATA PATHS');
    await typeSR(page, 0o17777707);
    await pressPaddle(page, 'LOAD_ADRS');
    await waitState(page, (s) => s.console.addr === 0o17777707, { message: 'console address 17777707' });
    await pressPaddle(page, 'EXAM');
    const pc = st.console.pc;
    const f = await waitFor(async () => {
      const fr = await lastFrame(page);
      return fr && lampBits(fr).data === pc ? fr : null;
    }, { message: `DATA lamps = PC ${pc.toString(8)}` });
    // a general register forces the ADDRESS pattern: 21:06 on, 05:04 off, 03:00 = 7
    assert.equal(lampBits(f).address, 0o17777707, 'ADDRESS lamps show 17777707');
    await shot(page, cfg.shots, 'b-halted-exam-pc');
    // ENABLE + CONT: running again, and the shell still answers
    await clickPaddle(page, 'ENABLE_HALT');
    await waitState(page, (s) => s.controls.enableHalt === 'enable', { message: 'ENABLE' });
    await pressPaddle(page, 'CONT');
    await waitState(page, (s) => s.run === 'running', { timeout: 10000, message: 'running after CONT' });
    await waitFor(async () => (await lastFrame(page))?.[LAMP.RUN] >= 128, { message: 'RUN lamp lit again' });
    await typeLine(page, 'echo ok');
    await waitFor(async () => (await outputOf(page, 'echo ok'))?.[0]?.trimEnd() === 'ok', { timeout: 20000, message: '"ok"' });
    assert.deepEqual(page.errors, []);
  });

  scenario('c. toggle-in tutorial on a fresh power cycle: the light keeps walking', async () => {
    // key OFF asks first; this time shut the OS down cleanly
    await clickKey(page, 'ccw');
    const dlg = page.locator('dialog.dialog[open]');
    await dlg.waitFor();
    assert.equal(await dlg.locator('h2').textContent(), 'Power off');
    assert.deepEqual(await dlg.locator('.dialog-actions button').evaluateAll((bs) => bs.map((b) => b.dataset.value)), ['cancel', 'plug', 'shutdown']);
    const sd = Date.now();
    await dlg.locator('[data-value="shutdown"]').click();
    await waitState(page, (s) => s.run === 'off' && s.controls.key === 'off', { timeout: 120000, interval: 200, message: 'clean shutdown, key OFF' });
    mark('shutdownViaKeyMs', Date.now() - sd);
    await waitConsole(page, /halting/, { timeout: 5000 });
    // ENABLE/HALT to HALT, key to POWER: the CPU powers up halted
    await clickPaddle(page, 'ENABLE_HALT');
    await waitState(page, (s) => s.controls.enableHalt === 'halt', { message: 'HALT' });
    await clickKey(page, 'cw');
    await waitState(page, (s) => s.controls.key === 'power' && s.run === 'halted', { timeout: 60000, message: 'powered on, halted' });
    await sleep(1000);
    assert.equal((await state(page)).run, 'halted', 'did not boot');
    // authentic speed, as the tutorial says
    await page.click('.toolbar .seg button:has-text("Authentic")');
    await waitState(page, (s) => s.speed === 'authentic', { message: 'authentic speed' });
    // the tutorial from help, then auto-toggle (real panel messages)
    await page.click('button[aria-label="Help"]');
    await page.click('.tabs button[data-tab="tutorial"]');
    await page.click('.help-body button:has-text("Open the tutorial")');
    await page.waitForSelector('.tutorial');
    const auto = Date.now();
    await page.click('.tutorial button:has-text("Auto-toggle")');
    await waitState(page, (s) => s.run === 'running' && s.controls.dataKnob % 4 === 3 && s.controls.enableHalt === 'enable',
      { timeout: 120000, interval: 200, message: 'program started, DATA knob at DISPLAY REGISTER' });
    mark('autoToggleMs', Date.now() - auto);
    await waitFor(() => page.evaluate(() => document.querySelectorAll('.tutorial li.done').length === 5), { timeout: 5000, message: 'all tutorial steps done' });
    assert.ok((await page.evaluate(() => window.pdp11Debug.sent.filter((e) => e.msg?.type === 'momentary' && e.msg.name === 'DEP' && e.msg.active).length)) >= 8, 'DEP raised for every word');
    // SimH calibrates its throttle during the first second of running
    await sleep(2000);
    // the DISPLAY REGISTER on the DATA lamps, frame by frame
    const rec = await page.evaluate((ms) => new Promise((resolve) => {
      const out = [];
      const off = window.pdp11Debug.app.conn.on('frame', (lamps) => {
        let d = 0;
        for (let i = 0; i < 16; i++) if (lamps[22 + i] >= 128) d |= 1 << i;
        out.push(d);
      });
      setTimeout(() => { off(); resolve(out); }, ms);
    }), 6000);
    await shot(page, cfg.shots, 'c-light-chaser');
    // states: bit k lit = k, all dark = 16 (the bit is in the carry)
    const multi = rec.filter((d) => d & (d - 1)).length;
    const seq = rec.map((d) => (d ? Math.log2(d) : 16)).filter((v, i, a) => i === 0 || v !== a[i - 1]);
    const steps = seq.slice(1).map((v, i) => (v - seq[i] + 17) % 17);
    const wraps = seq.map((v, i) => (i && v < seq[i - 1] ? i : -1)).filter((i) => i > 0);
    const afterWrap = wraps.length ? new Set(seq.slice(wraps[0]).filter((v) => v < 16)) : new Set();
    timings.c = { ...(timings.c ?? {}), frames: rec.length, states: seq.length, wraps: wraps.length, distinct: new Set(seq).size };
    assert.equal(multi, 0, 'never more than one DATA lamp lit');
    assert.ok(new Set(seq.filter((v) => v < 16)).size >= 6, `the light visits >= 6 positions (${seq.join(' ')})`);
    assert.ok(steps.every((s) => s >= 1 && s <= 3), `it walks forward, one position at a time (${seq.join(' ')})`);
    assert.ok(wraps.length >= 1, `it wraps around (${seq.join(' ')})`);
    assert.ok(afterWrap.size >= 6, `and keeps walking after the wrap (${seq.join(' ')})`);
    // the eight words are really in memory: HALT, LOAD ADRS 1000, EXAM x 8 (DATA PATHS)
    await page.click('.tutorial header button'); // close the tutorial card
    await clickPaddle(page, 'ENABLE_HALT');
    await waitState(page, (s) => s.run === 'halted', { timeout: 10000, message: 'halted in the loop' });
    const knobL = await page.locator('#panel-host .hit-knob[data-knob="data"]').boundingBox();
    for (let i = 0; i < 2; i++) {
      await page.mouse.click(knobL.x + knobL.width * 0.2, knobL.y + knobL.height * 0.5); // anticlockwise
      await sleep(150);
    }
    await waitState(page, (s) => s.controls.dataKnob % 4 === 1, { message: 'DATA knob at DATA PATHS' });
    await typeSR(page, 0o1000);
    await pressPaddle(page, 'LOAD_ADRS');
    await waitState(page, (s) => s.console.addr === 0o1000, { message: 'console address 1000' });
    for (const [i, w] of PROGRAM.entries()) {
      await pressPaddle(page, 'EXAM');
      await waitState(page, (s, a) => s.console.addr === a, { arg: 0o1000 + 2 * i, message: `EXAM ${(0o1000 + 2 * i).toString(8)}` });
      await waitFor(async () => lampBits(await lastFrame(page)).data === w, { message: `word ${i} = ${w.toString(8).padStart(6, '0')}` });
    }
    await shot(page, cfg.shots, 'c-exam-last-word');
    // and back: ENABLE, CONT, DISPLAY REGISTER: the light walks on
    await clickPaddle(page, 'ENABLE_HALT');
    await waitState(page, (s) => s.controls.enableHalt === 'enable', { message: 'ENABLE' });
    await pressPaddle(page, 'CONT');
    await waitState(page, (s) => s.run === 'running', { message: 'running again' });
    for (let i = 0; i < 2; i++) {
      await page.mouse.click(knobL.x + knobL.width * 0.8, knobL.y + knobL.height * 0.5); // clockwise
      await sleep(150);
    }
    await waitState(page, (s) => s.controls.dataKnob % 4 === 3, { message: 'DATA knob at DISPLAY REGISTER' });
    assert.deepEqual(page.errors, []);
  });

  scenario('d. pop-out panel window: lamp frames there, "Bring back" over BroadcastChannel', async () => {
    const [popup] = await Promise.all([ctx.waitForEvent('page'), page.click('.toolbar .pop-panel')]);
    watchErrors(popup);
    await popup.waitForFunction(() => window.pdp11Debug?.connected && window.pdp11Debug.lastFrame, null, { timeout: 20000 });
    assert.equal(await popup.evaluate(() => window.pdp11Debug.view), 'panel');
    assert.deepEqual(await popup.evaluate(() => window.pdp11Debug.app.conn.subs), ['panel']);
    // the pop-out receives the lamp frames: the walking light
    const values = await popup.evaluate((ms) => new Promise((resolve) => {
      const out = new Set();
      let n = 0;
      const off = window.pdp11Debug.app.conn.on('frame', (lamps) => {
        n++;
        let d = 0;
        for (let i = 0; i < 16; i++) if (lamps[22 + i] >= 128) d |= 1 << i;
        out.add(d);
      });
      setTimeout(() => { off(); resolve({ n, distinct: out.size }); }, ms);
    }), 2000);
    assert.ok(values.n >= 20 && values.distinct >= 6, `frames in the pop-out: ${JSON.stringify(values)}`);
    // the main window gives the panel up: notice, no panel subscription
    await page.waitForSelector('#panel-away:not([hidden])');
    assert.equal(await page.isVisible('#panel-host'), false);
    await waitFor(() => page.evaluate(() => !window.pdp11Debug.app.conn.subs.includes('panel')), { message: 'main window unsubscribed from panel' });
    await waitState(page, (s) => s.viewers === 2, { message: 'two viewers' });
    await shot(popup, cfg.shots, 'd-popout-panel');
    await shot(page, cfg.shots, 'd-main-panel-away');
    // "Bring back" closes the pop-out and restores the panel here
    const closed = popup.waitForEvent('close', { timeout: 10000 });
    await page.click('#panel-away button:has-text("Bring back")');
    await closed;
    await page.waitForSelector('#panel-away', { state: 'hidden' });
    await waitFor(() => page.evaluate(() => window.pdp11Debug.app.conn.subs.includes('panel')), { message: 'main window subscribed again' });
    const seq0 = await page.evaluate(() => window.pdp11Debug.lastFrame && Array.from(window.pdp11Debug.lastFrame).join());
    await waitFor(async () => (await page.evaluate(() => Array.from(window.pdp11Debug.lastFrame ?? []).join())) !== seq0, { message: 'frames in the main window again' });
    await waitState(page, (s) => s.viewers === 1, { message: 'one viewer' });
    // closing the pop-out window itself also gives the panel back ("popout-closed")
    const [again] = await Promise.all([ctx.waitForEvent('page'), page.click('.toolbar .pop-panel')]);
    watchErrors(again);
    await again.waitForFunction(() => window.pdp11Debug?.connected, null, { timeout: 20000 });
    await page.waitForSelector('#panel-away:not([hidden])');
    await again.close({ runBeforeUnload: true });
    await page.waitForSelector('#panel-away', { state: 'hidden', timeout: 5000 });
    assert.ok(await page.isVisible('#panel-host .panel-frame'), 'panel back in the main window');
    await waitFor(() => page.evaluate(() => window.pdp11Debug.app.conn.subs.includes('panel')), { message: 'main window subscribed again' });
    assert.deepEqual(again.errors, []);
    assert.deepEqual(popup.errors, []);
    assert.deepEqual(page.errors, []);
  });

  scenario('e. reconnect: the history is replayed once; after a pdp11d restart the page reconnects and the machine boots again', async () => {
    // fast speed again, so that the boot after the restart is quick, and the
    // DATA knob back at DATA PATHS (2.11BSD's idle pattern)
    await page.click('.toolbar .seg button:has-text("Fast")');
    await waitState(page, (s) => s.speed === 'fast', { message: 'fast speed' });
    const knob = await page.locator('#panel-host .hit-knob[data-knob="data"]').boundingBox();
    for (let i = 0; i < 2; i++) {
      await page.mouse.click(knob.x + knob.width * 0.2, knob.y + knob.height * 0.5);
      await sleep(150);
    }
    await waitState(page, (s) => s.controls.dataKnob % 4 === 1, { message: 'DATA knob at DATA PATHS' });
    // 1. the connection drops (same pdp11d): reset + history, exactly once
    const before = await consoleText(page);
    const loadId = await page.evaluate(() => window.__e2eId);
    await page.evaluate(() => window.pdp11Debug.app.conn.ws.close());
    await waitFor(() => page.evaluate(() => !window.pdp11Debug.connected), { message: 'disconnected' });
    await waitFor(() => page.evaluate(() => window.pdp11Debug.connected), { timeout: 15000, message: 'reconnected' });
    await waitFor(async () => (await consoleText(page)) === before, { timeout: 10000, message: 'the same console text after the replay' });
    await sleep(1000);
    assert.equal(await consoleText(page), before, 'history replayed exactly once');
    // 2. restart pdp11d on the same data dir (SIGTERM, PDP11_SHUTDOWN_ON_STOP=0)
    const stop = Date.now();
    const ex = await daemon.stop();
    mark('daemonStopMs', Date.now() - stop);
    assert.equal(ex.code, 0, `pdp11d exit ${JSON.stringify(ex)}`);
    assert.equal(daemon.leftover ?? 0, 0, 'the simulator stopped with pdp11d');
    await page.waitForSelector('.status-pill[data-conn="down"]', { timeout: 10000 });
    assert.match(await page.textContent('.status-pill'), /Disconnected|Connecting/);
    await sleep(2000);
    const restart = Date.now();
    await daemon.start();
    await waitFor(() => page.evaluate(() => window.pdp11Debug.connected), { timeout: 20000, message: 'the page reconnected by itself' });
    mark('restartToReconnectMs', Date.now() - restart);
    assert.equal(await page.evaluate(() => window.__e2eId), loadId, 'no page reload (same version)');
    await waitState(page, (s) => s.run === 'running' && s.ready, { timeout: 240000, interval: 200, message: 'autostart: 2.11BSD up again' });
    mark('restartToReadyMs', Date.now() - restart);
    await waitConsole(page, /login: *$/m, { timeout: 10000 });
    const lines = await consoleLines(page);
    const kernel = lines.filter((l) => /^2\.11 BSD UNIX #\d+/.test(l));
    assert.equal(kernel.length, 1, `one boot in the console, shown once: ${JSON.stringify(kernel)}`);
    assert.equal(lines.filter((l) => l.trimEnd() === '# echo ok').length, 0, 'the old session is gone (terminal reset)');
    // a viewer that connects now gets the same console from the history alone:
    // nothing was written twice (or lost) around the reconnect
    const fresh = watchErrors(await ctx.newPage());
    await fresh.goto(`${cfg.url}?view=console`);
    await fresh.waitForFunction(() => window.pdp11Debug?.connected && window.pdp11Debug.state, null, { timeout: 20000 });
    const mine = await consoleText(page);
    await waitFor(async () => (await consoleText(fresh)) === mine, { timeout: 10000, message: 'a fresh viewer shows the same console' });
    assert.equal(await consoleText(fresh), await consoleText(page), 'reconnected console = history of a fresh viewer');
    assert.deepEqual(fresh.errors, []);
    await fresh.close();
    await shot(page, cfg.shots, 'e-after-restart');
    assert.deepEqual(page.errors.filter((e) => !EXPECTED_ERRORS.test(e)), []);
  });

  scenario('f. screenshots while running, then Shut down OS from the toolbar', async () => {
    // main view at 1920x1080 and 1366x768, lamps lit (without the ready banner)
    if (await page.isVisible('.ready-banner')) await page.click('.ready-banner button[aria-label="Dismiss"]');
    assert.match(await page.textContent('.status-pill .ips'), /^\d+(\.\d+)? [kM] instr\/s$/, 'guest instruction rate in the status pill');
    for (const [w, h] of [[1920, 1080], [1366, 768]]) {
      await page.setViewportSize({ width: w, height: h });
      await sleep(600);
      await waitFor(() => page.evaluate(() => window.pdp11Debug.smoothed?.[42] > 0.5), { message: 'RUN lamp lit' });
      const fit = await page.evaluate(() => ({ sw: document.documentElement.scrollWidth, sh: document.documentElement.scrollHeight, iw: innerWidth, ih: innerHeight }));
      assert.ok(fit.sw <= fit.iw && fit.sh <= fit.ih, `no page scrolling at ${w}x${h} (${JSON.stringify(fit)})`);
      await shot(page, cfg.shots, `f-main-${w}x${h}`);
    }
    await page.setViewportSize({ width: 1920, height: 1080 });
    // the panel alone (pop-out view) with the panel 1340 px wide
    const pp = watchErrors(await ctx.newPage());
    let vw = 1356;
    await pp.setViewportSize({ width: vw, height: 800 });
    await pp.goto(`${cfg.url}?view=panel`);
    await pp.waitForFunction(() => window.pdp11Debug?.connected && window.pdp11Debug.lastFrame, null, { timeout: 20000 });
    let pw = 0;
    for (let i = 0; i < 4; i++) {
      await sleep(400);
      pw = await pp.evaluate(() => Math.round(document.querySelector('.panel-frame').getBoundingClientRect().width));
      if (pw === 1340) break;
      vw += 1340 - pw;
      await pp.setViewportSize({ width: vw, height: 800 });
    }
    assert.equal(pw, 1340, 'panel 1340 px wide');
    await waitFor(() => pp.evaluate(() => window.pdp11Debug.smoothed?.[42] > 0.5), { message: 'RUN lamp lit in the panel view' });
    await shot(pp, cfg.shots, 'f-panel-1340', { selector: '.panel-frame' });
    await shot(pp, cfg.shots, `f-popout-panel-${vw}x800`);
    assert.deepEqual(pp.errors, []);
    await pp.close();
    // Shut down OS from the toolbar: 2.11BSD halts itself, then power and key go off
    const btn = page.locator('.toolbar button:has-text("Shut down")');
    assert.ok(await btn.isEnabled(), 'Shut down OS enabled while 2.11BSD runs');
    const sd = Date.now();
    await btn.click();
    await waitState(page, (s) => s.run === 'off' && s.power === 'off' && s.controls.key === 'off', { timeout: 120000, interval: 200, message: 'shut down: off, key OFF' });
    mark('shutdownViaToolbarMs', Date.now() - sd);
    await waitConsole(page, /halting/, { timeout: 5000 });
    const toasts = (await page.evaluate(() => window.__e2e.toasts)).filter((x) => x.t >= sd).map((x) => x.text);
    assert.ok(!toasts.some((x) => /failed|timed out|Pull the plug/i.test(x)), `no shutdown warnings: ${JSON.stringify(toasts)}`);
    await shot(page, cfg.shots, 'f-after-shutdown');
    assert.deepEqual(page.errors.filter((e) => !EXPECTED_ERRORS.test(e)), []);
  });

  scenario('g. LAMP TEST: dark with the key OFF, all lit while the machine is off; autostart off leaves the key at POWER', async () => {
    const all = (v) => async () => (await lastFrame(page))?.every((x) => x === v);
    // with the key on, the knob lamps show the selected positions in every
    // Machine state; the CPU lamps only while halted or running (PANEL_LOGIC §4)
    const knobsOnly = async () => {
      const lit = await page.evaluate(() => {
        const { geometry: g, state: st } = window.pdp11Debug.app;
        return [g.knobs[0].lamps[st.controls.addrKnob], g.knobs[1].lamps[st.controls.dataKnob]];
      });
      const f = await lastFrame(page);
      return !!f && f.every((x, i) => x === (lit.includes(i) ? 255 : 0));
    };
    const holdLampTest = async () => {
      const p = await point(page, '#panel-host .hit-paddle[data-name="LAMP_TEST"]', 0.5, 0.75);
      await page.mouse.move(p.x, p.y);
      await page.mouse.down();
      await waitState(page, (s) => s.controls.lampTest === true, { message: 'LAMP TEST raised' });
    };
    const release = async () => {
      await page.mouse.up();
      await waitState(page, (s) => s.controls.lampTest === false, { message: 'LAMP TEST released' });
    };
    // key OFF: no power, so even LAMP TEST lights nothing
    assert.equal((await state(page)).controls.key, 'off');
    await holdLampTest();
    await sleep(500);
    assert.ok(await all(0)(), 'key OFF: all lamps dark despite LAMP TEST');
    await release();
    // key at POWER without starting anything: HALT, key POWER (halted), then a
    // pdp11d restart with PDP11_AUTOSTART=false keeps the machine off
    await clickPaddle(page, 'ENABLE_HALT');
    await waitState(page, (s) => s.controls.enableHalt === 'halt', { message: 'HALT' });
    await clickKey(page, 'cw');
    await waitState(page, (s) => s.controls.key === 'power' && s.run === 'halted', { timeout: 60000, message: 'powered on, halted' });
    await daemon.stop();
    await sleep(500);
    await daemon.start({ PDP11_AUTOSTART: 'false' });
    await waitFor(() => page.evaluate(() => window.pdp11Debug.connected), { timeout: 20000, message: 'reconnected' });
    await waitState(page, (s) => s.power === 'on' && s.run === 'off' && s.controls.key === 'power', { message: 'key at POWER, machine off' });
    await sleep(1500);
    assert.equal((await state(page)).run, 'off', 'autostart off: nothing started');
    assert.match(await page.textContent('.status-pill'), /Off.*not started/);
    await waitFor(knobsOnly, { message: 'machine off: only the knob lamps lit' });
    // LAMP TEST lights every lamp although the machine is off
    await holdLampTest();
    await waitFor(all(255), { message: 'LAMP TEST: all 64 lamps lit with the machine off' });
    await shot(page, cfg.shots, 'g-lamp-test-machine-off');
    await release();
    await waitFor(knobsOnly, { message: 'the knob lamps again' });
    // Boot starts it (ENABLE first)
    await clickPaddle(page, 'ENABLE_HALT');
    await waitState(page, (s) => s.controls.enableHalt === 'enable', { message: 'ENABLE' });
    await page.click('.toolbar button:has-text("Boot")');
    await waitState(page, (s) => s.run === 'running', { timeout: 60000, message: 'booting' });
    assert.deepEqual(page.errors.filter((e) => !EXPECTED_ERRORS.test(e)), []);
  });

  scenario('h. boot timeout: a boot that is not up within bootTimeoutSec gets one warning toast, and still comes up', async () => {
    // PDP11_SYSTEMS replaces the embedded catalog: one whose 2.11BSD allows 2 s
    const catalog = join(cfg.work, 'catalog');
    rmSync(catalog, { recursive: true, force: true });
    cpSync(join(REPO, 'systems'), catalog, { recursive: true });
    const file = join(catalog, 'bsd211', 'system.json');
    const bsd = JSON.parse(readFileSync(file, 'utf8'));
    writeFileSync(file, `${JSON.stringify({ ...bsd, bootTimeoutSec: 2 }, null, 2)}\n`);
    await daemon.stop();
    const t0 = Date.now();
    await daemon.start({ PDP11_SYSTEMS: catalog });
    await waitFor(() => page.evaluate(() => window.pdp11Debug.connected), { timeout: 20000, message: 'reconnected' });
    // autostart boots 2.11BSD (the key is at POWER)
    await waitState(page, (s) => s.run === 'running' && s.ready, { timeout: 240000, interval: 200, message: '2.11BSD up' });
    await sleep(1000);
    const warned = (await page.evaluate(() => window.__e2e.toasts)).filter((x) => x.t >= t0 && /has not reached its prompt/.test(x.text));
    assert.equal(warned.length, 1, `one warning: ${JSON.stringify(warned)}`);
    assert.match(warned[0].level, /toast-warn/);
    assert.equal(warned[0].text, `${bsd.name} has not reached its prompt after 2 s — check the console. If it hangs, power off and boot again.`);
    await shot(page, cfg.shots, 'h-boot-timeout');
    assert.deepEqual(page.errors.filter((e) => !EXPECTED_ERRORS.test(e)), []);
  });

  scenario('i. RT-11 has no shutdown script: Power, Boot, a system switch and key OFF offer only "Pull the plug" or Cancel', async () => {
    const top = () => page.locator('dialog.dialog[open]').last();
    const actions = () => top().locator('.dialog-actions button').evaluateAll((bs) => bs.map((b) => b.dataset.value));
    const shutdowns = () => page.evaluate(() => window.pdp11Debug.sent.filter((e) => e.msg?.type === 'shutdownOS').length);
    // install it from the chooser (hobbyist licence; the archive comes from the cache)
    await page.click('.system-btn');
    await page.click('.sys-card[data-id="rt11"] button[data-action="install"]');
    await page.check('#accept-licence');
    await top().locator('[data-value="install"]').click();
    await waitFor(() => page.evaluate(() => window.pdp11Debug.systems.list.find((x) => x.id === 'rt11').installed), { timeout: 120000, interval: 200, message: 'RT-11 installed' });
    // switch from the running 2.11BSD (which can shut down) by pulling the plug
    await page.click('.sys-card[data-id="rt11"] button[data-action="select"]');
    await waitFor(async () => (await actions()).includes('plug'), { message: 'switch confirmation' });
    assert.deepEqual(await actions(), ['cancel', 'plug', 'shutdown'], '2.11BSD offers a clean shutdown');
    await top().locator('[data-value="plug"]').click();
    await page.keyboard.press('Escape'); // the chooser
    await waitState(page, (s) => s.running === 'rt11' && s.run === 'running' && s.ready, { timeout: 300000, interval: 250, message: 'RT-11 up' });
    const sent = await shutdowns();
    await waitFor(() => page.evaluate(() => /RT-11 is up — the console is ready \(no login needed\)/.test(document.querySelector('.ready-banner')?.textContent ?? '')), { message: 'RT-11 ready banner' });
    // Shut down OS is disabled and says why
    const sd = page.locator('.toolbar button:has-text("Shut down")');
    assert.ok(await sd.isDisabled(), 'Shut down OS disabled');
    assert.match(await page.getAttribute('.toolbar .tip-wrap', 'title'), /RT-11 has no automatic shutdown/);
    const confirmations = [
      ['Power', () => page.click('.power-btn')],
      ['Boot', () => page.click('.toolbar button:has-text("Boot")')],
      ['key OFF', () => clickKey(page, 'ccw')],
      ['switch', async () => {
        await page.click('.system-btn');
        await page.click('.sys-card[data-id="bsd211"] button[data-action="select"]');
      }],
    ];
    for (const [what, open] of confirmations) {
      await open();
      await waitFor(async () => (await actions()).includes('plug'), { message: `${what}: confirmation` });
      assert.deepEqual(await actions(), ['cancel', 'plug'], what);
      assert.match(await top().textContent(), /has no automatic shutdown: do what it needs on the console first/, what);
      assert.match(await top().textContent(), /Make sure no program is running/, `${what}: RT-11's shutdown text`);
      assert.equal(await page.evaluate(() => document.activeElement?.dataset.value), 'cancel', `${what}: Cancel has the focus`);
      if (what === 'Power') await shot(page, cfg.shots, 'i-rt11-power-off');
      await top().locator('[data-value="cancel"]').click();
      if (what === 'switch') await page.keyboard.press('Escape');
    }
    assert.equal((await state(page)).run, 'running', 'still running after Cancel');
    assert.equal(await shutdowns(), sent, 'no shutdownOS sent');
    // pulling the plug works
    await page.click('.power-btn');
    await top().locator('[data-value="plug"]').click();
    await waitState(page, (s) => s.run === 'off' && s.controls.key === 'off', { timeout: 30000, message: 'off' });
    assert.deepEqual(page.errors.filter((e) => !EXPECTED_ERRORS.test(e)), []);
  });

  scenario('j. security headers: every response carries the Content-Security-Policy and X-Frame-Options', async () => {
    for (const path of ['', 'index.html', 'assets/js/main.js', 'assets/css/app.css', 'api/status', 'no-such-file']) {
      const r = await fetch(`${cfg.url}${path}`);
      assert.equal(r.headers.get('content-security-policy'),
        "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; "
        + "connect-src 'self' ws: wss:; frame-ancestors 'self'; object-src 'none'; base-uri 'self'; form-action 'self'", path || '/');
      assert.equal(r.headers.get('x-frame-options'), 'SAMEORIGIN', path || '/');
    }
  });
});
