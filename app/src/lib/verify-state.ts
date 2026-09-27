/**
 * The native verification mirror (design/notifications.md) was the Capacitor
 * Android worker's state: the Expo app has no native wake-up worker yet (the
 * relay's remote wake-up legs are a native-module follow-up), so there is
 * nothing to mirror. The web/PWA verification lives in the service worker.
 */
export async function pushVerifyState(): Promise<void> {
  // no native worker in this build: polling is the backstop
}
