/**
 * The app-wide notification state machine and self-test for iOS/Android
 * (relay/SPECIFICATION.md §4.3, §5.3.1; design/notifications.md).
 *
 * Android: the native FCM topic leg / UnifiedPush connector, with one native
 * permission and the native mirror as the notice gate. iOS: Expo local
 * notifications (the relay has no iOS wake-up leg), so permission is the whole
 * state and polling is the backstop. The web/PWA uses notify.ts.
 */

import { Platform } from 'react-native';
import * as Notifications from 'expo-notifications';
import { KeryxPush } from './native-push';
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

/** The browser's permission state (used by the web target). */
export function permissionState(): NotificationPermission | 'unsupported' {
  return 'unsupported';
}

/**
 * The native notification permission (Android POST_NOTIFICATIONS, or iOS).
 * Expo notifications owns the permission on both platforms.
 */
export async function nativeNotificationGranted(): Promise<boolean> {
  try {
    return (await Notifications.getPermissionsAsync()).granted;
  } catch {
    return false;
  }
}

/** Ask for the native notification permission (Android or iOS). */
export async function requestNativeNotificationPermission(): Promise<boolean> {
  try {
    return (await Notifications.requestPermissionsAsync()).granted;
  } catch {
    return false;
  }
}

/** The current app-wide notification state (no network, no prompt). */
export async function notificationState(): Promise<NotificationState> {
  const transport = await currentPushTransport();
  if (transport === 'none') {
    // an Android install can fix this by installing ntfy
    return Platform.OS === 'android' ? { kind: 'no-transport' } : { kind: 'unsupported' };
  }
  if (transport === 'expo') {
    // iOS: local notices only; the relay's wake-up legs need a native module
    if (!(await nativeNotificationGranted())) return { kind: 'default' };
    return { kind: 'ok' };
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
  // unifiedpush (de-Googled Android): the native permission is the source of
  // truth (POST_NOTIFICATIONS)
  if (!(await nativeNotificationGranted())) return { kind: 'default' };
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
 * topic leg, the endpoint leg (UnifiedPush), or the local-notice self-test on
 * iOS.
 */
export async function runSelfTest(companies: CompanyRecord[]): Promise<SelfTestResult> {
  const transport = await currentPushTransport();
  if (transport === 'fcm') return runFcmSelfTest(companies);
  if (transport === 'expo') return runNativeSelfTest();
  return runEndpointSelfTest(companies);
}

/** The iOS self-test: permission plus one local notice. */
async function runNativeSelfTest(): Promise<SelfTestResult> {
  if (!(await nativeNotificationGranted())) return { endpoint: 'failed', leg: 'registration' };
  try {
    await Notifications.scheduleNotificationAsync({
      content: { title: 'Keryx', body: 'Notifications are working.' },
      trigger: null,
    });
    return { endpoint: 'delivered', testedAt: Date.now(), leg: 'endpoint' };
  } catch {
    return { endpoint: 'failed', leg: 'endpoint' };
  }
}

/**
 * Run the endpoint-leg relay self-test (§5.3.1): ensure the registration,
 * ask the relay for a test, then wait up to ~10 s for the worker to record the
 * matching nonce. Never a wake-up. A slow push service is not a failure: on
 * timeout the pending nonce is kept (until its capability expires), so a late
 * delivery still counts and upgrades the state to ok.
 */
async function runEndpointSelfTest(companies: CompanyRecord[]): Promise<SelfTestResult> {
  const base = relayBaseUrl();
  if (!base) return { endpoint: 'failed', leg: 'registration' };
  let relay = await ensureRelayRegistration(companies);
  if (!relay) return { endpoint: 'failed', leg: 'registration' };
  const first = await attemptTest(base, relay);
  if (first !== 'dead') return first;
  // the push service says the endpoint is gone (410): the browser's or
  // distributor's subscription is stale and no wake-up can reach it. A fresh
  // subscription and registration is the only recovery; retry the test once.
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
    // keep the pending nonce: the worker still accepts it until expiresAt, so a
    // slow first delivery is not reported as a failure
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
