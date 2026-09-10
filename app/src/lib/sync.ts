/**
 * Sync engine (spec/clients.md §1): TUF metadata chain (root anchor →
 * timestamp → snapshot → targets) → followed channels' delegated role
 * metadata (`channels.<name>.json`) → hash-pinned public feed target files →
 * private capability feeds (whole-document verification) → verify every item
 * → store verified only. Anything that fails verification is never displayed
 * (binary rule); transport problems keep the cache and retry.
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
  type EditorAuth,
} from './tuf';
import {
  verifyItemSignatures,
  verifyChannelCrossCheck,
  isWithdrawn,
  signedContentKey,
  type FeedDoc,
  type FeedItem,
} from './item';
import { verifyPrivateFeedDocument, matchesPattern, PRIVATE_FEED_MAX_BYTES } from './private';
import { hexToBytes } from './bytes';
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
  /** cached items dropped: withdrawn or no longer verifying (binary rule) */
  toDelete: string[];
}

function asRecord(v: unknown): Record<string, unknown> {
  return typeof v === 'object' && v !== null ? (v as Record<string, unknown>) : {};
}

/** Channel display metadata from the role metadata's target custom (spec/repository.md §3). */
export function channelDisplay(
  role: TargetsDoc | null,
  fallbackName: string,
): { displayName: string; description?: string } {
  const first = Object.values(role?.signed.targets ?? {})[0];
  const custom = asRecord(first?.custom);
  return {
    displayName: typeof custom.display_name === 'string' ? custom.display_name : fallbackName,
    description: typeof custom.description === 'string' ? custom.description : undefined,
  };
}

/** All public channel delegations (role name `channels.<name>`, spec/repository.md §2). */
function publicChannelRoles(targets: TargetsDoc): { roleName: string; channel: string }[] {
  return (targets.signed.delegations?.roles ?? [])
    .filter((r) => r.name.startsWith('channels.'))
    .map((r) => ({ roleName: r.name, channel: r.name.slice('channels.'.length) }));
}

/** Is this origin a loopback dev origin (the local demo is served over http)? */
export function isLoopback(origin: string): boolean {
  try {
    const u = new URL(origin);
    return u.hostname === 'localhost' || u.hostname === '127.0.0.1' || u.hostname === '::1';
  } catch {
    return false;
  }
}

