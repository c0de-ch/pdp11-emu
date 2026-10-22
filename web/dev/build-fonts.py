#!/usr/bin/env python3
"""Build the vendored web fonts and the bezel artwork of the front panel.

Outputs (committed, so this only needs to run when a font changes):

  web/assets/fonts/glass-tty-vt220.woff2   console font (Unlicense), all glyphs
  web/assets/fonts/vt323.woff2             console font for non-integer scaling (OFL), subset
  web/assets/fonts/archivo-panel.woff2     panel labels: Archivo instanced at wght 600 / wdth 85 (OFL), subset
  web/assets/fonts/archivo-panel-500.woff2 switch-register tile numbers: Archivo wght 500 / wdth 85, digits only
  web/assets/fonts/LICENSE-*.txt           the fonts' licence texts (Montserrat Alternates: its
                                           outlines are used as artwork in geometry.json)
  web/dev/panel-art.json                   bezel tagline and "digital" logo letters as SVG path
                                           data in panel millimetres (Montserrat Alternates
                                           outlines, OFL); merged into geometry.json by
                                           gen-geometry.mjs

Requirements: Python 3.9+, fontTools and brotli (pip install fonttools brotli).

Usage:
  python3 -I web/dev/build-fonts.py --download [--cache DIR]   # fetch the pinned sources
  python3 -I web/dev/build-fonts.py --src DIR                  # use files already in DIR

Font files are untrusted input: run this with "python3 -I" (isolated mode). Every
download is checked against the pinned sha256 before it is parsed.
"""

import argparse
import hashlib
import json
import os
import sys
import tempfile
import urllib.request

try:
    from fontTools.pens.boundsPen import BoundsPen
    from fontTools.pens.svgPathPen import SVGPathPen
    from fontTools.pens.transformPen import TransformPen
    from fontTools.subset import Options, Subsetter
    from fontTools.ttLib import TTFont
    from fontTools.varLib.instancer import instantiateVariableFont
except ImportError:  # pragma: no cover - environment problem, explained to the user
    sys.exit("build-fonts.py needs fontTools and brotli: pip install fonttools brotli")

HERE = os.path.dirname(os.path.abspath(__file__))
WEB = os.path.dirname(HERE)
FONTS_OUT = os.path.join(WEB, "assets", "fonts")
ART_OUT = os.path.join(HERE, "panel-art.json")
ELEMENTS = os.path.join(os.path.dirname(WEB), "docs", "research", "r4-assets", "panel1170_elements.json")

GOOGLE_FONTS = "https://raw.githubusercontent.com/google/fonts/45e3d5269e4a17d823f6d84470bc084dc5ae46f6/ofl"
GLASSTTY = "https://raw.githubusercontent.com/svofski/glasstty/2bed112c6844db70fbd857ab138d3fc283439102"

# file name -> (url, sha256); pinned commits, verified before use.
SOURCES = {
    "Glass_TTY_VT220.ttf": (GLASSTTY + "/Glass_TTY_VT220.ttf",
                            "cc4d515abd736808f429f2df2fe950b6c13468c7a889a7032d074aa05f246958"),
    "LICENSE-glasstty": (GLASSTTY + "/LICENSE", None),
    "Archivo[wdth,wght].ttf": (GOOGLE_FONTS + "/archivo/Archivo%5Bwdth,wght%5D.ttf",
                               "0e094a7d3c7c4c25cf1310c4b30014f1dae9332220b1c2c88f4fa996f0b05053"),
    "OFL-archivo.txt": (GOOGLE_FONTS + "/archivo/OFL.txt", None),
    "VT323-Regular.ttf": (GOOGLE_FONTS + "/vt323/VT323-Regular.ttf",
                          "cf4de751ada78ceac033dbe16a687742939995b77bc2a052ae17a4957958594d"),
    "OFL-vt323.txt": (GOOGLE_FONTS + "/vt323/OFL.txt", None),
    "MontserratAlternates-Regular.ttf": (GOOGLE_FONTS + "/montserratalternates/MontserratAlternates-Regular.ttf",
                                         "3b7c563f2087888279fdb7c21cc57b073ba1931dbe413e1202485b46ebfb7ead"),
    "OFL-montserratalternates.txt": (GOOGLE_FONTS + "/montserratalternates/OFL.txt", None),
}

