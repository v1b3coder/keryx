/**
 * The app-wide notification state machine for iOS/Android (Expo
 * notifications): one native permission and one local-notification channel per
 * install. The relay's remote wake-up legs (WebPush endpoints / FCM topics)
 * need a native module and are a follow-up, so polling is the backstop
 * (design/notifications.md).
 */
import * as Notifications from 'expo-notifications';
import type { CompanyRecord } from './store';
import { currentPushTransport } from './push';

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
  /** iOS/Android: local notices only; polling is the backstop */
  | 'no-transport';

export interface NotificationState {
  kind: NotificationStateKind;
  /** the failing leg when kind === 'failed' */
  leg?: 'endpoint' | 'registration';
  /** the last successful self-test (epoch ms) */
  testedAt?: number;
}

/**
 * The browser helpers exist only in notify.ts (the web target); on native the
 * browser has no PushManager.
 */
export function permissionState(): NotificationPermission | 'unsupported' {
  return 'unsupported';
}

/** The native notification permission (iOS/Android; Expo notifications). */
export async function nativeNotificationGranted(): Promise<boolean> {
  try {
    return (await Notifications.getPermissionsAsync()).granted;
  } catch {
    return false;
  }
}

/** Ask for the native notification permission (iOS/Android). */
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
  if (transport !== 'expo') return { kind: 'unsupported' };
  if (!(await nativeNotificationGranted())) return { kind: 'default' };
  return { kind: 'ok' };
}

export interface SelfTestResult {
  /** 'pending' means the test is accepted; delivery is not confirmed yet */
  endpoint: 'delivered' | 'pending' | 'failed';
  /** the last successful self-test (epoch ms) */
  testedAt?: number;
  /** the failing leg when endpoint === 'failed' */
  leg: 'endpoint' | 'registration';
}

/**
 * Run the native self-test: permission plus one local notice. The relay's
 * remote wake-up legs are a native-module follow-up, so there is no endpoint
 * nonce to wait for; polling remains the backstop.
 */
export async function runSelfTest(_companies: CompanyRecord[]): Promise<SelfTestResult> {
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
