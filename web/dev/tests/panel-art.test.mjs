// The front panel's art (docs/UI.md §3): vectors without filters, every pose
// drawn, what its states look like in pixels, the labels at every panel width.
import { after, before, describe, test } from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { closeBrowser, mock, newContext, openPage, skip, sleep } from './helpers.mjs';

const DEV = join(dirname(fileURLToPath(import.meta.url)), '..');

test('geometry.json is what gen-geometry.mjs writes', () => {
  execFileSync(process.execPath, [join(DEV, 'gen-geometry.mjs'), '--check'], { stdio: 'pipe' });
});

/** Mean colour and darkest luminance of a piece of the page. */
async function pixels(page, clip) {
  const png = await page.screenshot({ clip });
  return page.evaluate(async (b64) => {
    const im = new Image();
    im.src = `data:image/png;base64,${b64}`;
    await im.decode();
    const c = new OffscreenCanvas(im.width, im.height);
    const g = c.getContext('2d');
    g.drawImage(im, 0, 0);
    const d = g.getImageData(0, 0, im.width, im.height).data;
    const sum = [0, 0, 0];
    let darkest = 255;
    for (let i = 0; i < d.length; i += 4) {
      for (let k = 0; k < 3; k++) sum[k] += d[i + k];
      darkest = Math.min(darkest, 0.3 * d[i] + 0.59 * d[i + 1] + 0.11 * d[i + 2]);
    }
    return { mean: sum.map((v) => v / (d.length / 4)), darkest };
  }, png.toString('base64'));
}

