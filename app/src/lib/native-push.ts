/**
 * The native (Android) push plugin bridge. The UnifiedPush probe and the
 * connector registration are real; FCM is mocked to false until the FCM phase
 * (see KeryxPushPlugin.java).
 */
import { registerPlugin, type PluginListenerHandle } from '@capacitor/core';

export interface NativePushSupport {
  /** Google services present (MOCK: always false until the FCM phase). */
  fcm: boolean;
  /** Installed UnifiedPush distributors (ntfy today). */
  unifiedPush: { available: boolean; distributors: string[] };
}

export interface NativeEndpoint {
  endpoint: string;
  p256dh: string;
  auth: string;
}

export interface NativeRegistration {
  baseUrl: string;
  id: string;
  managementToken: string;
}

export interface KeryxPushPlugin {
  getSupport(): Promise<NativePushSupport>;
  register(options: { vapid: string }): Promise<NativeEndpoint>;
  unregister(): Promise<void>;
  getEndpoint(): Promise<{ endpoint: string | null; p256dh: string | null; auth: string | null }>;
  setVerifyState(options: { state: { topics: unknown } }): Promise<void>;
  setRegistration(options: { registration: NativeRegistration | null }): Promise<void>;
  showNotification(options: { title: string; body: string }): Promise<void>;
  requestNotificationPermission(): Promise<{ granted: boolean }>;
  getNotificationPermission(): Promise<{ granted: boolean }>;
  drainMessages(): Promise<{ messages: string[] }>;
  addListener(
    event: 'push',
    handler: (data: { payload: string }) => void,
  ): Promise<PluginListenerHandle>;
}

const KeryxPush = registerPlugin<KeryxPushPlugin>('KeryxPush');

/** Ask the native side which wake-up transports this device can use. */
export function nativePushSupport(): Promise<NativePushSupport> {
  return KeryxPush.getSupport();
}

/** The native connector bridge; the page installs the subscription source. */
export { KeryxPush };
