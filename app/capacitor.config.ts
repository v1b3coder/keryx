import type { CapacitorConfig } from '@capacitor/cli';

const config: CapacitorConfig = {
  appId: 'cz.v1b3coder.keryx',
  appName: 'Keryx',
  webDir: 'dist',
  plugins: {
    BarcodeScanner: {
      // MLKit's bundled model: on-device, works without Play Services.
      // (The Capacitor MLKit plugin uses the bundled scanner by default.)
    },
  },
};

export default config;
