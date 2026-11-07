// Builds the front-panel SVG from geometry.json (millimetres, r4 frame): the
// moulded bezel with its walls, pockets and shelf, the logo plate, the dark
// panel with the printed sheet on it (boxes, bars, cells, tiles, labels, logo,
// unlit lamp windows), plus the movable parts (paddles in three poses, knobs,
// key) that controls.js animates. Lamps themselves are HTML layers (lamps.js).
//
// All shading is gradients and flat shapes, no filters: colours and shapes were
// sampled from the BlinkenBone photo of a real panel. Its light comes from the
// upper right: everything brightens towards the right of the panel, shadows
// fall to the lower left and the walls on the right side are in shade.

import { s, f2, pts, poly, line, outline, rrect, cornersPath, mix, linear, radial, acrossX, acrossY, Defs, USER } from './art.js';
import { paddle, paddleDefs } from './paddles.js';

export { s };

const BOTTOM = 223.75; // where the paddles end

// The left pocket's diagonal ridge: a point on it and its unit normal, pointing into the pocket.
const ridgeX = 1.4;
const ridgeY = 187.2;
const NX = 0.921;
const NY = 0.389;

/**
 * The moulding's pebbled surface: a tile of small light and dark dots, placed
 * by a fixed pseudo-random sequence, so that every panel looks the same.
 */
