/**
 * Push transport selection for the web/PWA (design/notifications.md): the
 * browser's PushManager — the endpoint leg (§6.2). The relay keeps one
 * registration holding the union of every followed company's topics.
 */
import { ensureRelayRegistration, topicBindings } from './relay-sw';
import { relayBaseUrl } from './relay';
import { getAllCompanies, type CompanyRecord } from './store';
import { pushTransport, type PushSupport, type PushTransport } from './push-shared';

export type { PushSupport, PushTransport } from './push-shared';
export { pushTransport };

/**
 * The ntfy install page (native-only UI). The web target never shows it — a
 * browser without PushManager is `unsupported`, not `no-transport` — but the
 * export must exist for the platform-neutral type surface.
 */
export const NTFY_INSTALL_URL = 'https://ntfy.sh/#subscribe-phone';

/** Probe the transports available on this target. */
export async function pushSupport(): Promise<PushSupport> {
  const webpush =
    typeof window !== 'undefined' &&
    typeof Notification !== 'undefined' &&
    typeof navigator !== 'undefined' &&
    'serviceWorker' in navigator &&
    'PushManager' in window;
  return { webpush, fcm: false, unifiedPush: { available: false, distributors: [] }, expo: false };
}

/** The transport this install should use right now. */
export async function currentPushTransport(): Promise<PushTransport> {
  return pushTransport(await pushSupport());
}

/** Open a URL in a new browser tab (no auto-open: the user tapped a link). */
export async function openExternal(url: string): Promise<void> {
  window.open(url, '_blank', 'noopener,noreferrer');
}

/**
 * Ensure this install's wake-up transport follows the topic union
 * (design/notifications.md): one relay registration on the web.
 */
export async function ensurePushWakeups(companies: CompanyRecord[]): Promise<void> {
  if ((await currentPushTransport()) !== 'webpush') return;
  await ensureRelayRegistration(companies);
}

/** Whether this install's wake-ups are set up for the given company. */
export async function wakeupsCurrent(company: CompanyRecord): Promise<boolean> {
  if ((await currentPushTransport()) !== 'webpush') return false;
  const base = relayBaseUrl();
  if (!base) return false;
  const relay = await ensureRelayRegistration(await getAllCompanies());
  if (!relay) return false;
  return Object.keys(topicBindings(company)).every((t) => t in relay.topics);
}
