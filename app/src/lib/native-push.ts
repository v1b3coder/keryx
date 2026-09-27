/**
 * The native (Android) push module bridge (modules/keryx-push): the real
 * UnifiedPush probe + connector registration, the real Google Play services probe,
 * the FCM topic leg, the native §4 envelope gate and the queue/notice path.
 * `requireOptionalNativeModule` is null on iOS and the web.
 */
import { requireOptionalNativeModule } from 'expo';
import type { SubscriptionSource } from './relay-sw';
import type {
  NativePushSupport,
  KeryxPushNativeModule,
} from '../../modules/keryx-push';

export type {
  NativePushSupport,
  NativeEndpoint,
  NativeRegistration,
  KeryxPushNativeModule,
} from '../../modules/keryx-push';

const KeryxPush = requireOptionalNativeModule<KeryxPushNativeModule>('KeryxPush');

export { KeryxPush };

/** Ask the native side which wake-up transports this device can use. */
export function nativePushSupport(): Promise<NativePushSupport> {
  if (!KeryxPush) return Promise.reject(new Error('no native push module'));
  return KeryxPush.getSupport();
}

/**
 * The UnifiedPush connector as a subscription source (Android only). The page
 * installs it with `setSubscriptionSource`; the service worker never imports it.
 */
export const nativePushSource: SubscriptionSource = {
  subscribe: (vapid) => KeryxPush!.register({ vapid }),
  async current() {
    const { endpoint, p256dh, auth } = await KeryxPush!.getEndpoint();
    if (!endpoint || !p256dh || !auth) return null;
    return { endpoint, p256dh, auth };
  },
  async unsubscribe() {
    await KeryxPush!.unregister();
  },
};
