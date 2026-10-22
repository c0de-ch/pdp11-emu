// Chooses the arrangement of panel and console so that the main view fits
// the viewport without page scrolling (UI.md §1): side by side when both
// fit at a useful size, else stacked; phones, and windows too small for a
// readable console next to the panel, get a scrolling layout.
//
// The console's pixel size depends on the font (Glass TTY at integer
// multiples of 20 px, else VT323 scaled), so candidates are scored on the
// resulting panel and console areas.

export const PANEL_ASPECT = 484.4 / 260.4;
const GAP = 10;
const MIN_PANEL_SIDE = 480; // px, narrower panels are not worth side-by-side
const MIN_PANEL_STACK = 360; // px, narrower stacked panels: the scrolling layout instead
const MIN_CONSOLE_ROW = 13; // px row height below which the console is unreadable
const CONSOLE_ROWS = 24;
const GLASS_BONUS = 0.14; // log-score bonus for the crisp integer-scaled font

/**
 * @param {object} o
 * @param {number} o.W, o.H            stage size (px, padding excluded)
 * @param {number} [o.pageH]           viewport height (padding excluded) the scrolling layout
 *                                     may fill once the toolbar has scrolled away (default H)
 * @param {boolean} o.showPanel, o.showConsole
 * @param {number} o.panelExtraH       px below the panel (readouts)
 * @param {{w: number, h: number}} o.consoleChrome   px around the terminal (frame padding, header)
 * @param {(box: {w: number, h: number}) => {mode: object, w: number, h: number}} o.consoleFit
 *        returns the terminal size the console would use for a box
 * @returns {{layout: string, panel: {w: number, h: number}|null, consoleBox: {w: number, h: number}|null}}
 */
export function computeLayout({ W, H, pageH = H, showPanel, showConsole, panelExtraH = 0, consoleChrome, consoleFit }) {
  const fitPanel = (w, h) => {
    const pw = Math.max(0, Math.min(w, (h - panelExtraH) * PANEL_ASPECT));
    return { w: Math.floor(pw), h: Math.floor(pw / PANEL_ASPECT) };
  };
  if (showPanel && !showConsole) return { layout: 'panel', panel: fitPanel(W, H), consoleBox: null };
  if (!showPanel && showConsole) {
    return { layout: 'console', panel: null, consoleBox: { w: W - consoleChrome.w, h: H - consoleChrome.h } };
  }
  if (!showPanel && !showConsole) return { layout: 'none', panel: null, consoleBox: null };

  // panel full width (landscape: as high as the viewport), console below, page scrolls
  const scrolling = () => {
    const panel = fitPanel(W, W > pageH ? pageH : Infinity);
    // the page scrolls here, so the console may be as tall as its width allows
    const cw = W - consoleChrome.w;
    return { layout: 'mobile', panel, consoleBox: { w: cw, h: Math.max(320, Math.round(cw * 0.62)) } };
  };
  // phones and tiny windows
  if (W < 700 || H < 420) return scrolling();

  const candidates = [];
  const consider = (layout, box) => {
    if (box.w < 100 || box.h < 100) return;
    const t = consoleFit(box);
    if (!t || t.rowH < MIN_CONSOLE_ROW && !t.glass) return;
    const cw = t.w + consoleChrome.w;
    const ch = t.h + consoleChrome.h;
    let panel;
    if (layout === 'side') {
      if (ch > H) return;
      panel = fitPanel(W - cw - GAP, H);
      if (panel.w < MIN_PANEL_SIDE) return;
    } else {
      if (cw > W) return;
      panel = fitPanel(W, H - ch - GAP);
      if (panel.w < MIN_PANEL_STACK) return;
    }
    const score = 0.6 * Math.log(panel.w * panel.h) + 0.4 * Math.log(t.w * t.h) + (t.glass ? GLASS_BONUS : 0);
    candidates.push({ layout, panel, consoleBox: box, score, font: t.mode });
  };
  // Glass TTY at 1x, 2x, 3x: boxes exactly the size of the scaled terminal
  for (const k of [1, 2, 3]) {
    const g = consoleFit({ w: 1e9, h: 1e9, glass: k });
    if (!g) continue;
    consider('side', { w: g.w, h: g.h });
    consider('stack', { w: g.w, h: g.h });
  }
  // smooth font: a range of console widths (side) / heights (stack)
  for (let f = 0.25; f <= 0.5; f += 0.025) {
    consider('side', { w: Math.floor(W * f) - consoleChrome.w, h: H - consoleChrome.h });
    consider('stack', { w: W - consoleChrome.w, h: Math.floor(H * f) - consoleChrome.h });
  }
  if (!candidates.length) {
    // nothing scores: stack, keeping 24 readable rows for the console and
    // giving the panel the rest; if that panel is too small, scroll instead.
    // The font's cell height is rounded to device pixels, so 13 px rows may
    // need a few px more than 24 x 13 (e.g. at a device pixel ratio of 1.25).
    let ch = CONSOLE_ROWS * MIN_CONSOLE_ROW;
    for (let h = ch; h <= ch + 2 * CONSOLE_ROWS; h += 2) {
      if ((consoleFit({ w: W - consoleChrome.w, h })?.rowH ?? 0) >= MIN_CONSOLE_ROW) {
        ch = h;
        break;
      }
    }
    const panel = fitPanel(W, H - ch - consoleChrome.h - GAP);
    if (panel.w < MIN_PANEL_STACK) return scrolling();
    return { layout: 'stack', panel, consoleBox: { w: W - consoleChrome.w, h: H - panel.h - panelExtraH - GAP - consoleChrome.h } };
  }
  candidates.sort((a, b) => b.score - a.score);
  const best = candidates[0];
  return { layout: best.layout, panel: best.panel, consoleBox: best.consoleBox };
}
