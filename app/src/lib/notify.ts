/**
 * The app-wide notification state machine and self-test
 * (relay/SPECIFICATION.md §4.3, §5.3.1; design/notifications.md).
 *
 * The relay is centralized, so everything here is per install: one browser
 * permission, one push subscription, one relay record holding the union of every
 * followed company's topics. Nothing durable is per company.
 */

import {
  clearPendingTest,
  getAllCompanies,
  getRegistration,
  lastPushAt,
  pendingTest,
  putPendingTest,
  type CompanyRecord,
} from './store';
import { relayBaseUrl, testRegistration, vapidPublicKey } from './relay';
import { ensureRelayRegistration, topicBindings, unionTopics } from './relay-sw';
import type { RelayRegistration } from './relay';

export type NotificationStateKind =
  | 'unsupported'
  | 'default'
  | 'denied'
  | 'no-subscription'
  | 'unregistered'
  | 'pending'
  | 'failed'
  | 'ok';

export interface NotificationState {
  kind: NotificationStateKind;
  /** the failing leg when kind === 'failed' */
  leg?: 'endpoint' | 'registration';
  /** the last successful self-test (epoch ms) */
  testedAt?: number;
}

/** The browser's permission state, or 'unsupported'. */
export function permissionState(): NotificationPermission | 'unsupported' {
  if (typeof Notification === 'undefined' || typeof navigator === 'undefined') return 'unsupported';
  if (!('serviceWorker' in navigator)) return 'unsupported';
  if (typeof window === 'undefined' || !('PushManager' in window)) return 'unsupported';
  if (!window.isSecureContext) return 'unsupported';
  return Notification.permission;
}

/** The current app-wide notification state (no network, no prompt). */
export async function notificationState(): Promise<NotificationState> {
  const permission = permissionState();
  if (permission === 'unsupported') return { kind: 'unsupported' };
  if (permission === 'default') return { kind: 'default' };
  if (permission === 'denied') return { kind: 'denied' };
  const base = relayBaseUrl();
  const vapid = vapidPublicKey();
  if (!base || !vapid) return { kind: 'unsupported' };
  const registration = await getRegistration(base);
  if (!registration) return { kind: 'no-subscription' };
  const topics = Object.keys(unionTopics(await getAllCompanies()));
  if (!topics.every((t) => t in registration.topics)) {
    return { kind: 'unregistered' };
  }
  const pending = await pendingTest(base);
  if (pending?.receivedAt) return { kind: 'ok', testedAt: pending.receivedAt };
  // a test that was accepted but not yet observed is in flight, never a
  // failure: the nonce stays pending until its capability expires
  if (pending && Date.now() <= pending.expiresAt) return { kind: 'pending' };
  const last = await lastPushAtForTopics(registration);
  return last ? { kind: 'ok', testedAt: last } : { kind: 'ok' };
}

/** The most recent accepted wake-up among the registration's topics. */
async function lastPushAtForTopics(registration: RelayRegistration): Promise<number> {
  let last = 0;
  for (const company of await getAllCompanies()) {
    for (const topic of Object.keys(topicBindings(company))) {
      if (!(topic in registration.topics)) continue;
      const at = await lastPushAt(company.origin);
      if (at > last) last = at;
    }
  }
  return last;
}

export interface SelfTestResult {
  /** 'pending' means the relay accepted the test; delivery is not confirmed yet */
  endpoint: 'delivered' | 'pending' | 'failed';
  /** the last successful self-test (epoch ms) */
  testedAt?: number;
  /** the failing leg when endpoint === 'failed' */
  leg: 'endpoint' | 'registration';
}

/**
 * Run the relay self-test (§5.3.1): ensure the registration, ask the relay
 * for a test, then wait up to ~10 s for the service worker to record the
 * matching nonce. Never a wake-up. A slow push service is not a failure: on
 * timeout the pending nonce is kept (until its capability expires), so a late
 * delivery still counts and upgrades the state to ok.
 */
export async function runSelfTest(companies: CompanyRecord[]): Promise<SelfTestResult> {
  const base = relayBaseUrl();
  if (!base) return { endpoint: 'failed', leg: 'registration' };
  const relay = await ensureRelayRegistration(companies);
  if (!relay) return { endpoint: 'failed', leg: 'registration' };
  try {
    const { nonce, expiresAt } = await testRegistration(base, relay.id, relay.managementToken);
    const expires = Date.parse(expiresAt);
    await putPendingTest({ baseUrl: base, nonce, expiresAt: expires });
    const deadline = Date.now() + 10_000;
    while (Date.now() < deadline) {
      const pending = await pendingTest(base);
      if (pending?.receivedAt) {
        return { endpoint: 'delivered', testedAt: pending.receivedAt, leg: 'endpoint' };
      }
      await sleep(500);
    }
    // keep the pending nonce: the service worker still accepts it until
    // expiresAt, so a slow first delivery is not reported as a failure
    return { endpoint: 'pending', leg: 'endpoint' };
  } catch {
    await clearPendingTest(base);
    return { endpoint: 'failed', leg: 'endpoint' };
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
