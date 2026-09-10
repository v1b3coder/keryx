/**
 * Private (per-order) capability feed verification (spec/feeds.md §3).
 *
 * A private feed is a single JSON Feed document signed as a whole: Ed25519
 * over the JCS bytes of the document with the top-level `_sig.signatures`
 * field removed. App enforcement sequence: size limit → authorized pattern
 * → whole-document signature + `_sig.channel` + `_sig.url` → `_sig.version`
 * monotonic → `_sig.expires` (stale = keep cache + retry, never close).
 */

import canonicalize from 'canonicalize';
import { base64urlToBytes, hexToBytes } from './bytes';
import { ed25519Verify } from './ed';
import { patternMatches } from './pattern';
import { ProtocolError, PrivateFeedPattern, AuthorizedKey } from './tuf';

/** RECOMMENDED maximum private-feed document size (spec/feeds.md §3). */
export const PRIVATE_FEED_MAX_BYTES = 1024 * 1024;

export function matchesPattern(pattern: PrivateFeedPattern, url: string): boolean {
  return patternMatches(pattern.pattern, url);
}

/** Resolve the entry's keys (key publication rule: keyid without key object → metadata error). */
function entryKeys(entry: PrivateFeedPattern): AuthorizedKey[] {
  const out: AuthorizedKey[] = [];
  for (const keyid of entry.keyids) {
    const key = entry.keys[keyid];
    if (!key) throw new ProtocolError(`private_feed_patterns.${entry.channel}: key object for ${keyid} missing`);
    if (key.keytype !== 'ed25519') throw new ProtocolError(`private_feed_patterns.${entry.channel}: unsupported keytype`);
    out.push({ keyid, pub: hexToBytes(key.keyval.public) });
  }
  return out;
}

export interface PrivateFeedDoc {
  version?: string;
  expired?: boolean;
  items?: unknown[];
  _sig?: {
    about?: string;
    channel?: string;
    url?: string;
    version?: number;
    expires?: string;
    signatures?: { keyid: string; sig: string; [extra: string]: unknown }[];
    [extra: string]: unknown;
  };
  [extra: string]: unknown;
}

export interface PrivateVerification {
  /** true when the feed is finished (`expired: true`) — stop polling, keep cache */
  closed: boolean;
  /** last seen version (write back for version memory) */
  version: number;
}

/**
 * Verify a fetched private feed document. `entry` is the authorized pattern
 * entry (already pattern-matched against `fetchedUrl`), `lastVersion` is the
 * client-side version memory. Throws ProtocolError on any verification
 * failure; the caller decides what counts as "not an error" (transport).
 */
export function verifyPrivateFeedDocument(
  doc: PrivateFeedDoc,
  entry: PrivateFeedPattern,
  fetchedUrl: string,
  lastVersion: number | undefined,
): PrivateVerification {
  const sig = doc._sig;
  if (!sig) throw new ProtocolError('private feed: missing top-level _sig');
  if (sig.channel !== entry.channel) {
    throw new ProtocolError(`private feed: _sig.channel "${sig.channel}" != pattern entry "${entry.channel}"`);
  }
  if (sig.url !== fetchedUrl) {
    throw new ProtocolError(`private feed: _sig.url "${sig.url}" != fetched URL "${fetchedUrl}"`);
  }
  const version = sig.version ?? 0;
  if (lastVersion !== undefined && version < lastVersion) {
    throw new ProtocolError(`private feed: _sig.version ${version} older than last seen ${lastVersion} (rollback?)`);
  }
  // whole-document signature: JCS of the doc with top-level _sig.signatures removed
  const clone = JSON.parse(JSON.stringify(doc)) as PrivateFeedDoc;
  if (clone._sig) delete clone._sig.signatures;
  const canonical = canonicalize(clone);
  if (canonical === undefined) throw new ProtocolError('private feed: not JCS-serializable');
  const canonicalBytes = new TextEncoder().encode(canonical);
  const sigs = sig.signatures ?? [];
  let valid = 0;
  const keys = entryKeys(entry);
  for (const s of sigs) {
    const key = keys.find((k) => k.keyid === s.keyid);
    if (!key) continue;
    try {
      if (ed25519Verify(base64urlToBytes(s.sig), canonicalBytes, key.pub)) valid++;
    } catch {
      // invalid encoding → not a valid signature
    }
  }
  if (valid < Math.max(1, entry.threshold || 1)) {
    throw new ProtocolError(`private feed: ${valid}/${entry.threshold || 1} valid whole-document signatures`);
  }
  return { closed: doc.expired === true, version };
}
