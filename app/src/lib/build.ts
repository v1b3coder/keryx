/**
 * Build-type signal for the local-dev HTTP exception.
 *
 * The Keryx protocol is HTTPS-only; plain HTTP is a dev/debug convenience
 * (the local demo is served over HTTP on a private network, see
 * spec/core.md §3). Release builds — the iOS/Android app and the built
 * PWA — must never accept it.
 *
 * Native (Expo): the `__DEV__` global is true in dev builds, false in
 * release builds. Web: the Metro dev server is a dev build, the exported PWA
 * is not. Until the native signal resolves, the safe default applies.
 */
import { isProductionBuild } from './env';

declare const __DEV__: boolean | undefined;

let debugBuild: boolean | null = null;

/** Test hook (and web fallback initializer). */
export function setDebugBuild(value: boolean | null): void {
  debugBuild = value;
}

export function isDebugBuild(): boolean {
  if (debugBuild !== null) return debugBuild;
  if (typeof __DEV__ === 'boolean') return __DEV__;
  return !isProductionBuild();
}

/** Resolve the native debug signal once; no-op on web. */
export function initDebugBuild(): void {
  // Expo's `__DEV__` is already correct on every target; nothing to probe.
}

/**
 * The build's short git commit, injected by CI (see the deploy workflows).
 * A local build without it reports 'dev'. Shown in the app so a running
 * install can be told apart from a stale one.
 */
import { gitCommitEnv } from './env';

export function appVersion(): string {
  const commit = gitCommitEnv();
  return commit ? commit.slice(0, 7) : 'dev';
}
