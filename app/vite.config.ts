/// <reference types="vitest/config" />
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import { VitePWA } from 'vite-plugin-pwa';

export default defineConfig({
  // GitHub Pages project site: https://v1b3coder.github.io/keryx/
  // vite-plugin-pwa derives manifest start_url/scope and the SW path from
  // this base automatically.
  base: '/keryx/',
  plugins: [
    react(),
    VitePWA({
      registerType: 'autoUpdate',
      manifest: {
        name: 'Keryx',
        short_name: 'Keryx',
        description: 'Verified announcements from companies. No email, no accounts, no phishing.',
        theme_color: '#ffffff',
        background_color: '#ffffff',
        display: 'standalone',
        // Relative to the manifest URL, so it stays correct under any base
        // (the plugin only derives this from base when it is not set).
        start_url: './',
        icons: [
          { src: 'icons/icon-192.png', sizes: '192x192', type: 'image/png' },
          { src: 'icons/icon-512.png', sizes: '512x512', type: 'image/png' },
          {
            src: 'icons/icon-maskable-512.png',
            sizes: '512x512',
            type: 'image/png',
            purpose: 'maskable',
          },
        ],
      },
      workbox: {
        globPatterns: ['**/*.{js,css,html,svg,png,ico,woff2}'],
        navigateFallback: 'index.html',
        maximumFileSizeToCacheInBytes: 4 * 1024 * 1024,
      },
    }),
  ],
  test: {
    environment: 'node',
    include: ['src/**/*.test.ts'],
  },
});
