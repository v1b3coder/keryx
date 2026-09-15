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

/**
 * Redirect policy (spec/core.md §1.2): at most one canonical redirect
 * (http→https, www↔apex) is allowed; cross-origin redirects stay blocked.
 * The origin the user confirmed must remain the origin we fetch from.
 */
export function redirectAllowed(from: string, to: string): boolean {
  let a: URL;
  let b: URL;
  try {
    a = new URL(from);
    b = new URL(to);
  } catch {
    return false;
  }
  if (a.origin === b.origin) return true;
  const strip = (host: string) => host.toLowerCase().replace(/^www\./, '');
  if (strip(a.hostname) !== strip(b.hostname)) return false; // cross-origin
  if (b.protocol !== 'https:') return false; // never downgrade to plain HTTP
  if (a.port !== '' && a.port !== b.port) return false;
  return true;
}

/**
 * Wrap a fetch so that a cross-origin redirect is rejected (spec/core.md §1.2).
 * `fetch` follows redirects by default; this checks the final URL's origin.
 */
export function safeFetch(base: typeof fetch = fetch): typeof fetch {
  const wrapped = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const requested = typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url;
    const res = await base(input, init);
    if (res.url && res.url !== requested && !redirectAllowed(requested, res.url)) {
      throw new Error(`blocked cross-origin redirect: ${requested} -> ${res.url}`);
    }
    return res;
  };
  return wrapped as typeof fetch;
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
