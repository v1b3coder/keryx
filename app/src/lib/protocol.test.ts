/**
 * Protocol tests against the REAL demo artifacts in ../demo (signed by the
 * Go publisher tool): full TUF chain verification (OLPC canonical metadata
 * signatures, root chain, timestamp/snapshot/targets, per-channel delegated
 * role metadata, hash-pinned feed targets), JCS item verification (default +
 * editor mode), whole-document private-feed verification, authorization,
 * pattern matching, payload parsing, suspension and rollback detection.
 *
 * The fetch stub serves exact file bytes (not re-serialized JSON) because
 * metadata and feed targets are hash-pinned — byte fidelity matters.
 */

import { readFileSync, readdirSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, it, expect } from 'vitest';
import canonicalize from 'canonicalize';
import { sign, hashes } from '@noble/ed25519';
import { sha512 } from '@noble/hashes/sha2.js';
import {
  loadAndVerifyMetadata,
  loadChannelRole,
  extractAuthorization,
  channelFromRole as chainFromRole,
  ChainBreakError,
  ProtocolError,
  fetchRootDoc,
  verifyRootTransition,
  verifyKeyid,
  versionedRootUrl,
  targetFileUrl,
  verifyTargetBytes,
  type RootDoc,
  type TargetsDoc,
} from './tuf';
import {
  verifyItemSignatures,
  verifyChannelCrossCheck,
  isWithdrawn,
  canonicalItemBytes,
  signedContentKey,
  type FeedItem,
} from './item';
import { verifyPrivateFeedDocument, matchesPattern, PRIVATE_FEED_MAX_BYTES } from './private';
import { parseJoinUrl, rootAnchorUrl, joinUrlFromDeepLink } from './payload';
import { patternMatches, pathPatternMatches } from './pattern';
import { olpcCanonical } from './olpc';
import { textToBase64url, bytesToBase64url, bytesToHex, hexToBytes } from './bytes';
import type { StoredItem } from './store';

hashes.sha512 = sha512;

const demoDir = join(dirname(fileURLToPath(import.meta.url)), '..', '..', '..', 'demo');
const origin = 'http://10.110.147.178:8000';

