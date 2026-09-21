/**
 * Relay protocol (relay/SPECIFICATION.md §3–§5) for the app: topic and
 * scope_id derivation, the §4 wake-up envelope with strict parsing and Ed25519
 * threshold verification, and the UnifiedPush/WebPush registration client.
 *
 * The browser's PushManager and the push service handle RFC 8291 encryption;
 * the service worker receives the decrypted §4 envelope in its `push` event, so
 * this module never implements encryption — it verifies authenticity (§4.2).
 */

import { sha256 } from '@noble/hashes/sha2.js';
import { base64urlToBytes, bytesToBase64url, hexToBytes, sha256Hex } from './bytes';
import { olpcCanonical } from './olpc';
import { ed25519Verify } from './ed';
import type { AuthorizedKey, TargetsDoc } from './tuf';
import { extractAuthorization } from './tuf';

const encoder = new TextEncoder();

/** Static public domain separators (relay/SPECIFICATION.md §3–§4). */
export const TOPIC_SALT = 'keryx/relay/v1|';
export const SCOPE_SALT = 'keryx/relay/scope/v1|';
export const WAKEUP_DOMAIN = 'keryx/wakeup/v1|';

/** The largest allowed `seq` value (2^53-1, relay/SPECIFICATION.md §4). */
export const MAX_SEQ = 9007199254740991;

function utf8(...parts: string[]): Uint8Array {
  return encoder.encode(parts.join(''));
}

/** scope_id for a public channel descriptor (§3.1). */
export function publicScopeId(channel: string): string {
  return sha256Hex(utf8(SCOPE_SALT, olpcCanonical({ kind: 'public', channel })));
}

/** scope_id for a private pattern descriptor (§3.1). */
export function privateScopeId(channel: string, pattern: string): string {
  return sha256Hex(utf8(SCOPE_SALT, olpcCanonical({ kind: 'private', channel, pattern })));
}

/**
 * The canonical company_id of a join origin (relay/SPECIFICATION.md §2):
 * lowercase ASCII host, punycode for IDNs, no scheme, no port, no slash.
 * The app stores the full origin; topics are always derived from this host.
 */
export function companyIdFromOrigin(origin: string): string {
  try {
    return new URL(origin).hostname.toLowerCase();
  } catch {
    return origin.toLowerCase();
  }
}

/** Source hash h = hex(sha256(company_id + "|" + subject)) (§3). */
export function sourceHash(companyId: string, subject: string): string {
  return sha256Hex(utf8(`${companyId}|${subject}`));
}

/** topic = base64url(sha256(salt + OLPC({company_id, scope_id, h}))) (§3). */
export function deriveTopic(companyId: string, scopeId: string, h: string): string {
  return bytesToBase64url(
    sha256(utf8(TOPIC_SALT, olpcCanonical({ company_id: companyId, scope_id: scopeId, h }))),
  );
}

// --- wake-up envelope (§4) -------------------------------------------------

export interface WakeupSig {
  keyid: string;
  sig: string;
}

export interface Wakeup {
  v: number;
  t: string;
  seq: number;
  sig: WakeupSig[];
}

export class WakeupError extends Error {}

/**
 * Reject duplicate JSON member names at any nesting level (§4). A minimal
 * scanner is needed because JSON.parse silently collapses duplicates.
 */
export function hasDuplicateKeys(text: string): boolean {
  let i = 0;
  const ws = () => {
    while (i < text.length && /\s/.test(text[i])) i++;
  };
  const value = (): boolean => {
    ws();
    const c = text[i];
    if (c === '{') {
      i++;
      const keys = new Set<string>();
      ws();
      if (text[i] === '}') {
        i++;
        return false;
      }
      for (;;) {
        ws();
        const key = string();
        if (key === null) throw new WakeupError('malformed JSON');
        if (keys.has(key)) return true;
        keys.add(key);
        ws();
        if (text[i] !== ':') throw new WakeupError('malformed JSON');
        i++;
        if (value()) return true;
        ws();
        if (text[i] === ',') {
          i++;
          continue;
        }
        if (text[i] === '}') {
          i++;
          return false;
        }
        throw new WakeupError('malformed JSON');
      }
    }
    if (c === '[') {
      i++;
      ws();
      if (text[i] === ']') {
        i++;
        return false;
      }
      for (;;) {
        if (value()) return true;
        ws();
        if (text[i] === ',') {
          i++;
          continue;
        }
        if (text[i] === ']') {
          i++;
          return false;
        }
        throw new WakeupError('malformed JSON');
      }
    }
    if (c === '"') return string() === null;
    while (i < text.length && !/[\s,\]}]/.test(text[i])) i++;
    return false;
  };
  const string = (): string | null => {
    if (text[i] !== '"') return null;
    i++;
    let out = '';
    while (i < text.length && text[i] !== '"') {
      if (text[i] === '\\') {
        i += 2;
        out += 'x';
        continue;
      }
      out += text[i++];
    }
    if (text[i] !== '"') return null;
    i++;
    return out;
  };
  value();
  return false;
}

