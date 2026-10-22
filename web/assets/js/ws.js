// WebSocket client for the pdp11d protocol (docs/PROTOCOL.md §1):
// binary panel frames and console bytes, JSON control messages, reconnect
// with backoff (0.5 s -> 5 s, x2), version-change reload, liveness pings.
// After a handshake that failed (e.g. a session cookie that a pdp11d
// restart invalidated) it fetches api/status before the next attempt, so
// that the browser sends its Basic credentials again (PROTOCOL.md).

import { Emitter } from './util.js';

export const MSG_FRAME = 0x01;
export const MSG_CONSOLE = 0x10;
const BACKOFF_MIN = 500;
const BACKOFF_MAX = 5000;
const PING_EVERY = 20000;
const PONG_TIMEOUT = 10000;
const SENT_LOG = 500;

/**
 * Events: open, close({code, retryIn}), hello, state, systems, progress,
 * toast, log, frame(Uint8Array lamps, seq), console(Uint8Array),
 * subscribed(subs, prev): prev is null after (re)connecting (every channel
 * is new and sends its history again), else the previous subscription.
 */
export class Connection extends Emitter {
  /**
   * @param {object} o
   * @param {string[]} o.subs        initial subscription (panel, console, log)
   * @param {string} [o.url]         base for the relative "ws" URL (default: the page)
   */
  constructor({ subs, url } = {}) {
    super();
    this.subs = [...new Set(subs ?? ['panel', 'console'])];
    this.base = url ?? location.href;
    this.ws = null;
    this.backoff = BACKOFF_MIN;
    this.retryTimer = 0;
    this.pingTimer = 0;
    this.pongTimer = 0;
    this.closedByUser = false;
    this.connected = false;
    this.hello = null;
    this.sent = []; // debug log of sent messages (window.pdp11Debug)
  }

  url() {
    const u = new URL(`ws?sub=${encodeURIComponent(this.subs.join(','))}`, this.base);
    u.protocol = u.protocol === 'https:' ? 'wss:' : 'ws:';
    return u.href;
  }

  connect() {
    this.closedByUser = false;
    clearTimeout(this.retryTimer);
    let ws;
    try {
      ws = new WebSocket(this.url());
    } catch (err) {
      console.error('WebSocket', err);
      this.scheduleRetry();
      return;
    }
    ws.binaryType = 'arraybuffer';
    this.ws = ws;
    ws.onopen = () => {
      if (ws !== this.ws) return;
      this.connected = true;
      this.emit('open');
      this.emit('subscribed', [...this.subs], null);
      this.startPing();
    };
    ws.onmessage = (ev) => {
      if (ws !== this.ws) return;
      this.onMessage(ev.data);
    };
    ws.onclose = (ev) => {
      if (ws !== this.ws) return;
      this.ws = null;
      const was = this.connected;
      this.connected = false;
      this.stopPing();
      if (this.closedByUser) {
        this.emit('close', { code: ev.code, retryIn: 0, wasConnected: was });
        return;
      }
      const retryIn = this.scheduleRetry({ renew: !was });
      this.emit('close', { code: ev.code, retryIn, wasConnected: was });
    };
    ws.onerror = () => { /* onclose follows */ };
  }

  /** Reconnects after the backoff delay; renew: fetch api/status first (the handshake failed). */
  scheduleRetry({ renew = false } = {}) {
    const delay = this.backoff;
    this.backoff = Math.min(BACKOFF_MAX, this.backoff * 2);
    clearTimeout(this.retryTimer);
    this.retryTimer = setTimeout(async () => {
      if (renew) await this.renewAuth();
      if (!this.closedByUser && !this.ws) this.connect();
    }, delay);
    return delay;
  }

  /**
   * An ordinary request lets the browser send its cached Basic credentials
   * again (or ask for them) and get a new session cookie; a refused
   * WebSocket handshake does not do that in every browser (Firefox, Safari).
   */
  async renewAuth() {
    try {
      await fetch(new URL('api/status', this.base), { credentials: 'same-origin', cache: 'no-store' });
    } catch { /* pdp11d not reachable yet: the next handshake fails too */ }
  }

  /** Reconnect immediately (e.g. the user clicked "retry"). */
  retryNow() {
    if (this.ws) return;
    this.backoff = BACKOFF_MIN;
    this.connect();
  }

  close() {
    this.closedByUser = true;
    clearTimeout(this.retryTimer);
    this.stopPing();
    this.ws?.close(1000, 'bye');
  }

  startPing() {
    this.stopPing();
    this.pingTimer = setInterval(() => {
      if (!this.connected) return;
      this.send({ type: 'ping' }, { log: false });
      clearTimeout(this.pongTimer);
      this.pongTimer = setTimeout(() => {
        // no pong: the connection is dead even if the socket did not notice
        this.ws?.close(4000, 'pong timeout');
      }, PONG_TIMEOUT);
    }, PING_EVERY);
  }

  stopPing() {
    clearInterval(this.pingTimer);
    clearTimeout(this.pongTimer);
  }

  onMessage(data) {
    if (data instanceof ArrayBuffer) {
      const b = new Uint8Array(data);
      if (b.length === 0) return;
      if (b[0] === MSG_FRAME) {
        if (b.length >= 4 + 64 && b[1] === 1) this.emit('frame', b.subarray(4, 4 + 64), b[2] | (b[3] << 8));
      } else if (b[0] === MSG_CONSOLE) {
        this.emit('console', b.subarray(1));
      }
      return;
    }
    let m;
    try {
      m = JSON.parse(data);
    } catch {
      console.warn('ignoring malformed message', String(data).slice(0, 80));
      return;
    }
    if (!m || typeof m.type !== 'string') return;
    switch (m.type) {
      case 'hello':
        this.hello = m;
        this.backoff = BACKOFF_MIN; // a full handshake resets the backoff
        this.emit('hello', m);
        break;
      case 'pong':
        clearTimeout(this.pongTimer);
        break;
      default:
        this.emit(m.type, m);
    }
  }

  log(entry) {
    this.sent.push({ t: Date.now(), ...entry });
    if (this.sent.length > SENT_LOG) this.sent.splice(0, this.sent.length - SENT_LOG);
  }

  /** Sends a JSON message; returns false when not connected. */
  send(msg, { log = true } = {}) {
    if (log) this.log({ msg });
    if (!this.connected || this.ws?.readyState !== WebSocket.OPEN) return false;
    this.ws.send(JSON.stringify(msg));
    return true;
  }

  /** Sends console input bytes (one binary message, caller keeps it <= 64 KiB). */
  sendConsole(bytes) {
    this.log({ console: bytes.length, text: bytes.length <= 64 ? String.fromCharCode(...bytes) : undefined });
    if (!this.connected || this.ws?.readyState !== WebSocket.OPEN) return false;
    const buf = new Uint8Array(bytes.length + 1);
    buf[0] = MSG_CONSOLE;
    buf.set(bytes, 1);
    this.ws.send(buf);
    return true;
  }

  /** Changes the subscription; history follows for newly added channels. */
  subscribe(subs) {
    const next = [...new Set(subs)];
    if (next.length === this.subs.length && next.every((s) => this.subs.includes(s))) return;
    const prev = this.subs;
    this.subs = next;
    if (this.send({ type: 'subscribe', sub: next })) this.emit('subscribed', [...next], prev);
  }

  /** Bytes the browser has not handed to the network yet. */
  buffered() {
    return this.ws?.bufferedAmount ?? 0;
  }
}
