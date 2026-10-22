// Console: data in both directions, key mapping, copy/paste, paced paste,
// send-key menu, bell, history replay, terminal-query answerer
// (docs/UI.md §5, docs/PROTOCOL.md §1.4).
import { after, before, describe, test } from 'node:test';
import assert from 'node:assert/strict';
import { closeBrowser, mock, openPage, skip, sleep, waitFor } from './helpers.mjs';

const text = (page) => page.evaluate(() => window.pdp11Debug.consoleText());
const binary = (srv, client) => srv.received.filter((r) => r.binary === 0x10 && (client === undefined || r.client === client));
const machine = async (srv) => (await fetch(`${srv.url}mock/machine`)).json();
const paste = (page, s) => page.evaluate((str) => {
  const ta = document.querySelector('.console-screen .xterm-helper-textarea');
  const dt = new DataTransfer();
  dt.setData('text/plain', str);
  ta.dispatchEvent(new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true }));
}, s);
const pasteLine = (page) => page.evaluate(() => {
  const el = document.querySelector('.paste-progress');
  return { shown: !el.hidden, text: el.querySelector('.paste-text').textContent, cancel: !el.querySelector('button').hidden };
});

async function waitText(page, re, message) {
  return waitFor(async () => re.test(await text(page)), { timeout: 10000, message: message ?? String(re) });
}