/** Strictly parse the §4 wake-up envelope. */
export function parseWakeup(data: string | ArrayBuffer | Uint8Array): Wakeup {
  const text = typeof data === 'string' ? data : new TextDecoder().decode(data);
  if (hasDuplicateKeys(text)) throw new WakeupError('duplicate JSON member');
  let raw: unknown;
  try {
    raw = JSON.parse(text);
  } catch {
    throw new WakeupError('malformed JSON');
  }
  if (typeof raw !== 'object' || raw === null || Array.isArray(raw)) {
    throw new WakeupError('wake-up must be an object');
  }
  const obj = raw as Record<string, unknown>;
  const keys = Object.keys(obj);
  for (const k of keys) {
    if (!['v', 't', 'seq', 'sig'].includes(k)) throw new WakeupError(`unknown field ${k}`);
  }
  if (obj.v !== 1) throw new WakeupError(`unknown version ${String(obj.v)}`);
  if (typeof obj.t !== 'string' || !/^[A-Za-z0-9_-]{43}$/.test(obj.t)) {
    throw new WakeupError('t must be a 43-char base64url topic');
  }
  if (
    typeof obj.seq !== 'number' ||
    !Number.isInteger(obj.seq) ||
    obj.seq < 1 ||
    obj.seq > MAX_SEQ
  ) {
    throw new WakeupError('seq must be an integer from 1 through 9007199254740991');
  }
  if (!Array.isArray(obj.sig) || obj.sig.length === 0) {
    throw new WakeupError('sig must be a nonempty array');
  }
  const sig: WakeupSig[] = obj.sig.map((entry) => {
    if (typeof entry !== 'object' || entry === null) throw new WakeupError('sig entry must be an object');
    const e = entry as Record<string, unknown>;
    if (typeof e.keyid !== 'string' || !/^[0-9a-f]{64}$/.test(e.keyid)) {
      throw new WakeupError('sig.keyid must be 64 lowercase hex');
    }
    if (typeof e.sig !== 'string') throw new WakeupError('sig.sig must be a string');
    return { keyid: e.keyid, sig: e.sig };
  });
  return { v: 1, t: obj.t, seq: obj.seq, sig };
}

/** The exact bytes signed by the publisher: domain + OLPC({v, t, seq}) (§4.1). */
export function wakeupSignedBytes(v: number, t: string, seq: number): Uint8Array {
  return utf8(WAKEUP_DOMAIN, olpcCanonical({ v, t, seq }));
}

/**
 * Verify the wake-up threshold against the topic's exact scope keys (§4.1):
 * unknown keyids are ignored, a known keyid with an invalid signature
 * rejects, and duplicate keyids do not count twice.
 */
export function verifyWakeup(
  wakeup: Wakeup,
  keys: AuthorizedKey[],
  threshold: number,
): boolean {
  const need = Math.max(1, threshold || 1);
  const msg = wakeupSignedBytes(wakeup.v, wakeup.t, wakeup.seq);
  const seen = new Set<string>();
  let valid = 0;
  for (const s of wakeup.sig) {
    const key = keys.find((k) => k.keyid === s.keyid);
    if (!key || seen.has(s.keyid)) continue;
    seen.add(s.keyid);
    let raw: Uint8Array;
    try {
      raw = base64urlToBytes(s.sig);
    } catch {
      return false;
    }
    if (raw.length !== 64 || !ed25519Verify(raw, msg, key.pub)) return false;
    valid++;
  }
  return valid >= need;
}

/**
 * The exact authorization for one topic: the public channel's role keys, or a
 * private pattern entry's keys. Returns null when the topic's binding is not
 * authorized by the company's verified metadata.
 */
export function topicAuthorization(
  targets: TargetsDoc,
  binding: { channel: string; scopeId: string },
): { keys: AuthorizedKey[]; threshold: number } | null {
  const auth = extractAuthorization(targets);
  const channel = auth.channels.get(binding.channel);
  if (channel && publicScopeId(binding.channel) === binding.scopeId) {
    return { keys: channel.keys, threshold: channel.threshold };
  }
  for (const entry of auth.privatePatterns) {
    if (entry.channel !== binding.channel) continue;
    if (privateScopeId(entry.channel, entry.pattern) !== binding.scopeId) continue;
    const keys: AuthorizedKey[] = [];
    for (const keyid of entry.keyids) {
      const key = entry.keys[keyid];
      if (!key) return null;
      keys.push({ keyid, pub: hexToBytes(key.keyval.public) });
    }
    return { keys, threshold: Math.max(1, entry.threshold || 1) };
  }
  return null;
}

// --- registration client (§5.3) ---------------------------------------------

export interface TopicBinding {
  channel: string;
  scopeId: string;
}

