"""Character-art generator: a small feed-forward network, for infai's half of the homepage.

Nodes are shaded spheres, connections are dotted one-pixel lines. Each
character cell holds 2x2 pixels drawn with Unicode quarter blocks; Geist Mono
draws a full block 1.3em tall and 0.6em wide, so with line-height 1.3em the
blocks tile and a cell is ASPECT times taller than it is wide.

Usage: python3 network.py         preview in the terminal
       python3 network.py html    the markup for the homepage
"""
import html
import math
import sys

COLS, ROWS = 100, 20
ASPECT = 1.3 / 0.6
LAYERS = [3, 4, 2]       # inputs, one hidden layer, outputs
RADIUS = 3.8
MARGIN_X, MARGIN_Y = 10.0, 1.5

BAYER = [[0, 8, 2, 10], [12, 4, 14, 6], [3, 11, 1, 9], [15, 7, 13, 5]]
# quarter-block glyph for bits TL=1, TR=2, BL=4, BR=8
QUAD = ' ▘▝▀▖▌▞▛▗▚▐▜▄▙▟█'
_l = math.sqrt(0.5 ** 2 + 0.7 ** 2 + 0.6 ** 2)
LIGHT = (-0.5 / _l, -0.7 / _l, 0.6 / _l)

H = ROWS * ASPECT
NODES = []
for i, n in enumerate(LAYERS):
    x = MARGIN_X + i * (COLS - 2 * MARGIN_X) / (len(LAYERS) - 1)
    span = H - 2 * MARGIN_Y
    NODES.append([(x, MARGIN_Y + (j + 0.5) * span / n) for j in range(n)])
EDGES = [(a, b) for left, right in zip(NODES, NODES[1:]) for a in left for b in right]


def node_density(x, y):
    best = None
    for cx, cy in (p for layer in NODES for p in layer):
        rho = math.hypot(x - cx, y - cy)
        if best is None or rho < best[0]:
            best = (rho, cx, cy)
    rho, cx, cy = best
    if rho > RADIUS + 2.5:
        return None
    if rho > RADIUS:
        return 0.12 * (1 - (rho - RADIUS) / 2.5) ** 2   # dust around the node
    if rho > RADIUS - 0.9:
        return 1.0                                      # crisp rim
    nx, ny = (x - cx) / RADIUS, (y - cy) / RADIUS
    nz = math.sqrt(max(0.0, 1 - nx * nx - ny * ny))
    lum = max(0.0, nx * LIGHT[0] + ny * LIGHT[1] + nz * LIGHT[2])
    return 0.15 + 0.85 * (1 - lum) ** 1.3


def on_edge(x, y):
    for (x0, y0), (x1, y1) in EDGES:
        dx, dy = x1 - x0, y1 - y0
        L = math.hypot(dx, dy)
        t = ((x - x0) * dx + (y - y0) * dy) / L       # distance along the edge
        if t < RADIUS + 0.9 or t > L - RADIUS - 0.9:
            continue
        if abs((x - x0) * dy - (y - y0) * dx) / L < 0.56:   # about half a pixel: one-pixel line
            return True
    return False


def render():
    rows = []
    for r in range(ROWS):
        line = []
        for c in range(COLS):
            bits = 0
            for i, (qx, qy) in enumerate(((0, 0), (1, 0), (0, 1), (1, 1))):
                px, py = 2 * c + qx, 2 * r + qy
                x, y = (px + 0.5) / 2, (py + 0.5) / 2 * ASPECT
                v = node_density(x, y)
                if v is not None:
                    lit = v > (BAYER[py % 4][px % 4] + 0.5) / 16
                else:
                    lit = (px + py) % 3 == 0 and on_edge(x, y)   # dotted connections
                if lit:
                    bits |= 1 << i
            line.append(QUAD[bits])
        rows.append(''.join(line).rstrip())
    return rows


if __name__ == '__main__':
    rows = render()
    if len(sys.argv) > 1 and sys.argv[1] == 'html':
        # HTML drops one newline straight after <pre>. The second copy glows and is
        # revealed by a moving mask: the forward pass.
        body = html.escape('\n'.join(rows), quote=False)
        print('      <div class="net" aria-hidden="true"><pre class="net-base">\n' + body
              + '</pre><pre class="net-pulse">\n' + body + '</pre></div>')
    else:
        print('\n'.join(rows))
