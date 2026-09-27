import { describe, it, expect, vi, beforeEach } from 'vitest';
import 'fake-indexeddb/auto';
import { notificationState, runSelfTest } from './notify.native';
import { KeryxPush } from './native-push';

vi.mock('react-native', () => ({ Platform: { OS: 'android' } }));
vi.mock('expo-notifications', () => ({
  getPermissionsAsync: vi.fn(),
  requestPermissionsAsync: vi.fn(),
  scheduleNotificationAsync: vi.fn(),
}));
vi.mock('./native-push', () => ({
  KeryxPush: {
    getSupport: vi.fn(),
    getNotificationPermission: vi.fn(),
    requestNotificationPermission: vi.fn(),
    setTopics: vi.fn(),
    getTopics: vi.fn(),
  },
}));
vi.mock('./push', () => ({
  currentPushTransport: vi.fn(),
}));
vi.mock('./fcm', () => ({
  fcmTopicsSynced: vi.fn(),
  runFcmSelfTest: vi.fn(),
}));

import { currentPushTransport } from './push';
import { fcmTopicsSynced } from './fcm';
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

beforeEach(() => {
  vi.clearAllMocks();
});

describe('notification state machine: Android transports', () => {
  it('asks for ntfy when there is no Google services and no distributor', async () => {
    vi.mocked(currentPushTransport).mockResolvedValue('none');
    expect((await notificationState()).kind).toBe('no-transport');
  });

  it('reports default when a distributor is installed but the native permission is off', async () => {
    vi.mocked(currentPushTransport).mockResolvedValue('unifiedpush');
    const Notifications = await import('expo-notifications');
    vi.mocked(Notifications.getPermissionsAsync).mockResolvedValue({ granted: false } as never);
    expect((await notificationState()).kind).toBe('default');
  });

  it('runs the ordinary registration flow when ntfy is ready and permitted', async () => {
    vi.mocked(currentPushTransport).mockResolvedValue('unifiedpush');
    const Notifications = await import('expo-notifications');
    vi.mocked(Notifications.getPermissionsAsync).mockResolvedValue({ granted: true } as never);
    vi.stubEnv('EXPO_PUBLIC_RELAY_URL', 'https://relay.example');
    vi.stubEnv('EXPO_PUBLIC_VAPID_PUBLIC', 'BP8R9RtW5iPVjjmii5jkxGWAs7Q0XJ85DcFnV-tjjcEV_KGPWDC4LyU5ZQPP2XaGYoCOxAdfs4WqDa9HAF0h8gs');
    expect((await notificationState()).kind).toBe('no-subscription');
    vi.unstubAllEnvs();
  });

  it('reports unregistered while the topic set is out of sync', async () => {
    vi.mocked(currentPushTransport).mockResolvedValue('fcm');
    const Notifications = await import('expo-notifications');
    vi.mocked(Notifications.getPermissionsAsync).mockResolvedValue({ granted: true } as never);
    vi.mocked(fcmTopicsSynced).mockResolvedValue(false);
    vi.stubEnv('EXPO_PUBLIC_RELAY_URL', 'https://relay.example');
    const c = company();
    await putCompany(c);
    expect((await notificationState()).kind).toBe('unregistered');
    vi.mocked(fcmTopicsSynced).mockResolvedValue(true);
    expect((await notificationState()).kind).toBe('ok');
    await deleteCompany(origin);
    vi.unstubAllEnvs();
  });
});

describe('notification state machine: iOS local notices', () => {
  it('reports default until the iOS permission is granted', async () => {
    vi.mocked(currentPushTransport).mockResolvedValue('expo');
    const Notifications = await import('expo-notifications');
    vi.mocked(Notifications.getPermissionsAsync).mockResolvedValue({ granted: false } as never);
    expect((await notificationState()).kind).toBe('default');
  });

  it('reports ok once the iOS permission is granted', async () => {
    vi.mocked(currentPushTransport).mockResolvedValue('expo');
    const Notifications = await import('expo-notifications');
    vi.mocked(Notifications.getPermissionsAsync).mockResolvedValue({ granted: true } as never);
    expect((await notificationState()).kind).toBe('ok');
  });

  it('runs the local self-test as one notice', async () => {
    const Notifications = await import('expo-notifications');
    vi.mocked(Notifications.getPermissionsAsync).mockResolvedValue({ granted: true } as never);
    vi.mocked(Notifications.scheduleNotificationAsync).mockResolvedValue('id' as never);
    const result = await runSelfTest([]);
    expect(result.endpoint).toBe('delivered');
    expect(result.leg).toBe('endpoint');
  });
});

describe('notification state machine: in-flight self-test', () => {
  it('reports pending while the test nonce is still awaited, then ok when it arrives', async () => {
    vi.mocked(currentPushTransport).mockResolvedValue('unifiedpush');
    const Notifications = await import('expo-notifications');
    vi.mocked(Notifications.getPermissionsAsync).mockResolvedValue({ granted: true } as never);
    vi.stubEnv('EXPO_PUBLIC_RELAY_URL', 'https://relay.example');
    vi.stubEnv('EXPO_PUBLIC_VAPID_PUBLIC', 'BP8R9RtW5iPVjjmii5jkxGWAs7Q0XJ85DcFnV-tjjcEV_KGPWDC4LyU5ZQPP2XaGYoCOxAdfs4WqDa9HAF0h8gs');
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
  });
});
