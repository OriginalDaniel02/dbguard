#!/usr/bin/env python3
"""Renders real `dbguard` output as a terminal-style SVG for the README.

    python scripts/render-demo.py output.txt docs/assets/cli-output.svg

output.txt is the captured terminal text: the first line is the command (without the prompt),
the rest is what the tool printed. Nothing is invented: regenerate it from a real run.
"""
import html
import re
import sys

FONT = "ui-monospace, SFMono-Regular, Menlo, Consolas, 'Liberation Mono', monospace"
BG, BAR, FG, DIM = "#0d1117", "#161b22", "#c9d1d9", "#8b949e"
TAGS = {  # risk tag -> colour
    "[HIGH]": "#ff7b72",
    "[MEDIUM-HIGH]": "#ffa657",
    "[MEDIUM]": "#e3b341",
    "[LOW]": "#e3b341",
    "[ACKNOWLEDGED]": "#7ee787",
}
CHAR_W, LINE_H, PAD_X, TOP = 8.45, 21, 22, 58
PRE = ' xml:space="preserve" style="white-space:pre"'  # keep leading spaces


def colorize(line: str):
    """Yield (text, colour, bold) segments for one output line."""
    for tag, colour in TAGS.items():
        if tag in line:
            before, after = line.split(tag, 1)
            yield before, FG, False
            yield tag, colour, True
            yield after, FG, False
            return
    m = re.match(r"^(\s+)(safer:)(.*)$", line)
    if m:
        yield m.group(1), FG, False
        yield m.group(2), "#7ee787", True
        yield m.group(3), FG, False
        return
    m = re.match(r"^(\s+)(override reason:)(.*)$", line)
    if m:
        yield m.group(1), FG, False
        yield m.group(2), DIM, True
        yield m.group(3), DIM, False
        return
    if re.match(r"^dbguard: \d+ blocking", line):
        yield line, "#ff7b72", True
        return
    if re.match(r"^dbguard: no blocking", line):
        yield line, "#7ee787", True
        return
    yield line, FG, False


def main(src: str, dst: str) -> None:
    lines = open(src, encoding="utf-8").read().rstrip("\n").split("\n")
    cmd, out = lines[0], lines[1:]
    longest = max(len(l) for l in [("$ " + cmd)] + out)
    width = int(PAD_X * 2 + longest * CHAR_W)
    height = TOP + LINE_H * (len(out) + 1) + 26
    parts = [
        f'<svg xmlns="http://www.w3.org/2000/svg" width="{width}" height="{height}" viewBox="0 0 {width} {height}" role="img" aria-label="dbguard check output">',
        f'<rect width="{width}" height="{height}" rx="10" fill="{BG}"/>',
        f'<path d="M0 10a10 10 0 0 1 10-10h{width-20}a10 10 0 0 1 10 10v26H0z" fill="{BAR}"/>',
        '<circle cx="22" cy="18" r="6" fill="#ff5f56"/><circle cx="42" cy="18" r="6" fill="#ffbd2e"/><circle cx="62" cy="18" r="6" fill="#27c93f"/>',
        f'<g font-family="{FONT}" font-size="14" xml:space="preserve">',
    ]
    y = TOP
    parts.append(
        f'<text x="{PAD_X}" y="{y}"{PRE}><tspan fill="#7ee787">$</tspan><tspan fill="{FG}"> {html.escape(cmd)}</tspan></text>'
    )
    for line in out:
        y += LINE_H
        bold = ' font-weight="bold"'
        spans = "".join(
            f'<tspan fill="{c}"{bold if b else ""}>{html.escape(t)}</tspan>'
            for t, c, b in colorize(line)
            if t
        )
        parts.append(f'<text x="{PAD_X}" y="{y}"{PRE}>{spans}</text>')
    parts.append("</g></svg>")
    open(dst, "w", encoding="utf-8").write("\n".join(parts) + "\n")
    print(f"wrote {dst} ({width}x{height})")


if __name__ == "__main__":
    if len(sys.argv) != 3:
        sys.exit(__doc__)
    main(sys.argv[1], sys.argv[2])
