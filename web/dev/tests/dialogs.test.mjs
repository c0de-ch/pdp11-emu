// The system chooser, install failures, help, menus and messages as the user
// sees them (docs/UI.md §2, §5), with the real catalog's long texts where the
// layout depends on them.
import { after, describe, test } from 'node:test';
import assert from 'node:assert/strict';
import { closeBrowser, mock, newContext, openPage, received, skip, sleep, waitFor } from './helpers.mjs';

const post = (srv, hook, body = {}) => fetch(`${srv.url}mock/${hook}`, { method: 'POST', body: JSON.stringify(body) });
const running = (page) => waitFor(() => page.evaluate(() => window.pdp11Debug.state.run === 'running'), { timeout: 10000, message: 'running' });
const ready = (page) => waitFor(() => page.evaluate(() => window.pdp11Debug.state.ready), { timeout: 10000, message: 'ready' });
const off = (page) => waitFor(() => page.evaluate(() => window.pdp11Debug.state.run === 'off'), { timeout: 10000, message: 'off' });
const card = (id) => `.sys-card[data-id="${id}"]`;

async function chooser(page) {
  await page.click('.system-btn');
  await page.waitForSelector('.sys-card');
}

/** The boxes that overlap: [a index, b index] for two lists of DOMRect-like boxes. */
const overlaps = (a, b, slack = 0.5) => {
  const out = [];
  a.forEach((p, i) => b.forEach((q, j) => {
    if (p !== q && p.left + slack < q.right && q.left + slack < p.right && p.top + slack < q.bottom && q.top + slack < p.bottom) out.push([i, j]);
  }));
  return out;
};