describe('front panel art', { skip }, () => {
  let srv;
  let page;
  before(async () => {
    srv = await mock();
    page = await openPage(srv, { width: 1950, height: 1100 });
    await page.waitForSelector('.panel-frame .lamp');
  });
  after(async () => {
    await page?.ctx.close();
    await srv?.close();
    await closeBrowser();
  });

  /** A panel of its own at the page's top left, above everything, of a given width. */
  const panelOfWidth = (width) => page.evaluate(async (w) => {
    const { FrontPanel } = await import('./assets/js/panel/panel.js');
    window.artPanel?.destroy();
    document.getElementById('art-host')?.remove();
    const host = document.createElement('div');
    host.id = 'art-host';
    host.style.cssText = `position:fixed;left:0;top:0;z-index:10000;width:${w}px`;
    document.body.append(host);
    window.artPanel = new FrontPanel(host, window.pdp11Debug.app.geometry, {});
    await document.fonts.ready;
  }, width);
  const removePanel = () => page.evaluate(() => {
    window.artPanel?.destroy();
    window.artPanel = null;
    document.getElementById('art-host')?.remove();
  });
  const controls = (c) => page.evaluate((v) => window.artPanel.setServerControls({
    sr: 0, enableHalt: 'enable', sinstSbus: 'inst', addrKnob: 0, dataKnob: 0, key: 'power', lampTest: false, pressed: {}, ...v,
  }), c);

  test('vector art only: no filters or bitmaps, and every paint it names exists', async () => {
    await panelOfWidth(1340);
    const r = await page.evaluate(() => {
      const svg = document.querySelector('#art-host .panel-svg');
      const all = [...svg.querySelectorAll('*')];
      const missing = new Set();
      for (const e of all) {
        for (const a of ['fill', 'stroke', 'mask']) {
          const m = /url\(#([^)]+)\)/.exec(e.getAttribute(a) ?? '');
          if (m && !document.getElementById(m[1])) missing.add(m[1]);
        }
      }
      const ids = all.map((e) => e.id).filter(Boolean);
      return {
        banned: svg.querySelectorAll('filter, image, foreignObject').length,
        filtered: all.filter((e) => getComputedStyle(e).filter !== 'none').length,
        missing: [...missing],
        duplicates: ids.length - new Set(ids).size,
      };
    });
    assert.deepEqual(r, { banned: 0, filtered: 0, missing: [], duplicates: 0 });
  });

  test('every paddle is drawn in the poses it can take, and only those', async () => {
    await panelOfWidth(1340);
    const paddles = await page.evaluate(() => window.pdp11Debug.app.geometry.paddles.map((p) => {
      const g = document.querySelector(`#art-host .paddle[data-name="${p.name}"]`);
      const shapes = (pose) => g.querySelector(`.pose-${pose}`).querySelectorAll('path').length;
      return {
        name: p.name, rises: p.type === 'sr' || p.dir === 'up', presses: p.type === 'latch' || p.dir === 'down',
        rest: shapes('rest'), back: shapes('back'), fwd: shapes('fwd'),
        highlights: g.querySelectorAll('.paddle-hl').length,
      };
    }));
    assert.equal(paddles.length, 30);
    for (const p of paddles) {
      assert.ok(p.rest > 3, `${p.name} at rest`);
      assert.equal(p.back > 0, p.rises, `${p.name} raised`);
      assert.equal(p.fwd > 0, p.presses, `${p.name} pressed`);
      assert.equal(p.highlights, 1 + (p.rises ? 1 : 0) + (p.presses ? 1 : 0), `${p.name}: one highlight per pose`);
    }
  });

  test('the hit areas cover what they operate', async () => {
    await panelOfWidth(1340);
    const r = await page.evaluate(() => {
      const box = (q) => document.querySelector(`#art-host ${q}`).getBoundingClientRect();
      const centre = (b) => [(b.left + b.right) / 2, (b.top + b.bottom) / 2];
      const px = 1340 / window.pdp11Debug.app.geometry.viewBox[2];
      const off = [];
      for (const p of window.pdp11Debug.app.geometry.paddles) {
        const art = box(`.paddle[data-name="${p.name}"] .pose-rest`);
        const hit = box(`.hit-paddle[data-name="${p.name}"]`);
        if (art.left < hit.left - 0.5 || art.right > hit.right + 0.5 || art.bottom > hit.bottom + 0.5 || art.top < hit.top) off.push(p.name);
      }
      const apart = (art, hit) => Math.hypot(centre(box(art))[0] - centre(box(hit))[0], centre(box(art))[1] - centre(box(hit))[1]) / px;
      return { off, addr: apart('.knob[data-knob="addr"] circle:nth-of-type(2)', '.hit-knob[data-knob="addr"]'), key: apart('.key-static', '.hit-key') };
    });
    assert.deepEqual(r.off, []);
    assert.ok(r.addr < 1, `the ADDRESS knob's skirt on its hit area (${r.addr} mm)`);
    assert.ok(r.key < 1, `the key on its hit area (${r.key} mm)`);
  });

  test('the moulding keeps an edge of its own inside the frame', async () => {
    await panelOfWidth(1340);
    const r = await page.evaluate(() => {
      const [vx, vy, vw, vh] = window.pdp11Debug.app.geometry.viewBox;
      const b = document.querySelector('#art-host .bezel').getBBox();
      return { left: b.x - vx, top: b.y - vy, right: vx + vw - (b.x + b.width), bottom: vy + vh - (b.y + b.height) };
    });
    for (const [side, margin] of Object.entries(r)) assert.ok(margin > 0.4, `${side}: ${margin} mm`);
  });

  test('the highlight under the pointer can be seen, on every kind of paddle', async () => {
    await removePanel();
    for (const name of ['SR5', 'SR4', 'EXAM', 'LOAD_ADRS', 'LAMP_TEST']) {
      await page.mouse.move(2, 2);
      await sleep(160);
      const box = await page.locator(`.hit-paddle[data-name="${name}"]`).boundingBox();
      const clip = { x: box.x + 2, y: box.y + box.height * 0.42, width: box.width - 4, height: box.height * 0.56 };
      const a = await pixels(page, clip);
      await page.mouse.move(box.x + box.width / 2, box.y + box.height * 0.75);
      await sleep(160);
      const b = await pixels(page, clip);
      const change = a.mean.reduce((sum, v, k) => sum + Math.abs(v - b.mean[k]), 0);
      assert.ok(change > 25, `${name}: the colours change by ${change.toFixed(1)}`);
    }
    await page.mouse.move(2, 2);
  });

  test('branding off leaves the plain moulding: no plate, no lettering, no badge', async () => {
    await panelOfWidth(1340);
    const k = 1340 / 484.4;
    const clip = { x: Math.round(6.1 * k), y: Math.round(6.9 * k), width: Math.round(304 * k), height: Math.round(19.8 * k) };
    assert.ok((await pixels(page, clip)).darkest < 130, 'the plate and its lettering show');
    await page.evaluate(() => window.artPanel.setBranding(false));
    assert.ok((await pixels(page, clip)).darkest > 150, 'nothing is left of them');
    const shown = await page.evaluate(() => ['.plate', '.branding', '.logo']
      .map((c) => getComputedStyle(document.querySelector(`#art-host ${c}`)).display));
    assert.deepEqual(shown, ['none', 'none', 'none']);
  });

  test('the key is seen edge-on at POWER and from its flat side at OFF and LOCK', async () => {
    await panelOfWidth(1340);
    const seen = () => page.evaluate(() => ['.key-edge', '.key-keyoff', '.key-keylock']
      .filter((c) => getComputedStyle(document.querySelector(`#art-host ${c}`)).display !== 'none'));
    for (const [key, part] of [['power', '.key-edge'], ['off', '.key-keyoff'], ['lock', '.key-keylock']]) {
      await controls({ key });
      assert.deepEqual(await seen(), [part], key);
    }
  });

  test('a knob reflects the light by its position', async () => {
    await panelOfWidth(1340);
    const look = () => page.evaluate(() => {
      const op = (c) => +getComputedStyle(document.querySelector(`#art-host .knob[data-knob="addr"] ${c}`)).opacity;
      return { a: op('.edge-a'), b: op('.edge-b'), sheen: op('.knob-sheen') };
    });
    const rows = [];
    for (let pos = 0; pos < 8; pos++) {
      await controls({ addrKnob: pos });
      rows.push(await look());
    }
    // the bar's axis repeats after four positions, with the other long edge up
    assert.equal(rows[2].sheen, 1);
    assert.equal(rows[6].sheen, 1);
    assert.equal(rows[0].sheen, 0);
    assert.ok(rows[2].b > 0.5 && rows[2].a === 0, 'KERNEL D: the upper long edge');
    assert.ok(rows[6].a > 0.5 && rows[6].b === 0, 'SUPER I: the same edge from the other end');
  });

  test('the labels at every panel width: readable, apart, on their tiles', async () => {
    for (const width of [374, 562, 824, 1058, 1340, 1920]) {
      await panelOfWidth(width);
      const r = await page.evaluate((w) => {
        const geo = window.pdp11Debug.app.geometry;
        const px = w / geo.viewBox[2];
        const texts = [...document.querySelectorAll('#art-host .labels text')];
        const box = (t) => t.getBoundingClientRect();
        const scale = (t) => new DOMMatrix(getComputedStyle(t).transform).d;
        const find = (text, x) => texts.find((t) => t.textContent === text && (x === undefined || Math.abs(+t.getAttribute('x') - x) < 8));
        // lines on the same baseline must not touch
        const rows = new Map();
        for (const t of texts) {
          const y = t.getAttribute('y');
          rows.set(y, [...(rows.get(y) ?? []), t]);
        }
        const touching = [];
        for (const row of rows.values()) {
          row.sort((p, q) => box(p).left - box(q).left);
          for (let i = 1; i < row.length; i++) {
            if (box(row[i]).left < box(row[i - 1]).right) touching.push(`${row[i - 1].textContent}|${row[i].textContent}`);
          }
        }
        // the words on the control tiles stay on them
        const tiles = [...document.querySelectorAll('#art-host .tiles path')].map(box);
        const offTile = [];
        for (const t of texts) {
          if (+t.getAttribute('y') < 165 || +t.getAttribute('x') < 340) continue;
          const b = box(t);
          const cx = (b.left + b.right) / 2;
          const cy = (b.top + b.bottom) / 2;
          const tile = tiles.find((q) => cx > q.left && cx < q.right && cy > q.top && cy < q.bottom);
          if (!tile || b.left < tile.left - 0.5 || b.right > tile.right + 0.5) offTile.push(t.textContent);
        }
        // two-line labels: the distance of the baselines against the capital letters' height
        const lines = (upper, lower, x) => {
          const u = find(upper, x);
          const l = find(lower, x);
          const pitch = box(l).bottom - box(u).bottom;
          const cap = parseFloat(getComputedStyle(l).fontSize) * geo.labelFont.capHeight * scale(l) * px;
          return pitch / cap;
        };
        return {
          touching, offTile, par: lines('PAR', 'ERR', 168), adrs: lines('ADRS', 'ERR', 182), load: lines('LOAD', 'ADRS', 354),
          smallScale: scale(find('ENABLE')), bigScale: scale(find('ADDRESS')),
        };
      }, width);
      assert.deepEqual(r.touching, [], `${width} px: labels apart`);
      assert.deepEqual(r.offTile, [], `${width} px: labels on their tiles`);
      for (const k of ['par', 'adrs', 'load']) assert.ok(r[k] > 1.2, `${width} px: ${k} lines ${r[k].toFixed(2)} apart`);
      assert.ok(r.smallScale >= (width <= 640 ? 1.3 : width <= 1000 ? 1.2 : 1), `${width} px: small labels at ${r.smallScale}`);
      assert.equal(r.bigScale, 1, `${width} px: large labels keep their size`);
    }
    await removePanel();
  });

  test('forced colours keep the panel its physical colours', async () => {
    const ctx = await newContext({ viewport: { width: 1600, height: 900 }, forcedColors: 'active' });
    const forced = await openPage(srv, { context: ctx });
    try {
      await forced.waitForSelector('.panel-frame .lamp');
      const box = await forced.locator('.hit-paddle[data-name="SR5"]').boundingBox();
      const { mean: [r, , b] } = await pixels(forced, { x: box.x + 3, y: box.y + box.height * 0.42, width: box.width - 6, height: box.height * 0.2 });
      assert.ok(r - b > 40, `the paddle stays rose (${r.toFixed(0)}, ${b.toFixed(0)})`);
    } finally {
      await ctx.close();
    }
  });
});
