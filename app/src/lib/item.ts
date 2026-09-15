/**
 * Feed item verification (spec/feeds.md §1.2) and update semantics.
 *
 * An item is canonicalized with OLPC (securesystemslib canonical JSON) with
 * its `sig` field removed — the same canonicalization as TUF metadata — then
 * signed with Ed25519 (base64url, no padding). In an authored channel the
 * authors-role signatures are load-bearing (threshold); in simple mode the
 * channel role keys are. Unknown keyids are ignored (attribution only); a known
 * keyid whose signature fails rejects the item — there is no third state.
 */

import { olpcCanonical } from './olpc';
import { base64urlToBytes, sha256Hex } from './bytes';
import { ed25519Verify } from './ed';
import { linkedUrlAllowed } from './urlpolicy';
import { ProtocolError, AuthorizedKey } from './tuf';

export interface ItemSigEntry {
  keyid: string;
  sig: string;
  [extra: string]: unknown;
}

export interface Attachment {
  name?: string;
  url: string;
  mime_type?: string;
  size_in_bytes?: number;
  sha256?: string;
  [extra: string]: unknown;
}

/** A public channel item (spec/feeds.md §1.1); unknown fields preserved. */
export interface FeedItem {
  id?: string;
  title?: string;
  content_html?: string;
  image?: string;
  image_sha256?: string;
  date_published?: string;
  date_modified?: string;
  tags?: string[];
  language?: string;
  attachments?: Attachment[];
  sig?: ItemSigEntry[];
  [extra: string]: unknown;
}

/** One authorizing key set (channel role or authors role). */
export interface KeySet {
  keys: AuthorizedKey[];
  threshold: number;
}

/**
 * OLPC canonical bytes of the item with its `sig` field removed — exactly
 * what the publisher signed (spec/feeds.md §1.2).
 */
export function canonicalItemBytes(item: FeedItem): Uint8Array {
  const clone = JSON.parse(JSON.stringify(item)) as FeedItem;
  delete clone.sig;
  const canonical = olpcCanonical(clone);
  if (canonical === undefined) throw new ProtocolError('item: not OLPC-serializable');
  return new TextEncoder().encode(canonical);
}

/** Content identity for update detection: OLPC of the signed item (signature-only changes are not updates). */
export function signedContentKey(item: FeedItem): string {
  return sha256Hex(canonicalItemBytes(item));
}

function tryVerify(entry: ItemSigEntry, keys: AuthorizedKey[], canonical: Uint8Array): boolean {
  const key = keys.find((k) => k.keyid === entry.keyid);
  if (!key) return false;
  try {
    return ed25519Verify(base64urlToBytes(entry.sig), canonical, key.pub);
  } catch {
    return false;
  }
}

/**
 * Verify one item's signatures (spec/feeds.md §1.2). `authors` is the
 * authors-role key set (authored channels, the default); `channel` is the
 * channel role key set (simple mode). Entries by unknown keys are ignored;
 * a known keyid whose signature fails rejects the item in both modes.
 */
export function verifyItemSignatures(
  item: FeedItem,
  authors: KeySet | undefined,
  channel: KeySet,
): void {
  const sigs = item.sig ?? [];
  const canonical = canonicalItemBytes(item);

  if (authors && authors.keys.length > 0) {
    // authored channel: at least `threshold` valid author signatures;
    // additional entries (channel-key signatures) are not load-bearing.
    let valid = 0;
    for (const entry of sigs) {
      if (tryVerify(entry, authors.keys, canonical)) valid++;
      else if (authors.keys.some((k) => k.keyid === entry.keyid)) {
        throw new ProtocolError(`item: signature by author key ${entry.keyid} invalid`);
      }
    }
    if (valid < Math.max(1, authors.threshold)) {
      throw new ProtocolError(`item: ${valid}/${Math.max(1, authors.threshold)} valid author signatures`);
    }
    return;
  }

  // simple mode: at least `threshold` entries verify against the channel
  // role keys; entries by unknown keys are ignored, a known keyid whose
  // signature fails rejects the item.
  let valid = 0;
  for (const entry of sigs) {
    if (!channel.keys.some((k) => k.keyid === entry.keyid)) continue;
    if (!tryVerify(entry, channel.keys, canonical)) {
      throw new ProtocolError(`item: signature by channel key ${entry.keyid} invalid`);
    }
    valid++;
  }
  if (valid < Math.max(1, channel.threshold)) {
    throw new ProtocolError(`item: ${valid}/${Math.max(1, channel.threshold)} valid channel signatures`);
  }
}

/** The id is the TUF target path segment (spec/feeds.md §1.1). */
export function itemIdFromPath(path: string): string {
  const base = path.split('/').pop() ?? '';
  return base.endsWith('.json') ? base.slice(0, -'.json'.length) : base;
}

/**
 * Validate the item's `image` rule (spec/feeds.md §1.1): an inline data URL
 * is self-contained; a linked URL REQUIRES `image_sha256`. A linked image
 * without a hash is a schema violation → the item is rejected.
 */
export function verifyImage(item: FeedItem): void {
  const image = item.image;
  if (!image) return;
  if (image.startsWith('data:')) {
    if (!image.includes(';base64,')) throw new ProtocolError('item: image data URL must be base64');
    return;
  }
  if (!linkedUrlAllowed(image)) {
    throw new ProtocolError('item: image must be a data URL or absolute HTTPS URL');
  }
  if (!item.image_sha256) {
    throw new ProtocolError('item: linked image requires image_sha256');
  }
}

/**
 * Verify an attachment's SHA-256 when present (spec/feeds.md §1.1). A mismatch
 * makes the resource unavailable — the item itself stays valid.
 */
export function verifyAttachmentHash(attachment: Attachment, bytes: Uint8Array): boolean {
  if (!attachment.sha256) return true;
  return sha256Hex(bytes) === attachment.sha256.toLowerCase();
}

/** The attachment's expected hash, when present. */
export function attachmentSha(attachment: Attachment): string | undefined {
  return attachment.sha256?.toLowerCase();
}

/**
 * Merge new items into the cached store per spec/feeds.md §1.3: (channel, id)
 * dedup; content difference = update (position and read-state kept by the
 * caller); absence from the index = unpublished (handled by the caller).
 */
export function mergeItems(channel: string, feedItems: FeedItem[], existing: Map<string, FeedItem>): {
  added: FeedItem[];
  updated: FeedItem[];
  unchanged: FeedItem[];
} {
  const added: FeedItem[] = [];
  const updated: FeedItem[] = [];
  const unchanged: FeedItem[] = [];
  const next = new Map(existing);
  for (const item of feedItems) {
    if (!item.id) continue;
    const key = `${channel}\u0000${item.id}`;
    const prev = next.get(key);
    if (!prev) {
      added.push(item);
      next.set(key, item);
      continue;
    }
    if (signedContentKey(item) !== signedContentKey(prev)) {
      updated.push(item);
      next.set(key, item);
    } else {
      unchanged.push(item);
    }
  }
  return { added, updated, unchanged };
}