describe('system chooser', { skip }, () => {
  after(closeBrowser);

  test('the real catalog fits a 1366x768 window: the rows line up, nothing is cut, no scroll hint', async () => {
    const srv = await mock({ catalog: true });
    const page = await openPage(srv, { width: 1366, height: 768 });
    try {
      await chooser(page);
      await sleep(100);
      const m = await page.evaluate(() => {
        const box = (e) => e.getBoundingClientRect();
        const body = document.querySelector('.dialog-body');
        return {
          body: { client: body.clientHeight, scroll: body.scrollHeight, rect: box(body).toJSON() },
          hint: !document.querySelector('.scroll-hint').hidden,
          cards: [...document.querySelectorAll('.sys-card')].map((c) => ({
            id: c.dataset.id,
            rect: box(c).toJSON(),
            desc: box(c.querySelector('.desc-box')).toJSON(),
            info: box(c.querySelector('.sys-info')).toJSON(),
            actions: box(c.querySelector('.actions')).toJSON(),
            wide: [...c.querySelectorAll('*')].filter((e) => box(e).right > box(c).right + 0.5).length,
          })),
        };
      });
      assert.equal(m.cards.length, 8, 'the eight systems of the catalog');
      assert.ok(m.body.scroll <= m.body.client, `no scrolling: ${m.body.scroll} of ${m.body.client} px`);
      assert.equal(m.hint, false);
      for (const c of m.cards) {
        assert.ok(c.rect.bottom <= m.body.rect.bottom, `${c.id} inside the dialog`);
        assert.equal(c.wide, 0, `${c.id}: nothing wider than the card`);
      }
      // a row: same top and height, its sizes and buttons at the same height
      const tops = [...new Set(m.cards.map((c) => Math.round(c.rect.top)))];
      assert.equal(tops.length, 2, 'two rows of four');
      for (const top of tops) {
        const row = m.cards.filter((c) => Math.round(c.rect.top) === top);
        assert.equal(row.length, 4);
        for (const c of row) {
          assert.equal(c.rect.height, row[0].rect.height, 'equal height');
          assert.equal(c.info.top, row[0].info.top, 'sizes on one line');
          assert.equal(c.actions.bottom, row[0].actions.bottom, 'buttons on one line');
        }
      }
      assert.deepEqual(page.errors, []);
    } finally {
      await page.ctx.close();
      await srv.close();
    }
  });

  test('a smaller window scrolls and says so: the hint pages down and goes away at the end', async () => {
    const srv = await mock({ catalog: true });
    const page = await openPage(srv, { width: 1024, height: 600 });
    try {
      await chooser(page);
      const hint = page.locator('.scroll-hint');
      await hint.waitFor();
      const top = () => page.evaluate(() => document.querySelector('.dialog-body').scrollTop);
      assert.equal(await top(), 0);
      // what shows through the fade stays clickable, the hint itself pages down without taking the focus
      const under = await page.evaluate(() => {
        const r = document.querySelector('.scroll-hint').getBoundingClientRect();
        return document.elementFromPoint(r.left + 40, r.top + 10)?.closest('.scroll-hint') === null;
      });
      assert.ok(under, 'the fade lets clicks through');
      const focused = await page.evaluate(() => document.activeElement?.dataset.value);
      await hint.locator('span').click();
      assert.equal(await page.evaluate(() => document.activeElement?.dataset.value), focused);
      await waitFor(async () => (await top()) > 50, { message: 'paged down' });
      await page.evaluate(() => { const b = document.querySelector('.dialog-body'); b.scrollTop = b.scrollHeight; });
      await hint.waitFor({ state: 'hidden' });
      await page.evaluate(() => { document.querySelector('.dialog-body').scrollTop = 0; });
      await hint.waitFor();
    } finally {
      await page.ctx.close();
      await srv.close();
    }
  });

  test('Install is the main action of a system that is not installed, Select of one that is', async () => {
    const srv = await mock({ catalog: true });
    const page = await openPage(srv, { width: 1600, height: 900 });
    const actions = (id) => page.locator(`${card(id)} .actions button`).evaluateAll((bs) => bs.map((b) => [b.dataset.action, b.classList.contains('btn-primary'), b.disabled]));
    try {
      await running(page);
      await chooser(page);
      assert.deepEqual(await actions('unixv7'), [['install', true, false], ['select', false, false]]);
      assert.deepEqual(await actions('rt11'), [['install', true, false], ['select', false, false]]);
      assert.ok(await page.getByRole('group', { name: 'UNIX Seventh Edition' }).getByRole('button', { name: 'Install' }).isVisible(), 'a screen reader hears which system a button belongs to');
      assert.equal(await page.textContent(`${card('rt11')} [data-action="install"]`), 'Licence and install');
      await page.click(`${card('unixv7')} [data-action="install"]`);
      await page.click('dialog.dialog[open] [data-value="install"]');
      await page.waitForSelector(`${card('unixv7')} .badge-ok:has-text("Installed")`, { timeout: 10000 });
      await page.waitForSelector(`${card('unixv7')} [data-action="select"].btn-primary`);
      assert.deepEqual(await actions('unixv7'), [['select', true, false], ['reset', false, false]]);
    } finally {
      await page.ctx.close();
      await srv.close();
    }
  });

  test('Factory reset: disabled it keeps its outline and says why; a system that is not selected can always be reset', async () => {
    const srv = await mock();
    const page = await openPage(srv, { width: 1600, height: 900 });
    const reset = (id) => page.locator(`${card(id)} [data-action="reset"]`);
    try {
      await running(page);
      await chooser(page);
      assert.ok(await reset('bsd211').isDisabled(), 'the selected system runs');
      assert.equal(await page.textContent(`${card('bsd211')} .reset .why`), 'Power off first');
      const style = await page.evaluate((sel) => {
        const s = getComputedStyle(document.querySelector(sel));
        return { width: parseFloat(s.borderTopWidth), border: s.borderTopStyle, color: s.borderTopColor, opacity: s.opacity };
      }, `${card('bsd211')} [data-action="reset"]`);
      assert.ok(style.width >= 1 && style.border === 'dashed' && style.color !== 'rgba(0, 0, 0, 0)', JSON.stringify(style));
      assert.equal(style.opacity, '1', 'not faded into the card');
      await page.keyboard.press('Escape');
      await page.evaluate(() => window.pdp11Debug.app.send({ type: 'key', pos: 'off' }));
      await off(page);
      await chooser(page);
      assert.ok(await reset('bsd211').isEnabled(), 'off: it can be reset');
      assert.equal(await page.locator(`${card('bsd211')} .reset .why`).count(), 0);
      // another installed system, without selecting it
      await page.keyboard.press('Escape');
      await page.evaluate(() => window.pdp11Debug.app.send({ type: 'install', id: 'unix-v7' }));
      await waitFor(() => page.evaluate(() => window.pdp11Debug.systems.list.find((s) => s.id === 'unix-v7').installed), { timeout: 10000, message: 'V7 installed' });
      await page.evaluate(() => window.pdp11Debug.app.send({ type: 'key', pos: 'power' }));
      await running(page);
      await chooser(page);
      assert.ok(await reset('unix-v7').isEnabled());
      await reset('unix-v7').click();
      await page.click('dialog.dialog[open] [data-value="ok"]');
      await waitFor(() => received(srv, (m) => m.type === 'factoryReset' && m.id === 'unix-v7').length === 1, { message: 'reset sent' });
    } finally {
      await page.ctx.close();
      await srv.close();
    }
  });

  test('key at LOCK: the chooser says why Select is disabled, and installing still works', async () => {
    const srv = await mock();
    const page = await openPage(srv, { width: 1600, height: 900 });
    try {
      await running(page);
      await post(srv, 'state', { controls: { key: 'lock' } });
      await waitFor(() => page.evaluate(() => window.pdp11Debug.state.controls.key === 'lock'), { message: 'locked' });
      await chooser(page);
      assert.match(await page.textContent('.notice'), /The key is at LOCK, so you cannot switch systems\. Turn the key to POWER or OFF first\./);
      assert.ok(await page.isVisible('.notice'));
      assert.ok(await page.isDisabled(`${card('unix-v7')} [data-action="select"]`));
      assert.ok(await page.isEnabled(`${card('unix-v7')} [data-action="install"]`));
      // the key turned back while the chooser is open
      await post(srv, 'state', { controls: { key: 'power' } });
      await page.waitForSelector('.notice', { state: 'hidden' });
      assert.ok(await page.isEnabled(`${card('unix-v7')} [data-action="select"]`));
    } finally {
      await page.ctx.close();
      await srv.close();
    }
  });

  test('progress: a reset says Resetting…, an install Installing…; the card keeps its height', async () => {
    const srv = await mock({ off: true, installMs: 1500, resetMs: 1500, catalog: true });
    const page = await openPage(srv, { width: 1600, height: 900 });
    const badges = (id) => page.locator(`${card(id)} .badges .badge`).allTextContents();
    // in one step: the cards are rebuilt when the machine's state changes
    const height = (id) => page.evaluate((sel) => document.querySelector(sel).getBoundingClientRect().height, card(id));
    try {
      await off(page);
      await chooser(page);
      const before = await height('unixv7');
      await page.click(`${card('unixv7')} [data-action="install"]`);
      await page.click('dialog.dialog[open] [data-value="install"]');
      await page.waitForSelector(`${card('unixv7')} .progress`);
      assert.ok((await badges('unixv7')).includes('Installing…'));
      assert.equal(await height('unixv7'), before, 'no jump when the progress appears');
      assert.ok(await page.isVisible(`${card('unixv7')} [data-action="cancel"]`));
      assert.equal(await page.locator(`${card('unixv7')} [data-action="select"]`).count(), 0, 'nothing to select yet');
      await page.waitForSelector(`${card('unixv7')} .badge-ok:has-text("Installed")`, { timeout: 10000 });
      assert.equal(await height('unixv7'), before, 'nor when it ends');
      // the reset of the selected system, the machine being off
      await page.click(`${card('bsd211')} [data-action="reset"]`);
      await page.click('dialog.dialog[open] [data-value="ok"]');
      await page.waitForSelector(`${card('bsd211')} .progress`);
      const seen = await badges('bsd211');
      assert.ok(seen.includes('Resetting…'), JSON.stringify(seen));
      assert.ok(!seen.includes('Installing…'), JSON.stringify(seen));
      await page.waitForSelector(`${card('bsd211')} .badge-ok:has-text("Installed")`, { timeout: 10000 });
      assert.match(await page.textContent(`${card('bsd211')} .sys-progress`), /restored to factory state/);
      await sleep(100);
      assert.equal(await page.locator('.toast').count(), 0, 'the open chooser says it: no toast behind it');
      assert.deepEqual(page.errors, []);
    } finally {
      await page.ctx.close();
      await srv.close();
    }
  });

  test('More shows the whole description of one card', async () => {
    const srv = await mock({ catalog: true });
    const page = await openPage(srv, { width: 1366, height: 768 });
    // in one step: the cards are rebuilt when the machine's state changes
    const desc = (id) => page.evaluate((sel) => {
      const e = document.querySelector(sel);
      return [e.clientHeight, e.scrollHeight];
    }, `${card(id)} .desc`);
    try {
      await chooser(page);
      await waitFor(async () => {
        const [h, f] = await desc('bsd211-pl499');
        return h > 0 && f > h;
      }, { message: 'this description is cut' });
      const [h0, full] = await desc('bsd211-pl499');
      const more = page.locator(`${card('bsd211-pl499')} .more`);
      assert.equal(await more.getAttribute('aria-expanded'), 'false');
      await more.click();
      const [h1, full1] = await desc('bsd211-pl499');
      assert.ok(h1 >= full1 && h1 > h0, `all of it: ${h1} of ${full1}`);
      assert.equal(await more.getAttribute('aria-expanded'), 'true');
      assert.equal(await more.textContent(), 'Less');
      const [other, otherFull] = await desc('unixv6');
      assert.ok(otherFull > other, `the others stay short: ${other} of ${otherFull} px`);
      await more.click();
      assert.deepEqual(await desc('bsd211-pl499'), [h0, full]);
    } finally {
      await page.ctx.close();
      await srv.close();
    }
  });

  test('Select closes the chooser and leaves the keyboard in the console; Cancel of its confirmation stays', async () => {
    const srv = await mock();
    const page = await openPage(srv, { width: 1600, height: 900 });
    try {
      await running(page);
      await chooser(page);
      await page.click(`${card('rt11')} [data-action="select"]`);
      await page.locator('dialog.dialog[open].nested').waitFor();
      await page.click('dialog.dialog[open].nested [data-value="cancel"]');
      assert.equal(await page.locator('dialog.dialog[open]').count(), 1, 'back in the chooser');
      assert.equal(await page.evaluate(() => document.activeElement?.dataset.action), 'select');
      await page.click(`${card('unix-v7')} [data-action="select"]`);
      await page.click('dialog.dialog[open].nested [data-value="plug"]');
      await page.waitForSelector('dialog.dialog[open]', { state: 'detached' });
      assert.ok(await page.evaluate(() => document.activeElement?.classList.contains('xterm-helper-textarea')), 'typing goes to the console');
      await waitFor(() => received(srv, (m) => m.type === 'selectSystem' && m.id === 'unix-v7').length === 1, { timeout: 10000, message: 'selected once off' });
    } finally {
      await page.ctx.close();
      await srv.close();
    }
  });
});

