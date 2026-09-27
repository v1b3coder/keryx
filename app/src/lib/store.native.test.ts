import { describe, it, expect, vi, beforeEach } from 'vitest';
import type { StoredItem } from './store.native';

/**
 * `store.native.ts` runs only on iOS/Android (Metro resolves the platform
 * extension), so the suite never touches it. These tests drive it against an
 * in-memory stand-in for expo-sqlite's kv-store and cover the NUL-key escaping
 * and the corrupt-record tolerance the device build depends on.
 */
const mem = new Map<string, string>();

vi.mock('expo-sqlite/kv-store', () => ({
  default: {
    getItemAsync: vi.fn(async (key: string) => mem.get(key) ?? null),
    setItemAsync: vi.fn(async (key: string, value: string) => {
      mem.set(key, value);
    }),
    removeItemAsync: vi.fn(async (key: string) => mem.delete(key)),
    getAllKeysAsync: vi.fn(async () => [...mem.keys()]),
  },
}));

import {
  deleteCompany,
  getAllItems,
  getItem,
  getMedia,
  itemKey,
  pendingTest,
  putCompany,
  putItems,
  putMedia,
  putPendingTest,
} from './store.native';
import { bytesToBase64url } from './bytes';
import type { CompanyRecord } from './store.native';

function company(origin: string): CompanyRecord {
  return {
    origin,
    joinUrl: '',
    identity: {},
    pinnedRoot: { signed: { version: 1, expires: '2099-01-01T00:00:00Z' }, signatures: [] } as never,
    pinnedRootVersion: 1,
    targets: {
      signed: {
        _type: 'targets',
        version: 1,
        expires: '2099-01-01T00:00:00Z',
        targets: {},
        delegations: { keys: {}, roles: [] },
      },
      signatures: [],
    } as never,
    targetsVersion: 1,
    seen: { targets: 1, roles: {} },
    channels: [],
    privateFeeds: [],
    status: 'active',
    joinedAt: 0,
    lastSyncAt: null,
    prefs: { languages: [], tags: [], loadRemoteMedia: true },
  };
}

function item(origin: string, channel: string, id: string): StoredItem {
  return {
    id: itemKey(origin, `public:${channel}`, id),
    origin,
    channel,
    feedUrl: '',
    isPrivate: false,
    item: { id, title: id } as never,
    published: '2026-01-01T00:00:00Z',
    receivedAt: 0,
    read: false,
  };
}

beforeEach(() => {
  mem.clear();
});

describe('native store keys', () => {
  it('keeps items with a NUL separator distinct', async () => {
    // expo-sqlite stores TEXT as a C string: without the key escaping both
    // items would collapse onto the same row and only the last would survive
    const a = item('http://a.example/x', 'news', 'one');
    const b = item('http://a.example/x', 'news', 'two');
    await putItems([a, b]);

    const all = await getAllItems();
    expect(all.map((i) => i.id).sort()).toEqual([a.id, b.id].sort());
    expect((await getItem('http://a.example/x', 'public:news', 'one'))?.item.id).toBe('one');
    expect((await getItem('http://a.example/x', 'public:news', 'two'))?.item.id).toBe('two');
  });

  it('does not surface a legacy key truncated at a NUL byte', async () => {
    const good = item('http://a.example/x', 'news', 'one');
    await putItems([good]);
    // the pre-escaping bug wrote `item:<origin>` with the rest cut off
    mem.set(`item:http://a.example/x`, JSON.stringify({ ...good, id: 'legacy' }));

    const all = await getAllItems();
    expect(all.map((i) => i.id)).toEqual([good.id]);
  });

  it('deletes a company with its items', async () => {
    await putCompany(company('http://a.example/x'));
    await putItems([item('http://a.example/x', 'news', 'one')]);
    await deleteCompany('http://a.example/x');

    expect(await getAllItems()).toEqual([]);
  });

  it('escapes the NUL in the relay replay/pending keys', async () => {
    await putPendingTest({ baseUrl: 'https://relay.example', nonce: 'n', expiresAt: 1 });
    const pending = await pendingTest('https://relay.example');
    expect(pending?.nonce).toBe('n');
    // the stored record's relay-state key is an internal detail, never the API value
    expect(pending).not.toHaveProperty('key');
  });
});

describe('native media cache', () => {
  it('round-trips bytes and treats a corrupt entry as a miss', async () => {
    const bytes = new Uint8Array([1, 2, 3, 250]).buffer;
    await putMedia({ url: 'https://a.example/logo.png', origin: 'http://a.example/x', bytes, mime: 'image/png', at: 1 });
    const cached = await getMedia('https://a.example/logo.png');
    expect(new Uint8Array(cached!.bytes)).toEqual(new Uint8Array([1, 2, 3, 250]));

    // a corrupt entry must be a cache miss, never a thrown error
    mem.set('media:https://a.example/logo.png', JSON.stringify({ url: 'https://a.example/logo.png', origin: 'x', bytes: 'not-base64url!!', mime: 'image/png', at: 1 }));
    expect(await getMedia('https://a.example/logo.png')).toBeUndefined();
  });

  it('writes a valid base64url payload', async () => {
    const bytes = new Uint8Array([0xde, 0xad, 0xbe, 0xef]).buffer;
    await putMedia({ url: 'https://a.example/i.png', origin: 'x', bytes, mime: 'image/png', at: 1 });
    const raw = JSON.parse(mem.get('media:https://a.example/i.png')!);
    expect(raw.bytes).toBe(bytesToBase64url(new Uint8Array([0xde, 0xad, 0xbe, 0xef])));
  });
});
