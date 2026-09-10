import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { Capacitor } from '@capacitor/core';
import { Root } from './App';
import './styles.css';

// Android 15+ draws the WebView edge-to-edge (under the status bar) and
// the overlay flag is ignored. Keep the app below the system bars: read the
// status bar height from the StatusBar plugin (dp = CSS px) and expose it as
// --safe-top; iOS keeps using env(safe-area-inset-top).
if (Capacitor.isNativePlatform()) {
  void import('@capacitor/status-bar').then(async ({ StatusBar, Style }) => {
    StatusBar.setOverlaysWebView({ overlay: false }).catch(() => {});
    const dark = window.matchMedia('(prefers-color-scheme: dark)').matches;
    StatusBar.setStyle({ style: dark ? Style.Light : Style.Dark }).catch(() => {});
    if (Capacitor.getPlatform() === 'android') {
      try {
        const info = await StatusBar.getInfo();
        document.documentElement.style.setProperty('--safe-top', `${info.height}px`);
      } catch {
        /* env() fallback */
      }
    }
  });
}

// PWA service worker (registered by vite-plugin-pwa via virtual:pwa-register
// only in the browser build; the app works without it too).
if ('serviceWorker' in navigator) {
  window.addEventListener('load', () => {
    navigator.serviceWorker.register('/sw.js').catch(() => {
      // offline caching is a bonus — the app works without it
    });
  });
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <Root />
  </StrictMode>,
);
