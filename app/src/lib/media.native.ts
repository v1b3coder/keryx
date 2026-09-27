/**
 * Media loading with integrity checks for iOS/Android (React Native):
 * `image_sha256` / `attachments[].sha256` (spec/feeds.md §1.1) and
 * `custom.logo_sha256` (spec/repository.md §2). Bytes are cached in the
 * native store; a mismatch makes the resource unavailable (placeholder) —
 * the item itself stays valid. Images render from a data URL so no
 * unverified byte is ever handed to the image loader.
 */

import { getMedia, putMedia } from './store';
import { bytesToBase64, sha256Hex } from './bytes';
import { logoDisplayable, readLimitedBody, mimeOf } from './media-shared';

export { logoDisplayable, readLimitedBody };

function dataUrlFor(bytes: ArrayBuffer, mime: string): string {
  return `data:${mime};base64,${bytesToBase64(new Uint8Array(bytes))}`;
}

/**
 * Load an image URL, optionally verifying its SHA-256. Returns a data URL for
 * rendering, or null when the resource is unavailable (fetch error or hash
 * mismatch — never rendered).
 */
export async function loadImage(url: string, origin: string, expectedSha?: unknown): Promise<string | null> {
  // a present non-string pin can never verify — unavailable, never rendered
  if (expectedSha !== undefined && typeof expectedSha !== 'string') return null;
  const want = expectedSha?.toLowerCase();
  const cached = await getMedia(url);
  if (cached) {
    if (want && sha256Hex(new Uint8Array(cached.bytes)) !== want) return null;
    return dataUrlFor(cached.bytes, cached.mime);
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
  return dataUrlFor(bytes, mime);
}
