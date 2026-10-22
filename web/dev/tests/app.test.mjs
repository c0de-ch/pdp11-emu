// Application shell: views and layout, pop-outs, dialogs, banners,
// first run, reconnect and version reload, tutorial (docs/UI.md §1-2, §6).
import { after, before, describe, test } from 'node:test';
import assert from 'node:assert/strict';
import { centre, closeBrowser, mock, newContext, openPage, received, skip, sleep, waitFor } from './helpers.mjs';

describe('application', { skip }, () => {
  let srv;
  before(async () => {
    srv = await mock();
  });
  after(async () => {
    await srv?.close();
    await closeBrowser();
  });

  test('loads without console errors and shows toolbar, panel and console', async () => {
    const page = await openPage(srv, { width: 1920, height: 1080 });
    try {
      await page.waitForSelector('.panel-frame');
      await page.waitForSelector('.console-screen .xterm');
      for (const sel of ['.system-btn', '.power-btn', 'button:has-text("Boot")', '.status-pill', 'button[aria-label="Help"]']) {
        assert.ok(await page.isVisible(sel), `${sel} visible`);
      }
      await sleep(1500);
      assert.deepEqual(page.errors, []);
      assert.equal(await page.getAttribute('meta[name="pdp11-version"]', 'content'), '0.0.0-mock');
    } finally {
      await page.ctx.close();
    }
  });

  for (const [w, h] of [[1366, 768], [1920, 1080], [2560, 1440]]) {
    test(`fits ${w}x${h} without page scrolling`, async () => {
      const page = await openPage(srv, { width: w, height: h });
      try {
        await page.waitForSelector('.console-screen .xterm-screen');
        await sleep(400);
        const r = await page.evaluate(() => {
          const rect = (s) => document.querySelector(s).getBoundingClientRect().toJSON();
          return {
            sw: document.documentElement.scrollWidth, sh: document.documentElement.scrollHeight,
            iw: innerWidth, ih: innerHeight, panel: rect('.panel-frame'), console: rect('.console-frame'),
            layout: window.pdp11Debug.layout.layout, font: window.pdp11Debug.consoleFont(),
          };
        });
        assert.ok(r.sw <= r.iw && r.sh <= r.ih, `no scrolling (${r.sw}x${r.sh} in ${r.iw}x${r.ih})`);
        for (const k of ['panel', 'console']) {
          const b = r[k];
          assert.ok(b.left >= 0 && b.top >= 0 && b.right <= r.iw + 0.5 && b.bottom <= r.ih + 0.5, `${k} inside the viewport`);
        }
        const overlap = !(r.panel.right <= r.console.left || r.console.right <= r.panel.left || r.panel.bottom <= r.console.top || r.console.bottom <= r.panel.top);
        assert.ok(!overlap, 'panel and console do not overlap');
        assert.ok(r.panel.width >= 0.45 * w, `panel at a useful size (${Math.round(r.panel.width)} px)`);
        if (w >= 1920) assert.match(r.font.family, /Glass TTY/, 'integer-scaled Glass TTY');
      } finally {
        await page.ctx.close();
      }
    });
  }

  test('?view=panel shows only the panel and subscribes to panel frames only', async () => {
    const page = await openPage(srv, { path: '?view=panel', width: 1300, height: 760 });
    try {
      await page.waitForSelector('.panel-frame');
      assert.equal(await page.isVisible('#console-area'), false);
      assert.equal(await page.isVisible('.system-btn'), false, 'pop-out toolbar is reduced');
      assert.ok(await page.isVisible('.status-pill'));
      assert.deepEqual(await page.evaluate(() => window.pdp11Debug.app.conn.subs), ['panel']);
      const pf = await page.evaluate(() => document.querySelector('.panel-frame').getBoundingClientRect().width);
      assert.ok(pf > 1200, `panel fills the window (${pf})`);
    } finally {
      await page.ctx.close();
    }
  });

  test('?view=console shows only the console; &chrome=0 hides the toolbars (kiosk)', async () => {
    const page = await openPage(srv, { path: '?view=console', width: 880, height: 660 });
    try {
      await page.waitForSelector('.console-screen .xterm');
      assert.equal(await page.isVisible('#panel-area'), false);
      assert.deepEqual(await page.evaluate(() => window.pdp11Debug.app.conn.subs), ['console']);
      await page.goto(`${srv.url}?view=panel&chrome=0`);
      await page.waitForFunction(() => window.pdp11Debug?.connected);
      assert.equal(await page.isVisible('#toolbar'), false);
      assert.ok(await page.isVisible('.panel-frame'));
    } finally {
      await page.ctx.close();
    }
  });

  test('pop out the panel: the main window shows "Bring back" until the pop-out closes', async () => {
    const page = await openPage(srv, { width: 1600, height: 900 });
    try {
      const [popup] = await Promise.all([page.ctx.waitForEvent('page'), page.click('.pop-panel')]);
      await popup.waitForFunction(() => window.pdp11Debug?.connected);
      assert.equal(await popup.evaluate(() => window.pdp11Debug.view), 'panel');
      await page.waitForSelector('#panel-away:not([hidden])');
      assert.equal(await page.isVisible('.panel-frame'), false);
      await waitFor(() => page.evaluate(() => !window.pdp11Debug.app.conn.subs.includes('panel')), { message: 'main unsubscribed from panel' });
      // preferences sync over BroadcastChannel
      await page.evaluate(() => window.pdp11Debug.app.prefs.set('lampStyle', 'incandescent'));
      await waitFor(() => popup.evaluate(() => window.pdp11Debug.app.prefs.get('lampStyle') === 'incandescent'), { message: 'prefs synced to the pop-out' });
      await page.evaluate(() => window.pdp11Debug.app.prefs.set('lampStyle', 'led'));
      // Bring back closes the pop-out
      const closed = popup.isClosed() ? null : popup.waitForEvent('close', { timeout: 5000 });
      await page.click('#panel-away button');
      await closed;
      assert.ok(popup.isClosed(), 'pop-out closed');
      await page.waitForSelector('#panel-away', { state: 'hidden' });
      assert.ok(await page.isVisible('.panel-frame'));
      await waitFor(() => page.evaluate(() => window.pdp11Debug.app.conn.subs.includes('panel')), { message: 'resubscribed' });
    } finally {
      await page.ctx.close();
    }
  });

  test('system chooser: a hobbyist system needs its licence accepted before install', async () => {
    const page = await openPage(srv, { width: 1600, height: 900 });
    try {
      await page.click('.system-btn');
      await page.waitForSelector('.sys-card[data-id="rt11"]');
      await page.click('.sys-card[data-id="rt11"] button[data-action="install"]');
      await page.waitForSelector('dialog.dialog[open] .licence-text');
      assert.ok(await page.isDisabled('dialog.dialog[open] [data-value="install"]'), 'disabled until accepted');
      await page.check('#accept-licence');
      await page.click('dialog.dialog[open] [data-value="install"]');
      await waitFor(() => received(srv, (m) => m.type === 'install' && m.id === 'rt11' && m.acceptLicense === true).length > 0, { message: 'install sent' });
      await page.waitForSelector('.sys-card[data-id="rt11"] .progress');
      await waitFor(() => page.evaluate(() => window.pdp11Debug.systems.list.find((s) => s.id === 'rt11').installed), { timeout: 10000, message: 'installed' });
      await page.waitForSelector('.sys-card[data-id="rt11"] .badge-ok:has-text("Installed")');
    } finally {
      await page.ctx.close();
    }
  });

  test('switching systems while running offers "Shut down the OS first"', async () => {
    const page = await openPage(srv, { width: 1600, height: 900 });
    try {
      await waitFor(() => page.evaluate(() => window.pdp11Debug.state.run === 'running'), { timeout: 10000, message: 'running' });
      await page.click('.system-btn');
      await page.click('.sys-card[data-id="unix-v7"] button[data-action="select"]');
      const confirm = page.locator('dialog.dialog[open]').last();
      await confirm.locator('[data-value="shutdown"]').waitFor();
      assert.match(await confirm.textContent(), /Shut down the OS first \(recommended\)/);
      assert.match(await confirm.textContent(), /shutdown -h now/, "the system's shutdown text");
      await confirm.locator('[data-value="shutdown"]').click();
      await waitFor(() => received(srv, (m) => m.type === 'shutdownOS').length > 0, { message: 'shutdownOS sent' });
      await waitFor(() => received(srv, (m) => m.type === 'selectSystem' && m.id === 'unix-v7').length > 0, { timeout: 15000, message: 'selected after power-off' });
      await waitFor(() => page.evaluate(() => window.pdp11Debug.systems.selected === 'unix-v7'), { message: 'V7 selected' });
      // Unix V7 is not installed but redistributable: powering on installs it
      await waitFor(() => page.evaluate(() => window.pdp11Debug.state.controls.key === 'power'), { timeout: 5000, message: 'key back on' });
      // back to 2.11BSD for the other tests
      await waitFor(() => page.evaluate(() => ['running', 'halted'].includes(window.pdp11Debug.state.run)), { timeout: 15000, message: 'V7 up' });
      await page.keyboard.press('Escape');
      await page.evaluate(() => window.pdp11Debug.app.send({ type: 'key', pos: 'off' }));
      await waitFor(() => page.evaluate(() => window.pdp11Debug.state.run === 'off'), { message: 'off' });
      await page.evaluate(() => window.pdp11Debug.app.send({ type: 'selectSystem', id: 'bsd211' }));
      await page.evaluate(() => window.pdp11Debug.app.send({ type: 'key', pos: 'power' }));
      await waitFor(() => page.evaluate(() => window.pdp11Debug.state.run === 'running'), { timeout: 10000, message: '2.11BSD back' });
    } finally {
      await page.ctx.close();
    }
  });

  test('the ready banner appears when the system comes up after a boot', async () => {
    const page = await openPage(srv, { width: 1600, height: 900 });
    try {
      await waitFor(() => page.evaluate(() => window.pdp11Debug.state.ready), { timeout: 10000, message: 'ready' });
      await page.click('button:has-text("Boot")');
      await page.waitForSelector('dialog.dialog[open]');
      await page.click('dialog.dialog[open] [data-value="plug"]');
      await page.waitForSelector('.ready-banner', { timeout: 10000 });
      assert.match(await page.textContent('.ready-banner'), /2\.11BSD is up — log in as root \(no password\)/);
      await page.click('.ready-banner button');
      await page.waitForSelector('.ready-banner', { state: 'detached' });
    } finally {
      await page.ctx.close();
    }
  });

  test('help: system notes, panel guide, keyboard shortcuts', async () => {
    const page = await openPage(srv, { width: 1600, height: 900 });
    try {
      await page.click('button[aria-label="Help"]');
      await page.waitForSelector('dialog.dialog[open] .help-body');
      assert.match(await page.textContent('.help-body'), /Things to try/);
      assert.match(await page.textContent('.help-body .kv'), /root \(no password\), user \(no password\)/, 'accounts');
      assert.ok(await page.isVisible('.help-body .notes code'), 'markdown code rendered');
      await page.click('.tabs button[data-tab="panel"]');
      assert.equal(await page.locator('.guide-list li').count(), 16);
      assert.equal(await page.locator('.guide-figure .guide-marker').count(), 16);
      await page.click('.tabs button[data-tab="keys"]');
      assert.match(await page.textContent('.help-body'), /LOAD ADRS/);
      await page.keyboard.press('Escape');
      await page.waitForSelector('dialog.dialog[open]', { state: 'detached' });
    } finally {
      await page.ctx.close();
    }
  });

  test('toggle-in tutorial: auto-toggle deposits the program and starts the light chaser', async () => {
    const page = await openPage(srv, { width: 1600, height: 900 });
    try {
      await page.evaluate(() => window.pdp11Debug.app.openTutorial());
      await page.waitForSelector('.tutorial');
      await page.click('.tutorial button:has-text("Auto-toggle")');
      const dlg = await page.waitForSelector('dialog.dialog[open]', { timeout: 3000 }).catch(() => null);
      if (dlg) await page.click('dialog.dialog[open] [data-value="plug"]');
      await waitFor(() => page.evaluate(() => window.pdp11Debug.state.controls.dataKnob % 4 === 3 && window.pdp11Debug.state.run === 'running'), { timeout: 40000, message: 'program running, DISPLAY REGISTER selected' });
      const mem = await (await fetch(`${srv.url}mock/machine`)).json();
      const words = ['12700', '1', '10037', '177570', '240', '77101', '6100', '772'];
      words.forEach((w, i) => assert.equal(mem.mem[(0o1000 + 2 * i).toString(8)], w));
      // a single light walks across the DATA lamps
      const seen = new Set();
      await waitFor(async () => {
        const d = await page.evaluate(() => {
          const f = window.pdp11Debug.lastFrame;
          let v = 0;
          for (let i = 0; i < 16; i++) if (f[22 + i] >= 128) v |= 1 << i;
          return v;
        });
        if (d && (d & (d - 1)) === 0) seen.add(d);
        return seen.size >= 3;
      }, { timeout: 5000, message: 'walking light' });
      // paddles visibly moved: DEP was pressed for every word
      assert.ok(received(srv, (m) => m.type === 'momentary' && m.name === 'DEP' && m.active).length >= 8);
      await page.click('.tutorial header button');
      // reboot the OS for other tests
      await page.evaluate(() => window.pdp11Debug.app.send({ type: 'speed', value: 'fast' }));
      await page.evaluate(() => window.pdp11Debug.app.send({ type: 'latch', name: 'ENABLE_HALT', value: 'enable' }));
      await page.evaluate(() => window.pdp11Debug.app.send({ type: 'boot' }));
    } finally {
      await page.ctx.close();
    }
  });

  test('simulator log: a help tab that subscribes only while shown, history then live lines', async () => {
    const page = await openPage(srv, { width: 1600, height: 900 });
    const subs = () => page.evaluate(() => window.pdp11Debug.app.conn.subs);
    try {
      assert.ok(!(await subs()).includes('log'), 'not subscribed before');
      await page.click('button[aria-label="Help"]');
      await page.click('.tabs button[data-tab="log"]');
      await waitFor(async () => (await subs()).includes('log'), { message: 'subscribed to log' });
      // the history: what the simulator printed at power-on
      await waitFor(async () => /Open SIMH/.test(await page.textContent('.log-lines')), { message: 'history shown' });
      assert.match(await page.textContent('.log-lines'), /sim> boot rq0/);
      const font = await page.evaluate(() => getComputedStyle(document.querySelector('.log-lines')).fontFamily);
      assert.match(font, /monospace/);
      // live lines follow, and the view follows the end
      const marker = `live-${Date.now()}`;
      await fetch(`${srv.url}mock/log`, { method: 'POST', body: JSON.stringify({ lines: [marker] }) });
      await waitFor(async () => (await page.textContent('.log-lines')).includes(marker), { message: 'live line shown' });
      await fetch(`${srv.url}mock/log`, { method: 'POST', body: JSON.stringify({ lines: Array.from({ length: 200 }, (_, i) => `filler line ${i}`) }) });
      await waitFor(() => page.evaluate(() => {
        const pre = document.querySelector('.log-lines');
        return pre.textContent.includes('filler line 199') && pre.scrollTop + pre.clientHeight >= pre.scrollHeight - 4;
      }), { message: 'scrolled to the end' });
      // scrolled up: new lines do not move the view; "Follow the end" brings it back
      await page.evaluate(() => { document.querySelector('.log-lines').scrollTop = 0; });
      await page.waitForSelector('.log-bar button:has-text("Follow the end")');
      await fetch(`${srv.url}mock/log`, { method: 'POST', body: JSON.stringify({ lines: ['after scrolling up'] }) });
      await waitFor(async () => (await page.textContent('.log-lines')).includes('after scrolling up'), { message: 'line appended' });
      assert.equal(await page.evaluate(() => document.querySelector('.log-lines').scrollTop), 0, 'stays where the user scrolled');
      await page.click('.log-bar button:has-text("Follow the end")');
      assert.ok(await page.evaluate(() => {
        const pre = document.querySelector('.log-lines');
        return pre.scrollTop + pre.clientHeight >= pre.scrollHeight - 4;
      }), 'back at the end');
      // copy goes through the DOM copy event
      await page.evaluate(() => {
        window.__copied = [];
        const orig = DataTransfer.prototype.setData;
        DataTransfer.prototype.setData = function setData(t, d) {
          window.__copied.push(d);
          return orig.call(this, t, d);
        };
      });
      await page.click('.log-bar button:has-text("Copy")');
      const copied = await page.evaluate(() => window.__copied.join('\n'));
      assert.ok(copied.includes(marker) && copied.includes('Open SIMH'), 'whole log copied');
      // a reconnect re-sends the history: shown once, not appended twice
      await fetch(`${srv.url}mock/drop`, { method: 'POST' });
      await waitFor(() => page.evaluate(() => !window.pdp11Debug.connected), { message: 'disconnected' });
      await waitFor(() => page.evaluate(() => window.pdp11Debug.connected), { timeout: 8000, message: 'reconnected' });
      await waitFor(async () => (await page.textContent('.log-lines')).includes(marker), { message: 'history again' });
      assert.equal((await page.textContent('.log-lines')).split(marker).length - 1, 1, 'history shown once');
      // another tab, or closing help, unsubscribes
      await page.click('.tabs button[data-tab="keys"]');
      await waitFor(async () => !(await subs()).includes('log'), { message: 'unsubscribed on tab switch' });
      await page.click('.tabs button[data-tab="log"]');
      await waitFor(async () => (await subs()).includes('log'), { message: 'subscribed again' });
      await page.keyboard.press('Escape');
      await waitFor(async () => !(await subs()).includes('log'), { message: 'unsubscribed on close' });
      const srvSubs = (await (await fetch(`${srv.url}mock/machine`)).json()).subs;
      const me = await page.evaluate(() => window.pdp11Debug.hello.clientId);
      assert.deepEqual(srvSubs[me].sort(), ['console', 'panel'], 'server agrees');
      assert.deepEqual(page.errors, []);
    } finally {
      await page.ctx.close();
    }
  });

  test('status pill: guest instructions per second, with what is (not) counted', async () => {
    const page = await openPage(srv, { width: 1600, height: 900 });
    try {
      await waitFor(() => page.evaluate(() => window.pdp11Debug.state.run === 'running' && window.pdp11Debug.state.stats.mips > 0), { timeout: 10000, message: 'running' });
      const r = await page.evaluate(() => {
        const ips = document.querySelector('.status-pill .ips');
        return { text: ips?.textContent ?? '', tip: ips?.title ?? '', pill: document.querySelector('.status-pill').title, all: document.querySelector('.status-pill').textContent };
      });
      assert.match(r.text, /^\d+(\.\d+)? [kM] instr\/s$/, r.text);
      assert.doesNotMatch(r.all, /MIPS/, 'not presented as MIPS');
      assert.match(r.tip, /WAIT/);
      assert.match(r.tip, /not a measure of the host/);
      assert.match(r.pill, /Guest instructions: .* instr\/s/);
    } finally {
      await page.ctx.close();
    }
  });

  test('reconnects with backoff and reloads when the server version changes', async () => {
    const page = await openPage(srv, { width: 1600, height: 900 });
    try {
      await fetch(`${srv.url}mock/drop`, { method: 'POST' });
      await page.waitForSelector('.status-pill[data-conn="down"]');
      await waitFor(() => page.evaluate(() => window.pdp11Debug.connected), { timeout: 5000, message: 'reconnected' });
      srv.setVersion('9.9.9');
      const nav = page.waitForNavigation({ timeout: 10000 });
      await fetch(`${srv.url}mock/drop`, { method: 'POST' });
      await nav;
      await page.waitForFunction(() => window.pdp11Debug?.connected);
      assert.equal(await page.getAttribute('meta[name="pdp11-version"]', 'content'), '9.9.9');
      await sleep(500);
      assert.equal(await page.evaluate(() => window.pdp11Debug.version), '9.9.9', 'no reload loop');
    } finally {
      srv.setVersion('0.0.0-mock');
      await page.ctx.close();
    }
  });
});

