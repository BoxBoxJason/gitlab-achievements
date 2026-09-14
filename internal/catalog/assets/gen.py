#!/usr/bin/env python3
"""Regenerate the 8-bit achievement avatars in this directory.

Run with `python3 gen.py` after editing an icon function; it rewrites every
PNG in place. Requires Pillow. The PNGs are the build input (catalog.go
embeds assets/*.png), so they are committed and this script only has to be
run when the art changes.

Art is authored on a 32x32 grid and emitted at 3x that, 96x96, by exact
pixel tripling. Every icon gets a 1px black keyline from outline() so the
set reads consistently on light and dark themes.

The 3x is not about detail, which a nearest-neighbour scale cannot add. It
is about what the consumer does with the file. GitLab renders an
achievement avatar in a GlAvatar at size 48 (`gl-avatar-s48`, shape rect)
and hands the browser the original file to fit, with no `?width=` variant
and no image-rendering hint. So the browser decides, and it interpolates
smoothly:

  - emitting 32 means a 1.5x *upscale* to 48. Half the source pixels land
    on two device pixels and half on one, and bilinear filtering smears the
    difference. This is what made the icons look soft.
  - emitting 64 is worse, not better: 48/64 is a 0.75 downscale, so the 1px
    keyline resamples to 1.5px and edges thin out unevenly.
  - emitting 96 is an exact 2:1 downscale, and 96 is also GitLab's retina
    width for a 48px avatar (Avatarable::COMBINED_AVATAR_SIZES_RETINA), so
    on a HiDPI display the file maps one file pixel to one device pixel and
    every art pixel is exactly 3 device pixels square.

Keep SCALE an integer. A fractional one reintroduces precisely the uneven
pixel sizes the tripling exists to avoid.
"""
import os
import sys

from PIL import Image

OUT = os.path.dirname(os.path.abspath(__file__))

N = 32             # authoring grid: one array cell is one art pixel
SCALE = 3          # integer pixel multiplier; see the note in the docstring
OUT_PX = N * SCALE  # 96, what actually lands in the PNG

# Icons drawn by hand rather than by this script, kept byte-for-byte.
#
# Their functions below are what the script would draw instead, and are left
# in place as the fallback if the hand-made file is ever lost. main() skips
# them, because a plain `python3 gen.py` overwriting somebody's artwork with
# a generated approximation is not a thing a regeneration script should be
# able to do by accident.
#
# Neither is drawn on the 32 grid, so neither gets the 3x here. They were
# brought to the same output by hand instead:
#
#   fix.png  20x16 art, scaled 4x with a nearest-neighbour filter and
#            centred on a transparent 96x96 canvas. Square matters more
#            than size: GlAvatar is `object-fit: contain` in a square box,
#            so a non-square file renders letterboxed at a fractional
#            scale no resolution can make exact, while a square one lands
#            on 48 CSS / 96 device pixels dead on.
#   owl.png  64x64, left as drawn. GitLab scales it itself.
#
# To bring one fully into the pipeline, redraw it on the 32 grid and take
# its name out of this set.
HAND_AUTHORED = frozenset({'fix', 'owl'})

PALETTE = {
    'K': (27, 27, 31),      # keyline
    'W': (245, 245, 245),   # white
    'w': (201, 204, 212),   # white shade
    'S': (138, 148, 166),   # steel
    's': (90, 98, 114),     # steel shade
    'Y': (255, 210, 63),    # yellow
    'y': (224, 168, 0),     # yellow shade
    'O': (245, 138, 31),    # orange
    'o': (194, 98, 13),     # orange shade
    'R': (224, 43, 43),     # red
    'r': (168, 24, 24),     # red shade
    'G': (63, 185, 80),     # green
    'g': (35, 134, 54),     # green shade
    'B': (47, 124, 224),    # blue
    'b': (27, 79, 150),     # blue shade
    'P': (139, 92, 246),    # purple
    'p': (91, 52, 176),     # purple shade
    'T': (43, 182, 176),    # teal
    't': (24, 122, 118),    # teal shade
    'N': (139, 94, 60),     # brown
    'n': (94, 61, 38),      # brown shade
}