describe('a failed install', { skip }, () => {
  after(closeBrowser);

  test('progress hidden by the user: a failure still gets its card, and Try again replaces that card with the new progress', async () => {
    const srv = await mock({ firstRun: true, installMs: 3000 });
    const page = await openPage(srv, { width: 1366, height: 768 });
    try {
      await post(srv, 'install-fail');
      await page.locator('.overlay-card button:has-text("Hide")').click({ timeout: 10000 });
      const failure = page.locator('.overlay-card .failure');
      await failure.waitFor({ timeout: 15000 });
      await post(srv, 'install-fail', { fail: false });
      await failure.locator('button:has-text("Try again")').click();
      await waitFor(async () => (await failure.count()) === 0, { timeout: 5000, message: 'the failure card goes' });
      await waitFor(async () => (await page.locator('.overlay-card').count()) === 1, { timeout: 5000, message: 'the new attempt shows its card' });
    } finally {
      await page.ctx.close();
      await srv.close();
    }
  });

  test('first run without network: one card says what happened and what to do, without a toast; Try again goes on to the boot', async () => {
    const srv = await mock({ firstRun: true, installMs: 800 });
    await post(srv, 'install-fail');
    const page = await openPage(srv, { width: 1366, height: 768 });
    try {
      const failure = page.locator('.overlay-card .failure');
      await failure.waitFor({ timeout: 10000 });
      const text = await failure.textContent();
      assert.match(text, /2\.11BSD could not be installed/);
      assert.match(text, /The download server could not be reached \(connection refused\)\./);
      assert.match(text, /Check that this machine can reach the internet and try again/);
      assert.ok(await failure.locator('code:has-text("/var/lib/pdp11/import")').isVisible(), 'the directory to put the file in');
      assert.ok(await failure.locator('a[href^="https://downloads.example/"]').isVisible(), 'a link to the file');
      assert.ok(!(await failure.locator('pre').isVisible()), 'the raw error only on request');
      await sleep(300);
      assert.equal(await page.locator('.toast').count(), 0, 'no toast besides the card');
      assert.equal(await page.locator('.overlay-card .progress').count(), 0, 'no stale progress bar');
      // it fits the console area, whatever the text
      const m = await page.evaluate(() => {
        const c = document.querySelector('.overlay-card').getBoundingClientRect();
        const a = document.querySelector('#console-area').getBoundingClientRect();
        return { inside: c.left >= a.left && c.right <= a.right && c.top >= a.top && c.bottom <= a.bottom, wide: [...document.querySelectorAll('.overlay-card *')].filter((e) => e.getBoundingClientRect().right > c.right + 0.5).length };
      });
      assert.deepEqual(m, { inside: true, wide: 0 });
      // the keyboard is on the way forward, and it can come back from the terminal, which keeps Tab
      assert.equal(await page.evaluate(() => document.activeElement?.textContent), 'Try again');
      await page.click('.console-screen', { position: { x: 10, y: 10 } });
      assert.ok(await page.evaluate(() => document.activeElement?.classList.contains('xterm-helper-textarea')));
      for (let i = 0; i < 10 && !(await page.evaluate(() => document.activeElement?.closest('.overlay-card'))); i++) await page.keyboard.press('Shift+Tab');
      assert.ok(await page.evaluate(() => document.activeElement?.closest('.overlay-card')), 'Shift+Tab from the terminal reaches the card');
      await failure.locator('summary').click();
      assert.match(await failure.locator('pre').textContent(), /cannot download bsd211\.tar\.gz .*attempt 3/);
      // the raw error for a bug report
      await page.evaluate(() => {
        window.__copied = [];
        const orig = DataTransfer.prototype.setData;
        DataTransfer.prototype.setData = function setData(t, d) {
          window.__copied.push(d);
          return orig.call(this, t, d);
        };
      });
      await failure.locator('.failure-detail button').click();
      assert.equal(await failure.locator('.failure-detail button').textContent(), 'Copied');
      assert.match((await page.evaluate(() => window.__copied)).join(''), /sha256 .*attempt 3/);
      // another way forward
      await failure.locator('button:has-text("Other systems")').click();
      await page.waitForSelector('dialog.dialog[open] .sys-card');
      await page.keyboard.press('Escape');
      // the network is back
      await post(srv, 'install-fail', { fail: false });
      await failure.locator('button:has-text("Try again")').click();
      await waitFor(async () => /Setting up/.test((await page.textContent('.overlay-card').catch(() => '')) ?? ''), { message: 'installing again' });
      assert.ok(await page.evaluate(() => document.activeElement?.classList.contains('xterm-helper-textarea')), 'the keyboard back in the console');
      await waitFor(async () => /Booting/.test((await page.textContent('.overlay-card').catch(() => '')) ?? ''), { timeout: 10000, message: 'booting' });
      await page.waitForSelector('.overlay-card', { state: 'detached', timeout: 15000 });
      await ready(page);
      assert.deepEqual(page.errors, []);
    } finally {
      await page.ctx.close();
      await srv.close();
    }
  });

  test('on a phone held sideways, where the console starts below the fold, the card comes into view', async () => {
    const srv = await mock({ firstRun: true, installMs: 400 });
    await post(srv, 'install-fail');
    const page = await openPage(srv, { width: 844, height: 390 });
    try {
      await page.locator('.overlay-card .failure').waitFor({ timeout: 10000 });
      await sleep(100);
      const r = await page.locator('.overlay-card').evaluate((c) => {
        const b = c.getBoundingClientRect();
        return { top: b.top, bottom: b.bottom, ih: innerHeight };
      });
      assert.ok(r.top >= 0 && r.bottom <= r.ih + 1, `in the window: ${JSON.stringify(r)}`);
    } finally {
      await page.ctx.close();
      await srv.close();
    }
  });

  test('in the chooser: the card says it and keeps saying it, nothing overflows, no bar or Installing… is left; it can be closed', async () => {
    const srv = await mock({ installMs: 400 });
    const page = await openPage(srv, { width: 1366, height: 768 });
    try {
      await running(page);
      await post(srv, 'install-fail');
      await chooser(page);
      await page.click(`${card('unix-v7')} [data-action="install"]`);
      await page.evaluate(() => {
        window.__texts = new Set();
        new MutationObserver(() => {
          for (const t of document.querySelectorAll('.sys-card .progress-text')) window.__texts.add(t.textContent);
        }).observe(document.querySelector('.sys-list'), { childList: true, subtree: true, characterData: true });
      });
      await page.click('dialog.dialog[open] [data-value="install"]');
      const failure = page.locator(`${card('unix-v7')} .failure`);
      await failure.waitFor({ timeout: 10000 });
      // at once: the first card, whatever was in view
      const first = await page.evaluate(() => {
        const c = document.querySelector('.sys-card');
        const b = document.querySelector('.dialog-body').getBoundingClientRect();
        const r = c.getBoundingClientRect();
        return { id: c.dataset.id, inView: r.top >= b.top && r.bottom <= b.bottom };
      });
      assert.deepEqual(first, { id: 'unix-v7', inView: true });
      // what the wait was about
      assert.ok((await page.evaluate(() => [...window.__texts])).some((t) => /Attempt 1 of 3 failed/.test(t)), 'the attempts');
      const check = async () => {
        const m = await page.evaluate((sel) => {
          const c = document.querySelector(sel);
          const r = c.getBoundingClientRect();
          return { over: [...c.querySelectorAll('*')].filter((e) => e.getBoundingClientRect().right > r.right + 0.5).length, bars: c.querySelectorAll('.progress').length, spans: r.width > 600 };
        }, card('unix-v7'));
        assert.deepEqual(m, { over: 0, bars: 0, spans: true }, 'inside the card, no bar, the whole row');
        assert.deepEqual(await page.locator(`${card('unix-v7')} .badges .badge`).allTextContents(), ['Free to use', 'Install failed']);
      };
      await check();
      assert.equal(await page.locator('.toast').count(), 0, 'the card says it');
      assert.match(await page.textContent('dialog.dialog[open] [role="status"]'), /Unix V7 could not be installed\. The download server could not be reached/, 'screen readers hear it');
      // after the time a finished task's progress stays
      await page.evaluate(() => {
        window.pdp11Debug.app.progress.clear();
        window.pdp11Debug.app.chooser.refresh();
      });
      await check();
      await failure.locator('.failure-x').click();
      await page.waitForSelector(`${card('unix-v7')} .failure`, { state: 'detached' });
      assert.ok(await page.isVisible(`${card('unix-v7')} [data-action="install"].btn-primary`), 'back to Install');
      // the next attempt fails with the same words: it shows again
      await page.click(`${card('unix-v7')} [data-action="install"]`);
      await page.click('dialog.dialog[open] [data-value="install"]');
      await failure.waitFor({ timeout: 10000 });
      // and Try again, without the install dialog, succeeds
      await post(srv, 'install-fail', { fail: false });
      await failure.locator('[data-action="retry"]').click();
      await page.waitForSelector(`${card('unix-v7')} .badge-ok:has-text("Installed")`, { timeout: 10000 });
      assert.equal(await failure.count(), 0, 'nothing left of it');
      assert.deepEqual(page.errors, []);
    } finally {
      await page.ctx.close();
      await srv.close();
    }
  });

  test('with the chooser closed a short toast leads to the card; a hobbyist licence is not asked for twice in a page', async () => {
    const srv = await mock({ installMs: 400 });
    const page = await openPage(srv, { width: 1366, height: 768 });
    try {
      await running(page);
      await post(srv, 'install-fail');
      await chooser(page);
      await page.click(`${card('rt11')} [data-action="install"]`);
      await page.check('#accept-licence');
      await page.click('dialog.dialog[open] [data-value="install"]');
      await page.waitForSelector(`${card('rt11')} .failure`, { timeout: 10000 });
      await page.keyboard.press('Escape');
      await page.evaluate(() => window.pdp11Debug.app.send({ type: 'install', id: 'unix-v7' }));
      const toast = page.locator('.toast-error');
      await toast.waitFor({ timeout: 10000 });
      assert.equal(await toast.locator('.toast-text').textContent(), 'Unix V7 could not be installed. The download server could not be reached (connection refused).');
      await toast.locator('.toast-action:has-text("What to do")').click();
      await page.waitForSelector(`${card('unix-v7')} .failure`);
      assert.equal(await page.evaluate(() => document.activeElement?.dataset.action), 'retry', 'on the way forward');
      assert.ok(await page.evaluate(() => {
        const r = document.activeElement.getBoundingClientRect();
        const b = document.querySelector('.dialog-body').getBoundingClientRect();
        return r.top >= b.top && r.bottom <= b.bottom;
      }), 'in view');
      // RT-11's licence was accepted in this page: Try again asks no more
      await post(srv, 'install-fail', { fail: false });
      await page.click(`${card('rt11')} [data-action="retry"]`);
      await waitFor(() => received(srv, (m) => m.type === 'install' && m.id === 'rt11' && m.acceptLicense === true).length === 2, { message: 'second install' });
      assert.equal(await page.locator('dialog.dialog[open] .licence-text').count(), 0, 'no licence dialog');
    } finally {
      await page.ctx.close();
      await srv.close();
    }
  });

  test('a page that opens later shows it too; whatever order the messages take, the raw error is never a toast', async () => {
    const srv = await mock({ firstRun: true, installMs: 400 });
    await post(srv, 'install-fail');
    const page = await openPage(srv, { width: 1366, height: 768 });
    try {
      await page.locator('.overlay-card .failure').waitFor({ timeout: 10000 });
      const later = await openPage(srv, { width: 1366, height: 768 });
      await later.locator('.overlay-card .failure').waitFor();
      assert.equal(await later.locator('.toast').count(), 0);
      await later.ctx.close();
      // the state message first, then the progress
      await page.evaluate(() => {
        const app = window.pdp11Debug.app;
        const failure = { task: 'install', summary: 'It failed.', hint: 'Do this.', detail: 'raw detail' };
        app.onState({ ...app.state, run: 'error', error: 'installing Other: raw detail' });
        app.onProgress({ type: 'progress', task: 'install', id: 'unix-v7', phase: 'error', text: 'raw detail', failure });
      });
      const texts = await page.locator('.toast-text').allTextContents();
      assert.deepEqual(texts, ['Unix V7 could not be installed. It failed.']);
    } finally {
      await page.ctx.close();
      await srv.close();
    }
  });

  test('a failed factory reset: the system stays installed and its card says so', async () => {
    const srv = await mock({ off: true, resetMs: 100 });
    const page = await openPage(srv, { width: 1600, height: 900 });
    try {
      await off(page);
      await post(srv, 'install-fail');
      await chooser(page);
      await page.click(`${card('bsd211')} [data-action="reset"]`);
      await page.click('dialog.dialog[open] [data-value="ok"]');
      const failure = page.locator(`${card('bsd211')} .failure`);
      await failure.waitFor({ timeout: 5000 });
      assert.match(await failure.textContent(), /The factory reset failed/);
      assert.ok((await page.locator(`${card('bsd211')} .badges .badge`).allTextContents()).includes('Installed'));
      assert.equal(await failure.locator('[data-action="retry"]').count(), 0);
      assert.ok(await page.isEnabled(`${card('bsd211')} [data-action="reset"]`), 'it can be tried again');
      await page.keyboard.press('Escape');
      await sleep(100);
      assert.equal(await page.locator('.overlay-card').count(), 0, 'no first-run card: the system is installed');
    } finally {
      await page.ctx.close();
      await srv.close();
    }
  });
});

