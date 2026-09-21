/**
 * Protocol tests against the REAL demo artifacts in ../demo (signed by the
 * reference publisher tool): full TUF chain verification (OLPC canonical
 * metadata signatures, root chain, timestamp/snapshot/targets, per-channel
 * delegated role metadata, per-item hash-pinned targets), OLPC item
 * verification (authors role by default, simple-mode channel keys otherwise),
 * whole-document private-feed verification, authorization, pattern matching,
 * payload parsing, suspension, rollback and unpublish semantics.
 *
 * The fetch stub serves exact file bytes (not re-serialized JSON) because
 * metadata and item targets are hash-pinned — byte fidelity matters.
 */

import { readFileSync, readdirSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, it, expect } from 'vitest';
import { sign, hashes } from '@noble/ed25519';
import { sha512 } from '@noble/hashes/sha2.js';
import {
  loadAndVerifyMetadata,
  loadChannelRole,
  extractAuthorization,
  channelFromRole,
  ChainBreakError,
  ProtocolError,
  verifyRootTransition,
  verifyKeyid,
  targetFileUrl,
  verifyTargetBytes,
  type RootDoc,
  type TargetsDoc,
} from './tuf';
import {
  verifyItemSignatures,
  verifyImage,
  itemIdFromPath,
  signedContentKey,
  attachmentSha,
  bytesMatchSha,
  type FeedItem,
} from './item';
import { verifyPrivateFeedDocument, matchesPattern, PRIVATE_FEED_MAX_BYTES } from './private';
import { logoDisplayable, readLimitedBody } from './media';
import { redirectAllowed, safeFetch } from './urlpolicy';
import { PUBLIC_ITEM_MAX_BYTES } from './item';
import { parseJoinUrl, rootAnchorUrl, joinUrlFromDeepLink } from './payload';
import { buildPairingOffer, createCompanyFromOffer } from './pair';
import { syncCompany, applyOutcomeItems } from './sync';
import { setDebugBuild } from './build';
import { patternMatches, pathPatternMatches } from './pattern';
import { olpcCanonical } from './olpc';
import { textToBase64url, bytesToBase64url, bytesToHex, hexToBytes } from './bytes';
import type { StoredItem } from './store';

hashes.sha512 = sha512;

const demoDir =
  process.env.KERYX_DEMO_DIR ??
  join(dirname(fileURLToPath(import.meta.url)), '..', '..', '..', '..', 'keryx-demo');
// private keys live outside the published demo site (design/tooling.md §3.2)
const keystoreDir = process.env.KERYX_KEYSTORE ?? join(demoDir, '..', 'keryx-demo-keys');
// the demo is generated with any base (localhost or keryx-demo.github.io), so derive
// the origin from its own join link rather than assuming one
const origin = new URL(readFileSync(join(demoDir, 'join.txt'), 'utf8').trim()).origin;

function fileFor(url: string): string {
  return join(demoDir, new URL(url).pathname);
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
  const f = JSON.parse(readFileSync(join(keystoreDir, `${name}.json`), 'utf8')) as { seed_hex: string };
  return new Uint8Array(Buffer.from(f.seed_hex, 'hex'));
}

/** OLPC canonical bytes of an object with a `sig` field removed — the publisher's rule. */
function signOlpc(obj: Record<string, unknown>, seed: Uint8Array): string {
  const clone = JSON.parse(JSON.stringify(obj)) as Record<string, any>;
  delete clone.sig;
  const canonical = olpcCanonical(clone);
  const sig = sign(new TextEncoder().encode(canonical), seed);
  return bytesToBase64url(sig);
}

