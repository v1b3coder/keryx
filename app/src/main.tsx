import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { Root } from './App';
import './styles.css';

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
