/**
 * Feed item verification (spec/feeds.md §1.2) and update semantics.
 *
 * An item is canonicalized with JCS (RFC 8785) with its `_sig.signatures`
 * field removed, then signed with Ed25519 (base64url, no padding). In
 * editor mode the editor signatures are load-bearing (threshold); in
 * default mode signatures are attribution-only (unknown keys ignored,
 * known key with a failing signature → item rejected — no third state).
 */

import canonicalize from 'canonicalize';
import { base64urlToBytes, sha256Hex } from './bytes';
import { ed25519Verify } from './ed';
import { EditorAuth, ProtocolError, AuthorizedKey } from './tuf';

export interface ItemSigEntry {
  keyid: string;
  sig: string;
  [extra: string]: unknown;
}

export interface ItemSig {
  channel?: string;
  withdrawn?: boolean;
  resources?: Record<string, string>;
  signatures?: ItemSigEntry[];
  [extra: string]: unknown;
}

/** A JSON Feed item with the `_sig` extension (unknown fields preserved). */
export interface FeedItem {
  id?: string;
  title?: string;
  content_html?: string;
  content_text?: string;
  summary?: string;
  image?: string;
  url?: string;
  date_published?: string;
  date_modified?: string;
  tags?: string[];
  language?: string;
  authors?: { name?: string; url?: string }[];
  attachments?: { url?: string; mime_type?: string; size_in_bytes?: number; title?: string }[];
  _sig?: ItemSig;
  [extra: string]: unknown;
}

export interface FeedDoc {
  version?: string;
  title?: string;
  feed_url?: string;
  description?: string;
  icon?: string;
  favicon?: string;
  language?: string;
  expired?: boolean;
  items: FeedItem[];
  _sig?: { about?: string; [extra: string]: unknown };
  [extra: string]: unknown;
}

/**
 * JCS (RFC 8785) canonical bytes of the item with `_sig.signatures`
 * removed — exactly what the publisher signed.
 */
export function canonicalItemBytes(item: FeedItem): Uint8Array {
  const clone = JSON.parse(JSON.stringify(item)) as FeedItem;
  if (clone._sig) delete clone._sig.signatures;
  const canonical = canonicalize(clone);
  if (canonical === undefined) throw new ProtocolError('item: not JCS-serializable');
  return new TextEncoder().encode(canonical);
}

/** Content identity for update detection: JCS of the signed item (signature-only changes are not updates). */
export function signedContentKey(item: FeedItem): string {
  return sha256Hex(canonicalItemBytes(item));
}

export function isWithdrawn(item: FeedItem): boolean {
  return item._sig?.withdrawn === true;
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
 * Verify one item's signatures. `editor` is the editor-mode entry for the
 * channel (undefined = default mode). `channelKeys` are the channel role
 * keys (known keys — a failing signature by one of them rejects the item in
 * both modes).
 */
export function verifyItemSignatures(
  item: FeedItem,
  editor: EditorAuth | undefined,
  channelKeys: AuthorizedKey[],
): void {
  const sigs = item._sig?.signatures ?? [];
  const canonical = canonicalItemBytes(item);

  if (editor) {
    // editor mode (spec/feeds.md §2): at least `threshold` valid signatures
    // by keys in the entry's keyids; additional entries (channel-key
    // signatures) are not load-bearing.
    let valid = 0;
    for (const entry of sigs) {
      if (tryVerify(entry, editor.keys, canonical)) valid++;
    }
    if (valid < editor.threshold) {
      throw new ProtocolError(`item: ${valid}/${editor.threshold} valid editor signatures`);
    }
    return;
  }

  // default mode (spec/feeds.md §1.2): signatures optional, attribution
  // only. Entries by unknown keys are ignored; a known keyid whose
  // signature does not verify → item rejected (no third state).
  for (const entry of sigs) {
    if (!channelKeys.some((k) => k.keyid === entry.keyid)) continue;
    if (!tryVerify(entry, channelKeys, canonical)) {
      throw new ProtocolError(`item: signature by channel key ${entry.keyid} invalid`);
    }
  }
}

/** Channel cross-check (spec/feeds.md §1.2): `_sig.channel` MUST equal the bare channel name of the feed it came from. */
export function verifyChannelCrossCheck(item: FeedItem, channel: string): void {
  if (item._sig?.channel !== undefined && item._sig.channel !== channel) {
    throw new ProtocolError(`item: _sig.channel "${item._sig.channel}" != feed channel "${channel}"`);
  }
}

/**
 * Verify `_sig.resources` (optional, normative when present): each listed
 * URL maps to the SHA-256 of the resource bytes. Call before rendering,
 * opening or saving the resource. Mismatch → resource unavailable (item
 * itself stays valid).
 */
export function verifyResourceHash(item: FeedItem, url: string, bytes: Uint8Array): boolean {
  const want = item._sig?.resources?.[url];
  if (!want) return true; // unlisted = ordinary web resource (mutable by design)
  return sha256Hex(bytes) === want.toLowerCase();
}

/**
 * Merge new feed items into the cached store per spec/feeds.md §1.2
 * semantics: (channel, id) dedup; content difference = update (position and
 * read-state kept by the caller); withdrawn items are hidden entirely.
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
    if (isWithdrawn(item)) continue; // hidden entirely — not shown, not unread
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
