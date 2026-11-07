// The 30 paddles of the front panel in their three poses. Silhouettes and
// colours were measured column by column on the BlinkenBone photo layers of a
// real panel: every paddle is a moulded bow-tie, with a saturated glossy upper
// face, a rounded bend above the crease and a duller lower face; the row
// brightens towards the light on the right. Per-paddle numbers are in
// geometry.json (paddles[].art), written by gen-geometry.mjs.

import { s, f2, outline, poly, line, lookup, colorAt, rampAt, mix, linear, radial, USER } from './art.js';

const TOP = 187.3;
const BOTTOM = 223.75;
const WAIST = 204.3; // where the edges are cut deepest, on a paddle with its crease at 207.5
const SIDE = 0.12; // half the dark gap between neighbours

// Face colours per row for the slots listed; slots in between are interpolated.
const UPPER_Y = [188, 189.5, 191, 194, 197, 200];
const UPPER = {
  rose: [
    [0, 'b91e3d b9213f ba213f bb203f be2140 bc1e3f bf2040 c42342 9e1431 570517 3a1217'],
    [5, 'c42041 c62343 c82444 cb2344 cd2445 cb2346 ca2144 d52849 c01d3e 840d27 441a1f'],
    [12, 'c91e42 d02346 d12648 d52649 d6274a d4264a d32249 df2c4d cb1d42 98162f 3e0f15'],
    [17, 'd22146 da274b dc2e4f df2f50 e33454 e23655 e02b4f eb3958 cb1f42 8d0d28 593034'],
    [23, 'dd3150 e13353 e6435e ec4863 ee4a65 ec4261 ea3a59 ee3557 e03a58 ad1d39 490813'],
    [27, 'e33755 e53253 ee4c67 f1516b f04d69 f1506b f14e6a f14e6d dd3250 ad1939 571e26'],
    [29, 'dd2d4f e52d52 f2546d f65b73 f55571 f65672 f6546f f3395b e8304f c82947 5c0819'],
  ],
  mag: [
    [2, '5c1f44 5f2247 5f2146 622347 602247 5e2246 642448 642849 44132b 17070e 291e22'],
    [9, '62274a 67284d 65254a 67254b 67264b 65254a 622148 702a51 5d1f41 340f21 271b21'],
    [15, '652349 6b284f 6d2b51 6f2d53 702f55 6e2c54 6c2951 793259 612044 330e1f 302429'],
    [21, '733159 75325b 78375f 7a3860 7d3c62 76355d 74325a 742f59 742f59 5b1c3c 1d0912'],
    [24, '712e56 79375f 814369 884a71 884d73 85496f 7e3f66 7a335d 7a335d 5e2343 1f0a14'],
    [28, '702e56 7c3f66 895076 91597e 8f567b 8b5177 85466d 78325b 78325b 6f3355 4b1a35'],
  ],
  blank: [[22, 'e8e9e4 eaebe6 eaebe6 eaebe6 ebece7 ebece7 edeee9 f3f3ef e7e6e1 bab3a6 7b746a']],
};
const LOWER_Y = [206, 210.5, 212, 213.5, 215, 216.5, 218, 219.5, 221, 222.5, 223.75];
const LOWER = {
  rose: [
    [0, '5a3b3d 5c3f41 5d3f41 5a3c3e 573739 533132 472526 3e1a1c 391416 300f10 1a0a0a'],
    [12, '664647 664649 624244 624243 573335 542d2f 4d2527 461d1e 401618 391213 200b0b'],
    [23, '745456 765657 765657 755455 6b4747 5d3436 522627 4c1e21 431519 3e1415 240c0c'],
    [29, '725051 745354 765455 745253 694345 623b3d 5b3133 512326 4b1d20 411417 2a0f0e'],
  ],
  mag: [
    [2, '3c3233 3c3233 3e3335 403637 3d3333 392f2e 312527 2b1f1f 251619 1b0f11 0e0808'],
    [15, '4d4144 4b4044 493d41 483c3f 463739 3c2d30 37272a 2e1f22 26171b 211212 120a0a'],
    [24, '5b5154 5d5255 5b4f51 55494c 4c3c40 403135 352327 2f1c20 29181e 241315 140a0b'],
    [28, '5f5558 5f5558 5d5256 54474a 47383a 3b2b2e 322125 2d1d21 2a1a1d 251417 150b0c'],
  ],
  blank: [[22, '817d74 838077 837f76 838077 817e75 7a766b 726e63 69635a 645e53 5f594f 2f2b25']],
};
// the side walls beside the faces: their width at the waist (they can fill the
// whole cut, and in the camera's view those on the left only start right of the
// row's middle) and their colours
const WALL_WIDTH_R = [[0, 1.9], [1, 1.5], [2, 1.5], [12, 1.7], [14, 1.5], [16, 1.5], [17, 1.1], [18, 0.9], [21, 0.8], [22, 0.5], [23, 0]];
const WALL_WIDTH_L = [[14, 0], [15, 0.3], [16, 0.5], [17, 2.1], [18, 2], [19, 3.4], [21, 3.4], [22, 3.2], [23, 3.8], [29, 4.2]];
const WALL_R = {
  rose: [[0, '#7d0224'], [12, '#6e0921'], [16, '#73061d'], [18, '#510515'], [23, '#3a0410']],
  mag: [[1, '#320f23'], [13, '#280d1e'], [21, '#2a1820']],
  blank: [[22, '#49403c']],
};
const WALL_L = {
  rose: [[16, '#490210'], [18, '#4d000e'], [23, '#57151d'], [25, '#32050b'], [29, '#1e0308']],
  mag: [[14, '#10030a'], [19, '#180205'], [29, '#1b0307']],
  blank: [[22, '#382223']],
};