describe('messages', { skip }, () => {
  after(closeBrowser);

  test('server toasts: their own text and level; one that carries the shutdown request replaces its toast', async () => {
    const srv = await mock();
    const page = await openPage(srv, { width: 1600, height: 900 });
    try {
      const toasts = () => page.locator('#toasts .toast').evaluateAll((ts) => ts.map((t) => [t.className.match(/toast-(\w+)/)[1], t.querySelector('.toast-text').textContent]));
      await post(srv, 'toast', { level: 'warn', text: 'a warning' });
      await post(srv, 'toast', { level: 'error', text: 'an error' });
      await waitFor(async () => (await toasts()).length === 2, { message: 'two toasts' });
      assert.deepEqual(await toasts(), [['warn', 'a warning'], ['error', 'an error']]);
      assert.equal(await page.getAttribute('.toast-error', 'role'), 'alert');
      await page.evaluate(() => { for (const t of document.querySelectorAll('#toasts .toast')) t.remove(); });
      await page.evaluate(() => {
        const app = window.pdp11Debug.app;
        app.onProgress({ type: 'progress', task: 'shutdown', id: 'bsd211', phase: 'step', pct: 50, text: 'Typing “sync”', req: 'r9' });
        // not about this shutdown: its own toast, the shutdown's stays
        app.onToast({ type: 'toast', level: 'info', text: 'something else', req: 'r1' });
        app.onToast({ type: 'toast', level: 'warn', text: 'another thing' });
      });
      assert.deepEqual(await toasts(), [['info', 'Typing “sync”'], ['info', 'something else'], ['warn', 'another thing']]);
      await page.evaluate(() => window.pdp11Debug.app.onToast({ type: 'toast', level: 'warn', text: 'The shutdown failed.', req: 'r9' }));
      assert.deepEqual(await toasts(), [['warn', 'The shutdown failed.'], ['info', 'something else'], ['warn', 'another thing']]);
    } finally {
      await page.ctx.close();
      await srv.close();
    }
  });

  test('not connected: one message for keys and buttons, gone when the connection is back', async () => {
    const srv = await mock();
    const page = await openPage(srv, { width: 1600, height: 900 });
    try {
      await ready(page);
      await page.click('.console-screen');
      await post(srv, 'refuse', { n: 1 });
      await post(srv, 'drop');
      await waitFor(() => page.evaluate(() => !window.pdp11Debug.connected), { message: 'disconnected' });
      await page.keyboard.type('abc');
      await page.evaluate(() => window.pdp11Debug.app.send({ type: 'boot' }));
      const texts = await page.locator('#toasts .toast-text').allTextContents();
      assert.deepEqual(texts, ['Not connected to pdp11d: that was not sent. It reconnects by itself.']);
      await waitFor(() => page.evaluate(() => window.pdp11Debug.connected), { timeout: 15000, message: 'reconnected' });
      await page.locator('#toasts .toast').waitFor({ state: 'detached', timeout: 2000 }); // long before its own time is up
    } finally {
      await page.ctx.close();
      await srv.close();
    }
  });

  test('a simulator error is a sentence, with the way to its log', async () => {
    const srv = await mock();
    const page = await openPage(srv, { width: 1600, height: 900 });
    try {
      await running(page);
      await post(srv, 'state', { run: 'error', error: 'the simulator exited during start-up (exit status 1)' });
      const toast = page.locator('.toast-error');
      await toast.waitFor();
      assert.equal(await toast.locator('.toast-text').textContent(), 'The simulator exited during start-up (exit status 1)');
      assert.ok(await toast.locator('button:has-text("Simulator log")').isVisible());
    } finally {
      await page.ctx.close();
      await srv.close();
    }
  });

  test('the stop dialogs fit the system: RT-11 needs no repair, a system with a clean shutdown recommends it', async () => {
    const rt = await mock({ system: 'rt11' });
    const page = await openPage(rt, { width: 1366, height: 768 });
    try {
      await ready(page);
      const dlg = page.locator('dialog.dialog[open]');
      await page.click('.power-btn');
      let text = await dlg.textContent();
      assert.doesNotMatch(text, /repair|fsck/, 'running');
      assert.match(text, /Make sure no program is running/);
      await page.keyboard.press('Escape');
      // halted from the panel: still in memory
      await page.evaluate(() => window.pdp11Debug.app.panelActions().latch('ENABLE_HALT', 'halt'));
      await waitFor(() => page.evaluate(() => window.pdp11Debug.state.run === 'halted'), { message: 'halted' });
      await page.click('.power-btn');
      text = await dlg.textContent();
      assert.match(text, /RT-11 V5\.3 is still in memory/);
      assert.doesNotMatch(text, /repair|fsck|pulling the plug/, 'halted');
      assert.match(text, /Switching off while a program writes to a disk can damage its files/);
      assert.match(text, /set ENABLE\/HALT to ENABLE and press CONT, then do what it needs on the console/);
      await page.keyboard.press('Escape');
    } finally {
      await page.ctx.close();
      await rt.close();
    }
    const bsd = await mock();
    const page2 = await openPage(bsd, { width: 1366, height: 768 });
    try {
      await ready(page2);
      await page2.click('.power-btn');
      const dlg = page2.locator('dialog.dialog[open]');
      const text = await dlg.textContent();
      assert.match(text, /the disks may need repair at the next boot/);
      assert.match(text, /Clean shutdown \(recommended\): /);
      const b = await dlg.locator('.dialog-actions button').evaluateAll((bs) => bs.map((x) => [x.textContent, x.getBoundingClientRect().width]));
      assert.deepEqual(b.map(([t]) => t), ['Cancel', 'Pull the plug', 'Shut down the OS first']);
      assert.ok(b[2][1] < 2.2 * b[1][1], `no very wide button: ${JSON.stringify(b)}`);
      await page2.keyboard.press('Escape');
    } finally {
      await page2.ctx.close();
      await bsd.close();
    }
  });
});

