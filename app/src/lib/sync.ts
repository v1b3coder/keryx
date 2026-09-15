/**
 * Sync engine (spec/clients.md §1): TUF metadata chain (root anchor →
 * timestamp → snapshot → targets) → followed channels' delegated role metadata
 * (`channels.<name>.json`, the item index) → hash-pinned per-item TUF target
 * files → private capability feeds (whole-document verification) → verify every
 * item → store verified only. Anything that fails verification is never
 * displayed (binary rule); transport problems keep the cache and retry.
 */

import {
  loadAndVerifyMetadata,
  loadChannelRole,
  extractAuthorization,
  targetFileUrl,
  verifyTargetBytes,
  ChainBreakError,
  ProtocolError,
  type RootDoc,
  type TargetsDoc,
  type SeenVersions,
  type PrivateFeedPattern,
  type AuthorizedKey,
  type KeySet,
} from './tuf';
import {
  verifyItemSignatures,
  verifyImage,
  itemIdFromPath,
  signedContentKey,
  type FeedItem,
} from './item';
import { verifyPrivateFeedDocument, matchesPattern, PRIVATE_FEED_MAX_BYTES } from './private';
import { hexToBytes } from './bytes';
import { isLocalDevOrigin, linkedUrlAllowed } from './urlpolicy';
import {
  type CompanyRecord,
  type ChannelState,
  type StoredItem,
  itemKey,
  publicFeedKey,
  privateFeedKey,
} from './store';

export interface SyncOutcome {
  company: CompanyRecord;
  newItems: number;
  rejected: number;
  suspended: boolean;
  errors: string[];
  /** verified items to persist (added + content-updated) */
  toPut: StoredItem[];
  /** cached items dropped: unpublished or no longer verifying (binary rule) */
  toDelete: string[];
}

function asRecord(v: unknown): Record<string, unknown> {
  return typeof v === 'object' && v !== null ? (v as Record<string, unknown>) : {};
}

/** Channel display metadata from master-signed `custom.channels` (spec/repository.md §2). */
export function channelDisplay(
  targets: TargetsDoc,
  fallbackName: string,
): { displayName: string; description?: string } {
  const entry = targets.signed.custom?.channels?.[fallbackName];
  return {
    displayName: typeof entry?.display_name === 'string' ? entry.display_name : fallbackName,
    description: typeof entry?.description === 'string' ? entry.description : undefined,
  };
}

/** All public channel delegations (role name `channels.<name>`, spec/repository.md §2). */
function publicChannelRoles(targets: TargetsDoc): { roleName: string; channel: string }[] {
  return (targets.signed.delegations?.roles ?? [])
    .filter((r) => r.name.startsWith('channels.') && !r.name.endsWith('.authors'))
    .map((r) => ({ roleName: r.name, channel: r.name.slice('channels.'.length) }));
}

/**
 * Is this a local-dev origin? Loopback, or a private LAN address (RFC 1918).
 * The demo artifact is served over plain HTTP on a private network; the
 * protocol mandates HTTPS, so this is a documented dev-only exception.
 */
export { isLocalDevOrigin };

/** Private-feed transport rule (spec/feeds.md §3): HTTPS only — HTTP allowed on local-dev origins in debug builds only. */
export function privateFeedUrlAllowed(url: string): boolean {
  return linkedUrlAllowed(url);
}

function entryKeysOf(entry: PrivateFeedPattern): AuthorizedKey[] {
  const out: AuthorizedKey[] = [];
  for (const keyid of entry.keyids) {
    const key = entry.keys[keyid];
    if (!key) throw new ProtocolError(`private_feed_patterns.${entry.channel}: key object for ${keyid} missing`);
    out.push({ keyid, pub: hexToBytes(key.keyval.public) });
  }
  return out;
}

/** The key set authorizing an item in a channel (authors role or channel role). */
function authorizingKeys(
  targets: TargetsDoc,
  channel: string,
): { authors?: KeySet; channel: KeySet } {
  const auth = extractAuthorization(targets);
  return { authors: auth.authors.get(channel), channel: auth.channels.get(channel)! };
}

