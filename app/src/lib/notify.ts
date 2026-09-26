/**
 * The app-wide notification state machine and self-test
 * (relay/SPECIFICATION.md §4.3, §5.3.1; design/notifications.md).
 *
 * The relay is centralized, so everything here is per install: one browser
 * permission, one push subscription, one relay record holding the union of every
 * followed company's topics. Nothing durable is per company.
 */

import { Capacitor } from '@capacitor/core';
import {
  clearPendingTest,
  getAllCompanies,
  getRegistration,
  lastPushAt,
  pendingTest,
  putPendingTest,
  type CompanyRecord,
} from './store';
import { relayBaseUrl, testRegistration, vapidPublicKey, RelayGone } from './relay';
import { currentPushTransport } from './push';
import { KeryxPush } from './native-push';
import { ensureRelayRegistration, topicBindings, unionTopics } from './relay-sw';
import { fcmTopicsSynced, runFcmSelfTest } from './fcm';
import type { RelayRegistration } from './relay';

export type NotificationStateKind =
  | 'checking'
  | 'unsupported'
  | 'default'
  | 'denied'
  | 'no-subscription'
  | 'unregistered'
  | 'pending'
  | 'failed'
  | 'ok'
  /** Android: no FCM and no UnifiedPush distributor — install ntfy */
  | 'no-transport';

export interface NotificationState {
  kind: NotificationStateKind;
  /** the failing leg when kind === 'failed' */
  leg?: 'endpoint' | 'registration' | 'topic';
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

/** The native notification permission (Android; POST_NOTIFICATIONS). */
export async function nativeNotificationGranted(): Promise<boolean> {
  try {
    return (await KeryxPush.getNotificationPermission()).granted;
  } catch {
    return false;
  }
}

/** Ask for the native notification permission (Android; POST_NOTIFICATIONS). */
export async function requestNativeNotificationPermission(): Promise<boolean> {
  try {
    return (await KeryxPush.requestNotificationPermission()).granted;
  } catch {
    return false;
  }
}

/** The current app-wide notification state (no network, no prompt). */
export async function notificationState(): Promise<NotificationState> {
  const transport = await currentPushTransport();
  if (transport === 'none') {
    // an Android install can fix this by installing ntfy; a browser without
    // PushManager cannot (polling is its backstop)
    return Capacitor.getPlatform() === 'android'
      ? { kind: 'no-transport' }
      : { kind: 'unsupported' };
  }
  if (transport === 'fcm') {
    // The topic leg has no relay registration: the native permission is the
    // source of truth and the native topic set is the local registration.
    if (!(await nativeNotificationGranted())) return { kind: 'default' };
    // An in-flight test is checked before the topic set: the subscribed test
    // topic is expected until its capability expires, and a late nonce still
    // upgrades the state to green instead of being masked by a stale topic set.
    const base = relayBaseUrl();
    const pending = base ? await pendingTest(base) : undefined;
    if (pending?.receivedAt) return { kind: 'ok', testedAt: pending.receivedAt };
    if (pending && Date.now() <= pending.expiresAt) return { kind: 'pending' };
    const companies = await getAllCompanies();
    if (!(await fcmTopicsSynced(companies))) return { kind: 'unregistered' };
    return { kind: 'ok' };
  }
  if (transport === 'unifiedpush') {
    // Android: the native permission is the source of truth (POST_NOTIFICATIONS);
    // the WebView's Notification API is not usable
    if (!(await nativeNotificationGranted())) return { kind: 'default' };
  } else {
    const permission = permissionState();
    if (permission === 'unsupported') return { kind: 'unsupported' };
    if (permission === 'default') return { kind: 'default' };
    if (permission === 'denied') return { kind: 'denied' };
  }
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
  leg: 'endpoint' | 'registration' | 'topic';
}

/**
 * Run the relay self-test (§5.3.1) on this install's transport: the FCM
 * topic leg or the endpoint leg.
 */
export async function runSelfTest(companies: CompanyRecord[]): Promise<SelfTestResult> {
  if ((await currentPushTransport()) === 'fcm') return runFcmSelfTest(companies);
  return runEndpointSelfTest(companies);
}

/**
 * Run the endpoint-leg relay self-test (§5.3.1): ensure the registration,
 * ask the relay for a test, then wait up to ~10 s for the service worker to
 * record the matching nonce. Never a wake-up. A slow push service is not a
 * failure: on timeout the pending nonce is kept (until its capability
 * expires), so a late delivery still counts and upgrades the state to ok.
 */
async function runEndpointSelfTest(companies: CompanyRecord[]): Promise<SelfTestResult> {
  const base = relayBaseUrl();
  if (!base) return { endpoint: 'failed', leg: 'registration' };
  let relay = await ensureRelayRegistration(companies);
  if (!relay) return { endpoint: 'failed', leg: 'registration' };
  const first = await attemptTest(base, relay);
  if (first !== 'dead') return first;
  // the push service says the endpoint is gone (410): the browser's
  // subscription is stale and no wake-up can reach it. A fresh subscription
  // and registration is the only recovery; retry the test once on it.
  relay = await ensureRelayRegistration(companies, true);
  if (relay) {
    const second = await attemptTest(base, relay);
    if (second !== 'dead') return second;
  }
  await clearPendingTest(base);
  return { endpoint: 'failed', leg: 'endpoint' };
}

/** One self-test attempt; 'dead' means the endpoint is gone at the push service. */
async function attemptTest(base: string, relay: RelayRegistration): Promise<SelfTestResult | 'dead'> {
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
  } catch (err) {
    // the relay says the registration is gone: a fresh subscription and
    // registration are the only recovery (§5.3)
    if (err instanceof RelayGone) return 'dead';
    await clearPendingTest(base);
    return { endpoint: 'failed', leg: 'endpoint' };
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
