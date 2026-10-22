#!/usr/bin/env python3
"""Prototype generator: PDP-11/70 front panel as SVG from the measured element table.

All geometry is in millimetres in the INDICATOR-PANEL frame (origin = top-left corner of the
464 x 186 mm acrylic indicator panel, per Chiappa's mechanical drawing). The SVG viewBox is the
full console bezel: x in [-10.1, 474.3], y in [-9.9, 250.5]  (484.4 x 260.4 mm).

Sources of the numbers: 300 dpi scan (J. Noel Chiappa) segmented with scripts/segment*.py and
build_table.py; bezel/paddle geometry from the BlinkenBone photo fitted with bbfit.py.
This is a research prototype only (used to validate the element table visually).
"""
import json, math, sys

# ---------------------------------------------------------------- geometry (mm, panel frame)
PITCH = 14.31
COL0 = 25.25                      # slot/column 0 = SR21 / A21
def col(k): return COL0 + PITCH * k          # k = 0..29 (30 paddle slots)
def acol(bit): return col(21 - bit)          # address bit column
Y_STATUS, Y_ADDR, Y_DATA, Y_TILE = 65.75, 103.55, 144.02, 170.70
LAMP_W, LAMP_H, LAMP_R = 9.2, 9.3, 1.3
KNOB_LAMP_X = (358.7, 390.5)
KNOB_LAMP_Y_ADDR = (65.70, 77.15, 88.50, 100.15)
KNOB_LAMP_Y_DATA = (130.55, 141.80)
KNOB_C = {'addr': (432.3, 82.9), 'data': (432.3, 136.1)}
KNOB_R = 12.5
KEY_C = (32.2, 136.0)
BEZEL = (-10.1, -9.9, 474.3, 250.5)
WINDOW = (0.4, 23.5, 463.6, 186.0)            # visible dark acrylic
STRIP = (-3.4, -2.7, 300.1, 16.5)              # raised logo strip on top band
PADDLE_TOP, PADDLE_CREASE, PADDLE_BOT, PADDLE_W = 186.6, 205.4, 224.2, 13.5
ADDR_BAR_Y = (80.2, 86.7); ADDR_CELL_Y = (86.7, 110.1)
DATA_BAR_Y = (120.6, 127.2); DATA_CELL_Y = (127.5, 150.3)
STATUS_BOX_Y = (54.4, 72.3)
PARITY_BOX = (61.4, 120.4, 88.9, 150.3)
KNOB_BOX = {'addr': (348.2, 52.3, 446.8, 110.3), 'data': (348.1, 120.4, 446.6, 150.8)}
NOTCH_X = (132.9, 175.7, 218.6)               # on DATA bars, measured
TILE_W, TILE_H, TILE_R = 13.0, 17.5, 2.0
HALF_TILE_Y = ((162.4, 170.2), (171.6, 179.5))

# ---------------------------------------------------------------- colours (recommended display palette)
C = {
    'bezel': '#dcd9d0', 'bezel_hi': '#ebe9e3', 'bezel_lo': '#b9b5ab', 'bezel_shadow': '#6d655a',
    'panel': '#1f141c', 'panel_hi': '#2a1a26', 'box': '#0f0f13', 'sep': '#1b1419',
    'bar_rose': '#7e2639', 'bar_mag': '#503049', 'tile_rose': '#86293f', 'tile_mag': '#58304f',
    'ink': '#ece6da',
    'lamp_off': '#170c0d', 'lamp_off_hi': '#2a1416',
    'pad_rose': '#cf2647', 'pad_rose_hi': '#ef3c5b', 'pad_rose_lo': '#5f2a30', 'pad_rose_dk': '#3a0a12',
    'pad_mag': '#6e2a55', 'pad_mag_hi': '#8f5079', 'pad_mag_lo': '#3e2b34', 'pad_mag_dk': '#1c0b14',
    'pad_blank': '#e8e7e1', 'pad_blank_lo': '#85807a',
    'knob': '#0b0b0c', 'knob_hi': '#3a3a3e', 'chrome': '#c9c9c6',
}

