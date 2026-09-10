/**
 * Generates the PWA icons (public/icons/*) as flat square PNGs with the
 * accent blue background — the app itself stays unbranded in the UI, but
 * the home-screen icon needs to exist.
 */

import { writeFileSync, mkdirSync } from 'node:fs';
import { PNG } from 'pngjs';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const outDir = join(dirname(fileURLToPath(import.meta.url)), '..', 'public', 'icons');
mkdirSync(outDir, { recursive: true });

const BLUE = [0x00, 0x00, 0xee];
const WHITE = [0xff, 0xff, 0xff];

function render(size, draw) {
  const png = new PNG({ width: size, height: size });
  for (let y = 0; y < size; y++) {
    for (let x = 0; x < size; x++) {
      const [r, g, b] = draw(x, y, size);
      const idx = (size * y + x) << 2;
      png.data[idx] = r;
      png.data[idx + 1] = g;
      png.data[idx + 2] = b;
      png.data[idx + 3] = 255;
    }
  }
  return PNG.sync.write(png);
}

// Blue square with a white rounded-square "message" glyph (three bars).
function icon(x, y, size) {
  const u = size / 100;
  const inset = 22 * u;
  // rounded rect background: white card on blue
  const r = 20 * u;
  const inX = x >= inset && x <= size - inset && y >= inset && y <= size - inset;
  const cx = Math.min(Math.max(x, inset + r), size - inset - r);
  const cy = Math.min(Math.max(y, inset + r), size - inset - r);
  const dist = Math.hypot(x - cx, y - cy);
  const card = dist <= r;
  if (!inX || !card) return BLUE;
  // three white bars (content lines)
  const barX0 = inset + 14 * u;
  const barX1 = size - inset - 14 * u;
  if (y > inset + 22 * u && y < inset + 30 * u && x >= barX0 && x <= barX1) return WHITE;
  if (y > inset + 44 * u && y < inset + 52 * u && x >= barX0 && x <= barX1 * 0.8) return WHITE;
  if (y > inset + 66 * u && y < inset + 74 * u && x >= barX0 && x <= barX1 * 0.6) return WHITE;
  return WHITE;
}

// Maskable icon: the glyph fills more of the canvas (safe zone at 80%).
function maskable(x, y, size) {
  const u = size / 100;
  const inset = 12 * u;
  const r = 22 * u;
  const cx = Math.min(Math.max(x, inset + r), size - inset - r);
  const cy = Math.min(Math.max(y, inset + r), size - inset - r);
  const dist = Math.hypot(x - cx, y - cy);
  if (dist > r) return BLUE;
  const barX0 = inset + 16 * u;
  const barX1 = size - inset - 16 * u;
  if (y > inset + 24 * u && y < inset + 33 * u && x >= barX0 && x <= barX1) return BLUE;
  if (y > inset + 48 * u && y < inset + 57 * u && x >= barX0 && x <= barX1 * 0.82) return BLUE;
  if (y > inset + 72 * u && y < inset + 81 * u && x >= barX0 && x <= barX1 * 0.62) return BLUE;
  return WHITE;
}

for (const size of [192, 512]) {
  writeFileSync(join(outDir, `icon-${size}.png`), render(size, icon));
}
writeFileSync(join(outDir, 'icon-maskable-512.png'), render(512, maskable));
console.log('icons written to', outDir);
