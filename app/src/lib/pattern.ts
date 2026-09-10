/**
 * Pattern matching for two glob dialects in the protocol:
 *
 * - TUF delegation path globs (spec/repository.md §2): `*` matches exactly
 *   one path segment and never crosses `/`. `channels/marketing/*` matches
 *   `channels/marketing/feed.json` but not `channels/marketing/x/feed.json`.
 * - Private-feed capability URL patterns (spec/feeds.md §3): origin-exact
 *   (scheme + host + port), wildcards only in the path at segment
 *   boundaries; never in host or query.
 */

/**
 * TUF path glob: `*` = one path segment, never crosses `/`.
 * `pattern` and `path` are absolute paths starting with `/` (TUF target
 * paths are relative, e.g. `channels/marketing/*` — we accept either form).
 */
export function pathPatternMatches(pattern: string, path: string): boolean {
  const p = pattern.replace(/^\//, '');
  const t = path.replace(/^\//, '');
  const ps = p.split('/');
  const ts = t.split('/');
  if (ps.length !== ts.length) return false;
  for (let i = 0; i < ps.length; i++) {
    if (ps[i] === '*') continue;
    if (ps[i] !== ts[i]) return false;
  }
  return true;
}

function pathGlob(pattern: string, path: string): boolean {
  const ps = pattern.split('/');
  const ts = path.split('/');
  if (ps.length !== ts.length) return false;
  for (let i = 0; i < ps.length; i++) {
    if (ps[i] === '*') continue;
    if (ps[i] !== ts[i]) return false;
  }
  return true;
}

/**
 * Private-feed capability URL pattern (spec/feeds.md §3): origin must match
 * exactly (scheme + host + port); `*` allowed only as a whole path segment;
 * never in host or query. The pattern's query, if any, must match exactly;
 * a pattern without query requires a queryless candidate.
 */
export function patternMatches(pattern: string, url: string): boolean {
  let pat: URL;
  let cand: URL;
  try {
    pat = new URL(pattern);
    cand = new URL(url);
  } catch {
    return false;
  }
  if (pat.origin !== cand.origin) return false;
  if (pat.protocol !== 'https:' && pat.protocol !== 'http:') return false;
  const patPath = pat.pathname.replace(/^\/+/, '');
  const candPath = cand.pathname.replace(/^\/+/, '');
  if (!pathGlob(patPath, candPath)) return false;
  // query: exact match if present in the pattern, otherwise must be absent
  if (pat.search) {
    if (cand.search !== pat.search) return false;
  } else if (cand.search) {
    return false;
  }
  return true;
}