describe('systems without login or shutdown (RT-11), boot timeout', { skip }, () => {
  after(closeBrowser);

  test('noLogin: the ready banner says the console is ready and never asks to log in', async () => {
    const srv = await mock({ system: 'rt11' });
    const page = await openPage(srv, { width: 1600, height: 900 });
    try {
      await waitFor(() => page.evaluate(() => window.pdp11Debug.state.ready), { timeout: 10000, message: 'ready' });
      // reboot (RT-11 has no shutdown: only pull the plug), then the banner shows
      await page.click('button:has-text("Boot")');
      const dlg = page.locator('dialog.dialog[open]');
      await dlg.waitFor();
      assert.deepEqual(await dlg.locator('.dialog-actions button').evaluateAll((bs) => bs.map((b) => b.dataset.value)), ['cancel', 'plug']);
      await dlg.locator('[data-value="plug"]').click();
      await page.waitForSelector('.ready-banner', { timeout: 10000 });
      const t = await page.textContent('.ready-banner');
      assert.match(t, /RT-11 is up — the console is ready \(no login needed\)/);
      assert.doesNotMatch(t, /log in/i);
      assert.match(t, /Ctrl\+C twice stops a program/, 'with the key help');
      // help says so too
      await page.click('button[aria-label="Help"]');
      assert.match(await page.textContent('.help-body .kv'), /No login needed: the console is ready to use/);
      assert.ok(await page.isVisible('.help-body button:has-text("Show the ready message")'));
      await page.keyboard.press('Escape');
      assert.deepEqual(page.errors, []);
    } finally {
      await page.ctx.close();
      await srv.close();
    }
  });

  test('canShutdown=false: every confirmation offers only "Pull the plug" and "Cancel"; Shut down OS is disabled', async () => {
    const srv = await mock({ system: 'rt11' });
    const page = await openPage(srv, { width: 1600, height: 900 });
    const actions = () => page.locator('dialog.dialog[open] .dialog-actions button').evaluateAll((bs) => bs.map((b) => b.dataset.value));
    try {
      await waitFor(() => page.evaluate(() => window.pdp11Debug.state.run === 'running'), { timeout: 10000, message: 'running' });
      // toolbar: disabled, and the tooltip explains why
      const sd = page.locator('.toolbar button:has-text("Shut down")');
      assert.ok(await sd.isDisabled(), 'Shut down OS disabled');
      const tip = await page.getAttribute('.toolbar .tip-wrap', 'title');
      assert.match(tip, /RT-11 has no automatic shutdown/);
      assert.match(tip, /needs no shutdown/, "the system's shutdown text");
      assert.equal(await sd.getAttribute('title'), tip);
      // power off (toolbar)
      await page.click('.power-btn');
      await page.waitForSelector('dialog.dialog[open]');
      assert.deepEqual(await actions(), ['cancel', 'plug']);
      assert.match(await page.textContent('dialog.dialog[open]'), /has no automatic shutdown: do what it needs on the console first/);
      assert.match(await page.textContent('dialog.dialog[open]'), /Make sure no program is running/);
      assert.equal(await page.evaluate(() => document.activeElement.dataset.value), 'cancel', 'Cancel has the focus');
      await page.click('dialog.dialog[open] [data-value="cancel"]');
      // the key switch on the panel
      const k = await centre(page, '.hit-key', 0.2, 0.5);
      await page.mouse.click(k.x, k.y);
      await page.waitForSelector('dialog.dialog[open]');
      assert.deepEqual(await actions(), ['cancel', 'plug']);
      await page.click('dialog.dialog[open] [data-value="cancel"]');
      // switching systems
      await page.click('.system-btn');
      await page.click('.sys-card[data-id="bsd211"] button[data-action="select"]');
      await waitFor(async () => (await page.locator('dialog.dialog[open]').count()) === 2, { message: 'confirmation over the chooser' });
      assert.deepEqual(await page.locator('dialog.dialog[open]').last().locator('.dialog-actions button').evaluateAll((bs) => bs.map((b) => b.dataset.value)), ['cancel', 'plug']);
      await page.locator('dialog.dialog[open]').last().locator('[data-value="cancel"]').click();
      await page.keyboard.press('Escape');
      assert.equal(received(srv, (m) => m.type === 'shutdownOS').length, 0, 'no shutdownOS sent');
      assert.equal(await page.evaluate(() => window.pdp11Debug.state.run), 'running', 'still running after Cancel');
      // pulling the plug works
      await page.click('.power-btn');
      await page.click('dialog.dialog[open] [data-value="plug"]');
      await waitFor(() => page.evaluate(() => window.pdp11Debug.state.run === 'off' && window.pdp11Debug.state.controls.key === 'off'), { message: 'off' });
      assert.deepEqual(page.errors, []);
    } finally {
      await page.ctx.close();
      await srv.close();
    }
  });

  test('boot timeout: the server warns once, and the first-run overlay makes room for the console', async () => {
    const srv = await mock({ firstRun: true, installMs: 400, bootHang: true, bootTimeoutSec: 2 });
    const page = await openPage(srv, { width: 1600, height: 900 });
    try {
      await waitFor(async () => /Booting/.test((await page.textContent('.overlay-card').catch(() => '')) ?? ''), { timeout: 8000, message: 'booting' });
      assert.match(await page.textContent('.overlay-card'), /the login prompt appears/);
      await page.waitForSelector('.toast-warn:has-text("has not reached its prompt after 2 s")', { timeout: 6000 });
      assert.match(await page.textContent('.toast-warn'), /2\.11BSD has not reached its prompt after 2 s [-—] check the console\. If it hangs, power off and boot again\./);
      await page.waitForSelector('.overlay-card', { state: 'detached', timeout: 3000 });
      await sleep(2500);
      assert.equal(await page.locator('.toast-text:has-text("has not reached its prompt")').count(), 1, 'one warning');
      assert.equal(await page.evaluate(() => window.pdp11Debug.state.ready), false);
      assert.equal(await page.locator('.ready-banner').count(), 0);
      assert.deepEqual(page.errors, []);
    } finally {
      await page.ctx.close();
      await srv.close();
    }
  });

  test('autostart off with the key at POWER: "not started", and Boot starts it', async () => {
    const srv = await mock({ autostart: false });
    const page = await openPage(srv, { width: 1600, height: 900 });
    try {
      const st = await page.evaluate(() => window.pdp11Debug.state);
      assert.equal(st.power, 'on');
      assert.equal(st.run, 'off');
      assert.match(await page.textContent('.status-pill'), /Off.*not started/);
      await page.click('button:has-text("Boot")');
      await waitFor(() => page.evaluate(() => window.pdp11Debug.state.run === 'running'), { timeout: 10000, message: 'booted' });
      assert.doesNotMatch(await page.textContent('.status-pill'), /not started/);
    } finally {
      await page.ctx.close();
      await srv.close();
    }
  });

  test('a power-on error toast leads to the simulator log', async () => {
    const srv = await mock();
    const page = await openPage(srv, { width: 1600, height: 900 });
    try {
      // after the autostart: a power-on in flight would clear the injected error
      await waitFor(() => page.evaluate(() => window.pdp11Debug.state.run === 'running'), { timeout: 10000, message: 'autostarted' });
      await fetch(`${srv.url}mock/log`, { method: 'POST', body: JSON.stringify({ lines: ['%SIM-ERROR: mock: cannot attach rq0'] }) });
      await fetch(`${srv.url}mock/state`, { method: 'POST', body: JSON.stringify({ run: 'error', error: 'power on failed: boot.ini line 3: attach rq0 failed' }) });
      const toast = page.locator('.toast-error:has-text("power on failed")');
      await toast.waitFor();
      await toast.locator('button:has-text("Simulator log")').click();
      await page.waitForSelector('.help-body[data-tab="log"] .log-lines');
      await waitFor(async () => /cannot attach rq0/.test(await page.textContent('.log-lines')), { message: 'log shown' });
      await page.keyboard.press('Escape');
    } finally {
      await page.ctx.close();
      await srv.close();
    }
  });
});