/** HTTPS-only for private feeds (spec/feeds.md §3) — loopback allowed for the local demo. */
export function privateFeedUrlAllowed(url: string): boolean {
  try {
    const u = new URL(url);
    if (u.protocol === 'https:') return true;
    if (u.protocol === 'http:' && isLoopback(u.origin)) return true;
    return false;
  } catch {
    return false;
  }
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

interface FeedOutcome {
  newItems: number;
  rejected: number;
}

/**
 * Verify and merge feed items for one source (public channel or private
 * capability feed). Items that fail verification are rejected — never
 * displayed. Withdrawn items are hidden entirely (not counted as unread).
 * Returns StoredItem records to persist (added + content-updated).
 */
export function processFeedItems(
  company: CompanyRecord,
  channel: string,
  feedUrl: string,
  isPrivate: boolean,
  feedItems: FeedItem[],
  channelKeys: AuthorizedKey[],
  editor: EditorAuth | undefined,
  existing: Map<string, StoredItem>,
): { outcome: FeedOutcome; toPut: StoredItem[]; toDelete: string[] } {
  const feedKey = isPrivate ? privateFeedKey(feedUrl) : publicFeedKey(channel);
  let newItems = 0;
  let rejected = 0;
  const toPut: StoredItem[] = [];
  const toDelete: string[] = [];
  const seen = new Set<string>();
  feedItems.forEach((item, feedIndex) => {
    if (!item.id) return; // id is the dedup key; items without it are ignored
    const key = itemKey(company.origin, feedKey, item.id);
    seen.add(key);
    if (isWithdrawn(item)) {
      // withdrawn (spec/feeds.md §1.2): hidden entirely — removed from display
      if (existing.has(key)) {
        existing.delete(key);
        toDelete.push(key);
      }
      return;
    }
    try {
      verifyChannelCrossCheck(item, channel);
      verifyItemSignatures(item, editor, channelKeys);
    } catch {
      // binary rule: a failing item is dropped, including one previously displayed
      rejected++;
      if (existing.has(key)) {
        existing.delete(key);
        toDelete.push(key);
      }
      return;
    }
    const prev = existing.get(key);
    const published = item.date_published ?? '';
    const stored: StoredItem = {
      id: key,
      origin: company.origin,
      channel,
      feedUrl,
      isPrivate,
      item,
      published,
      feedIndex,
      receivedAt: prev?.receivedAt ?? Date.now(),
      read: prev?.read ?? false,
      updated: prev ? signedContentKey(item) !== signedContentKey(prev.item) : false,
    };
    if (!prev) newItems++;
    existing.set(key, stored);
    toPut.push(stored);
  });
  void seen;
  return { outcome: { newItems, rejected }, toPut, toDelete };
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

  // --- 3. channel role metadata (verified; pins feeds + display metadata) --
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
    const display = channelDisplay(roles.get(`channels.${channel}`) ?? null, channel);
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

  // --- 4. public feeds: hash-pinned targets from each followed channel -----
  const followed = new Set(updated.channels.filter((c) => c.followed).map((c) => c.name));
  const base = meta.base;
  for (const { roleName, channel } of publicChannelRoles(targets)) {
    if (!followed.has(channel) || !roles.has(roleName)) continue;
    const channelState = updated.channels.find((c) => c.name === channel);
    if (channelState?.closed) continue; // feed finished — stop syncing, keep cache
    const role = roles.get(roleName)!;
    const path = `channels/${channel}/feed.json`;
    const info = role.signed.targets[path];
    if (!info) {
      errors.push(`channel ${channel}: role metadata does not pin ${path}`);
      continue;
    }
    const url = targetFileUrl(base, path, info, consistent);
    try {
      const res = await fetchFn(url, { cache: 'no-cache' });
      if (!res.ok) {
        errors.push(`feed ${url}: HTTP ${res.status}`);
        continue;
      }
      const bytes = new Uint8Array(await res.arrayBuffer());
      verifyTargetBytes(bytes, info, path); // length + sha256 pinning
      const doc = JSON.parse(new TextDecoder().decode(bytes)) as FeedDoc;
      const { outcome, toPut: puts, toDelete: dels } = processFeedItems(
        updated,
        channel,
        '',
        false,
        doc.items ?? [],
        auth.channels.get(channel)?.keys ?? [],
        auth.editor.get(channel),
        existing,
      );
      newItems += outcome.newItems;
      rejected += outcome.rejected;
      toPut.push(...puts);
      toDelete.push(...dels);
      if (doc.expired === true) {
        const st = updated.channels.find((c) => c.name === channel);
        if (st) st.closed = true;
      }
    } catch (err) {
      if (err instanceof ProtocolError) {
        rejected++;
        errors.push(`channel ${channel}: ${err.message}`);
      } else {
        errors.push(`channel ${channel}: ${err instanceof Error ? err.message : String(err)}`);
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
      const doc = JSON.parse(new TextDecoder().decode(bytes)) as FeedDoc;
      const verification = verifyPrivateFeedDocument(
        doc as never,
        entry,
        sub.url,
        sub.version,
      );
      sub.version = verification.version;
      sub.expires =
        (doc._sig as { expires?: string } | undefined)?.expires ?? sub.expires;
      if (verification.closed) sub.closed = true;
      const { outcome, toPut: puts, toDelete: dels } = processFeedItems(
        updated,
        sub.channel,
        sub.url,
        true,
        doc.items ?? [],
        entryKeysOf(entry),
        undefined,
        existing,
      );
      newItems += outcome.newItems;
      rejected += outcome.rejected;
      toPut.push(...puts);
      toDelete.push(...dels);
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