ADDR_GROUPS = [(21, 21, 'rose'), (20, 18, 'mag'), (17, 15, 'rose'), (14, 12, 'mag'), (11, 9, 'rose'), (8, 6, 'mag'), (5, 3, 'rose'), (2, 0, 'mag')]
DATA_GROUPS = [(15, 15, 'rose'), (14, 12, 'mag'), (11, 9, 'rose'), (8, 6, 'mag'), (5, 3, 'rose'), (2, 0, 'mag')]
STATUS = [('PAR_ERR', ('PAR', 'ERR')), ('ADRS_ERR', ('ADRS', 'ERR')), ('RUN', ('RUN',)), ('PAUSE', ('PAUSE',)),
          ('MASTER', ('MASTER',)), ('USER', ('USER',)), ('SUPER', ('SUPER',)), ('KERNEL', ('KERNEL',)),
          ('DATA_SPACE', ('DATA',)), ('ADRS16', ('', '16')), ('ADRS18', ('', '18')), ('ADRS22', ('', '22'))]
STATUS_GROUPS = [(0, 1), (2, 4), (5, 7), (8, 11)]       # index ranges into STATUS (cols 10..21)
KNOB_LABELS_ADDR = [('USER_D', 'USER D', 0, 0), ('SUPER_D', 'SUPER D', 0, 1), ('KERNEL_D', 'KERNEL D', 0, 2), ('CONS_PHY', 'CONS PHY', 0, 3),
                    ('USER_I', 'USER I', 1, 0), ('SUPER_I', 'SUPER I', 1, 1), ('KERNEL_I', 'KERNEL I', 1, 2), ('PROG_PHY', 'PROG PHY', 1, 3)]
KNOB_LABELS_DATA = [('DATA_PATHS', ('DATA', 'PATHS'), 0, 0), ('BUS_REG', ('BUS REG',), 0, 1),
                    ('UADRS', ('μ ADRS', 'FPP/CPU'), 1, 0), ('DISPLAY_REG', ('DISPLAY', 'REGISTER'), 1, 1)]
# knob detents: angle (deg, clockwise from 12 o'clock) -> position name (BlinkenBone photos)
ADDR_KNOB_ANGLES = {'PROG_PHY': 22.5, 'CONS_PHY': 67.5, 'KERNEL_D': 112.5, 'SUPER_D': 157.5, 'USER_D': 202.5,
                    'USER_I': 247.5, 'SUPER_I': 292.5, 'KERNEL_I': 337.5}
DATA_KNOB_ANGLES = {'BUS_REG': (22.5, 202.5), 'DATA_PATHS': (67.5, 247.5), 'UADRS': (112.5, 292.5), 'DISPLAY_REG': (157.5, 337.5)}
SLOTS = [f'SR{b}' for b in range(21, -1, -1)] + ['LAMP_TEST', 'LOAD_ADRS', 'EXAM', 'DEP', 'CONT', 'ENABLE_HALT', 'SINST_SBUS', 'START']
SLOT_COLOR = {}
for b in range(22):
    SLOT_COLOR[f'SR{b}'] = 'rose' if b in (21, 17, 16, 15, 11, 10, 9, 5, 4, 3) else 'mag'
SLOT_COLOR.update({'LAMP_TEST': 'blank', 'LOAD_ADRS': 'rose', 'EXAM': 'mag', 'DEP': 'rose', 'CONT': 'mag',
                   'ENABLE_HALT': 'rose', 'SINST_SBUS': 'mag', 'START': 'rose'})


