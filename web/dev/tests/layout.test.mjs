// Layout at many window sizes: readable console rows, panel and console
// inside the window with their edges in line, a view in another window, phones,
// and a ready banner and toasts that keep clear of the console text and of each
// other (docs/UI.md §1-2).
import { after, before, describe, test } from 'node:test';
import assert from 'node:assert/strict';
import { closeBrowser, mock, newContext, openPage, skip, sleep, waitFor } from './helpers.mjs';

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
      await page.waitForSelector('.ready-banner', { state: 'detached', timeout: 2000 }); // not the banner's own 15 s timeout
      assert.deepEqual(page.errors, []);
    } finally {
      await page.ctx.close();
    }
  });
  test('menus stay inside small windows: every item can be reached', async () => {
    for (const [w, h] of [[390, 844], [844, 390], [640, 360]]) {
      const page = await openPage(srv, { width: w, height: h });
      try {
        const inside = () => page.evaluate(() => {
          const m = document.querySelector('.menu, .context-menu');
          const b = m.getBoundingClientRect();
          const ok = b.left >= 0 && b.top >= 0 && b.right <= innerWidth && b.bottom <= innerHeight;
          const scrolls = m.scrollHeight <= m.clientHeight + 1 || ['auto', 'scroll'].includes(getComputedStyle(m).overflowY);
          const clipped = m.scrollWidth > m.clientWidth + 1;
          return { ok, scrolls, clipped, box: [b.left, b.top, b.right, b.bottom], win: [innerWidth, innerHeight] };
        });
        await page.click('button[aria-label="View settings"]');
        await page.waitForSelector('.menu');
        let r = await inside();
        assert.ok(r.ok && r.scrolls && !r.clipped, `settings menu at ${w}x${h}: ${JSON.stringify(r)}`);
        await page.keyboard.press('Escape');
        await page.evaluate(() => document.querySelector('.console-frame').scrollIntoView({ block: 'center' }));
        const b = await page.locator('.console-screen').boundingBox();
        await page.mouse.click(b.x + 30, Math.max(5, Math.min(b.y + 30, h - 5)), { button: 'right' });
        await page.waitForSelector('.context-menu');
        r = await inside();
        assert.ok(r.ok && r.scrolls, `console menu at ${w}x${h}: ${JSON.stringify(r)}`);
      } finally {
        await page.ctx.close();
      }
    }
  });
});

/** The geometry of what the composition promises, on top of measure(). */
const pageBoxes = (page) => page.evaluate(() => {
  const box = (sel) => {
    const e = document.querySelector(sel);
    return e && e.getClientRects().length ? e.getBoundingClientRect().toJSON() : null;
  };
  const con = window.pdp11Debug.app.console;
  const f = window.pdp11Debug.consoleFont();
  return {
    toolbar: box('#toolbar'), bar: box('.head-bar'), dockBar: box('#panel-bar'), head: box('#console-head'), info: box('.head-info'), keysBtn: box('.keys-btn'),
    dock: ['.head-info', '.try-notes', '#panel-notes', '#panel-keys', '#panel-about'].map(box).filter(Boolean),
    screen: box('.console-screen .xterm-screen'), banner: box('.ready-banner'), tutorial: box('.tutorial'), hint: box('.console-hint'),
    toasts: [...document.querySelectorAll('#toasts .toast:not(.leaving)')].map((t) => t.getBoundingClientRect().toJSON()),
    family: f?.family ?? '', size: f?.size ?? 0, predicted: f ? con.sizeFor(f) : null,
    keys: [...document.querySelectorAll('.keyhelp li')].map((e) => {
      const b = e.getBoundingClientRect();
      return { text: e.textContent, inside: !!e.getClientRects().length && b.top >= 0 && b.bottom <= innerHeight && e.scrollWidth <= e.clientWidth };
    }),
  };
});

const DESKTOP = [[1280, 720], [1366, 768], [1440, 900], [1600, 900], [1920, 1080], [2560, 1440]];
const DPRS = [1, 1.25, 1.5, 2];
const hit = (a, b, slack = 0) => !!a && !!b && a.left < b.right - slack && b.left < a.right - slack && a.top < b.bottom - slack && b.top < a.bottom - slack;

