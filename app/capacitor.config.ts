import type { CapacitorConfig } from '@capacitor/cli';

const config: CapacitorConfig = {
  appId: 'cz.v1b3coder.keryx',
  appName: 'Keryx',
  webDir: 'dist',
  android: {
    // WebView-served app; no Play Services required (de-Googled friendly).
    // allowMixedContent: the demo artifact is served over plain HTTP on the
    // local dev network — the WebView page runs at https://localhost, so
    // http:// fetches are otherwise blocked as mixed content. Production
    // traffic is HTTPS-only; this is a dev-only convenience.
    allowMixedContent: true,
  },
  plugins: {
    BarcodeScanner: {
      // MLKit's bundled model: on-device, works without Play Services.
      // (The Capacitor MLKit plugin uses the bundled scanner by default.)
    },
  },
};

export default config;
