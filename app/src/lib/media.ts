/**
 * Media loading with integrity checks: `image_sha256` / `attachments[].sha256`
 * (spec/feeds.md §1.1) and `custom.logo_sha256` (spec/repository.md §2).
 * Bytes are cached in IndexedDB; a mismatch makes the resource unavailable
 * (placeholder) — the item itself stays valid.
 */

import { getMedia, putMedia } from './store';
import { sha256Hex } from './bytes';

const objectUrlCache = new Map<string, string>();

/**
 * A linked logo REQUIRES `logo_sha256` (spec/repository.md §2): without it the
 * logo is a metadata error and MUST NOT be displayed (neutral placeholder). An
 * inline data URL is self-authenticated by the metadata signature.
 */
export function logoDisplayable(logo: string | undefined, sha256: string | undefined): boolean {
  if (!logo) return false;
  if (logo.startsWith('data:')) return true;
  return !!sha256;
}

/** Read a response body, aborting as soon as it exceeds max bytes. */
export async function readLimitedBody(res: Response, max: number): Promise<Uint8Array> {
  const reader = res.body?.getReader();
  if (!reader) {
    const bytes = new Uint8Array(await res.arrayBuffer());
    if (bytes.length > max) throw new Error(`response is ${bytes.length} bytes, over the ${max}-byte limit`);
    return bytes;
  }
  const chunks: Uint8Array[] = [];
  let total = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    total += value.length;
    if (total > max) {
      await reader.cancel();
      throw new Error(`response exceeds the ${max}-byte limit`);
    }
    chunks.push(value);
  }
  const out = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    out.set(chunk, offset);
    offset += chunk.length;
  }
  return out;
}

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