function toStored(
  company: CompanyRecord,
  channel: string,
  feedUrl: string,
  isPrivate: boolean,
  item: FeedItem,
  hash: string | undefined,
  prev: StoredItem | undefined,
): StoredItem {
  const feedKey = isPrivate ? privateFeedKey(feedUrl) : publicFeedKey(channel);
  return {
    id: itemKey(company.origin, feedKey, item.id!),
    origin: company.origin,
    channel,
    feedUrl,
    isPrivate,
    item,
    published: item.date_published ?? '',
    hash,
    receivedAt: prev?.receivedAt ?? Date.now(),
    read: prev?.read ?? false,
    updated: prev ? signedContentKey(item) !== signedContentKey(prev.item) : false,
  };
}

/**
 * Merge the fetched item set of one source. The channel role metadata IS the
 * index (spec/feeds.md §1.3): an item absent from it is unpublished and is
 * dropped from display and cache. Items that fail verification are dropped too,
 * including ones previously displayed (binary rule).
 */
export function mergeSourceItems(
  company: CompanyRecord,
  channel: string,
  feedUrl: string,
  isPrivate: boolean,
  items: { item: FeedItem; hash?: string }[],
  authorizing: { authors?: KeySet; channel: KeySet },
  existing: Map<string, StoredItem>,
): { newItems: number; rejected: number; toPut: StoredItem[]; toDelete: string[]; seen: Set<string> } {
  const feedKey = isPrivate ? privateFeedKey(feedUrl) : publicFeedKey(channel);
  let newItems = 0;
  let rejected = 0;
  const toPut: StoredItem[] = [];
  const toDelete: string[] = [];
  const seen = new Set<string>();
  for (const { item, hash } of items) {
    if (!item.id) continue;
    const key = itemKey(company.origin, feedKey, item.id);
    seen.add(key);
    try {
      verifyImage(item);
      verifyItemSignatures(item, authorizing.authors, authorizing.channel);
    } catch {
      rejected++;
      if (existing.has(key)) {
        existing.delete(key);
        toDelete.push(key);
      }
      continue;
    }
    const prev = existing.get(key);
    if (prev && prev.hash === hash && signedContentKey(prev.item) === signedContentKey(item)) {
      continue; // unchanged and already verified
    }
    if (!prev) newItems++;
    const stored = toStored(company, channel, feedUrl, isPrivate, item, hash, prev);
    existing.set(key, stored);
    toPut.push(stored);
  }
  // absence from the index = unpublished: drop cached items of this source
  for (const [key, cached] of existing) {
    if (cached.origin !== company.origin || cached.isPrivate !== isPrivate) continue;
    if (isPrivate ? cached.feedUrl !== feedUrl : cached.channel !== channel) continue;
    if (!seen.has(key)) {
      existing.delete(key);
      toDelete.push(key);
    }
  }
  return { newItems, rejected, toPut, toDelete, seen };
}