# Licence texts are small and checked by content instead of hash (first line).
LICENCE_FIRST_LINE = {
    "LICENSE-glasstty": "This is free and unencumbered software released into the public domain.",
    "OFL-archivo.txt": "Copyright 2020 The Archivo Project Authors",
    "OFL-vt323.txt": "Copyright 2011, The VT323 Project Authors",
    "OFL-montserratalternates.txt": "Copyright 2011 The Montserrat Project Authors",
}

# Every character printed on the panel (labels in geometry.json) plus a little slack.
PANEL_TEXT = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 /.,-&µμ"
# ASCII, Latin-1 and the DEC Special Graphics replacements xterm.js may draw with a font.
CONSOLE_UNICODES = (list(range(0x20, 0x7F)) + list(range(0xA0, 0x100)) +
                    [0x25C6, 0x2592, 0x2409, 0x240C, 0x240D, 0x240A, 0x2424, 0x240B,
                     0x23BA, 0x23BB, 0x23BC, 0x23BD, 0x2264, 0x2265, 0x03C0, 0x2260,
                     0x2022, 0x2026, 0x2018, 0x2019, 0x201C, 0x201D])


def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for block in iter(lambda: f.read(1 << 16), b""):
            h.update(block)
    return h.hexdigest()


def fetch(cache):
    os.makedirs(cache, exist_ok=True)
    for name, (url, digest) in SOURCES.items():
        dst = os.path.join(cache, name)
        if not os.path.exists(dst):
            print("download", url)
            tmp = dst + ".part"
            with urllib.request.urlopen(url, timeout=60) as r, open(tmp, "wb") as f:
                f.write(r.read())
            os.replace(tmp, dst)
    return cache


def verify(src):
    for name, (_, digest) in SOURCES.items():
        path = os.path.join(src, name)
        if not os.path.exists(path):
            sys.exit(f"missing source file {path}")
        if digest and sha256(path) != digest:
            sys.exit(f"{path}: sha256 mismatch (expected {digest})")
        if name in LICENCE_FIRST_LINE:
            with open(path, encoding="utf-8") as f:
                if not f.readline().startswith(LICENCE_FIRST_LINE[name]):
                    sys.exit(f"{path}: unexpected licence text")


def save_woff2(font, name):
    font.recalcTimestamp = False  # keep the source timestamp: reproducible output
    font.flavor = "woff2"
    out = os.path.join(FONTS_OUT, name)
    font.save(out)
    print(f"wrote {out} ({os.path.getsize(out)} bytes)")


def subset(font, unicodes):
    opts = Options()
    opts.layout_features = ["kern"]
    opts.name_IDs = [0, 1, 2, 3, 4, 5, 6]
    opts.notdef_outline = True
    opts.hinting = False
    sub = Subsetter(opts)
    sub.populate(unicodes=unicodes)
    sub.subset(font)
    return font


def build_console_fonts(src):
    glass = TTFont(os.path.join(src, "Glass_TTY_VT220.ttf"))
    save_woff2(glass, "glass-tty-vt220.woff2")
    vt323 = subset(TTFont(os.path.join(src, "VT323-Regular.ttf")), CONSOLE_UNICODES)
    save_woff2(vt323, "vt323.woff2")


def build_panel_font(src):
    """Returns the per-label metrics gen-geometry.mjs needs to turn measured ink
    widths into SVG textLength values."""
    path = os.path.join(src, "Archivo[wdth,wght].ttf")
    static = instantiateVariableFont(TTFont(path), {"wght": 600, "wdth": 85}, updateFontNames=False)
    subset(static, [ord(c) for c in PANEL_TEXT])
    metrics = label_metrics(static)
    save_woff2(static, "archivo-panel.woff2")
    medium = instantiateVariableFont(TTFont(path), {"wght": 500, "wdth": 85}, updateFontNames=False)
    subset(medium, [ord(c) for c in "0123456789 "])
    save_woff2(medium, "archivo-panel-500.woff2")
    return metrics


