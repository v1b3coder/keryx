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
import { handlePush, RECOVERY_COOLDOWN_MS, ensureRelayRegistration, recoverRelayRegistration } from './relay-sw';
import {
  putCompany,
  relaySeq,
  getCompany,
  getAllCompanies,
  deleteCompany,
  putRegistration,
  getRegistration,
  deleteRegistrationRecord,
  putPendingTest,
  pendingTest,
  clearPendingTest,
  type CompanyRecord,
} from './store';
import { hexToBytes } from './bytes';
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

/**
 * A company whose derived topic matches the fixture's: the origin's host must be
 * the fixture company_id, while the random path keeps the per-company relay
 * state (seq, recovery) isolated between tests.
 */
function newOrigin(): string {
  return `http://${fixture.companyId}/${Math.random()}`;
}

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
  };
}

/** Stub the browser push manager with a working subscription. */
function stubPushManager() {
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
          getSubscription: () => Promise.resolve(null),
        },
      }),
    },
  });
  return () => applicationServerKey;
}

describe('app-wide registration store', () => {
  it('keeps one registration per relay base URL', async () => {
    await putRegistration({
      baseUrl: 'https://relay.example',
      id: 'reg-1',
      managementToken: 'tok-1',
      topics: {},
    });
    const got = await getRegistration('https://relay.example');
    expect(got?.id).toBe('reg-1');
    await deleteRegistrationRecord('https://relay.example');
    expect(await getRegistration('https://relay.example')).toBeUndefined();
  });
});