class Canvas:
    def __init__(self, w=N, h=N):
        self.w, self.h = w, h
        self.g = [['.'] * w for _ in range(h)]

    def set(self, x, y, c):
        # '.' erases, so the shape primitives double as hole punches.
        x, y = int(x), int(y)
        if 0 <= x < self.w and 0 <= y < self.h:
            self.g[y][x] = c

    def get(self, x, y):
        if 0 <= x < self.w and 0 <= y < self.h:
            return self.g[y][x]
        return '.'

    def rect(self, x, y, w, h, c):
        for j in range(int(y), int(y + h)):
            for i in range(int(x), int(x + w)):
                self.set(i, j, c)

    def disc(self, cx, cy, r, c):
        for y in range(self.h):
            for x in range(self.w):
                if (x + .5 - cx) ** 2 + (y + .5 - cy) ** 2 <= r * r:
                    self.set(x, y, c)

    def ring(self, cx, cy, r, t, c):
        for y in range(self.h):
            for x in range(self.w):
                d2 = (x + .5 - cx) ** 2 + (y + .5 - cy) ** 2
                if (r - t) ** 2 < d2 <= r * r:
                    self.set(x, y, c)

    def line(self, x0, y0, x1, y1, c, t=1):
        x0, y0, x1, y1 = int(x0), int(y0), int(x1), int(y1)
        dx, dy = abs(x1 - x0), abs(y1 - y0)
        sx = 1 if x0 < x1 else -1
        sy = 1 if y0 < y1 else -1
        err = dx - dy
        x, y = x0, y0
        while True:
            o = t // 2
            for j in range(t):
                for i in range(t):
                    self.set(x - o + i, y - o + j, c)
            if x == x1 and y == y1:
                break
            e2 = 2 * err
            if e2 > -dy:
                err -= dy
                x += sx
            if e2 < dx:
                err += dx
                y += sy

    def tri(self, x0, y0, x1, y1, x2, y2, c):
        """Filled triangle via barycentric sign test."""
        def sign(ax, ay, bx, by, cx, cy):
            return (ax - cx) * (by - cy) - (bx - cx) * (ay - cy)
        for y in range(self.h):
            for x in range(self.w):
                px, py = x + .5, y + .5
                d1 = sign(px, py, x0, y0, x1, y1)
                d2 = sign(px, py, x1, y1, x2, y2)
                d3 = sign(px, py, x2, y2, x0, y0)
                neg = (d1 < 0) or (d2 < 0) or (d3 < 0)
                pos = (d1 > 0) or (d2 > 0) or (d3 > 0)
                if not (neg and pos):
                    self.set(x, y, c)

    def replace(self, a, b):
        for y in range(self.h):
            for x in range(self.w):
                if self.g[y][x] == a:
                    self.g[y][x] = b

    def shade(self, src, dst, pred):
        """Recolour src->dst wherever pred(x, y) holds. Used for lighting."""
        for y in range(self.h):
            for x in range(self.w):
                if self.g[y][x] == src and pred(x, y):
                    self.g[y][x] = dst

    def outline(self, c='K'):
        """Add a keyline on transparent pixels touching any coloured pixel."""
        add = []
        for y in range(self.h):
            for x in range(self.w):
                if self.g[y][x] != '.':
                    continue
                for dx, dy in ((1, 0), (-1, 0), (0, 1), (0, -1)):
                    n = self.get(x + dx, y + dy)
                    if n not in ('.', c):
                        add.append((x, y))
                        break
        for x, y in add:
            self.g[y][x] = c
        return self

    def png(self, name):
        img = Image.new('RGBA', (self.w, self.h), (0, 0, 0, 0))
        px = img.load()
        for y in range(self.h):
            for x in range(self.w):
                ch = self.g[y][x]
                if ch != '.':
                    px[x, y] = PALETTE[ch] + (255,)
        if SCALE != 1:
            # NEAREST is the whole point: it copies each art pixel into a
            # SCALE x SCALE block and invents nothing. Any other filter
            # would blend the palette and soften the keyline, which is the
            # problem this is here to avoid rather than cause.
            img = img.resize((self.w * SCALE, self.h * SCALE), Image.NEAREST)
        path = os.path.join(OUT, name + '.png')
        img.save(path)
        return path

    def svg(self, name):
        parts = [f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {self.w} {self.h}" '
                 f'width="{self.w}" height="{self.h}" shape-rendering="crispEdges">']
        for y in range(self.h):
            x = 0
            row = self.g[y]
            while x < self.w:
                ch = row[x]
                if ch == '.':
                    x += 1
                    continue
                run = 1
                while x + run < self.w and row[x + run] == ch:
                    run += 1
                r, g, b = PALETTE[ch]
                parts.append(f'<rect x="{x}" y="{y}" width="{run}" height="1" '
                             f'fill="#{r:02x}{g:02x}{b:02x}"/>')
                x += run
        parts.append('</svg>')
        path = os.path.join(OUT, name + '.svg')
        open(path, 'w').write('\n'.join(parts))
        return path


