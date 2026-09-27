/**
 * The native (Android) wake-up bridge: the UnifiedPush connector registration,
 * the FCM topic leg and the native §4 envelope gate. The web/PWA has no native
 * side (PushManager instead), so `requireOptionalNativeModule` returns null there.
 */
import { requireOptionalNativeModule } from 'expo';

export interface NativePushSupport {
  /** Google services present (the real probe). */
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

export interface KeryxPushNativeModule {
  getSupport(): Promise<NativePushSupport>;
  register(options: { vapid: string }): Promise<NativeEndpoint>;
  unregister(): Promise<void>;
  getEndpoint(): Promise<{ endpoint: string | null; p256dh: string | null; auth: string | null }>;
  setTopics(options: { topics: string[] }): Promise<{ topics: string[] }>;
  getTopics(): Promise<{ topics: string[] }>;
  setVerifyState(options: { state: Record<string, unknown> }): Promise<void>;
  setRegistration(options: { registration: NativeRegistration | null }): Promise<void>;
  showNotification(options: { title: string; body: string; tag?: string }): Promise<void>;
  drainMessages(): Promise<{ messages: string[] }>;
  addListener(
    event: 'push',
    handler: (data: { payload: string }) => void,
  ): { remove(): void };
}

const KeryxPush = requireOptionalNativeModule<KeryxPushNativeModule>('KeryxPush');

export default KeryxPush;

/** Ask the native side which wake-up transports this device can use. */
export function nativePushSupport(): Promise<NativePushSupport> {
  if (!KeryxPush) return Promise.reject(new Error('no native push module'));
  return KeryxPush.getSupport();
}

export { KeryxPush };