LABELS = [
 # status row (cap 2.5 mm), centred on lamp columns; line 1 baseline 57.3, line 2 baseline 60.0
 ('PAR', 168.3, 57.3, 2.5, 6.7, 'middle'), ('ERR', 168.3, 60.0, 2.5, 6.9, 'middle'),
 ('ADRS', 182.7, 57.3, 2.5, 9.0, 'middle'), ('ERR', 182.6, 60.0, 2.5, 6.9, 'middle'),
 ('RUN', 197.0, 57.3, 2.5, 7.3, 'middle'), ('PAUSE', 211.4, 57.3, 2.5, 10.4, 'middle'),
 ('MASTER', 225.8, 57.3, 2.5, 12.8, 'middle'), ('USER', 240.1, 57.3, 2.5, 9.0, 'middle'),
 ('SUPER', 254.2, 57.3, 2.5, 10.8, 'middle'), ('KERNEL', 268.5, 57.3, 2.5, 12.5, 'middle'),
 ('DATA', 282.6, 57.3, 2.5, 8.0, 'middle'), ('ADDRESSING', 311.4, 57.3, 2.5, 20.1, 'middle'),
 ('16', 297.2, 60.0, 2.5, 3.7, 'middle'), ('18', 311.5, 60.0, 2.5, 3.7, 'middle'), ('22', 325.3, 60.0, 2.5, 4.3, 'middle'),
 # bar captions (cap 3.6), right aligned inside the bit 2-0 bar
 ('ADDRESS', 329.6, 84.8, 3.6, 21.8, 'end'), ('DATA', 329.5, 125.5, 3.6, 11.3, 'end'),
 # parity box
 ('PARITY', 75.3, 125.4, 3.6, 15.9, 'middle'), ('HIGH', 68.2, 129.9, 3.2, 10.2, 'middle'), ('LOW', 82.4, 129.9, 3.2, 9.1, 'middle'),
 # key switch (cap 2.6)
 ('POWER', 25.6, 122.9, 2.6, 11.3, 'middle'), ('LOCK', 42.2, 122.9, 2.6, 8.6, 'middle'), ('OFF', 23.4, 150.0, 2.6, 6.8, 'middle'),
 # ADDRESS select (cap 3.1), left aligned at 363.9 / 395.6
 ('USER D', 363.9, 66.9, 3.1, 15.6, 'start'), ('USER I', 395.9, 66.9, 3.1, 13.7, 'start'),
 ('SUPER D', 363.9, 78.3, 3.1, 18.1, 'start'), ('SUPER I', 395.7, 78.3, 3.1, 16.1, 'start'),
 ('KERNEL D', 363.9, 89.8, 3.1, 20.5, 'start'), ('KERNEL I', 395.6, 89.8, 3.1, 18.7, 'start'),
 ('CONS PHY', 363.9, 101.1, 3.1, 21.0, 'start'), ('PROG PHY', 395.6, 101.1, 3.1, 20.6, 'start'),
 # DATA select
 ('DATA', 363.8, 129.7, 3.1, 9.7, 'start'), ('PATHS', 363.8, 133.5, 3.1, 12.8, 'start'),
 ('\u03bc ADRS', 395.7, 129.8, 3.1, 15.5, 'start'), ('FPP/CPU', 395.7, 133.9, 3.1, 17.5, 'start'),
 ('BUS REG', 363.9, 143.1, 3.1, 18.5, 'start'),
 ('DISPLAY', 395.6, 141.2, 3.1, 16.9, 'start'), ('REGISTER', 395.6, 145.3, 3.1, 19.7, 'start'),
 # control tiles (cap 2.5)
 ('LOAD', 354.1, 170.5, 2.5, 9.0, 'middle'), ('ADRS', 354.1, 173.3, 2.5, 8.9, 'middle'),
 ('EXAM', 368.5, 171.85, 2.5, 9.1, 'middle'), ('DEP', 382.7, 171.85, 2.5, 6.9, 'middle'),
 ('CONT', 397.2, 171.95, 2.5, 9.1, 'middle'), ('START', 440.2, 171.9, 2.5, 10.0, 'middle'),
 ('ENABLE', 411.8, 167.3, 2.5, 12.6, 'middle'), ('HALT', 411.6, 176.6, 2.5, 8.0, 'middle'),
 ('S INST', 425.9, 167.3, 2.5, 11.6, 'middle'), ('S BUS', 425.95, 175.3, 2.5, 10.5, 'middle'), ('CYCLE', 425.8, 178.2, 2.5, 10.2, 'middle'),
]

FONT = "font-family=\"'Helvetica Neue',Helvetica,Arial,'Liberation Sans',Arimo,sans-serif\" font-weight=\"700\""
def fs(cap): return cap / 0.717        # Helvetica cap-height ratio


