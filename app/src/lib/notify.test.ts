import { describe, it, expect, vi } from 'vitest';
import 'fake-indexeddb/auto';
import { notificationState } from './notify';

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
