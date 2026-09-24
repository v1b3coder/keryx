import { describe, it, expect, vi } from 'vitest';
import 'fake-indexeddb/auto';
import { notificationState, runSelfTest } from './notify';
import {
  putCompany,
  putRegistration,
  putPendingTest,
  pendingTest,
  clearPendingTest,
  deleteRegistrationRecord,
  deleteCompany,
  type CompanyRecord,
} from './store';
import { topicBindings } from './relay-sw';
import type { TargetsDoc } from './tuf';

function stubPermission(permission: NotificationPermission) {
  vi.stubGlobal('Notification', { permission, requestPermission: () => Promise.resolve(permission) });
  vi.stubGlobal('navigator', { serviceWorker: {} });
  vi.stubGlobal('window', { isSecureContext: true, PushManager: class {} });
}

describe('notification state machine', () => {
  it('reports unsupported when the browser has no push manager', async () => {
    vi.stubGlobal('Notification', undefined);
    expect((await notificationState()).kind).toBe('unsupported');
    vi.unstubAllGlobals();
  });

  it('reports the permission states', async () => {
    stubPermission('default');
    expect((await notificationState()).kind).toBe('default');
    stubPermission('denied');
    expect((await notificationState()).kind).toBe('denied');
    vi.unstubAllGlobals();
  });

  it('reports no-subscription when permission is granted and nothing is registered', async () => {
    stubPermission('granted');
    vi.stubEnv('VITE_RELAY_URL', 'https://relay.example');
    vi.stubEnv('VITE_VAPID_PUBLIC', 'BP8R9RtW5iPVjjmii5jkxGWAs7Q0XJ85DcFnV-tjjcEV_KGPWDC4LyU5ZQPP2XaGYoCOxAdfs4WqDa9HAF0h8gs');
    expect((await notificationState()).kind).toBe('no-subscription');
    vi.unstubAllEnvs();
    vi.unstubAllGlobals();
  });
});

describe('notification state machine: in-flight self-test', () => {
  const origin = 'http://127.0.0.1/x';
  function company(): CompanyRecord {
    return {
      origin,
      joinUrl: '',
      identity: {},
      pinnedRoot: { signed: { version: 1, expires: '2099-01-01T00:00:00Z' }, signatures: [] } as never,
      pinnedRootVersion: 1,
      targets: {
        signed: { _type: 'targets', version: 1, expires: '2099-01-01T00:00:00Z', targets: {}, delegations: { keys: {}, roles: [] } },
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

  it('reports pending while the test nonce is still awaited, then ok when it arrives', async () => {
    stubPermission('granted');
    vi.stubEnv('VITE_RELAY_URL', 'https://relay.example');
    vi.stubEnv('VITE_VAPID_PUBLIC', 'BP8R9RtW5iPVjjmii5jkxGWAs7Q0XJ85DcFnV-tjjcEV_KGPWDC4LyU5ZQPP2XaGYoCOxAdfs4WqDa9HAF0h8gs');
    const c = company();
    await putCompany(c);
    const topics = topicBindings(c);
    await putRegistration({ baseUrl: 'https://relay.example', id: 'reg-1', managementToken: 'tok', topics });
    await putPendingTest({ baseUrl: 'https://relay.example', nonce: 'A'.repeat(43), expiresAt: Date.now() + 60_000 });
    expect((await notificationState()).kind).toBe('pending');

    const pending = await pendingTest('https://relay.example');
    await putPendingTest({ ...pending!, receivedAt: Date.now() });
    const ok = await notificationState();
    expect(ok.kind).toBe('ok');
    expect(ok.testedAt).toBeGreaterThan(0);

    await clearPendingTest('https://relay.example');
    await deleteRegistrationRecord('https://relay.example');
    await deleteCompany(origin);
    vi.unstubAllEnvs();
    vi.unstubAllGlobals();
  });

  it('treats a missing registration like a dead endpoint', async () => {
    vi.stubEnv('VITE_RELAY_URL', 'https://relay.example');
    vi.stubEnv('VITE_VAPID_PUBLIC', 'BP8R9RtW5iPVjjmii5jkxGWAs7Q0XJ85DcFnV-tjjcEV_KGPWDC4LyU5ZQPP2XaGYoCOxAdfs4WqDa9HAF0h8gs');
    const c = company();
    await putCompany(c);
    await putRegistration({ baseUrl: 'https://relay.example', id: 'old', managementToken: 'old-token', topics: topicBindings(c) });
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
    const calls: string[] = [];
    vi.stubGlobal('fetch', (url: string, init: RequestInit) => {
      calls.push(`${init.method} ${url}`);
      // every test call finds the row gone; the fresh POST succeeds
      if (String(url).endsWith('/test')) return Promise.resolve(new Response('x', { status: 404 }));
      return Promise.resolve(
        new Response(JSON.stringify({ id: 'new', management_token: 'new-token' }), { status: 200 }),
      );
    });
    const result = await runSelfTest([c]);
    expect(unsubscribed).toBe(true);
    expect(calls).toContain('POST https://relay.example/v1/registrations');
    expect(result.endpoint).toBe('failed');
    await clearPendingTest('https://relay.example');
    await deleteRegistrationRecord('https://relay.example');
    await deleteCompany(c.origin);
    vi.unstubAllEnvs();
    vi.unstubAllGlobals();
  });
});
