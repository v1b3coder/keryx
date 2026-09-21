/**
 * Relay protocol tests (relay/SPECIFICATION.md §3–§5) against the real demo
 * targets metadata and a wake-up fixture emitted by the Go relay's end-to-end
 * test — proving the app's derivation and verification match the relay's.
 */

import { readFileSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, it, expect, vi } from 'vitest';
import {
  publicScopeId,
  privateScopeId,
  sourceHash,
  deriveTopic,
  parseWakeup,
  wakeupSignedBytes,
  verifyWakeup,
  topicAuthorization,
  relayBaseUrl,
  vapidPublicKey,
  testRegistration,
  DEFAULT_RELAY_URL,
  DEFAULT_VAPID_PUBLIC,
  type Wakeup,
} from './relay';
import { topicBindings, orderTokenFromUrl } from './relay-sw';
import { extractAuthorization, type TargetsDoc } from './tuf';
import { hexToBytes } from './bytes';
import type { CompanyRecord } from './store';

const here = dirname(fileURLToPath(import.meta.url));
const demoDir = process.env.KERYX_DEMO_DIR ?? join(here, '..', '..', '..', '..', 'keryx-demo');
const fixture = JSON.parse(readFileSync(join(here, '__fixtures__', 'relay-e2e.json'), 'utf8')) as {
  companyId: string;
  scopeId: string;
  h: string;
  topic: string;
  seq: number;
  wakeup: Wakeup;
  keys: { keyid: string; pub: string }[];
  threshold: number;
};

describe('topic derivation (relay/SPECIFICATION.md §3)', () => {
  it('matches the Go relay derivation exactly', () => {
    expect(publicScopeId('security')).toBe(fixture.scopeId);
    expect(sourceHash(fixture.companyId, 'security')).toBe(fixture.h);
    expect(deriveTopic(fixture.companyId, fixture.scopeId, fixture.h)).toBe(fixture.topic);
  });

  it('binds company and scope into the topic', () => {
    const topic = deriveTopic('company.example', 'a'.repeat(64), 'b'.repeat(64));
    expect(topic).toMatch(/^[A-Za-z0-9_-]{43}$/);
    expect(deriveTopic('company.example', 'c'.repeat(64), 'b'.repeat(64))).not.toBe(topic);
    expect(deriveTopic('other.example', 'a'.repeat(64), 'b'.repeat(64))).not.toBe(topic);
  });

  it('keeps public and private scope identities separate', () => {
    expect(publicScopeId('tracking')).not.toBe(
      privateScopeId('tracking', 'https://company.example/channels/tracking/*/feed.json'),
    );
    expect(privateScopeId('tracking', 'https://a.example/x')).not.toBe(
      privateScopeId('tracking', 'https://a.example/y'),
    );
    // the descriptor excludes keys, so rotation leaves scope_id unchanged
    expect(privateScopeId('tracking', 'https://a.example/x')).toBe(
      privateScopeId('tracking', 'https://a.example/x'),
    );
  });

  it('reads the order token from a capability URL', () => {
    expect(orderTokenFromUrl('https://company.example/channels/tracking/AbCdEf0123456789_-xyZ/feed.json')).toBe(
      'AbCdEf0123456789_-xyZ',
    );
    expect(orderTokenFromUrl('not a url')).toBe(null);
  });
});

describe('wake-up envelope (relay/SPECIFICATION.md §4)', () => {
  it('signs exactly the domain-separated OLPC of {v, t, seq}', () => {
    const bytes = new TextDecoder().decode(wakeupSignedBytes(1, 'abc', 7));
    expect(bytes).toBe('keryx/wakeup/v1|{"seq":7,"t":"abc","v":1}');
  });

  it('verifies the relay-generated wake-up against the scope keys', () => {
    const wakeup = parseWakeup(JSON.stringify(fixture.wakeup));
    expect(wakeup.t).toBe(fixture.topic);
    expect(wakeup.seq).toBe(fixture.seq);
    const keys = fixture.keys.map((k) => ({ keyid: k.keyid, pub: hexToBytes(k.pub) }));
    expect(verifyWakeup(wakeup, keys, fixture.threshold)).toBe(true);
  });

  it('rejects an invalid signature, a changed topic and a changed seq', () => {
    const keys = fixture.keys.map((k) => ({ keyid: k.keyid, pub: hexToBytes(k.pub) }));
    const wakeup = parseWakeup(JSON.stringify(fixture.wakeup));

    const badSig: Wakeup = { ...wakeup, sig: [{ ...wakeup.sig[0], sig: 'A'.repeat(86) }] };
    expect(verifyWakeup(badSig, keys, fixture.threshold)).toBe(false);
    expect(verifyWakeup({ ...wakeup, t: 'x'.repeat(43) }, keys, fixture.threshold)).toBe(false);
    expect(verifyWakeup({ ...wakeup, seq: wakeup.seq + 1 }, keys, fixture.threshold)).toBe(false);
    // unknown keyids are ignored, so the threshold is not met
    expect(
      verifyWakeup({ ...wakeup, sig: [{ keyid: 'f'.repeat(64), sig: wakeup.sig[0].sig }] }, keys, 1),
    ).toBe(false);
  });

  it('resolves only the exact scope authorization for a topic binding', () => {
    const targets = JSON.parse(
      readFileSync(join(demoDir, 'keryx', 'targets.json'), 'utf8'),
    ) as TargetsDoc;
    const publicAuth = topicAuthorization(targets, { channel: 'security', scopeId: publicScopeId('security') });
    expect(publicAuth).not.toBe(null);
    expect(publicAuth!.keys.length).toBeGreaterThan(0);
    // a wrong scope_id must not select the same keys
    expect(topicAuthorization(targets, { channel: 'security', scopeId: publicScopeId('news') })).toBe(null);
    expect(topicAuthorization(targets, { channel: 'nope', scopeId: publicScopeId('security') })).toBe(null);
  });

  it('strictly parses the envelope and rejects unknown fields', () => {
    const valid = JSON.stringify(fixture.wakeup);
    expect(parseWakeup(valid).t).toBe(fixture.topic);
    expect(() => parseWakeup(`{"v":2,"t":"${fixture.topic}","seq":1,"sig":[]}`)).toThrow();
    expect(() => parseWakeup(`{"v":1,"t":"${fixture.topic}","seq":1,"sig":[],"n":3}`)).toThrow();
    expect(() => parseWakeup(`{"v":1,"v":1,"t":"${fixture.topic}","seq":1,"sig":[]}`)).toThrow();
    expect(() => parseWakeup(`{"v":1,"t":"${fixture.topic}","seq":0,"sig":[]}`)).toThrow();
    expect(() => parseWakeup(`{"v":1,"t":"${fixture.topic}","seq":1.5,"sig":[]}`)).toThrow();
    expect(() => parseWakeup(`{"v":1,"t":"${fixture.topic}","seq":9007199254740992,"sig":[]}`)).toThrow();
    expect(() => parseWakeup(`{"v":1,"t":"${fixture.topic}","seq":1}`)).toThrow();
  });
});

