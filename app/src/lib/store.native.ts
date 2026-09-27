/**
 * Local-only persistence for iOS and Android: a SQLite key-value store
 * (expo-sqlite/kv-store). Same API as the web IndexedDB store
 * (`store.ts`), so the protocol core never knows which target it runs on.
 * Nothing here ever leaves the device (zero PII, no server state).
 */

import Storage from 'expo-sqlite/kv-store';
import { base64urlToBytes, bytesToBase64url } from './bytes';
import type { RootDoc, TargetsDoc, SeenVersions } from './tuf';
import type { FeedItem } from './item';
import type { RelayRegistration } from './relay';

export interface ChannelState {
  /** bare channel name (the item target path segment) */
  name: string;
  displayName: string;
  description?: string;
  followed: boolean;
  /** seen for the first time since this company was paired ("new" badge) */
  isNew?: boolean;
  /** feed-level `expired: true` → finished: no further updates, cache kept */
  closed?: boolean;
}

/** A private (per-order) capability feed subscription (spec/feeds.md §3). */
export interface PrivateFeedSub {
  url: string;
  /** the pattern entry's channel (label only — NOT a TUF role) */
  channel: string;
  displayName?: string;
  purpose?: string;
  /** last seen document `version` (anti-rollback via version memory) */
  version?: number;
  /** document `expires` — the order window end (anti-freeze) */
  expires?: string;
  /** expired/404/410/pattern removed → stop polling, keep cached items */
  closed?: boolean;
}

export interface CompanyRecord {
  /** key: the join origin (the user-confirmed domain — the trust anchor) */
  origin: string;
  joinUrl: string;
  /** company identity as confirmed at pairing (spec/core.md §2) */
  identity: { companyName?: string; logo?: string; logoSHA256?: string };
  /** company_name changed since pairing → prominent warning, re-pair required */
  rebrandPending?: boolean;
  /** logo changed since pairing → one-tap acknowledgement */
  logoChangePending?: boolean;
  pinnedRoot: RootDoc;
  pinnedRootVersion: number;
  targets: TargetsDoc;
  targetsVersion: number;
  /** anti-rollback version memory */
  seen: SeenVersions;
  channels: ChannelState[];
  privateFeeds: PrivateFeedSub[];
  status: 'active' | 'suspended' | 'rebrand';
  suspendedReason?: string;
  joinedAt: number;
  lastSyncAt: number | null;
  /** transient sync problems (feed fetch failures etc.) — shown to the user, never suspension */
  lastSyncErrors?: string[];
  prefs: { languages: string[]; tags: string[]; loadRemoteMedia: boolean };
}

export interface StoredItem {
  /** dedup key: `${origin}\0${feedKey}\0${itemId}` — feedKey = `public:<channel>` or `private:<url>` */
  id: string;
  origin: string;
  /** bare channel name (public) or pattern channel label (private) */
  channel: string;
  /** private capability URL (empty for public items) */
  feedUrl: string;
  isPrivate: boolean;
  item: FeedItem;
  published: string;
  /** TUF target sha256 of the signed item bytes (public channels) */
  hash?: string;
  /** position in the feed document (ordering fallback when date_published is absent) */
  feedIndex?: number;
  receivedAt: number;
  read: boolean;
  /** in-place update: content differs from the previous signed copy */
  updated?: boolean;
}

export interface Prefs {
  languages: string[];
  tags: string[];
  loadRemoteMedia: boolean;
}

export const defaultPrefs = (): Prefs => ({
  languages: [],
  tags: [],
  loadRemoteMedia: true,
});

const COMPANY_PREFIX = 'company:';
const ITEM_PREFIX = 'item:';
const MEDIA_PREFIX = 'media:';
const RELAY_PREFIX = 'relay:';
const REG_PREFIX = 'registration:';

/**
 * SQLite stores TEXT as a C string: a NUL byte terminates it, so a key like
 * `item:<origin>\0public:<channel>\0<id>` would silently collapse to
 * `item:<origin>` and every item would overwrite the previous one. Escape the NUL
 * separators before they reach the store (and unescape on the way out).
 */
