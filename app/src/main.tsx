import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { Capacitor } from '@capacitor/core';
import { Root } from './App';
import './styles.css';

// System bars. The app opts out of Android 15 edge-to-edge in the manifest
// (windowOptOutEdgeToEdgeEnforcement) so the WebView is laid out below the
// status bar; on Android 16+ the opt-out is ignored, so we also read the
// real status bar height (dp = CSS px) and pad the sticky header via
// --safe-top only when the bar actually overlays the WebView. Status bar
// color and icon style follow the light/dark theme in both cases.
async function syncSystemBars() {
  const { StatusBar, Style } = await import('@capacitor/status-bar');
  const dark = window.matchMedia('(prefers-color-scheme: dark)').matches;
  await StatusBar.setOverlaysWebView({ overlay: false }).catch(() => {});
  await StatusBar.setStyle({ style: dark ? Style.Light : Style.Dark }).catch(() => {});
  await StatusBar.setBackgroundColor({ color: dark ? '#101013' : '#ffffff' }).catch(() => {});
  if (Capacitor.getPlatform() === 'android') {
    try {
      const info = await StatusBar.getInfo();
      if (info.overlays) {
        document.documentElement.style.setProperty('--safe-top', `${info.height}px`);
      } else {
        document.documentElement.style.removeProperty('--safe-top');
      }
    } catch {
      /* env() fallback */
    }
  }
}
if (Capacitor.isNativePlatform()) {
  void syncSystemBars();
  window
    .matchMedia('(prefers-color-scheme: dark)')
    .addEventListener('change', () => void syncSystemBars());
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