// ------------------------------------------------------------- raised poses
// The roof's width on either side, over the row: its left end is inset on the
// paddles right of the row's middle, its right end on those left of it. Below the
// roof the body's right edge curves in to `depth` at `knee`, then out again; its
// left edge runs out from the roof to the slot's side and swells beyond it towards
// the lower end (traced at y = 198, 204 and 211).
const RAISED = {
  left: [[0, 0.3], [2, 0.4], [8, 1.2], [12, 1.7], [13, 2.2], [14, 2.8], [17, 3.6], [20, 4.2], [22, 4.6], [25, 3.9]],
  right: [[0, 5.3], [1, 4.9], [2, 3.8], [7, 3.4], [12, 3.2], [13, 2.7], [14, 2.8], [17, 1.9], [20, 1.2], [22, 0.6], [25, 1.3]],
  depth: [[0, 3.6], [1, 3.6], [2, 2.9], [3, 2.8], [4, 2.4], [12, 2.1], [14, 1.7], [17, 1.15], [19, 0.4], [25, 0.4]],
  knee: [[0, 199.5], [2, 198.5], [12, 197.5], [14, 195.5], [19, 193.5], [25, 194]],
  left198: [[0, -0.6], [1, -0.3], [2, 0.4], [3, 0.7], [5, 0.4], [7, 0.9], [8, 1.3], [9, 1.3], [12, 0.7], [14, 1.5], [18, 1.2], [19, 1.8], [20, 2.4], [21, 2], [25, 1.9]],
  left204: [[0, -0.9], [1, -0.7], [2, -0.1], [6, -0.1], [8, 0.6], [12, 0.2], [14, 0.9], [17, 0.4], [19, 1.5], [21, 1.2], [25, 0.7]],
  left211: [[0, -1.3], [1, -1.05], [2, -0.9], [6, -0.6], [8, -0.2], [12, -0.3], [14, 0.2], [17, 0.1], [19, 0.5], [21, 0.4], [25, 0.1]],
  band: [[0, 190.7], [2, 191.7], [5, 189.2], [12, 189], [14, 188.9], [17, 188], [19, 187], [21, 186.9]],
  bandH: [[0, 3.1], [2, 2.5], [5, 2.5], [12, 2.7], [14, 2.1], [19, 1.9], [21, 1.9]],
  bandColor: {
    rose: [[0, '#ee3655'], [5, '#fd3a5c'], [12, '#ff4263'], [18, '#ff4667']],
    mag: [[1, '#843561'], [7, '#8c3b6a'], [13, '#993e72'], [14, '#9d4276'], [21, '#b05b8d']],
  },
  roof: {
    rose: [[0, '#b82642'], [5, '#a5213c'], [12, '#b02441'], [18, '#c02a4a']],
    mag: [[1, '#672a4f'], [7, '#5b2948'], [13, '#5e2f4c'], [14, '#6d3056'], [21, '#a85485']],
  },
  dark: {
    rose: [[196, '#490a13'], [198, '#450a12'], [200, '#420911'], [208, '#3d0811'], [214, '#38080f'], [218, '#33070e'], [220, '#30070d'], [222, '#330f11'], [BOTTOM, '#2c1212']],
    mag: [[196, '#1d0a12'], [198, '#1a0910'], [200, '#17080f'], [208, '#16080e'], [214, '#16080e'], [218, '#15080e'], [220, '#15080e'], [222, '#1c0e10'], [BOTTOM, '#1a0f0f']],
  },
  facet: {
    rose: [[188, '#e22c50'], [191, '#cc1c45'], [194, '#c11b40'], [197, '#b81b3c'], [200, '#550715']],
    mag: [[188, '#74305a'], [190, '#5e1f45'], [194, '#591e41'], [197, '#4c1838'], [201, '#2c0c1a']],
  },
  sideL: {
    rose: [[186, '#5d0314'], [190, '#4f0925'], [196, '#3c0619']],
    mag: [[186, '#4e0b25'], [188, '#2e0a1b'], [190, '#18060e'], [194, '#120408']],
    blank: [[186, '#422722'], [189, '#542835'], [194, '#3d2423'], [198, '#2f1c1a']],
  },
};
const DEP_FACE = [[185.7, '#d83050'], [186.2, '#f13559'], [187, '#f4395c'], [188, '#f64665'], [189, '#d82248'], [190, '#b9173b'], [191, '#881228'], [192, '#7f1825'],
  [194, '#781923'], [196, '#721821'], [198, '#6e1820'], [200, '#68181f'], [203, '#5f171e'], [206, '#55161c'], [209, '#4b1519'], [212, '#461418'], [215, '#441015'],
  [218, '#410b12'], [219.2, '#4c1419'], [220, '#622f32'], [221, '#592528'], [222, '#4a181b'], [BOTTOM, '#3c1415']];