describe('topic bindings (§10)', () => {
  const targets = JSON.parse(
    readFileSync(join(demoDir, 'keryx', 'targets.json'), 'utf8'),
  ) as TargetsDoc;

  function company(): CompanyRecord {
    return {
      origin: 'company.example',
      joinUrl: '',
      identity: {},
      pinnedRoot: { signed: { version: 1, expires: '2099-01-01T00:00:00Z' }, signatures: [] } as never,
      pinnedRootVersion: 1,
      targets,
      targetsVersion: targets.signed.version,
      seen: { targets: targets.signed.version, roles: {} },
      channels: [
        { name: 'security', displayName: 'Security', followed: true },
        { name: 'news', displayName: 'News', followed: false },
      ],
      privateFeeds: [
        {
          url: 'https://company.example/channels/tracking/AbCdEf0123456789_-xyZ/feed.json',
          channel: 'tracking',
        },
      ],
      status: 'active',
      joinedAt: 0,
      lastSyncAt: null,
      prefs: { languages: [], tags: [], loadRemoteMedia: true },
    };
  }

  it('derives a topic for every followed channel and private feed', () => {
    const bindings = topicBindings(company());
    const publicTopic = deriveTopic(
      'company.example',
      publicScopeId('security'),
      sourceHash('company.example', 'security'),
    );
    expect(bindings[publicTopic]).toEqual({ channel: 'security', scopeId: publicScopeId('security') });
    expect(Object.keys(bindings)).toHaveLength(2); // security + the order feed

    const privateTopic = deriveTopic(
      'company.example',
      privateScopeId('tracking', targets.signed.custom!.private_feed_patterns![0].pattern),
      sourceHash('company.example', 'AbCdEf0123456789_-xyZ'),
    );
    expect(bindings[privateTopic].channel).toBe('tracking');
  });
});

describe('self-test client (§5.3.1)', () => {
  it('posts the self-test and returns the nonce', async () => {
    vi.stubEnv('VITE_RELAY_URL', 'https://relay.example');
    vi.stubEnv('VITE_VAPID_PUBLIC', 'BP8R9RtW5iPVjjmii5jkxGWAs7Q0XJ85DcFnV-tjjcEV_KGPWDC4LyU5ZQPP2XaGYoCOxAdfs4WqDa9HAF0h8gs');
    let captured: { url: string; init: RequestInit } | null = null;
    vi.stubGlobal('fetch', (url: string, init: RequestInit) => {
      captured = { url, init };
      return Promise.resolve(new Response(JSON.stringify({ nonce: 'n-1', expires_at: '2026-01-01T00:00:00Z' }), { status: 202 }));
    });
    const got = await testRegistration('https://relay.example', 'reg-1', 'tok-1');
    expect(got.nonce).toBe('n-1');
    expect(captured!.url).toBe('https://relay.example/v1/registrations/reg-1/test');
    expect((captured!.init.headers as Record<string, string>).Authorization).toBe('Bearer tok-1');
    vi.unstubAllEnvs();
    vi.unstubAllGlobals();
  });
});

describe('relay configuration', () => {
  it('defaults to the staging relay in production builds only', () => {
    expect(DEFAULT_RELAY_URL).toBe('https://keryx-relay.fly.dev');
    expect(DEFAULT_VAPID_PUBLIC).toMatch(/^B[A-Za-z0-9_-]{80,}$/);
    // test/dev builds are not production builds: no relay unless configured
    expect(relayBaseUrl()).toBeNull();
    expect(vapidPublicKey()).toBeNull();
  });

  it('lets the build override the relay and its VAPID key', () => {
    vi.stubEnv('VITE_RELAY_URL', 'https://relay.example/');
    vi.stubEnv('VITE_VAPID_PUBLIC', 'BP8R9RtW5iPVjjmii5jkxGWAs7Q0XJ85DcFnV-tjjcEV_KGPWDC4LyU5ZQPP2XaGYoCOxAdfs4WqDa9HAF0h8gs');
    expect(relayBaseUrl()).toBe('https://relay.example');
    expect(vapidPublicKey()).toBe(
      'BP8R9RtW5iPVjjmii5jkxGWAs7Q0XJ85DcFnV-tjjcEV_KGPWDC4LyU5ZQPP2XaGYoCOxAdfs4WqDa9HAF0h8gs',
    );
    vi.unstubAllEnvs();
  });
});