ICONS = {}


def icon(fn):
    ICONS[fn.__name__] = fn
    return fn


# ---------------------------------------------------------------- GIT
#
# The four graph icons share one vocabulary so they read as a family: blue
# trunk and nodes, with the accent colour marking what makes each different
# (green for a branch that leaves, purple for one that comes back).

@icon
def commit(c):
    """Committer - a node on the trunk."""
    c.rect(1, 14, 30, 4, 'b')
    c.disc(16, 16, 8, 'B')
    c.shade('B', 'b', lambda x, y: x + y > 34)
    c.disc(16, 16, 4, 'W')
    c.shade('W', 'w', lambda x, y: x + y > 33)
    c.outline()


@icon
def push(c):
    """Product Shipping - a commit leaving for the remote."""
    c.rect(1, 22, 30, 4, 'b')
    c.disc(16, 24, 6.5, 'B')
    c.shade('B', 'b', lambda x, y: x + y > 42)
    c.disc(16, 24, 3, 'W')
    c.tri(16, 1, 4, 13, 28, 13, 'G')
    c.rect(11, 13, 10, 6, 'G')
    c.shade('G', 'g', lambda x, y: x > 17)
    c.outline()


@icon
def branch(c):
    """Friend of the Trees - a branch leaving the trunk."""
    # Accent path first, trunk over the top: letting the trunk cover the
    # junction is what makes the branch read as leaving it.
    c.disc(24, 9, 5, 'G')
    c.rect(22, 11, 4, 5, 'G')
    c.line(24, 15, 19, 20, 'G', 4)
    c.rect(10, 18, 10, 4, 'G')
    c.shade('G', 'g', lambda x, y: x + y > 36)
    c.rect(7, 5, 5, 22, 'B')
    c.disc(9.5, 5, 4.5, 'B')
    c.disc(9.5, 26, 4.5, 'B')
    c.shade('B', 'b', lambda x, y: y > 27)
    c.outline()


@icon
def merge(c):
    """Merger - a branch coming back into the trunk."""
    # Two lines converging into one, rather than branch() with the accent
    # reversed: at 32px a mirrored fork was indistinguishable from it, and
    # an arrowhead small enough to fit just blurred into the trunk.
    c.disc(24, 6, 5, 'P')
    c.rect(22, 8, 4, 8, 'P')
    c.line(24, 15, 17, 22, 'P', 5)
    c.shade('P', 'p', lambda x, y: x + y > 34)
    c.rect(6, 9, 5, 9, 'B')
    c.disc(8.5, 8, 4.5, 'B')
    c.line(9, 17, 16, 23, 'B', 5)
    c.rect(14, 21, 5, 6, 'B')
    c.disc(16.5, 26, 4.5, 'B')
    c.shade('B', 'b', lambda x, y: y > 28)
    c.outline()


