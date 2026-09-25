import { describe, it, expect, vi, beforeEach } from 'vitest';
import 'fake-indexeddb/auto';
import { ensureFcmTopics, fcmTopicsSynced, runFcmSelfTest } from './fcm';
import { KeryxPush } from './native-push';
import { fcmTestReady, startFcmTest } from './relay';
import { unionTopics } from './relay-sw';
import { pendingTest, putPendingTest, type CompanyRecord } from './store';
import type { TargetsDoc } from './tuf';

vi.mock('./native-push', () => ({
  KeryxPush: { setTopics: vi.fn(), getTopics: vi.fn() },
}));
vi.mock('./relay', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./relay')>()),
  relayBaseUrl: () => 'https://relay.example',
  startFcmTest: vi.fn(),
  fcmTestReady: vi.fn(),
}));

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

const unionOf = (...companies: CompanyRecord[]): string[] =>
  Object.keys(unionTopics(companies)).sort();

beforeEach(() => {
  vi.clearAllMocks();
});

describe('FCM topic sync', () => {
  it('subscribes the native SDK to the union of every followed topic', async () => {
    const a = company('http://a.example/x');
    const b = company('http://b.example/y');
    vi.mocked(KeryxPush.setTopics).mockResolvedValue({ topics: ['a', 'b'] });
    expect(await ensureFcmTopics([a, b])).toEqual(['a', 'b']);
    expect(vi.mocked(KeryxPush.setTopics).mock.calls[0]![0].topics).toEqual(unionOf(a, b));
  });

  it('reports the sync failed when the SDK rejects the subscription', async () => {
    vi.mocked(KeryxPush.setTopics).mockRejectedValue(new Error('no play services'));
    expect(await ensureFcmTopics([company('http://a.example/x')])).toBeUndefined();
  });

  it('reports whether the native topic set matches the union', async () => {
    const c = company('http://a.example/x');
    vi.mocked(KeryxPush.getTopics).mockResolvedValue({ topics: unionOf(c) });
    expect(await fcmTopicsSynced([c])).toBe(true);
    vi.mocked(KeryxPush.getTopics).mockResolvedValue({ topics: [] });
    expect(await fcmTopicsSynced([c])).toBe(false);
  });
});

describe('FCM topic-leg self-test', () => {
  it('reports delivered when the handler records the nonce', async () => {
    vi.mocked(startFcmTest).mockResolvedValue({
      testId: 'id',
      topic: 'test-topic',
      nonce: 'n',
      expiresAt: new Date(Date.now() + 60_000).toISOString(),
    });
    vi.mocked(KeryxPush.setTopics).mockResolvedValue({ topics: [] });
    vi.mocked(fcmTestReady).mockImplementation(async () => {
      const pending = await pendingTest('https://relay.example');
      await putPendingTest({ ...pending!, receivedAt: Date.now() });
    });
    const result = await runFcmSelfTest([company('http://a.example/x')], 100);
    expect(result.endpoint).toBe('delivered');
    expect(result.leg).toBe('topic');
  });

  it('reports failed when the relay cannot publish', async () => {
    vi.mocked(startFcmTest).mockResolvedValue({
      testId: 'id',
      topic: 'test-topic',
      nonce: 'n',
      expiresAt: new Date(Date.now() + 60_000).toISOString(),
    });
    vi.mocked(KeryxPush.setTopics).mockResolvedValue({ topics: [] });
    vi.mocked(fcmTestReady).mockRejectedValue(new Error('HTTP 503'));
    const result = await runFcmSelfTest([company('http://a.example/x')], 100);
    expect(result.endpoint).toBe('failed');
    expect(result.leg).toBe('topic');
  });
});
