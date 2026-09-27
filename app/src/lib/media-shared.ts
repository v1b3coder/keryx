/**
 * Media rules shared by every target (spec/feeds.md §1.1,
 * spec/repository.md §2): the linked-logo pin requirement and the
 * size-limited body reader.
 */

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

/** The image mime type from the content type header or the URL extension. */
export function mimeOf(url: string, contentType: string | null): string {
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
