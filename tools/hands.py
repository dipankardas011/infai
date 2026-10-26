"""Character-art generator: your hand reaching down to an agent's robot hand.

Each hand is a forearm, a palm and four fingers plus a thumb. The human hand
is organic: fingers blend into the palm but never into each other, so the gaps
between them survive. The robot hand is mechanical: square-cut finger segments
with round joints, a plate palm, a wrist joint and a seamed forearm, all meeting
at hard edges. Each character cell holds 2x2 pixels drawn with Unicode quarter
blocks, so the result is a dithered pixel image made only of text. Geist Mono
draws a full block 1.3em tall and 0.6em wide, so with line-height 1.3em the
blocks tile and a cell is ASPECT times taller than it is wide.

Usage: python3 hands.py         preview in the terminal
       python3 hands.py html    the markup for the infaiw hero
"""
import html
import math
import sys

COLS, ROWS = 150, 40
ASPECT = 1.3 / 0.6
ZOOM, FOCUS = 1.4, (80.0, 42.0)   # magnify around the gap between the fingertips


def norm(x, y):
    n = math.hypot(x, y)
    return x / n, y / n


def hand(kind, wrist_from, wrist, palm_c, toward, palm_len, palm_wid, fingers, thumb):
    """A hand as primitives. toward: direction from palm to fingers."""
    d = norm(*toward)
    return {
        'kind': kind,
        'arm': [(wrist_from[0], wrist_from[1], 6.6, wrist[0], wrist[1], 5.2)],
        'palm': (palm_c, d, palm_len, palm_wid),
        'digits': fingers + [thumb],
    }


# capsules are (x0, y0, r0, x1, y1, r1); y grows downward
# you: palm down, pointing, coming from the upper right
YOU = hand(
    kind='flesh',
    wrist_from=(166, 2), wrist=(124, 25), palm_c=(115.5, 30.5), toward=(-0.912, 0.41),
    palm_len=6.5, palm_wid=5.8,
    fingers=[
        [(108.0, 28.6, 2.4, 98.0, 33.2, 2.2), (98.0, 33.2, 2.2, 86.5, 38.0, 1.9)],                       # index, pointing
        [(109.0, 31.4, 2.4, 101.5, 35.2, 2.2), (101.5, 35.2, 2.2, 99.5, 40.0, 2.0), (99.5, 40.0, 2.0, 102.5, 42.8, 1.8)],  # middle, curled
        [(110.0, 34.0, 2.3, 104.0, 38.0, 2.1), (104.0, 38.0, 2.1, 103.2, 42.4, 1.9), (103.2, 42.4, 1.9, 106.0, 44.8, 1.7)],  # ring, curled
        [(111.0, 36.4, 2.0, 106.8, 39.8, 1.8), (106.8, 39.8, 1.8, 107.0, 43.4, 1.6), (107.0, 43.4, 1.6, 109.2, 45.2, 1.5)],  # little, curled
    ],
    thumb=[(119.0, 36.5, 2.6, 112.5, 40.2, 2.3), (112.5, 40.2, 2.3, 107.6, 40.8, 2.0)],
)
# the agent: palm up, reaching from the lower left
AGENT = hand(
    kind='robot',
    wrist_from=(-10, 80), wrist=(35, 58.5), palm_c=(42.0, 55.0), toward=(0.94, -0.34),
    palm_len=6.0, palm_wid=5.4,
    fingers=[
        [(46.4, 49.6, 2.3, 56.0, 46.5, 2.1), (56.0, 46.5, 2.1, 65.0, 46.3, 1.9), (65.0, 46.3, 1.9, 73.0, 48.3, 1.6)],  # index, relaxed
        [(47.2, 51.9, 2.3, 55.0, 51.5, 2.1), (55.0, 51.5, 2.1, 61.0, 54.0, 1.9), (61.0, 54.0, 1.9, 63.0, 58.0, 1.7)],  # middle
        [(48.0, 54.1, 2.2, 54.5, 55.0, 2.0), (54.5, 55.0, 2.0, 58.5, 58.5, 1.8), (58.5, 58.5, 1.8, 59.0, 62.0, 1.6)],  # ring
        [(48.8, 56.2, 1.9, 53.0, 58.0, 1.7), (53.0, 58.0, 1.7, 55.0, 61.0, 1.6), (55.0, 61.0, 1.6, 55.0, 64.0, 1.4)],  # little
    ],
    thumb=[(40.0, 51.0, 2.6, 46.0, 45.5, 2.3), (46.0, 45.5, 2.3, 51.0, 43.0, 1.9)],
)

BAYER = [[0, 8, 2, 10], [12, 4, 14, 6], [3, 11, 1, 9], [15, 7, 13, 5]]
# quarter-block glyph for bits TL=1, TR=2, BL=4, BR=8
QUAD = ' ▘▝▀▖▌▞▛▗▚▐▜▄▙▟█'
_l = math.sqrt(0.5 ** 2 + 0.7 ** 2 + 0.6 ** 2)
LIGHT = (-0.5 / _l, -0.7 / _l, 0.6 / _l)    # from the upper left, towards the viewer


def capsule(px, py, s):
    x0, y0, r0, x1, y1, r1 = s
    dx, dy = x1 - x0, y1 - y0
    t = max(0.0, min(1.0, ((px - x0) * dx + (py - y0) * dy) / (dx * dx + dy * dy)))
    cx, cy = x0 + t * dx, y0 + t * dy
    return math.hypot(px - cx, py - cy) - (r0 + (r1 - r0) * t)


