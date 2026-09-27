/**
 * Builds the custom web service worker after `expo export --platform web`:
 * 1. scans the exported dist/ into a workbox precache manifest,
 * 2. bundles src/sw.ts with esbuild (the manifest injected as
 *    `self.__WB_MANIFEST`),
 * 3. writes dist/sw.js and makes sure dist/index.html links the PWA
 *    manifest.
 *
 * This is the Expo-web replacement for vite-plugin-pwa's injectManifest.
 */
import { createHash } from 'node:crypto';
import { readdirSync, readFileSync, statSync, writeFileSync } from 'node:fs';
import { dirname, join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';
import { build } from 'esbuild';

const appDir = join(dirname(fileURLToPath(import.meta.url)), '..');
const distDir = join(appDir, 'dist');

function walk(dir) {
  const out = [];
  for (const entry of readdirSync(dir)) {
    const path = join(dir, entry);
    if (statSync(path).isDirectory()) out.push(...walk(path));
    else out.push(path);
  }
  return out;
}

const files = walk(distDir);
const manifest = files
  .filter((path) => {
    const rel = relative(distDir, path).replace(/\\/g, '/');
    return rel !== 'sw.js' && !rel.endsWith('.map');
  })
  .map((path) => {
    const rel = relative(distDir, path).replace(/\\/g, '/');
    const bytes = readFileSync(path);
    return { url: rel, revision: createHash('sha256').update(bytes).digest('hex') };
  });

await build({
  entryPoints: [join(appDir, 'src', 'sw.ts')],
  bundle: true,
  format: 'iife',
  target: 'es2020',
  outfile: join(distDir, 'sw.js'),
  define: { 'self.__WB_MANIFEST': JSON.stringify(manifest) },
  logLevel: 'warning',
});

// The PWA manifest link: Expo's web index.html does not add one.
const indexPath = join(distDir, 'index.html');
const html = readFileSync(indexPath, 'utf8');
if (!html.includes('rel="manifest"')) {
  writeFileSync(
    indexPath,
    html.replace('</head>', '  <link rel="manifest" href="/manifest.json">\n</head>'),
  );
}

console.log(`web service worker built: ${manifest.length} precached files`);
