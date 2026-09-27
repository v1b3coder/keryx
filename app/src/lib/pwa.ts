/**
 * PWA service-worker registration for the Expo web build. A manual
 * registration needs the update handling the Vite plugin used to do:
 * revalidate sw.js, activate a new worker immediately (skipWaiting in
 * src/sw.ts), and reload the page once so it runs the new precache instead of
 * the old one. Without this an installed PWA can serve a stale bundle for days.
 */
export function registerServiceWorker(): void {
  if (typeof navigator === 'undefined' || !('serviceWorker' in navigator)) return;
  // The service worker sits next to the page, under the deploy base path
  // (`experiments.baseUrl` in app.json).
  const base = new URL(document.baseURI);
  if (!base.pathname.endsWith('/')) base.pathname += '/';
  const swPath = new URL('sw.js', base).pathname;
  const start = () => {
    void (async () => {
      try {
        const registration = await navigator.serviceWorker.register(swPath, {
          updateViaCache: 'none',
        });
        // reload once when a new worker takes control, but never on the first
        // install (there was no controller to replace)
        const hadController = !!navigator.serviceWorker.controller;
        let reloading = false;
        navigator.serviceWorker.addEventListener('controllerchange', () => {
          if (!hadController || reloading) return;
          reloading = true;
          window.location.reload();
        });
        // an installed PWA can stay open for days without a navigation, so
        // check for a new build whenever it returns to the foreground
        document.addEventListener('visibilitychange', () => {
          if (document.visibilityState === 'visible') void registration.update();
        });
      } catch {
        // offline caching is a bonus — the app works without it
      }
    })();
  };
  if (document.readyState === 'complete') start();
  else window.addEventListener('load', start);
}
