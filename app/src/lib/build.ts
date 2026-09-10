/**
 * Build-type signal for the local-dev HTTP exception.
 *
 * The Keryx protocol is HTTPS-only; plain HTTP is a dev/debug convenience
 * (the local demo is served over HTTP on a private network, see
 * spec/core.md §3 and the Android network security config). Release builds
 * — the Android APK and the built PWA — must never accept it.
 *
 * Web: vite dev server (`import.meta.env.DEV`) is a dev build, the built
 * PWA is not. Native: the KeryxEnv plugin exposes the app's debuggable
 * flag. Until the native signal resolves, the safe default (not debug)
 * applies.
 */
import { Capacitor, registerPlugin } from '@capacitor/core';

interface KeryxEnvPlugin {
  isDebug(): Promise<{ debug: boolean }>;
}

const KeryxEnv = registerPlugin<KeryxEnvPlugin>('KeryxEnv');

let debugBuild: boolean | null = null;

/** Test hook (and web fallback initializer). */
export function setDebugBuild(value: boolean | null): void {
  debugBuild = value;
}

export function isDebugBuild(): boolean {
  if (debugBuild !== null) return debugBuild;
  return import.meta.env.DEV;
}

/** Resolve the native debug signal once; no-op on web. */
export function initDebugBuild(): void {
  if (!Capacitor.isNativePlatform() || debugBuild !== null) return;
  void KeryxEnv.isDebug()
    .then(({ debug }) => {
      debugBuild = debug;
    })
    .catch(() => {
      debugBuild = false;
    });
}