describe('first run and banners', { skip }, () => {
  after(closeBrowser);

  test('first run: the overlay shows the download, then "Booting…", then the ready banner', async () => {
    const srv = await mock({ firstRun: true, installMs: 1500, insecure: true });
    const page = await openPage(srv, { width: 1600, height: 900 });
    try {
      await page.waitForSelector('.overlay-card');
      await waitFor(async () => /Downloading 2\.11BSD… [\d.]+ [KMG]?B \/ 47\.9 MB/.test(await page.textContent('.overlay-card')), { message: 'download progress text' });
      await waitFor(async () => /Booting/.test((await page.textContent('.overlay-card').catch(() => '')) ?? ''), { timeout: 8000, message: 'booting' });
      await page.waitForSelector('.overlay-card', { state: 'detached', timeout: 15000 });
      await page.waitForSelector('.ready-banner');
      // the no-password banner is persistent until dismissed
      assert.match(await page.textContent('.banner-warn'), /No password set/);
      await page.click('.banner-warn button');
      await page.waitForSelector('.banner-warn', { state: 'detached' });
      assert.deepEqual(page.errors, []);
    } finally {
      await page.ctx.close();
      await srv.close();
    }
  });

  test('works under a reverse-proxy sub-path (relative URLs)', async () => {
    const srv = await mock({ base: '/pdp11/' });
    const ctx = await newContext({ viewport: { width: 1600, height: 900 } });
    const page = await ctx.newPage();
    const failed = [];
    page.on('requestfailed', (r) => failed.push(r.url()));
    page.on('response', (r) => { if (r.status() >= 400) failed.push(`${r.status()} ${r.url()}`); });
    try {
      await page.goto(srv.url);
      await page.waitForFunction(() => window.pdp11Debug?.connected && window.pdp11Debug.state);
      await page.waitForSelector('.panel-frame .lamp');
      assert.deepEqual(failed, []);
    } finally {
      await ctx.close();
      await srv.close();
    }
  });

  test('needsInstall opens the licence dialog', async () => {
    const srv = await mock({ off: true });
    const page = await openPage(srv, { width: 1600, height: 900 });
    try {
      await page.evaluate(() => window.pdp11Debug.app.send({ type: 'selectSystem', id: 'rsx11mp' }));
      await waitFor(() => page.evaluate(() => window.pdp11Debug.systems.selected === 'rsx11mp'), { message: 'selected' });
      await page.click('.power-btn');
      await page.waitForSelector('dialog.dialog[open] .licence-text', { timeout: 5000 });
      assert.match(await page.textContent('dialog.dialog[open] h2'), /Install RSX-11M-PLUS/);
      await page.keyboard.press('Escape');
    } finally {
      await page.ctx.close();
      await srv.close();
    }
  });
});

