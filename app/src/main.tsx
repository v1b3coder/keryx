import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { Capacitor } from '@capacitor/core';
import { Root } from './App';
import './styles.css';

// System bars. On Android the WebView stays edge-to-edge (the manifest
// opt-out is honored on Android 15 only; Android 16 ignores it), so the
// sticky header reserves the status bar height via env(safe-area-inset-top)
// and is fully opaque — scrolled content never shows behind the bar. Icon
// style follows the theme; the Android plugin maps Style.DARK to white
// icons and Style.LIGHT to black icons (verified against the plugin source).
async function syncSystemBars() {
  const { StatusBar, Style } = await import('@capacitor/status-bar');
  const dark = window.matchMedia('(prefers-color-scheme: dark)').matches;
  await StatusBar.setOverlaysWebView({ overlay: false }).catch(() => {});
  await StatusBar.setStyle({ style: dark ? Style.Dark : Style.Light }).catch(() => {});
  await StatusBar.setBackgroundColor({ color: dark ? '#101013' : '#ffffff' }).catch(() => {});
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