def text(x, y, s, cap, anchor='middle', fill=None, extra='', width=None):
    tl = f' textLength="{width:.2f}" lengthAdjust="spacingAndGlyphs"' if width else ''
    return (f'<text x="{x:.2f}" y="{y:.2f}" font-size="{fs(cap):.2f}" text-anchor="{anchor}"{tl} '
            f'fill="{fill or C["ink"]}" {extra}>{s}</text>')


def rrect(x0, y0, x1, y1, r, fill, extra=''):
    return f'<rect x="{x0:.2f}" y="{y0:.2f}" width="{x1-x0:.2f}" height="{y1-y0:.2f}" rx="{r}" fill="{fill}" {extra}/>'


def lamp(id_, cx, cy):
    x0, y0 = cx - LAMP_W/2, cy - LAMP_H/2
    return (f'<g class="lamp" id="{id_}" data-cx="{cx:.2f}" data-cy="{cy:.2f}">'
            f'<rect x="{x0:.2f}" y="{y0:.2f}" width="{LAMP_W}" height="{LAMP_H}" rx="{LAMP_R}" fill="url(#lampOff)"/>'
            f'<rect class="lit" x="{x0:.2f}" y="{y0:.2f}" width="{LAMP_W}" height="{LAMP_H}" rx="{LAMP_R}" fill="url(#lampOn)" opacity="0"/>'
            f'</g>')


def glow(id_, cx, cy):
    return f'<circle class="glow" data-for="{id_}" cx="{cx:.2f}" cy="{cy:.2f}" r="9" fill="url(#lampGlow)" opacity="0"/>'


def bar_path(x0, x1, y0, y1, notches, r_left, r_right):
    """bar with optional rounded top-left/top-right corners and notches cut from the bottom edge"""
    p = [f'M{x0:.2f},{y1:.2f}', f'L{x0:.2f},{y0 + r_left:.2f}']
    if r_left: p.append(f'Q{x0:.2f},{y0:.2f} {x0 + r_left:.2f},{y0:.2f}')
    p.append(f'L{x1 - r_right:.2f},{y0:.2f}')
    if r_right: p.append(f'Q{x1:.2f},{y0:.2f} {x1:.2f},{y0 + r_right:.2f}')
    p.append(f'L{x1:.2f},{y1:.2f}')
    for nx in sorted(notches, reverse=True):          # walk right->left along the bottom edge
        if not (x0 < nx < x1): continue
        hb, ht, h = 2.3, 0.75, 3.9                   # half base, half top, height (mm)
        p += [f'L{nx + hb:.2f},{y1:.2f}', f'C{nx + ht:.2f},{y1:.2f} {nx + ht:.2f},{y1 - h:.2f} {nx:.2f},{y1 - h:.2f}',
              f'C{nx - ht:.2f},{y1 - h:.2f} {nx - ht:.2f},{y1:.2f} {nx - hb:.2f},{y1:.2f}']
    p.append('Z')
    return ' '.join(p)


