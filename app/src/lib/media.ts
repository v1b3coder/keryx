/**
 * Media loading with integrity checks: `_sig.resources` hashes (spec/feeds.md
 * §1.2) and `custom.logo_sha256` (spec/repository.md §2). Bytes are cached
 * in IndexedDB; a mismatch makes the resource unavailable (placeholder) —
 * the item itself stays valid.
 */

import { getMedia, putMedia } from './store';
import { sha256Hex } from './bytes';

const objectUrlCache = new Map<string, string>();

function mimeOf(url: string, contentType: string | null): string {
  if (contentType && contentType.startsWith('image/')) return contentType.split(';')[0];
  const ext = url.split('.').pop()?.toLowerCase() ?? '';
  switch (ext) {
    case 'png':
      return 'image/png';
    case 'webp':
      return 'image/webp';
    case 'jpg':
    case 'jpeg':
      return 'image/jpeg';
    case 'gif':
      return 'image/gif';
    case 'svg':
      return 'image/svg+xml';
    default:
      return 'image/*';
  }
}

function objectUrlFor(url: string, bytes: ArrayBuffer, mime: string): string {
  let existing = objectUrlCache.get(url);
  if (existing) return existing;
  existing = URL.createObjectURL(new Blob([bytes], { type: mime || 'image/*' }));
  objectUrlCache.set(url, existing);
  return existing;
}

/**
 * Load an image URL, optionally verifying its SHA-256. Returns an object URL
 * for rendering, or null when the resource is unavailable (fetch error or
 * hash mismatch — never rendered).
 */
export async function loadImage(url: string, origin: string, expectedSha?: string): Promise<string | null> {
  const want = expectedSha?.toLowerCase();
  const cached = await getMedia(url);
  if (cached) {
    if (want && sha256Hex(new Uint8Array(cached.bytes)) !== want) return null;
    return objectUrlFor(url, cached.bytes, cached.mime);
  }
  let res: Response;
  try {
    res = await fetch(url);
  } catch {
    return null;
  }
  if (!res.ok) return null;
  const bytes = await res.arrayBuffer();
  if (want && sha256Hex(new Uint8Array(bytes)) !== want) return null;
  const mime = mimeOf(url, res.headers.get('content-type'));
  await putMedia({ url, origin, bytes, mime, at: Date.now() });
  return objectUrlFor(url, bytes, mime);
}
