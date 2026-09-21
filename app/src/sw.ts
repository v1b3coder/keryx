/// <reference lib="webworker" />
/**
 * Custom service worker: workbox precache (offline app shell) plus the relay
 * wake-up handler (relay/SPECIFICATION.md §4.2). The browser decrypts the
 * RFC 8291 payload, so the `push` event data is the plaintext §4 envelope.
 */
import { precacheAndRoute } from 'workbox-precaching';
import { handlePush } from './lib/relay-sw';

declare const self: ServiceWorkerGlobalScope;

precacheAndRoute(self.__WB_MANIFEST);

self.addEventListener('push', (event) => {
  event.waitUntil(
    (async () => {
      const data = event.data ? await event.data.text() : '';
      let outcome;
      try {
        outcome = await handlePush(data);
      } catch {
        return; // a wake-up is best-effort; failures are not user-visible
      }
      if (!outcome.accepted) return;
      if (outcome.test) return; // silent record: the app shows the result
      await self.registration.showNotification(outcome.title ?? 'Keryx', {
        body: outcome.body,
        tag: `keryx-${outcome.origin ?? 'update'}`,
        data: { origin: outcome.origin },
      });
    })(),
  );
});

self.addEventListener('notificationclick', (event) => {
  event.notification.close();
  event.waitUntil(self.clients.openWindow(self.registration.scope));
});