describe('help', { skip }, () => {
  let srv;
  after(async () => {
    await srv?.close();
    await closeBrowser();
  });

  const openHelp = async (page, tab) => {
    await page.click('button[aria-label="Help"]');
    await page.click(`.tabs button[data-tab="${tab}"]`);
    await page.waitForSelector(`.help-body[data-tab="${tab}"]`);
  };

  for (const [w, h] of [[1920, 1080], [1366, 768], [1024, 768], [844, 390], [390, 844]]) {
    test(`panel guide at ${w}x${h}: the markers cover no label or lamp and no other marker; the figure stays in view with the legend`, async () => {
      srv ??= await mock();
      const page = await openPage(srv, { width: w, height: h });
      try {
        await ready(page);
        await openHelp(page, 'panel');
        await sleep(100);
        const m = await page.evaluate(() => {
          const list = (sel) => [...document.querySelectorAll(sel)].map((e) => e.getBoundingClientRect().toJSON());
          return {
            markers: list('.guide-marker'),
            labels: list('.guide-art svg.panel-svg text'),
            lamps: list('.guide-art .lamp'),
            lit: [...document.querySelectorAll('.guide-art .lamp')].filter((l) => l.style.opacity === '1').map((l) => l.dataset.lamp),
            knob: document.querySelector('.guide-art .key-rot').style.transform,
          };
        });
        assert.equal(m.markers.length, 16);
        assert.deepEqual(overlaps(m.markers, m.labels), [], 'no label covered');
        assert.deepEqual(overlaps(m.markers, m.lamps), [], 'no lamp covered');
        assert.deepEqual(overlaps(m.markers, m.markers), [], 'no marker on another');
        assert.ok(m.lit.includes('RUN') && m.lit.includes('KERNEL') && m.lit.filter((l) => /^A\d/.test(l)).length > 2 && m.lit.filter((l) => /^D\d/.test(l)).length > 2, `a running panel: ${m.lit}`);
        assert.match(m.knob, /rotate\(315deg\)/, 'the key at POWER, as the lamps say');
        // the figure and some legend at once; the legend's end with the figure still there
        const view = () => page.evaluate(() => {
          const body = document.querySelector('.dialog-body').getBoundingClientRect();
          const fig = document.querySelector('.guide-art').getBoundingClientRect();
          const shown = [...document.querySelectorAll('.guide-list li')].filter((l) => {
            const r = l.getBoundingClientRect();
            return r.top >= body.top && r.bottom <= body.bottom;
          }).map((l) => Number(l.dataset.n));
          return { figure: fig.top >= body.top - 1 && fig.bottom <= body.bottom + 1, shown };
        });
        // a window this low has room for the figure alone beside its legend
        const first = await view();
        assert.ok(first.figure, `figure in view: ${JSON.stringify(first)}`);
        assert.ok(first.shown.length >= (h < 500 ? 1 : 2), `legend beside or under it: ${JSON.stringify(first)}`);
        await page.evaluate(() => { const b = document.querySelector('.dialog-body'); b.scrollTop = b.scrollHeight; });
        const last = await view();
        assert.ok(last.figure, `after scrolling: ${JSON.stringify(last)}`);
        assert.ok(last.shown.includes(16), `the legend's end: ${JSON.stringify(last)}`);
        // the entries that scroll under a figure above them stay readable
        await page.keyboard.press('Escape');
        assert.deepEqual(page.errors, []);
      } finally {
        await page.ctx.close();
      }
    });
  }

  test('panel guide: pointing at an entry marks its number on the panel, and the other way round', async () => {
    srv ??= await mock();
    const page = await openPage(srv, { width: 1366, height: 768 });
    try {
      await openHelp(page, 'panel');
      await page.hover('.guide-list li[data-n="4"]');
      assert.ok(await page.locator('.guide-marker[data-n="4"].active').count());
      await page.hover('.guide-marker[data-n="11"]');
      assert.equal(await page.locator('.guide-marker[data-n="4"].active').count(), 0);
      assert.ok(await page.locator('.guide-list li[data-n="11"].active').count());
    } finally {
      await page.ctx.close();
    }
  });

  test('tutorial tab: the steps and the program it will toggle in; the button opens it', async () => {
    srv ??= await mock();
    const page = await openPage(srv, { width: 1366, height: 768 });
    try {
      await openHelp(page, 'tutorial');
      const words = (await page.locator('.listing tbody td:nth-child(2)').allTextContents()).join(' ').split(' ');
      assert.deepEqual(words, ['012700', '000001', '010037', '177570', '000240', '077101', '006100', '000772'], 'UI.md §6');
      assert.equal(await page.locator('.tutorial-steps li').count(), 5);
      // it uses the dialog: no large empty area under a short text
      const m = await page.evaluate(() => {
        const body = document.querySelector('.dialog-body').getBoundingClientRect();
        const cols = document.querySelector('.help-cols').getBoundingClientRect();
        return { filled: (cols.bottom - body.top) / body.height, wide: cols.width / body.width };
      });
      assert.ok(m.filled > 0.5 && m.wide > 0.9, JSON.stringify(m));
      await page.click('.help-body button:has-text("Open the tutorial")');
      await page.waitForSelector('.tutorial');
      assert.equal(await page.locator('dialog.dialog[open]').count(), 0);
    } finally {
      await page.ctx.close();
    }
  });

  test('system tab: every fact readable on a phone, and side by side with the notes on a laptop', async () => {
    srv ??= await mock();
    const phone = await openPage(srv, { width: 390, height: 844 });
    try {
      await openHelp(phone, 'system');
      const m = await phone.evaluate(() => {
        const dialog = document.querySelector('dialog.dialog[open]').getBoundingClientRect();
        return [...document.querySelectorAll('.sys-facts dd')].map((d) => d.getBoundingClientRect().width / dialog.width);
      });
      assert.ok(m.every((f) => f > 0.7), `value columns ${m}`);
      // the install dialog's sizes too
      await phone.keyboard.press('Escape');
      await chooser(phone);
      await phone.click(`${card('unix-v7')} [data-action="install"]`);
      const widths = await phone.evaluate(() => {
        const dialog = [...document.querySelectorAll('dialog.dialog[open]')].at(-1);
        return [...dialog.querySelectorAll('.kv dd')].map((d) => d.getBoundingClientRect().width / dialog.getBoundingClientRect().width);
      });
      assert.ok(widths.length === 3 && widths.every((f) => f > 0.7), `value columns ${widths}`);
    } finally {
      await phone.ctx.close();
    }
    const laptop = await openPage(srv, { width: 1366, height: 768 });
    try {
      await openHelp(laptop, 'system');
      const m = await laptop.evaluate(() => {
        const box = (s) => document.querySelector(s).getBoundingClientRect();
        return { facts: box('.sys-facts').left, text: box('.sys-text').right, sameTop: Math.abs(box('.sys-facts').top - box('.sys-intro').top) };
      });
      assert.ok(m.facts > m.text && m.sameTop < 2, JSON.stringify(m));
      assert.ok(await laptop.isVisible('.sys-facts button:has-text("Show the login hint")'));
    } finally {
      await laptop.ctx.close();
    }
  });

  test('every tab has the same dialog size', async () => {
    srv ??= await mock();
    const page = await openPage(srv, { width: 1366, height: 768 });
    try {
      await page.click('button[aria-label="Help"]');
      const sizes = new Set();
      for (const tab of ['system', 'panel', 'keys', 'tutorial', 'log']) {
        await page.click(`.tabs button[data-tab="${tab}"]`);
        const r = await page.locator('dialog.dialog[open]').boundingBox();
        sizes.add(`${r.width}x${r.height}`);
      }
      assert.equal(sizes.size, 1, [...sizes].join(' '));
    } finally {
      await page.ctx.close();
    }
  });
});