describe('dialogs, toasts, live regions, security headers', { skip }, () => {
  after(closeBrowser);

  test('an OS halted from the panel is still in memory: the stop dialogs say so, Cancel is the default, "Continue and shut down" works', async () => {
    const srv = await mock();
    const page = await openPage(srv, { width: 1600, height: 900 });
    // the topmost dialog (a confirmation over the system chooser)
    const dialog = () => page.evaluate(() => {
      const d = [...document.querySelectorAll('dialog.dialog[open]')].at(-1);
      return d && { text: d.textContent, actions: [...d.querySelectorAll('.dialog-actions button')].map((b) => b.dataset.value), focus: document.activeElement?.dataset.value };
    });
    try {
      await waitFor(() => page.evaluate(() => window.pdp11Debug.state.ready), { timeout: 10000, message: 'ready' });
      await page.evaluate(() => window.pdp11Debug.app.panelActions().latch('ENABLE_HALT', 'halt'));
      await waitFor(() => page.evaluate(() => window.pdp11Debug.state.run === 'halted'), { message: 'halted' });
      for (const open of [() => page.click('.power-btn'), () => page.click('button:has-text("Boot")'),
        async () => { await page.click('.system-btn'); await page.click('.sys-card[data-id="unix-v7"] button[data-action="select"]'); }]) {
        await open();
        const d = await waitFor(() => dialog().then((x) => x?.actions.includes('plug') && x), { message: 'confirmation' });
        assert.match(d.text, /2\.11BSD \(patch level 482\) is still in memory/);
        assert.match(d.text, /set ENABLE\/HALT to ENABLE and press CONT, then use Shut down OS/);
        assert.deepEqual(d.actions, ['cancel', 'plug', 'resume']);
        assert.equal(d.focus, 'cancel', 'Cancel has the focus');
        await page.keyboard.press('Enter');
        await page.waitForSelector('dialog.dialog[open] .shutdown-text', { state: 'detached' });
        if (await page.locator('dialog.dialog[open]').count()) await page.keyboard.press('Escape'); // the chooser
      }
      assert.equal(await page.evaluate(() => window.pdp11Debug.state.run), 'halted', 'nothing happened');
      // Continue and shut down: ENABLE, CONT, then the clean shutdown
      await page.click('.power-btn');
      await page.click('dialog.dialog[open] [data-value="resume"]');
      await waitFor(() => received(srv, (m) => m.type === 'shutdownOS').length === 1, { message: 'shutdownOS sent' });
      const order = received(srv, (m) => ['latch', 'momentary', 'shutdownOS'].includes(m.type)).slice(-4).map((m) => `${m.type}${m.name ? ` ${m.name}` : ''}${m.value ? ` ${m.value}` : ''}${m.active !== undefined ? ` ${m.active}` : ''}`);
      assert.deepEqual(order, ['latch ENABLE_HALT enable', 'momentary CONT true', 'momentary CONT false', 'shutdownOS']);
      await waitFor(() => page.evaluate(() => window.pdp11Debug.state.run === 'off'), { timeout: 10000, message: 'shut down' });
      // powered up halted (nothing booted): the plain confirmation
      await page.evaluate(() => window.pdp11Debug.app.panelActions().latch('ENABLE_HALT', 'halt'));
      await page.evaluate(() => window.pdp11Debug.app.send({ type: 'key', pos: 'power' }));
      await waitFor(() => page.evaluate(() => window.pdp11Debug.state.run === 'halted'), { message: 'halted at power-on' });
      await page.click('.power-btn');
      await waitFor(dialog, { message: 'dialog' });
      assert.match((await dialog()).text, /The CPU is not running, so no operating system needs to be shut down/);
      await page.keyboard.press('Escape');
      assert.deepEqual(page.errors, []);
    } finally {
      await page.ctx.close();
      await srv.close();
    }
  });

  test('Shut down OS: one toast, updated in place, ends with the result', async () => {
    const srv = await mock();
    const page = await openPage(srv, { width: 1600, height: 900 });
    try {
      await waitFor(() => page.evaluate(() => window.pdp11Debug.state.ready), { timeout: 10000, message: 'ready' });
      await page.evaluate(() => {
        window.__toasts = { max: 0, texts: [] };
        const host = document.querySelector('#toasts');
        new MutationObserver(() => {
          const t = [...host.querySelectorAll('.toast:not(.leaving)')];
          window.__toasts.max = Math.max(window.__toasts.max, t.length);
          for (const e of t) if (window.__toasts.texts.at(-1) !== e.textContent) window.__toasts.texts.push(e.textContent);
        }).observe(host, { childList: true, subtree: true, characterData: true });
      });
      await page.click('.toolbar button:has-text("Shut down")');
      await waitFor(() => page.evaluate(() => window.pdp11Debug.state.run === 'off'), { timeout: 10000, message: 'off' });
      await waitFor(() => page.evaluate(() => /power is off/.test(document.querySelector('#toasts').textContent)), { message: 'final message' });
      const t = await page.evaluate(() => window.__toasts);
      assert.equal(t.max, 1, `one toast at a time (${JSON.stringify(t)})`);
      assert.deepEqual(t.texts.map((x) => x.replace(/×$/, '')), ['Typing “root”', 'Typing “shutdown -h now”', 'Waiting for the system to halt…', 'The OS has shut down; power is off.']);
    } finally {
      await page.ctx.close();
      await srv.close();
    }
  });

  test('long error toasts wrap: the text scrolls, the buttons stay inside the window', async () => {
    const srv = await mock();
    try {
      for (const [w, h] of [[1600, 900], [390, 844]]) {
        const page = await openPage(srv, { width: w, height: h });
        // after the autostart: a power-on in flight would clear the injected error
        await waitFor(() => page.evaluate(() => window.pdp11Debug.state.run === 'running'), { timeout: 10000, message: 'autostarted' });
        for (const error of [
          `install bsd211: cannot download https://www.tuhs.org/Archive/Distributions/UCB/2.11BSD/bsd211pl482.tar.gz: sha256 ${'7bb16343d280a888'.repeat(4)} does not match`,
          'cannot open /var/lib/pdp11/systems/unixv5/unix_v5_rk.dsk: permission denied',
        ]) {
          await fetch(`${srv.url}mock/state`, { method: 'POST', body: JSON.stringify({ run: 'error', error }) });
          const toast = page.locator('.toast-error').last();
          await toast.waitFor();
          const r = await toast.evaluate((el) => [el, ...el.querySelectorAll('button')].map((e) => e.getBoundingClientRect().toJSON()));
          for (const b of r) assert.ok(b.left >= 0 && b.right <= w && b.top >= 0 && b.bottom <= h, `${w}x${h}: inside (${JSON.stringify(b)})`);
          await toast.locator('.toast-x').click();
          await toast.waitFor({ state: 'detached' });
          await fetch(`${srv.url}mock/state`, { method: 'POST', body: JSON.stringify({ run: 'running', error: '' }) });
          await sleep(100);
        }
        await page.ctx.close();
      }
    } finally {
      await srv.close();
    }
  });

  test('many long toasts at once: the oldest give way, every one shown stays inside the window with its buttons', async () => {
    const srv = await mock();
    try {
      for (const [w, h] of [[1366, 768], [844, 390], [390, 844]]) {
        const page = await openPage(srv, { width: w, height: h });
        const r = await page.evaluate(() => {
          const long = `cannot download https://www.tuhs.org/Archive/Distributions/UCB/2.11BSD/bsd211pl482.tar.gz: sha256 ${'7bb16343d280a888'.repeat(4)} does not match ${'x'.repeat(300)}`;
          for (let i = 0; i < 5; i++) window.pdp11Debug.app.toasts.show(`${i}: ${long}`, 'error', { action: { label: 'Simulator log', onClick: () => {} } });
          return [...document.querySelectorAll('#toasts .toast')].map((t) => ({
            text: t.querySelector('.toast-text').textContent.slice(0, 2),
            inside: [t, ...t.querySelectorAll('button')].every((e) => {
              const b = e.getBoundingClientRect();
              return b.left >= 0 && b.top >= 0 && b.right <= innerWidth && b.bottom <= innerHeight;
            }),
          }));
        });
        assert.ok(r.length >= 1 && r.at(-1).text === '4:', `${w}x${h}: the newest is shown (${JSON.stringify(r)})`);
        assert.ok(r.every((t) => t.inside), `${w}x${h}: ${JSON.stringify(r)}`);
        await page.ctx.close();
      }
    } finally {
      await srv.close();
    }
  });

  test('a shutdown toast whose final message cannot come (the connection dropped) goes away', async () => {
    const srv = await mock();
    const page = await openPage(srv, { width: 1600, height: 900 });
    try {
      await page.evaluate(() => window.pdp11Debug.app.onProgress({ type: 'progress', task: 'shutdown', id: 'bsd211', phase: 'step', pct: 50, text: 'Waiting for the system to halt…', req: 'r1' }));
      const toast = page.locator('.toast:has-text("Waiting for the system to halt")');
      await toast.waitFor();
      await sleep(5500);
      assert.equal(await toast.count(), 1, 'sticky while the steps run');
      await fetch(`${srv.url}mock/drop`, { method: 'POST' });
      await toast.waitFor({ state: 'detached', timeout: 8000 });
    } finally {
      await page.ctx.close();
      await srv.close();
    }
  });

  test('screen readers hear run-state changes, not the instruction rate; the first-run overlay announces phases only', async () => {
    const srv = await mock({ firstRun: true, installMs: 1500 });
    const ctx = await newContext({ viewport: { width: 1600, height: 900 } });
    // status regions that appear together with their text: not every screen reader reads those
    await ctx.addInitScript(() => {
      window.__bornWithText = [];
      new MutationObserver((ms) => {
        for (const m of ms) {
          for (const n of m.addedNodes) {
            if (n.nodeType !== 1) continue;
            for (const el of [n, ...n.querySelectorAll('[role="status"]')]) if (el.getAttribute('role') === 'status' && el.textContent) window.__bornWithText.push(el.textContent);
          }
        }
      }).observe(document, { childList: true, subtree: true });
    });
    const page = await openPage(srv, { context: ctx });
    try {
      await page.evaluate(() => {
        window.__live = [];
        const watch = (el) => new MutationObserver(() => window.__live.push(el.textContent)).observe(el, { childList: true, subtree: true, characterData: true });
        for (const el of document.querySelectorAll('[aria-live]:not([aria-live="off"]), [role="status"], [role="alert"]')) watch(el);
        // regions added later (the overlay's)
        new MutationObserver((ms) => {
          for (const m of ms) for (const n of m.addedNodes) if (n.nodeType === 1) for (const el of [n, ...n.querySelectorAll('*')]) if (el.getAttribute('role') === 'status') watch(el);
        }).observe(document.body, { childList: true, subtree: true });
      });
      assert.equal(await page.getAttribute('.status-pill', 'aria-live'), null, 'the pill is not a live region');
      assert.equal(await page.getAttribute('.status-pill', 'role'), null);
      await waitFor(() => page.evaluate(() => window.pdp11Debug.state.ready), { timeout: 15000, message: 'installed and ready' });
      await sleep(3000); // the instruction rate changes meanwhile
      const live = await page.evaluate(() => window.__live);
      assert.ok(live.every((t) => !/instr\/s|\d+(\.\d+)? [KM]?B \//.test(t)), `no rates or byte counts announced: ${JSON.stringify(live)}`);
      assert.ok(live.includes('Running, ready'), JSON.stringify(live));
      assert.ok(live.some((t) => /^Setting up 2\.11BSD.*: downloading$/.test(t)), JSON.stringify(live));
      assert.ok(live.some((t) => /^Booting 2\.11BSD.*… Watch the console/.test(t)), JSON.stringify(live));
      assert.ok(live.length < 25, `${live.length} announcements`);
      // the overlay's region is in the page before its first phase, so that phase is read too
      assert.deepEqual(await page.evaluate(() => window.__bornWithText.filter((t) => /^(Setting up|Booting)/.test(t))), []);
    } finally {
      await ctx.close();
      await srv.close();
    }
  });

  test('prefers-reduced-motion: no animations, no blinking cursor', async () => {
    const srv = await mock({ firstRun: true, installMs: 3000 });
    const ctx = await newContext({ viewport: { width: 1600, height: 900 }, reducedMotion: 'reduce' });
    const page = await openPage(srv, { context: ctx });
    const animation = (sel) => page.evaluate((s) => getComputedStyle(document.querySelector(s)).animationName, sel);
    try {
      // installing: the CPU is starting (a pulsing status dot without reduced motion)
      await page.waitForSelector('.overlay-card');
      await page.waitForSelector('.status-pill[data-run="starting"]');
      assert.equal(await animation('.status-pill .dot'), 'none');
      await page.evaluate(() => window.pdp11Debug.app.toast('a toast'));
      await page.evaluate(() => window.pdp11Debug.app.showReadyBanner(true));
      assert.equal(await animation('.toast'), 'none');
      assert.equal(await animation('.ready-banner'), 'none');
      // booting: an indeterminate progress bar
      await page.waitForSelector('.progress.indeterminate', { timeout: 10000 });
      assert.equal(await animation('.progress.indeterminate > span'), 'none');
      // still, it fills the track (busy, not a partial progress)
      assert.ok(await page.evaluate(() => {
        const bar = document.querySelector('.progress.indeterminate');
        return Math.abs(bar.firstElementChild.getBoundingClientRect().width - bar.getBoundingClientRect().width) < 2;
      }), 'a full still bar');
      assert.equal(await page.evaluate(() => window.pdp11Debug.app.console.term.options.cursorBlink), false, 'steady cursor');
      assert.equal(await page.evaluate(() => window.pdp11Debug.app.prefs.get('lampStyle')), 'reduced');
    } finally {
      await ctx.close();
      await srv.close();
    }
  });

  test('a refused WebSocket handshake: api/status is fetched before the next attempt (renews Basic credentials); the outage is announced once', async () => {
    const srv = await mock();
    const page = await openPage(srv, { width: 1600, height: 900 });
    try {
      await waitFor(() => page.evaluate(() => window.pdp11Debug.state.ready), { timeout: 10000, message: 'ready' });
      await page.evaluate(() => {
        window.__said = [];
        const el = document.querySelector('#toolbar .sr-only[role="status"]');
        new MutationObserver(() => window.__said.push(el.textContent)).observe(el, { childList: true, subtree: true, characterData: true });
      });
      const before = (await (await fetch(`${srv.url}mock/machine`)).json()).statusRequests;
      await fetch(`${srv.url}mock/refuse`, { method: 'POST', body: JSON.stringify({ n: 2 }) });
      await fetch(`${srv.url}mock/drop`, { method: 'POST' });
      await waitFor(() => page.evaluate(() => !window.pdp11Debug.connected), { message: 'disconnected' });
      await waitFor(() => page.evaluate(() => window.pdp11Debug.connected), { timeout: 10000, message: 'reconnected' });
      const after = (await (await fetch(`${srv.url}mock/machine`)).json()).statusRequests;
      assert.equal(after - before, 2, 'one api/status before each attempt after a refusal');
      // three attempts, one announcement of the outage (not one per attempt)
      await waitFor(() => page.evaluate(() => window.__said.at(-1) === 'Running, ready'), { message: 'back' });
      assert.deepEqual(await page.evaluate(() => window.__said.filter(Boolean)), ['Disconnected from pdp11d', 'Running, ready']);
    } finally {
      await page.ctx.close();
      await srv.close();
    }
  });

  test('every response carries the Content-Security-Policy and X-Frame-Options; the UI runs under them', async () => {
    const srv = await mock();
    try {
      for (const path of ['', 'assets/js/main.js', 'api/status', 'nothing-here']) {
        const r = await fetch(`${srv.url}${path}`);
        assert.match(r.headers.get('content-security-policy') ?? '', /script-src 'self'; style-src 'self' 'unsafe-inline';.*frame-ancestors 'self'/, path);
        assert.equal(r.headers.get('x-frame-options'), 'SAMEORIGIN', path);
      }
      // the panel, console, dialogs and menus (violations fail the suite, see helpers.mjs)
      const page = await openPage(srv, { width: 1600, height: 900 });
      await page.click('button[aria-label="Help"]');
      for (const tab of ['panel', 'keys', 'tutorial', 'log', 'system']) await page.click(`.tabs button[data-tab="${tab}"]`);
      await page.keyboard.press('Escape');
      await page.click('.system-btn');
      await page.keyboard.press('Escape');
      await page.click('button[aria-label="View settings"]');
      await page.keyboard.press('Escape');
      await page.evaluate(() => window.pdp11Debug.app.openTutorial());
      await page.waitForSelector('.tutorial');
      assert.deepEqual(page.errors, []);
      await page.ctx.close();
    } finally {
      await srv.close();
    }
  });
});
