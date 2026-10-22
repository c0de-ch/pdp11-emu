// Layout at many window sizes: readable console rows, panel and console
// inside the window, a view in another window, phones, and a ready banner
// that keeps clear of the console text (docs/UI.md §1-2).
import { after, before, describe, test } from 'node:test';
import assert from 'node:assert/strict';
import { closeBrowser, mock, newContext, openPage, skip, waitFor } from './helpers.mjs';

/** The main view's geometry: page scroll, frames, console row height, the panel's hit areas. */
const measure = (page) => page.evaluate(() => {
  const box = (sel) => {
    const e = document.querySelector(sel);
    return e && e.getClientRects().length ? e.getBoundingClientRect().toJSON() : null;
  };
  const screen = box('.console-screen .xterm-screen');
  const hits = [...document.querySelectorAll('.panel-frame .hit')].filter((e) => e.getClientRects().length).map((e) => {
    const b = e.getBoundingClientRect();
    const at = document.elementFromPoint(b.left + b.width / 2, b.top + b.height / 2);
    return {
      name: e.dataset.name ?? e.dataset.knob ?? 'key',
      inside: b.left >= 0 && b.top >= 0 && b.right <= innerWidth && b.bottom <= innerHeight,
      reachable: at === e,
    };
  });
  return {
    iw: innerWidth, ih: innerHeight, sw: document.documentElement.scrollWidth, sh: document.documentElement.scrollHeight,
    layout: window.pdp11Debug.layout.layout, panel: box('.panel-frame'), console: box('.console-frame'),
    away: box('.away:not([hidden])'), rowH: screen ? screen.height / 24 : null, hits,
  };
});

/** Measures once the layout pass and xterm.js's re-render after a resize are done: two measurements a frame apart agree. */
async function settled(page) {
  let prev = '';
  for (let i = 0; i < 50; i++) {
    await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
    const m = await measure(page);
    const sig = JSON.stringify([m.layout, m.panel, m.console, m.rowH, m.away]);
    if (sig === prev) return m;
    prev = sig;
  }
  throw new Error('the layout does not settle');
}

const inside = (b, m) => !!b && b.left >= 0 && b.top >= 0 && b.right <= m.iw + 0.5 && b.bottom <= m.ih + 0.5;
const overlap = (a, b) => !!a && !!b && a.left < b.right && b.left < a.right && a.top < b.bottom && b.top < a.bottom;

