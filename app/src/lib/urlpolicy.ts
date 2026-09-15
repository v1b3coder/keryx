/**
 * Transport policy for linked media (spec/feeds.md §1.1, spec/repository.md §2):
 * absolute HTTPS URLs only. The local demo serves plain HTTP on a private
 * address; that is a documented dev-only exception in debug builds, never in
 * production.
 */

import { isDebugBuild } from './build';

/** Loopback or RFC 1918 private address (the local demo exception). */
export function isLocalDevOrigin(origin: string): boolean {
  try {
    const u = new URL(origin);
    const host = u.hostname;
    if (host === 'localhost' || host === '127.0.0.1' || host === '::1') return true;
    const m = host.match(/^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/);
    if (!m) return false;
    const [a, b] = [Number(m[1]), Number(m[2])];
    if (a === 10) return true;
    if (a === 172 && b >= 16 && b <= 31) return true;
    if (a === 192 && b === 168) return true;
    return false;
  } catch {
    return false;
  }
}

/** True when a linked resource URL is allowed: HTTPS, or local-dev HTTP in debug builds. */
export function linkedUrlAllowed(url: string): boolean {
  try {
    const u = new URL(url);
    if (u.protocol === 'https:') return true;
    if (u.protocol === 'http:' && isLocalDevOrigin(u.origin) && isDebugBuild()) return true;
    return false;
  } catch {
    return false;
  }
}