describe('console', { skip }, () => {
  let srv;
  let page;
  before(async () => {
    srv = await mock();
    page = await openPage(srv, { width: 1920, height: 1080 });
    await waitText(page, /login: $/m, 'login prompt');
  });
  after(async () => {
    await page?.ctx.close();
    await srv?.close();
    await closeBrowser();
  });

  test('uses Glass TTY VT220 at an integer multiple of 20 px when it fits', async () => {
    const f = await page.evaluate(() => {
      const r = document.querySelector('.console-screen .xterm-screen').getBoundingClientRect();
      return { font: window.pdp11Debug.consoleFont(), w: r.width, h: r.height };
    });
    assert.match(f.font.family, /Glass TTY VT220/);
    assert.equal(f.font.size % 20, 0);
    const k = f.font.size / 20;
    assert.equal(Math.round(f.h), 480 * k, 'rows are exactly 20 px per scale step');
    assert.equal(Math.round(f.w), 800 * k);
  });

  test('typed keys go to the guest and its output appears', async () => {
    await page.click('.console-screen');
    await page.keyboard.type('root');
    await page.keyboard.press('Enter');
    await waitText(page, /^# $/m, 'shell prompt');
    await page.keyboard.type('echo hello pdp');
    await page.keyboard.press('Enter');
    await waitText(page, /^hello pdp$/m, 'echo output');
    const sent = binary(srv).map((r) => r.text).join('');
    assert.ok(sent.includes('root\r') && sent.includes('echo hello pdp\r'), `sent: ${JSON.stringify(sent)}`);
  });

  test('Backspace and Ctrl+C follow the system\'s key mapping, unless "authentic keys"', async () => {
    const last = () => binary(srv).at(-1)?.text;
    await page.click('.console-screen');
    await page.keyboard.press('Backspace');
    await waitFor(() => last() === '\x7f', { message: 'DEL for 2.11BSD' });
    await page.evaluate(() => window.pdp11Debug.app.console.setKeys({ backspace: '#', interrupt: 'del' }));
    await page.keyboard.press('Backspace');
    await waitFor(() => last() === '#', { message: '# for Unix V7' });
    // with a modifier (word or line erase habits), and Delete: never DEL, the V7 interrupt
    for (const key of ['Control+Backspace', 'Alt+Backspace', 'Meta+Backspace', 'Shift+Backspace', 'Delete', 'Control+Delete']) {
      const n = binary(srv).length;
      await page.keyboard.press(key);
      await waitFor(() => binary(srv).length > n, { message: `${key} sent` });
      assert.deepEqual(binary(srv).slice(n).map((r) => r.text), ['#'], key);
    }
    await page.keyboard.press('Control+c');
    await waitFor(() => last() === '\x7f', { message: 'Ctrl+C sends DEL' });
    await page.evaluate(() => window.pdp11Debug.app.prefs.set('authenticKeys', true));
    await page.keyboard.press('Backspace');
    await waitFor(() => last() === '\x7f', { message: 'authentic: Backspace = DEL' });
    await page.keyboard.press('Control+c');
    await waitFor(() => last() === '\x03', { message: 'authentic: Ctrl+C = ^C' });
    await page.evaluate(() => {
      window.pdp11Debug.app.prefs.set('authenticKeys', false);
      window.pdp11Debug.app.console.setKeys(window.pdp11Debug.app.selectedSystem().console);
    });
  });

  test('pastes are split into messages of at most 4 KiB', async () => {
    const before = binary(srv).length;
    const n = 10 * 1024;
    await page.evaluate((len) => {
      const ta = document.querySelector('.console-screen .xterm-helper-textarea');
      const dt = new DataTransfer();
      dt.setData('text/plain', 'x'.repeat(len - 2) + '\r\n');
      ta.dispatchEvent(new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true }));
    }, n);
    await waitFor(() => binary(srv).slice(before).reduce((s, r) => s + r.length, 0) >= n - 1, { timeout: 15000, message: 'whole paste sent' });
    const chunks = binary(srv).slice(before).map((r) => r.length);
    assert.ok(chunks.length >= 3, `split into ${chunks.length} messages`);
    assert.ok(chunks.every((c) => c <= 4096), `chunks ${chunks}`);
    assert.equal(chunks.reduce((a, b) => a + b, 0), n - 1, 'CR LF became a single CR');
    await page.keyboard.press('Control+u');
  });

  test('copy uses the DOM copy event (works without a secure context)', async () => {
    await page.evaluate(() => {
      window.__copied = [];
      const orig = DataTransfer.prototype.setData;
      DataTransfer.prototype.setData = function setData(t, d) {
        window.__copied.push(d);
        return orig.call(this, t, d);
      };
      window.pdp11Debug.app.console.term.selectAll();
    });
    await page.click('.console-screen');
    await page.evaluate(() => window.pdp11Debug.app.console.term.selectAll());
    await page.keyboard.press('Control+Shift+C');
    const copied = await page.evaluate(() => window.__copied);
    assert.ok(copied.some((c) => c.includes('login:')), 'selection copied');
    // and Ctrl+Shift+C was not sent to the guest
    assert.notEqual(binary(srv).at(-1)?.text, '\x03');
  });

  test('the send-key menu sends control characters and leaves the focus in the terminal', async () => {
    await page.click('.console-screen');
    await page.click('.console-head button:has-text("Send key")');
    assert.deepEqual(await page.locator('.menu .menu-item').allTextContents(), ['^C', '^O', '^Q', '^S', '^T', '^U', '^W', '^Z', 'DEL', 'ESC']);
    await page.click('.menu .menu-item:has-text("^Z")');
    await waitFor(() => binary(srv).at(-1)?.text === '\x1a', { message: '^Z sent' });
    // the next keys go to the PDP-11, not to the menu button
    assert.ok(await page.evaluate(() => document.activeElement?.classList.contains('xterm-helper-textarea')), 'focus in the terminal');
    const n = binary(srv).length;
    await page.click('.console-head button:has-text("Send key")');
    await page.keyboard.press('ArrowDown');
    await page.keyboard.press('Enter'); // ^O, chosen with the keyboard
    await page.keyboard.type('ok');
    await waitFor(() => binary(srv).slice(n).map((r) => r.text).join('') === '\x0fok', { message: '^O, then the typed keys' });
    assert.equal(await page.locator('.menu').count(), 0, 'menu closed');
    // Esc still returns to the button
    await page.click('.console-head button:has-text("Send key")');
    await page.keyboard.press('Escape');
    assert.equal(await page.evaluate(() => document.activeElement?.textContent), 'Send key ▾');
    await page.click('.console-screen');
    await page.keyboard.press('Control+u');
  });

  test('Shift+Tab leaves the console, Tab goes to the PDP-11; screen reader mode', async () => {
    await page.click('.console-screen');
    const n = binary(srv).length;
    await page.keyboard.press('Tab');
    await waitFor(() => binary(srv).length > n, { message: 'Tab sent' });
    await page.keyboard.press('Shift+Tab');
    assert.equal(await page.evaluate(() => document.activeElement?.textContent), 'Send key ▾', 'the focus moved back');
    await sleep(200);
    assert.deepEqual(binary(srv).slice(n).map((r) => r.text), ['\t'], 'no back-tab sent');
    // the console text for screen readers (xterm.js accessibility tree)
    assert.equal(await page.locator('.xterm-accessibility-tree').count(), 0);
    await page.evaluate(() => window.pdp11Debug.app.prefs.set('screenReader', true));
    await waitFor(async () => /#/.test(await page.locator('.xterm-accessibility-tree').textContent().catch(() => '')), { message: 'accessibility tree' });
    await page.evaluate(() => window.pdp11Debug.app.prefs.set('screenReader', false));
    await waitFor(async () => (await page.locator('.xterm-accessibility-tree').count()) === 0, { message: 'tree removed' });
    await page.click('.console-screen');
  });

  test('a bell flashes the console frame', async () => {
    const flashed = page.waitForFunction(() => document.querySelector('.console-frame').classList.contains('bell'), null, { timeout: 3000 });
    await fetch(`${srv.url}mock/console`, { method: 'POST', body: JSON.stringify({ text: '\x07' }) });
    await flashed;
  });

  test('reconnecting resets the terminal and replays the history once', async () => {
    const marker = `marker-${Date.now()}`;
    await fetch(`${srv.url}mock/console`, { method: 'POST', body: JSON.stringify({ text: `\r\n${marker}\r\n` }) });
    await waitText(page, new RegExp(marker));
    await fetch(`${srv.url}mock/drop`, { method: 'POST' });
    await waitFor(() => page.evaluate(() => !window.pdp11Debug.connected), { message: 'disconnected' });
    await waitFor(() => page.evaluate(() => window.pdp11Debug.connected), { timeout: 8000, message: 'reconnected' });
    await waitText(page, new RegExp(marker));
    const t = await text(page);
    assert.equal(t.split(marker).length - 1, 1, 'history written exactly once');
  });

  test('only the answering client replies to terminal queries', async () => {
    const b = await openPage(srv, { width: 1600, height: 900 });
    try {
      await waitText(b, new RegExp('login|#'));
      const ids = await Promise.all([page, b].map((p) => p.evaluate(() => window.pdp11Debug.hello.clientId)));
      // page typed last, so page is the answerer
      await page.click('.console-screen');
      await page.keyboard.type(' ');
      await waitFor(() => page.evaluate((id) => window.pdp11Debug.state.console.answerer === id, ids[0]), { message: 'page is the answerer' });
      await sleep(1600); // let the "just typed" grace period of b (none) and page settle
      const n0 = srv.received.length;
      await fetch(`${srv.url}mock/query`, { method: 'POST' });
      await waitFor(() => binary(srv, ids[0]).some((r, i, a) => /\x1b\[\?[\d;]*c/.test(r.text ?? '') && srv.received.indexOf(r) >= n0), { message: 'answerer replied' });
      await sleep(400);
      assert.equal(srv.received.slice(n0).filter((r) => r.client === ids[1] && r.binary === 0x10).length, 0, 'the other client stayed silent');
      // now b types and becomes the answerer
      await b.click('.console-screen');
      await b.keyboard.type(' ');
      await waitFor(() => b.evaluate((id) => window.pdp11Debug.state.console.answerer === id, ids[1]), { message: 'b is the answerer' });
      const n1 = srv.received.length;
      await sleep(1600);
      await fetch(`${srv.url}mock/query`, { method: 'POST' });
      await waitFor(() => srv.received.slice(n1).some((r) => r.client === ids[1] && /\x1b\[\?[\d;]*c/.test(r.text ?? '')), { message: 'b replied' });
      await sleep(400);
      assert.equal(srv.received.slice(n1).filter((r) => r.client === ids[0] && /\x1b\[/.test(r.text ?? '')).length, 0, 'page stayed silent');
      // DECRQSS and the colour queries follow the same rule, and a silent
      // viewer stays silent for the queries after them
      const n2 = srv.received.length;
      for (const seq of ['\x1bP$qm\x1b\\', '\x1b]11;?\x07', '\x1b]10;?\x1b\\', '\x1b]4;1;?\x07', '\x1b[c']) {
        await fetch(`${srv.url}mock/query`, { method: 'POST', body: JSON.stringify({ seq }) });
      }
      await waitFor(() => srv.received.slice(n2).filter((r) => r.client === ids[1] && r.binary === 0x10).length >= 5, { message: 'b answered all five' });
      await sleep(400);
      assert.deepEqual(srv.received.slice(n2).filter((r) => r.client === ids[0] && r.binary === 0x10).map((r) => r.text), [], 'page answered none');
    } finally {
      await b.ctx.close();
    }
  });

  test('history replay does not answer old queries; live ones are answered', async () => {
    // a query sits in the history of a machine nobody watches; the first
    // client becomes the answerer, but must not reply to the replayed query
    const fresh = await mock();
    try {
      await waitFor(async () => (await (await fetch(`${fresh.url}api/state`)).json()).ready, { timeout: 10000, message: 'booted' });
      await fetch(`${fresh.url}mock/query`, { method: 'POST' });
      const c = await openPage(fresh, { width: 1600, height: 900 });
      try {
        const id = await c.evaluate(() => window.pdp11Debug.hello.clientId);
        await waitFor(() => c.evaluate((me) => window.pdp11Debug.state.console.answerer === me, id), { message: 'sole client is the answerer' });
        await sleep(1200);
        assert.equal(binary(fresh, id).length, 0, 'no reply while writing the history');
        await fetch(`${fresh.url}mock/query`, { method: 'POST' });
        await waitFor(() => binary(fresh, id).some((r) => /\x1b\[\?[\d;]*c/.test(r.text ?? '')), { message: 'live query answered' });
      } finally {
        await c.ctx.close();
      }
    } finally {
      await fresh.close();
    }
  });

  test('the history is exactly the first message: live output right behind it is answered', async () => {
    // a query in the history (not answered) and one live query that the server
    // sends immediately after the history, while the client is still parsing it
    const fresh = await mock();
    try {
      await waitFor(async () => (await (await fetch(`${fresh.url}api/state`)).json()).ready, { timeout: 10000, message: 'booted' });
      await fetch(`${fresh.url}mock/console`, { method: 'POST', body: JSON.stringify({ text: `${'z'.repeat(60000)}\r\n` }) });
      await fetch(`${fresh.url}mock/query`, { method: 'POST' });
      await fetch(`${fresh.url}mock/query-after-history`, { method: 'POST' });
      const c = await openPage(fresh, { width: 1600, height: 900 });
      try {
        const id = await c.evaluate(() => window.pdp11Debug.hello.clientId);
        const replies = () => binary(fresh, id).filter((r) => /\x1b\[\?[\d;]*c/.test(r.text ?? ''));
        await waitFor(() => replies().length > 0, { message: 'live query answered' });
        await sleep(1200);
        assert.equal(replies().length, 1, 'one reply: the live query, not the replayed one');
        // keys typed during the replay are kept, replies are dropped
        assert.deepEqual(c.errors, []);
      } finally {
        await c.ctx.close();
      }
    } finally {
      await fresh.close();
    }
  });

  test('keys typed while the history is replayed are sent afterwards', async () => {
    const fresh = await mock();
    try {
      await waitFor(async () => (await (await fetch(`${fresh.url}api/state`)).json()).ready, { timeout: 10000, message: 'booted' });
      // a large history takes a moment to parse
      await fetch(`${fresh.url}mock/console`, { method: 'POST', body: JSON.stringify({ text: `${'h'.repeat(200000)}\r\nlogin: ` }) });
      const c = await openPage(fresh, { width: 1600, height: 900 });
      try {
        const id = await c.evaluate(() => window.pdp11Debug.hello.clientId);
        // simulate typing during the replay: drive the gate directly
        const r = await c.evaluate(() => {
          const con = window.pdp11Debug.app.console;
          con.beginHistory();
          con.write(new TextEncoder().encode('x'.repeat(100000)));
          const gated = con.gate;
          con.term.input('k', true); // typed while gated: kept
          con.term.input('\x1b[?1;2c', true); // a reply-like sequence: dropped
          // keys mapped outside xterm.js, Send key and a paste wait too, in order
          con.onKey(new KeyboardEvent('keydown', { key: 'Backspace' }));
          con.sendKey('\x15');
          con.term.input('z', true);
          con.pasteText('pasted');
          return { gated };
        });
        assert.equal(r.gated, true, 'gate closed while the history is parsed');
        await waitFor(() => binary(fresh, id).some((b) => b.text === 'pasted'), { message: 'kept keys sent after the replay' });
        await sleep(300);
        assert.deepEqual(binary(fresh, id).map((b) => b.text), ['k', '\x7f', '\x15', 'z', 'pasted'], 'in the order typed');
        assert.equal(binary(fresh, id).filter((b) => /\x1b\[/.test(b.text ?? '')).length, 0, 'reply-like sequence dropped');
      } finally {
        await c.ctx.close();
      }
    } finally {
      await fresh.close();
    }
  });

  test('a reconnect drops the output xterm.js has not parsed yet: nothing old follows the reset', async () => {
    const c = await openPage(srv, { width: 1600, height: 900 });
    try {
      const r = await c.evaluate(async () => {
        const con = window.pdp11Debug.app.console;
        const enc = new TextEncoder();
        // the backlog of a slow client: far more than xterm.js parses at once
        for (let i = 0; i < 3000; i++) con.write(enc.encode(`OLD-${i} ${'o'.repeat(70)}\r\n`));
        con.beginHistory();
        con.write(enc.encode('HISTORY-START\r\nthe history\r\n'));
        for (let i = 0; i < 1500 && !window.pdp11Debug.consoleText().includes('the history'); i++) await new Promise((res) => setTimeout(res, 20));
        const t = window.pdp11Debug.consoleText();
        return { old: (t.match(/OLD-/g) ?? []).length, first: t.split('\n')[0] };
      });
      assert.equal(r.old, 0, 'no old output after the reset');
      assert.equal(r.first, 'HISTORY-START');
      // a second reconnect before that history reached xterm.js, with an empty
      // history: the gate the first one closed opens at once
      const gate = await c.evaluate(() => {
        const con = window.pdp11Debug.app.console;
        const enc = new TextEncoder();
        for (let i = 0; i < 3000; i++) con.write(enc.encode(`OLD-${i}\r\n`));
        con.beginHistory();
        con.write(enc.encode('a history still waiting for xterm.js\r\n'));
        const closed = con.gate;
        con.beginHistory();
        con.write(new Uint8Array(0));
        return { closed, open: !con.gate };
      });
      assert.deepEqual(gate, { closed: true, open: true });
    } finally {
      await c.ctx.close();
    }
  });

  test('keys typed during a paste follow the pasted text', async () => {
    const fast = await mock({ cps: 3000 });
    const p = await openPage(fast, { width: 1600, height: 900 });
    try {
      await waitText(p, /login: $/m);
      await p.click('.console-screen');
      // more than the paste window: the rest waits in the browser, and Q behind it
      await paste(p, 'y'.repeat(12000));
      await p.keyboard.type('Q');
      await waitFor(async () => (await machine(fast)).guestInput.includes('Q'), { timeout: 15000, message: 'Q typed' });
      assert.equal((await machine(fast)).guestInput, `${'y'.repeat(12000)}Q`);
    } finally {
      await p.ctx.close();
      await fast.close();
    }
  });

  test('a slow paste shows its progress; the interrupt key or Cancel stops it', async () => {
    const slow = await mock({ cps: 50 });
    const p = await openPage(slow, { width: 1600, height: 900 });
    const cancels = () => slow.received.filter((r) => r.msg?.type === 'inputCancel').length;
    try {
      await waitText(p, /login: $/m);
      await p.click('.console-screen');
      await paste(p, 'y'.repeat(12000));
      await p.waitForSelector('.paste-progress:not([hidden])');
      assert.match(await p.textContent('.paste-progress .paste-text'), /Pasting… \d+ bytes/);
      // Ctrl+C (the interrupt on 2.11BSD): the rest of the paste is dropped, then ^C goes out
      await p.keyboard.press('Control+c');
      await waitFor(async () => (await machine(slow)).guestInput.endsWith('\x03'), { message: '^C reached the guest' });
      assert.equal(cancels(), 1, 'inputCancel sent');
      assert.ok((await machine(slow)).guestInput.length < 1000, 'the paste stopped');
      await p.waitForSelector('.paste-progress[hidden]', { state: 'attached' });
      // Cancel in the console header
      await paste(p, 'y'.repeat(3000));
      await p.waitForSelector('.paste-progress:not([hidden])');
      await p.click('.paste-progress button');
      await waitFor(() => cancels() === 2, { message: 'inputCancel sent' });
      await waitFor(() => p.evaluate(() => window.pdp11Debug.state.console.inputQueued === 0), { message: 'queue dropped' });
      await p.waitForSelector('.paste-progress[hidden]', { state: 'attached' });
    } finally {
      await p.ctx.close();
      await slow.close();
    }
  });

  test('the paste line belongs to the window that pastes; input held while halted is a hint without Cancel', async () => {
    const slow = await mock({ cps: 50 });
    const a = await openPage(slow, { width: 1600, height: 900 });
    const b = await openPage(slow, { width: 1600, height: 900 });
    const cancels = () => slow.received.filter((r) => r.msg?.type === 'inputCancel').length;
    try {
      await waitText(a, /login: $/m);
      await paste(a, 'p'.repeat(3000));
      await waitFor(async () => (await pasteLine(a)).shown, { message: 'A shows its paste' });
      await sleep(600);
      assert.deepEqual(await pasteLine(b), { shown: false, text: '', cancel: false }, 'B did not paste: nothing to show or cancel');
      // halted: the server holds the input, B's keys too
      await b.evaluate(() => window.pdp11Debug.app.panelActions().latch('ENABLE_HALT', 'halt'));
      await waitFor(() => b.evaluate(() => window.pdp11Debug.state.run === 'halted'), { message: 'halted' });
      await b.click('.console-screen');
      await b.keyboard.type('abc');
      await waitFor(async () => /^\d+ characters waiting until the CPU runs$/.test((await pasteLine(b)).text), { message: 'B: a hint' });
      assert.equal((await pasteLine(b)).cancel, false, 'B cannot cancel');
      assert.match((await pasteLine(a)).text, /^Pasting… \d+ bytes$/);
      await waitFor(() => a.evaluate(() => window.pdp11Debug.app.console.otherQueued() > 0), { message: "A sees B's keys queued" });
      // A's Cancel would drop B's keys too: it asks first
      await a.click('.paste-progress button');
      await a.waitForSelector('dialog.dialog[open]');
      assert.match(await a.textContent('dialog.dialog[open]'), /not part of this paste/);
      await a.click('dialog.dialog[open] [data-value="cancel"]');
      await sleep(200);
      assert.equal(cancels(), 0, 'kept');
      await a.click('.paste-progress button');
      await a.click('dialog.dialog[open] [data-value="ok"]');
      await waitFor(() => cancels() === 1, { message: 'inputCancel sent' });
      await waitFor(async () => !(await pasteLine(a)).shown && !(await pasteLine(b)).shown, { message: 'both lines gone' });
      assert.deepEqual(a.errors, []);
      assert.deepEqual(b.errors, []);
    } finally {
      await a.ctx.close();
      await b.ctx.close();
      await slow.close();
    }
  });

  test('no console errors', () => {
    assert.deepEqual(page.errors, []);
  });
});