describe('menus, focus, the console after a switch', { skip }, () => {
  after(closeBrowser);

  test('Send key says what each key does, on this system', async () => {
    const srv = await mock();
    const page = await openPage(srv, { width: 1600, height: 900 });
    const hints = () => page.locator('.key-menu .menu-item').evaluateAll((ms) => Object.fromEntries(ms.map((m) => [m.querySelector('.key').textContent, m.querySelector('.muted')?.textContent])));
    try {
      await page.click('.console-head button:has-text("Send key")');
      const h = await hints();
      assert.equal(h['^C'], 'Interrupt a program');
      assert.equal(h['^Z'], 'Suspend or end input');
      assert.equal(h.DEL, 'Erase a character', 'Backspace sends DEL on 2.11BSD');
      assert.equal(Object.keys(h).length, 10);
      assert.ok(Object.values(h).every(Boolean), 'every key has a hint');
      assert.ok(await page.isVisible('.key-menu .menu-title'), 'the guest decides: the menu says so');
      await page.keyboard.press('Escape');
      await page.evaluate(() => {
        const app = window.pdp11Debug.app;
        app.selectedSystem = () => ({ console: { backspace: '#', interrupt: 'del' } });
      });
      await page.click('.console-head button:has-text("Send key")');
      const v7 = await hints();
      assert.equal(v7.DEL, 'Interrupt a program', 'DEL interrupts on UNIX V5-V7');
      assert.match(v7['^C'], /DEL interrupts/);
    } finally {
      await page.ctx.close();
      await srv.close();
    }
  });

  test('after a click on a dialog or menu no focus ring stays on its button; for the keyboard it does', async () => {
    const srv = await mock();
    const page = await openPage(srv, { width: 1600, height: 900 });
    const ring = (sel) => page.locator(sel).evaluate((e) => ({ focused: document.activeElement === e, outline: getComputedStyle(e).outlineStyle }));
    try {
      await ready(page);
      // the mouse: opened and closed with clicks, then with Esc
      await page.click('.system-btn');
      await page.click('dialog.dialog[open] [data-value="close"]');
      assert.deepEqual(await ring('.system-btn'), { focused: true, outline: 'none' });
      await page.click('.system-btn');
      await page.keyboard.press('Escape');
      assert.deepEqual(await ring('.system-btn'), { focused: true, outline: 'none' }, 'Esc after a click');
      await page.click('button[aria-label="View settings"]');
      await page.keyboard.press('Escape');
      assert.deepEqual(await ring('button[aria-label="View settings"]'), { focused: true, outline: 'none' });
      // a key pressed there: the ring is back
      await page.locator('.system-btn').focus();
      await page.keyboard.press('Shift');
      assert.equal((await ring('.system-btn')).focused, true);
      // the keyboard: Tab to the button, Enter, Esc
      await page.evaluate(() => document.activeElement.blur());
      await page.locator('.system-btn').focus();
      await page.keyboard.press('Shift+Tab');
      await page.keyboard.press('Tab');
      await page.keyboard.press('Enter');
      await page.waitForSelector('dialog.dialog[open]');
      await page.keyboard.press('Escape');
      assert.deepEqual(await ring('.system-btn'), { focused: true, outline: 'solid' });
    } finally {
      await page.ctx.close();
      await srv.close();
    }
  });

  test('a system switch starts on a clear console, for every viewer and after a reload; a reboot keeps the text', async () => {
    const srv = await mock();
    const a = await openPage(srv, { width: 1366, height: 768 });
    const b = await openPage(srv, { width: 1366, height: 768 });
    const text = (p) => p.evaluate(() => window.pdp11Debug.consoleText());
    try {
      await ready(a);
      await waitFor(async () => /login: $/m.test(await text(a)), { message: 'login prompt' });
      await a.evaluate(() => window.pdp11Debug.app.send({ type: 'boot' }));
      await waitFor(async () => (await text(a)).match(/Boot from/g)?.length >= 2, { timeout: 10000, message: 'the reboot keeps the old output' });
      await a.evaluate(() => window.pdp11Debug.app.send({ type: 'key', pos: 'off' }));
      await off(a);
      assert.match(await text(a), /2\.11 BSD UNIX/);
      await a.evaluate(() => window.pdp11Debug.app.send({ type: 'selectSystem', id: 'bsd211' }));
      await sleep(200);
      assert.match(await text(a), /2\.11 BSD UNIX/, 'selecting the selected system changes nothing');
      await a.evaluate(() => window.pdp11Debug.app.send({ type: 'selectSystem', id: 'rt11' }));
      await waitFor(async () => (await text(a)) === '' && (await text(b)) === '', { message: 'both consoles empty' });
      await b.reload();
      await b.waitForFunction(() => window.pdp11Debug?.connected && window.pdp11Debug.state);
      await sleep(200);
      assert.equal(await text(b), '', 'nothing old after a reload');
      assert.deepEqual(a.errors, []);
    } finally {
      await a.ctx.close();
      await b.ctx.close();
      await srv.close();
    }
  });

  test('the booting card keeps clear of the boot messages', async () => {
    // long enough for the card to be looked at, in a window whose console takes the whole height
    const srv = await mock({ firstRun: true, installMs: 300, bootHang: true, bootTimeoutSec: 30 });
    const page = await openPage(srv, { width: 1100, height: 560 });
    try {
      const cardEl = page.locator('.overlay-card.compact');
      await cardEl.waitFor({ timeout: 10000 });
      await waitFor(() => page.evaluate(() => /panic/.test(window.pdp11Debug.consoleText())), { timeout: 10000, message: 'the boot messages' });
      await sleep(100);
      const m = await page.evaluate(() => {
        const c = document.querySelector('.overlay-card.compact').getBoundingClientRect();
        const con = window.pdp11Debug.app.console;
        const screen = con.screen.getBoundingClientRect();
        const row = con.cursorRow();
        const rowH = screen.height / row.rows;
        return { card: c.toJSON(), cursorTop: screen.top + row.row * rowH, cursorBottom: screen.top + (row.row + 1) * rowH, height: c.height, rowH };
      });
      assert.ok(m.card.bottom <= m.cursorTop || m.card.top >= m.cursorBottom, `not on the cursor's row: ${JSON.stringify(m)}`);
      assert.ok(m.height < 8 * m.rowH, `a small card: ${m.height} px`);
    } finally {
      await page.ctx.close();
      await srv.close();
    }
  });

  test('no booting card when no boot follows: a failed or cancelled reset with the key at POWER', async () => {
    const srv = await mock({ autostart: false, resetMs: 1500 });
    const page = await openPage(srv, { width: 1366, height: 768 });
    try {
      assert.equal(await page.evaluate(() => window.pdp11Debug.state.power), 'on');
      await post(srv, 'install-fail');
      await chooser(page);
      await page.click(`${card('bsd211')} [data-action="reset"]`);
      await page.click('dialog.dialog[open] [data-value="ok"]');
      await page.waitForSelector(`${card('bsd211')} .failure`, { timeout: 5000 });
      await sleep(200);
      assert.equal(await page.locator('.overlay-card').count(), 0, 'failed');
      await page.click(`${card('bsd211')} [data-action="reset"]`);
      await page.click('dialog.dialog[open] [data-value="ok"]');
      await page.click(`${card('bsd211')} [data-action="cancel"]`);
      await page.waitForSelector(`${card('bsd211')} .badge-ok:has-text("Installed")`);
      await sleep(200);
      assert.equal(await page.locator('.overlay-card').count(), 0, 'cancelled');
    } finally {
      await page.ctx.close();
      await srv.close();
    }
  });

  test('forced colours and reduced motion: the guide keeps its numbers, the hint scrolls at once', async () => {
    const srv = await mock({ catalog: true });
    const ctx = await newContext({ viewport: { width: 1024, height: 600 }, forcedColors: 'active', reducedMotion: 'reduce' });
    const page = await openPage(srv, { context: ctx });
    try {
      await page.click('button[aria-label="Help"]');
      await page.click('.tabs button[data-tab="panel"]');
      assert.equal(await page.locator('.guide-marker').first().evaluate((m) => getComputedStyle(m).forcedColorAdjust), 'none');
      await page.keyboard.press('Escape');
      await chooser(page);
      await page.click('.scroll-hint span');
      assert.ok(await page.evaluate(() => document.querySelector('.dialog-body').scrollTop > 50), 'scrolled at once');
    } finally {
      await ctx.close();
      await srv.close();
    }
  });
});
