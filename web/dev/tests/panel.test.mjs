// Front panel: structure, pointer/keyboard interaction and the messages
// they send (docs/UI.md §3-4, docs/PROTOCOL.md §1.3).
import { after, before, describe, test } from 'node:test';
import assert from 'node:assert/strict';
import { centre, closeBrowser, mock, openPage, received, skip, sleep, waitFor } from './helpers.mjs';

describe('front panel', { skip }, () => {
  let srv;
  let page;
  before(async () => {
    srv = await mock();
    page = await openPage(srv, { width: 1920, height: 1080 });
    await page.waitForSelector('.panel-frame .lamp');
  });
  after(async () => {
    await page?.ctx.close();
    await srv?.close();
    await closeBrowser();
  });

  const serverSR = () => page.evaluate(() => window.pdp11Debug.state.controls.sr);
  const sent = (pred) => page.evaluate((src) => {
    // eslint-disable-next-line no-new-func
    const f = new Function('m', `return (${src})(m)`);
    return window.pdp11Debug.sent.filter((e) => e.msg && f(e.msg)).map((e) => e.msg);
  }, pred.toString());

  test('renders 64 lamps, 30 paddles, 2 knobs and the key', async () => {
    const counts = await page.evaluate(() => ({
      lamps: document.querySelectorAll('.lamp-layer .lamp').length,
      paddles: document.querySelectorAll('.panel-svg .paddle').length,
      poses: document.querySelectorAll('.panel-svg .paddle .pose').length,
      knobs: document.querySelectorAll('.panel-svg .knob').length,
      key: document.querySelectorAll('.panel-svg .key').length,
      hitPaddles: document.querySelectorAll('.hit-paddle').length,
      switches: document.querySelectorAll('.hit-paddle[role="switch"]').length,
      buttons: document.querySelectorAll('.hit-paddle[role="button"]').length,
      sliders: document.querySelectorAll('.hit[role="slider"]').length,
      labels: document.querySelectorAll('.panel-svg .labels text').length,
      lampsHidden: document.querySelector('.lamp-layer').getAttribute('aria-hidden'),
    }));
    assert.deepEqual(counts, {
      lamps: 64, paddles: 30, poses: 90, knobs: 2, key: 1, hitPaddles: 30, switches: 24, buttons: 6, sliders: 3, labels: 71, lampsHidden: 'true',
    });
  });

  test('lamps are small round discs positioned over their windows', async () => {
    const r = await page.evaluate(() => {
      const geo = window.pdp11Debug.app.geometry;
      const frame = document.querySelector('.panel-frame').getBoundingClientRect();
      const px = frame.width / geo.viewBox[2];
      const run = geo.lamps.find((l) => l.id === 'RUN');
      const el = document.querySelector('.lamp[data-lamp="RUN"]');
      const b = el.getBoundingClientRect();
      const cs = getComputedStyle(el);
      return {
        dx: Math.abs(b.x + b.width / 2 - (frame.x + (run.cx - geo.viewBox[0]) * px)),
        dy: Math.abs(b.y + b.height / 2 - (frame.y + (run.cy - geo.viewBox[1]) * px)),
        sizeMM: b.width / px, round: cs.borderRadius, willChange: cs.willChange, filter: cs.filter,
      };
    });
    assert.ok(r.dx < 1 && r.dy < 1, `lamp centred (${r.dx}, ${r.dy})`);
    assert.ok(Math.abs(r.sizeMM - 8.2) < 0.1, `5.2 mm disc + 1.5 mm halo (${r.sizeMM})`);
    assert.equal(r.round, '50%');
    assert.equal(r.willChange, 'opacity');
    assert.equal(r.filter, 'none');
  });

  test('lamp frames are smoothed towards the received duties', async () => {
    await waitFor(() => page.evaluate(() => {
      const f = window.pdp11Debug.lastFrame;
      const s = window.pdp11Debug.smoothed;
      return f && s && f[42] > 200 && s[42] > 0.7; // RUN while the OS runs
    }), { message: 'RUN lamp smoothed on' });
  });

  test('clicking an SR paddle toggles it and sends the whole switch register', async () => {
    await page.evaluate(() => window.pdp11Debug.app.send({ type: 'sr', value: 0 }));
    await waitFor(async () => (await serverSR()) === 0);
    const p = await centre(page, '.hit-paddle[data-name="SR5"]', 0.5, 0.75);
    await page.mouse.click(p.x, p.y);
    await waitFor(async () => (await serverSR()) === 0o40, { message: 'server SR = 040' });
    const msgs = received(srv, (m) => m.type === 'sr');
    assert.equal(msgs.at(-1).value, 0o40);
    assert.equal(await page.getAttribute('.panel-svg .paddle[data-name="SR5"]', 'data-pose'), 'back');
    assert.equal(await page.getAttribute('.hit-paddle[data-name="SR5"]', 'aria-checked'), 'true');
    await page.mouse.click(p.x, p.y);
    await waitFor(async () => (await serverSR()) === 0, { message: 'server SR = 0 again' });
    assert.equal(await page.getAttribute('.panel-svg .paddle[data-name="SR5"]', 'data-pose'), 'rest');
  });

  test('dragging across SR paddles paints the first paddle\'s new state', async () => {
    const a = await centre(page, '.hit-paddle[data-name="SR3"]', 0.5, 0.8);
    const c = await centre(page, '.hit-paddle[data-name="SR0"]', 0.5, 0.8);
    await page.mouse.move(a.x, a.y);
    await page.mouse.down();
    for (let i = 1; i <= 10; i++) await page.mouse.move(a.x + ((c.x - a.x) * i) / 10, a.y);
    await page.mouse.up();
    await waitFor(async () => (await serverSR()) === 0o17, { message: 'SR3..SR0 set' });
    // painting back to 0 starting on a set paddle
    await page.mouse.move(c.x, c.y);
    await page.mouse.down();
    for (let i = 1; i <= 10; i++) await page.mouse.move(c.x + ((a.x - c.x) * i) / 10, c.y);
    await page.mouse.up();
    await waitFor(async () => (await serverSR()) === 0, { message: 'SR3..SR0 cleared' });
  });

  test('a vertical flick sets a paddle explicitly', async () => {
    const p = await centre(page, '.hit-paddle[data-name="SR10"]', 0.5, 0.7);
    const mm = p.box.width / 14.31;
    await page.mouse.move(p.x, p.y);
    await page.mouse.down();
    await page.mouse.move(p.x, p.y - 4 * mm);
    await page.mouse.move(p.x, p.y - 9 * mm);
    await page.mouse.up();
    await waitFor(async () => (await serverSR()) === 0o2000, { message: 'flick up sets SR10' });
    await page.mouse.move(p.x, p.y - 9 * mm);
    await page.mouse.down();
    await page.mouse.move(p.x, p.y - 4 * mm);
    await page.mouse.move(p.x, p.y + 1 * mm);
    await page.mouse.up();
    await waitFor(async () => (await serverSR()) === 0, { message: 'flick down clears SR10' });
  });

  test('momentary switches: press on pointerdown, release on pointerup', async () => {
    await page.evaluate(() => window.pdp11Debug.app.send({ type: 'latch', name: 'ENABLE_HALT', value: 'enable' }));
    const p = await centre(page, '.hit-paddle[data-name="EXAM"]', 0.5, 0.8);
    const before = received(srv, (m) => m.type === 'momentary').length;
    await page.mouse.move(p.x, p.y);
    await page.mouse.down();
    await waitFor(() => received(srv, (m) => m.type === 'momentary').length > before, { message: 'press sent' });
    assert.deepEqual(received(srv, (m) => m.type === 'momentary').at(-1), { type: 'momentary', name: 'EXAM', active: true, req: received(srv, (m) => m.type === 'momentary').at(-1).req });
    assert.equal(await page.getAttribute('.panel-svg .paddle[data-name="EXAM"]', 'data-pose'), 'fwd');
    await sleep(50);
    await page.mouse.up();
    await waitFor(() => received(srv, (m) => m.type === 'momentary' && m.name === 'EXAM' && m.active === false).length > 0, { message: 'release sent' });
    // the pressed pose is held at least 120 ms, then released
    await waitFor(async () => (await page.getAttribute('.panel-svg .paddle[data-name="EXAM"]', 'data-pose')) === 'rest', { message: 'pose released' });
    // the server ignored EXAM (CPU running) and told this client why
    await waitFor(() => page.evaluate(() => [...document.querySelectorAll('.toast-text')].some((t) => /halted/.test(t.textContent))), { message: 'hint toast' });
  });

  test('a held momentary switch is released when the window loses focus', async () => {
    const p = await centre(page, '.hit-paddle[data-name="LAMP_TEST"]', 0.5, 0.8);
    await page.mouse.move(p.x, p.y);
    await page.mouse.down();
    await waitFor(() => page.evaluate(() => window.pdp11Debug.state.controls.lampTest === true), { message: 'lamp test on' });
    await waitFor(() => page.evaluate(() => window.pdp11Debug.lastFrame?.every((v) => v === 255)), { message: 'all lamps lit' });
    await page.evaluate(() => window.dispatchEvent(new Event('blur')));
    await waitFor(() => page.evaluate(() => window.pdp11Debug.state.controls.lampTest === false), { message: 'lamp test released on blur' });
    await page.mouse.up();
  });

  test('knobs: right half turns clockwise, left half anticlockwise, wheel steps detents', async () => {
    const pos = () => page.evaluate(() => window.pdp11Debug.state.controls.addrKnob);
    const start = await pos();
    const r = await centre(page, '.hit-knob[data-knob="addr"]', 0.8, 0.5);
    await page.mouse.click(r.x, r.y);
    await waitFor(async () => (await pos()) === (start + 1) % 8, { message: 'clockwise' });
    const l = await centre(page, '.hit-knob[data-knob="addr"]', 0.2, 0.5);
    await page.mouse.click(l.x, l.y);
    await waitFor(async () => (await pos()) === start, { message: 'anticlockwise' });
    await page.mouse.move(r.x, r.y);
    await page.mouse.wheel(0, 130);
    await waitFor(async () => (await pos()) === (start + 2) % 8, { message: 'wheel: two detents' });
    const text = await page.getAttribute('.hit-knob[data-knob="addr"]', 'aria-valuetext');
    const names = ['PROG PHY', 'CONS PHY', 'KERNEL D', 'SUPER D', 'USER D', 'USER I', 'SUPER I', 'KERNEL I'];
    assert.equal(text, names[(start + 2) % 8]);
    // the knob rotates to 22.5 + 45k degrees (no backwards spin on wrap)
    const t = await page.evaluate(() => document.querySelector('.knob[data-knob="addr"] .knob-rot').style.transform);
    const deg = Number(t.match(/rotate\((-?[\d.]+)deg\)/)[1]);
    assert.equal(((deg % 360) + 360) % 360, 22.5 + 45 * ((start + 2) % 8));
    // and the selected-position lamp follows the server
    await waitFor(() => page.evaluate((k) => window.pdp11Debug.lastFrame?.[[59, 55, 54, 53, 52, 56, 57, 58][k]] === 255, (start + 2) % 8), { message: 'knob lamp' });
  });

  test('dragging a knob around its centre snaps to detents', async () => {
    const knob = await centre(page, '.hit-knob[data-knob="data"]');
    const rad = knob.box.width * 0.4;
    const at = (deg) => ({ x: knob.x + rad * Math.sin((deg * Math.PI) / 180), y: knob.y - rad * Math.cos((deg * Math.PI) / 180) });
    const s0 = at(67.5);
    await page.mouse.move(s0.x, s0.y);
    await page.mouse.down();
    for (const d of [80, 100, 120, 140, 157.5]) {
      const p = at(d);
      await page.mouse.move(p.x, p.y);
    }
    await page.mouse.up();
    await waitFor(() => page.evaluate(() => window.pdp11Debug.state.controls.dataKnob === 3), { message: 'DATA knob at DISPLAY REGISTER' });
    await page.evaluate(() => window.pdp11Debug.app.send({ type: 'knob', name: 'data', pos: 1 }));
  });

  test('keyboard: octal digits shift into SR, letters press switches, brackets turn knobs', async () => {
    await page.evaluate(() => window.pdp11Debug.app.send({ type: 'sr', value: 0 }));
    await waitFor(async () => (await serverSR()) === 0);
    await page.focus('.hit-paddle[data-name="SR0"]');
    await page.keyboard.type('1750');
    await waitFor(async () => (await serverSR()) === 0o1750, { message: 'SR 001750' });
    await page.keyboard.press('Backspace');
    await waitFor(async () => (await serverSR()) === 0o175, { message: 'SR 000175' });
    await page.keyboard.press('Escape');
    await waitFor(async () => (await serverSR()) === 0, { message: 'SR cleared' });
    // arrows move the roving focus; up sets the focused bit
    await page.keyboard.press('ArrowLeft');
    assert.equal(await page.evaluate(() => document.activeElement.dataset.name), 'SR1');
    await page.keyboard.press('ArrowUp');
    await waitFor(async () => (await serverSR()) === 2, { message: 'SR1 up' });
    await page.keyboard.press('ArrowDown');
    await waitFor(async () => (await serverSR()) === 0, { message: 'SR1 down' });
    // L = LOAD ADRS (held while the key is down)
    await page.keyboard.down('l');
    await waitFor(() => received(srv, (m) => m.type === 'momentary' && m.name === 'LOAD_ADRS' && m.active).length > 0, { message: 'LOAD ADRS pressed' });
    await page.keyboard.up('l');
    await waitFor(() => received(srv, (m) => m.type === 'momentary' && m.name === 'LOAD_ADRS' && !m.active).length > 0, { message: 'LOAD ADRS released' });
    // ] turns the ADDRESS knob clockwise
    const k0 = await page.evaluate(() => window.pdp11Debug.state.controls.addrKnob);
    await page.keyboard.press(']');
    await waitFor(() => page.evaluate((k) => window.pdp11Debug.state.controls.addrKnob === (k + 1) % 8, k0), { message: '] turns the knob' });
    // H toggles ENABLE/HALT
    await page.keyboard.press('h');
    await waitFor(() => page.evaluate(() => window.pdp11Debug.state.controls.enableHalt === 'halt'), { message: 'H: HALT' });
    await page.keyboard.press('h');
    await waitFor(() => page.evaluate(() => window.pdp11Debug.state.controls.enableHalt === 'enable'), { message: 'H: ENABLE' });
  });

  test('console switches work when halted: LOAD ADRS, DEP, EXAM', async () => {
    const st = () => page.evaluate(() => window.pdp11Debug.state);
    const press = async (name) => {
      await page.evaluate((n) => window.pdp11Debug.app.panelActions().press(n, true), name);
      await sleep(30);
      await page.evaluate((n) => window.pdp11Debug.app.panelActions().press(n, false), name);
      await sleep(30);
    };
    await page.evaluate(() => window.pdp11Debug.app.panelActions().latch('ENABLE_HALT', 'halt'));
    await waitFor(async () => (await st()).run === 'halted', { message: 'halted by the HALT switch' });
    await page.evaluate(() => window.pdp11Debug.app.panelActions().sr(0o1000));
    await press('LOAD_ADRS');
    await waitFor(async () => (await st()).console.addr === 0o1000, { message: 'console address 1000' });
    await page.evaluate(() => window.pdp11Debug.app.panelActions().sr(0o123456));
    await press('DEP');
    await page.evaluate(() => window.pdp11Debug.app.panelActions().sr(0o654321 & 0o177777));
    await press('DEP'); // steps to 1002
    await waitFor(async () => (await st()).console.addr === 0o1002, { message: 'DEP stepped to 1002' });
    await page.evaluate(() => window.pdp11Debug.app.panelActions().sr(0o1000));
    await press('LOAD_ADRS');
    await press('EXAM');
    // DATA PATHS shows the examined word on the DATA lamps
    await waitFor(() => page.evaluate(() => {
      const f = window.pdp11Debug.lastFrame;
      let d = 0;
      for (let i = 0; i < 16; i++) if (f[22 + i] >= 128) d |= 1 << i;
      return d === 0o123456;
    }), { message: 'DATA lamps show 123456' });
    await page.evaluate(() => window.pdp11Debug.app.panelActions().latch('ENABLE_HALT', 'enable'));
  });

  test('turning the key to OFF asks first; "Pull the plug" powers off', async () => {
    await page.evaluate(() => window.pdp11Debug.app.send({ type: 'key', pos: 'power' }));
    const k = await centre(page, '.hit-key', 0.2, 0.5);
    await page.mouse.click(k.x, k.y);
    await page.waitForSelector('dialog.dialog[open]');
    const title = await page.textContent('dialog.dialog[open] h2');
    assert.equal(title, 'Power off');
    await page.click('dialog.dialog[open] button[data-value="plug"]');
    await waitFor(() => page.evaluate(() => window.pdp11Debug.state.power === 'off' && window.pdp11Debug.state.run === 'off'), { message: 'powered off' });
    await waitFor(() => page.evaluate(() => window.pdp11Debug.lastFrame?.every((v) => v === 0)), { message: 'lamps dark' });
    // clockwise again: POWER (no confirmation)
    const r = await centre(page, '.hit-key', 0.8, 0.5);
    await page.mouse.click(r.x, r.y);
    await waitFor(() => page.evaluate(() => window.pdp11Debug.state.controls.key === 'power'), { message: 'key at POWER' });
    assert.equal(await page.getAttribute('.hit-key', 'aria-valuetext'), 'POWER');
    assert.deepEqual(page.errors, []);
  });
});

