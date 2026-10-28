"""Character-art generator: a chip on a board, for infai's half of the homepage.

A package with pins on all four sides, an inner die split into cores, and
board traces that run in from both edges with 45-degree bends and end in vias.
Each character cell holds 2x2 pixels drawn with Unicode quarter blocks; Geist
Mono draws a full block 1.3em tall and 0.6em wide, so with line-height 1.3em
the blocks tile and a cell is ASPECT times taller than it is wide.

Usage: python3 chip.py         preview in the terminal
       python3 chip.py html    the markup for the homepage
"""
import html
import math
import sys

COLS, ROWS = 100, 20
ASPECT = 1.3 / 0.6
H = ROWS * ASPECT
CX, CY = COLS / 2, H / 2
PKG = (15.0, 13.0)          # package half-width, half-height
DIE = (9.5, 7.6)
CORES = (3, 2)              # columns x rows of cores on the die
PIN_LEN = 2.6

BAYER = [[0, 8, 2, 10], [12, 4, 14, 6], [3, 11, 1, 9], [15, 7, 13, 5]]
# quarter-block glyph for bits TL=1, TR=2, BL=4, BR=8
QUAD = ' ▘▝▀▖▌▞▛▗▚▐▜▄▙▟█'


def box(x, y, cx, cy, a, b, rr=0.0):
    """Signed distance to an axis-aligned rounded box."""
    qx, qy = abs(x - cx) - (a - rr), abs(y - cy) - (b - rr)
    return math.hypot(max(qx, 0.0), max(qy, 0.0)) + min(max(qx, qy), 0.0) - rr


def segment(x, y, x0, y0, x1, y1):
    dx, dy = x1 - x0, y1 - y0
    t = max(0.0, min(1.0, ((x - x0) * dx + (y - y0) * dy) / (dx * dx + dy * dy)))
    return math.hypot(x - x0 - t * dx, y - y0 - t * dy)


SIDE_PINS = [CY + (i - 4) * 2.6 for i in range(9)]
TOP_PINS = [CX + (i - 4) * 3.0 for i in range(9)]


def trace(y_pin, y_edge, side):
    """Polyline from a via near the board edge to a side pin."""
    pin_x = CX + side * (PKG[0] + PIN_LEN)
    edge_x = CX + side * 46.0
    bend = abs(y_edge - y_pin)
    knee_x = pin_x + side * 5.0
    pts = [(edge_x, y_edge), (knee_x + side * bend, y_edge), (knee_x, y_pin), (pin_x, y_pin)]
    return [p for i, p in enumerate(pts) if i == 0 or p != pts[i - 1]]   # a straight trace has no bend


# which pins get a trace, and where on the board edge it starts
TRACES = [trace(SIDE_PINS[i], y, s) for s, picks in (
    (-1, [(1, CY - 13.5), (2, CY - 7.0), (4, CY), (6, CY + 7.0), (7, CY + 13.5)]),
    (+1, [(0, CY - 15.0), (3, CY - 4.5), (5, CY + 4.5), (8, CY + 15.0)]),
) for i, y in picks]
VIAS = [t[0] for t in TRACES]


def density(x, y):
    # package, die and cores
    d_pkg = box(x, y, CX, CY, *PKG, rr=1.6)
    if d_pkg <= 0:
        if d_pkg > -1.0:
            return 1.0                                   # package outline
        if math.hypot(x - (CX - PKG[0] + 3.2), y - (CY - PKG[1] + 3.0)) < 1.3:
            return 0.0                                   # pin-1 dimple
        d_die = box(x, y, CX, CY, *DIE, rr=0.6)
        if d_die > 0:
            return 0.0                                   # the package body stays dark
        if d_die > -0.9:
            return 1.0
        cw, ch = 2 * (DIE[0] - 1.6) / CORES[0], 2 * (DIE[1] - 1.6) / CORES[1]
        i = min(CORES[0] - 1, int((x - (CX - DIE[0] + 1.6)) // cw))
        j = min(CORES[1] - 1, int((y - (CY - DIE[1] + 1.6)) // ch))
        ccx = CX - DIE[0] + 1.6 + (i + 0.5) * cw
        ccy = CY - DIE[1] + 1.6 + (j + 0.5) * ch
        return 1.0 if box(x, y, ccx, ccy, cw / 2 - 0.8, ch / 2 - 0.9) <= 0 else 0.0
    # pins
    for py in SIDE_PINS:
        for s in (-1, 1):
            if box(x, y, CX + s * (PKG[0] + PIN_LEN / 2), py, PIN_LEN / 2, 0.55) <= 0:
                return 1.0
    for px in TOP_PINS:
        for s in (-1, 1):
            if box(x, y, px, CY + s * (PKG[1] + PIN_LEN / 2), 0.55, PIN_LEN / 2) <= 0:
                return 1.0
    # vias: rings at the start of each trace
    for vx, vy in VIAS:
        r = math.hypot(x - vx, y - vy)
        if r < 1.9:
            return 1.0 if r > 0.8 else 0.0
    # traces: one-pixel lines
    for t in TRACES:
        for (x0, y0), (x1, y1) in zip(t, t[1:]):
            if segment(x, y, x0, y0, x1, y1) < 0.5:
                return 1.0
    return 0.0


def render():
    rows = []
    for r in range(ROWS):
        line = []
        for c in range(COLS):
            bits = 0
            for i, (qx, qy) in enumerate(((0, 0), (1, 0), (0, 1), (1, 1))):
                px, py = 2 * c + qx, 2 * r + qy
                v = density((px + 0.5) / 2, (py + 0.5) / 2 * ASPECT)
                if v > (BAYER[py % 4][px % 4] + 0.5) / 16:
                    bits |= 1 << i
            line.append(QUAD[bits])
        rows.append(''.join(line).rstrip())
    return rows


if __name__ == '__main__':
    rows = render()
    if len(sys.argv) > 1 and sys.argv[1] == 'html':
        # HTML drops one newline straight after <pre>; the extra one keeps both layers row-aligned
        body = html.escape('\n'.join(rows), quote=False)
        print('<div class="chip" aria-hidden="true"><pre class="chip-base">\n' + body
              + '</pre><pre class="chip-pulse">\n' + body + '</pre></div>')
    else:
        print('\n'.join(rows))