@icon
def tag(c):
    """Tagger - a luggage tag."""
    c.rect(11, 5, 16, 22, 'O')
    c.tri(11, 5, 11, 27, 2, 16, 'O')
    c.shade('O', 'o', lambda x, y: y > 21)
    c.shade('O', 'Y', lambda x, y: y < 8 and x > 10)
    c.disc(14, 16, 2.8, '.')      # punch hole
    c.outline()


# ------------------------------------------- MERGE REQUESTS & REVIEW

@icon
def mropen(c):
    """Merge Request Opener - a new request page.

    Deliberately not a branch glyph: branch.png and merge.png already own
    that shape, and at 32px three node-and-curve icons are indistinguishable.
    """
    c.rect(4, 2, 18, 27, 'W')
    c.tri(22, 2, 22, 9, 15, 2, 'W')      # folded corner
    c.shade('W', 'w', lambda x, y: x > 15)
    c.rect(22, 2, 1, 7, 'w')
    for y in range(12, 25, 4):
        c.rect(7, y, 12, 2, 's')
    c.disc(22, 23, 7, 'G')
    c.shade('G', 'g', lambda x, y: x + y > 46)
    c.rect(21, 18, 3, 11, 'W')
    c.rect(16, 22, 12, 3, 'W')
    c.outline()


@icon
def stamp(c):
    """Stamp of Approval - a stamp above a fresh green mark."""
    c.rect(11, 2, 10, 6, 'P')
    c.rect(14, 8, 4, 3, 'p')
    c.rect(7, 11, 18, 6, 'P')
    c.shade('P', 'p', lambda x, y: y >= 15)
    c.line(11, 25, 15, 29, 'G', 4)
    c.line(15, 29, 24, 20, 'G', 4)
    c.outline()


@icon
def reject(c):
    """Second Thoughts - a red badge with a cross."""
    c.disc(16, 16, 13, 'R')
    c.shade('R', 'r', lambda x, y: x + y > 34)
    c.line(10, 10, 21, 21, 'W', 4)
    c.line(21, 10, 10, 21, 'W', 4)
    c.outline()


@icon
def stopwatch(c):
    """Rubber Stamp - merged fast, a stopwatch."""
    c.rect(14, 1, 4, 4, 's')
    c.rect(11, 3, 10, 2, 'S')
    c.line(24, 6, 27, 3, 'S', 3)
    c.disc(16, 19, 12, 'S')
    c.disc(16, 19, 10, 'W')
    c.shade('W', 'w', lambda x, y: x + y > 42)
    c.line(16, 19, 22, 13, 'R', 2)
    c.disc(16, 19, 1.6, 'K')
    c.outline()


@icon
def comment(c):
    """Commentator - a back-and-forth.

    Two bubbles rather than one, so it stays distinct from resolved(), which
    is a single bubble with a tick.
    """
    c.rect(13, 2, 16, 11, 'B')
    for cx, cy in ((14, 3), (27, 3), (14, 11), (27, 11)):
        c.disc(cx, cy, 2.5, 'B')
    c.tri(20, 12, 27, 12, 25, 17, 'B')
    c.shade('B', 'b', lambda x, y: y > 10)
    c.rect(3, 13, 17, 11, 'W')
    for cx, cy in ((4, 14), (18, 14), (4, 22), (18, 22)):
        c.disc(cx, cy, 2.5, 'W')
    c.tri(6, 23, 13, 23, 7, 29, 'W')
    c.shade('W', 'w', lambda x, y: y > 21)
    for x in range(6, 18, 4):
        c.rect(x, 17, 3, 3, 's')
    c.outline()