describe('two windows on one panel', { skip }, () => {
  after(closeBrowser);

  test('switches flipped at the same time in two windows are all kept (each sends the whole register)', async () => {
    const srv = await mock();
    const a = await openPage(srv, { width: 1600, height: 900 });
    const b = await openPage(srv, { width: 1600, height: 900 });
    const sr = (p) => p.evaluate(() => window.pdp11Debug.app.panel.controls().sr);
    try {
      await a.evaluate(() => window.pdp11Debug.app.send({ type: 'sr', value: 0 }));
      await waitFor(async () => (await sr(a)) === 0 && (await sr(b)) === 0, { message: 'SR 0' });
      // A raises SR0-SR5 while B raises SR6-SR11, faster than the state echo
      for (let i = 0; i < 6; i++) {
        await a.evaluate((bit) => window.pdp11Debug.app.panel.input.setBit(bit, 1), i);
        await b.evaluate((bit) => window.pdp11Debug.app.panel.input.setBit(bit, 1), i + 6);
      }
      await waitFor(async () => srv.machine.controls.sr === 0o7777 && (await sr(a)) === 0o7777 && (await sr(b)) === 0o7777,
        { timeout: 5000, message: 'all twelve switches up' });
      // and they stay up once the unconfirmed changes have expired
      await sleep(1700);
      assert.deepEqual([srv.machine.controls.sr, await sr(a), await sr(b)], [0o7777, 0o7777, 0o7777]);
      // clearing works the same way
      for (let i = 0; i < 6; i++) {
        await b.evaluate((bit) => window.pdp11Debug.app.panel.input.setBit(bit, 0), i);
        await a.evaluate((bit) => window.pdp11Debug.app.panel.input.setBit(bit, 0), i + 6);
      }
      await waitFor(async () => srv.machine.controls.sr === 0 && (await sr(a)) === 0 && (await sr(b)) === 0, { timeout: 5000, message: 'all down' });
      assert.deepEqual(a.errors, []);
      assert.deepEqual(b.errors, []);
    } finally {
      await a.ctx.close();
      await b.ctx.close();
      await srv.close();
    }
  });

  test('a toggle the server never confirmed expires after 1.5 s: a later write by another viewer is not overridden', async () => {
    const srv = await mock();
    const page = await openPage(srv, { width: 1600, height: 900 });
    try {
      const sent = await page.evaluate(() => {
        const p = window.pdp11Debug.app.panel;
        const send = p.send;
        const out = [];
        p.send = (m) => out.push(m.value);
        try {
          p.setSR((p.server.sr | 0o20) >>> 0); // SR4 up, never confirmed
          p.sr.t -= 2000; // ... two seconds ago
          p.setServerControls({ ...p.server, sr: 0o200 }); // another viewer's write
        } finally {
          p.send = send;
        }
        return { out, view: p.controls().sr };
      });
      assert.equal(sent.out.length, 1, `nothing sent again (${JSON.stringify(sent.out)})`);
      assert.equal(sent.view, 0o200, "the other viewer's value stands");
      assert.deepEqual(page.errors, []);
    } finally {
      await page.ctx.close();
      await srv.close();
    }
  });
});

