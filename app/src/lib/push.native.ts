/**
 * Push transport selection for iOS/Android (design/notifications.md): the Expo
 * app can show local notifications; the relay's remote wake-up legs (WebPush
 * endpoints / FCM topics) need a native module and are a follow-up, so polling
 * is the backstop for now. The web target is the endpoint leg (push.ts).
 */
import { Linking } from 'react-native';
import { nativeNotificationGranted } from './notify';
import { pushTransport, type PushSupport, type PushTransport } from './push-shared';
import type { CompanyRecord } from './store';

export type { PushSupport, PushTransport } from './push-shared';
export { pushTransport };

/** Probe the transports available on this target. */
export async function pushSupport(): Promise<PushSupport> {
  return { webpush: false, expo: true };
}

/** The transport this install should use right now. */
export async function currentPushTransport(): Promise<PushTransport> {
  return pushTransport(await pushSupport());
}

/** Open a URL in the system browser. */
export async function openExternal(url: string): Promise<void> {
  await Linking.openURL(url);
}

/**
 * Ensure this install's wake-up transport follows the topic union. The Expo
 * build has no relay leg yet, so there is nothing to register; polling is the
 * backstop (design/notifications.md).
 */
export async function ensurePushWakeups(_companies: CompanyRecord[]): Promise<void> {
  // no relay wake-up leg in this build
}

/** Whether this install's wake-ups are set up for the given company. */
export async function wakeupsCurrent(_company: CompanyRecord): Promise<boolean> {
  // the native build shows local notices while it runs; polling remains the backstop
  return nativeNotificationGranted();
}