@icon
def resolved(c):
    """Loose Ends - a discussion bubble, ticked off."""
    c.rect(3, 4, 26, 17, 'T')
    c.disc(6, 7, 3, 'T')
    c.disc(26, 7, 3, 'T')
    c.disc(6, 18, 3, 'T')
    c.disc(26, 18, 3, 'T')
    c.tri(8, 19, 16, 19, 9, 27, 'T')
    c.shade('T', 't', lambda x, y: y > 17)
    c.line(10, 12, 14, 16, 'W', 3)
    c.line(14, 16, 22, 8, 'W', 3)
    c.outline()


# ------------------------------------------------------------- ISSUES

@icon
def bug(c):
    """Issue Opener - a beetle."""
    for x, y in ((7, 10), (25, 10), (6, 17), (26, 17), (7, 24), (25, 24)):
        c.line(16, y, x, y + (-3 if y < 16 else 3), 'K', 2)
    c.line(12, 6, 9, 2, 'K', 2)
    c.line(20, 6, 23, 2, 'K', 2)
    c.disc(16, 20, 9, 'R')
    c.shade('R', 'r', lambda x, y: x > 18)
    c.disc(16, 9, 5.5, 'g')
    c.disc(13.5, 8, 1.6, 'W')
    c.disc(18.5, 8, 1.6, 'W')
    c.rect(15, 13, 2, 15, 'r')
    for cx, cy in ((12, 17), (20, 22), (12, 24)):
        c.disc(cx, cy, 1.8, 'K')
    c.outline()


@icon
def fix(c):
    """Problem Solver - the hard hat, redrawn on the 32px grid."""
    c.disc(16, 19, 10, 'Y')
    c.rect(0, 20, 32, 12, '.')          # flatten the dome
    c.rect(2, 20, 28, 4, 'Y')           # brim
    c.disc(3.5, 21.5, 2, 'Y')
    c.disc(28.5, 21.5, 2, 'Y')
    c.shade('Y', 'y', lambda x, y: x > 19)
    c.rect(14, 10, 4, 10, 'y')          # centre ridge
    c.rect(9, 13, 2, 7, 'y')
    c.rect(21, 13, 2, 7, 'y')
    c.outline()


# ------------------------------------------------------------- CI/CD

@icon
def pipes(c):
    """Pipeline Operator - plumbing with a valve."""
    c.rect(2, 20, 28, 8, 'S')
    c.rect(4, 18, 3, 12, 's')
    c.rect(25, 18, 3, 12, 's')
    c.rect(12, 8, 8, 13, 'S')
    c.rect(10, 8, 12, 3, 's')
    c.shade('S', 's', lambda x, y: y > 25)
    c.line(11, 5, 21, 5, 'R', 3)
    c.rect(15, 4, 2, 5, 'R')
    c.outline()


@icon
def allgreen(c):
    """All Green - pipelines succeeded."""
    c.disc(16, 16, 13, 'G')
    c.shade('G', 'g', lambda x, y: x + y > 34)
    c.line(10, 17, 14, 21, 'W', 4)
    c.line(14, 21, 23, 11, 'W', 4)
    c.outline()


@icon
def firefighter(c):
    """Firefighter - pipelines failed, an extinguisher."""
    c.rect(9, 11, 14, 19, 'R')
    c.disc(13, 12, 4, 'R')
    c.disc(19, 12, 4, 'R')
    c.shade('R', 'r', lambda x, y: x > 18)
    c.rect(13, 6, 6, 6, 's')
    c.rect(10, 4, 12, 3, 'S')
    c.line(22, 5, 27, 9, 'S', 3)
    c.line(27, 9, 26, 15, 'S', 3)
    c.rect(11, 16, 10, 8, 'W')
    c.rect(13, 18, 6, 4, 'K')
    c.outline()