const KEY_ESCAPE = '\u241f';
function encodeKey(key: string): string {
  return key.split('\u0000').join(KEY_ESCAPE);
}
function decodeKey(key: string): string {
  return key.split(KEY_ESCAPE).join('\u0000');
}

async function readJson<T>(key: string): Promise<T | undefined> {
  const raw = await Storage.getItemAsync(encodeKey(key));
  if (raw === null) return undefined;
  try {
    return JSON.parse(raw) as T;
  } catch {
    return undefined;
  }
}

async function writeJson(key: string, value: unknown): Promise<void> {
  await Storage.setItemAsync(encodeKey(key), JSON.stringify(value));
}

async function removeKey(key: string): Promise<void> {
  await Storage.removeItemAsync(encodeKey(key));
}

async function keysWithPrefix(prefix: string): Promise<string[]> {
  const encoded = encodeKey(prefix);
  const keys = await Storage.getAllKeysAsync();
  return keys.filter((k) => k.startsWith(encoded)).map(decodeKey);
}

// --- companies ---

export async function getAllCompanies(): Promise<CompanyRecord[]> {
  const keys = await keysWithPrefix(COMPANY_PREFIX);
  const list = (await Promise.all(keys.map((k) => readJson<CompanyRecord>(k)))).filter(
    (c): c is CompanyRecord => !!c,
  );
  return list.sort((a, b) => a.joinedAt - b.joinedAt);
}

export async function getCompany(origin: string): Promise<CompanyRecord | undefined> {
  return readJson<CompanyRecord>(COMPANY_PREFIX + origin);
}

export async function putCompany(company: CompanyRecord): Promise<void> {
  await writeJson(COMPANY_PREFIX + company.origin, company);
}

export async function deleteCompany(origin: string): Promise<void> {
  await removeKey(COMPANY_PREFIX + origin);
  for (const key of await keysWithPrefix(ITEM_PREFIX)) {
    const item = await readJson<StoredItem>(key);
    if (item?.origin === origin) await removeKey(key);
  }
  for (const key of await keysWithPrefix(MEDIA_PREFIX)) {
    const media = await readJson<StoredMedia>(key);
    if (media?.origin === origin) await removeKey(key);
  }
}

// --- app-wide relay registration (relay/SPECIFICATION.md §5.3) ---

export async function getRegistration(baseUrl: string): Promise<RelayRegistration | undefined> {
  return readJson<RelayRegistration>(REG_PREFIX + baseUrl);
}

export async function getRegistrations(): Promise<RelayRegistration[]> {
  const keys = await keysWithPrefix(REG_PREFIX);
  const list = await Promise.all(keys.map((k) => readJson<RelayRegistration>(k)));
  return list.filter((r): r is RelayRegistration => !!r);
}

export async function putRegistration(reg: RelayRegistration): Promise<void> {
  await writeJson(REG_PREFIX + reg.baseUrl, reg);
}

export async function deleteRegistrationRecord(baseUrl: string): Promise<void> {
  await removeKey(REG_PREFIX + baseUrl);
}

// --- items ---

export function itemKey(origin: string, feedKey: string, itemId: string): string {
  return `${origin}\u0000${feedKey}\u0000${itemId}`;
}

export function publicFeedKey(channel: string): string {
  return `public:${channel}`;
}

export function privateFeedKey(url: string): string {
  return `private:${url}`;
}

export async function getAllItems(): Promise<StoredItem[]> {
  const keys = await keysWithPrefix(ITEM_PREFIX);
  const entries = await Promise.all(
    keys.map(async (key) => ({ key, item: await readJson<StoredItem>(key) })),
  );
  // A key must match the item's own id: a legacy key truncated at a NUL byte
  // (written before the key escaping existed) would otherwise surface a duplicate.
  return entries
    .filter((e): e is { key: string; item: StoredItem } => !!e.item && e.key === ITEM_PREFIX + e.item.id)
    .map((e) => e.item);
}