// LAMP TEST's edges below its dark roof side as traced on the photo, x from the slot's left side
const LAMP_LEFT = [[186.7, 4.35], [190, 4.35], [192, 4], [194, 3.66], [196, 3.14], [198, 2.62], [200, 2.28], [202, 1.76], [204, 1.25], [206, 0.9], [208, 0.39], [210.5, 0.21], [215, -0.14], [217.5, -0.32], [220, -0.49], [222, -0.84], [223.75, -1.1]];
const LAMP_RIGHT = [[186.7, 14.9], [194, 14.89], [198, 14.71], [202, 14.87], [207, 15.03], [211, 15.02], [215, 14.67], [219, 14.32], [222, 13.97], [223.75, 13.6]];
const LAMP_FACE = [[186.7, '#cfcdc7'], [187.1, '#f0f1ed'], [188, '#f3f3f0'], [189, '#eeede9'], [190, '#e3e0d9'], [191, '#c2b6a9'], [192, '#a99889'], [194, '#9f8c7c'],
  [196, '#998575'], [198, '#958172'], [200, '#957f70'], [203, '#987f71'], [206, '#9a8476'], [209, '#9e8a7c'], [212, '#9c887a'], [215, '#978476'], [218, '#8b7869'],
  [219, '#827365'], [220, '#887b6f'], [221, '#85796b'], [222, '#6c5f51'], [BOTTOM, '#5a4f40']];