@icon
def gears(c):
    """Assembly Line - jobs run."""
    import math
    c.disc(12, 20, 7.5, 'S')
    for k in range(6):
        a = k * math.pi / 3
        c.rect(12 + 9 * math.cos(a) - 2, 20 + 9 * math.sin(a) - 2, 4, 4, 'S')
    c.shade('S', 's', lambda x, y: y > 22)
    c.disc(12, 20, 2.8, '.')
    c.disc(23, 9, 5, 'Y')
    for k in range(6):
        a = k * math.pi / 3 + 0.5
        c.rect(23 + 6.2 * math.cos(a) - 1.5, 9 + 6.2 * math.sin(a) - 1.5, 3, 3, 'Y')
    c.shade('Y', 'y', lambda x, y: y > 10)
    c.disc(23, 9, 1.8, '.')
    c.outline()


@icon
def rocket(c):
    """Ship It - deployments."""
    c.tri(16, 1, 11, 12, 21, 12, 'W')
    c.rect(11, 11, 10, 12, 'W')
    c.disc(16, 22, 5, 'W')
    c.shade('W', 'w', lambda x, y: x > 17)
    c.tri(11, 14, 5, 25, 11, 24, 'R')
    c.tri(21, 14, 27, 25, 21, 24, 'R')
    c.shade('R', 'r', lambda x, y: x > 21)
    c.disc(16, 12, 3.2, 'B')
    c.disc(16, 12, 2, 'b')
    c.tri(12, 26, 20, 26, 16, 31, 'Y')
    c.tri(14, 26, 18, 26, 16, 30, 'R')
    c.outline()


@icon
def bullseye(c):
    """Stuck the Landing - deployments succeeded."""
    c.disc(15, 18, 13, 'R')
    c.disc(15, 18, 10, 'W')
    c.disc(15, 18, 7, 'R')
    c.disc(15, 18, 4, 'W')
    c.disc(15, 18, 1.8, 'r')
    c.line(15, 18, 27, 6, 'S', 3)
    c.tri(24, 2, 31, 3, 26, 9, 'Y')
    c.outline()


# ------------------------------------------------------ COLLABORATION

@icon
def thumbsup(c):
    """Reaction Time - emoji awarded."""
    c.rect(10, 14, 15, 14, 'Y')
    c.disc(24, 17, 3.5, 'Y')
    c.disc(24, 25, 3.5, 'Y')
    c.rect(13, 5, 6, 10, 'Y')
    c.disc(16, 5, 3, 'Y')
    c.shade('Y', 'y', lambda x, y: y > 23)
    c.line(19, 18, 26, 18, 'y', 1)
    c.line(20, 22, 27, 22, 'y', 1)
    c.rect(5, 14, 6, 15, 'B')
    c.shade('B', 'b', lambda x, y: y > 25)
    c.outline()


@icon
def librarian(c):
    """Librarian - wiki pages created."""
    c.rect(2, 6, 13, 20, 'W')
    c.rect(17, 6, 13, 20, 'W')
    c.tri(2, 6, 15, 4, 15, 8, 'W')
    c.tri(30, 6, 17, 4, 17, 8, 'W')
    c.shade('W', 'w', lambda x, y: x > 16)
    for i, y in enumerate(range(11, 23, 4)):
        c.rect(4, y, 9 - (i % 2) * 3, 2, 's')
        c.rect(19 + (i % 2) * 3, y, 9 - (i % 2) * 3, 2, 's')
    c.rect(2, 23, 13, 4, 'B')
    c.rect(17, 23, 13, 4, 'B')
    c.shade('B', 'b', lambda x, y: x > 16)
    c.rect(15, 4, 2, 24, 'n')
    c.outline()


# --------------------------------------------------------- ENGAGEMENT