export async function getItems(origin: string): Promise<StoredItem[]> {
  return (await getAllItems()).filter((i) => i.origin === origin);
}

export async function getItem(origin: string, feedKey: string, itemId: string): Promise<StoredItem | undefined> {
  return readJson<StoredItem>(ITEM_PREFIX + itemKey(origin, feedKey, itemId));
}

export async function putItems(items: StoredItem[]): Promise<void> {
  for (const item of items) await writeJson(ITEM_PREFIX + item.id, item);
}

export async function deleteItems(ids: string[]): Promise<void> {
  for (const id of ids) await removeKey(ITEM_PREFIX + id);
}

export async function markRead(origin: string, feedKey: string, itemId: string, read: boolean): Promise<void> {
  const key = ITEM_PREFIX + itemKey(origin, feedKey, itemId);
  const item = await readJson<StoredItem>(key);
  if (item) await writeJson(key, { ...item, read });
}

// --- media cache (images/logo bytes keyed by URL) ---

export interface CachedMedia {
  url: string;
  origin: string;
  bytes: ArrayBuffer;
  mime: string;
  at: number;
}

/** Media bytes are stored as base64url text; JSON cannot carry an ArrayBuffer. */
interface StoredMedia {
  url: string;
  origin: string;
  bytes: string;
  mime: string;
  at: number;
}

export async function getMedia(url: string): Promise<CachedMedia | undefined> {
  const rec = await readJson<StoredMedia>(MEDIA_PREFIX + url);
  if (!rec) return undefined;
  try {
    return { ...rec, bytes: toArrayBuffer(base64urlToBytes(rec.bytes)) };
  } catch {
    // a corrupt cache entry is a cache miss, never a crash: the caller refetches
    return undefined;
  }
}

export async function putMedia(entry: CachedMedia): Promise<void> {
  await writeJson(MEDIA_PREFIX + entry.url, {
    ...entry,
    bytes: bytesToBase64url(new Uint8Array(entry.bytes)),
  });
}

export async function deleteMediaFor(origin: string): Promise<void> {
  for (const key of await keysWithPrefix(MEDIA_PREFIX)) {
    const media = await readJson<StoredMedia>(key);
    if (media?.origin === origin) await removeKey(key);
  }
}

function toArrayBuffer(bytes: Uint8Array): ArrayBuffer {
  const out = new ArrayBuffer(bytes.byteLength);
  new Uint8Array(out).set(bytes);
  return out;
}

// --- prefs (per company, stored on the company record) ---

// --- relay wake-up state (relay/SPECIFICATION.md §4.2) -----------------------

interface RelayStateRecord {
  key: string;
  seq?: number;
  at?: number;
}

async function putRelayState(rec: RelayStateRecord): Promise<void> {
  await writeJson(RELAY_PREFIX + rec.key, rec);
}

async function getRelayState<T = RelayStateRecord>(key: string): Promise<T | undefined> {
  return readJson<T>(RELAY_PREFIX + key);
}

/** The last accepted `seq` for a topic (zero when never accepted). */
export async function relaySeq(origin: string, topic: string): Promise<number> {
  const rec = await getRelayState(`seq\u0000${origin}\u0000${topic}`);
  return rec?.seq ?? 0;
}

/** Persist the last accepted `seq` for a topic (verified wake-ups only). */
export async function setRelaySeq(origin: string, topic: string, seq: number): Promise<void> {
  await putRelayState({ key: `seq\u0000${origin}\u0000${topic}`, seq });
}

/** The recovery-cooldown expiry for a company (0 when never attempted). */
export async function relayRecoveryAt(origin: string): Promise<number> {
  const rec = await getRelayState(`recovery\u0000${origin}`);
  return rec?.at ?? 0;
}

