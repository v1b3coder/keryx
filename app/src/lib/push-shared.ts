/**
 * Push transport selection shared by every target (design/notifications.md):
 * one install has exactly one wake-up transport.
 *
 * - web / PWA: the browser's PushManager — the endpoint leg (§6.2),
 * - iOS/Android (Expo): the app's local notifications; the relay's remote
 *   wake-up legs (WebPush endpoints / FCM topics) need a native module and
 *   are a follow-up, so polling is the backstop for now.
 *
 * The choice is a capability probe, not a user setting.
 */

export type PushTransport = 'webpush' | 'expo' | 'none';

export interface PushSupport {
  /** the browser PushManager (web/PWA) */
  webpush: boolean;
  /** Expo local notifications (iOS/Android) */
  expo: boolean;
}

/** Pick the transport for a probed device. */
export function pushTransport(support: PushSupport): PushTransport {
  if (support.webpush) return 'webpush';
  if (support.expo) return 'expo';
  return 'none';
}