def paddle(slot, k, pose):
    """pose: 'rest' (bowtie), 'back' (top tilted away: SR=1, DEP raised, LAMP TEST raised), 'fwd' (pressed down/HALT/S BUS)"""
    cx = col(k); w = PADDLE_W; x0, x1 = cx - w/2, cx + w/2
    t, m, b = PADDLE_TOP, PADDLE_CREASE, PADDLE_BOT
    colr = SLOT_COLOR[slot]
    if colr == 'blank':
        hi, lo, dk, base = C['pad_blank'], C['pad_blank_lo'], '#5f5a54', C['pad_blank']
    elif colr == 'rose':
        hi, lo, dk, base = C['pad_rose_hi'], C['pad_rose_lo'], C['pad_rose_dk'], C['pad_rose']
    else:
        hi, lo, dk, base = C['pad_mag_hi'], C['pad_mag_lo'], C['pad_mag_dk'], C['pad_mag']
    g = [f'<g class="paddle" id="sw_{slot}" data-pose="{pose}" tabindex="-1">']
    if pose == 'rest':
        waist = 1.6   # side notches at the crease (bowtie silhouette)
        g.append(f'<path d="M{x0:.2f},{t:.2f} L{x1:.2f},{t:.2f} L{x1:.2f},{m - 4:.2f} L{x1 - waist:.2f},{m:.2f} L{x0 + waist:.2f},{m:.2f} L{x0:.2f},{m - 4:.2f} Z" fill="{base}"/>')
        g.append(f'<rect x="{x0:.2f}" y="{t:.2f}" width="{w:.2f}" height="1.2" fill="{hi}" opacity="0.8"/>')
        g.append(f'<path d="M{x0 + waist:.2f},{m:.2f} L{x1 - waist:.2f},{m:.2f} L{x1:.2f},{m + 4:.2f} L{x1:.2f},{b:.2f} L{x0:.2f},{b:.2f} L{x0:.2f},{m + 4:.2f} Z" fill="url(#lowFace_{colr})"/>')
        g.append(f'<path d="M{x0:.2f},{m - 4:.2f} L{x0 + waist:.2f},{m:.2f} L{x0:.2f},{m + 4:.2f} Z M{x1:.2f},{m - 4:.2f} L{x1 - waist:.2f},{m:.2f} L{x1:.2f},{m + 4:.2f} Z" fill="#000" opacity="0.55"/>')
    elif pose == 'back':
        g.append(f'<rect x="{x0:.2f}" y="{t:.2f}" width="{w:.2f}" height="{b - t:.2f}" fill="{dk}"/>')
        g.append(f'<rect x="{x0:.2f}" y="{t:.2f}" width="{w:.2f}" height="1.6" fill="{base}"/>')
        g.append(f'<rect x="{x0 + 1:.2f}" y="{m + 2:.2f}" width="{w - 2:.2f}" height="5" fill="{hi}" opacity="0.10"/>')
    else:  # fwd
        g.append(f'<rect x="{x0:.2f}" y="{t:.2f}" width="{w:.2f}" height="{b - t:.2f}" fill="{hi}"/>')
        g.append(f'<rect x="{x0:.2f}" y="{b - 3:.2f}" width="{w:.2f}" height="3" fill="{base}"/>')
        g.append(f'<rect x="{x0 + 1.5:.2f}" y="{t + 2:.2f}" width="1.2" height="{b - t - 6:.2f}" fill="#fff" opacity="0.18"/>')
    g.append(f'<rect class="hit" x="{x0:.2f}" y="{t:.2f}" width="{w:.2f}" height="{b - t:.2f}" fill="transparent"/>')
    g.append('</g>')
    return ''.join(g)


def knob(name, pos_angle):
    cx, cy = KNOB_C[name]
    return (f'<g class="knob" id="knob_{name}" transform="translate({cx},{cy})">'
            f'<circle r="{KNOB_R + 0.8}" fill="#000" opacity="0.45" transform="translate(1.2,2.0)"/>'
            f'<circle r="{KNOB_R}" fill="url(#knobBody)"/>'
            f'<g class="rot" transform="rotate({pos_angle})">'
            f'<rect x="-4.6" y="-13.2" width="9.2" height="26.4" rx="3.2" fill="url(#knobBar)"/>'
            f'<rect x="-0.45" y="-10.2" width="0.9" height="10.2" rx="0.45" fill="{C["ink"]}"/>'
            f'<circle r="1.1" fill="{C["ink"]}"/></g></g>')


def keylock(state):
    cx, cy = KEY_C
    ang = {'off': 225, 'power': 315, 'lock': 45}[state]
    return (f'<g class="keyswitch" id="keyswitch" transform="translate({cx},{cy})">'
            f'<circle r="9.6" fill="#050506"/><circle r="9.6" fill="none" stroke="#2a2a2e" stroke-width="0.5"/>'
            f'<circle r="5.6" fill="url(#chrome)"/><circle r="3.4" fill="#8d8d8a"/>'
            f'<g transform="rotate({ang})"><rect x="-1.9" y="-11.5" width="3.8" height="23" rx="1.2" fill="url(#chrome)" stroke="#555" stroke-width="0.25"/></g>'
            f'</g>')


