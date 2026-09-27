import { describe, it, expect, vi, beforeEach } from 'vitest';
import { notificationState, runSelfTest } from './notify.native';
import * as Notifications from 'expo-notifications';

vi.mock('expo-notifications', () => ({
  getPermissionsAsync: vi.fn(),
  requestPermissionsAsync: vi.fn(),
  scheduleNotificationAsync: vi.fn(),
}));
vi.mock('./push', () => ({
  currentPushTransport: vi.fn(),
}));

import { currentPushTransport } from './push';

beforeEach(() => {
  vi.clearAllMocks();
});

describe('notification state machine (iOS/Android)', () => {
  it('reports default until the native permission is granted', async () => {
    vi.mocked(currentPushTransport).mockResolvedValue('expo');
    vi.mocked(Notifications.getPermissionsAsync).mockResolvedValue({ granted: false } as never);
    expect((await notificationState()).kind).toBe('default');
  });

  it('reports ok once the native permission is granted', async () => {
    vi.mocked(currentPushTransport).mockResolvedValue('expo');
    vi.mocked(Notifications.getPermissionsAsync).mockResolvedValue({ granted: true } as never);
    expect((await notificationState()).kind).toBe('ok');
  });

  it('reports unsupported when the native transport is unavailable', async () => {
    vi.mocked(currentPushTransport).mockResolvedValue('none');
    expect((await notificationState()).kind).toBe('unsupported');
  });

  it('runs the native self-test as one local notice', async () => {
    vi.mocked(Notifications.getPermissionsAsync).mockResolvedValue({ granted: true } as never);
    vi.mocked(Notifications.scheduleNotificationAsync).mockResolvedValue('id' as never);
    const result = await runSelfTest([]);
    expect(result.endpoint).toBe('delivered');
    expect(result.leg).toBe('endpoint');
  });

  it('fails the native self-test without the permission', async () => {
    vi.mocked(Notifications.getPermissionsAsync).mockResolvedValue({ granted: false } as never);
    const result = await runSelfTest([]);
    expect(result.endpoint).toBe('failed');
    expect(result.leg).toBe('registration');
  });
});