@icon
def mug(c):
    """Regular - active days, a daily coffee."""
    c.ring(24, 17, 6, 3, 'w')
    c.rect(5, 10, 18, 19, 'W')
    c.disc(9, 27, 4, 'W')
    c.disc(19, 27, 4, 'W')
    c.shade('W', 'w', lambda x, y: x > 17)
    c.rect(5, 10, 18, 4, 'n')
    c.rect(6, 11, 16, 2, 'N')
    c.rect(5, 18, 18, 3, 'R')
    for x in range(8, 22, 6):
        c.line(x, 3, x - 2, 8, 'w', 2)
    c.outline()


@icon
def streak(c):
    """Connection Streak - a calendar carrying a flame."""
    c.rect(10, 1, 3, 6, 's')
    c.rect(20, 1, 3, 6, 's')
    c.rect(2, 4, 28, 25, 'W')
    c.rect(2, 4, 28, 7, 'R')
    c.shade('R', 'r', lambda x, y: y > 9)
    c.shade('W', 'w', lambda x, y: y > 25)
    for gy in range(14, 26, 5):
        for gx in range(5, 28, 6):
            c.rect(gx, gy, 4, 3, 'w')
    c.tri(21, 13, 14, 24, 28, 24, 'O')  # flame
    c.disc(21, 23, 6, 'O')
    c.shade('O', 'o', lambda x, y: x > 22)
    c.tri(21, 19, 17, 26, 25, 26, 'Y')
    c.disc(21, 25, 3.2, 'Y')
    c.outline()


@icon
def owl(c):
    """Night Owl - kept brown and wide-eyed like the original."""
    c.tri(4, 12, 10, 2, 15, 11, 'n')
    c.tri(28, 12, 22, 2, 17, 11, 'n')
    c.disc(16, 18, 11, 'N')
    c.shade('N', 'n', lambda x, y: x > 18)
    c.disc(11, 15, 5.5, 'W')
    c.disc(21, 15, 5.5, 'W')
    c.shade('W', 'w', lambda x, y: x > 22)
    c.disc(11, 15, 3.2, 'Y')
    c.disc(21, 15, 3.2, 'Y')
    c.disc(11, 15, 1.6, 'K')
    c.disc(21, 15, 1.6, 'K')
    c.tri(16, 17, 13, 22, 19, 22, 'O')  # beak
    c.rect(11, 26, 4, 3, 'O')           # feet
    c.rect(17, 26, 4, 3, 'O')
    c.outline()


@icon
def bird(c):
    """Early Bird - a chick.

    No sun: at 32px it collided with the head and read as a growth. The
    yellow already carries "early" against owl()'s brown.
    """
    c.line(16, 8, 16, 3, 'y', 2)        # cowlick
    c.line(16, 3, 20, 2, 'y', 2)
    c.disc(16, 18, 11, 'Y')
    c.shade('Y', 'y', lambda x, y: x > 18)
    c.disc(9, 22, 4.5, 'y')             # wing
    c.disc(12, 15, 2.2, 'K')
    c.disc(20, 15, 2.2, 'K')
    c.tri(16, 18, 12, 22, 20, 22, 'O')  # beak
    c.tri(16, 23, 13, 21, 19, 21, 'o')
    c.rect(12, 28, 4, 3, 'O')           # feet
    c.rect(18, 28, 4, 3, 'O')
    c.outline()


def main():
    """Regenerate every generated icon in place.

    Pass --svg for scalable copies too, or --force to redraw the
    hand-authored icons as well, which overwrites artwork this script did
    not make. There is no reason to do that except to recover a lost file.
    """
    want_svg = '--svg' in sys.argv
    force = '--force' in sys.argv
    for name, fn in ICONS.items():
        if name in HAND_AUTHORED and not force:
            print(f'{name}.png: hand-authored, left alone')
            continue
        c = Canvas()
        fn(c)
        print(c.png(name))
        if want_svg:
            c.svg(name)


if __name__ == '__main__':
    main()