def palm_box(px, py, palm, rr=2.2):
    (cx, cy), (dx, dy), a, b = palm
    vx, vy = px - cx, py - cy
    u, w = vx * dx + vy * dy, vx * -dy + vy * dx
    qx, qy = abs(u) - (a - rr), abs(w) - (b - rr)
    return math.hypot(max(qx, 0.0), max(qy, 0.0)) + min(max(qx, qy), 0.0) - rr


def smin(a, b, k):
    h = max(0.0, min(1.0, 0.5 + 0.5 * (b - a) / k))
    return b + (a - b) * h - k * h * (1 - h)


def plate(px, py, x0, y0, x1, y1, w, gap, rr=0.7):
    """A square-cut segment from (x0, y0) to (x1, y1), shortened by gap."""
    L = math.hypot(x1 - x0, y1 - y0)
    return palm_box(px, py, (((x0 + x1) / 2, (y0 + y1) / 2), norm(x1 - x0, y1 - y0), L / 2 - gap / 2, w), rr)


def sdf_robot(h, x, y):
    x0, y0, r0, x1, y1, r1 = h['arm'][0]
    ax, ay = norm(x1 - x0, y1 - y0)
    ex, ey = x1 - 4.6 * ax, y1 - 4.6 * ay              # forearm stops short of the wrist joint
    d = plate(x, y, x0, y0, ex, ey, 4.8, 0, rr=1.0)
    u = (ex - x) * ax + (ey - y) * ay                   # distance back from the forearm's end
    for t in (5.0, 11.0, 17.0):                        # panel seams across the forearm
        d = max(d, -(abs(u - t) - 0.45))
    d = min(d, math.hypot(x - x1, y - y1) - 3.4)       # wrist joint
    (pcx, pcy), (pdx, pdy), a, b = h['palm']
    d = min(d, palm_box(x, y, ((pcx + 1.2 * pdx, pcy + 1.2 * pdy), (pdx, pdy), a - 1.2, b), rr=0.8))
    for digit in h['digits']:
        for sx0, sy0, sr0, sx1, sy1, sr1 in digit:
            w = (sr0 + sr1) / 2 * 0.9
            d = min(d, plate(x, y, sx0, sy0, sx1, sy1, w, 1.4))
            d = min(d, math.hypot(x - sx0, y - sy0) - w * 0.85)   # joint
    return d


def sdf_hand(h, x, y):
    if h['kind'] == 'robot':
        return sdf_robot(h, x, y)
    body = smin(capsule(x, y, h['arm'][0]), palm_box(x, y, h['palm']), 3.0)
    d = body
    for digit in h['digits']:
        f = min(capsule(x, y, s) for s in digit)
        d = min(d, smin(body, f, 1.2))      # blend into the palm, never into a neighbour
    return d


def sdf(h, x, y):
    """Signed distance in canvas units; canvas centre maps to FOCUS."""
    sx = FOCUS[0] + (x - COLS / 2) / ZOOM
    sy = FOCUS[1] + (y - ROWS * ASPECT / 2) / ZOOM
    return sdf_hand(h, sx, sy) * ZOOM


def density(h, x, y):
    """Ink density 0..1 at a canvas point: shaded inside, a fading halo outside."""
    d = sdf(h, x, y)
    if d > 4.0:
        return 0.0
    if d > 0:
        return 0.16 * (1 - d / 4.0) ** 2           # dust around the silhouette
    if d > -1.1:
        return 1.0                                  # crisp contour, at least one pixel tall
    e = 0.3
    gx = sdf(h, x + e, y) - sdf(h, x - e, y)
    gy = sdf(h, x, y + e) - sdf(h, x, y - e)
    n = math.hypot(gx, gy) or 1.0
    nz = min(1.0, -d / 3.5)                         # deeper inside faces the viewer
    s = math.sqrt(max(0.0, 1 - nz * nz))
    lum = max(0.0, gx / n * s * LIGHT[0] + gy / n * s * LIGHT[1] + nz * LIGHT[2])
    v = 0.2 + 0.8 * (1 - lum) ** 1.2
    if h['kind'] == 'robot':
        v = 0.3 if v < 0.45 else 0.58 if v < 0.72 else 0.9   # flat metal steps
    return v


def on(pattern, v, px, py):
    if pattern == 'checker':
        return v > (BAYER[py % 4][px % 4] + 0.5) / 16
    # scanlines: whole pixel columns switch on as the ink gets darker
    return v > ((px * 3) % 4 + 0.5) / 4 if v < 0.97 else True


def render(h, pattern):
    rows = []
    for r in range(ROWS):
        line = []
        for c in range(COLS):
            bits = 0
            for i, (qx, qy) in enumerate(((0, 0), (1, 0), (0, 1), (1, 1))):
                px, py = 2 * c + qx, 2 * r + qy
                v = density(h, (px + 0.5) / 2, (py + 0.5) / 2 * ASPECT)
                if v and on(pattern, v, px, py):
                    bits |= 1 << i
            line.append(QUAD[bits])
        rows.append(''.join(line).rstrip())
    return rows


if __name__ == '__main__':
    agent, you = render(AGENT, 'scanlines'), render(YOU, 'checker')
    if len(sys.argv) > 1 and sys.argv[1] == 'html':
        # HTML drops one newline straight after <pre>; the extra one keeps both layers row-aligned
        pre = lambda cls, rows: f'<pre class="hand {cls}">\n' + html.escape('\n'.join(rows), quote=False) + '</pre>'
        print('    <div class="hands" aria-hidden="true">' + pre('hand-agent', agent) + pre('hand-you', you) + '</div>')
    else:
        for h, m in zip(agent, you):
            print(''.join(a if a != ' ' else b for a, b in zip(h.ljust(COLS), m.ljust(COLS))).rstrip())