function pebbles(id) {
  const size = 24; // mm
  let seed = 0x2f6e2b1;
  const rnd = () => {
    seed = (seed + 0x6d2b79f5) | 0;
    let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
  const dots = (n) => Array.from({ length: n }, () => `M${f2(rnd() * size)},${f2(rnd() * size)}h0`).join('');
  const layer = (stroke, opacity, n, width) => s('path', { d: dots(n), fill: 'none', stroke, opacity, 'stroke-linecap': 'round', 'stroke-width': width });
  return s('pattern', { id: `${id}-pebbles`, width: size, height: size, patternUnits: USER },
    layer('#2c281c', 0.05, 420, 0.34),
    layer('#ffffff', 0.1, 420, 0.38),
    layer('#2c281c', 0.018, 110, 0.75),
    layer('#ffffff', 0.04, 110, 0.8),
  );
}

function fixedDefs(d, geo) {
  const id = d.id;
  d.add(pebbles(id));
  d.add(
    // the cream as photographed
    acrossX(`${id}-face`, [[-10, '#c5c2b5'], [8, '#c8c5b9'], [30, '#cbc9bd'], [120, '#d6d3ca'], [200, '#dedbd3'],
      [300, '#e4e3db'], [400, '#ebeae4'], [440, '#f0efeb'], [474, '#f1f0ec']]),
    acrossX(`${id}-chamfer`, [[-10, '#a0a095'], [30, '#a2a497'], [120, '#b1b2a6'], [200, '#b9bab0'],
      [400, '#d0d3cb'], [440, '#d6dad3'], [474, '#dcdfd8']]),
    acrossX(`${id}-chamfer2`, [[-10, '#bab9ad'], [30, '#b4b4a8'], [120, '#b9baae'], [200, '#c0c2b7'],
      [400, '#dadfd7'], [440, '#dfe2dc'], [474, '#e4e7e1']]),
    acrossY(`${id}-falloff`, [[20, '#000000', 0], [120, '#000000', 0.012], [205, '#000000', 0.03]]),
    // the walls around the panel, top and right, in shade
    acrossY(`${id}-wall`, [[23.3, '#4e443c'], [28, '#4a4137'], [31, '#42392f'], [39, '#403830'], [59, '#362f28'], [97, '#2b231f'],
      [146, '#31221f'], [165, '#3e2b25'], [177, '#49362f'], [187.4, '#544338'], [195, '#62564c'], [204.4, '#6a5d52']]),
    // the top wall lightens into the lit left wall around the panel's corner
    linear(`${id}-wallend`, [[0, 1], [0.115, 0.9], [0.27, 0.8], [0.385, 0.66], [0.46, 0.5], [0.54, 0.3], [0.615, 0.15], [0.77, 0.02], [1, 0]].map(([o, a]) => [o, mix('#4e443c', '#c6c3b6', a)]),
      { x1: -4.58, y1: 25.61, x2: 7.94, y2: 22.12, units: USER }),
    acrossX(`${id}-walledge`, [[5, '#8d867b', 0.3], [14, '#8d867b', 0.75], [400, '#8d867b', 0.75], [470, '#b5b0a5', 0.7]]),
    acrossY(`${id}-leftridge`, [[44, '#f6f4e8', 0], [75, '#f6f4e8', 0.09], [204, '#f6f4e8', 0.09]]),
    // the left pocket's surfaces, in planes that meet in a diagonal ridge: the
    // gradients run across the ridge, from outside in
    linear(`${id}-pocketB`, [[0, '#c2bab0', 0], [0.27, '#c0b9ae', 1], [0.52, '#c2bab0', 1], [0.67, '#cfc9be', 1], [0.74, '#d5d0c5', 1], [0.8, '#d8d4ca', 1], [0.87, '#dcd8cf', 1], [0.95, '#e2dfd8', 1], [1, '#e4e1da', 1]],
      { x1: ridgeX - 14.2 * NX, y1: ridgeY - 14.2 * NY, x2: ridgeX, y2: ridgeY, units: USER }),
    linear(`${id}-pocketC`, [[0, '#e2e0d8'], [0.08, '#dfdcd3'], [0.19, '#d9d6cb'], [0.31, '#d5d2c6'], [0.54, '#d2cfc3'], [1, '#cecbbf']],
      { x1: ridgeX, y1: ridgeY, x2: ridgeX + 13 * NX, y2: ridgeY + 13 * NY, units: USER }),
    radial(`${id}-pocketglow`, [[0, '#f0eee7', 0.42], [0.4, '#f0eee7', 0.26], [1, '#f0eee7', 0]], { cx: 2.5, cy: 188, r: 13, units: USER }),
    acrossX(`${id}-pocketedge`, [[-7, '#c6c2b6', 1], [-6.1, '#c6c2b6', 0]]),
    // the right pocket, lit up to the wall's shadow, whose edge runs diagonally
    linear(`${id}-pocketR`, [[0, '#e9e8e3', 1], [0.25, '#e9e8e3', 0.86], [0.5, '#e9e8e3', 0.48], [0.75, '#e9e8e3', 0.1], [1, '#e9e8e3', 0]],
      { x1: 462.1, y1: 196.5, x2: 465.9, y2: 193.5, units: USER }),
    // the fold above the shelf
    acrossY(`${id}-foldL`, [[204.4, '#cbc8bb'], [205.4, '#c5c1b4'], [206.4, '#b8b3a5'], [207, '#9c9689'], [207.5, '#797367'], [208.1, '#544d43']]),
    acrossY(`${id}-foldR`, [[204.4, '#ecebe6'], [205.2, '#f6f5f2'], [206, '#dedad2'], [206.8, '#cac5bb'], [207.6, '#9d978d'], [208.1, '#726c63']]),
    // the shelf: the same cream in its own shadow, catching the light towards its lower edge
    acrossY(`${id}-shelf`, [[207.8, '#4e473e'], [215, '#4f4840'], [220, '#4e473e'], [225, '#4a4339'], [228, '#463d33'], [230, '#413830'], [235, '#3f352b'], [239.5, '#42382e'],
      [241.5, '#4c4137'], [243, '#5a5046'], [244.5, '#6a6054'], [246, '#7d7468'], [247.5, '#8d857a'], [249, '#9b9488'], [250, '#a59e92']]),
    acrossX(`${id}-shelfacross`, [[0, '#fff2e0', 0], [120, '#fff2e0', 0.012], [300, '#fff2e0', 0.035], [440, '#fff2e0', 0.05]]),
    radial(`${id}-shelfglow`, [[0, '#8c897e', 0.92], [0.45, '#8c897e', 0.62], [0.75, '#8c897e', 0.22], [1, '#8c897e', 0]],
      { cx: 478, cy: 214, r: 34, units: USER, transform: 'translate(478 214) scale(1 0.5) translate(-478 -214)' }),
    linear(`${id}-shelfcorner`, [[0, '#d7d4cb', 0], [0.2, '#d7d4cb', 0.14], [0.45, '#d7d4cb', 0.55], [0.72, '#d7d4cb', 0.88], [1, '#d7d4cb', 1]],
      { x1: 458, y1: 237.1, x2: 462.3, y2: 242, units: USER }),
    // the lettering on the logo plate: faces a little lighter than the cream, shadow edges
    acrossX(`${id}-plateface`, [[15, '#e2e0d7'], [70, '#e4e2da'], [120, '#e8e6de'], [200, '#ece9e2'], [285, '#f0eee8']]),
    acrossX(`${id}-emboss`, [[15, '#4a4a40'], [70, '#55554a']]),
    acrossX(`${id}-embosstag`, [[60, '#6a6a5e'], [285, '#747468']]),
    // the panel: a sheen towards the upper right ...
    radial(`${id}-sheen`, [[0, '#ffffff', 0.16], [0.136, '#ffffff', 0.137], [0.326, '#ffffff', 0.06], [0.48, '#ffffff', 0.047], [0.66, '#ffffff', 0.026], [1, '#ffffff', 0]],
      { cx: 445, cy: 30, r: 310, units: USER, transform: 'translate(445 30) scale(1 0.58) translate(-445 -30)' }),
    radial(`${id}-printsheen`, [[0, '#ffffff', 0.085], [0.136, '#ffffff', 0.075], [0.326, '#ffffff', 0.034], [0.48, '#ffffff', 0.026], [0.66, '#ffffff', 0.014], [1, '#ffffff', 0]],
      { cx: 445, cy: 30, r: 310, units: USER, transform: 'translate(445 30) scale(1 0.58) translate(-445 -30)' }),
    // and along the bottom: broad towards the right of the panel, small in its left corner
    radial(`${id}-sheenlow`, [[0, '#ffffff', 0.09], [0.37, '#ffffff', 0.07], [0.55, '#ffffff', 0.05], [0.72, '#ffffff', 0.03], [1, '#ffffff', 0]],
      { cx: 350, cy: 195, r: 210, units: USER, transform: 'translate(350 195) scale(1 0.27) translate(-350 -195)' }),
    radial(`${id}-sheencorner`, [[0, '#ffffff', 0.055], [0.4, '#ffffff', 0.04], [1, '#ffffff', 0]], { cx: 0, cy: 190, r: 35, units: USER }),
    acrossY(`${id}-topshade`, [[28.7, '#060303', 0.92], [31.3, '#060303', 0.8], [34.4, '#060303', 0.34], [37.5, '#060303', 0]]),
    acrossX(`${id}-rightshade`, [[452, '#060303', 0], [458, '#060303', 0.35], [463.5, '#060303', 0.75]]),
    acrossY(`${id}-lip`, [[40, '#2a2622', 0.26], [170, '#2a2622', 0.26], [181, '#2a2622', 0.14], [187, '#2a2622', 0]]),
    acrossX(`${id}-lipedge`, [[16, '#463638'], [100, '#3f3135'], [200, '#6d5b5c'], [350, '#776668'], [440, '#7d6a6a'], [448, '#3a2e32']]),
    // the printed colours, lit from the right
    acrossX(`${id}-tile-rose`, [[18, '#7a273b'], [90, '#7e2439'], [175, '#872b40'], [270, '#8e2e45'], [350, '#913049'], [440, '#96344c']]),
    acrossX(`${id}-tile-mag`, [[38, '#4a2841'], [125, '#512e4a'], [210, '#593551'], [297, '#5e3855'], [397, '#633d5a'], [440, '#603a57']]),
    acrossX(`${id}-bar-rose`, [[20, '#762438'], [100, '#7a263b'], [182, '#81283d'], [270, '#892d43']]),
    acrossX(`${id}-bar-mag`, [[50, '#4c2c44'], [139, '#51304a'], [225, '#57344f'], [320, '#5c3954']]),
    // knob: black, glossy, lit from the upper right
    radial(`${id}-knobskirt`, [[0, '#060607'], [0.62, '#060606'], [1, '#08080a']], { cx: 0.6, cy: 0.38, r: 0.7 }),
    linear(`${id}-knobsheen`, [[0, '#ffffff', 0.2], [0.12, '#ffffff', 0.55], [0.3, '#ffffff', 0.3], [0.55, '#ffffff', 0.06], [0.75, '#ffffff', 0]], { x1: 0.82, y1: 0.117, x2: 0.5, y2: 0.5 }),
    radial(`${id}-knobshadow`, [[0, '#050403', 0.88], [0.7, '#050403', 0.84], [0.86, '#050403', 0.42], [1, '#050403', 0]]),
    linear(`${id}-knobbar`, [[0, '#141416'], [0.3, '#121213'], [1, '#101011']], { x1: 0, y1: 0, x2: 1, y2: 0 }),
    linear(`${id}-flutes`, [[0, '#ffffff', 0.028], [0.55, '#ffffff', 0.008], [1, '#ffffff', 0]], { x1: 1, y1: 0, x2: 0, y2: 1 }),
    // key: dull worn metal in a black escutcheon
    radial(`${id}-cylinder`, [[0, '#352c2b'], [0.7, '#332a29'], [0.92, '#3c3331'], [1, '#2a2322']], { cx: 0.5, cy: 0.5, r: 0.5 }),
    linear(`${id}-cylinderrim`, [[0, '#4b403a'], [0.45, '#3f3634'], [0.8, '#5f534f'], [1, '#7a6e69']], { x1: 0, y1: 0.6, x2: 1, y2: 0.35 }),
    radial(`${id}-keyring`, [[0, '#0c0c0d'], [0.86, '#0d0d0e'], [0.915, '#040404'], [0.955, '#19181a'], [1, '#100f11']]),
    acrossY(`${id}-keyblade`, [[-12, '#7f7b77'], [-6, '#6c6866'], [-4, '#4a4542'], [9.5, '#4a4542']]),
    // the flat side of the key at OFF (its short end up to the right) and at LOCK (its long end there)
    acrossY(`${id}-keyoff`, [[-10.2, '#6d615e'], [-8, '#514744'], [-2, '#5a504d'], [2, '#6e6260'], [4.2, '#8d7e7c'], [5.2, '#cfc9cd'], [8.4, '#e4e2e6']]),
    acrossY(`${id}-keylock`, [[-10.2, '#e4e2e6'], [-7, '#cfc9cd'], [-6, '#8d7e7c'], [-3.8, '#6e6260'], [2, '#5a504d'], [6.4, '#514744'], [8.4, '#6d615e']]),
  );
  paddleDefs(d);
}

// ---------------------------------------------------------------- bezel

function bezel(geo, d) {
  const f = geo.frame;
  const b = f.bezel;
  const w = f.wall;
  const le = f.ledge;
  const bay = f.bay;
  const { x0, y0, x1, y1, r } = b;
  const tx = x1 - b.taper.dx; // the shelf's right edge
  const rightEdge = (y) => (y <= b.taper.y0 ? x1 : y >= b.taper.y1 ? tx : x1 - (b.taper.dx * (y - b.taper.y0)) / (b.taper.y1 - b.taper.y0));
  const silhouette = `M${f2(x0 + r)},${y0} L${f2(x1 - r)},${y0} Q${x1},${y0} ${x1},${f2(y0 + r)} L${x1},${b.taper.y0} ` +
    `L${f2(tx)},${b.taper.y1} L${f2(tx)},${f2(y1 - r)} Q${f2(tx)},${y1} ${f2(tx - r)},${y1} ` +
    `L${f2(x0 + r)},${y1} Q${x0},${y1} ${x0},${f2(y1 - r)} L${x0},${f2(y0 + r)} Q${x0},${y0} ${f2(x0 + r)},${y0} Z`;
  const g = s('g', { class: 'bezel' });

  // the face, its rounded outer edge and the top edge's chamfer with its mitred ends
  const ch = f.chamfer.y;
  g.append(
    s('path', { d: silhouette, fill: d.url('face'), stroke: '#6d695d', 'stroke-width': 0.7, 'stroke-opacity': 0.75 }),
    s('path', { d: silhouette, fill: d.url('falloff') }),
    s('path', { d: poly([[x0 + 0.3, y0], [x1 - 0.3, y0], [x1, y0 + 0.3], [x1 - 1.1, ch - 0.3], [x0 + 1.5, ch - 0.3], [x0, y0 + 0.3]]), fill: d.url('chamfer') }),
    s('path', { d: poly([[x0 + 1.5, ch - 0.3], [x1 - 1.1, ch - 0.3], [x1 - 1.3, ch + 0.5], [x0 + 1.7, ch + 0.5]]), fill: d.url('chamfer2') }),
  );

  // the left wall: a soft ridge where it folds out of the face, running down
  // into the pocket, and the planes of the pocket below it
  const ridge = outline([[[-5, 44], [-5, 60], [-5.3, 100], [-6, 140], [-6.2, 180], [-6.15, 204.4]]], { closed: false });
  const yFold = le.y0 + 0.1;
  const ridgeBot = [-5.9, yFold];
  const ridgeTop = [ridgeX, ridgeY];
  g.append(
    s('path', { d: poly([[-6.5, 150], [ridgeX + 2, 150], ridgeTop, ridgeBot, [-6.5, yFold]]), fill: d.url('pocketB') }),
    s('path', { d: poly([ridgeTop, [ridgeX + 2, 165], [18, 165], [18, yFold], ridgeBot]), fill: d.url('pocketC') }),
    s('path', { d: poly([[-4, 178], [16, 178], [16, 201], [-4, 201]]), fill: d.url('pocketglow') }),
    rrect(-7, 165, -6.1, yFold, 0, { fill: d.url('pocketedge') }),
    ...[2, 1.3, 0.7].map((sw) => s('path', { d: ridge, fill: 'none', stroke: d.url('leftridge'), 'stroke-width': sw })),
  );

  // the top and right walls in shade, the corner between them, the lit right pocket over the wall's end
  const arch = [[-3.6, 38], [-3.45, 33], [-2.7, 29.5], [-1, 26], [1.4, 23.9], [5, 22.95]]; // around the panel's corner
  const wallEnd = outline([arch, [[9, 23.03]], [[9, 40]], [[-3.6, 40]]]);
  const wallShape = outline([[[8, 23.02], ...w.top.slice(1), ...w.right], [[w.right.at(-1)[0], yFold]], [[452, yFold]], [[452, 40]], [[8, 40]]]);
  const edgeLine = outline([[...w.top.map(([x, y]) => [x, y - 0.12]), ...w.right.map(([x, y]) => [x + 0.08, y])]], { closed: false });
  g.append(
    s('path', { d: edgeLine, fill: 'none', stroke: d.url('walledge'), 'stroke-width': 0.32 }),
    s('path', { d: wallShape, fill: d.url('wall') }),
    s('path', { d: poly([[448, 187], [w.right.at(-1)[0] + 0.1, 187], [w.right.at(-1)[0] + 0.1, yFold], [453, yFold]]), fill: d.url('pocketR') }),
    s('path', { d: wallEnd, fill: d.url('wallend') }),
  );

  // the shelf
  const shelfTop = le.y1;
  const shelf = `M${x0},${f2(shelfTop)} L${f2(rightEdge(shelfTop))},${f2(shelfTop)} L${f2(tx)},${b.taper.y1} L${f2(tx)},${f2(y1 - r)} ` +
    `Q${f2(tx)},${y1} ${f2(tx - r)},${y1} L${f2(x0 + r)},${y1} Q${x0},${y1} ${x0},${f2(y1 - r)} Z`;
  g.append(
    s('path', { d: shelf, fill: d.url('shelf') }),
    s('path', { d: shelf, fill: d.url('shelfacross') }),
    s('path', { d: shelf, fill: d.url('shelfglow') }),
    s('path', { d: shelf, fill: d.url('shelfcorner') }),
  );

  // the fold between pockets and shelf
  g.append(
    rrect(x0, le.y0, 10.25, le.y1, 0, { fill: d.url('foldL') }),
    s('path', { d: line([[10.05, 203.4], [10.05, le.y1]]), fill: 'none', stroke: '#e8e5da', 'stroke-width': 0.26, opacity: 0.55 }),
    s('path', { d: poly([[453.2, le.y0], [rightEdge(le.y0), le.y0], [rightEdge(le.y1), le.y1], [453.2, le.y1]]), fill: d.url('foldR') }),
  );

  // the pebbled surface of everything so far: fine on the lit cream, barely visible in the shade
  const wallTop = w.top[0][1];
  const lit = `M${x0},${y0} L${x1},${y0} L${x1},${wallTop} L${x0},${wallTop} Z M${x0},${wallTop} L0,${wallTop} L0,${yFold} L${x0},${yFold} Z ` +
    `M450,187.3 L${x1},187.3 L${x1},${yFold} L450,${yFold} Z M0,187.3 L16,187.3 L16,${yFold} L0,${yFold} Z`;
  g.append(
    s('path', { d: lit, fill: d.url('pebbles') }),
    s('path', { d: shelf, fill: d.url('pebbles'), opacity: 0.28 }),
    s('path', { d: wallShape, fill: d.url('pebbles'), opacity: 0.2 }),
    s('path', { d: wallEnd, fill: d.url('pebbles'), opacity: 0.2 }),
  );

  // The recess the paddles stand in: dark, with a cheek on each side where its
  // wall meets the slope of the pocket.
  const cheekTop = 187.4;
  g.append(
    // the left cheek: a lit slope, its dark wall, the black recess
    s('path', { d: poly([[15.3, cheekTop], [12, 210.5], [12.6, 212], [15.2, 218.5], [16.3, BOTTOM + 0.3], [19, BOTTOM + 0.3], [19, cheekTop]]), fill: '#0d0605' }),
    s('path', { d: outline([[[15.25, 188], [10.35, 204.2]], [[10.25, le.y1 + 0.2], [10.7, 209], [11.55, 211.1]], [[12.05, 210], [12.7, 204.3], [15.35, 190]]]),
      fill: d.vertical('cheekL', [[188.4, '#c9b6ae'], [192, '#c2aea5'], [196, '#b8a89f'], [200, '#b7aaa2'], [204, '#bdb2aa'], [208, '#c3bcb2'], [211, '#bfb6ab']]) }),
    s('path', { d: outline([[[14.75, 194.2], [12.7, 204.3], [12.05, 210], [11.75, 211.2]], [[15.3, 219.5]], [[14.8, 214], [14.2, 208], [14.3, 203], [14.95, 197]]]),
      fill: d.vertical('cheekL2', [[194, '#201312'], [200, '#221412'], [206, '#261615'], [208, '#34221e'], [212, '#3c2d27'], [219.5, '#2a1f1b']]) }),
    // the right cheek: a triangle reflecting the last paddle
    s('path', { d: poly([[445, 186], [448.5, 187.2], [453.4, le.y0 + 2], [448.45, 218.4], [447, BOTTOM + 0.3], [445, BOTTOM + 0.3]]), fill: '#140807' }),
    s('path', { d: outline([[[448.5, 190.5], [450.05, 197.2], [451.6, 200.8], [453.3, 206.5]], [[452, 212], [449.8, 215.6], [448.6, 217.8]]]),
      fill: d.vertical('cheekR', [[190, '#5a2224'], [203, '#5e2628'], [207, '#592b2a'], [209, '#573331'], [212, '#59403c'], [216, '#4c4340']]) }),
    s('path', { d: poly([[bay.x0, 184], [bay.x1, 184], [bay.x1, 216], [447, BOTTOM + 0.3], [bay.x0 + 1.2, BOTTOM + 0.3]]), fill: '#0b0204' }),
    // the row's contact shadow on the shelf
    s('path', { d: outline([[[bay.x0 + 0.8, BOTTOM + 0.2], [446.6, BOTTOM + 0.2]], [[446.2, BOTTOM + 2.4], [bay.x0 + 1.2, BOTTOM + 2.4]]]),
      fill: d.vertical('contact', [[BOTTOM + 0.2, '#060202', 0.9], [BOTTOM + 0.7, '#060202', 0.5], [BOTTOM + 1.2, '#060202', 0.24], [BOTTOM + 1.8, '#060202', 0.08], [BOTTOM + 2.4, '#060202', 0]]) }),
  );
  return g;
}

/**
 * Logo plate: an edge pressed into the moulding, strong on the sides away from
 * the light, a dark line with a light one beside it; faint towards the light.
 */
function plate(geo) {
  const { x0, y0, x1, y1, r } = geo.frame.strip;
  const shade = `M${f2(x0 + r * 0.3)},${f2(y0 + r * 0.3)} A${r},${r} 0 0 0 ${x0},${f2(y0 + r)} L${x0},${f2(y1 - r)} A${r},${r} 0 0 0 ${f2(x0 + r)},${y1} ` +
    `L${f2(x1 - r)},${y1} A${r},${r} 0 0 0 ${f2(x1 - r * 0.3)},${f2(y1 - r * 0.3)}`;
  const lit = `M${f2(x0 + r * 0.3)},${f2(y0 + r * 0.3)} A${r},${r} 0 0 1 ${f2(x0 + r)},${y0} L${f2(x1 - r)},${y0} A${r},${r} 0 0 1 ${x1},${f2(y0 + r)} L${x1},${f2(y1 - r)} A${r},${r} 0 0 1 ${f2(x1 - r * 0.3)},${f2(y1 - r * 0.3)}`;
  const seg = (dd, color, width, opacity, tx = 0, ty = 0) => s('path', {
    d: dd, fill: 'none', stroke: color, 'stroke-width': width, 'stroke-linecap': 'round', opacity,
    transform: tx || ty ? `translate(${tx},${ty})` : null,
  });
  return s('g', { class: 'plate', 'aria-hidden': 'true' },
    seg(lit, '#f2f0e8', 0.26, 0.55, -0.1, 0.34),
    seg(shade, '#f4f2ea', 0.3, 0.62, -0.68, 0.36),
    seg(lit, '#8f8d80', 0.2, 0.42),
    seg(shade, '#3d3c33', 0.36, 0.88),
  );
}

function branding(geo, d) {
  const a = geo.art;
  const g = s('g', { class: 'branding', 'aria-hidden': 'true' });
  // Raised, tone on tone: a dark line on the side away from the light, the face
  // lighter than the cream around it.
  const shadow = d.url('emboss');
  const face = d.url('plateface');
  const { tiles: t, letters } = a.digital;
  letters.forEach((l, i) => {
    const x = t.x0 + i * t.pitch;
    const tf = `translate(${f2(x)},0)`;
    g.append(
      // the tile: light along its top and right, dark along its left and bottom
      s('rect', { x: f2(x + t.pitch - 0.72), y: f2(t.y0 + 0.1), width: 0.6, height: f2(t.y1 - t.y0 - 0.1), fill: face }),
      s('path', { d: `M${f2(x + 0.2)},${f2(t.y0)} L${f2(x + t.pitch - 0.12)},${f2(t.y0)}`, fill: 'none', stroke: face, 'stroke-width': 0.22 }),
      s('path', { d: `M${f2(x)},${f2(t.y0)} L${f2(x)},${f2(t.y1)} L${f2(x + t.pitch - 0.12)},${f2(t.y1)}`, fill: 'none', stroke: shadow, 'stroke-width': 0.3 }),
      // the letter: a stroke drawn twice
      s('path', { d: l, transform: `translate(${f2(x - 0.26)},0.22)`, fill: 'none', stroke: shadow, 'stroke-width': t.stroke }),
      s('path', { d: l, transform: tf, fill: 'none', stroke: face, 'stroke-width': t.stroke }),
    );
  });
  // the tagline's letters are shallower than the logo's: their shadow edge is paler
  const tg = a.tagline;
  const tagShadow = d.url('embosstag');
  const tf = `translate(0,${tg.ty}) scale(1,${tg.sy})`;
  const grown = { 'stroke-width': tg.stroke, 'stroke-linejoin': 'round' };
  g.append(
    s('path', { d: tg.d, transform: `translate(-0.3,${f2(tg.ty + 0.24)}) scale(1,${tg.sy})`, fill: tagShadow, stroke: tagShadow, ...grown }),
    s('path', { d: tg.d, transform: tf, fill: face, stroke: face, ...grown }),
  );
  return g;
}

// ---------------------------------------------------------------- the panel

function panelOutline(geo) {
  const w = geo.frame.window;
  const b = geo.frame.bay;
  return outline([
    [...w.left.slice(-3), ...w.top, ...w.right],
    [[b.x1, 187.3]], [[b.x1, b.y0R]], [[b.x0, b.y0L]], [[b.x0, 187.5]],
    [...w.left.slice(0, -2)],
  ]);
}

/** Light and shade over the panel and whatever is printed dark on it. */
function panelLight(geo, d, body) {
  const b = geo.frame.bay;
  const lip = outline([geo.frame.window.left.filter(([, y]) => y >= 40).reverse()], { closed: false });
  return s('g', { class: 'panel-light' },
    s('path', { d: body, fill: d.url('sheen') }),
    s('path', { d: body, fill: d.url('sheenlow') }),
    s('path', { d: body, fill: d.url('sheencorner') }),
    s('path', { d: body, fill: d.url('topshade') }),
    s('path', { d: body, fill: d.url('rightshade') }),
    // the panel's lower edge catches the light above the paddles
    s('path', { d: line([[b.x0 + 0.4, b.y0L - 0.1], [b.x1 - 0.3, b.y0R - 0.1]]), fill: 'none', stroke: d.url('lipedge'), 'stroke-width': 0.42 }),
    // the soft shadow lip where the panel is set into the lit left wall, fading around its lower corner
    ...[1.1, 0.7, 0.35].map((width) => s('path', { d: lip, fill: 'none', stroke: d.url('lip'), 'stroke-width': width })),
  );
}

/**
 * Black lamp cells as one path: fused at the top (rounded top corners for
 * the status boxes), separated by ~0.95 mm gaps with rounded upper ends from
 * `sep` down, rounded bottom corners per cell (the scalloped bottom edge).
 * The gaps are real holes, so the window shows through them.
 */
function cellPath(c, { rt = 0, top = c.y0 } = {}) {
  const gap = 0.95;
  const n = c.cols.length;
  const rb = c.r;
  const ys = c.sep + gap / 2;
  const cells = [];
  for (let i = 0; i < n; i++) {
    const l = i === 0 ? c.x0 : (c.cols[i - 1] + c.cols[i]) / 2 + gap / 2;
    const r = i === n - 1 ? c.x1 : (c.cols[i] + c.cols[i + 1]) / 2 - gap / 2;
    cells.push([l, r]);
  }
  const { x0, x1, y1 } = c;
  const p = [`M${f2(x0)},${f2(top + rt)}`];
  if (rt) p.push(`A${rt},${rt} 0 0 1 ${f2(x0 + rt)},${f2(top)}`);
  p.push(`L${f2(x1 - rt)},${f2(top)}`);
  if (rt) p.push(`A${rt},${rt} 0 0 1 ${f2(x1)},${f2(top + rt)}`);
  for (let i = n - 1; i >= 0; i--) {
    const [l, r] = cells[i];
    if (i < n - 1) p.push(`L${f2(r)},${f2(ys)}`);
    p.push(`L${f2(r)},${f2(y1 - rb)}`, `A${rb},${rb} 0 0 1 ${f2(r - rb)},${f2(y1)}`,
      `L${f2(l + rb)},${f2(y1)}`, `A${rb},${rb} 0 0 1 ${f2(l)},${f2(y1 - rb)}`);
    if (i > 0) p.push(`L${f2(l)},${f2(ys)}`, `A${gap / 2},${gap / 2} 0 0 0 ${f2(cells[i - 1][1])},${f2(ys)}`);
  }
  p.push('Z');
  return p.join(' ');
}

/** Colour bar with optional rounded top corners and the DATA notches. */
function barPath(b, notch) {
  const { x0, x1, y0, y1, rl, rr } = b;
  const p = [`M${f2(x0)},${f2(y1)}`, `L${f2(x0)},${f2(y0 + rl)}`];
  if (rl) p.push(`A${rl},${rl} 0 0 1 ${f2(x0 + rl)},${f2(y0)}`);
  p.push(`L${f2(x1 - rr)},${f2(y0)}`);
  if (rr) p.push(`A${rr},${rr} 0 0 1 ${f2(x1)},${f2(y0 + rr)}`);
  p.push(`L${f2(x1)},${f2(y1)}`);
  // black tabs rising into the bar from its bottom edge: wide concave flares at
  // the base, straight flanks, a flat top with barely rounded corners
  const ht = notch.top / 2;
  const hb = notch.base / 2;
  const yt = y1 - notch.h;
  const c = 0.14;
  for (const nx of [...b.notches].sort((a, e) => e - a)) {
    p.push(
      `L${f2(nx + hb)},${f2(y1)}`,
      `C${f2(nx + hb - 1.5)},${f2(y1)} ${f2(nx + ht)},${f2(y1 - 1)} ${f2(nx + ht)},${f2(y1 - 2.45)}`,
      `L${f2(nx + ht)},${f2(yt + c)}`,
      `Q${f2(nx + ht)},${f2(yt)} ${f2(nx + ht - c)},${f2(yt)}`,
      `L${f2(nx - ht + c)},${f2(yt)}`,
      `Q${f2(nx - ht)},${f2(yt)} ${f2(nx - ht)},${f2(yt + c)}`,
      `L${f2(nx - ht)},${f2(y1 - 2.45)}`,
      `C${f2(nx - ht)},${f2(y1 - 1)} ${f2(nx - hb + 1.5)},${f2(y1)} ${f2(nx - hb)},${f2(y1)}`,
    );
  }
  p.push('Z');
  return p.join(' ');
}

/** The black print: on matt black the panel's sheen is half as strong as on the panel. */
function boxesAndCells(geo, d) {
  const c = geo.colors;
  const shapes = [];
  for (const b of geo.boxes) {
    if (b.id === 'parity') shapes.push(s('path', { d: cellPath(geo.parityCell, { rt: b.r }) }));
    else if (b.corners) shapes.push(s('path', { d: cornersPath(b.x0, b.y0, b.x1, b.y1, b.corners) }));
    else shapes.push(rrect(b.x0, b.y0, b.x1, b.y1, b.r));
  }
  // cells under the bars start inside the bar (no seam; the black shows in the notches)
  for (const cc of geo.cells) shapes.push(s('path', { d: cellPath(cc, { top: cc.y0 - 4.3 }) }));
  const g = s('g', { class: 'boxes', fill: c.box }, shapes);
  const lit = s('g', { class: 'boxes-sheen', fill: d.url('printsheen') }, shapes.map((e) => e.cloneNode()));
  return [g, lit];
}

/** The windows printed on the sheet, with the same half sheen as the black print. */
function lampWindows(geo, d) {
  const shapes = geo.lamps.map((l) => rrect(l.cx - l.w / 2, l.cy - l.h / 2, l.cx + l.w / 2, l.cy + l.h / 2, 1.5));
  return [
    s('g', { class: 'lamp-windows', fill: geo.colors.window_unlit }, shapes),
    s('g', { fill: d.url('printsheen') }, shapes.map((e) => e.cloneNode())),
  ];
}

function bars(geo, d) {
  const g = s('g', { class: 'bars' });
  for (const b of geo.bars) g.append(s('path', { d: barPath(b, geo.notch), fill: d.url(`bar-${b.color}`) }));
  return g;
}

function tiles(geo, d) {
  const g = s('g', { class: 'tiles' });
  for (const t of geo.tiles) g.append(s('path', { d: cornersPath(t.x0, t.y0, t.x1, t.y1, t.corners), fill: d.url(`tile-${t.color}`) }));
  return g;
}

/** Labels up to this cap height (mm) are enlarged on small panels: see panel.css. */
const SMALL_CAP = 2.8;

/** Is this label printed on a colour tile? */
const onTile = (l) => l.tile || (l.y > 165 && l.x > 340);

/** The point a small label grows from, and how far it may grow sideways before it meets its neighbours. */
function growth(l) {
  const w = l.textLength ?? 3;
  // left-aligned beside the lamps of the knob boxes, up to the next lamp or the knob's shadow
  if (l.x > 345 && l.y < 150) return { x: l.x, xmax: f2(Math.max(1, (l.x < 385 ? 20.5 : 19.7) / w)) };
  if (onTile(l)) return { x: l.x, xmax: f2(11.9 / w) }; // stays on its tile
  return { x: l.x, xmax: w > 15 ? 1.1 : f2(Math.max(1.12, 13.1 / w)) }; // above a lamp, one column wide
}

function labelText(geo, l, { live = false } = {}) {
  const lf = { capHeight: 0.686, weight: 600, tileWeight: 500, ...geo.labelFont };
  const size = l.cap / lf.capHeight;
  const small = live && !l.tile && l.cap <= SMALL_CAP;
  const g = small ? growth(l) : null;
  const t = s('text', {
    x: l.x, y: l.y, 'font-size': f2(size), 'text-anchor': l.anchor,
    'font-weight': l.tile ? lf.tileWeight : null,
    'word-spacing': l.wordSpacing ? f2(l.wordSpacing * size) : null,
    textLength: l.textLength ?? null, lengthAdjust: l.textLength ? 'spacingAndGlyphs' : null,
    class: small ? ['small', l.upper && 'upper', l.upper && l.width > 15 && 'wide', /^\d+$/.test(l.text) && 'digits'].filter(Boolean).join(' ') : null,
    style: small ? `transform-origin:${g.x}px ${l.y}px;--xmax:${g.xmax}` : null,
  });
  if (l.text.startsWith('μ')) {
    // the panel's µ is taller than the letters beside it, and slanted
    t.append(s('tspan', { 'font-size': f2(size * 1.25), 'font-style': 'italic' }, 'μ'), l.text.slice(1));
  } else {
    t.textContent = l.text;
  }
  return t;
}

function labels(geo) {
  // The sheet is thick: white print on the colour tiles throws a faint shadow onto the backing.
  const ghost = s('g', { class: 'label-shadows', fill: '#14060f', opacity: 0.2, transform: 'translate(-0.25,0.8)', 'aria-hidden': 'true' },
    geo.labels.filter(onTile).map((l) => labelText(geo, l)));
  const g = s('g', { class: 'labels', fill: geo.colors.ink, color: geo.colors.ink, 'aria-hidden': 'true' }, geo.labels.map((l) => labelText(geo, l, { live: true })));
  return [ghost, g];
}

/** pdp11/70 badge: outlined "pdp" and "70" (union outline via a mask), solid "11", thin "/". */
function logo(geo, id) {
  const L = geo.logo;
  const ink = L.ink;
  const t = L.t;
  const tr = L.transform;
  const mask = s('mask', { id: `${id}-logo-mask`, maskUnits: 'userSpaceOnUse', x: 0, y: 0, width: 800, height: 300 });
  const grow = s('g', { fill: 'white', stroke: 'white' });
  const shrink = s('g', { fill: 'black', stroke: 'black' });
  for (const r of L.rings) {
    const mid = (r.R + r.r) / 2;
    grow.append(s('circle', { cx: r.cx, cy: r.cy, r: mid, fill: 'none', 'stroke-width': r.R - r.r + t }));
    shrink.append(s('circle', { cx: r.cx, cy: r.cy, r: mid, fill: 'none', 'stroke-width': r.R - r.r - t }));
  }
  for (const st of L.stems) {
    grow.append(s('rect', { x: st.x0 - t / 2, y: st.y0 - t / 2, width: st.x1 - st.x0 + t, height: st.y1 - st.y0 + t, stroke: 'none' }));
    shrink.append(s('rect', { x: st.x0 + t / 2, y: st.y0 + t / 2, width: st.x1 - st.x0 - t, height: st.y1 - st.y0 - t, stroke: 'none' }));
  }
  grow.append(s('polygon', { points: pts(L.seven), 'stroke-width': t, 'stroke-linejoin': 'miter' }));
  shrink.append(s('polygon', { points: pts(insetPolygon(L.seven, t / 2)), stroke: 'none' }));
  mask.append(grow, shrink);
  return s('g', { class: 'logo', transform: `translate(${tr.tx},${tr.ty}) scale(${tr.scale})`, 'aria-hidden': 'true' },
    s('defs', {}, mask),
    s('rect', { x: 0, y: 0, width: 800, height: 300, fill: ink, mask: `url(#${id}-logo-mask)` }),
    ...L.ones.map((o) => s('path', { d: o.d, fill: ink })),
    s('line', { x1: L.slash.x0, y1: L.slash.y0, x2: L.slash.x1, y2: L.slash.y1, stroke: ink, 'stroke-width': t }),
  );
}

/** Offsets a simple polygon's edges inwards by d (miter joins). */
export function insetPolygon(list, d) {
  const n = list.length;
  let area = 0;
  for (let i = 0; i < n; i++) {
    const [x1, y1] = list[i];
    const [x2, y2] = list[(i + 1) % n];
    area += x1 * y2 - x2 * y1;
  }
  const sign = area > 0 ? 1 : -1;
  const lines = [];
  for (let i = 0; i < n; i++) {
    const [x1, y1] = list[i];
    const [x2, y2] = list[(i + 1) % n];
    const len = Math.hypot(x2 - x1, y2 - y1);
    const nx = (sign * -(y2 - y1)) / len;
    const ny = (sign * (x2 - x1)) / len;
    lines.push([x1 + nx * d, y1 + ny * d, x2 + nx * d, y2 + ny * d]);
  }
  const out = [];
  for (let i = 0; i < n; i++) {
    const a = lines[(i + n - 1) % n];
    const b = lines[i];
    const den = (a[0] - a[2]) * (b[1] - b[3]) - (a[1] - a[3]) * (b[0] - b[2]);
    if (Math.abs(den) < 1e-9) {
      out.push([b[0], b[1]]);
      continue;
    }
    const ta = ((a[0] - b[0]) * (b[1] - b[3]) - (a[1] - b[1]) * (b[0] - b[2])) / den;
    out.push([a[0] + ta * (a[2] - a[0]), a[1] + ta * (a[3] - a[1])]);
  }
  return out;
}

// ---------------------------------------------------------------- key and knobs

function keyStatic(geo, d) {
  const k = geo.key;
  return s('g', { class: 'key-static', transform: `translate(${k.cx},${k.cy})` },
    // the black escutcheon with its raised ring, the small hole for the lock pin
    s('circle', { r: 10.3, fill: d.url('keyring') }),
    s('circle', { cx: 4.4, cy: 4.7, r: 0.42, fill: '#000000' }),
    // the worn cylinder, its rim catching the light on the right
    s('circle', { r: 5.1, fill: '#080707' }),
    s('circle', { r: 4.8, fill: d.url('cylinder') }),
    s('circle', { r: 4.45, fill: 'none', stroke: d.url('cylinderrim'), 'stroke-width': 0.62 }),
    s('path', { d: 'M3.3,-3.05 A4.5,4.5 0 0 1 4.3,-1.3', fill: 'none', stroke: '#c0bbb9', 'stroke-width': 0.4, 'stroke-linecap': 'round' }),
  );
}

/**
 * The flat key, rotated by controls.js about the cylinder's centre; the plug
 * inside the cylinder turns with it. The key stands proud of a panel seen from
 * its middle: at POWER the camera looks along the blade (a thin edge and the
 * head, wide and flat, pointing at the position), at OFF and LOCK at its flat
 * side, which then shows beside the cylinder and is bright towards the light.
 */
function keyMovable(geo, d) {
  const k = geo.key;
  // A blade seen flat, from its long end (y0) to its short end (y1), on a slight
  // slant; x is its centre line's place, the side towards the light is `lit`.
  const flat = (name, x, y0, y1) => {
    const at = (y) => x + 0.119 * y;
    const lit = x > 0 ? 1 : -1;
    const hw = (y) => 1.2 + ((y - y0) / (y1 - y0)) * -0.4;
    const edge = (side, y) => [at(y) + side * hw(y), y];
    const blade = poly([edge(lit, y0), edge(lit, y1 - 0.9), [at(y1) + lit * 0.2, y1], edge(-lit, y1 - 0.4), edge(-lit, y0 + 0.25)]);
    return s('g', { class: `key-flat key-${name}` },
      s('path', { d: blade, fill: '#040303', opacity: 0.75, transform: `translate(${-lit * 0.75},0.15)` }),
      s('path', { d: blade, fill: d.url(name) }),
      // the maker's lettering stamped into it, the edge towards the light, the edge away from it
      s('line', { x1: f2(at(-6)), y1: -6, x2: f2(at(3)), y2: 3, stroke: '#1d1a18', 'stroke-width': 0.9, 'stroke-dasharray': '0.22 0.3', opacity: 0.42 }),
      s('path', { d: line([edge(lit, y0 + 1), edge(lit, y1 - 1)]), fill: 'none', stroke: '#d9d5d1', 'stroke-width': 0.14, opacity: 0.6 }),
      s('path', { d: line([edge(-lit, y0 + 0.3), edge(-lit, y1 - 0.5)]), fill: 'none', stroke: '#0a0807', 'stroke-width': 0.22, opacity: 0.8 }),
    );
  };
  const rot = s('g', { class: 'key-rot' },
    s('circle', { class: 'key-plug', r: 3.7 }),
    s('g', { class: 'key-edge', transform: 'translate(-1.5,0)' },
      s('path', { d: 'M0.5,-11.8 L1.9,-8 L1,-3.8 L1,6 L1.9,7.6 L0.95,8.1 Z', fill: '#040303', opacity: 0.8 }),
      s('path', { d: 'M-1.4,-11 L0.5,-11.8 L0.5,-5.8 L0.15,-3.8 L0.15,5.4 L0.95,7.7 L-0.35,8.1 L-1,5.2 L-1,-4 L-1.4,-6 Z', fill: d.url('keyblade') }),
      s('path', { d: 'M0.15,5.2 L0.95,7.7 L-0.35,8.1 L-1,5.2 Z', fill: '#a58764' }),
      s('path', { d: 'M0.12,-3.8 L0.12,5.3', fill: 'none', stroke: '#d4cfcb', 'stroke-width': 0.2 }),
      s('path', { d: 'M-0.5,-5 L-0.45,4.6', fill: 'none', stroke: '#26221f', 'stroke-width': 0.22 }),
      s('path', { d: 'M0.48,-11.8 L0.48,-5.8 L0.14,-3.8', fill: 'none', stroke: '#b9b5b0', 'stroke-width': 0.16 }),
    ),
    flat('keyoff', 2.8, -10.2, 8.4),
    flat('keylock', -2.8, -10.2, 8.4),
  );
  return s('g', { class: 'key', transform: `translate(${k.cx},${k.cy})` }, rot);
}

function knobMovable(geo, d, kn) {
  const ink = '#ecebe5';
  const rs = 11; // the dome; below its top it is one with its shadow
  // the raised bar: a strip across the knob's round top
  const bar = (hw, R, c) => {
    const yc = Math.sqrt(R * R - hw * hw);
    const xc = hw - c;
    return `M${-hw},${f2(-yc + c)} Q${-hw},${f2(-yc)} ${f2(-xc)},${f2(-yc - 0.1)} A${R},${R} 0 0 1 ${f2(xc)},${f2(-yc - 0.1)} Q${hw},${f2(-yc)} ${hw},${f2(-yc + c)} ` +
      `L${hw},${f2(yc - c)} Q${hw},${f2(yc)} ${f2(xc)},${f2(yc + 0.1)} A${R},${R} 0 0 1 ${f2(-xc)},${f2(yc + 0.1)} Q${-hw},${f2(yc)} ${-hw},${f2(yc - c)} Z`;
  };
  // a long edge of the bar catches the light when it faces it (panel.css, by position)
  const edge = (cls, x) => s('path', { class: `knob-edge ${cls}`, d: `M${x},-7.5 L${x},7.5`, fill: 'none', stroke: '#f4f4f2', 'stroke-width': 0.2, 'stroke-linecap': 'round' });
  const rot = s('g', { class: 'knob-rot' },
    s('path', { d: bar(4.45, 10.55, 0.7), fill: '#020203' }),
    s('path', { d: bar(4.2, 10.3, 0.7), fill: d.url('knobbar') }),
    edge('edge-a', 4.17),
    edge('edge-b', -4.17),
    // the pointer: a thin line from the pivot to the bar's end
    s('path', { d: 'M-0.34,-0.4 L-0.24,-10.25 L0.24,-10.25 L0.34,-0.4 Z', fill: ink }),
    s('circle', { r: 1.22, fill: ink }),
  );
  return s('g', { class: 'knob', 'data-knob': kn.name, transform: `translate(${kn.px},${kn.py})` },
    // the big soft shadow falls to the lower left
    s('circle', { cx: -7.8, cy: 10.25, r: 12.2, fill: d.url('knobshadow') }),
    s('g', { transform: `translate(${f2(kn.cx - kn.px)},${f2(kn.cy - kn.py)})` },
      s('circle', { r: rs + 0.15, fill: '#040405' }),
      s('circle', { r: rs, fill: d.url('knobskirt') }),
      // the dome's broad reflection, when the bar lies across the light (panel.css, by position)
      s('circle', { class: 'knob-sheen', r: rs, fill: d.url('knobsheen') }),
      // shallow flutes around the rim, barely catching the light
      s('circle', { r: rs - 0.45, fill: 'none', stroke: d.url('flutes'), 'stroke-width': 0.8, 'stroke-dasharray': '0.8 0.95' }),
    ),
    rot,
  );
}

/**
 * Builds the complete panel SVG. Returns { svg, paddles: Map(name -> g),
 * knobs: Map(name -> rotating g), key: rotating g }.
 */
export function buildPanelSvg(geo, { id = 'pnl', title = 'PDP-11/70 front panel' } = {}) {
  const [vx, vy, vw, vh] = geo.viewBox;
  const d = new Defs(id);
  fixedDefs(d, geo);
  const svg = s('svg', {
    class: 'panel-svg', viewBox: `${vx} ${vy} ${vw} ${vh}`, preserveAspectRatio: 'xMidYMid meet',
    role: 'img', 'aria-label': title, focusable: 'false',
  });
  const body = panelOutline(geo);
  svg.append(
    d.el,
    bezel(geo, d),
    plate(geo),
    branding(geo, d),
    s('path', { class: 'window', d: body, fill: geo.colors.panel }),
    panelLight(geo, d, body),
    // the printed sheet
    s('g', { class: 'sheet', transform: `matrix(${geo.sheet.join(' ')})` }, ...boxesAndCells(geo, d), ...lampWindows(geo, d), bars(geo, d), tiles(geo, d), ...labels(geo), logo(geo, id)),
    keyStatic(geo, d),
  );
  const paddles = new Map();
  const pg = s('g', { class: 'paddles' });
  for (const p of geo.paddles) {
    const g = paddle(d, p, p.type === 'sr' ? p.art.x0 + 8.3 : null);
    paddles.set(p.name, g);
    pg.append(g);
  }
  svg.append(pg);
  const knobs = new Map();
  for (const kn of geo.knobs) {
    const g = knobMovable(geo, d, kn);
    knobs.set(kn.name, g.querySelector('.knob-rot'));
    svg.append(g);
  }
  const keyG = keyMovable(geo, d);
  svg.append(keyG);
  return { svg, paddles, knobs, key: keyG.querySelector('.key-rot') };
}