def label_metrics(font):
    """For every label of the r4 element table: advance width including kerning
    (adv), left/right side bearings of the ink (lsb, rsb) and ink width, in
    font units, plus the font's cap-height ratio."""
    with open(ELEMENTS, encoding="utf-8") as f:
        table = json.load(f)
    texts = sorted({e["id"][len('label."'):-1] for e in table["elements"] if e["kind"] == "label"})
    cmap = font.getBestCmap()
    gs = font.getGlyphSet()
    hmtx = font["hmtx"]
    kern = pair_kerning(font)
    out = {}
    for text in texts:
        pen_x, prev, ink0, ink1 = 0, None, None, None
        for ch in text:
            g = cmap[ord(ch)]
            if prev is not None:
                pen_x += kern.get((prev, g), 0)
            bp = BoundsPen(gs)
            gs[g].draw(bp)
            if bp.bounds:
                a, _, b, _ = bp.bounds
                ink0 = pen_x + a if ink0 is None else min(ink0, pen_x + a)
                ink1 = pen_x + b if ink1 is None else max(ink1, pen_x + b)
            pen_x += hmtx[g][0]
            prev = g
        out[text] = {"adv": pen_x, "lsb": round(ink0, 1), "rsb": round(pen_x - ink1, 1), "ink": round(ink1 - ink0, 1)}
    bp = BoundsPen(gs)
    gs[cmap[ord("H")]].draw(bp)
    cap = bp.bounds[3] / font["head"].unitsPerEm
    return {"capHeight": round(cap, 4), "labels": out}


def text_path(font, text, tracking_em, x0, baseline, size_mm, fit_width=None, precision=2):
    """SVG path data for text laid out with advances + tracking (no shaping),
    scaled to size_mm per em; optionally squeezed horizontally to fit_width mm
    of ink (left ink edge at x0)."""
    cmap = font.getBestCmap()
    gs = font.getGlyphSet()
    upm = font["head"].unitsPerEm
    hmtx = font["hmtx"]
    kern = pair_kerning(font)
    pen_x = 0.0
    placed = []
    prev = None
    for ch in text:
        g = cmap.get(ord(ch))
        if g is None:
            raise SystemExit(f"glyph for {ch!r} missing")
        if prev is not None:
            pen_x += kern.get((prev, g), 0)
        placed.append((g, pen_x))
        pen_x += hmtx[g][0] + tracking_em * upm
        prev = g
    # ink bounds in font units
    bx0, bx1 = None, None
    for g, x in placed:
        bp = BoundsPen(gs)
        gs[g].draw(bp)
        if bp.bounds:
            a, _, b, _ = bp.bounds
            bx0 = x + a if bx0 is None else min(bx0, x + a)
            bx1 = x + b if bx1 is None else max(bx1, x + b)
    s = size_mm / upm
    sx = s if fit_width is None else fit_width / (bx1 - bx0)
    parts = []
    for g, x in placed:
        pen = SVGPathPen(gs, ntos=lambda v: f"{round(v, precision):g}")
        # font y is up; SVG y is down
        tp = TransformPen(pen, (sx, 0, 0, -s, x0 + (x - bx0) * sx, baseline))
        gs[g].draw(tp)
        d = pen.getCommands()
        if d:
            parts.append(d)
    return " ".join(parts), {"x0": x0, "x1": x0 + (bx1 - bx0) * sx, "scaleX": sx / s}


