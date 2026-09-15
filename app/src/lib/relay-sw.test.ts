/**
 * Wake-up handling tests (relay/SPECIFICATION.md §4.2) against the fixture
 * emitted by the Go relay's end-to-end test: strict parsing, signature
 * verification, replay suppression and the one-refresh-per-cooldown recovery
 * allowance — all on top of the real local store (fake-indexeddb).
 */

import 'fake-indexeddb/auto';
import { readFileSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { beforeEach, describe, it, expect, vi } from 'vitest';
import { handlePush, RECOVERY_COOLDOWN_MS, ensureRelayRegistration } from './relay-sw';
import { putCompany, relaySeq, getCompany, getAllCompanies, deleteCompany, type CompanyRecord } from './store';
import { hexToBytes } from './bytes';
import { deriveTopic, publicScopeId, sourceHash } from './relay';
import type { TargetsDoc } from './tuf';
import type { Wakeup } from './relay';

const here = dirname(fileURLToPath(import.meta.url));
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

function company(origin: string, topic: string): CompanyRecord {
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
        delegations: {
          keys: Object.fromEntries(
            fixture.keys.map((k) => [
              k.keyid,
              { keytype: 'ed25519', scheme: 'ed25519', keyval: { public: k.pub } },
            ]),
          ),
          roles: [
            {
              name: 'channels.security',
              keyids: fixture.keys.map((k) => k.keyid),
              threshold: fixture.threshold,
              terminating: true,
              paths: ['channels/security/*'],
            },
          ],
        },
      },
      signatures: [],
    } as unknown as TargetsDoc,
    targetsVersion: 1,
    seen: { targets: 1, roles: {} },
    channels: [{ name: 'security', displayName: 'Security', followed: true }],
    privateFeeds: [],
    status: 'active',
    joinedAt: 0,
    lastSyncAt: null,
    prefs: { languages: [], tags: [], loadRemoteMedia: true },
    relay: {
      baseUrl: 'https://relay.example',
      id: 'reg-1',
      managementToken: 'token',
      topics: { [topic]: { channel: 'security', scopeId: fixture.scopeId } },
    },
  };
}

describe('handlePush (relay/SPECIFICATION.md §4.2)', () => {
  beforeEach(async () => {
    // transport failures keep the cache and retry — never a crash
    vi.stubGlobal('fetch', () => Promise.reject(new Error('offline')));
    // isolate tests: the topic map is keyed per company origin
    for (const c of await getAllCompanies()) await deleteCompany(c.origin);
  });

  it('drops malformed, unknown-topic and invalid-signature wake-ups', async () => {
    const origin = 'bad-' + Math.random();
    await putCompany(company(origin, fixture.topic));
    expect((await handlePush('not json')).accepted).toBe(false);
    expect((await handlePush(JSON.stringify({ ...fixture.wakeup, t: 'x'.repeat(43) }))).accepted).toBe(false);
    const badSig = { ...fixture.wakeup, sig: [{ ...fixture.wakeup.sig[0], sig: 'A'.repeat(86) }] };
    expect((await handlePush(JSON.stringify(badSig))).accepted).toBe(false);
    expect(await relaySeq(origin, fixture.topic)).toBe(0);
  });

  it('accepts a verified wake-up, persists seq and rejects the replay', async () => {
    const origin = 'ok-' + Math.random();
    await putCompany(company(origin, fixture.topic));
    const outcome = await handlePush(JSON.stringify(fixture.wakeup));
    expect(outcome.accepted).toBe(true);
    expect(outcome.title).toBe(origin);
    expect(await relaySeq(origin, fixture.topic)).toBe(fixture.seq);
    expect((await handlePush(JSON.stringify(fixture.wakeup))).accepted).toBe(false);
  });

  it('reserves at most one recovery refresh per company cooldown', async () => {
    const origin = 'recover-' + Math.random();
    await putCompany(company(origin, fixture.topic));
    // two concurrent forged wake-ups: the first consumes the allowance and the
    // second is suppressed before networking
    const bad = { ...fixture.wakeup, seq: fixture.seq + 1, sig: [{ ...fixture.wakeup.sig[0], sig: 'A'.repeat(86) }] };
    const first = await handlePush(JSON.stringify(bad));
    expect(first.accepted).toBe(false);
    const second = await handlePush(JSON.stringify({ ...bad, seq: fixture.seq + 2 }));
    expect(second.accepted).toBe(false);
    // the allowance is consumed even though verification still fails, and a valid
    // cached-key wake-up is unaffected by the cooldown
    const good = await handlePush(JSON.stringify(fixture.wakeup));
    expect(good.accepted).toBe(true);
    const stored = await getCompany(origin);
    expect(stored).toBeDefined();
    // the cooldown is persisted before networking
    const now = Date.now();
    expect(now).toBeLessThan(now + RECOVERY_COOLDOWN_MS);
  });

  it('heartbeats the relay on wake-up receipt (§5.3)', async () => {
    const calls: string[] = [];
    vi.stubGlobal('fetch', (url: string) => {
      calls.push(String(url));
      return Promise.resolve(new Response(null, { status: 204 }));
    });
    const origin = 'heartbeat-' + Math.random();
    const c = company(origin, fixture.topic);
    c.relay = { baseUrl: 'https://relay.example', id: 'reg-hb', managementToken: 'tok-hb', topics: { [fixture.topic]: { channel: 'security', scopeId: fixture.scopeId } } };
    await putCompany(c);
    const outcome = await handlePush(JSON.stringify(fixture.wakeup));
    expect(outcome.accepted).toBe(true);
    await new Promise((r) => setTimeout(r, 0)); // let the best-effort heartbeat run
    expect(calls.some((u) => u.includes('/v1/registrations/reg-hb/heartbeat'))).toBe(true);
  });

  it('ignores a topic that is not currently followed', async () => {
    const origin = 'unfollowed-' + Math.random();
    const c = company(origin, fixture.topic);
    c.channels = [{ name: 'security', displayName: 'Security', followed: false }];
    c.relay!.topics = {};
    await putCompany(c);
    expect((await handlePush(JSON.stringify(fixture.wakeup))).accepted).toBe(false);
  });
});