describe('composition', { skip }, () => {
  let srv;
  before(async () => {
    srv = await mock();
  });
  after(async () => {
    await srv?.close();
    await closeBrowser();
  });

  for (const dpr of DPRS) {
    test(`desktop sizes at device pixel ratio ${dpr}: panel and console share their edges under the toolbar, in Glass TTY, without page scrolling`, async () => {
      const ctx = await newContext({ viewport: { width: 1280, height: 720 }, deviceScaleFactor: dpr });
      const page = await openPage(srv, { context: ctx });
      try {
        for (const [w, h] of DESKTOP) {
          await page.setViewportSize({ width: w, height: h });
          const m = await settled(page);
          const b = await pageBoxes(page);
          const at = `${w}x${h}`;
          assert.equal(m.layout, 'side', at);
          assert.ok(m.sw <= m.iw && m.sh <= m.ih, `${at}: no page scrolling (${m.sw}x${m.sh})`);
          assert.ok(inside(m.panel, m) && inside(m.console, m), `${at}: inside the window`);
          assert.ok(Math.abs(m.panel.top - m.console.top) <= 1, `${at}: tops ${m.panel.top} and ${m.console.top}`);
          assert.ok(Math.abs(m.panel.bottom - m.console.bottom) <= 1, `${at}: bottoms ${m.panel.bottom} and ${m.console.bottom}`);
          assert.ok(Math.abs(m.console.left - m.panel.right - 10) <= 1, `${at}: the gap between the columns`);
          assert.ok(Math.abs(m.panel.left - (m.iw - m.console.right)) <= 1, `${at}: the group is centred`);
          assert.ok(m.panel.top - b.toolbar.bottom <= 32, `${at}: right under the toolbar (${m.panel.top - b.toolbar.bottom} px)`);
          assert.ok(m.panel.width >= 0.5 * w, `${at}: panel at a useful size (${m.panel.width} px)`);
          assert.match(b.family, /Glass TTY/, `${at}: one font at every size`);
          assert.ok(m.rowH >= 13, `${at}: rows of ${m.rowH} px`);
          // what the layout planned with is what xterm.js draws
          assert.ok(Math.abs(b.screen.height - b.predicted.h) <= 1, `${at}: terminal ${b.screen.height} px high, predicted ${b.predicted.h}`);
          assert.ok(Math.abs(b.screen.width + 10 - b.predicted.w) <= 1, `${at}: terminal ${b.screen.width} px wide and its 10 px gutter, predicted ${b.predicted.w}`);
          assert.ok(b.screen.left >= m.console.left && b.screen.right <= m.console.right && b.screen.top >= m.console.top && b.screen.bottom <= m.console.bottom, `${at}: the terminal stays in its frame`);
          for (const edge of [b.screen.left, b.screen.top]) assert.ok(Math.abs(edge * dpr - Math.round(edge * dpr)) < 0.05, `${at}: the terminal on whole device pixels (${edge})`);
          assert.ok(Math.abs(b.screen.top - m.console.top - (m.console.bottom - b.screen.bottom)) <= 2 / dpr + 0.01, `${at}: the terminal in the middle of its frame`);
          assert.ok(b.bar.top >= m.console.bottom && inside(b.bar, m), `${at}: the header bar under the frame`);
          assert.ok(Math.abs(b.bar.top - b.dockBar.top) <= 1, `${at}: the bars of the two columns on one line (${b.bar.top}, ${b.dockBar.top})`);
          for (const d of b.dock) assert.ok(inside(d, m), `${at}: dock content inside the window (${JSON.stringify(d)})`);
          for (const hitbox of m.hits) assert.ok(hitbox.inside && hitbox.reachable, `${at}: ${JSON.stringify(hitbox)}`);
        }
        assert.deepEqual(page.errors, []);
      } finally {
        await ctx.close();
      }
    });
  }

  test('the panel keeps its size at common windows: the free height goes to the dock, not to a larger console', async () => {
    for (const [dpr, sizes] of [
      [1, [[1280, 720, 660], [1366, 768, 745], [1440, 900, 820], [1600, 900, 898], [1920, 1080, 1058], [2560, 1440, 1380]]],
      [2, [[1440, 900, 820], [1920, 1080, 1058]]],
    ]) {
      const ctx = await newContext({ viewport: { width: 1280, height: 720 }, deviceScaleFactor: dpr });
      const page = await openPage(srv, { context: ctx });
      try {
        for (const [w, h, min] of sizes) {
          await page.setViewportSize({ width: w, height: h });
          const m = await settled(page);
          assert.ok(m.panel.width >= min, `${w}x${h} at ${dpr}: panel ${m.panel.width} px, at least ${min}`);
        }
      } finally {
        await ctx.close();
      }
    }
  });

  test('from the console Shift+Tab goes through its header to the panel; Tab from the toolbar reaches the panel before the console', async () => {
    const page = await openPage(srv, { width: 1920, height: 1080 });
    const focused = () => page.evaluate(() => {
      const a = document.activeElement;
      return a.classList.contains('xterm-helper-textarea') ? 'terminal' : a.classList.contains('hit-key') ? 'key' : a.textContent.trim() || a.type || a.tagName;
    });
    try {
      await page.click('.console-screen');
      assert.equal(await focused(), 'terminal');
      const seen = [];
      for (let i = 0; i < 3; i++) {
        await page.keyboard.press('Shift+Tab');
        seen.push(await focused());
      }
      assert.deepEqual(seen, ['Send key ▾', 'checkbox', 'key']);
      await page.focus('button[aria-label="Help"]');
      await page.keyboard.press('Tab');
      assert.equal(await focused(), 'checkbox', 'the dock bar first: the octal readouts');
      await page.keyboard.press('Tab');
      assert.equal(await focused(), 'Panel guide');
      await page.keyboard.press('Tab');
      assert.equal(await focused(), 'Toggle-in tutorial');
      await page.keyboard.press('Tab');
      assert.ok(await page.evaluate(() => !!document.activeElement.closest('.hit-layer')), 'then the panel');
    } finally {
      await page.ctx.close();
    }
  });

  test('a mouse click on a switch of the header or the dock leaves the keyboard in the console; Space keeps it on the switch', async () => {
    const page = await openPage(srv, { width: 1920, height: 1080 });
    const inTerminal = () => page.evaluate(() => document.activeElement?.classList.contains('xterm-helper-textarea'));
    try {
      for (const label of ['#panel-bar label', '.head-bar label']) {
        await page.click('.console-screen');
        await page.click(label);
        await waitFor(inTerminal, { message: `${label}: back in the terminal` });
        await page.click(label);
        await waitFor(inTerminal, { message: `${label}: back in the terminal again` });
      }
      await page.focus('#panel-bar input');
      await page.keyboard.press('Space');
      await sleep(50);
      assert.equal(await inTerminal(), false);
      assert.equal(await page.isChecked('#panel-bar input'), true);
      await page.keyboard.press('Space');
    } finally {
      await page.ctx.close();
    }
  });

  test('new data from the server leaves the notes in the dock where they were scrolled', async () => {
    const page = await openPage(srv, { width: 1366, height: 700 });
    try {
      await page.evaluate(() => {
        const a = window.pdp11Debug.app;
        const sys = a.selectedSystem();
        sys.notes = `## Things to try\n\n${'- a line of notes\n'.repeat(40)}\n## Shut down\n\nPower off.`;
        a.onSystems(a.systems);
      });
      await settled(page);
      const notes = page.locator('.try-notes');
      await notes.evaluate((e) => { e.scrollTop = 40; });
      await page.evaluate(() => { const a = window.pdp11Debug.app; a.onSystems({ ...a.systems }); });
      assert.equal(await notes.evaluate((e) => e.scrollTop), 40);
    } finally {
      await page.ctx.close();
    }
  });

  test('on a large screen the console is larger: more than 20 px rows at 2560x1440, 20 px at 1920x1080', async () => {
    const page = await openPage(srv, { width: 1920, height: 1080 });
    try {
      assert.equal((await settled(page)).rowH, 20);
      await page.setViewportSize({ width: 2560, height: 1440 });
      const m = await settled(page);
      assert.ok(m.rowH > 24, `rows of ${m.rowH} px`);
    } finally {
      await page.ctx.close();
    }
  });

  test('the key help can be read without a tooltip; messages do not move the frame or wrap the header bar', async () => {
    const page = await openPage(srv, { width: 1366, height: 768 });
    try {
      for (const [w, h] of [...DESKTOP, [1024, 768], [390, 844]]) {
        await page.setViewportSize({ width: w, height: h });
        await settled(page);
        const before = await pageBoxes(page);
        const m = await measure(page);
        if (before.keysBtn) {
          // no room in this window: its button opens all of it
          await page.click('.keys-btn');
          assert.deepEqual((await page.locator('.menu .menu-item').allTextContents()).length, before.keys.length, `${w}x${h}: all of it in the menu`);
          await page.keyboard.press('Escape');
        } else {
          assert.ok(before.keys.length >= 3 && before.keys.every((k) => k.inside), `${w}x${h}: ${JSON.stringify(before.keys)}`);
        }
        await page.evaluate(() => {
          const a = window.pdp11Debug.app;
          a.pasteEl.hidden = false;
          a.pasteEl.querySelector('.paste-text').textContent = '9 characters waiting until the CPU runs';
          a.pasteEl.querySelector('button').hidden = true;
        });
        const during = await settled(page);
        const after = await pageBoxes(page);
        const clipped = await page.evaluate(() => {
          const e = document.querySelector('.paste-text');
          return e.scrollWidth > e.clientWidth;
        });
        await page.evaluate(() => { window.pdp11Debug.app.pasteEl.hidden = true; });
        assert.deepEqual(during.console, m.console, `${w}x${h}: the frame stays`);
        assert.deepEqual(after.bar, before.bar, `${w}x${h}: the bar stays`);
        assert.equal(clipped, false, `${w}x${h}: the message fits`);
      }
    } finally {
      await page.ctx.close();
    }
  });

  for (const dpr of DPRS) test(`ready banner and toasts at device pixel ratio ${dpr} cover neither each other nor the header bar, and leave the terminal alone where the page has room`, async () => {
    const ctx = await newContext({ viewport: { width: 1366, height: 768 }, deviceScaleFactor: dpr });
    const page = await openPage(srv, { context: ctx });
    try {
      await waitFor(() => page.evaluate(() => /login: $/m.test(window.pdp11Debug.consoleText())), { timeout: 10000, message: 'login prompt' });
      await page.evaluate(() => {
        const a = window.pdp11Debug.app;
        a.showReadyBanner(true);
        a.toasts.show('The OS has shut down; power is off.', 'info', { sticky: true });
        a.toasts.show('RT-11 installed', 'info', { sticky: true });
      });
      for (const [w, h] of [...DESKTOP, [1366, 620], [1024, 768], [768, 1024]]) {
        await page.setViewportSize({ width: w, height: h });
        await settled(page);
        await sleep(250); // toast entrance
        const at = `${w}x${h}`;
        const b = await pageBoxes(page);
        const m = await measure(page);
        assert.equal(b.toasts.length, 2, at);
        assert.ok(b.banner, at);
        for (const t of b.toasts) {
          assert.ok(!hit(t, b.banner), `${at}: toast over the banner ${JSON.stringify([t, b.banner])}`);
          assert.ok(!hit(t, b.bar), `${at}: toast over the header bar`);
          assert.ok(inside(t, m), `${at}: toast inside the window`);
          if (m.layout === 'side') assert.ok(!hit(t, b.screen), `${at}: toast over the terminal`);
        }
        assert.ok(!hit(b.toasts[0], b.toasts[1]), `${at}: toasts over each other`);
        assert.ok(!hit(b.banner, b.bar), `${at}: banner over the header bar`);
        if (m.layout === 'side') assert.ok(!hit(b.banner, b.screen), `${at}: banner over the terminal`);
      }
      assert.deepEqual(page.errors, []);
    } finally {
      await ctx.close();
    }
  });

  test('a terminal wider than the layout planned (another browser\'s font metrics) widens its frame and is not cut', async () => {
    const page = await openPage(srv, { width: 1920, height: 1080 });
    try {
      await page.evaluate(() => {
        const a = window.pdp11Debug.app;
        const c = a.console;
        const real = c.sizeFor.bind(c);
        c.sizeFor = (mode) => { const s = real(mode); return { ...s, w: s.w - 16 }; };
        c.choices = null;
        a.layout();
      });
      const m = await settled(page);
      const b = await pageBoxes(page);
      assert.ok(b.screen.left >= m.console.left && b.screen.right <= m.console.right, `the terminal ${b.screen.left}-${b.screen.right} in its frame ${m.console.left}-${m.console.right}`);
    } finally {
      await page.ctx.close();
    }
  });

  test('a kiosk console: toasts stay clear of its "Click to type" pill', async () => {
    const page = await openPage(srv, { path: '?view=console&chrome=0', width: 1280, height: 800 });
    try {
      await page.evaluate(() => {
        document.activeElement?.blur();
        window.pdp11Debug.app.toast('a toast');
      });
      await sleep(250);
      const b = await pageBoxes(page);
      assert.ok(b.hint && b.toasts.length === 1 && !hit(b.toasts[0], b.hint), JSON.stringify([b.hint, b.toasts]));
    } finally {
      await page.ctx.close();
    }
  });

  test('narrow windows at every device pixel ratio: one axis for panel and console, nothing outside the width; phones hold the console whole', async () => {
    for (const [w, h, ratios, mobile] of [[1024, 768, DPRS, false], [768, 1024, DPRS, false], [390, 844, [...DPRS, 3], true], [844, 390, [...DPRS, 3], true]]) {
      for (const dpr of ratios) {
        const ctx = await newContext({ viewport: { width: w, height: h }, deviceScaleFactor: dpr, isMobile: mobile, hasTouch: mobile });
        const page = await openPage(srv, { context: ctx });
        try {
          const m = await settled(page);
          const b = await pageBoxes(page);
          const at = `${w}x${h} at ${dpr}`;
          assert.ok(m.sw <= m.iw, `${at}: no sideways page scrolling (${m.sw})`);
          const axis = (r) => (r.left + r.right) / 2;
          assert.ok(Math.abs(axis(m.panel) - axis(m.console)) <= 1, `${at}: panel ${axis(m.panel)}, console ${axis(m.console)}`);
          assert.ok(m.console.left >= 0 && m.console.right <= m.iw, `${at}: the frame fits the width (${JSON.stringify(m.console)})`);
          assert.ok(b.screen.right <= m.console.right && b.screen.left >= m.console.left, `${at}: all columns in the frame`);
          assert.ok(b.bar.left >= 0 && b.bar.right <= m.iw, `${at}: the header bar fits the width`);
          if (mobile) assert.ok(b.bar.height >= 40 && (await page.locator('.head-bar button:has-text("Send key")').boundingBox()).height <= b.bar.height, `${at}: the button in the bar`);
          for (const sel of ['.head-bar button:has-text("Send key")', '.head-bar .toggle']) {
            const r = await page.locator(sel).boundingBox();
            assert.ok(r && r.width > 0 && r.x >= 0 && r.x + r.width <= m.iw, `${at}: ${sel} in the bar (${JSON.stringify(r)})`);
          }
          if (w === 768) assert.ok(Math.abs(m.panel.width - m.console.width) <= 1, `${at}: the same width (${m.panel.width}, ${m.console.width})`);
          if (w === 844) assert.ok(m.console.height <= h, `${at}: 24 rows fit the window (${m.console.height} px)`);
          if (!mobile) assert.ok(m.sh <= m.ih, `${at}: no page scrolling (${m.sh})`);
          assert.ok(m.rowH >= (mobile ? 12 : 13), `${at}: rows of ${m.rowH} px`);
          assert.deepEqual(page.errors, []);
        } finally {
          await ctx.close();
        }
      }
    }
  });

  test('toolbar: one row at 1024 px, no orphan; two rows on a phone with Power labelled and different pop-out icons', async () => {
    const rows = (page) => page.evaluate(() => {
      const tops = [...document.querySelectorAll('#toolbar > :not(.sr-only, .row-break, .spacer)')].filter((e) => e.getClientRects().length).map((e) => {
        const b = e.getBoundingClientRect();
        return Math.round(b.top + b.height / 2);
      });
      const sorted = [...new Set(tops)].sort((a, b) => a - b);
      const groups = sorted.filter((t, i) => i === 0 || t - sorted[i - 1] > 10);
      return {
        rows: groups.length,
        power: document.querySelector('.power-btn .label').getBoundingClientRect().width,
        icons: [document.querySelector('.pop-panel svg').innerHTML, document.querySelector('.pop-console svg').innerHTML],
        inside: [...document.querySelectorAll('#toolbar button')].every((e) => e.getBoundingClientRect().right <= innerWidth),
      };
    });
    const page = await openPage(srv, { width: 1024, height: 768 });
    try {
      for (const w of [2560, 1366, 1280, 1101, 1100, 1024, 900, 800, 768, 760]) {
        await page.setViewportSize({ width: w, height: 768 });
        await settled(page);
        const r = await rows(page);
        assert.equal(r.rows, 1, `${w} px: ${r.rows} rows`);
        assert.ok(r.inside, `${w} px: every button inside`);
      }
      for (const w of [430, 390, 375, 360]) {
        await page.setViewportSize({ width: w, height: 844 });
        await settled(page);
        const r = await rows(page);
        assert.equal(r.rows, 2, `${w} px: ${r.rows} rows`);
        assert.ok(r.power > 20, `${w} px: Power keeps its label`);
        assert.notEqual(r.icons[0], r.icons[1]);
        assert.ok(r.inside, `${w} px: every button inside`);
      }
    } finally {
      await page.ctx.close();
    }
  });

  test('a phone: knobs and key take targets of about 44 px; paddles stay 44 px high', async () => {
    const ctx = await newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 3, isMobile: true, hasTouch: true });
    const page = await openPage(srv, { context: ctx });
    try {
      await settled(page);
      const r = await page.evaluate(() => {
        const reach = (el, dx, dy) => {
          const b = el.getBoundingClientRect();
          return document.elementFromPoint(b.left + b.width / 2 + dx, b.top + b.height / 2 + dy) === el;
        };
        const paddles = [...document.querySelectorAll('.hit-paddle')].map((e) => e.getBoundingClientRect().height);
        const knobs = [...document.querySelectorAll('.hit-knob')];
        const key = document.querySelector('.hit-key');
        return {
          coarse: matchMedia('(pointer: coarse)').matches,
          paddle: Math.min(...paddles),
          knobs: knobs.map((k) => [reach(k, -20, 0), reach(k, 20, 0)]),
          knobsApart: reach(knobs[0], 0, 18) && reach(knobs[1], 0, -18),
          key: [reach(key, -20, 0), reach(key, 20, 0), reach(key, 0, -20)],
        };
      });
      assert.ok(r.coarse, 'a touch screen');
      assert.ok(r.paddle >= 44, `paddles ${r.paddle} px high`);
      assert.deepEqual(r.knobs, [[true, true], [true, true]]);
      assert.ok(r.knobsApart, 'each knob keeps its half of the way to the other');
      assert.deepEqual(r.key, [true, true, true]);
      assert.deepEqual(page.errors, []);
    } finally {
      await ctx.close();
    }
  });

  test('tutorial card: in the free space under the panel where there is room, always inside the window', async () => {
    for (const [w, h, docked] of [[1920, 1080, true], [1366, 768, true], [1280, 720, true], [1024, 768, false], [1366, 600, false], [390, 844, false]]) {
      const page = await openPage(srv, { width: w, height: h });
      try {
        await page.evaluate(() => window.pdp11Debug.app.openTutorial());
        await page.waitForSelector('.tutorial');
        await settled(page);
        const m = await measure(page);
        const b = await pageBoxes(page);
        const at = `${w}x${h}`;
        assert.ok(inside(b.tutorial, m), `${at}: inside the window (${JSON.stringify(b.tutorial)})`);
        assert.ok(!hit(b.tutorial, m.panel, 1), `${at}: the panel stays in view`);
        assert.equal(await page.evaluate(() => document.querySelector('.tutorial').classList.contains('docked')), docked, at);
        if (docked) {
          assert.ok(!hit(b.tutorial, m.console, 1) && !hit(b.tutorial, b.bar, 1), `${at}: clear of the console and its header`);
        } else if (m.layout !== 'mobile') {
          assert.ok(!hit(b.tutorial, b.bar, 1), `${at}: clear of the console's header`);
        }
      } finally {
        await page.ctx.close();
      }
    }
  });

  test('the console frame shows clearly where the keyboard is', async () => {
    const page = await openPage(srv, { width: 1366, height: 768 });
    try {
      const look = () => page.evaluate(() => {
        const cs = getComputedStyle(document.querySelector('.console-frame'));
        return { border: cs.borderTopColor, shadow: cs.boxShadow, text: document.querySelector('.focus-chip').textContent };
      });
      await page.click('.console-screen');
      await sleep(200);
      const on = await look();
      await page.focus('button[aria-label="Help"]');
      await sleep(200);
      const off = await look();
      assert.notEqual(on.border, off.border);
      assert.notEqual(on.shadow, off.shadow);
      assert.match(on.text, /Typing/);
      assert.match(off.text, /Click to type/);
    } finally {
      await page.ctx.close();
    }
  });

  test('with the machine off a focused console says so, not that typing reaches the PDP-11', async () => {
    const page = await openPage(srv, { width: 1366, height: 768 });
    try {
      const chip = () => page.evaluate(() => {
        const el = document.querySelector('.focus-chip');
        return { text: el.textContent, on: el.classList.contains('on') };
      });
      await page.click('.console-screen');
      await sleep(200);
      assert.deepEqual(await chip(), { text: 'Typing to the PDP-11', on: true });
      await fetch(`${srv.url}mock/state`, { method: 'POST', body: JSON.stringify({ run: 'off' }) });
      await page.waitForFunction(() => document.querySelector('.focus-chip').textContent === 'Machine off', { timeout: 5000 });
      assert.equal((await chip()).on, false);
    } finally {
      await fetch(`${srv.url}mock/state`, { method: 'POST', body: JSON.stringify({ run: 'running' }) });
      await page.ctx.close();
    }
  });

  test('forced colours: VT323 at a larger size, a visible steady cursor, and the layout follows the change', async () => {
    const page = await openPage(srv, { width: 1366, height: 768 });
    try {
      const font = () => page.evaluate(() => document.querySelector('.console-frame').dataset.font);
      assert.match(await font(), /^glass/);
      await page.emulateMedia({ forcedColors: 'active' });
      await page.waitForFunction(() => document.querySelector('.console-frame').dataset.font.startsWith('smooth'), { timeout: 5000 });
      await page.click('.console-screen');
      await sleep(200);
      const cursor = await page.evaluate(() => {
        const el = document.querySelector('.xterm-rows .xterm-cursor');
        const cs = el && getComputedStyle(el);
        const screen = getComputedStyle(document.querySelector('.xterm-rows'));
        return cs && { bg: cs.backgroundColor, canvasText: screen.color };
      });
      assert.ok(cursor, 'a cursor element');
      assert.equal(cursor.bg, cursor.canvasText, 'the block cursor is in the text colour');
      await page.emulateMedia({ forcedColors: 'none' });
      await page.waitForFunction(() => document.querySelector('.console-frame').dataset.font.startsWith('glass'), { timeout: 5000 });
    } finally {
      await page.ctx.close();
    }
  });

  test('an action toast in a narrow margin keeps its text readable, its button under the text', async () => {
    const page = await openPage(srv, { width: 1024, height: 768 });
    try {
      await page.evaluate(() => window.pdp11Debug.app.toasts.show('2.11BSD could not be installed: the download failed.', 'error', { action: { label: 'What to do', onClick() {} } }));
      await sleep(300);
      const m = await page.evaluate(() => {
        const r = (q) => document.querySelector(q).getBoundingClientRect();
        const host = document.querySelector('.toasts');
        const box = r('.toast');
        return { narrow: host.classList.contains('narrow'), w: r('.toast .toast-text').width, textBottom: r('.toast .toast-text').bottom, btnTop: r('.toast .toast-action').top, boxW: box.width, innerH: document.querySelector('.toast').scrollHeight, boxH: box.height };
      });
      if (m.narrow) {
        assert.ok(m.btnTop >= m.textBottom - 1, 'the button is under the text');
        assert.ok(m.w >= m.boxW * 0.6, `text column ${m.w} of ${m.boxW}`);
      } else {
        assert.ok(m.w >= 200, `text column ${m.w}`);
      }
      assert.ok(m.innerH <= m.boxH + 1, 'nothing is cut');
    } finally {
      await page.ctx.close();
    }
  });

  test('a kiosk main view: the machine in the middle of the page with its edges in line, with and without the readouts', async () => {
    const page = await openPage(srv, { path: '?chrome=0', width: 1366, height: 768 });
    try {
      for (const readouts of [false, true]) {
        await page.evaluate((r) => window.pdp11Debug.app.prefs.set('readouts', r), readouts);
        const m = await settled(page);
        assert.equal(m.layout, 'side');
        assert.ok(Math.abs(m.panel.top - m.console.top) <= 1 && Math.abs(m.panel.bottom - m.console.bottom) <= 1, `readouts ${readouts}: edges ${JSON.stringify([m.panel, m.console])}`);
        assert.ok(m.sw <= m.iw && m.sh <= m.ih, `readouts ${readouts}: no page scrolling`);
        if (!readouts) assert.ok(Math.abs(m.panel.top - (m.ih - m.panel.bottom)) <= 2, `in the middle (${m.panel.top}, ${m.ih - m.panel.bottom})`);
        else assert.ok(inside(await page.evaluate(() => document.querySelector('#readouts').getBoundingClientRect().toJSON()), m), 'the readouts inside');
      }
    } finally {
      await page.evaluate(() => window.pdp11Debug.app.prefs.set('readouts', false));
      await page.ctx.close();
    }
  });

  test('views: the console alone fills its window, balanced; a kiosk panel keeps a margin; notices in the row', async () => {
    let page = await openPage(srv, { path: '?view=console', width: 1280, height: 800 });
    try {
      const m = await settled(page);
      assert.ok(Math.abs(m.console.left - (m.iw - m.console.right)) <= 1, 'centred');
      assert.ok(m.console.width > 900, `a larger font than 20 px fits this window (${m.console.width})`);
      assert.ok(m.sw <= m.iw && m.sh <= m.ih, 'no page scrolling');
    } finally {
      await page.ctx.close();
    }
    page = await openPage(srv, { path: '?view=panel&chrome=0', width: 1920, height: 1080 });
    try {
      const m = await settled(page);
      assert.ok(m.panel.left >= 8 && m.iw - m.panel.right >= 8 && m.panel.top >= 8 && m.ih - m.panel.bottom >= 8, `a margin all around ${JSON.stringify(m.panel)}`);
    } finally {
      await page.ctx.close();
    }
    for (const [view, shown] of [['console', 'panel'], ['panel', 'console']]) {
      page = await openPage(srv, { width: 1600, height: 900 });
      try {
        await page.evaluate((v) => window.pdp11Debug.app.setAway(v, true), view);
        const m = await settled(page);
        const v = m[shown];
        const left = Math.min(v.left, m.away.left);
        const right = Math.max(v.right, m.away.right);
        assert.ok(Math.abs(left - (m.iw - right)) <= 1, `${view} away: the row is centred (${left}, ${m.iw - right})`);
        assert.ok(Math.abs(m.away.height - v.height) <= 1 && Math.abs(m.away.top - v.top) <= 1, `${view} away: the notice lines up with the ${shown}`);
      } finally {
        await page.ctx.close();
      }
    }
  });

  for (const dpr of DPRS) test(`132 columns at device pixel ratio ${dpr}: the panel stays large, its edges and the header card in line with the console`, async () => {
    const ctx = await newContext({ viewport: { width: 1366, height: 768 }, deviceScaleFactor: dpr });
    const page = await openPage(srv, { context: ctx });
    try {
      await page.evaluate(() => window.pdp11Debug.app.prefs.set('cols', 132));
      for (const [w, h, minPanel] of [[1366, 768, 540], [1600, 900, 700], [1920, 1080, 900], [2560, 1440, 1100]]) {
        await page.setViewportSize({ width: w, height: h });
        const m = await settled(page);
        const b = await pageBoxes(page);
        const at = `${w}x${h}`;
        assert.equal(m.layout, 'wide', at);
        assert.ok(m.sw <= m.iw && m.sh <= m.ih, `${at}: no page scrolling`);
        assert.ok(m.panel.width >= minPanel, `${at}: panel ${m.panel.width} px`);
        assert.ok(m.rowH >= 14, `${at}: rows of ${m.rowH} px`);
        assert.match(b.family, /Glass TTY/, at);
        assert.ok(inside(m.console, m), `${at}: the console inside`);
        assert.ok(Math.abs(m.panel.left - m.console.left) <= 1, `${at}: left edges`);
        assert.ok(Math.abs(b.head.right - m.console.right) <= 1, `${at}: right edges`);
        assert.ok(Math.abs(b.head.top - m.panel.top) <= 1 && Math.abs(b.head.bottom - m.panel.bottom) <= 1, `${at}: the card beside the panel`);
        assert.ok(inside(b.bar, m) && !hit(b.bar, m.console), `${at}: the bar in the card`);
      }
      // the ready banner goes into the card, toasts stay out of its way
      await page.evaluate(() => {
        const a = window.pdp11Debug.app;
        a.showReadyBanner(true);
        a.toasts.show('a toast', 'info', { sticky: true });
      });
      await page.setViewportSize({ width: 1600, height: 900 });
      await settled(page);
      await sleep(250);
      const b = await pageBoxes(page);
      assert.ok(b.banner && b.banner.left >= b.head.left && b.banner.bottom <= b.head.bottom, `the banner in the card ${JSON.stringify(b.banner)}`);
      assert.ok(!hit(b.toasts[0], b.banner), 'the toast clear of the banner');
    } finally {
      await page.evaluate(() => window.pdp11Debug.app.prefs.set('cols', 80));
      await ctx.close();
    }
  });

  test('octal readouts: in the dock or a row under the panel, no page scrolling', async () => {
    const page = await openPage(srv, { width: 1366, height: 768 });
    try {
      await page.evaluate(() => window.pdp11Debug.app.prefs.set('readouts', true));
      for (const [w, h] of [[1920, 1080], [1366, 768], [1280, 720], [1024, 768], [768, 1024]]) {
        await page.setViewportSize({ width: w, height: h });
        const m = await settled(page);
        const r = await page.evaluate(() => document.querySelector('#readouts').getBoundingClientRect().toJSON());
        assert.ok(m.sw <= m.iw && m.sh <= m.ih, `${w}x${h}: no page scrolling`);
        assert.ok(inside(r, m) && r.height > 0, `${w}x${h}: inside ${JSON.stringify(r)}`);
        assert.ok(!overlap(r, m.panel) && !overlap(r, m.console), `${w}x${h}: over nothing`);
      }
    } finally {
      await page.evaluate(() => window.pdp11Debug.app.prefs.set('readouts', false));
      await page.ctx.close();
    }
  });
});