def pair_kerning(font):
    """Plain glyph-pair kerning from the GPOS 'kern' feature (format 1 pairs and
    class-based format 2), good enough for one line of text."""
    pairs = {}
    if "GPOS" not in font:
        return pairs
    gpos = font["GPOS"].table
    for lookup in gpos.LookupList.Lookup:
        for st in lookup.SubTable:
            if lookup.LookupType == 9:
                st = st.ExtSubTable
            if getattr(st, "LookupType", lookup.LookupType) != 2:
                continue
            cov = st.Coverage.glyphs
            if st.Format == 1:
                for i, first in enumerate(cov):
                    for pvr in st.PairSet[i].PairValueRecord:
                        v = getattr(pvr.Value1, "XAdvance", 0) if pvr.Value1 else 0
                        if v:
                            pairs.setdefault((first, pvr.SecondGlyph), v)
            elif st.Format == 2:
                c1, c2 = st.ClassDef1.classDefs, st.ClassDef2.classDefs
                seconds = {}
                for g, c in c2.items():
                    seconds.setdefault(c, []).append(g)
                for first in cov:
                    rec = st.Class1Record[c1.get(first, 0)]
                    for ci, r2 in enumerate(rec.Class2Record):
                        v = getattr(r2.Value1, "XAdvance", 0) if r2.Value1 else 0
                        if v and ci in seconds:
                            for second in seconds[ci]:
                                pairs.setdefault((first, second), v)
    return pairs


def build_art(src, panel_metrics):
    """Bezel branding, positioned in the r4 element-table frame (mm):
    tagline ink x 65.2..279.4, baseline 9.9, x-height 3.8 mm; the seven
    "digital" boxes start at x 15.9 with pitch 6.63 mm, letters on baseline 10.7."""
    font = TTFont(os.path.join(src, "MontserratAlternates-Regular.ttf"))
    upm = font["head"].unitsPerEm
    xh = font["OS/2"].sxHeight / upm
    tag_d, tag_box = text_path(font, "digital equipment corporation • maynard, massachusetts",
                               -0.012, 65.2, 9.9, 3.8 / xh, fit_width=214.2)
    letters = []
    for i, ch in enumerate("digital"):
        bx = 15.9 + i * 6.63
        d, box = text_path(font, ch, 0, 0, 10.7, 5.0 / xh)
        w = box["x1"] - box["x0"]
        # centre the letter's ink in its 6.1 mm box
        letters.append({"ch": ch, "dx": round(bx + 3.05 - w / 2, 3), "d": d})
    art = {
        "source": "Montserrat Alternates Regular (OFL), outlines converted by web/dev/build-fonts.py",
        "labelCapHeight": panel_metrics["capHeight"],
        "labelMetrics": panel_metrics["labels"],
        "tagline": {"d": tag_d, "x0": round(tag_box["x0"], 2), "x1": round(tag_box["x1"], 2)},
        "digital": {"boxes": [{"x": round(15.9 + i * 6.63, 2), "y": 0.6, "w": 6.1, "h": 12.6} for i in range(7)],
                    "letters": letters},
    }
    with open(ART_OUT, "w", encoding="utf-8") as f:
        json.dump(art, f, indent=1)
        f.write("\n")
    print(f"wrote {ART_OUT} ({os.path.getsize(ART_OUT)} bytes)")


def copy_licences(src):
    for name, out in (("LICENSE-glasstty", "LICENSE-glass-tty-vt220.txt"),
                      ("OFL-archivo.txt", "LICENSE-archivo-OFL.txt"),
                      ("OFL-vt323.txt", "LICENSE-vt323-OFL.txt"),
                      ("OFL-montserratalternates.txt", "LICENSE-montserrat-alternates-OFL.txt")):
        with open(os.path.join(src, name), encoding="utf-8") as f:
            text = f.read()
        with open(os.path.join(FONTS_OUT, out), "w", encoding="utf-8") as f:
            f.write(text)
        print("wrote", os.path.join(FONTS_OUT, out))


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    g = ap.add_mutually_exclusive_group(required=True)
    g.add_argument("--src", help="directory that already holds the source files (names as in SOURCES)")
    g.add_argument("--download", action="store_true", help="download the pinned sources")
    ap.add_argument("--cache", help="download directory (default: a new temporary directory)")
    args = ap.parse_args()
    src = args.src or fetch(args.cache or tempfile.mkdtemp(prefix="pdp11-fonts-"))
    verify(src)
    os.makedirs(FONTS_OUT, exist_ok=True)
    build_console_fonts(src)
    metrics = build_panel_font(src)
    build_art(src, metrics)
    copy_licences(src)


if __name__ == "__main__":
    main()