// ------------------------------------------------------------ pressed poses
// The pressed face comes forward into the light: the lit part narrows towards
// its rolled lower end, which ends in a bright streak.
const DOWN_LEFT = [[187.2, 0.12], [190, 0.2], [192, 0.3], [195, 0.45], [198, 0.6], [201, 0.85], [204, 1.2], [207, 1.75], [210, 2.4], [213, 3], [216, 3.5], [218, 3.9], [220, 4.2], [224.1, 4.2]];
const DOWN_LIT = [[187.2, 14], [192, 13.9], [195, 13.1], [198, 12.8], [201, 12.6], [204, 12.5], [207, 12.3], [210, 12.1], [213, 12.1], [216, 12.5], [224.1, 12.5]];
const DOWN_SIDE = [[187.2, 14.25], [192, 14.25], [195, 14.2], [198, 14.2], [204, 14.05], [207, 13.8], [210, 13.4], [213, 12.9], [216, 12.6], [224.1, 12.55]];
const DOWN_FACE = {
  rose: [[187.2, '#a02a3e'], [188, '#d93451'], [190, '#e8405c'], [192, '#ec3b5a'], [195, '#ef3d5c'], [216, '#f03b5c'], [218.3, '#ef3f5e'], [219.8, '#e8455f'],
    [221.1, '#d5314d'], [222, '#bc2740'], [223, '#831e27'], [224.1, '#3a1518']],
  mag: [[187.2, '#54294a'], [188, '#75375a'], [190, '#7e3d62'], [192, '#823f66'], [195, '#8a4d73'], [198, '#8c4e74'], [204, '#884a71'], [207, '#85446b'], [210, '#84436b'],
    [216, '#7f3b64'], [218.3, '#7b3860'], [219.8, '#805a7a'], [221.1, '#6c284c'], [222, '#5f203f'], [223, '#3e131d'], [224.1, '#2a1015']],
  magLit: [[187.2, '#573352'], [188, '#793b5d'], [190, '#834268'], [192, '#87456d'], [195, '#975d83'], [198, '#9a6186'], [204, '#996387'], [207, '#9b6388'], [210, '#945d81'],
    [216, '#8d5377'], [218.3, '#8a4c72'], [219.8, '#8f5e80'], [221.1, '#7a3a5d'], [222, '#6f2c4f'], [223, '#551d39'], [224.1, '#2d1117']],
};

/** The shared paints of the paddles. */
export function paddleDefs(d) {
  d.add(
    // the shade right below the crease
    linear(`${d.id}-creaseshade`, [[0, '#050102', 0.8], [0.4, '#050102', 0.4], [1, '#050102', 0]]),
    s('radialGradient', { id: `${d.id}-gloss` },
      s('stop', { offset: 0, 'stop-color': '#ffffff', 'stop-opacity': 0.04 }),
      s('stop', { offset: 0.55, 'stop-color': '#ffffff', 'stop-opacity': 0.025 }),
      s('stop', { offset: 1, 'stop-color': '#ffffff', 'stop-opacity': 0 })),
  );
}

/**
 * One paddle: a group with its three poses. `reflection` is where the glossy
 * face at rest mirrors the number printed on the tile above it (x in mm).
 */
export function paddle(d, p, reflection) {
  const g = s('g', { class: 'paddle', 'data-slot': p.slot, 'data-name': p.name, 'data-pose': 'rest' });
  const rises = p.type === 'sr' || p.dir === 'up';
  const presses = p.type === 'latch' || p.dir === 'down';
  g.append(
    rest(d, p, reflection),
    rises ? raised(d, p) : s('g', { class: 'pose pose-back' }),
    presses ? pressed(d, p) : s('g', { class: 'pose pose-fwd' }),
  );
  return g;
}

/** The hover and focus highlight of a pose, shaped like its silhouette. */
const highlight = (dd, p) => s('path', { class: 'paddle-hl', d: dd, fill: p.color === 'blank' ? '#4a4030' : '#ffffff' });

// ----------------------------------------------------------------- at rest

// An edge leaves the slot's side higher, and returns to it lower, the deeper it is cut.
const leaves = (inset) => 193.4 - 0.58 * inset;
const returns = (inset) => 217.2 + 0.36 * inset;