export async function syncCompany(
  company: CompanyRecord,
  fetchFn: typeof fetch = fetch,
  existingItems?: Map<string, StoredItem>,
): Promise<SyncOutcome> {
  const errors: string[] = [];
  let suspended = false;
  let newItems = 0;
  let rejected = 0;

  // --- 1. metadata chain (anchor → timestamp → snapshot → targets) --------
  const anchorUrl = company.origin + '/.well-known/keryx/root.json';
  let meta: Awaited<ReturnType<typeof loadAndVerifyMetadata>>;
  try {
    meta = await loadAndVerifyMetadata(fetchFn, anchorUrl, company.pinnedRoot ?? null, company.seen ?? null);
  } catch (err) {
    if (err instanceof ChainBreakError || err instanceof ProtocolError) {
      suspended = true;
      errors.push(err.message);
    } else {
      errors.push(err instanceof Error ? err.message : String(err));
    }
    return {
      company: {
        ...company,
        status: suspended ? 'suspended' : company.status,
        suspendedReason: suspended ? errors[0] : company.suspendedReason,
        lastSyncAt: Date.now(),
        lastSyncErrors: errors.slice(0, 8),
      },
      newItems: 0,
      rejected: 0,
      suspended,
      errors,
      toPut: [],
      toDelete: [],
    };
  }

  const root = meta.root;
  const targets = meta.targets;
  const auth = extractAuthorization(targets);
  const consistent = root.signed.consistent_snapshot === true;

  // --- 2. company identity (spec/core.md §2): name change → re-pair; logo → one-tap ack
  const companyName =
    typeof targets.signed.custom?.company_name === 'string' ? targets.signed.custom.company_name : undefined;
  const logo = typeof targets.signed.custom?.logo === 'string' ? targets.signed.custom.logo : undefined;
  let rebrandPending = company.rebrandPending ?? false;
  let logoChangePending = company.logoChangePending ?? false;
  if (companyName !== undefined && companyName !== company.identity.companyName) rebrandPending = true;
  if (logo !== undefined && logo !== company.identity.logo) logoChangePending = true;

  // --- 3. channel role metadata (verified; pins the item index) -----------
  const roles: Map<string, TargetsDoc> = new Map();
  for (const { roleName, channel } of publicChannelRoles(targets)) {
    try {
      const role = await loadChannelRole(fetchFn, meta, roleName, consistent, company.seen?.roles?.[roleName]);
      roles.set(roleName, role);
    } catch (err) {
      if (err instanceof ChainBreakError || err instanceof ProtocolError) {
        suspended = true;
        errors.push(err.message);
        break;
      }
      errors.push(`channel ${channel}: ${err instanceof Error ? err.message : String(err)}`);
    }
  }

  const roleVersions: Record<string, number> = { ...(company.seen?.roles ?? {}) };
  for (const [roleName, role] of roles) roleVersions[roleName] = role.signed.version;

  // new channels surface as "new"; existing keep their followed state
  const channels: ChannelState[] = publicChannelRoles(targets).map(({ channel }) => {
    const prev = company.channels.find((c) => c.name === channel);
    const display = channelDisplay(targets, channel);
    return prev
      ? { ...prev, ...display, isNew: false }
      : { ...display, name: channel, followed: false, isNew: true };
  });

  const seen: SeenVersions = {
    timestamp: meta.timestamp.signed.version,
    snapshot: meta.snapshot.signed.version,
    targets: targets.signed.version,
    roles: roleVersions,
  };

  const updated: CompanyRecord = {
    ...company,
    rebrandPending,
    logoChangePending,
    pinnedRoot: root,
    pinnedRootVersion: root.signed.version,
    targets,
    targetsVersion: targets.signed.version,
    seen,
    channels,
    status: 'active',
    lastSyncAt: Date.now(),
  };

  if (suspended) {
    updated.status = 'suspended';
    updated.suspendedReason = errors[0];
    return { company: updated, newItems, rejected, suspended, errors, toPut: [], toDelete: [] };
  }

  // Expired-but-verified metadata → no new content, cached content kept
  // (expiry ≠ suspension, spec/repository.md §5). Retried on the next sync.
  if (meta.stale) {
    errors.push('Metadata expired. Showing saved messages; will retry later.');
    return {
      company: { ...updated, lastSyncErrors: errors.slice(0, 8) },
      newItems,
      rejected,
      suspended,
      errors,
      toPut: [],
      toDelete: [],
    };
  }

  const existing = existingItems ?? new Map<string, StoredItem>();
  const toPut: StoredItem[] = [];
  const toDelete: string[] = [];

  // --- 4. public channels: one hash-pinned item file per TUF target ------
  const followed = new Set(updated.channels.filter((c) => c.followed).map((c) => c.name));
  const base = meta.base;
  for (const { roleName, channel } of publicChannelRoles(targets)) {
    if (!followed.has(channel) || !roles.has(roleName)) continue;
    const channelState = updated.channels.find((c) => c.name === channel);
    if (channelState?.closed) continue;
    const role = roles.get(roleName)!;
    const authorizing = authorizingKeys(targets, channel);
    const feedKey = publicFeedKey(channel);
    const paths = Object.keys(role.signed.targets);
    const present = new Set<string>();
    for (const path of paths) {
      const info = role.signed.targets[path];
      if (!info) continue;
      const id = itemIdFromPath(path);
      const key = itemKey(updated.origin, feedKey, id);
      present.add(key);
      const prev = existing.get(key);
      const wantHash = info.hashes?.sha256;
      if (prev && wantHash && prev.hash === wantHash) continue; // unchanged, already verified
      const url = targetFileUrl(base, path, info, consistent);
      try {
        const res = await fetchFn(url, { cache: 'no-cache' });
        if (!res.ok) {
          errors.push(`item ${path}: HTTP ${res.status}`);
          continue;
        }
        const bytes = new Uint8Array(await res.arrayBuffer());
        verifyTargetBytes(bytes, info, path); // length + sha256 pinning
        const item = JSON.parse(new TextDecoder().decode(bytes)) as FeedItem;
        if (item.id !== id) {
          throw new ProtocolError(`item ${path}: id "${item.id}" != path segment "${id}"`);
        }
        verifyImage(item);
        verifyItemSignatures(item, authorizing.authors, authorizing.channel);
        const stored = toStored(updated, channel, '', false, item, wantHash, prev);
        if (!prev) newItems++;
        existing.set(key, stored);
        toPut.push(stored);
      } catch (err) {
        if (err instanceof ProtocolError) {
          rejected++;
          errors.push(`channel ${channel}: ${err.message}`);
        } else {
          errors.push(`channel ${channel}: ${err instanceof Error ? err.message : String(err)}`);
        }
      }
    }
    // absence = unpublished: drop cached items no longer in the index
    for (const [key, cached] of existing) {
      if (cached.origin !== updated.origin || cached.channel !== channel || cached.isPrivate) continue;
      if (!present.has(key)) {
        existing.delete(key);
        toDelete.push(key);
      }
    }
  }

  // --- 5. private capability feeds (auto-subscribed from the join payload) -
  for (const sub of updated.privateFeeds) {
    const entry = auth.privatePatterns.find((p) => matchesPattern(p, sub.url));
    if (!entry) {
      sub.closed = true; // pattern removed (master-signed) — stop syncing, keep cached items
      errors.push(`delivery feed no longer available: ${sub.url}`);
      continue;
    }
    if (sub.closed) continue; // expired/closed — keep cached items, stop polling
    if (!privateFeedUrlAllowed(sub.url)) {
      errors.push(`delivery feed skipped (not https): ${sub.url}`);
      continue;
    }
    try {
      const res = await fetchFn(sub.url, { cache: 'no-cache' });
      if (res.status === 404 || res.status === 410) {
        sub.closed = true; // gone — keep cached items, stop polling
        errors.push(`delivery feed closed: HTTP ${res.status}`);
        continue;
      }
      if (!res.ok) {
        errors.push(`delivery feed ${sub.url}: HTTP ${res.status}`);
        continue;
      }
      const bytes = new Uint8Array(await res.arrayBuffer());
      if (bytes.length > PRIVATE_FEED_MAX_BYTES) {
        errors.push(`delivery feed too large (${bytes.length} bytes), aborted`);
        continue;
      }
      const doc = JSON.parse(new TextDecoder().decode(bytes)) as never;
      const verification = verifyPrivateFeedDocument(doc, entry, sub.url, sub.version);
      sub.version = verification.version;
      const docExpires = (doc as { expires?: string }).expires;
      if (docExpires) sub.expires = docExpires;
      if (verification.closed) sub.closed = true;
      const privateItems = ((doc as { items?: FeedItem[] }).items ?? []).map((item) => ({
        item,
        hash: undefined,
      }));
      const merged = mergeSourceItems(
        updated,
        sub.channel,
        sub.url,
        true,
        privateItems,
        { authors: undefined, channel: { keys: entryKeysOf(entry), threshold: Math.max(1, entry.threshold || 1) } },
        existing,
      );
      newItems += merged.newItems;
      rejected += merged.rejected;
      toPut.push(...merged.toPut);
      toDelete.push(...merged.toDelete);
    } catch (err) {
      if (err instanceof ProtocolError) {
        rejected++;
        errors.push(`delivery feed: ${err.message}`);
      } else {
        errors.push(`delivery feed ${sub.url}: ${err instanceof Error ? err.message : String(err)}`);
      }
    }
  }

  updated.privateFeeds = updated.privateFeeds.map((sub) => ({ ...sub }));
  updated.lastSyncErrors = errors.slice(0, 8);
  updated.lastSyncAt = Date.now();
  return { company: updated, newItems, rejected, suspended, errors, toPut, toDelete };
}
