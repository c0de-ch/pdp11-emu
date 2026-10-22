// Unit tests of UI modules, run inside Chromium (the modules use the DOM):
// markdown safety, guest encoding, paddle poses, layout choice, lamp
// smoothing, octal readouts, login wording, instruction-rate formatting.
import { after, before, describe, test } from 'node:test';
import assert from 'node:assert/strict';
import { closeBrowser, mock, newContext, skip } from './helpers.mjs';

const MODULES = ['console.js', 'panel/controls.js', 'panel/lamps.js', 'ui/banners.js', 'ui/layout.js', 'ui/markdown.js',
  'ui/readouts.js', 'ui/systems.js', 'ui/toolbar.js'];

describe('UI modules', { skip }, () => {
  let srv;
  let page;
  before(async () => {
    srv = await mock({ off: true });
    page = await (await newContext()).newPage();
    // a blank document of the same origin, so module imports resolve; the
    // modules are loaded once here, again if a host network change aborted
    // the loading (net::ERR_NETWORK_CHANGED, see helpers.mjs)
    for (let attempt = 1; ; attempt++) {
      await page.goto(`${srv.url}assets/fonts/LICENSE-glass-tty-vt220.txt`);
      const err = await page.evaluate((mods) => Promise.all(mods.map((m) => import(`/assets/js/${m}`))).then(() => '', (e) => e.message), MODULES);
      if (!err) break;
      if (attempt === 3) throw new Error(`cannot load the UI modules: ${err}`);
    }
  });
  after(async () => {
    await page?.context().close();
    await srv?.close();
    await closeBrowser();
  });

  const run = (fn, arg) => page.evaluate(fn, arg);

  test('markdown: headings, lists, code, links; no HTML from the source', async () => {
    const r = await run(async () => {
      const { renderMarkdown } = await import('/assets/js/ui/markdown.js');
      const div = document.createElement('div');
      div.append(renderMarkdown([
        '## Log in', '', 'Log in as **root** (no `password`).', '',
        '- one', '- two *em*', '', '1. first', '2. second', '',
        '```', '<b>raw</b>', '```', '',
        '<img src=x onerror="window.__xss=1"> and <script>window.__xss=2</script>',
        '[ok](https://example.com) [bad](javascript:window.__xss=3)',
      ].join('\n')));
      document.body.append(div);
      return {
        html: div.innerHTML, xss: window.__xss ?? null,
        h3: div.querySelector('h3')?.textContent, ul: div.querySelectorAll('ul li').length, ol: div.querySelectorAll('ol li').length,
        code: div.querySelector('pre code')?.textContent, links: [...div.querySelectorAll('a')].map((a) => a.href),
        imgs: div.querySelectorAll('img,script').length,
      };
    });
    assert.equal(r.xss, null);
    assert.equal(r.imgs, 0, 'no elements from source HTML');
    assert.equal(r.h3, 'Log in');
    assert.equal(r.ul, 2);
    assert.equal(r.ol, 2);
    assert.equal(r.code, '<b>raw</b>');
    assert.deepEqual(r.links, ['https://example.com/']);
    assert.match(r.html, /<strong>root<\/strong>/);
    assert.match(r.html, /&lt;img src=x/);
  });

  test('encodeForGuest folds typographic characters and line ends', async () => {
    const r = await run(async () => {
      const { encodeForGuest } = await import('/assets/js/console.js');
      const s = (u) => String.fromCharCode(...u);
      return {
        paste: s(encodeForGuest('a\r\nb\nc\rd')),
        quotes: s(encodeForGuest('“hi” — it’s ok…')),
        other: s(encodeForGuest('café \u{1f600}')),
        typed: s(encodeForGuest('x\ny', { paste: false })),
      };
    });
    assert.equal(r.paste, 'a\rb\rc\rd');
    assert.equal(r.quotes, '"hi" - it\'s ok...');
    assert.equal(r.other, 'caf? ?');
    assert.equal(r.typed, 'x\ny', 'typed LF (Ctrl+J) stays LF');
  });

  test('paddle poses follow UI.md §3', async () => {
    const r = await run(async () => {
      const { poseFor } = await import('/assets/js/panel/controls.js');
      const geo = await (await fetch('/assets/panel/geometry.json')).json();
      const P = Object.fromEntries(geo.paddles.map((p) => [p.name, p]));
      const base = { sr: 0, enableHalt: 'enable', sinstSbus: 'inst', lampTest: false, pressed: {} };
      return {
        sr1: poseFor(P.SR5, { ...base, sr: 0o40 }), sr0: poseFor(P.SR5, base),
        sr21: poseFor(P.SR21, { ...base, sr: 0o10000000 }),
        dep: poseFor(P.DEP, { ...base, pressed: { DEP: true } }), exam: poseFor(P.EXAM, { ...base, pressed: { EXAM: true } }),
        lt: poseFor(P.LAMP_TEST, { ...base, lampTest: true }), halt: poseFor(P.ENABLE_HALT, { ...base, enableHalt: 'halt' }),
        bus: poseFor(P.SINST_SBUS, { ...base, sinstSbus: 'bus' }), rest: poseFor(P.START, base),
        types: geo.paddles.map((p) => p.type).join(','), colors: geo.paddles.map((p) => p.color[0]).join(''),
      };
    });
    assert.deepEqual({ ...r, types: undefined, colors: undefined }, {
      sr1: 'back', sr0: 'rest', sr21: 'back', dep: 'back', exam: 'fwd', lt: 'back', halt: 'fwd', bus: 'fwd', rest: 'rest', types: undefined, colors: undefined,
    });
    assert.equal(r.types, `${'sr,'.repeat(22)}momentary,momentary,momentary,momentary,momentary,latch,latch,momentary`);
    // SR colours in octal groups (21 rose | 20-18 mag | ...), LAMP TEST cream, controls alternate
    assert.equal(r.colors, 'rmmmrrrmmmrrrmmmrrrmmmbrmrmrmr');
  });

  test('geometry: lamps in protocol order, 22 + 16 + status + knob lamps', async () => {
    const g = await run(async () => (await fetch('/assets/panel/geometry.json')).json());
    assert.equal(g.lamps.length, 64);
    assert.deepEqual(g.lamps.slice(0, 3).map((l) => l.id), ['A0', 'A1', 'A2']);
    assert.equal(g.lamps[22].id, 'D0');
    assert.equal(g.lamps[38].id, 'PARITY_LOW');
    assert.equal(g.lamps[48].id, 'DATA_SPACE');
    assert.equal(g.lamps[48].name, 'DATA');
    assert.equal(g.lamps[63].id, 'DISPLAY_REG');
    assert.deepEqual(g.viewBox, [-10.1, -9.9, 484.4, 260.4]);
    // A21 is leftmost, A0 rightmost, on the 14.31 mm grid
    assert.ok(Math.abs(g.lamps[21].cx - 25.25) < 0.01 && Math.abs(g.lamps[0].cx - 325.76) < 0.01);
    assert.equal(g.knobs[0].positions[1], 'CONS PHY');
    assert.deepEqual(g.knobs[1].lamps.slice(0, 4), [61, 60, 62, 63]);
  });

  test('layout: side by side on wide screens, stacked on narrow ones, mobile on phones', async () => {
    const r = await run(async () => {
      const { computeLayout } = await import('/assets/js/ui/layout.js');
      // a console model: Glass TTY 10 x 20 px cells at 1x; VT323 0.4 x 1.0 em
      const fit = (box) => {
        if (box.glass) return { w: 800 * box.glass, h: 480 * box.glass, rowH: 20 * box.glass, glass: true };
        const k = Math.floor(Math.min(box.w / 800, box.h / 480));
        if (k >= 1) return { w: 800 * k, h: 480 * k, rowH: 20 * k, glass: true };
        const size = Math.floor(Math.min(box.w / 32, box.h / 24));
        return { w: size * 32, h: size * 24, rowH: size, glass: false };
      };
      const L = (W, H) => computeLayout({ W, H, showPanel: true, showConsole: true, consoleChrome: { w: 22, h: 56 }, consoleFit: fit });
      return { hd: L(1904, 1020), wide: L(2544, 1380), narrow: L(1100, 1000), phone: L(374, 760), panelOnly: computeLayout({ W: 1200, H: 600, showPanel: true, showConsole: false, consoleChrome: { w: 0, h: 0 }, consoleFit: fit }) };
    });
    assert.equal(r.hd.layout, 'side');
    assert.equal(r.wide.layout, 'side');
    assert.ok(r.wide.panel.w > 1300, `wide panel ${r.wide.panel.w}`);
    assert.equal(r.narrow.layout, 'stack');
    assert.equal(r.phone.layout, 'mobile');
    assert.equal(r.panelOnly.panel.w, Math.floor(600 * (484.4 / 260.4)));
    for (const k of ['hd', 'wide', 'narrow']) {
      const x = r[k];
      const ch = x.consoleBox.h;
      assert.ok(x.panel.w > 0 && ch > 0, k);
    }
  });

  test('layout: without a readable arrangement 24 rows of 13 px stay for the console, else the page scrolls; landscape phones fit the height', async () => {
    const r = await run(async () => {
      const { computeLayout } = await import('/assets/js/ui/layout.js');
      // the console model of the previous test
      const fit = (box) => {
        if (box.glass) return { w: 800 * box.glass, h: 480 * box.glass, rowH: 20 * box.glass, glass: true };
        const k = Math.floor(Math.min(box.w / 800, box.h / 480));
        if (k >= 1) return { w: 800 * k, h: 480 * k, rowH: 20 * k, glass: true };
        const size = Math.floor(Math.min(box.w / 32, box.h / 24));
        return { w: size * 32, h: size * 24, rowH: size, glass: false };
      };
      // the same at a device pixel ratio of 1.25: cells are whole device pixels (13 px fonts: 17 device px, 13.6 px)
      const fit125 = (box) => {
        const t = fit(box);
        if (t.glass) return t;
        const cell = (size) => Math.ceil(size * 1.25) / 1.25;
        let size = t.rowH;
        while (size > 6 && Math.round(cell(size) * 24) > box.h) size--;
        return { w: size * 32, h: Math.round(cell(size) * 24), rowH: cell(size), glass: false };
      };
      const L = (W, H, pageH, consoleFit = fit) => computeLayout({ W, H, pageH, showPanel: true, showConsole: true, consoleChrome: { w: 22, h: 56 }, consoleFit });
      const s125 = L(750, 700, 700, fit125);
      return { stack: L(750, 700), small: L(800, 560), landscape: L(828, 300, 374), portrait: L(374, 760, 828), s125, rows125: fit125(s125.consoleBox).rowH };
    });
    assert.equal(r.stack.layout, 'stack');
    assert.ok(r.stack.consoleBox.h >= 24 * 13 && r.stack.consoleBox.h < 24 * 13 + 2, `room for 24 rows of 13 px (${r.stack.consoleBox.h})`);
    assert.equal(r.s125.layout, 'stack');
    assert.ok(r.rows125 >= 13, `DPR 1.25: rows of ${r.rows125} px in ${r.s125.consoleBox.h} px`);
    assert.ok(r.stack.panel.w >= 360, `panel ${r.stack.panel.w}`);
    assert.equal(r.small.layout, 'mobile', 'the panel would be narrower than 360 px: scroll instead');
    assert.equal(r.landscape.layout, 'mobile');
    assert.ok(r.landscape.panel.h <= 374 && r.landscape.panel.h >= 372, `landscape: as high as the viewport (${r.landscape.panel.h})`);
    assert.equal(r.portrait.panel.w, 374, 'portrait: the full width');
  });

  test('lamp smoothing converges with the preset time constants', async () => {
    const r = await run(async () => {
      const { LampBank, LAMP_PRESETS } = await import('/assets/js/panel/lamps.js');
      const geo = await (await fetch('/assets/panel/geometry.json')).json();
      const layer = document.createElement('div');
      document.body.append(layer);
      const bank = new LampBank(layer, geo);
      const out = {};
      for (const name of Object.keys(LAMP_PRESETS)) {
        bank.setPreset(name);
        bank.b.fill(0);
        bank.target.fill(0);
        bank.target[42] = 1; // RUN
        bank.running = true; // drive tick() by hand with exact times
        let t = 1000;
        bank.last = t;
        const tau = LAMP_PRESETS[name].rise;
        for (let i = 0; i < 6; i++) bank.tick((t += tau / 6));
        const afterTau = bank.b[42];
        bank.target[42] = 0;
        for (let i = 0; i < 6; i++) bank.tick((t += LAMP_PRESETS[name].fall / 6));
        out[name] = { afterTau, afterFall: bank.b[42], opacity: Number(bank.els[42].style.opacity) };
      }
      bank.target.fill(0.25);
      bank.b.fill(0.25);
      bank.tick(1e6);
      out.gamma = Number(bank.els[0].style.opacity);
      bank.target.fill(0.01);
      bank.b.fill(0.01);
      bank.tick(2e6);
      out.dim = Number(bank.els[0].style.opacity);
      bank.running = false;
      layer.remove();
      return out;
    });
    const e1 = 1 - Math.exp(-1);
    for (const name of ['led', 'pidp11', 'incandescent', 'reduced']) {
      assert.ok(Math.abs(r[name].afterTau - e1) < 0.01, `${name}: 63 % after one rise time (${r[name].afterTau})`);
      assert.ok(Math.abs(r[name].afterFall - e1 * Math.exp(-1)) < 0.01, `${name}: decays with the fall time`);
    }
    assert.ok(Math.abs(r.gamma - 0.25 ** (1 / 2.2)) < 0.005, `opacity = b^(1/2.2) (${r.gamma})`);
    assert.equal(r.dim, 0, 'below 1/64 the lamp is off');
  });

  test('noLogin: from the catalog, else inferred from missing credentials (older pdp11d)', async () => {
    const r = await run(async () => {
      const { noLogin } = await import('/assets/js/ui/systems.js');
      const { readyMessage } = await import('/assets/js/ui/banners.js');
      const text = (sys) => {
        const d = document.createElement('div');
        d.append(...readyMessage(sys));
        return d.textContent;
      };
      const root = [{ user: 'root', password: '' }];
      return {
        flag: noLogin({ noLogin: true, credentials: [{ user: 'SYSTEM', password: 'SYSTEM' }] }),
        flagOff: noLogin({ noLogin: false, credentials: root }),
        inferred: noLogin({ credentials: null }),
        inferredEmpty: noLogin({ credentials: [] }),
        withCreds: noLogin({ credentials: root }),
        bsd: text({ short: '2.11BSD', credentials: root }),
        v7: text({ short: 'UNIX V7', credentials: [{ user: 'root', password: 'root' }] }),
        rt11: text({ short: 'RT-11', noLogin: true }),
        rsx: text({ short: 'RSX-11M-PLUS', noLogin: true, credentials: [{ user: 'SYSTEM', password: 'SYSTEM' }] }),
        legacy: text({ short: 'RT-11', credentials: null }),
      };
    });
    assert.equal(r.flag, true, 'noLogin wins over credentials (terminal-line accounts)');
    assert.equal(r.flagOff, false);
    assert.equal(r.inferred, true);
    assert.equal(r.inferredEmpty, true);
    assert.equal(r.withCreds, false);
    assert.equal(r.bsd, '2.11BSD is up — log in as root (no password)');
    assert.equal(r.v7, 'UNIX V7 is up — log in as root (password root)');
    assert.equal(r.rt11, 'RT-11 is up — the console is ready (no login needed)');
    assert.equal(r.rsx, 'RSX-11M-PLUS is up — the console is ready (no login needed)', 'never a login instruction');
    assert.equal(r.legacy, 'RT-11 is up — the console is ready (no login needed)');
  });

  test('instruction rate: guest instructions per second, never "MIPS"', async () => {
    const r = await run(async () => {
      const { fmtInstrRate } = await import('/assets/js/ui/toolbar.js');
      return [12.4, 1.5, 0.98, 0.021, 0.0004, 0, NaN].map((m) => fmtInstrRate(m));
    });
    assert.deepEqual(r, ['12.4 M instr/s', '1.50 M instr/s', '980 k instr/s', '21 k instr/s', '400 instr/s', '', '']);
  });

  test('octal readout bits from a lamp frame', async () => {
    const r = await run(async () => {
      const { lampBits } = await import('/assets/js/ui/readouts.js');
      const f = new Uint8Array(64);
      f[21] = 255; // A21
      f[0] = 200; // A0
      f[1] = 100; // A1 below half: off
      f[22 + 15] = 255; // D15
      f[22 + 3] = 128; // D3
      return lampBits(f);
    });
    assert.equal(r.address, 0o10000001);
    assert.equal(r.data, 0o100010);
  });
});