function rest(d, p, reflection) {
  const { slot, color, art: a } = p;
  const { x0, x1, insetL: iL, insetR: iR, crease: C, streak: S, gain } = a;
  const W = WAIST + (C - 207.5) * 0.3;
  const xL = x0 + iL; // the waist on either side
  const xR = x1 - iR;
  const xm = (xL + xR) / 2 + 0.3;
  const smile = C + 0.3; // the crease's soft curve across the face
  const creaseEdge = (inset) => inset * 0.985;
  const yL = leaves(iL);
  const yR = leaves(iR);
  const near = (inset) => 202.4 - 0.1 * inset;
  const xRk = (y) => x1 - (iR * (y - yR)) / (W - yR); // straight along the cut, down to its knee
  const xLk = (y) => x0 + (iL * (y - yL)) / (W - yL);

  const sideR = iR > 0.2 ? `L${f2(x1 - SIDE)},${f2(yR)} L${f2(xRk(near(iR)))},${f2(near(iR))} Q${f2(xR)},${f2(W - 0.6)} ${f2(xR)},${f2(W)} L${f2(x1 - creaseEdge(iR))},${f2(C)} ` : `L${f2(x1 - SIDE)},${f2(C)} `;
  const sideL = iL > 0.2 ? `L${f2(x0 + creaseEdge(iL))},${f2(C)} L${f2(xL)},${f2(W)} Q${f2(xL)},${f2(W - 0.6)} ${f2(xLk(near(iL)))},${f2(near(iL))} L${f2(x0 + SIDE)},${f2(yL)} ` : `L${f2(x0 + SIDE)},${f2(C)} `;
  const upperFace = `M${f2(x0 + SIDE + 0.2)},${TOP} L${f2(x1 - SIDE - 0.2)},${TOP} Q${f2(x1 - SIDE)},${TOP} ${f2(x1 - SIDE)},${f2(TOP + 0.2)} ${sideR}` +
    `Q${f2(xm)},${f2(smile)} ${f2(x0 + creaseEdge(iL))},${f2(C)} ${sideL}L${f2(x0 + SIDE)},${f2(TOP + 0.2)} Q${f2(x0 + SIDE)},${TOP} ${f2(x0 + SIDE + 0.2)},${TOP} Z`;
  const endL = returns(iL);
  const endR = returns(iR);
  const lowerFace = `M${f2(x0 + creaseEdge(iL))},${f2(C)} Q${f2(xm)},${f2(smile)} ${f2(x1 - creaseEdge(iR))},${f2(C)} L${f2(x1 - SIDE)},${f2(endR)} L${f2(x1 - SIDE)},${f2(BOTTOM - 0.3)} ` +
    `Q${f2(x1 - SIDE)},${BOTTOM} ${f2(x1 - SIDE - 0.25)},${BOTTOM} L${f2(x0 + SIDE + 0.25)},${BOTTOM} Q${f2(x0 + SIDE)},${BOTTOM} ${f2(x0 + SIDE)},${f2(BOTTOM - 0.3)} L${f2(x0 + SIDE)},${f2(endL)} Z`;

  // Colours of this slot; the rows of the rounded bend hang on the crease and the streak.
  const uc = rampAt(UPPER[color], slot);
  const upperStops = [
    [TOP, mix(uc[0], '#0a0506', 0.45)], [TOP + 0.5, mix(uc[0], '#0a0506', 0.1)],
    ...UPPER_Y.map((y, i) => [y, uc[i]]).filter(([y]) => y < S - 1.3),
    [S - 1.3, uc[6]], [S, uc[7]], [S + 1.5, uc[8]], [C - 1.3, uc[9]], [C, uc[10]],
  ];
  const upper = d.vertical(`up-${p.name}`, upperStops);
  const step = Math.round(slot / 2) * 2;
  const lc = rampAt(LOWER[color], step);
  const lower = d.vertical(`lo-${color}-${step}`, LOWER_Y.map((y, i) => [y, lc[i]]));

  const pose = s('g', { class: 'pose pose-rest' });

  // The side walls beside the faces: blades from the cut's upper end down past the crease.
  const wall = (side, inset, width, colour, name) => {
    if (inset < 0.4 || width < 0.1) return;
    const w = Math.min(width, inset - 0.02);
    const sideX = side > 0 ? x1 : x0;
    const xKnee = sideX - side * inset;
    const yTop = leaves(inset);
    const yEnd = returns(inset);
    const yBlade = C + (w / inset) * (yEnd - C);
    pose.append(s('path', {
      d: poly([[sideX - side * SIDE, yTop], [xKnee, W], [sideX - side * creaseEdge(inset), C], [xKnee + side * w, yBlade], [xKnee + side * w, W - 0.8]]),
      fill: d.vertical(name, [[yTop, mix(colour, '#ffffff', 0.05)], [W, colour], [C + 2, mix(colour, '#000000', 0.3)]]),
    }));
  };
  wall(1, iR, lookup(WALL_WIDTH_R, slot), colorAt(WALL_R[color], slot), `wr-${p.name}`);
  wall(-1, iL, lookup(WALL_WIDTH_L, slot), colorAt(WALL_L[color], slot), `wl-${p.name}`);

  const creaseShade = `M${f2(x0 + creaseEdge(iL))},${f2(C)} Q${f2(xm)},${f2(smile)} ${f2(x1 - creaseEdge(iR))},${f2(C)} L${f2(x1 - creaseEdge(iR) + 0.2)},${f2(C + 0.65)} Q${f2(xm)},${f2(smile + 0.65)} ${f2(x0 + creaseEdge(iL) - 0.2)},${f2(C + 0.65)} Z`;
  pose.append(
    s('path', { d: lowerFace, fill: lower }),
    s('path', { d: upperFace, fill: upper }),
    s('path', { d: creaseShade, fill: d.url('creaseshade') }),
  );

  // the glossy face mirrors the number printed above it, as a soft smudge
  if (reflection != null && color !== 'blank') {
    pose.append(s('ellipse', { cx: f2(reflection), cy: f2(S - 5.5), rx: 2.6, ry: 1.7, fill: d.url('gloss') }));
  }

  // the lit edge of the moulding where the faces turn into the walls
  const rim = color === 'blank' ? '#ffffff' : mix(uc[3], '#ffffff', 0.35);
  const rimAt = (side, inset, lit) => {
    if (inset < 1) return;
    const sideX = side > 0 ? x1 : x0;
    const xKnee = sideX - side * inset;
    const yTop = leaves(inset);
    pose.append(s('path', {
      d: `M${f2(sideX - side * 0.25 * inset)},${f2(yTop + 0.25 * (W - yTop))} L${f2(sideX - side * inset * 0.96)},${f2(near(inset))} Q${f2(xKnee)},${f2(W - 0.6)} ${f2(xKnee)},${f2(W)} L${f2(sideX - side * creaseEdge(inset))},${f2(C)}`,
      fill: 'none', stroke: rim, 'stroke-width': 0.16, opacity: lit,
    }));
  };
  rimAt(1, iR, lookup([[0, 0.45], [17, 0.5], [21, 0.4]], slot));
  rimAt(-1, iL, lookup([[14, 0.2], [22, 0.4], [29, 0.55]], slot));

  // the specular streak on the rounded bend
  if (gain > 0) {
    const xa = x0 + iL * 0.92;
    const xb = x1 - iR * 0.92;
    const m = (xa + xb) / 2;
    pose.append(s('path', {
      d: `M${f2(xa)},${f2(S - 0.2)} Q${f2(m)},${f2(S - 0.42)} ${f2(xb)},${f2(S - 0.2)} L${f2(xb)},${f2(S + 0.3)} Q${f2(m)},${f2(S + 0.16)} ${f2(xa)},${f2(S + 0.3)} Z`,
      fill: color === 'blank' ? '#ffffff' : mix(uc[3], '#ffffff', 0.92), opacity: gain,
    }));
  }
  pose.append(highlight(`${upperFace} ${lowerFace}`, p));
  return pose;
}

