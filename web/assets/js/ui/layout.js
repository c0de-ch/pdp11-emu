// Chooses the arrangement of panel and console and the console's font so that
// the main view fits the viewport without page scrolling (UI.md §1): side by
// side as columns of equal height when both fit at a useful size, else stacked
// ('wide': the console's header beside a panel much narrower than the console);
// phones, and windows too small for a readable console next to the panel, get a
// scrolling layout.
//
// The console's pixel size depends on the font and the panel has a fixed
// aspect, so the candidates are all fonts of the console (fontChoices() in
// console.js) in each arrangement, scored on the resulting panel and console
// areas. Side by side the terminal must fit into the panel's height: the frame
// takes that height, so both columns share their top and bottom edges.

export const PANEL_ASPECT = 484.4 / 260.4;
export const GAP = 10; // px between panel and console
const MIN_PANEL_SIDE = 480; // px, narrower panels are not worth side-by-side
const MIN_PANEL_STACK = 360; // px, narrower stacked panels: the scrolling layout instead
const MIN_CONSOLE_ROW = 13; // px row height below which the console is unreadable
const MIN_MOBILE_ROW = 12; // the same where the page scrolls and the window is narrower than 80 columns
const WIDE_MARGIN = 48; // px of frame above and below the text that still look like a screen's margin
const TIGHT_MARGIN = 6; // px of frame above and below the text below which it looks cramped
const SIDE_BONUS = 0.2; // the same arrangement at every common desktop size
// side by side the panel stays the larger column: below this share of the width a
// larger console costs more than it brings, whatever height is free
const PANEL_SHARE = 0.54;
const PANEL_SHARE_WEIGHT = 5;
const MIN_HEAD_BESIDE = 300; // px, the console's header beside a stacked panel needs this much

/** The font's own merit, in the units of the area score. */
function fontBonus(f) {
  let b = f.quality * 0.06; // on the pixel grid: crisp strokes and even scan lines
  if (!f.glass) b -= 0.5; // the look changes: only where Glass TTY cannot do it
  if (f.mode?.letterSpacing) b -= 0.1; // condensed
  const row = f.rowH * (f.sharp ? 1.15 : 1); // a dense screen shows a small font better
  if (row < 16) b -= 0.02 * (16 - row) ** 2; // small rows are harder to read than the area says
  return b;
}

/**
 * @param {object} o
 * @param {number} o.W, o.H            stage size (px, padding excluded)
 * @param {number} [o.pageH]           viewport height (padding excluded) the scrolling layout
 *                                     may fill once the toolbar has scrolled away (default H)
 * @param {boolean} o.showPanel, o.showConsole
 * @param {number} [o.panelExtraH]     px below a panel that has no dock under it (readouts)
 * @param {number} [o.dockExtraH]      px the dock under a side-by-side panel needs beyond the header bar's height
 * @param {object} o.chrome            px around the terminal: x (frame, horizontal), y (frame,
 *        vertical, usual margins), minY (the same, smallest margins), mx (x on a phone),
 *        border (the frame's own borders), bar (the console's header bar outside the frame)
 * @param {Array<{w: number, h: number, rowH: number, glass: boolean, quality: number}>} o.fonts
 *        terminal size of every font the console offers (fontChoices() in console.js)
 * @returns {{layout: string, panel: {w: number, h: number}|null, font: object|null, frame: {w: number, h: number}|null}}
 */
