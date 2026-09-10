/**
 * Join URL / QR payload (spec/core.md §3).
 *
 * The QR encodes a standard HTTPS URL `https://<origin>/join?p=<payload>`
 * (payload optional — a payload-less join pairs with the origin's public
 * channels only). The payload is base64url (no padding) JSON
 * `{v, channels, private_feeds}` — there is NO metadata URL in it: the root
 * anchor is derived from the join origin
 * (`<origin>/.well-known/keryx/root.json`). The payload is not signed; trust
 * comes from (a) the user confirming the origin and (b) private-feed pattern
 * authorization after pairing.
 */

import { base64urlToText } from './bytes';

export interface JoinPayload {
  v: number;
  /** suggested public channels (preselects only — the user taps to subscribe) */
  channels: string[];
  /** private capability feed URLs (auto-subscribe, order-scoped) */
  privateFeeds: string[];
}

/** Version we implement. An app that does not implement the payload's v MUST NOT partial-parse. */
export const PAYLOAD_VERSION = 1;

export class PayloadError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'PayloadError';
  }
}

export interface ParsedJoin {
  /** the join URL's origin (the one confirmed origin; trust anchor host) */
  origin: string;
  payload: JoinPayload;
  joinUrl: string;
}

const CHANNEL_RE = /^[a-z0-9-_]+$/;

/**
 * Parse a join URL (or a bare join URL origin string) into the confirmed
 * origin and the payload. Throws PayloadError on malformed input; throws
 * PayloadError with `needsNewerApp` semantics when the payload version is
 * unknown (MUST NOT partial-parse).
 */
export function parseJoinUrl(input: string): ParsedJoin {
  let url: URL;
  try {
    url = new URL(input.trim());
  } catch {
    throw new PayloadError('This does not look like a join link.');
  }
  if (url.protocol !== 'https:' && url.protocol !== 'http:') {
    throw new PayloadError('This link is not a web address.');
  }
  const p = url.searchParams.get('p');
  if (p === null) {
    // Payload-less join (§3): only unambiguous origin-level URLs — the bare
    // origin or the join path. The app pairs with the confirmed origin and
    // shows the publisher's public channels; there are no preselects and
    // never any private feeds (capability URLs travel only in ?p=).
    const path = url.pathname.replace(/\/+$/, '') || '/';
    if (path !== '/' && path !== '/join') {
      throw new PayloadError('This link is missing its join information.');
    }
    return {
      origin: url.origin,
      payload: { v: PAYLOAD_VERSION, channels: [], privateFeeds: [] },
      joinUrl: url.toString(),
    };
  }
  if (p === '') {
    throw new PayloadError('This join link is damaged. Ask the company for a new one.');
  }
  let raw: unknown;
  try {
    raw = JSON.parse(base64urlToText(p));
  } catch {
    throw new PayloadError('This join link is damaged. Ask the company for a new one.');
  }
  if (typeof raw !== 'object' || raw === null || Array.isArray(raw)) {
    throw new PayloadError('This join link is damaged.');
  }
  const obj = raw as Record<string, unknown>;
  const v = obj.v;
  if (typeof v !== 'number' || !Number.isInteger(v)) {
    throw new PayloadError('This join link is damaged.');
  }
  if (v !== PAYLOAD_VERSION) {
    const err = new PayloadError(
      'This company uses a newer version of the join link. Update the app and try again.',
    );
    (err as PayloadError & { needsNewerApp: boolean }).needsNewerApp = true;
    throw err;
  }
  // Within a known v, unrecognized members MUST be ignored (forward
  // compatibility) — we read only what we understand.
  const channels = (Array.isArray(obj.channels) ? obj.channels : [])
    .filter((c): c is string => typeof c === 'string' && CHANNEL_RE.test(c));
  const privateFeeds = (Array.isArray(obj.private_feeds) ? obj.private_feeds : [])
    .filter((f): f is string => typeof f === 'string')
    .filter((f) => {
      try {
        const u = new URL(f);
        return u.protocol === 'https:' || u.protocol === 'http:';
      } catch {
        return false;
      }
    });
  return { origin: url.origin, payload: { v, channels, privateFeeds }, joinUrl: url.toString() };
}

/** The root anchor URL for a confirmed origin (RFC 8615 well-known space). */
export function rootAnchorUrl(origin: string): string {
  return origin + '/.well-known/keryx/root.json';
}