// ------------------------------------------------------------------ raised

function raised(d, p) {
  const { slot, color, art: a, name } = p;
  const { x0, x1 } = a;
  const special = name === 'DEP' || name === 'LAMP_TEST';
  const T = name === 'DEP' ? 185.7 : name === 'LAMP_TEST' ? 186.7 : 186.9;
  const lt = lookup(RAISED.left, slot); // the roof's left end
  const rt = lookup(RAISED.right, slot); // its right end
  const depth = lookup(RAISED.depth, slot);
  const band = lookup(RAISED.band, slot);
  const bandEnd = band + lookup(RAISED.bandH, slot);
  const knee = lookup(RAISED.knee, slot);
  const pose = s('g', { class: 'pose pose-back' });

  // top, right edge, bottom, left edge
  const inR = (k) => x1 - Math.max(SIDE, depth * k);
  const swell = lookup(RAISED.left211, slot);
  const face = name === 'LAMP_TEST'
    ? outline([LAMP_RIGHT.map(([y, dx]) => [x0 + dx, y]), LAMP_LEFT.map(([y, dx]) => [x0 + dx, y]).reverse()])
    : outline([
      [[x0 + lt, T], [x1 - SIDE - 0.2, T]],
      [[x1 - SIDE, T + 0.3], [inR(0.3), knee - 7], [inR(0.82), knee - 2.5], [inR(1), knee], [inR(0.82), knee + 5], [inR(0.35), knee + 12.5], [inR(0.18), knee + 19], [x1 - SIDE, BOTTOM - 0.3]],
      [[x1 - SIDE - 0.25, BOTTOM], [x0 + SIDE + 0.3, BOTTOM]],
      [[x0 + SIDE, BOTTOM - 0.3], [x0 + 0.4 * swell, 219], [x0 + swell, 211], [x0 + lookup(RAISED.left204, slot), 204], [x0 + lookup(RAISED.left198, slot), 198], [x0 + Math.max(lt, SIDE), 190]],
      ...(lt > 0.8 ? [] : [[[x0 + Math.max(lt, SIDE), T + 0.3]]]),
    ]);

  // the dark side of the roof on the left
  if (lt > 0.8) {
    pose.append(s('path', {
      d: poly([[x0 + 0.1, T + 0.1], [x0 + lt + 0.1, T], [x0 + lt + 0.1, 190], [x0 + lt * 0.72, 190 + 0.28 * lt / 0.19], [x0 + lt * 0.5, T + 2.8], [x0 + 0.1, T + 0.9]]),
      fill: d.vertical(`us-${color}`, RAISED.sideL[color]),
    }));
  }

  let fill;
  if (name === 'DEP') fill = d.vertical('up-dep', DEP_FACE);
  else if (name === 'LAMP_TEST') fill = d.vertical('up-lamp', LAMP_FACE);
  else {
    const bandC = colorAt(RAISED.bandColor[color], slot);
    const roof = colorAt(RAISED.roof[color], slot);
    const dark = RAISED.dark[color];
    const y0 = dark[0][1];
    const stops = [[T, mix(roof, '#000000', 0.4)], [T + 0.4, roof]];
    if (band > T + 1) stops.push([band - 0.35, roof]);
    stops.push([Math.max(band, T + 0.5), bandC], [bandEnd - 0.7, bandC], [bandEnd + 0.3, mix(bandC, y0, 0.4)], [bandEnd + 1.7, mix(bandC, y0, 0.9)]);
    stops.push([Math.max(bandEnd + 2.7, 197), y0], ...dark.slice(1));
    fill = d.vertical(`ra-${name}`, stops);
  }
  pose.append(s('path', { d: face, fill }));

  // the side wall under the bright band: a wedge between its inner edge and the silhouette
  if (!special && rt > 0.6) {
    const xi = x1 - rt;
    const yTop = Math.min(band, 192);
    const tipY = Math.max(knee + 1, bandEnd + 2.5);
    const inner = [[xi, yTop + 0.1], [xi + 0.1, bandEnd], [xi + rt * 0.26, tipY]];
    pose.append(s('path', {
      d: outline([[[x1 - 0.2, T + 0.6], inner[0]], inner, [[inR(0.9), knee - 1]]]),
      fill: d.vertical(`uf-${color}`, RAISED.facet[color]),
    }));
    // the lit fold between roof and wall, then down the wedge's inner edge, where it fades
    const lit = color === 'rose' ? '#ff6a80' : mix(colorAt(RAISED.bandColor[color], slot), '#ffffff', 0.12);
    const stroke = { fill: 'none', 'stroke-width': 0.18, 'stroke-linecap': 'round' };
    pose.append(
      s('path', { d: line([[x1 - 0.3, T + 0.5], inner[0]]), ...stroke, stroke: lit, opacity: 0.8 }),
      s('path', {
        d: outline([inner], { closed: false }), ...stroke,
        stroke: d.vertical(`ul-${name}`, [[yTop, lit, 0.8], [bandEnd, lit, 0.6], [tipY, lit, 0]]),
      }),
    );
  }
  if (name === 'LAMP_TEST') {
    // the dark neighbour's reflection fades in from nothing towards the lower right, as on the photo
    d.add(radial(`${d.id}-lampshade`, [[0, '#5e4537', 0.9], [0.35, '#5e4537', 0.75], [0.56, '#5e4537', 0.45], [0.83, '#5e4537', 0.1], [1, '#5e4537', 0]],
      { cx: 0, cy: 0, r: 1, units: USER, transform: `translate(${f2(x1 + 2.15)} 216.45) scale(15.3 20.1)` }));
    pose.append(s('path', { d: face, fill: `url(#${d.id}-lampshade)` }));
  }
  pose.append(highlight(face, p));
  return pose;
}