export function computeLayout({ W, H, pageH = H, showPanel, showConsole, panelExtraH = 0, dockExtraH = 0, chrome, fonts }) {
  const fitPanel = (w, h) => {
    const pw = Math.floor(Math.max(0, Math.min(w, h * PANEL_ASPECT)));
    return { w: pw, h: pw / PANEL_ASPECT };
  };
  if (showPanel && !showConsole) return { layout: 'panel', panel: fitPanel(W, H - panelExtraH), font: null, frame: null };
  if (!showPanel && !showConsole) return { layout: 'none', panel: null, font: null, frame: null };
  if (!showPanel) {
    // the console alone: the largest font that fits, the smallest if none does
    const room = H - chrome.bar;
    let best = null;
    for (const f of fonts) {
      if (f.w + chrome.x > W || f.h + chrome.minY > room) continue;
      const score = Math.log(f.w * f.h) + fontBonus(f);
      if (!best || score > best.score) best = { score, f };
    }
    const f = best?.f ?? fonts.reduce((a, b) => (b.w * b.h < a.w * a.h ? b : a));
    return { layout: 'console', panel: null, font: f, frame: { w: f.w + chrome.x, h: Math.max(f.h + chrome.minY, Math.min(f.h + chrome.y, room)) } };
  }

  // panel full width (landscape: as high as the viewport), console below, page
  // scrolls: the console's whole width and all its rows fit the window if they can,
  // in Glass TTY if that can; else the terminal scrolls sideways in its frame
  const scrolling = () => {
    const panel = fitPanel(W, W > pageH ? pageH - panelExtraH : Infinity);
    const readable = fonts.filter((f) => f.rowH >= MIN_MOBILE_ROW);
    const fits = readable.filter((f) => f.w + chrome.mx <= W && f.h + chrome.minY <= pageH);
    const glass = fits.filter((f) => f.glass);
    const pool = glass.length ? glass : fits;
    const f = pool.length
      ? pool.reduce((a, b) => (b.rowH + 12 * fontBonus(b) > a.rowH + 12 * fontBonus(a) ? b : a))
      : (readable.length ? readable : fonts).reduce((a, b) => (b.w < a.w ? b : a));
    return { layout: 'mobile', panel, font: f, frame: { w: Math.max(f.w + chrome.mx, panel.w), h: f.h + chrome.minY } };
  };
  // phones and tiny windows
  if (W < 700 || H < 420) return scrolling();

  let best = null;
  const consider = (cand, relate = 0) => {
    const { panel, font: f } = cand;
    const score = 0.55 * Math.log(panel.w * panel.h) + 0.45 * Math.log(f.w * f.h) + fontBonus(f) + relate;
    if (!best || score > best.score) best = { ...cand, score };
  };
  for (const f of fonts) {
    if (f.rowH < MIN_CONSOLE_ROW) continue;
    const fr = { w: f.w + chrome.x, h: f.h + chrome.y };
    // side by side: the frame takes the panel's height
    const side = fitPanel(W - fr.w - GAP, H - chrome.bar - dockExtraH);
    if (side.w >= MIN_PANEL_SIDE && f.h + chrome.minY <= side.h) {
      const margin = (side.h - f.h - chrome.border) / 2;
      let fill = SIDE_BONUS;
      if (margin < TIGHT_MARGIN) fill -= 0.12;
      if (margin > WIDE_MARGIN) fill -= 0.5 * ((margin - WIDE_MARGIN) / side.h);
      fill -= PANEL_SHARE_WEIGHT * Math.max(0, PANEL_SHARE - side.w / W);
      consider({ layout: 'side', panel: side, font: f, frame: { w: fr.w, h: side.h } }, fill);
    }
    if (fr.w > W) continue;
    // stacked: the panel takes what the console leaves; similar widths look composed
    const panel = fitPanel(W, H - fr.h - chrome.bar - GAP - panelExtraH);
    if (panel.w >= MIN_PANEL_STACK) {
      consider({ layout: 'stack', panel, font: f, frame: { w: Math.max(fr.w, panel.w), h: fr.h } }, -0.05 * Math.abs(Math.log(panel.w / fr.w)));
    }
    // a console much wider than the panel: the header moves up beside the panel, so
    // the panel gains its height and all of it lines up with the console's edges
    if (chrome.bar) {
      const beside = fitPanel(fr.w - GAP - MIN_HEAD_BESIDE, H - fr.h - GAP - panelExtraH);
      if (beside.w >= MIN_PANEL_SIDE) consider({ layout: 'wide', panel: beside, font: f, frame: fr });
    }
  }
  if (!best) return scrolling();
  return { layout: best.layout, panel: best.panel, font: best.font, frame: best.frame };
}