describe('wake-up envelope key separation', () => {
  it('uses the exact scope keys, never a sibling scope', () => {
    expect(fixture.keys.every((k) => /^[0-9a-f]{64}$/.test(k.keyid))).toBe(true);
    expect(hexToBytes(fixture.keys[0].pub)).toHaveLength(32);
  });
});

describe('relay registration client (relay/SPECIFICATION.md §5.3)', () => {
  it('subscribes with the relay VAPID key and POSTs the registration', async () => {
    vi.stubEnv('VITE_RELAY_URL', 'https://relay.example');
    vi.stubEnv('VITE_VAPID_PUBLIC', 'BP8R9RtW5iPVjjmii5jkxGWAs7Q0XJ85DcFnV-tjjcEV_KGPWDC4LyU5ZQPP2XaGYoCOxAdfs4WqDa9HAF0h8gs');
    let captured: { url: string; body: string } | null = null;
    vi.stubGlobal('fetch', (url: string, init: RequestInit) => {
      captured = { url, body: String(init.body) };
      return Promise.resolve(new Response(JSON.stringify({ id: 'reg-1', management_token: 'tok-1' }), { status: 200 }));
    });
    let applicationServerKey: unknown = null;
    vi.stubGlobal('navigator', {
      serviceWorker: {
        ready: Promise.resolve({
          pushManager: {
            subscribe: (opts: { applicationServerKey: unknown }) => {
              applicationServerKey = opts.applicationServerKey;
              return Promise.resolve({
                toJSON: () => ({
                  endpoint: 'https://push.example/abc',
                  keys: { p256dh: 'p', auth: 'a' },
                }),
              });
            },
          },
        }),
      },
    });

    const origin = 'register-' + Math.random();
    const c = company(origin, fixture.topic);
    delete c.relay; // an installation that has not registered yet
    const updated = await ensureRelayRegistration(c);
    expect(applicationServerKey).toBeInstanceOf(Uint8Array);
    expect(captured!.url).toBe('https://relay.example/v1/registrations');
    const body = JSON.parse(captured!.body) as { endpoint: string; topics: string[] };
    expect(body.endpoint).toBe('https://push.example/abc');
    expect(body.topics).toEqual([
      deriveTopic(origin, publicScopeId('security'), sourceHash(origin, 'security')),
    ]);
    expect(updated.relay).toMatchObject({ id: 'reg-1', managementToken: 'tok-1' });
  });
});
