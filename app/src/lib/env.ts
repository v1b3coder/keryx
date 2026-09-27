/**
 * Build-time configuration for every target. Expo/Metro inlines literal
 * `process.env.EXPO_PUBLIC_*` reads at bundle time; the Node test runner
 * reads `process.env` at runtime (vitest stubs it). The Vite-era `VITE_*`
 * names are still honored so existing local builds keep working.
 */

declare const process: { env?: Record<string, string | undefined> } | undefined;

function read(name: string): string | undefined {
  const env = typeof process !== 'undefined' ? process?.env : undefined;
  const value = env?.[`EXPO_PUBLIC_${name}`] ?? env?.[`VITE_${name}`];
  return typeof value === 'string' && value ? value : undefined;
}

/** The relay base URL override, or undefined for the built-in default. */
export function relayUrlEnv(): string | undefined {
  return read('RELAY_URL');
}

/** The VAPID public key override, or undefined for the built-in default. */
export function vapidEnv(): string | undefined {
  return read('VAPID_PUBLIC');
}

/** The short git commit injected by CI, or undefined for a local build. */
export function gitCommitEnv(): string | undefined {
  return read('GIT_COMMIT');
}

/** The deploy base path (Expo web `experiments.baseUrl`), default `/`. */
export function baseUrlEnv(): string {
  return read('BASE_URL') ?? '/';
}

/** True for a production bundle, false for development and tests. */
export function isProductionBuild(): boolean {
  const env = typeof process !== 'undefined' ? process?.env : undefined;
  return env?.NODE_ENV === 'production';
}
