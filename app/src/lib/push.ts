/**
 * Push transport selection (design/notifications.md, relay/SPECIFICATION.md §6).
 *
 * One install has exactly one wake-up transport:
 * - web / PWA: the browser's PushManager — the endpoint leg (§6.2),
 * - Android with Google services: FCM topics — the topic leg (§6.1),
 * - de-Googled Android: a UnifiedPush distributor (ntfy today) — the same
 *   endpoint leg as the browser, over the distributor's connection (§6.3),
 * - Android without either: no wake-ups; the user is asked to install ntfy.
 *
 * The choice is a capability probe, not a user setting: FCM is used only when the
 * device can actually use it, and the user never has to know which leg is active.
 * The relay needs no change for ntfy — its endpoint leg already delivers to any
 * WebPush endpoint, and ntfy.sh is an approved push origin by default (§5.6).
 */
import { Capacitor } from '@capacitor/core';
import { Browser } from '@capacitor/browser';
import { nativePushSupport, KeryxPush, type NativePushSupport } from './native-push';
import { ensureFcmTopics, fcmTopicsSynced } from './fcm';
import { ensureRelayRegistration, topicBindings, type SubscriptionSource } from './relay-sw';
import { relayBaseUrl } from './relay';
import { getAllCompanies, type CompanyRecord } from './store';

export type PushTransport = 'webpush' | 'fcm' | 'unifiedpush' | 'none';

export type PushSupport = NativePushSupport;

/** The ntfy install page: F-Droid and Google Play builds, plus direct APKs. */
export const NTFY_INSTALL_URL = 'https://ntfy.sh/#subscribe-phone';

/** The ntfy package id, used in the install guidance. */
export const NTFY_PACKAGE = 'io.heckel.ntfy';

/** Probe the native transports; the web has none of its own (PushManager instead). */
export async function pushSupport(): Promise<PushSupport> {
  if (Capacitor.getPlatform() !== 'android') {
    return { fcm: false, unifiedPush: { available: false, distributors: [] } };
  }
  try {
    return await nativePushSupport();
  } catch {
    // a broken probe must not leave the app without a state; polling remains
    return { fcm: false, unifiedPush: { available: false, distributors: [] } };
  }
}

/** Pick the transport for a probed device. */
export function pushTransport(support: PushSupport): PushTransport {
  if (support.fcm) return 'fcm';
  if (support.unifiedPush.available) return 'unifiedpush';
  return 'none';
}

/** The transport this install should use right now. */
export async function currentPushTransport(): Promise<PushTransport> {
  if (Capacitor.getPlatform() === 'android') return pushTransport(await pushSupport());
  return typeof window !== 'undefined' && 'PushManager' in window ? 'webpush' : 'none';
}

/** Open the ntfy install page in the system browser. */
export async function openNtfyInstallPage(): Promise<void> {
  if (Capacitor.isNativePlatform()) await Browser.open({ url: NTFY_INSTALL_URL });
  else window.open(NTFY_INSTALL_URL, '_blank', 'noopener,noreferrer');
}

/**
 * Ensure this install's wake-up transport follows the topic union
 * (design/notifications.md). FCM subscribes the native SDK; the endpoint leg
 * keeps one relay registration. An FCM failure falls back to UnifiedPush
 * before giving up.
 */
export async function ensurePushWakeups(companies: CompanyRecord[]): Promise<void> {
  if ((await pushSupport()).fcm) {
    if ((await ensureFcmTopics(companies)) !== undefined) {
      await dropEndpointRegistration();
      return;
    }
    // the FCM topic subscribe failed: fall back to UnifiedPush
  }
  await ensureRelayRegistration(companies);
}

/** Whether this install's wake-ups are set up for the given company. */
export async function wakeupsCurrent(company: CompanyRecord): Promise<boolean> {
  if ((await pushSupport()).fcm) return fcmTopicsSynced(await getAllCompanies());
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
  await ensureRelayRegistration([]);
  try {
    await KeryxPush.setRegistration({ registration: null });
    await KeryxPush.unregister();
  } catch {
    // the connector may not be registered at all
  }
}

/**
 * The UnifiedPush connector as a subscription source (Android only). The page
 * installs it with `setSubscriptionSource`; the service worker never imports it.
 */
export const nativePushSource: SubscriptionSource = {
  subscribe: (vapid) => KeryxPush.register({ vapid }),
  async current() {
    const { endpoint, p256dh, auth } = await KeryxPush.getEndpoint();
    if (!endpoint || !p256dh || !auth) return null;
    return { endpoint, p256dh, auth };
  },
  async unsubscribe() {
    await KeryxPush.unregister();
  },
};