// ----------------------------------------------------------------- pressed

function pressed(d, p) {
  const { slot, color, art: a, name } = p;
  const { x0 } = a;
  const T = 187.3;
  const B = 224.1;
  const ys = [T, 190, 192, 195, 198, 201, 204, 207, 210, 213, 216, 218, 220, 222, B];
  const at = (table, y) => x0 + lookup(table, y);
  const paint = color === 'rose' ? 'rose' : slot >= 26 ? 'magLit' : 'mag';
  const left = ys.slice().reverse().map((y) => [at(DOWN_LEFT, y), y]);
  const lit = outline([ys.map((y) => [at(DOWN_LIT, y), y]), left]);
  const whole = outline([ys.map((y) => [at(DOWN_SIDE, y), y]), left]);
  const mid = outline([ys.map((y) => [(at(DOWN_LIT, y) + at(DOWN_SIDE, y)) / 2, y]), left]);
  const face = DOWN_FACE[paint];
  const pose = s('g', { class: 'pose pose-fwd' });

  // Below the narrowing face the floor of the recess shows, tinted by the paddle
  const floor = color === 'rose' ? '#4a2622' : '#22100f';
  pose.append(s('path', {
    d: outline([[[x0 + 0.5, 204], [x0 + 1.4, 212], [x0 + 3.2, 217.2], [x0 + 4.4, 220]], [[x0 + 4.4, B], [x0 + 1, B]], [[x0 + 0.3, 217]]]),
    fill: d.vertical(`fl-${color}`, [[204, '#120406'], [210, '#2a0a0e'], [217, floor], [222, floor], [B, '#2c1512']]),
  }));
  pose.append(
    s('path', { d: whole, fill: d.vertical(`dn0-${paint}`, face.map(([y, c]) => [y, mix(c, '#000000', 0.56)])) }),
    s('path', { d: mid, fill: d.vertical(`dn1-${paint}`, face.map(([y, c]) => [y, mix(c, '#000000', 0.28)])) }),
    s('path', { d: lit, fill: d.vertical(`dn-${paint}`, face) }),
  );
  // the bright streak on the roll at the lower end, strongest on the left
  const xa = x0 + 4.25;
  const xb = x0 + 12.5;
  const white = color === 'rose' ? '#fff3f5' : '#f7f0f7';
  d.add(linear(`${d.id}-spec-${name}`, [[0, white, 0.95], [0.16, white, 0.6], [0.45, white, 0.3], [1, white, color === 'rose' ? 0.18 : 0.34]],
    { x1: f2(xa), y1: 0, x2: f2(xb), y2: 0, units: USER }));
  pose.append(s('path', {
    d: `M${f2(xa)},219.75 Q${f2(xa + 3)},219.95 ${f2(xb)},220.5 L${f2(xb)},220.95 Q${f2(xa + 3)},220.7 ${f2(xa)},220.55 Z`,
    fill: `url(#${d.id}-spec-${name})`,
  }));
  pose.append(highlight(whole, p));
  return pose;
}