/**
 * Atomically reserve the one recovery allowance per company per cooldown
 * (relay/SPECIFICATION.md §4.2): persist `next_recovery_at` BEFORE
 * networking. Returns false when the allowance is still in force.
 *
 * The native JS runtime is single-threaded, but two async callers can still
 * interleave at awaits, so the read-check-write runs under a promise lock.
 */
let recoveryLock: Promise<unknown> = Promise.resolve();

export async function reserveRecovery(origin: string, now: number, cooldownMs: number): Promise<boolean> {
  const run = recoveryLock.then(async () => {
    const key = `recovery\u0000${origin}`;
    const rec = await getRelayState(key);
    if (rec?.at && now < rec.at) return false;
    await putRelayState({ key, at: now + cooldownMs });
    return true;
  });
  recoveryLock = run.catch(() => undefined);
  return run;
}

/** The last wake-up this install accepted for a company (epoch ms). */
export async function markPushReceived(origin: string, at: number): Promise<void> {
  await putRelayState({ key: `push\u0000${origin}`, at });
}

export async function lastPushAt(origin: string): Promise<number> {
  const rec = await getRelayState(`push\u0000${origin}`);
  return rec?.at ?? 0;
}

/** A wake-up the worker could not verify against its cached metadata. */
export interface PendingRecovery {
  origin: string;
  topic: string;
  seq: number;
  at: number;
}

/**
 * Record a wake-up the worker could not verify: the cached metadata may simply
 * be stale after a key rotation, so the page re-verifies with the full TUF
 * state and its recovery allowance (design/notifications.md, worker rule).
 */
export async function markPendingRecovery(pending: PendingRecovery): Promise<void> {
  await putRelayState({ key: `recovery-pending\u0000${pending.origin}`, ...pending });
}

export async function pendingRecoveries(): Promise<PendingRecovery[]> {
  const keys = await keysWithPrefix(RELAY_PREFIX + 'recovery-pending\u0000');
  const list = await Promise.all(keys.map((k) => getRelayState<PendingRecovery>(k.slice(RELAY_PREFIX.length))));
  return list.filter((r): r is PendingRecovery => !!r);
}

export async function clearPendingRecovery(origin: string): Promise<void> {
  await removeKey(RELAY_PREFIX + `recovery-pending\u0000${origin}`);
}

/** The pending self-test the service worker matches by nonce (§5.3.1). */
export interface PendingTest {
  baseUrl: string;
  nonce: string;
  expiresAt: number;
  /** the topic-leg test topic the native SDK is subscribed to while it is pending */
  topic?: string;
  receivedAt?: number;
}

export async function putPendingTest(t: PendingTest): Promise<void> {
  await putRelayState({ key: `test\u0000${t.baseUrl}`, ...t });
}

export async function pendingTest(baseUrl: string): Promise<PendingTest | undefined> {
  const rec = await getRelayState<PendingTest & { key?: string }>(`test\u0000${baseUrl}`);
  if (!rec) return undefined;
  // the stored record carries the relay-state key; the API returns the pending
  // test itself (the web store strips it too)
  const { key: _key, ...test } = rec;
  return test;
}

export async function clearPendingTest(baseUrl: string): Promise<void> {
  await removeKey(RELAY_PREFIX + `test\u0000${baseUrl}`);
}

export function makeCompany(
  origin: string,
  joinUrl: string,
  pinnedRoot: RootDoc,
  targets: TargetsDoc,
  identity: { companyName?: string; logo?: string; logoSHA256?: string },
  channels: ChannelState[],
  privateFeeds: PrivateFeedSub[],
): CompanyRecord {
  return {
    origin,
    joinUrl,
    identity,
    pinnedRoot,
    pinnedRootVersion: pinnedRoot.signed.version,
    targets,
    targetsVersion: targets.signed.version,
    seen: {
      timestamp: undefined,
      snapshot: undefined,
      targets: targets.signed.version,
      roles: {},
    },
    channels,
    privateFeeds,
    status: 'active',
    joinedAt: Date.now(),
    lastSyncAt: null,
    prefs: defaultPrefs(),
  };
}