def build(state=None):
    state = state or {}
    lit = state.get('lit', {})
    poses = state.get('poses', {})
    o = []
    bx0, by0, bx1, by1 = BEZEL
    o.append(f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="{bx0} {by0} {bx1 - bx0:.1f} {by1 - by0:.1f}" '
             f'width="100%" role="img" aria-label="PDP-11/70 front panel" {FONT}>')
    o.append('''<defs>
  <radialGradient id="lampOn" cx="50%" cy="48%" r="60%">
    <stop offset="0" stop-color="#ffd2c0"/><stop offset="0.18" stop-color="#ff5a3c"/>
    <stop offset="0.5" stop-color="#f0101e"/><stop offset="1" stop-color="#8a0012"/></radialGradient>
  <radialGradient id="lampGlow"><stop offset="0" stop-color="#ff2a1a" stop-opacity="0.55"/>
    <stop offset="0.45" stop-color="#ff1a10" stop-opacity="0.18"/><stop offset="1" stop-color="#ff1a10" stop-opacity="0"/></radialGradient>
  <linearGradient id="lampOff" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#241214"/><stop offset="1" stop-color="#120a0b"/></linearGradient>
  <linearGradient id="lowFace_rose" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#6a3438"/><stop offset="1" stop-color="#2e0c12"/></linearGradient>
  <linearGradient id="lowFace_mag" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#46323a"/><stop offset="1" stop-color="#1e1015"/></linearGradient>
  <linearGradient id="lowFace_blank" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#8a857d"/><stop offset="1" stop-color="#5a554f"/></linearGradient>
  <radialGradient id="knobBody" cx="40%" cy="35%" r="70%"><stop offset="0" stop-color="#2e2e31"/><stop offset="1" stop-color="#050506"/></radialGradient>
  <linearGradient id="knobBar" x1="0" y1="0" x2="1" y2="0"><stop offset="0" stop-color="#1a1a1c"/><stop offset="0.5" stop-color="#3b3b3f"/><stop offset="1" stop-color="#0d0d0e"/></linearGradient>
  <linearGradient id="chrome" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="#f4f4f2"/><stop offset="0.5" stop-color="#9a9a97"/><stop offset="1" stop-color="#e2e2df"/></linearGradient>
  <linearGradient id="panelSheen" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="#2a1a26"/><stop offset="0.5" stop-color="#1f141c"/><stop offset="1" stop-color="#1a1118"/></linearGradient>
</defs>''')
    # bezel & top band
    o.append(rrect(bx0, by0, bx1, by1, 4, C['bezel']))
    o.append(rrect(bx0, by0, bx1, by0 + 1.2, 0.6, C['bezel_hi']))
    o.append(rrect(STRIP[0], STRIP[1], STRIP[2], STRIP[3], 3.5, C['bezel_hi'], f'stroke="{C["bezel_lo"]}" stroke-width="0.35"'))
    # tone-on-tone 'digital' boxes + tagline (optional branding; DEC trademarks)
    for i, ch in enumerate('digital'):
        x = 15.9 + i * 6.63
        o.append(rrect(x, 0.6, x + 6.1, 13.2, 0.3, 'none', f'stroke="{C["bezel_lo"]}" stroke-width="0.35"'))
        o.append(f'<text x="{x + 3.05:.2f}" y="10.6" font-size="9" text-anchor="middle" fill="none" stroke="{C["bezel_lo"]}" stroke-width="0.3" font-weight="400">{ch}</text>')
    o.append(f'<text x="65.2" y="11.0" font-size="8.2" fill="none" stroke="{C["bezel_lo"]}" stroke-width="0.3" font-weight="400" '
             f'font-family="\'URW Gothic\',\'Century Gothic\',\'Avant Garde\',sans-serif" textLength="214" lengthAdjust="spacingAndGlyphs">digital equipment corporation • maynard, massachusetts</text>')
    # paddle bay floor + lip
    o.append(rrect(bx0, 224.2, bx1, 245.6, 0, '#4a3f36'))
    o.append(rrect(bx0, 245.6, bx1, by1, 2, '#a0988c'))
    o.append(rrect(18.0, 186.0, 447.5, 224.6, 0, '#120c0e'))
    # dark window
    wx0, wy0, wx1, wy1 = WINDOW
    o.append(f'<path d="M{wx0},{wy1} L{wx0},{wy0 + 8} Q{wx0},{wy0} {wx0 + 8},{wy0} L{wx1 - 8},{wy0} Q{wx1},{wy0} {wx1},{wy0 + 8} L{wx1},{wy1 - 6} Q{wx1},{wy1} {wx1 - 6},{wy1} Z" fill="url(#panelSheen)"/>')
    # pdp11/70 logo: traced path (scripts/trace_logo.py) if available, else a crude text approximation
    if state.get('logo_path'):
        o.append(open(state['logo_path']).read())
    else:
        o.append(f'<text x="18.5" y="67.6" font-size="19.5" fill="none" stroke="{C["ink"]}" stroke-width="0.55" font-weight="400">pdp11/70</text>')
    # status boxes, labels, lamps
    glows = []
    for (a, b) in STATUS_GROUPS:
        o.append(rrect(col(10 + a) - 6.6, STATUS_BOX_Y[0], col(10 + b) + 6.6, STATUS_BOX_Y[1], 2.2, C['box']))
    for i, (nm, lab) in enumerate(STATUS):
        x = col(10 + i)
        o.append(lamp(f'L_{nm}', x, Y_STATUS)); glows.append(glow(f'L_{nm}', x, Y_STATUS))
    # address row
    for hi, lo, c in ADDR_GROUPS:
        x0, x1 = acol(hi) - 6.47, acol(lo) + 6.47
        o.append(f'<path d="{bar_path(x0, x1, ADDR_BAR_Y[0], ADDR_BAR_Y[1], [], 2.4 if hi == 21 else 0, 2.4 if lo == 0 else 0)}" fill="{C["bar_" + c]}"/>')
        for bit in range(lo, hi + 1):
            cx = acol(bit)
            o.append(f'<path d="M{cx - 6.6:.2f},{ADDR_CELL_Y[0]} L{cx + 6.6:.2f},{ADDR_CELL_Y[0]} L{cx + 6.6:.2f},{ADDR_CELL_Y[1] - 2.4} Q{cx + 6.6:.2f},{ADDR_CELL_Y[1]} {cx + 4.2:.2f},{ADDR_CELL_Y[1]} L{cx - 4.2:.2f},{ADDR_CELL_Y[1]} Q{cx - 6.6:.2f},{ADDR_CELL_Y[1]} {cx - 6.6:.2f},{ADDR_CELL_Y[1] - 2.4} Z" fill="{C["box"]}"/>')
            o.append(lamp(f'L_A{bit}', cx, Y_ADDR)); glows.append(glow(f'L_A{bit}', cx, Y_ADDR))
    # data row
    for hi, lo, c in DATA_GROUPS:
        x0, x1 = acol(hi) - 6.47, acol(lo) + 6.47
        o.append(f'<path d="{bar_path(x0, x1, DATA_BAR_Y[0], DATA_BAR_Y[1], NOTCH_X, 2.4 if hi == 15 else 0, 2.4 if lo == 0 else 0)}" fill="{C["bar_" + c]}"/>')
        for bit in range(lo, hi + 1):
            cx = acol(bit)
            o.append(f'<path d="M{cx - 6.6:.2f},{DATA_CELL_Y[0]} L{cx + 6.6:.2f},{DATA_CELL_Y[0]} L{cx + 6.6:.2f},{DATA_CELL_Y[1] - 2.4} Q{cx + 6.6:.2f},{DATA_CELL_Y[1]} {cx + 4.2:.2f},{DATA_CELL_Y[1]} L{cx - 4.2:.2f},{DATA_CELL_Y[1]} Q{cx - 6.6:.2f},{DATA_CELL_Y[1]} {cx - 6.6:.2f},{DATA_CELL_Y[1] - 2.4} Z" fill="{C["box"]}"/>')
            o.append(lamp(f'L_D{bit}', cx, Y_DATA)); glows.append(glow(f'L_D{bit}', cx, Y_DATA))
    # parity
    o.append(rrect(*PARITY_BOX, 2.4, C['box']))
    for nm, x in (('PARITY_HIGH', acol(18)), ('PARITY_LOW', acol(17))):
        o.append(lamp(f'L_{nm}', x, Y_DATA)); glows.append(glow(f'L_{nm}', x, Y_DATA))
    # key switch
    o.append(keylock(state.get('key', 'power')))
    # knob boxes, labels, lamps, knobs
    for nm, box in KNOB_BOX.items():
        o.append(rrect(*box, 3.0, C['box']))
    for idn, lab, c_, r_ in KNOB_LABELS_ADDR:
        x, y = KNOB_LAMP_X[c_], KNOB_LAMP_Y_ADDR[r_]
        o.append(lamp(f'L_{idn}', x, y)); glows.append(glow(f'L_{idn}', x, y))
    for idn, lab, c_, r_ in KNOB_LABELS_DATA:
        x, y = KNOB_LAMP_X[c_], KNOB_LAMP_Y_DATA[r_]
        o.append(lamp(f'L_{idn}', x, y)); glows.append(glow(f'L_{idn}', x, y))
    o.append(knob('addr', ADDR_KNOB_ANGLES[state.get('addr_knob', 'CONS_PHY')]))
    o.append(knob('data', DATA_KNOB_ANGLES[state.get('data_knob', 'DATA_PATHS')][0]))
    # tiles
    for k, slot in enumerate(SLOTS):
        cx = col(k); c = SLOT_COLOR[slot]
        if c == 'blank': continue
        fill = C['tile_rose'] if c == 'rose' else C['tile_mag']
        if slot in ('ENABLE_HALT', 'SINST_SBUS'):
            for (y0, y1) in HALF_TILE_Y: o.append(rrect(cx - TILE_W/2, y0, cx + TILE_W/2, y1, 1.6, fill))
            continue
        o.append(rrect(cx - TILE_W/2, Y_TILE - TILE_H/2, cx + TILE_W/2, Y_TILE + TILE_H/2, TILE_R, fill))
        if slot.startswith('SR'): o.append(text(cx, 172.35, slot[2:], 3.5, extra='font-weight="500"', width=(5.1 if len(slot) > 3 else (2.5 if slot == 'SR1' else 3.5))))
    # printed labels (measured positions / ink widths -> textLength keeps any fallback font on its footprint)
    for (t_, x_, y_, cap_, w_, anc_) in LABELS:
        o.append(text(x_, y_, t_, cap_, anchor=anc_, width=w_))
    # paddles
    for k, slot in enumerate(SLOTS):
        o.append(paddle(slot, k, poses.get(slot, 'rest')))
    # glow layer last (additive look), then set brightness
    o.append('<g id="glowLayer" style="mix-blend-mode:screen">' + ''.join(glows) + '</g>')
    svg = '\n'.join(o) + '\n</svg>'
    # apply lit state (static rendering for validation)
    for id_, v in lit.items():
        svg = svg.replace(f'<g class="lamp" id="{id_}"', f'<g class="lamp" id="{id_}" data-b="{v}"')
        # crude: set the lit rect / glow opacity
    import re
    def setop(m):
        return m.group(0)
    for id_, v in lit.items():
        svg = re.sub(rf'(<g class="lamp" id="{id_}"[^>]*>.*?<rect class="lit"[^>]*?)opacity="0"', rf'\1opacity="{v}"', svg, count=1, flags=re.S)
        svg = svg.replace(f'data-for="{id_}" ', f'data-for="{id_}" ', 1)
        svg = re.sub(rf'(<circle class="glow" data-for="{id_}"[^>]*?)opacity="0"', rf'\1opacity="{v}"', svg, count=1)
    return svg


if __name__ == '__main__':
    out = sys.argv[1]
    st = json.loads(sys.argv[2]) if len(sys.argv) > 2 else {}
    open(out, 'w').write(build(st))
    print('wrote', out)
