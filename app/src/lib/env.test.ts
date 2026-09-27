import { describe, it, expect, vi, afterEach } from 'vitest';

/**
 * The build-time overrides only work because every read is a plain
 * `process.env.EXPO_PUBLIC_*` member expression: babel-preset-expo inlines
 * exactly those and silently ignores a computed lookup. The production export is
 * verified separately (`expo export` + a bundle grep); this covers the runtime
 * contract the inliner depends on.
 */
import { baseUrlEnv, gitCommitEnv, isProductionBuild, relayUrlEnv, vapidEnv } from './env';

afterEach(() => {
  vi.unstubAllEnvs();
});

describe('build-time env overrides', () => {
  it('reads the EXPO_PUBLIC relay override', () => {
    vi.stubEnv('EXPO_PUBLIC_RELAY_URL', 'https://relay.example');
    expect(relayUrlEnv()).toBe('https://relay.example');
  });

  it('prefers EXPO_PUBLIC over the Vite-era name', () => {
    vi.stubEnv('EXPO_PUBLIC_RELAY_URL', 'https://new.example');
    vi.stubEnv('VITE_RELAY_URL', 'https://old.example');
    expect(relayUrlEnv()).toBe('https://new.example');
  });

  it('still honors the Vite-era name for local harness builds', () => {
    vi.stubEnv('VITE_RELAY_URL', 'http://127.0.0.1:18099');
    expect(relayUrlEnv()).toBe('http://127.0.0.1:18099');
  });

  it('treats an empty override as unset', () => {
    vi.stubEnv('EXPO_PUBLIC_VAPID_PUBLIC', '');
    expect(vapidEnv()).toBeUndefined();
  });

  it('reports the commit and the base path', () => {
    vi.stubEnv('EXPO_PUBLIC_GIT_COMMIT', 'deadbeef');
    vi.stubEnv('EXPO_PUBLIC_BASE_URL', '/keryx/');
    expect(gitCommitEnv()).toBe('deadbeef');
    expect(baseUrlEnv()).toBe('/keryx/');
  });

  it('defaults the base path to /', () => {
    expect(baseUrlEnv()).toBe('/');
  });

  it('detects a production bundle', () => {
    vi.stubEnv('NODE_ENV', 'production');
    expect(isProductionBuild()).toBe(true);
    vi.stubEnv('NODE_ENV', 'test');
    expect(isProductionBuild()).toBe(false);
  });
});