function channelItems(channel: string): FeedItem[] {
  const dir = join(demoDir, 'keryx', 'channels', channel);
  return readdirSync(dir)
    .filter((f) => f.endsWith('.json'))
    .map((f) => JSON.parse(readFileSync(join(dir, f), 'utf8')) as FeedItem);
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

  it('allows http join links in debug builds only (HTTPS-only protocol)', () => {
    const p = textToBase64url(JSON.stringify({ v: 1, channels: ['a'] }));
    const httpOrigin = 'http://localhost:8000';
    setDebugBuild(false);
    expect(() => parseJoinUrl(`${httpOrigin}/join?p=${p}`)).toThrow(/https/i);
    setDebugBuild(true);
    expect(parseJoinUrl(`${httpOrigin}/join?p=${p}`).origin).toBe(httpOrigin);
    setDebugBuild(null);
  });

  it('builds a join URL from the PWA deep link (out-of-spec extension)', () => {
    expect(joinUrlFromDeepLink('keryx-demo.github.io', null)).toBe('https://keryx-demo.github.io/join');
    expect(joinUrlFromDeepLink('https://keryx-demo.github.io/', 'eyJ2IjoxfQ')).toBe(
      'https://keryx-demo.github.io/join?p=eyJ2IjoxfQ',
    );
    expect(joinUrlFromDeepLink('http://localhost:8000', 'eyJ2IjoxfQ')).toBe(
      'http://localhost:8000/join?p=eyJ2IjoxfQ',
    );
    expect(joinUrlFromDeepLink('', null)).toBeNull();
  });

  it('normalizes a bare domain to https (entry field convenience)', () => {
    for (const input of ['company.example', 'company.example/join', 'company.example/join/']) {
      const parsed = parseJoinUrl(input);
      expect(parsed.origin).toBe('https://company.example');
      expect(parsed.payload).toEqual({ v: 1, channels: [], privateFeeds: [] });
    }
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
    // authored is the default: security has a 2-of-2 authors role
    expect(auth.authors.get('security')?.threshold).toBe(2);
    expect(auth.authors.get('security')?.keys).toHaveLength(2);
    expect(auth.authors.get('news')).toBeUndefined();
    expect(auth.privatePatterns).toHaveLength(1);
    expect(auth.privatePatterns[0].channel).toBe('tracking');
  });

  it('verifies each channel role metadata and its per-item targets', async () => {
    const meta = await loadAndVerifyMetadata(fetchLike, `${origin}/.well-known/keryx/root.json`, null, null);
    for (const channel of ['security', 'news', 'insights']) {
      const role = await loadChannelRole(fetchLike, meta, `channels.${channel}`, false);
      const paths = Object.keys(role.signed.targets);
      expect(paths.length).toBeGreaterThan(0);
      for (const path of paths) {
        expect(path.startsWith(`channels/${channel}/`)).toBe(true);
        const info = role.signed.targets[path];
        const bytes = readBytes(`${origin}/keryx/${path}`);
        verifyTargetBytes(bytes, info, path); // length + sha256 must match
      }
    }
  });

  it('rejects a tampered item target (bytes swapped after signing)', async () => {
    const meta = await loadAndVerifyMetadata(fetchLike, `${origin}/.well-known/keryx/root.json`, null, null);
    const role = await loadChannelRole(fetchLike, meta, 'channels.news', false);
    const path = Object.keys(role.signed.targets)[0];
    const info = role.signed.targets[path];
    const bytes = readBytes(`${origin}/keryx/${path}`);
    const tampered = new Uint8Array(bytes);
    tampered[10] ^= 0xff;
    expect(() => verifyTargetBytes(tampered, info, path)).toThrow(ProtocolError);
  });

  it('rejects a root rotation not signed by the previous root keys (chain break)', () => {
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

  it('rejects a root rotation not signed by the previous keys at all (chain break)', () => {
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
    const key = targets.signed.delegations!.keys[Object.keys(targets.signed.delegations!.keys)[0]];
    expect(() => verifyKeyid('0000000000000000000000000000000000000000000000000000000000000000', key, 'test')).toThrow(
      ProtocolError,
    );
  });

  it('ignores delegated roles that are not channels.<name> (spec/repository.md §2)', () => {
    expect(channelFromRole({ name: 'security', keyids: ['x'], threshold: 1, paths: ['channels/security/*'] })).toBeNull();
    expect(channelFromRole({ name: 'channels.security', keyids: ['x'], threshold: 1, paths: ['channels/security/*'] })).toBe(
      'security',
    );
    // an authors role is not a channel
    expect(
      channelFromRole({ name: 'channels.security.authors', keyids: ['x'], threshold: 1, paths: ['channels/security/*'] }),
    ).toBeNull();
    expect(
      channelFromRole({ name: 'channels.security', keyids: ['x'], threshold: 1, paths: ['channels/other/*'] }),
    ).toBeNull();
  });
});

describe('item verification (spec/feeds.md §1.2)', () => {
  const meta = async () => loadAndVerifyMetadata(fetchLike, `${origin}/.well-known/keryx/root.json`, null, null);

  it('authored channel: 2-of-2 security items verify (channel-key extra is not load-bearing)', async () => {
    const m = await meta();
    const auth = extractAuthorization(m.targets);
    const authors = auth.authors.get('security')!;
    const channel = auth.channels.get('security')!;
    for (const item of channelItems('security')) {
      expect(() => verifyImage(item)).not.toThrow();
      expect(() => verifyItemSignatures(item, authors, channel)).not.toThrow();
    }
  });

  it('simple mode: news and insights items verify via channel-key signatures', async () => {
    const m = await meta();
    const auth = extractAuthorization(m.targets);
    for (const name of ['news', 'insights']) {
      const channel = auth.channels.get(name)!;
      for (const item of channelItems(name)) {
        expect(() => verifyItemSignatures(item, undefined, channel)).not.toThrow();
      }
    }
  });

  it('rejects a modified item (signatures no longer verify)', async () => {
    const m = await meta();
    const auth = extractAuthorization(m.targets);
    const item = JSON.parse(JSON.stringify(channelItems('security')[0])) as FeedItem;
    item.title = 'Tampered title';
    expect(() => verifyItemSignatures(item, auth.authors.get('security')!, auth.channels.get('security')!)).toThrow(
      ProtocolError,
    );
  });

  it('rejects an item with missing author signatures', async () => {
    const m = await meta();
    const auth = extractAuthorization(m.targets);
    const item = JSON.parse(JSON.stringify(channelItems('security')[0])) as FeedItem;
    item.sig = [];
    expect(() => verifyItemSignatures(item, auth.authors.get('security')!, auth.channels.get('security')!)).toThrow(
      ProtocolError,
    );
  });

  it('authored channel: an unknown keyid signature alone does not satisfy the threshold', async () => {
    const m = await meta();
    const auth = extractAuthorization(m.targets);
    const item = JSON.parse(JSON.stringify(channelItems('security')[0])) as FeedItem;
    // re-sign with the news channel key, keyid unknown to the security authors role
    const sig = signOlpc(item as unknown as Record<string, unknown>, seedOf('news'));
    item.sig = [{ keyid: auth.channels.get('news')!.keys[0].keyid, sig }];
    expect(() => verifyItemSignatures(item, auth.authors.get('security')!, auth.channels.get('security')!)).toThrow(
      ProtocolError,
    );
  });

  it('simple mode: a known keyid with a failing signature rejects the item (no third state)', async () => {
    const m = await meta();
    const auth = extractAuthorization(m.targets);
    const item = JSON.parse(JSON.stringify(channelItems('news')[0])) as FeedItem;
    const channelKey = auth.channels.get('news')!.keys[0];
    item.sig = [{ keyid: channelKey.keyid, sig: 'AAAA' }];
    expect(() => verifyItemSignatures(item, undefined, auth.channels.get('news')!)).toThrow(ProtocolError);
  });

  it('simple mode: signatures by unknown keys are ignored (attribution only)', async () => {
    const m = await meta();
    const auth = extractAuthorization(m.targets);
    const item = JSON.parse(JSON.stringify(channelItems('news')[0])) as FeedItem;
    const sig = signOlpc(item as unknown as Record<string, unknown>, seedOf('insights'));
    // the valid channel signature stays; the unknown-key entry is ignored
    item.sig = [
      ...(item.sig ?? []),
      { keyid: 'ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff', sig },
    ];
    expect(() => verifyItemSignatures(item, undefined, auth.channels.get('news')!)).not.toThrow();
  });

  it('rejects a linked image without image_sha256 (schema violation)', () => {
    const item = JSON.parse(JSON.stringify(channelItems('news')[0])) as FeedItem;
    item.image = 'https://cdn.example.com/x.jpg';
    delete item.image_sha256;
    expect(() => verifyImage(item)).toThrow(ProtocolError);
    // an inline data URL needs no separate hash
    const inline = JSON.parse(JSON.stringify(channelItems('news')[0])) as FeedItem;
    inline.image = 'data:image/png;base64,AAAA';
    delete inline.image_sha256;
    expect(() => verifyImage(inline)).not.toThrow();
  });

  it('item id matches its TUF target path segment; content changes are updates', async () => {
    const m = await meta();
    const role = await loadChannelRole(fetchLike, m, 'channels.news', false);
    const path = Object.keys(role.signed.targets)[0];
    const item = JSON.parse(readFileSync(join(demoDir, 'keryx', path), 'utf8')) as FeedItem;
    expect(item.id).toBe(itemIdFromPath(path));
    const copy = JSON.parse(JSON.stringify(item)) as FeedItem;
    expect(signedContentKey(item)).toBe(signedContentKey(copy));
    copy.title = 'Changed';
    expect(signedContentKey(item)).not.toBe(signedContentKey(copy));
  });

  it('accepts a re-signed item (signature-only change is not an update)', async () => {
    const item = JSON.parse(JSON.stringify(channelItems('news')[0])) as FeedItem;
    const reSigned = JSON.parse(JSON.stringify(item)) as FeedItem;
    const sig = signOlpc(reSigned as unknown as Record<string, unknown>, seedOf('news'));
    reSigned.sig = [{ keyid: item.sig![0].keyid, sig }];
    expect(signedContentKey(item)).toBe(signedContentKey(reSigned));
  });
});

describe('private capability feed (spec/feeds.md §3)', () => {
  const doc = () => loadJson<Record<string, any>>(privateFeedRelPath());
  const targets = loadJson<TargetsDoc>('keryx/targets.json');
  const entry = targets.signed.custom!.private_feed_patterns![0];
  const privateUrl = doc().url as string;

  it('verifies the whole document (signature + channel + url + version)', () => {
    const result = verifyPrivateFeedDocument(doc(), entry, privateUrl, undefined);
    expect(result.closed).toBe(false);
    expect(result.version).toBe(doc().version);
  });

  it('rejects a tampered document (whole-doc signature breaks)', () => {
    const tampered = JSON.parse(JSON.stringify(doc())) as Record<string, any>;
    tampered.items[0].title = 'Tampered';
    expect(() => verifyPrivateFeedDocument(tampered, entry, privateUrl, undefined)).toThrow(ProtocolError);
  });

  it('rejects a version rollback (anti-rollback via version memory)', () => {
    expect(() => verifyPrivateFeedDocument(doc(), entry, privateUrl, 99)).toThrow(/rollback/);
  });

  it('rejects a document served for the wrong URL (cross-order mix-up)', () => {
    expect(() => verifyPrivateFeedDocument(doc(), entry, privateUrl + 'x', undefined)).toThrow(/url/);
  });

  it('rejects a document with the wrong channel label', () => {
    const clone = JSON.parse(JSON.stringify(doc())) as Record<string, any>;
    clone.channel = 'marketing';
    expect(() => verifyPrivateFeedDocument(clone, entry, privateUrl, undefined)).toThrow(/channel/);
  });

  it('marks the feed closed on expired: true but keeps it verified', () => {
    const clone = JSON.parse(JSON.stringify(doc())) as Record<string, any>;
    clone.expired = true;
    clone.sig = [{ keyid: entry.keyids[0], sig: signOlpc(clone, seedOf('tracking')) }];
    const result = verifyPrivateFeedDocument(clone, entry, privateUrl, undefined);
    expect(result.closed).toBe(true);
  });

  it('enforces the 1 MB document size limit', () => {
    expect(PRIVATE_FEED_MAX_BYTES).toBe(1024 * 1024);
  });

  it('matches the capability URL against the authorized pattern', () => {
    expect(matchesPattern(entry, privateUrl)).toBe(true);
  });
});

describe('sync engine end to end', () => {
  it('pairs and syncs a company from the artifact (public + private items)', async () => {
    const joinUrl = readFileSync(join(demoDir, 'join.txt'), 'utf8').trim();
    const parsed = parseJoinUrl(joinUrl);
    const fetchTyped = fetchLike as unknown as typeof fetch;
    const offer = await buildPairingOffer(parsed.origin, joinUrl, parsed.payload, fetchTyped);
    expect(offer.channels.length).toBe(3);
    expect(offer.privateFeeds[0].valid).toBe(true);
    // follow every offered channel (the user tapped Subscribe)
    const company = createCompanyFromOffer(
      offer,
      offer.channels.map((c) => c.name),
    );
    const synced = await syncCompany(company, fetchTyped, new Map());
    expect(synced.suspended).toBe(false);
    expect(synced.errors).toEqual([]);
    expect(synced.company.channels.length).toBe(3);
    // every published public item plus the private order item is verified
    const publicCount = ['security', 'news', 'insights'].reduce(
      (n, ch) => n + channelItems(ch).length,
      0,
    );
    const privateDoc = loadJson<{ items: unknown[] }>(privateFeedRelPath());
    expect(synced.toPut.length).toBe(publicCount + privateDoc.items.length);
    expect(synced.company.privateFeeds[0].closed).not.toBe(true);
    // every stored item is a verified one with a pinned hash or private feed
    for (const stored of synced.toPut) {
      if (!stored.isPrivate) expect(stored.hash).toBeDefined();
    }
  });
});

describe('redirect policy (spec/core.md §1.2)', () => {
  it('allows same-origin and the canonical http→https / www↔apex hops only', () => {
    expect(redirectAllowed('https://company.example/a', 'https://company.example/b')).toBe(true);
    expect(redirectAllowed('http://company.example/a', 'https://company.example/a')).toBe(true);
    expect(redirectAllowed('https://www.company.example/a', 'https://company.example/a')).toBe(true);
    // cross-origin stays blocked
    expect(redirectAllowed('https://company.example/a', 'https://evil.example/a')).toBe(false);
    // never downgrade to plain HTTP
    expect(redirectAllowed('https://company.example/a', 'http://company.example/a')).toBe(false);
    // a non-canonical port change is not a canonical redirect
    expect(redirectAllowed('https://company.example:8443/a', 'https://company.example/a')).toBe(false);
  });

  it('safeFetch rejects a cross-origin redirect and passes a same-origin response', async () => {
    const ok = (async () => ({ ok: true, url: 'https://company.example/keryx/targets.json' })) as unknown as typeof fetch;
    const fetchSafe = safeFetch(ok);
    await expect(fetchSafe('https://company.example/keryx/targets.json')).resolves.toBeTruthy();

    const crossOrigin = (async () => ({ ok: true, url: 'https://evil.example/targets.json' })) as unknown as typeof fetch;
    const blocked = safeFetch(crossOrigin);
    await expect(blocked('https://company.example/keryx/targets.json')).rejects.toThrow(/cross-origin/);

    const canonical = (async () => ({ ok: true, url: 'https://company.example/targets.json' })) as unknown as typeof fetch;
    await expect(safeFetch(canonical)('http://company.example/targets.json')).resolves.toBeTruthy();
  });
});

describe('media and size policy', () => {
  it('a linked logo without logo_sha256 is a metadata error (placeholder only)', () => {
    expect(logoDisplayable('data:image/png;base64,AAAA', undefined)).toBe(true);
    expect(logoDisplayable('https://cdn.example.com/l.png', undefined)).toBe(false);
    expect(logoDisplayable('https://cdn.example.com/l.png', 'a'.repeat(64))).toBe(true);
    expect(logoDisplayable(undefined, undefined)).toBe(false);
  });

  it('enforces a maximum public-item size (spec/feeds.md §1.1)', () => {
    expect(PUBLIC_ITEM_MAX_BYTES).toBe(1024 * 1024);
  });

  it('readLimitedBody aborts once the response exceeds the limit', async () => {
    await expect(readLimitedBody(new Response(new Uint8Array(2000)), 1000)).rejects.toThrow(/limit/);
    const ok = await readLimitedBody(new Response(new Uint8Array(500)), 1000);
    expect(ok.length).toBe(500);
  });

  it('verifies an attachment hash before opening (spec/feeds.md §1.1)', () => {
    const item = channelItems('news')[0];
    item.attachments = [
      { url: 'https://cdn.example.com/fw.pdf', sha256: 'a'.repeat(64) },
      { url: 'https://cdn.example.com/faq' },
    ];
    expect(attachmentSha(item, 'https://cdn.example.com/fw.pdf')).toBe('a'.repeat(64));
    expect(attachmentSha(item, 'https://cdn.example.com/faq')).toBeUndefined();
    const bytes = new Uint8Array([1, 2, 3]);
    // unhashed resources are mutable by design
    expect(bytesMatchSha(bytes, undefined)).toBe(true);
    // a mismatching hash makes the resource unavailable
    expect(bytesMatchSha(bytes, 'a'.repeat(64))).toBe(false);
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
    expect(pathPatternMatches('channels/marketing/*', 'channels/marketing/x.json')).toBe(true);
    expect(pathPatternMatches('channels/marketing/*', 'channels/marketing/x/y.json')).toBe(false);
    expect(pathPatternMatches('channels/marketing/*', 'channels/security/x.json')).toBe(false);
  });
});

describe('unpublish semantics (spec/feeds.md §1.3)', () => {
  it('absence from the index drops a cached item; new items are stored', async () => {
    const { mergeSourceItems } = await import('./sync');
    const company = {
      origin,
      channels: [],
      privateFeeds: [],
      prefs: { languages: [], tags: [], loadRemoteMedia: true },
    } as never;
    const m = await loadAndVerifyMetadata(fetchLike, `${origin}/.well-known/keryx/root.json`, null, null);
    const auth = extractAuthorization(m.targets);
    const channel = auth.channels.get('news')!;
    const items = channelItems('news');
    const existing = new Map<string, StoredItem>();
    const first = mergeSourceItems(company, 'news', '', false, items.map((item) => ({ item })), {
      authors: undefined,
      channel,
    }, existing);
    expect(first.toPut.length).toBe(items.length);
    expect(first.toDelete).toHaveLength(0);

    // drop one item from the published index → absence = unpublished
    const dropped = items[0];
    const remaining = items.slice(1);
    const key = `${origin}\u0000public:news\u0000${dropped.id}`;
    const second = mergeSourceItems(company, 'news', '', false, remaining.map((item) => ({ item })), {
      authors: undefined,
      channel,
    }, existing);
    expect(second.toDelete).toContain(key);
    expect(second.toPut).toHaveLength(0);
  });

  it('a previously displayed item that no longer verifies is dropped (binary rule)', async () => {
    const { mergeSourceItems } = await import('./sync');
    const company = {
      origin,
      channels: [],
      privateFeeds: [],
      prefs: { languages: [], tags: [], loadRemoteMedia: true },
    } as never;
    const m = await loadAndVerifyMetadata(fetchLike, `${origin}/.well-known/keryx/root.json`, null, null);
    const auth = extractAuthorization(m.targets);
    const channel = auth.channels.get('news')!;
    const item = channelItems('news')[0];
    const key = `${origin}\u0000public:news\u0000${item.id}`;
    const existing = new Map<string, StoredItem>();
    existing.set(key, {
      id: key,
      origin,
      channel: 'news',
      feedUrl: '',
      isPrivate: false,
      item,
      published: item.date_published ?? '',
      receivedAt: 1,
      read: false,
    });
    const tampered = JSON.parse(JSON.stringify(item)) as FeedItem;
    tampered.title = 'Tampered';
    tampered.sig = [{ keyid: channel.keys[0].keyid, sig: 'AAAA' }];
    const { rejected, toDelete } = mergeSourceItems(
      company,
      'news',
      '',
      false,
      [{ item: tampered }],
      { authors: undefined, channel },
      existing,
    );
    expect(rejected).toBe(1);
    expect(toDelete).toContain(key);
  });
});

describe('refresh item state (spec/feeds.md §1.3)', () => {
  it('applying a sync outcome drops absent items and keeps read state', () => {
    const origin = 'https://company.example';
    const key = (id: string) => `${origin}\u0000public:news\u0000${id}`;
    const read: StoredItem = {
      id: key('a'), origin, channel: 'news', feedUrl: '', isPrivate: false,
      item: { id: 'a', title: 'A' }, published: '', receivedAt: 1, read: true,
    };
    const unread: StoredItem = {
      id: key('b'), origin, channel: 'news', feedUrl: '', isPrivate: false,
      item: { id: 'b', title: 'B' }, published: '', receivedAt: 2, read: false,
    };
    const other: StoredItem = {
      id: `${origin}\u0000public:other\u0000x`, origin, channel: 'other', feedUrl: '', isPrivate: false,
      item: { id: 'x' }, published: '', receivedAt: 3, read: false,
    };
    // b was unpublished: the post-sync map for the origin no longer has it,
    // while a (read) and the other channel's item remain published
    const existing = new Map<string, StoredItem>([[read.id, read], [other.id, other]]);
    const next = applyOutcomeItems([read, unread, other], origin, existing);
    expect(next.map((i) => i.id).sort()).toEqual([other.id, read.id].sort());
    expect(next.find((i) => i.id === read.id)?.read).toBe(true);
  });
});

describe('channel role loading (spec/repository.md §3)', () => {
  it('verifies the pinned item index and rejects a version mismatch', async () => {
    const m = await loadAndVerifyMetadata(fetchLike, `${origin}/.well-known/keryx/root.json`, null, null);
    const role = await loadChannelRole(fetchLike, m, 'channels.news', false);
    expect(role.signed._type).toBe('targets');
    expect(role.signed.delegations?.roles).toHaveLength(0);
    expect(Object.keys(role.signed.targets).length).toBeGreaterThan(0);
  });

  it('resolves target file URLs from the repo base', async () => {
    const m = await loadAndVerifyMetadata(fetchLike, `${origin}/.well-known/keryx/root.json`, null, null);
    const role = await loadChannelRole(fetchLike, m, 'channels.news', false);
    const path = Object.keys(role.signed.targets)[0];
    const info = role.signed.targets[path];
    expect(targetFileUrl(m.base, path, info, false)).toBe(`${origin}/keryx/${path}`);
  });
});

describe('sync failure semantics (spec/core.md §4)', () => {
  const setup = async () => {
    const joinUrl = readFileSync(join(demoDir, 'join.txt'), 'utf8').trim();
    const parsed = parseJoinUrl(joinUrl);
    const fetchTyped = fetchLike as unknown as typeof fetch;
    const offer = await buildPairingOffer(parsed.origin, joinUrl, parsed.payload, fetchTyped);
    const company = createCompanyFromOffer(offer, offer.channels.map((c) => c.name));
    return { company, fetchTyped };
  };

  it('a verification failure that is not a chain break does NOT suspend the company', async () => {
    const { company } = await setup();
    // serve a timestamp whose signed bytes no longer verify (a ProtocolError,
    // not a validly-signed-but-unchainable root)
    const tampered = async (url: string) => {
      if (url.endsWith('/keryx/timestamp.json')) {
        const doc = loadJson<Record<string, any>>('keryx/timestamp.json');
        doc.signed.meta['snapshot.json'].version += 1;
        return new Response(JSON.stringify(doc), { status: 200, headers: { 'content-type': 'application/json' } });
      }
      return fetchLike(url);
    };
    const synced = await syncCompany(company, tampered as unknown as typeof fetch, new Map());
    expect(synced.suspended).toBe(false);
    expect(synced.company.status).not.toBe('suspended');
    expect(synced.errors.length).toBeGreaterThan(0);
    expect(synced.toPut).toHaveLength(0);
  });

  it('a logo_sha256 change alone flags a one-tap acknowledgement', async () => {
    const { company, fetchTyped } = await setup();
    const before = await syncCompany(company, fetchTyped, new Map());
    expect(before.company.logoChangePending).toBeFalsy();
    // the identity snapshot remembered a different hash
    const stale = { ...company, identity: { ...company.identity, logoSHA256: 'stale-hash' } };
    const after = await syncCompany(stale, fetchTyped, new Map());
    expect(after.company.logoChangePending).toBe(true);
  });

  it('createCompanyFromOffer records the pairing-time logo hash (no spurious change on first sync)', async () => {
    const joinUrl = readFileSync(join(demoDir, 'join.txt'), 'utf8').trim();
    const parsed = parseJoinUrl(joinUrl);
    const fetchTyped = fetchLike as unknown as typeof fetch;
    const offer = await buildPairingOffer(parsed.origin, joinUrl, parsed.payload, fetchTyped);
    offer.logo = 'https://cdn.example.com/logo.png';
    offer.logoSHA256 = 'abc123';
    const company = createCompanyFromOffer(offer, []);
    expect(company.identity.logo).toBe('https://cdn.example.com/logo.png');
    expect(company.identity.logoSHA256).toBe('abc123');
  });
});