describe('layout', { skip }, () => {
  let srv;
  before(async () => {
    srv = await mock();
  });
  after(async () => {
    await srv?.close();
    await closeBrowser();
  });

  test('700-1100 x 550-800 px windows: console rows of at least 13 px (12 when the page scrolls), everything inside unless it scrolls', async () => {
    const page = await openPage(srv, { width: 1100, height: 800 });
    try {
      const bad = [];
      for (let w = 700; w <= 1100; w += 50) {
        for (let h = 550; h <= 800; h += 50) {
          await page.setViewportSize({ width: w, height: h });
          const m = await settled(page);
          const why = [];
          if (!(m.rowH >= (m.layout === 'mobile' ? 12 : 13))) why.push(`rows ${m.rowH?.toFixed(1)} px`);
          if (m.layout !== 'mobile') {
            if (m.sw > m.iw || m.sh > m.ih) why.push('page scrolls');
            if (!inside(m.panel, m) || !inside(m.console, m)) why.push('outside the window');
            if (overlap(m.panel, m.console)) why.push('panel and console overlap');
          }
          if (why.length) bad.push(`${w}x${h} ${m.layout}: ${why.join(', ')}`);
        }
      }
      assert.deepEqual(bad, []);
      // the narrow windows of the review, each loaded fresh
      for (const [w, h] of [[800, 600], [900, 768], [1000, 700]]) {
        const p = await openPage(srv, { width: w, height: h });
        const m = await settled(p);
        await p.ctx.close();
        assert.ok(m.rowH >= (m.layout === 'mobile' ? 12 : 13), `${w}x${h}: rows ${m.rowH} px (${m.layout})`);
      }
    } finally {
      await page.ctx.close();
    }
  });

  test('132 columns at 1366x768: rows of at least 13 px without page scrolling, also at device pixel ratios 1.25 and 1.5', async () => {
    for (const dpr of [1, 1.25, 1.5]) {
      const ctx = await newContext({ viewport: { width: 1366, height: 768 }, deviceScaleFactor: dpr });
      const page = await openPage(srv, { context: ctx });
      try {
        await page.evaluate(() => window.pdp11Debug.app.prefs.set('cols', 132));
        const m = await settled(page);
        assert.ok(m.sw <= m.iw && m.sh <= m.ih, `DPR ${dpr}: no page scrolling (${m.sw}x${m.sh})`);
        assert.ok(m.rowH >= 13, `DPR ${dpr}: rows ${m.rowH} px`);
        assert.ok(inside(m.console, m) && inside(m.panel, m) && !overlap(m.panel, m.console), `DPR ${dpr}: ${m.layout}`);
      } finally {
        await page.evaluate(() => window.pdp11Debug.app.prefs.set('cols', 80));
        await ctx.close();
      }
    }
  });

  for (const [w, h] of [[1366, 768], [1920, 1080], [2560, 1440]]) {
    for (const view of ['console', 'panel']) {
      test(`the ${view} in its own window at ${w}x${h}: both windows keep their frames and hit areas inside`, async () => {
        const page = await openPage(srv, { width: w, height: h });
        try {
          const [pop] = await Promise.all([page.ctx.waitForEvent('page'), page.click(`.pop-${view}`)]);
          await pop.waitForFunction(() => window.pdp11Debug?.connected);
          await page.waitForSelector(`#${view}-away:not([hidden])`);
          const m = await settled(page);
          const shown = view === 'console' ? m.panel : m.console;
          assert.ok(m.sw <= m.iw && m.sh <= m.ih, 'main window: no page scrolling');
          assert.ok(inside(shown, m), `main window: the remaining view is inside (${JSON.stringify(shown)})`);
          assert.ok(inside(m.away, m) && !overlap(m.away, shown), 'main window: the notice is inside and beside it');
          for (const hit of m.hits) assert.ok(hit.inside && hit.reachable, `main window: ${JSON.stringify(hit)}`);
          // the pop-out itself at the same size
          await pop.setViewportSize({ width: w, height: h });
          const p = await settled(pop);
          assert.ok(p.sw <= p.iw && p.sh <= p.ih, 'pop-out: no page scrolling');
          assert.ok(inside(view === 'console' ? p.console : p.panel, p), 'pop-out: its view is inside');
          for (const hit of p.hits) assert.ok(hit.inside && hit.reachable, `pop-out: ${JSON.stringify(hit)}`);
          if (view === 'panel') assert.equal(p.hits.length, 33);
          assert.deepEqual(page.errors, []);
        } finally {
          await page.ctx.close();
        }
      });
    }
  }

  test('phones: landscape fits the whole panel in the viewport height; help tabs stay inside the dialog', async () => {
    const page = await openPage(srv, { width: 844, height: 390 });
    try {
      let m = await settled(page);
      assert.equal(m.layout, 'mobile');
      assert.ok(m.panel.height <= m.ih, `panel ${m.panel.height} px high in ${m.ih}`);
      await page.setViewportSize({ width: 390, height: 844 });
      m = await settled(page);
      assert.ok(m.panel.width >= 370, `portrait: the full width (${m.panel.width})`);
      assert.equal(await page.isVisible('#landscape-hint'), true);
      await page.click('button[aria-label="Help"]');
      const r = await page.evaluate(() => {
        const d = document.querySelector('dialog.dialog[open]').getBoundingClientRect();
        return [...document.querySelectorAll('.tabs button')].map((b) => {
          const t = b.getBoundingClientRect();
          return t.left >= d.left && t.right <= d.right;
        });
      });
      assert.deepEqual(r, [true, true, true, true, true]);
      await page.click('.tabs button[data-tab="log"]');
      await page.waitForSelector('.help-body[data-tab="log"]');
    } finally {
      await page.ctx.close();
    }
  });

  test('the ready banner keeps clear of the console text: below the console when there is room, else away from the cursor until a key is typed', async () => {
    const page = await openPage(srv, { width: 1920, height: 1080 });
    const geometry = () => page.evaluate(() => {
      const s = document.querySelector('.console-screen .xterm-screen').getBoundingClientRect();
      const b = document.querySelector('.ready-banner')?.getBoundingClientRect();
      const c = window.pdp11Debug.app.console.cursorRow();
      const rowH = s.height / c.rows;
      return {
        screen: s.toJSON(), banner: b?.toJSON() ?? null,
        cursor: { left: s.left, right: s.right, top: s.top + c.row * rowH, bottom: s.top + (c.row + 1) * rowH },
      };
    });
    try {
      await waitFor(() => page.evaluate(() => /login: $/m.test(window.pdp11Debug.consoleText())), { timeout: 10000, message: 'login prompt' });
      await page.evaluate(() => window.pdp11Debug.app.showReadyBanner(false));
      await settled(page);
      let g = await geometry();
      assert.ok(g.banner && !overlap(g.banner, g.screen), `1920x1080: clear of the terminal (${JSON.stringify(g)})`);
      // no room below the console: over the terminal, but never over the cursor row
      for (const [w, h] of [[900, 768], [1024, 600]]) {
        await page.setViewportSize({ width: w, height: h });
        await settled(page);
        g = await geometry();
        assert.ok(g.banner && !overlap(g.banner, g.cursor), `${w}x${h}: clear of the cursor row (${JSON.stringify(g)})`);
      }
      // the cursor moves into its half: it moves to the other one
      await fetch(`${srv.url}mock/console`, { method: 'POST', body: JSON.stringify({ text: '\x1b[H' }) });
      await waitFor(() => geometry().then((x) => !overlap(x.banner, x.cursor) && x.cursor.top - x.screen.top < 1), { message: 'banner left the cursor' });
      await fetch(`${srv.url}mock/console`, { method: 'POST', body: JSON.stringify({ text: '\x1b[24;1H' }) });
      await waitFor(() => geometry().then((x) => !overlap(x.banner, x.cursor)), { message: 'banner left the cursor again' });
      // the first key typed into the console
      await page.click('.console-screen');
      await page.keyboard.press('Control+u');
      await page.waitForSelector('.ready-banner', { state: 'detached' });
      assert.deepEqual(page.errors, []);
    } finally {
      await page.ctx.close();
    }
  });
});