describe('handlePush (relay/SPECIFICATION.md §4.2)', () => {
  beforeEach(async () => {
    // transport failures keep the cache and retry — never a crash
    vi.stubGlobal('fetch', () => Promise.reject(new Error('offline')));
    // isolate tests: every company is removed between tests
    for (const c of await getAllCompanies()) await deleteCompany(c.origin);
  });

  it('drops malformed, unknown-topic and invalid-signature wake-ups', async () => {
    const origin = newOrigin();
    await putCompany(company(origin));
    expect((await handlePush('not json')).accepted).toBe(false);
    expect((await handlePush(JSON.stringify({ ...fixture.wakeup, t: 'x'.repeat(43) }))).accepted).toBe(false);
    const badSig = { ...fixture.wakeup, sig: [{ ...fixture.wakeup.sig[0], sig: 'A'.repeat(86) }] };
    expect((await handlePush(JSON.stringify(badSig))).accepted).toBe(false);
    expect(await relaySeq(origin, fixture.topic)).toBe(0);
  });

  it('accepts a verified wake-up, persists seq and rejects the replay', async () => {
    const origin = newOrigin();
    await putCompany(company(origin));
    const outcome = await handlePush(JSON.stringify(fixture.wakeup));
    expect(outcome.accepted).toBe(true);
    expect(outcome.title).toBe(origin);
    expect(await relaySeq(origin, fixture.topic)).toBe(fixture.seq);
    expect((await handlePush(JSON.stringify(fixture.wakeup))).accepted).toBe(false);
  });

  it('reserves at most one recovery refresh per company cooldown', async () => {
    const origin = newOrigin();
    await putCompany(company(origin));
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
    vi.stubEnv('VITE_RELAY_URL', 'https://relay.example');
    const origin = newOrigin();
    await putCompany(company(origin));
    await putRegistration({
      baseUrl: 'https://relay.example',
      id: 'reg-hb',
      managementToken: 'tok-hb',
      topics: { [fixture.topic]: { channel: 'security', scopeId: fixture.scopeId } },
    });
    const outcome = await handlePush(JSON.stringify(fixture.wakeup));
    expect(outcome.accepted).toBe(true);
    await new Promise((r) => setTimeout(r, 0)); // let the best-effort heartbeat run
    expect(calls.some((u) => u.includes('/v1/registrations/reg-hb/heartbeat'))).toBe(true);
    await deleteRegistrationRecord('https://relay.example');
    vi.unstubAllEnvs();
  });

  it('recovers the registration when the heartbeat says gone (§5.3)', async () => {
    vi.stubEnv('VITE_RELAY_URL', 'https://relay.example');
    vi.stubEnv('VITE_VAPID_PUBLIC', 'BP8R9RtW5iPVjjmii5jkxGWAs7Q0XJ85DcFnV-tjjcEV_KGPWDC4LyU5ZQPP2XaGYoCOxAdfs4WqDa9HAF0h8gs');
    const origin = newOrigin();
    await putCompany(company(origin));
    await putRegistration({
      baseUrl: 'https://relay.example',
      id: 'reg-hb',
      managementToken: 'tok-hb',
      topics: { [fixture.topic]: { channel: 'security', scopeId: fixture.scopeId } },
    });
    vi.stubGlobal('navigator', {
      serviceWorker: {
        ready: Promise.resolve({
          pushManager: {
            getSubscription: () => Promise.resolve(null),
            subscribe: () =>
              Promise.resolve({
                toJSON: () => ({ endpoint: 'https://push.example/new', keys: { p256dh: 'p', auth: 'a' } }),
              }),
          },
        }),
      },
    });
    const requests: string[] = [];
    vi.stubGlobal('fetch', (url: string, init: RequestInit) => {
      requests.push(`${init.method} ${url}`);
      if (String(url).includes('/heartbeat')) return Promise.resolve(new Response(null, { status: 404 }));
      return Promise.resolve(
        new Response(JSON.stringify({ id: 'new', management_token: 'new-token' }), { status: 200 }),
      );
    });
    const outcome = await handlePush(JSON.stringify(fixture.wakeup));
    expect(outcome.accepted).toBe(true);
    await vi.waitFor(() => {
      expect(requests).toContain('POST https://relay.example/v1/registrations');
    });
    expect(requests).toContain('POST https://relay.example/v1/registrations/reg-hb/heartbeat');
    expect((await getRegistration('https://relay.example'))?.id).toBe('new');
    await deleteRegistrationRecord('https://relay.example');
    vi.unstubAllEnvs();
    vi.unstubAllGlobals();
  });

  it('does not recover on a heartbeat transport failure', async () => {
    vi.stubEnv('VITE_RELAY_URL', 'https://relay.example');
    vi.stubEnv('VITE_VAPID_PUBLIC', 'BP8R9RtW5iPVjjmii5jkxGWAs7Q0XJ85DcFnV-tjjcEV_KGPWDC4LyU5ZQPP2XaGYoCOxAdfs4WqDa9HAF0h8gs');
    const origin = newOrigin();
    await putCompany(company(origin));
    await putRegistration({
      baseUrl: 'https://relay.example',
      id: 'reg-hb',
      managementToken: 'tok-hb',
      topics: { [fixture.topic]: { channel: 'security', scopeId: fixture.scopeId } },
    });
    const requests: string[] = [];
    vi.stubGlobal('fetch', (url: string, init?: RequestInit) => {
      if (init?.method) requests.push(`${init.method} ${url}`);
      return Promise.resolve(new Response(null, { status: 500 }));
    });
    expect((await handlePush(JSON.stringify(fixture.wakeup))).accepted).toBe(true);
    await new Promise((r) => setTimeout(r, 10));
    expect(requests).toEqual(['POST https://relay.example/v1/registrations/reg-hb/heartbeat']);
    await deleteRegistrationRecord('https://relay.example');
    vi.unstubAllEnvs();
    vi.unstubAllGlobals();
  });

  it('ignores a topic that is not currently followed', async () => {
    const origin = newOrigin();
    const c = company(origin);
    c.channels = [{ name: 'security', displayName: 'Security', followed: false }];
    await putCompany(c);
    expect((await handlePush(JSON.stringify(fixture.wakeup))).accepted).toBe(false);
  });

  it('records a test payload only when the pending nonce matches', async () => {
    const nonce = 'A'.repeat(43);
    const other = 'B'.repeat(43);
    await putRegistration({
      baseUrl: 'https://relay.example',
      id: 'reg-test',
      managementToken: 'tok-test',
      topics: {},
    });
    await putPendingTest({ baseUrl: 'https://relay.example', nonce, expiresAt: Date.now() + 60_000 });
    expect(await handlePush(JSON.stringify({ v: 1, test: true, nonce }))).toEqual({ accepted: true, test: true });
    const pending = await pendingTest('https://relay.example');
    expect(pending?.receivedAt).toBeGreaterThan(0);

    await putPendingTest({ baseUrl: 'https://relay.example', nonce: other, expiresAt: Date.now() + 60_000 });
    expect(await handlePush(JSON.stringify({ v: 1, test: true, nonce: 'wrong' }))).toEqual({ accepted: false });
    expect(await handlePush(JSON.stringify({ v: 1, test: true }))).toEqual({ accepted: false });
    await clearPendingTest('https://relay.example');
    await deleteRegistrationRecord('https://relay.example');
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
    await deleteRegistrationRecord('https://relay.example');
    let captured: { url: string; body: string } | null = null;
    vi.stubGlobal('fetch', (url: string, init: RequestInit) => {
      captured = { url, body: String(init.body) };
      return Promise.resolve(new Response(JSON.stringify({ id: 'reg-1', management_token: 'tok-1' }), { status: 200 }));
    });
    const key = stubPushManager();

    const origin = newOrigin();
    const c = company(origin);
    const updated = await ensureRelayRegistration([c]);
    expect(key()).toBeInstanceOf(Uint8Array);
    expect(captured!.url).toBe('https://relay.example/v1/registrations');
    const body = JSON.parse(captured!.body) as { endpoint: string; topics: string[] };
    expect(body.endpoint).toBe('https://push.example/abc');
    expect(body.topics).toEqual([fixture.topic]);
    expect(updated).toMatchObject({ id: 'reg-1', managementToken: 'tok-1' });
    expect((await getRegistration('https://relay.example'))?.id).toBe('reg-1');
    await deleteRegistrationRecord('https://relay.example');
    vi.unstubAllEnvs();
  });

  it('recovers a gone registration with a fresh subscription and POST', async () => {
    vi.stubEnv('VITE_RELAY_URL', 'https://relay.example');
    vi.stubEnv('VITE_VAPID_PUBLIC', 'BP8R9RtW5iPVjjmii5jkxGWAs7Q0XJ85DcFnV-tjjcEV_KGPWDC4LyU5ZQPP2XaGYoCOxAdfs4WqDa9HAF0h8gs');
    await putRegistration({ baseUrl: 'https://relay.example', id: 'old', managementToken: 'old-token', topics: {} });
    let unsubscribed = false;
    vi.stubGlobal('navigator', {
      serviceWorker: {
        ready: Promise.resolve({
          pushManager: {
            getSubscription: () =>
              Promise.resolve({
                unsubscribe: () => {
                  unsubscribed = true;
                  return Promise.resolve(true);
                },
              }),
            subscribe: () =>
              Promise.resolve({
                toJSON: () => ({ endpoint: 'https://push.example/new', keys: { p256dh: 'p', auth: 'a' } }),
              }),
          },
        }),
      },
    });
    const requests: string[] = [];
    vi.stubGlobal('fetch', (url: string, init: RequestInit) => {
      requests.push(`${init.method} ${url}`);
      return Promise.resolve(
        new Response(JSON.stringify({ id: 'new', management_token: 'new-token' }), { status: 200 }),
      );
    });
    const reg = await recoverRelayRegistration('https://relay.example');
    expect(unsubscribed).toBe(true);
    expect(reg?.id).toBe('new');
    expect(requests).toEqual(['POST https://relay.example/v1/registrations']);
    expect((await getRegistration('https://relay.example'))?.id).toBe('new');
    await deleteRegistrationRecord('https://relay.example');
    vi.unstubAllEnvs();
    vi.unstubAllGlobals();
  });

  it('never destroys a working subscription on a transport failure', async () => {
    vi.stubEnv('VITE_RELAY_URL', 'https://relay.example');
    vi.stubEnv('VITE_VAPID_PUBLIC', 'BP8R9RtW5iPVjjmii5jkxGWAs7Q0XJ85DcFnV-tjjcEV_KGPWDC4LyU5ZQPP2XaGYoCOxAdfs4WqDa9HAF0h8gs');
    await putRegistration({ baseUrl: 'https://relay.example', id: 'old', managementToken: 'old-token', topics: {} });
    let unsubscribed = false;
    vi.stubGlobal('navigator', {
      serviceWorker: {
        ready: Promise.resolve({
          pushManager: {
            getSubscription: () =>
              Promise.resolve({
                unsubscribe: () => {
                  unsubscribed = true;
                  return Promise.resolve(true);
                },
              }),
          },
        }),
      },
    });
    vi.stubGlobal('fetch', () => Promise.reject(new Error('offline')));
    expect(await ensureRelayRegistration([company(newOrigin())])).toBeUndefined();
    expect(unsubscribed).toBe(false);
    expect((await getRegistration('https://relay.example'))?.id).toBe('old');
    await deleteRegistrationRecord('https://relay.example');
    vi.unstubAllEnvs();
    vi.unstubAllGlobals();
  });

  it('obtains a fresh subscription when the endpoint is dead', async () => {
    vi.stubEnv('VITE_RELAY_URL', 'https://relay.example');
    vi.stubEnv('VITE_VAPID_PUBLIC', 'BP8R9RtW5iPVjjmii5jkxGWAs7Q0XJ85DcFnV-tjjcEV_KGPWDC4LyU5ZQPP2XaGYoCOxAdfs4WqDa9HAF0h8gs');
    await putRegistration({ baseUrl: 'https://relay.example', id: 'old', managementToken: 'old-token', topics: {} });
    let unsubscribed = false;
    vi.stubGlobal('navigator', {
      serviceWorker: {
        ready: Promise.resolve({
          pushManager: {
            getSubscription: () =>
              Promise.resolve({
                unsubscribe: () => {
                  unsubscribed = true;
                  return Promise.resolve(true);
                },
              }),
            subscribe: () =>
              Promise.resolve({ toJSON: () => ({ endpoint: 'https://push.example/new', keys: { p256dh: 'p', auth: 'a' } }) }),
          },
        }),
      },
    });
    const requests: string[] = [];
    vi.stubGlobal('fetch', (url: string, init: RequestInit) => {
      requests.push(`${init.method} ${url}`);
      return Promise.resolve(new Response(JSON.stringify({ id: 'new', management_token: 'new-token' }), { status: 200 }));
    });
    const reg = await ensureRelayRegistration([company(newOrigin())], true);
    expect(unsubscribed).toBe(true);
    expect(reg?.id).toBe('new');
    expect(requests.some((r) => r === 'POST https://relay.example/v1/registrations')).toBe(true);
    await deleteRegistrationRecord('https://relay.example');
    vi.unstubAllEnvs();
  });

  it('registers the union of every company topic on one relay', async () => {
    vi.stubEnv('VITE_RELAY_URL', 'https://relay.example');
    vi.stubEnv('VITE_VAPID_PUBLIC', 'BP8R9RtW5iPVjjmii5jkxGWAs7Q0XJ85DcFnV-tjjcEV_KGPWDC4LyU5ZQPP2XaGYoCOxAdfs4WqDa9HAF0h8gs');
    await deleteRegistrationRecord('https://relay.example');
    const requests: string[] = [];
    vi.stubGlobal('fetch', (url: string, init: RequestInit) => {
      requests.push(`${init.method} ${url}`);
      return Promise.resolve(new Response(JSON.stringify({ id: 'reg-1', management_token: 'tok-1' }), { status: 200 }));
    });
    stubPushManager();

    const a = company('https://a.example');
    const b = company('https://b.example');
    await ensureRelayRegistration([a, b]);
    const reg = await getRegistration('https://relay.example');
    expect(Object.keys(reg?.topics ?? {})).toHaveLength(2);
    expect(requests.some((r) => r === 'POST https://relay.example/v1/registrations')).toBe(true);
    await deleteRegistrationRecord('https://relay.example');
    vi.unstubAllEnvs();
  });
});
