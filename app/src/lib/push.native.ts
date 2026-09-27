/**
 * Push transport selection for iOS/Android (design/notifications.md).
 *
 * Android probes the native module: FCM > UnifiedPush > Expo local. iOS has no
 * relay wake-up leg (the relay carries FCM topics and WebPush endpoints only),
 * so it uses Expo local notifications and polling is the backstop.
 */
import { Linking, Platform } from 'react-native';
import { nativePushSupport, KeryxPush, type KeryxPushNativeModule } from './native-push';
import { ensureFcmTopics, fcmTopicsSynced } from './fcm';
import { ensureRelayRegistration, topicBindings, type SubscriptionSource } from './relay-sw';
import { relayBaseUrl } from './relay';
import { getAllCompanies, type CompanyRecord } from './store';
import { pushTransport, type PushSupport, type PushTransport } from './push-shared';

export type { PushSupport, PushTransport } from './push-shared';
export { pushTransport };

/**
 * The ntfy install page: F-Droid and Google Play builds, plus direct APKs. A
 * de-Googled phone has no other way to receive wake-ups.
 */
export const NTFY_INSTALL_URL = 'https://ntfy.sh/#subscribe-phone';

/** Probe the transports available on this target. */
export async function pushSupport(): Promise<PushSupport> {
  if (Platform.OS !== 'android') {
    return { webpush: false, fcm: false, unifiedPush: { available: false, distributors: [] }, expo: true };
  }
  if (!KeryxPush) {
    // a build without the native module (or Expo Go) has no relay leg: the
    // Android no-transport state must stay visible, not be masked as working
    return { webpush: false, fcm: false, unifiedPush: { available: false, distributors: [] }, expo: false };
  }
  try {
    const native = await nativePushSupport();
    return { webpush: false, fcm: native.fcm, unifiedPush: native.unifiedPush, expo: false };
  } catch {
    // a broken probe must not claim wake-ups work; polling remains the backstop
    return { webpush: false, fcm: false, unifiedPush: { available: false, distributors: [] }, expo: false };
  }
}

/** The transport this install should use right now. */
export async function currentPushTransport(): Promise<PushTransport> {
  return pushTransport(await pushSupport());
}

/** Open a URL in the system browser. */
export async function openExternal(url: string): Promise<void> {
  await Linking.openURL(url);
}

/** The native bridge, or null on iOS and the web. */
function native(): KeryxPushNativeModule | null {
  return KeryxPush;
}

/**
 * Ensure this install's wake-up transport follows the topic union
 * (design/notifications.md). FCM subscribes the native SDK; the endpoint leg
 * keeps one relay registration. An FCM failure falls back to UnifiedPush
 * before giving up. Expo-only installs have no relay leg.
 */
export async function ensurePushWakeups(companies: CompanyRecord[]): Promise<void> {
  const support = await pushSupport();
  if (support.fcm) {
    if ((await ensureFcmTopics(companies)) !== undefined) {
      await dropEndpointRegistration();
      return;
    }
    // the FCM topic subscribe failed: fall back to UnifiedPush
  }
  if (support.unifiedPush.available) {
    await ensureRelayRegistration(companies);
  }
}

/** Whether this install's wake-ups are set up for the given company. */
export async function wakeupsCurrent(company: CompanyRecord): Promise<boolean> {
  const support = await pushSupport();
  if (support.fcm) return fcmTopicsSynced(await getAllCompanies());
  // the native build can show local notices; polling remains the backstop
  if (pushTransport(support) === 'expo') return true;
  if (!support.unifiedPush.available) return false;
  const base = relayBaseUrl();
  if (!base) return false;
  const relay = await ensureRelayRegistration(await getAllCompanies());
  if (!relay) return false;
  return Object.keys(topicBindings(company)).every((t) => t in relay.topics);
}

/**
 * Drop a leftover endpoint-leg registration when this install uses FCM: one
 * install has exactly one wake-up transport (design/notifications.md), and a
 * stale registration would make the relay fan out to both.
 */
async function dropEndpointRegistration(): Promise<void> {
  const module = native();
  if (!module) return;
  await ensureRelayRegistration([]);
  try {
    await module.setRegistration({ registration: null });
    await module.unregister();
  } catch {
    // the connector may not be registered at all
  }
}