function fileFor(url: string): string {
  return join(demoDir, url.replace(/^http:\/\/10\.110\.147\.178:8000\//, ''));
}

function readBytes(url: string): Uint8Array {
  return new Uint8Array(readFileSync(fileFor(url)));
}

const fetchLike = async (url: string) => {
  const bytes = readBytes(url);
  return new Response(bytes as unknown as BodyInit, { status: 200, headers: { 'content-type': 'application/json' } });
};

function loadJson<T = unknown>(path: string): T {
  return JSON.parse(readFileSync(join(demoDir, path), 'utf8')) as T;
}

/** The single generated capability token (demo regenerates it on every build). */
function privateFeedRelPath(): string {
  const token = readdirSync(join(demoDir, 'channels', 'tracking'))[0];
  return `channels/tracking/${token}/feed.json`;
}

function seedOf(name: string): Uint8Array {
  const f = loadJson<{ seed_hex: string }>(`keys/${name}.json`);
  return new Uint8Array(Buffer.from(f.seed_hex, 'hex'));
}

/** JCS canonical bytes of an object with a `_sig.signatures` field removed — the publisher's rule. */
function signJcs(obj: Record<string, unknown>, seed: Uint8Array): string {
  const clone = JSON.parse(JSON.stringify(obj)) as Record<string, any>;
  delete clone._sig?.signatures;
  const canonical = canonicalize(clone)!;
  const sig = sign(new TextEncoder().encode(canonical), seed);
  return bytesToBase64url(sig);
}

describe('join payload (spec/core.md §3)', () => {
  const joinUrl = readFileSync(join(demoDir, 'join.txt'), 'utf8').trim();

  it('parses the demo join URL: origin, channels, private feed, v=1', () => {
    const parsed = parseJoinUrl(joinUrl);
    expect(parsed.origin).toBe(origin);
    expect(parsed.payload.v).toBe(1);
    expect(parsed.payload.channels).toEqual(['security', 'news', 'insights']);
    expect(parsed.payload.privateFeeds).toHaveLength(1);
    expect(parsed.payload.privateFeeds[0]).toContain('/channels/tracking/');
    expect(parsed.payload.privateFeeds[0]).toMatch(/\/feed\.json$/);
  });

  it('derives the root anchor from the join origin — there is no metadata URL in the payload', () => {
    const parsed = parseJoinUrl(joinUrl);
    expect(rootAnchorUrl(parsed.origin)).toBe(`${origin}/.well-known/keryx/root.json`);
    expect(parsed.payload).not.toHaveProperty('root');
    expect(parsed.payload).not.toHaveProperty('feeds');
  });

  it('ignores unrecognized members within a known v (forward compatibility)', () => {
    const p = textToBase64url(JSON.stringify({ v: 1, channels: ['a'], something_new: { x: 1 } }));
    const parsed = parseJoinUrl(`${origin}/join?p=${p}`);
    expect(parsed.payload.channels).toEqual(['a']);
  });

  it('refuses to parse an unknown v (MUST NOT partial-parse)', () => {
    const p = textToBase64url(JSON.stringify({ v: 2, channels: ['a'] }));
    expect(() => parseJoinUrl(`${origin}/join?p=${p}`)).toThrow(/newer version/i);
  });

  it('rejects malformed input', () => {
    expect(() => parseJoinUrl('not a url')).toThrow();
    expect(() => parseJoinUrl(`${origin}/join?p=%%%`)).toThrow();
    expect(() => parseJoinUrl('ftp://x/join?p=eyJ2IjoxfQ')).toThrow();
    expect(() => parseJoinUrl(`${origin}/blog/hello.html`)).toThrow();
    expect(() => parseJoinUrl(`${origin}/join?p=`)).toThrow();
  });

  it('builds a join URL from the PWA deep link (out-of-spec extension)', () => {
    expect(joinUrlFromDeepLink('keryx-demo.github.io', null)).toBe('https://keryx-demo.github.io/join');
    expect(joinUrlFromDeepLink('https://keryx-demo.github.io/', 'eyJ2IjoxfQ')).toBe(
      'https://keryx-demo.github.io/join?p=eyJ2IjoxfQ',
    );
    expect(joinUrlFromDeepLink('http://10.110.147.178:8000', 'eyJ2IjoxfQ')).toBe(
      'http://10.110.147.178:8000/join?p=eyJ2IjoxfQ',
    );
    expect(joinUrlFromDeepLink('', null)).toBeNull();
  });

  it('normalizes a bare domain to https (entry field convenience)', () => {
    for (const input of ['company.example', 'company.example/join', 'company.example/join/']) {
      const parsed = parseJoinUrl(input);
      expect(parsed.origin).toBe('https://company.example');
      expect(parsed.payload).toEqual({ v: 1, channels: [], privateFeeds: [] });
    }
    // an explicit scheme is honored (the local-dev HTTP exception)
    expect(parseJoinUrl(`${origin}/join`).origin).toBe(origin);
  });

  it('accepts a payload-less join (spec: public channels only)', () => {
    const expected = { v: 1, channels: [], privateFeeds: [] };
    for (const url of [`${origin}/join`, `${origin}/join/`, origin, `${origin}/`]) {
      const parsed = parseJoinUrl(url);
      expect(parsed.origin).toBe(origin);
      expect(parsed.payload).toEqual(expected);
    }
  });

  it('filters invalid channel names out of the payload (spec: [a-z0-9-_]+)', () => {
    const p = textToBase64url(JSON.stringify({ v: 1, channels: ['ok', 'bad name', 'UPPER'] }));
    const parsed = parseJoinUrl(`${origin}/join?p=${p}`);
    expect(parsed.payload.channels).toEqual(['ok']);
  });
});

describe('TUF chain against the real demo repo', () => {
  it('verifies the full metadata chain and finds the channels.* delegations', async () => {
    const meta = await loadAndVerifyMetadata(fetchLike, `${origin}/.well-known/keryx/root.json`, null, null);
    expect(meta.root.signed._type).toBe('root');
    expect(meta.base).toBe(`${origin}/keryx/`);
    expect(meta.rootRotated).toBe(false);
    const auth = extractAuthorization(meta.targets);
    expect([...auth.channels.keys()].sort()).toEqual(['insights', 'news', 'security']);
    // editor mode: security is 2-of-2
    expect(auth.editor.get('security')?.threshold).toBe(2);
    expect(auth.editor.get('security')?.keys).toHaveLength(2);
    expect(auth.editor.get('news')).toBeUndefined();
    // private pattern
    expect(auth.privatePatterns).toHaveLength(1);
    expect(auth.privatePatterns[0].channel).toBe('tracking');
  });

  it('verifies each channel role metadata and the pinned feed target', async () => {
    const meta = await loadAndVerifyMetadata(fetchLike, `${origin}/.well-known/keryx/root.json`, null, null);
    for (const channel of ['security', 'news', 'insights']) {
      const role = await loadChannelRole(fetchLike, meta, `channels.${channel}`, false);
      const path = `channels/${channel}/feed.json`;
      const info = role.signed.targets[path];
      expect(info).toBeDefined();
      const bytes = readBytes(`${origin}/keryx/${path}`);
      verifyTargetBytes(bytes, info, path); // length + sha256 must match
    }
  });

  it('rejects a tampered feed (bytes swapped after signing)', async () => {
    const meta = await loadAndVerifyMetadata(fetchLike, `${origin}/.well-known/keryx/root.json`, null, null);
    const role = await loadChannelRole(fetchLike, meta, 'channels.news', false);
    const path = 'channels/news/feed.json';
    const info = role.signed.targets[path];
    const bytes = readBytes(`${origin}/keryx/${path}`);
    const tampered = new Uint8Array(bytes);
    tampered[100] ^= 0xff;
    expect(() => verifyTargetBytes(tampered, info, path)).toThrow(ProtocolError);
  });

  it('rejects a root rotation not signed by the previous root keys (chain break)', async () => {
    const v1 = loadJson<RootDoc>('.well-known/keryx/root.json');
    const v2 = JSON.parse(JSON.stringify(v1)) as RootDoc;
    v2.signed.version = 2;
    // signed ONLY by the old master key — step 4(1) passes, but the new
    // root role keys must self-sign too (step 4(2)): hand root to ops key
    v2.signed.roles.root.keyids = [...v1.signed.roles.snapshot.keyids];
    const canonical = olpcCanonical(v2.signed);
    const sig = sign(new TextEncoder().encode(canonical), seedOf('master'));
    v2.signatures = [{ keyid: v1.signed.roles.root.keyids[0], sig: bytesToHex(sig) }];
    expect(() => verifyRootTransition(v1, v2)).toThrow(ChainBreakError);
  });

  it('rejects a root rotation not signed by the previous keys at all (chain break)', async () => {
    const v1 = loadJson<RootDoc>('.well-known/keryx/root.json');
    const v2 = JSON.parse(JSON.stringify(v1)) as RootDoc;
    v2.signed.version = 2;
    // signed by the OPS key only — no master signature at all
    const canonical = olpcCanonical(v2.signed);
    const sig = sign(new TextEncoder().encode(canonical), seedOf('ops'));
    v2.signatures = [{ keyid: v1.signed.roles.snapshot.keyids[0], sig: bytesToHex(sig) }];
    expect(() => verifyRootTransition(v1, v2)).toThrow(ChainBreakError);
  });

  it('rejects timestamp rollback via version memory', async () => {
    const meta = await loadAndVerifyMetadata(fetchLike, `${origin}/.well-known/keryx/root.json`, null, null);
    const seen = {
      timestamp: meta.timestamp.signed.version + 5,
      snapshot: meta.snapshot.signed.version,
      targets: meta.targets.signed.version,
    };
    await expect(
      loadAndVerifyMetadata(fetchLike, `${origin}/.well-known/keryx/root.json`, null, seen),
    ).rejects.toThrow(/rollback/);
  });

  it('never accepts an older root than the pinned one (anti-rollback)', async () => {
    const v1 = loadJson<RootDoc>('.well-known/keryx/root.json');
    const meta = await loadAndVerifyMetadata(fetchLike, `${origin}/.well-known/keryx/root.json`, v1, null);
    expect(meta.root.signed.version).toBe(v1.signed.version);
  });

  it('keyids are the TUF-standard hash of the key object (spec/core.md §1)', () => {
    const targets = loadJson<TargetsDoc>('keryx/targets.json');
    for (const [keyid, key] of Object.entries(targets.signed.delegations!.keys)) {
      expect(() => verifyKeyid(keyid, key, 'test')).not.toThrow();
    }
    // a keyid computed WITH the extra `name` field must be rejected
    const key = targets.signed.delegations!.keys[Object.keys(targets.signed.delegations!.keys)[0]];
    expect(() => verifyKeyid('0000000000000000000000000000000000000000000000000000000000000000', key, 'test')).toThrow(
      ProtocolError,
    );
  });

  it('ignores delegated roles that are not channels.<name> (spec/repository.md §2)', () => {
    const targets = loadJson<TargetsDoc>('keryx/targets.json');
    expect(chainFromRole({ name: 'security', keyids: ['x'], threshold: 1, paths: ['channels/security/*'] })).toBeNull();
    expect(chainFromRole({ name: 'channels.security', keyids: ['x'], threshold: 1, paths: ['channels/security/*'] })).toBe(
      'security',
    );
    expect(
      chainFromRole({ name: 'channels.security', keyids: ['x'], threshold: 1, paths: ['channels/other/*'] }),
    ).toBeNull();
    // roles with paths outside the namespace are not channels
    void targets;
  });
});

describe('item verification (spec/feeds.md §1.2)', () => {
  const securityFeed = loadJson<{ items: FeedItem[] }>('channels/security/feed.json').items;
  const newsFeed = loadJson<{ items: FeedItem[] }>('channels/news/feed.json').items;

  it('editor mode: 2-of-2 security items verify (channel-key extra is not load-bearing)', async () => {
    const meta = await loadAndVerifyMetadata(fetchLike, `${origin}/.well-known/keryx/root.json`, null, null);
    const auth = extractAuthorization(meta.targets);
    const editor = auth.editor.get('security')!;
    const channelKeys = auth.channels.get('security')!.keys;
    for (const item of securityFeed) {
      verifyChannelCrossCheck(item, 'security');
      expect(() => verifyItemSignatures(item, editor, channelKeys)).not.toThrow();
    }
  });

  it('default mode: news items verify via attribution signatures', async () => {
    const meta = await loadAndVerifyMetadata(fetchLike, `${origin}/.well-known/keryx/root.json`, null, null);
    const auth = extractAuthorization(meta.targets);
    const channelKeys = auth.channels.get('news')!.keys;
    for (const item of newsFeed) {
      verifyChannelCrossCheck(item, 'news');
      expect(() => verifyItemSignatures(item, undefined, channelKeys)).not.toThrow();
    }
  });

  it('rejects a modified item (signatures no longer verify)', async () => {
    const meta = await loadAndVerifyMetadata(fetchLike, `${origin}/.well-known/keryx/root.json`, null, null);
    const auth = extractAuthorization(meta.targets);
    const editor = auth.editor.get('security')!;
    const item = JSON.parse(JSON.stringify(securityFeed[0])) as FeedItem;
    item.title = 'Tampered title';
    expect(() => verifyItemSignatures(item, editor, auth.channels.get('security')!.keys)).toThrow(ProtocolError);
  });

  it('rejects an item with missing editor signatures', async () => {
    const meta = await loadAndVerifyMetadata(fetchLike, `${origin}/.well-known/keryx/root.json`, null, null);
    const auth = extractAuthorization(meta.targets);
    const item = JSON.parse(JSON.stringify(securityFeed[0])) as FeedItem;
    item._sig!.signatures = [];
    expect(() => verifyItemSignatures(item, auth.editor.get('security')!, auth.channels.get('security')!.keys)).toThrow(
      ProtocolError,
    );
  });

  it('editor mode: an unknown keyid signature alone does not satisfy the threshold', async () => {
    const meta = await loadAndVerifyMetadata(fetchLike, `${origin}/.well-known/keryx/root.json`, null, null);
    const auth = extractAuthorization(meta.targets);
    const item = JSON.parse(JSON.stringify(securityFeed[0])) as FeedItem;
    // re-sign with the WRONG key (news channel key), keyid unknown to editor mode
    const sig = signJcs(item as unknown as Record<string, unknown>, seedOf('news'));
    item._sig!.signatures = [{ keyid: auth.channels.get('news')!.keys[0].keyid, sig }];
    expect(() => verifyItemSignatures(item, auth.editor.get('security')!, auth.channels.get('security')!.keys)).toThrow(
      ProtocolError,
    );
  });

  it('default mode: a known keyid with a failing signature rejects the item (no third state)', async () => {
    const meta = await loadAndVerifyMetadata(fetchLike, `${origin}/.well-known/keryx/root.json`, null, null);
    const auth = extractAuthorization(meta.targets);
    const item = JSON.parse(JSON.stringify(newsFeed[0])) as FeedItem;
    const channelKey = auth.channels.get('news')!.keys[0];
    item._sig!.signatures = [{ keyid: channelKey.keyid, sig: 'AAAA' }];
    expect(() => verifyItemSignatures(item, undefined, auth.channels.get('news')!.keys)).toThrow(ProtocolError);
  });

  it('default mode: signatures by unknown keys are ignored (attribution only)', async () => {
    const meta = await loadAndVerifyMetadata(fetchLike, `${origin}/.well-known/keryx/root.json`, null, null);
    const auth = extractAuthorization(meta.targets);
    const item = JSON.parse(JSON.stringify(newsFeed[0])) as FeedItem;
    const sig = signJcs(item as unknown as Record<string, unknown>, seedOf('insights'));
    item._sig!.signatures = [{ keyid: 'ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff', sig }];
    expect(() => verifyItemSignatures(item, undefined, auth.channels.get('news')!.keys)).not.toThrow();
  });

  it('rejects a channel cross-check mismatch', () => {
    const item = JSON.parse(JSON.stringify(securityFeed[0])) as FeedItem;
    expect(() => verifyChannelCrossCheck(item, 'news')).toThrow(ProtocolError);
    expect(() => verifyChannelCrossCheck(item, 'security')).not.toThrow();
  });

  it('withdrawn items are hidden; signature-only re-signing is not an update', () => {
    const item = JSON.parse(JSON.stringify(securityFeed[0])) as FeedItem;
    expect(isWithdrawn(item)).toBe(false);
    const copy = JSON.parse(JSON.stringify(item)) as FeedItem;
    expect(signedContentKey(item)).toBe(signedContentKey(copy));
    // content change → different key
    copy.title = 'Changed';
    expect(signedContentKey(item)).not.toBe(signedContentKey(copy));
  });
});

describe('private capability feed (spec/feeds.md §3)', () => {
  const meta = loadJson<{ _sig: { channel: string; url: string; version: number } }>(
    privateFeedRelPath(),
  );
  const privateUrl = meta._sig.url;
  const targets = loadJson<TargetsDoc>('keryx/targets.json');
  const entry = targets.signed.custom!.private_feed_patterns![0];

  it('verifies the whole document (signature + channel + url + version)', () => {
    const doc = loadJson<Record<string, any>>(privateFeedRelPath());
    const result = verifyPrivateFeedDocument(doc, entry, privateUrl, undefined);
    expect(result.closed).toBe(false);
    expect(result.version).toBe(doc._sig.version);
  });

  it('rejects a tampered document (whole-doc signature breaks)', () => {
    const doc = loadJson<Record<string, any>>(privateFeedRelPath());
    const tampered = JSON.parse(JSON.stringify(doc)) as Record<string, any>;
    tampered.items[0].title = 'Tampered';
    expect(() => verifyPrivateFeedDocument(tampered, entry, privateUrl, undefined)).toThrow(ProtocolError);
  });

  it('rejects a version rollback (anti-rollback via version memory)', () => {
    const doc = loadJson<Record<string, any>>(privateFeedRelPath());
    expect(() => verifyPrivateFeedDocument(doc, entry, privateUrl, 99)).toThrow(/rollback/);
  });

  it('rejects a document served for the wrong URL (cross-order mix-up)', () => {
    const doc = loadJson<Record<string, any>>(privateFeedRelPath());
    expect(() => verifyPrivateFeedDocument(doc, entry, privateUrl + 'x', undefined)).toThrow(/url/);
  });

  it('rejects a document with the wrong channel label', () => {
    const doc = loadJson<Record<string, any>>(privateFeedRelPath());
    const clone = JSON.parse(JSON.stringify(doc)) as Record<string, any>;
    clone._sig.channel = 'marketing';
    expect(() => verifyPrivateFeedDocument(clone, entry, privateUrl, undefined)).toThrow(/channel/);
  });

  it('marks the feed closed on expired: true but keeps it verified', () => {
    const doc = loadJson<Record<string, any>>(privateFeedRelPath());
    const clone = JSON.parse(JSON.stringify(doc)) as Record<string, any>;
    clone.expired = true;
    clone._sig.signatures = [{ keyid: entry.keyids[0], sig: signJcs(clone, seedOf('tracking')) }];
    const result = verifyPrivateFeedDocument(clone, entry, privateUrl, undefined);
    expect(result.closed).toBe(true);
  });

  it('enforces the 1 MB document size limit', () => {
    expect(PRIVATE_FEED_MAX_BYTES).toBe(1024 * 1024);
  });
});

describe('pattern matching', () => {
  it('URL patterns: origin-exact, segment-boundary wildcards', () => {
    const p = 'https://eshop.example.com/channels/tracking/*/feed.json';
    expect(patternMatches(p, 'https://eshop.example.com/channels/tracking/abc123/feed.json')).toBe(true);
    expect(patternMatches(p, 'https://eshop.example.com/channels/tracking/a/b/feed.json')).toBe(false);
    expect(patternMatches(p, 'https://evil.example.com/channels/tracking/abc/feed.json')).toBe(false);
    expect(patternMatches(p, 'https://eshop.example.com/channels/tracking/abc/feed.json?x=1')).toBe(false);
    expect(patternMatches(p, 'http://eshop.example.com/channels/tracking/abc/feed.json')).toBe(false);
  });

  it('TUF path globs: * matches exactly one segment, never crosses /', () => {
    expect(pathPatternMatches('channels/marketing/*', 'channels/marketing/feed.json')).toBe(true);
    expect(pathPatternMatches('channels/marketing/*', 'channels/marketing/x/feed.json')).toBe(false);
    expect(pathPatternMatches('channels/marketing/*', 'channels/security/feed.json')).toBe(false);
  });
});

describe('feed processing semantics (spec/feeds.md §1.2)', () => {
  it('withdrawn items are hidden and dropped from the cache (binary rule)', async () => {
    const { processFeedItems } = await import('./sync');
    const company = {
      origin: 'http://10.110.147.178:8000',
      channels: [],
      privateFeeds: [],
      prefs: { languages: [], tags: [], loadRemoteMedia: true },
    } as never;
    const existing = new Map<string, StoredItem>();
    const feed: FeedItem[] = [
      { id: 'a', title: 'A', _sig: { channel: 'news', withdrawn: false } },
      { id: 'b', title: 'B', _sig: { channel: 'news', withdrawn: true } },
    ];
    // seed a cached copy of b (previously displayed)
    existing.set('http://10.110.147.178:8000\u0000public:news\u0000b', {
      id: 'http://10.110.147.178:8000\u0000public:news\u0000b',
      origin: 'http://10.110.147.178:8000',
      channel: 'news',
      feedUrl: '',
      isPrivate: false,
      item: { id: 'b', title: 'B old' },
      published: '',
      receivedAt: 1,
      read: false,
    });
    const { outcome, toPut, toDelete } = processFeedItems(
      company,
      'news',
      '',
      false,
      feed,
      [],
      undefined,
      existing,
    );
    expect(outcome.rejected).toBe(0);
    expect(toPut).toHaveLength(1);
    expect(toDelete).toContain('http://10.110.147.178:8000\u0000public:news\u0000b');
  });

  it('a previously displayed item that no longer verifies is dropped', async () => {
    const { processFeedItems } = await import('./sync');
    const company = {
      origin: 'http://10.110.147.178:8000',
      channels: [],
      privateFeeds: [],
      prefs: { languages: [], tags: [], loadRemoteMedia: true },
    } as never;
    const existing = new Map<string, StoredItem>();
    const key = 'http://10.110.147.178:8000\u0000public:news\u0000c';
    existing.set(key, {
      id: key,
      origin: 'http://10.110.147.178:8000',
      channel: 'news',
      feedUrl: '',
      isPrivate: false,
      item: { id: 'c', title: 'C' },
      published: '',
      receivedAt: 1,
      read: false,
    });
    // a KNOWN channel key whose signature fails → item rejected (no third state)
    const targets = loadJson<TargetsDoc>('keryx/targets.json');
    const newsEntry = Object.entries(targets.signed.delegations!.keys)[0];
    const known = { keyid: newsEntry[0], pub: hexToBytes(newsEntry[1].keyval.public) };
    const { outcome, toDelete } = processFeedItems(
      company,
      'news',
      '',
      false,
      [
        {
          id: 'c',
          title: 'C tampered',
          _sig: { channel: 'news', signatures: [{ keyid: known.keyid, sig: 'AAAA' }] },
        },
      ],
      [known],
      undefined,
      existing,
    );
    expect(outcome.rejected).toBe(1);
    expect(toDelete).toContain(key);
  });
});