export interface RelayRegistration {
  baseUrl: string;
  id: string;
  managementToken: string;
  /** derived topic → the followed channel + exact scope it binds */
  topics: Record<string, TopicBinding>;
}

export interface PushSubscriptionKeys {
  endpoint: string;
  p256dh: string;
  auth: string;
}

/**
 * The staging relay (relay/fly.toml, relay/README.md "Deploy to Fly.io"). A
 * production build that sets neither VITE_RELAY_URL nor VITE_VAPID_PUBLIC uses it,
 * so the published PWA receives wake-ups by default; dev and test builds without
 * the variables stay offline (polling is the backstop).
 */
export const DEFAULT_RELAY_URL = 'https://keryx-relay.fly.dev';
export const DEFAULT_VAPID_PUBLIC =
  'BOJ7j2UTkiGYAKjEs5SiMiKl7UdQAhcKogExGAvbdzX0HE5CX48NS9q9Yo_eOJEhaDfdVUoDf7_dne5ABqd5YYY';

/** The relay base URL: the build's VITE_RELAY_URL or the staging relay. */
export function relayBaseUrl(): string | null {
  const url = import.meta.env.VITE_RELAY_URL;
  if (typeof url === 'string' && url) return url.replace(/\/+$/, '');
  return import.meta.env.PROD ? DEFAULT_RELAY_URL : null;
}

/** The VAPID public key: the build's VITE_VAPID_PUBLIC or the staging relay's. */
export function vapidPublicKey(): string | null {
  const key = import.meta.env.VITE_VAPID_PUBLIC;
  if (typeof key === 'string' && key) return key;
  return import.meta.env.PROD ? DEFAULT_VAPID_PUBLIC : null;
}

async function relayFetch(baseUrl: string, path: string, init: RequestInit): Promise<Response> {
  const res = await fetch(baseUrl + path, {
    ...init,
    headers: { 'Content-Type': 'application/json', ...(init.headers ?? {}) },
  });
  return res;
}

/** Subscribe the browser's PushManager with the relay's VAPID key. */
export async function subscribePush(vapid: string): Promise<PushSubscriptionKeys> {
  const registration = await navigator.serviceWorker.ready;
  const sub = await registration.pushManager.subscribe({
    userVisibleOnly: true,
    applicationServerKey: base64urlToBytes(vapid) as BufferSource,
  });
  const json = sub.toJSON();
  const keys = json.keys ?? {};
  if (!json.endpoint || !keys.p256dh || !keys.auth) {
    throw new Error('push subscription is missing endpoint or keys');
  }
  return { endpoint: json.endpoint, p256dh: keys.p256dh, auth: keys.auth };
}

/** POST /v1/registrations — create the registration and return its token. */
export async function createRegistration(
  baseUrl: string,
  sub: PushSubscriptionKeys,
  topics: string[],
): Promise<{ id: string; managementToken: string }> {
  const res = await relayFetch(baseUrl, '/v1/registrations', {
    method: 'POST',
    body: JSON.stringify({ endpoint: sub.endpoint, keys: { p256dh: sub.p256dh, auth: sub.auth }, topics }),
  });
  if (!res.ok) throw new Error(`relay registration: HTTP ${res.status}`);
  const body = (await res.json()) as { id?: string; management_token?: string };
  if (!body.id || !body.management_token) throw new Error('relay registration: malformed response');
  return { id: body.id, managementToken: body.management_token };
}

/** PUT /v1/registrations/{id} — replace the followed-topic set. */
export async function updateRegistration(
  baseUrl: string,
  id: string,
  managementToken: string,
  topics: string[],
): Promise<void> {
  const res = await relayFetch(baseUrl, `/v1/registrations/${id}`, {
    method: 'PUT',
    headers: { Authorization: `Bearer ${managementToken}` },
    body: JSON.stringify({ topics }),
  });
  if (!res.ok) throw new Error(`relay update: HTTP ${res.status}`);
}

/** POST /v1/registrations/{id}/heartbeat — liveness ack (§5.3). */
export async function relayHeartbeat(
  baseUrl: string,
  id: string,
  managementToken: string,
): Promise<void> {
  const res = await relayFetch(baseUrl, `/v1/registrations/${id}/heartbeat`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${managementToken}` },
  });
  if (!res.ok) throw new Error(`relay heartbeat: HTTP ${res.status}`);
}

/** DELETE /v1/registrations/{id} — remove the registration (uninstall). */
export async function deleteRegistration(
  baseUrl: string,
  id: string,
  managementToken: string,
): Promise<void> {
  const res = await relayFetch(baseUrl, `/v1/registrations/${id}`, {
    method: 'DELETE',
    headers: { Authorization: `Bearer ${managementToken}` },
  });
  if (!res.ok && res.status !== 404) throw new Error(`relay delete: HTTP ${res.status}`);
}
