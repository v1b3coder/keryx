/**
 * Local-only persistence: IndexedDB for companies + verified items + media.
 * Nothing here ever leaves the device (zero PII, no server state).
 */

import { openDB, type IDBPDatabase } from 'idb';
import type { RootDoc, TargetsDoc, SeenVersions } from './tuf';
import type { FeedItem } from './item';

export interface ChannelState {
  /** bare channel name (`_sig.channel`, path segment) */
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
  /** last seen `_sig.version` (anti-rollback via version memory) */
  version?: number;
  /** `_sig.expires` — the order window end (anti-freeze) */
  expires?: string;
  /** expired/404/410/pattern removed → stop polling, keep cached items */
  closed?: boolean;
}

export interface CompanyRecord {
  /** key: the join origin (the user-confirmed domain — the trust anchor) */
  origin: string;
  joinUrl: string;
  /** company identity as confirmed at pairing (spec/core.md §2) */
  identity: { companyName?: string; logo?: string };
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

const DB_NAME = 'keryx';
const DB_VERSION = 3;

let dbPromise: Promise<IDBPDatabase> | null = null;

export function openAppDb(): Promise<IDBPDatabase> {
  if (!dbPromise) {
    dbPromise = openDB(DB_NAME, DB_VERSION, {
      upgrade(db, oldVersion) {
        // This is a fresh implementation: previous layouts (the earlier web
        // client, or pre-rewrite shapes) are incompatible — drop and rebuild
        // rather than attempt an unreliable migration.
        if (oldVersion > 0) {
          for (const name of ['contacts', 'items', 'media', 'companies']) {
            if (db.objectStoreNames.contains(name)) db.deleteObjectStore(name);
          }
        }
        db.createObjectStore('companies', { keyPath: 'origin' });
        const items = db.createObjectStore('items', { keyPath: 'id' });
        items.createIndex('by-origin', 'origin');
        const media = db.createObjectStore('media', { keyPath: 'url' });
        media.createIndex('by-origin', 'origin');
      },
    });
  }
  return dbPromise;
}

// --- companies ---

export async function getAllCompanies(): Promise<CompanyRecord[]> {
  const db = await openAppDb();
  const list = (await db.getAll('companies')) as CompanyRecord[];
  return list.sort((a, b) => a.joinedAt - b.joinedAt);
}

export async function getCompany(origin: string): Promise<CompanyRecord | undefined> {
  const db = await openAppDb();
  return (await db.get('companies', origin)) as CompanyRecord | undefined;
}

export async function putCompany(company: CompanyRecord): Promise<void> {
  const db = await openAppDb();
  await db.put('companies', company);
}

export async function deleteCompany(origin: string): Promise<void> {
  const db = await openAppDb();
  await db.delete('companies', origin);
  const tx = db.transaction(['items', 'media'], 'readwrite');
  await deleteByIndex(tx.objectStore('items'), 'by-origin', origin);
  await deleteByIndex(tx.objectStore('media'), 'by-origin', origin);
  await tx.done;
}

async function deleteByIndex(store: any, indexName: string, value: string): Promise<void> {
  const idx = store.index(indexName);
  let cursor = await idx.openCursor(IDBKeyRange.only(value));
  while (cursor) {
    await cursor.delete();
    cursor = await cursor.continue();
  }
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
  const db = await openAppDb();
  return (await db.getAll('items')) as StoredItem[];
}

export async function getItems(origin: string): Promise<StoredItem[]> {
  const db = await openAppDb();
  const all = (await db.getAll('items')) as StoredItem[];
  return all.filter((i) => i.origin === origin);
}

export async function getItem(origin: string, feedKey: string, itemId: string): Promise<StoredItem | undefined> {
  const db = await openAppDb();
  return (await db.get('items', itemKey(origin, feedKey, itemId))) as StoredItem | undefined;
}

export async function putItems(items: StoredItem[]): Promise<void> {
  const db = await openAppDb();
  const tx = db.transaction('items', 'readwrite');
  for (const item of items) await tx.store.put(item);
  await tx.done;
}

export async function deleteItems(ids: string[]): Promise<void> {
  const db = await openAppDb();
  const tx = db.transaction('items', 'readwrite');
  for (const id of ids) await tx.store.delete(id);
  await tx.done;
}

export async function markRead(origin: string, feedKey: string, itemId: string, read: boolean): Promise<void> {
  const db = await openAppDb();
  const id = itemKey(origin, feedKey, itemId);
  const item = (await db.get('items', id)) as StoredItem | undefined;
  if (item) {
    item.read = read;
    await db.put('items', item);
  }
}

// --- media cache (images/logo bytes keyed by URL) ---

export interface CachedMedia {
  url: string;
  origin: string;
  bytes: ArrayBuffer;
  mime: string;
  at: number;
}

export async function getMedia(url: string): Promise<CachedMedia | undefined> {
  const db = await openAppDb();
  return (await db.get('media', url)) as CachedMedia | undefined;
}

export async function putMedia(entry: CachedMedia): Promise<void> {
  const db = await openAppDb();
  await db.put('media', entry);
}

export async function deleteMediaFor(origin: string): Promise<void> {
  const db = await openAppDb();
  const tx = db.transaction('media', 'readwrite');
  await deleteByIndex(tx.store, 'by-origin', origin);
  await tx.done;
}

// --- prefs (per company, stored on the company record) ---

export function makeCompany(
  origin: string,
  joinUrl: string,
  pinnedRoot: RootDoc,
  targets: TargetsDoc,
  identity: { companyName?: string; logo?: string },
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
