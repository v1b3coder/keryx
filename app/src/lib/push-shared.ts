/**
 * Push transport selection shared by every target (design/notifications.md):
 * one install has exactly one wake-up transport.
 *
 * - web / PWA: the browser's PushManager — the endpoint leg (§6.2),
 * - Android with Google services: FCM topics — the topic leg (§6.1),
 * - de-Googled Android: a UnifiedPush distributor (ntfy today) — the same
 *   endpoint leg as the browser, over the distributor's connection (§6.3),
 * - iOS (and Android without either): Expo local notifications only; polling
 *   is the backstop.
 *
 * The choice is a capability probe, not a user setting.
 */

export type PushTransport = 'webpush' | 'fcm' | 'unifiedpush' | 'expo' | 'none';

export interface PushSupport {
  /** the browser PushManager (web/PWA) */
  webpush: boolean;
  /** Google services present (the FCM topic leg) */
  fcm: boolean;
  /** installed UnifiedPush distributors (ntfy today) */
  unifiedPush: { available: boolean; distributors: string[] };
  /** Expo local notifications (iOS, or Android without a native leg) */
  expo: boolean;
}

/** Pick the transport for a probed device. */
export function pushTransport(support: PushSupport): PushTransport {
  if (support.webpush) return 'webpush';
  if (support.fcm) return 'fcm';
  if (support.unifiedPush.available) return 'unifiedpush';
  if (support.expo) return 'expo';
  return 'none';
}
