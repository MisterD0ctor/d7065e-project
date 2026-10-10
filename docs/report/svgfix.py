#!/usr/bin/env python3
"""Make a d2 SVG render correctly with librsvg (ImageMagick, rsvg-convert).

Two things go wrong otherwise.

1. d2 hides the part of a line that runs under a label with an SVG mask.
   librsvg ignores a mask on a straight horizontal or vertical line (its
   bounding box has no area), so the line is drawn through the text. Here the
   mask's rectangles are painted white on top of the lines instead, and every
   text is moved on top of them.

2. d2 embeds its font in the SVG, and librsvg can't load an embedded font. It
   falls back to a system font with other widths, so text no longer fits what
   d2 measured. With --font NAME, the file is pointed at the installed font
   NAME; give d2 the same font's files (--font-regular and so on) and what is
   measured is what is drawn.

    svgfix.py [--font "Liberation Sans"] in.svg out.svg
"""
import re
import sys

# Without --font the fallback font is usually a little wider than d2's, so the
# white backing is made wider than d2's rectangle by this share on each side.
WIDEN_FALLBACK = 0.12
WIDEN_SAME_FONT = 0.02

STYLE = {"regular": "", "bold": "font-weight:bold;", "semibold": "font-weight:600;", "italic": "font-style:italic;"}


def widen(rect: str, share: float) -> str:
    x = re.search(r'\bx="([-0-9.]+)"', rect)
    w = re.search(r'\bwidth="([-0-9.]+)"', rect)
    if not x or not w:
        return rect
    x0, w0 = float(x.group(1)), float(w.group(1))
    rect = rect.replace(x.group(0), 'x="%.1f"' % (x0 - share * w0), 1)
    return rect.replace(w.group(0), 'width="%.1f"' % (w0 * (1 + 2 * share)), 1)


def use_font(svg: str, name: str) -> str:
    # Drop the embedded fonts, then name the installed one where they were used.
    svg = re.sub(r"@font-face\s*\{.*?\}", "", svg, flags=re.S)

    def family(m: re.Match) -> str:
        return 'font-family:"%s";%s' % (name, STYLE.get(m.group(1), ""))

    return re.sub(r'font-family:\s*"?d2-\d+-font-(\w+)"?;?', family, svg)


def fix(svg: str, font: str = "") -> str:
    if font:
        svg = use_font(svg, font)
    mask = re.search(r"<mask\b.*?</mask>", svg, re.S)
    if not mask:
        return svg
    share = WIDEN_SAME_FONT if font else WIDEN_FALLBACK
    # The first rectangle is the mask's white background; the rest are labels.
    rects = re.findall(r"<rect\b[^>]*\bfill=\"black\"[^>]*>(?:</rect>)?", mask.group(0))
    backing = "".join(widen(r, share).replace('fill="black"', 'fill="#ffffff"') for r in rects)

    texts = re.findall(r"<text\b.*?</text>", svg, re.S)
    body = re.sub(r"<text\b.*?</text>", "", svg, flags=re.S)
    body = re.sub(r'\smask="url\(#[^)]*\)"', "", body)
    at = body.index("<mask")
    return body[:at] + "<g>" + backing + "</g><g>" + "".join(texts) + "</g>" + body[at:]


if __name__ == "__main__":
    args = sys.argv[1:]
    font = ""
    if args[:1] == ["--font"]:
        font, args = args[1], args[2:]
    src, dst = args
    with open(src, encoding="utf-8") as f:
        out = fix(f.read(), font)
    with open(dst, "w", encoding="utf-8") as f:
        f.write(out)
