import { describe, it, expect, vi, beforeEach } from 'vitest';
import 'fake-indexeddb/auto';
import type { CompanyRecord } from './store';
import type { TargetsDoc } from './tuf';
import { pushTransport } from './push';
import { ensurePushWakeups, pushSupport, wakeupsCurrent } from './push.native';
import { ensureFcmTopics, fcmTopicsSynced } from './fcm';
import { ensureRelayRegistration } from './relay-sw';

/**
 * The native bridge is optional (`requireOptionalNativeModule` is null on iOS and
 * on a build without the module). The mock is mutable so a test can simulate a
 * missing module, a working probe and a throwing probe without re-importing the
 * module under test.
 */
const bridge: {
  KeryxPush: unknown;
  nativePushSupport: (() => Promise<{ fcm: boolean; unifiedPush: { available: boolean; distributors: string[] } }>) & ReturnType<typeof vi.fn>;
} = {
  KeryxPush: { getSupport: vi.fn() },
  nativePushSupport: vi.fn() as never,
};

vi.mock('react-native', () => ({ Linking: { openURL: vi.fn() }, Platform: { OS: 'android' } }));
vi.mock('./native-push', () => ({
  get KeryxPush() {
    return bridge.KeryxPush;
  },
  nativePushSupport: () => bridge.nativePushSupport(),
}));
vi.mock('./fcm', () => ({
  ensureFcmTopics: vi.fn(),
  fcmTopicsSynced: vi.fn(),
}));
vi.mock('./relay-sw', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./relay-sw')>()),
  ensureRelayRegistration: vi.fn(),
}));
vi.mock('./relay', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./relay')>()),
  relayBaseUrl: () => 'https://relay.example',
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

beforeEach(() => {
  vi.clearAllMocks();
  bridge.KeryxPush = { getSupport: vi.fn() };
});

describe('push transport selection', () => {
  it('uses the browser PushManager on the web/PWA', () => {
    expect(
      pushTransport({ webpush: true, fcm: false, unifiedPush: { available: false, distributors: [] }, expo: false }),
    ).toBe('webpush');
  });

  it('prefers FCM when Google services are present', () => {
    expect(
      pushTransport({ webpush: false, fcm: true, unifiedPush: { available: true, distributors: ['io.heckel.ntfy'] }, expo: false }),
    ).toBe('fcm');
  });

  it('falls back to UnifiedPush (ntfy) without Google services', () => {
    expect(
      pushTransport({ webpush: false, fcm: false, unifiedPush: { available: true, distributors: ['io.heckel.ntfy'] }, expo: false }),
    ).toBe('unifiedpush');
  });

  it('uses Expo local notifications on iOS (no relay leg)', () => {
    expect(
      pushTransport({ webpush: false, fcm: false, unifiedPush: { available: false, distributors: [] }, expo: true }),
    ).toBe('expo');
  });

  it('reports none when neither is available', () => {
    expect(
      pushTransport({ webpush: false, fcm: false, unifiedPush: { available: false, distributors: [] }, expo: false }),
    ).toBe('none');
  });
});

describe('native probe', () => {
  it('reports no transport when the native module is missing', async () => {
    bridge.KeryxPush = null;
    const support = await pushSupport();
    expect(support).toEqual({
      webpush: false,
      fcm: false,
      unifiedPush: { available: false, distributors: [] },
      expo: false,
    });
    // the missing module must not be probed at all
    expect(bridge.nativePushSupport).not.toHaveBeenCalled();
  });

  it('reports no transport when the probe throws', async () => {
    bridge.nativePushSupport.mockRejectedValue(new Error('broken probe'));
    const support = await pushSupport();
    expect(support.expo).toBe(false);
    expect(support.fcm).toBe(false);
  });

  it('passes the probed transports through when the module works', async () => {
    bridge.nativePushSupport.mockResolvedValue({
      fcm: false,
      unifiedPush: { available: true, distributors: ['io.heckel.ntfy'] },
    });
    const support = await pushSupport();
    expect(support.expo).toBe(false);
    expect(support.unifiedPush.available).toBe(true);
  });
});

describe('ensurePushWakeups', () => {
  it('falls back to the UnifiedPush endpoint leg when the FCM subscribe fails', async () => {
    // FCM present and a distributor installed: the failed topic subscribe must
    // still register the endpoint leg, not silently leave the install dead
    bridge.nativePushSupport.mockResolvedValue({
      fcm: true,
      unifiedPush: { available: true, distributors: ['io.heckel.ntfy'] },
    });
    vi.mocked(ensureFcmTopics).mockResolvedValue(undefined);
    vi.mocked(ensureRelayRegistration).mockResolvedValue(undefined as never);

    await ensurePushWakeups([company('http://a.example/x')]);

    expect(ensureRelayRegistration).toHaveBeenCalledTimes(1);
  });

  it('drops the endpoint leg, not the topic union, when FCM succeeded', async () => {
    bridge.nativePushSupport.mockResolvedValue({
      fcm: true,
      unifiedPush: { available: true, distributors: ['io.heckel.ntfy'] },
    });
    vi.mocked(ensureFcmTopics).mockResolvedValue(['topic']);
    vi.mocked(ensureRelayRegistration).mockResolvedValue(undefined as never);

    await ensurePushWakeups([company('http://a.example/x')]);

    expect(vi.mocked(ensureRelayRegistration).mock.calls[0]![0]).toEqual([]);
  });

  it('registers the endpoint leg when only UnifiedPush is available', async () => {
    bridge.nativePushSupport.mockResolvedValue({
      fcm: false,
      unifiedPush: { available: true, distributors: ['io.heckel.ntfy'] },
    });
    vi.mocked(ensureRelayRegistration).mockResolvedValue(undefined as never);

    const companies = [company('http://a.example/x')];
    await ensurePushWakeups(companies);

    expect(vi.mocked(ensureRelayRegistration).mock.calls[0]![0]).toEqual(companies);
  });

  it('does nothing when there is no transport', async () => {
    bridge.nativePushSupport.mockResolvedValue({
      fcm: false,
      unifiedPush: { available: false, distributors: [] },
    });

    await ensurePushWakeups([company('http://a.example/x')]);

    expect(ensureFcmTopics).not.toHaveBeenCalled();
    expect(ensureRelayRegistration).not.toHaveBeenCalled();
  });
});

describe('wakeupsCurrent', () => {
  it('is false on a device with no transport', async () => {
    bridge.nativePushSupport.mockResolvedValue({
      fcm: false,
      unifiedPush: { available: false, distributors: [] },
    });
    expect(await wakeupsCurrent(company('http://a.example/x'))).toBe(false);
  });

  it('reads the FCM topic set when Google services are present', async () => {
    bridge.nativePushSupport.mockResolvedValue({
      fcm: true,
      unifiedPush: { available: false, distributors: [] },
    });
    vi.mocked(fcmTopicsSynced).mockResolvedValue(true);
    expect(await wakeupsCurrent(company('http://a.example/x'))).toBe(true);
    expect(ensureRelayRegistration).not.toHaveBeenCalled();
  });
});
