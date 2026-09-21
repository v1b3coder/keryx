import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { Capacitor } from '@capacitor/core';
import { Root } from './App';
import './styles.css';

// System bars (Android edge-to-edge). The sticky header reserves the status
// bar height via env(safe-area-inset-top) and is fully opaque, so scrolled
// content never shows behind the transparent bar. Icon style follows the
// theme — the Android plugin maps Style.DARK to white icons and Style.LIGHT
// to black icons (verified against the plugin source).
async function syncSystemBars() {
  const { StatusBar, Style } = await import('@capacitor/status-bar');
  const dark = window.matchMedia('(prefers-color-scheme: dark)').matches;
  await StatusBar.setStyle({ style: dark ? Style.Dark : Style.Light }).catch(() => {});
}
if (Capacitor.isNativePlatform()) {
  void syncSystemBars();
  window
    .matchMedia('(prefers-color-scheme: dark)')
    .addEventListener('change', () => void syncSystemBars());
}

// PWA service worker (registered by vite-plugin-pwa via virtual:pwa-register
// only in the browser build; the app works without it too).
//
// A manual registration needs the update handling the generated one would do:
// revalidate sw.js, activate a new worker immediately (skipWaiting in src/sw.ts),
// and reload the page once so it runs the new precache instead of the old one.
// Without this an installed PWA can serve a stale bundle for days.
if ('serviceWorker' in navigator) {
  window.addEventListener('load', () => {
    void (async () => {
      try {
        const registration = await navigator.serviceWorker.register(
          `${import.meta.env.BASE_URL}sw.js`,
          { updateViaCache: 'none' },
        );
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
  });
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <Root />
  </StrictMode>,
);