describe('lamp test precedence (PANEL_LOGIC §3-4)', { skip }, () => {
  after(closeBrowser);

  test('key OFF: all dark; LAMP TEST: all lit; the knob lamps whenever the key is on; the CPU lamps only while halted or running', async () => {
    const srv = await mock({ autostart: false });
    const page = await openPage(srv, { width: 1600, height: 900 });
    const frame = (pred) => page.evaluate(() => window.pdp11Debug.lastFrame && Array.from(window.pdp11Debug.lastFrame)).then((f) => !!f && pred(f));
    const run = (want) => waitFor(() => page.evaluate((w) => window.pdp11Debug.state.run === w, want), { timeout: 10000, message: want });
    // the selected ADDRESS and DATA positions' lamps (geometry), and only them among the knob lamps
    const knobLamps = () => page.evaluate(() => {
      const { geometry: g, state: st } = window.pdp11Debug.app;
      return [g.knobs[0].lamps[st.controls.addrKnob], g.knobs[1].lamps[st.controls.dataKnob]];
    });
    const knobs = (lit) => (f) => f.slice(52).every((v, i) => v === (lit.includes(52 + i) ? 255 : 0));
    const cpuDark = (f) => f.slice(0, 52).every((v) => v === 0);
    try {
      // key at POWER, nothing started: the knob lamps, and they follow the knobs
      assert.equal(await page.evaluate(() => window.pdp11Debug.state.run), 'off');
      let lit = await knobLamps();
      await waitFor(() => frame((f) => knobs(lit)(f) && cpuDark(f)), { message: 'off: knob lamps only' });
      await page.evaluate(() => window.pdp11Debug.app.panelActions().knob('addr', 3));
      await page.evaluate(() => window.pdp11Debug.app.panelActions().knob('data', 3));
      await waitFor(() => page.evaluate(() => window.pdp11Debug.state.controls.addrKnob === 3 && window.pdp11Debug.state.controls.dataKnob === 3), { message: 'knobs turned' });
      lit = await knobLamps();
      assert.deepEqual(lit, [53, 63], 'SUPER D and DISPLAY REGISTER');
      await waitFor(() => frame((f) => knobs(lit)(f) && cpuDark(f)), { message: 'off: the new knob positions' });
      // Boot: starting (knob lamps only), running (CPU lamps too), halted
      await page.evaluate(() => window.pdp11Debug.app.send({ type: 'boot' }));
      await run('starting');
      assert.ok(await frame((f) => knobs(lit)(f) && cpuDark(f)), 'starting: knob lamps only');
      await run('running');
      await waitFor(() => frame((f) => knobs(lit)(f) && f[42] > 0), { message: 'running: knob lamps and RUN' });
      await page.evaluate(() => window.pdp11Debug.app.panelActions().latch('ENABLE_HALT', 'halt'));
      await run('halted');
      await waitFor(() => frame((f) => knobs(lit)(f) && f[44] === 255), { message: 'halted: knob lamps and MASTER' });
      // in error (as after a failed power-on): knob lamps only ...
      await fetch(`${srv.url}mock/state`, { method: 'POST', body: JSON.stringify({ run: 'error', error: 'mock failure' }) });
      await waitFor(() => frame((f) => knobs(lit)(f) && cpuDark(f)), { message: 'error: knob lamps only' });
      // ... but LAMP TEST lights every lamp
      await page.evaluate(() => window.pdp11Debug.app.panelActions().press('LAMP_TEST', true));
      await waitFor(() => frame((f) => f.every((v) => v === 255)), { message: 'lamp test lights all 64 lamps' });
      // with the key at OFF nothing lights, not even LAMP TEST or the knob lamps
      await page.evaluate(() => window.pdp11Debug.app.send({ type: 'key', pos: 'off' }));
      await waitFor(() => frame((f) => f.every((v) => v === 0)), { message: 'key off: dark despite lamp test' });
      await page.evaluate(() => window.pdp11Debug.app.panelActions().press('LAMP_TEST', false));
      // the browser: a dim lamp (< 1/64 linear) is off
      await waitFor(() => page.evaluate(() => [...document.querySelectorAll('.lamp-layer .lamp')].every((e) => Number(e.style.opacity || 0) === 0)), { message: 'all lamp discs transparent' });
      assert.deepEqual(page.errors, []);
    } finally {
      await page.ctx.close();
      await srv.close();
    }
  });
});
