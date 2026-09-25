/**
 * The native (Android) push plugin. The UnifiedPush probe is real: it lists the
 * installed distributors exactly like the UnifiedPush connector library does
 * (broadcast receivers handling org.unifiedpush.android.distributor.REGISTER).
 *
 * MOCK (FCM phase): `fcm` is always false until the FCM phase lands, so the
 * transport decision below always takes the UnifiedPush (ntfy) branch.
 * TODO(fcm): report Google Play services availability (com.google.android.gms).
 */
import { registerPlugin } from '@capacitor/core';

export interface NativePushSupport {
  /** Google services present (MOCK: always false until the FCM phase). */
  fcm: boolean;
  /** Installed UnifiedPush distributors (ntfy today). */
  unifiedPush: { available: boolean; distributors: string[] };
}

interface KeryxPushPlugin {
  getSupport(): Promise<NativePushSupport>;
}

const KeryxPush = registerPlugin<KeryxPushPlugin>('KeryxPush');

/** Ask the native side which wake-up transports this device can use. */
export function nativePushSupport(): Promise<NativePushSupport> {
  return KeryxPush.getSupport();
}
