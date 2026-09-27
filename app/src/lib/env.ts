/**
 * Build-time configuration for every target.
 *
 * Every read MUST be a plain `process.env.EXPO_PUBLIC_<NAME>` member expression:
 * babel-preset-expo only inlines those at bundle time (and Metro's dev serializer
 * only injects those). A computed lookup (`process.env[name]`) or an optional
 * chain (`process?.env.X`) is invisible to the transform, so the override would
 * silently be dropped from every bundled build. Metro's runtime prelude always
 * defines `process.env` on web, native and in the vitest process, so the plain
 * reads are safe.
 */

function present(value: string | undefined): string | undefined {
  return typeof value === 'string' && value ? value : undefined;
}

/**
 * The Vite-era `VITE_*` names are still honored (read at runtime, never
 * inlined) so local harness builds keep working; `EXPO_PUBLIC_*` wins.
 */
function override(expo: string | undefined, vite: string | undefined): string | undefined {
  return present(expo) ?? present(vite);
}

/** The relay base URL override, or undefined for the built-in default. */
export function relayUrlEnv(): string | undefined {
  return override(process.env.EXPO_PUBLIC_RELAY_URL, process.env.VITE_RELAY_URL);
}

/** The VAPID public key override, or undefined for the built-in default. */
export function vapidEnv(): string | undefined {
  return override(process.env.EXPO_PUBLIC_VAPID_PUBLIC, process.env.VITE_VAPID_PUBLIC);
}

/** The short git commit injected by CI, or undefined for a local build. */
export function gitCommitEnv(): string | undefined {
  return override(process.env.EXPO_PUBLIC_GIT_COMMIT, process.env.VITE_GIT_COMMIT);
}

/** The deploy base path (Expo web `experiments.baseUrl`), default `/`. */
export function baseUrlEnv(): string {
  return present(process.env.EXPO_PUBLIC_BASE_URL) ?? '/';
}

/** True for a production bundle, false for development and tests. */
export function isProductionBuild(): boolean {
  return process.env.NODE_ENV === 'production';
}
