/**
 * Service-worker wake-up handling (relay/SPECIFICATION.md §4.2): the push
 * event carries the decrypted §4 envelope (the browser performs RFC 8291),
 * so the worker parses it strictly, resolves the locally followed topic, tries
 * the signature threshold against the company's verified TUF authorization, and
 * persists the accepted `seq`.
 *
 * The worker does no TUF metadata or content network work: verification reads
 * only the locally cached metadata, an unverifiable wake-up is recorded for the
 * page to re-verify with the full TUF state and its recovery allowance, and the
 * content sync belongs to the page (design/notifications.md, "Worker-side
 * processing"). The one network call is the liveness/delivery ack (§5.3).
 */

/** The service worker global scope (only imported by src/sw.ts and tests). */
declare const self: ServiceWorkerGlobalScope;

import {
  getAllCompanies,
  getAllItems,
  putCompany,
  putItems,
  deleteItems,
  relaySeq,
  setRelaySeq,
  markPendingRecovery,
  getRegistration,
  getRegistrations,
  putRegistration,
  deleteRegistrationRecord,
  pendingTest,
  clearPendingTest,
  putPendingTest,
  markPushReceived,
  type CompanyRecord,
  type StoredItem,
} from './store';
import { syncCompany } from './sync';
import {
  parseWakeup,
  verifyWakeup,
  topicAuthorization,
  deriveTopic,
  sourceHash,
  publicScopeId,
  privateScopeId,
  companyIdFromOrigin,
  createRegistration,
  updateRegistration,
  relayHeartbeat,
  subscribePush,
  deleteRegistration,
  relayBaseUrl,
  vapidPublicKey,
  hasDuplicateKeys,
  RelayGone,
  type RelayRegistration,
  type PushSubscriptionKeys,
  type TopicBinding,
  type Wakeup,
} from './relay';

/** Client recovery cooldown: X hours per company, persisted (§4.2). */
export const RECOVERY_COOLDOWN_MS = 6 * 60 * 60 * 1000;

/**
 * Where the endpoint subscription comes from (design/notifications.md): the
 * browser's PushManager by default, the UnifiedPush connector on Android. The page
 * installs the native source; the service-worker bundle never imports Capacitor.
 */
export interface SubscriptionSource {
  subscribe(vapid: string): Promise<PushSubscriptionKeys>;
  current(): Promise<PushSubscriptionKeys | null>;
  unsubscribe(): Promise<void>;
}

let subscriptionSource: SubscriptionSource | null = null;

/** The page installs the native source on Android; the SW keeps the default. */
export function setSubscriptionSource(source: SubscriptionSource | null): void {
  subscriptionSource = source;
}

/** The browser's PushManager as a subscription source (the default). */
const webPushSource: SubscriptionSource = {
  subscribe: (vapid) => subscribePush(vapid),
  async current() {
    const sub = await pushManagerSubscription();
    if (!sub) return null;
    const json = sub.toJSON();
    const keys = json.keys ?? {};
    if (!json.endpoint || !keys.p256dh || !keys.auth) return null;
    return { endpoint: json.endpoint, p256dh: keys.p256dh, auth: keys.auth };
  },
  async unsubscribe() {
    const sub = await pushManagerSubscription();
    if (sub) await sub.unsubscribe();
  },
};

function activeSource(): SubscriptionSource {
  return subscriptionSource ?? webPushSource;
}

/** The order token is the capability URL's path segment before `feed.json`. */
export function orderTokenFromUrl(url: string): string | null {
  try {
    const parts = new URL(url).pathname.split('/').filter(Boolean);
    if (parts.length < 2) return null;
    return parts[parts.length - 2];
  } catch {
    return null;
  }
}

/**
 * Derive the topic → binding map for every followed item: public channels and
 * private capability feeds (relay/SPECIFICATION.md §3, §10).
 */
export function topicBindings(company: CompanyRecord): Record<string, TopicBinding> {
  const out: Record<string, TopicBinding> = {};
  const companyId = companyIdFromOrigin(company.origin);
  for (const ch of company.channels) {
    if (!ch.followed) continue;
    const scopeId = publicScopeId(ch.name);
    const h = sourceHash(companyId, ch.name);
    out[deriveTopic(companyId, scopeId, h)] = { channel: ch.name, displayName: ch.displayName, scopeId };
  }
  for (const sub of company.privateFeeds) {
    if (sub.closed) continue;
    const token = orderTokenFromUrl(sub.url);
    if (!token) continue;
    const entry = company.targets.signed.custom?.private_feed_patterns?.find((e) => e.channel === sub.channel);
    if (!entry) continue;
    const scopeId = privateScopeId(entry.channel, entry.pattern);
    const h = sourceHash(companyId, token);
    out[deriveTopic(companyId, scopeId, h)] = {
      channel: entry.channel,
      displayName: sub.displayName ?? entry.channel,
      scopeId,
    };
  }
  return out;
}

/** Find the company whose derived topic bindings contain `topic`. */
export async function companyForTopic(
  topic: string,
): Promise<{ company: CompanyRecord; binding: TopicBinding } | null> {
  for (const company of await getAllCompanies()) {
    const binding = topicBindings(company)[topic];
    if (binding) return { company, binding };
  }
  return null;
}

export interface PushOutcome {
  /** the wake-up verified and its `seq` was accepted */
  accepted: boolean;
  /** the payload was a §4.3 self-test, not a wake-up */
  test?: boolean;
  /** a wake-up the worker could not verify against cached metadata: the page re-verifies */
  pendingRecovery?: boolean;
  title?: string;
  body?: string;
  origin?: string;
  /** the wake-up's derived topic: the notification tag keys off it */
  topic?: string;
}

/**
 * Handle one push event. Returns the notification to show, or `accepted: false`
 * when the wake-up is dropped without a content fetch. Replay state is never
 * advanced from an unverified message.
 */
export async function handlePush(data: string | ArrayBuffer | Uint8Array): Promise<PushOutcome> {
  // a §4.3 self-test is never a wake-up: it only records receipt
  const maybeTest = testPayloadOf(data);
  if (maybeTest) {
    const accepted = await handleTestPayload(maybeTest);
    return { accepted, test: accepted };
  }

  let wakeup: Wakeup;
  try {
    wakeup = parseWakeup(data);
  } catch {
    return { accepted: false }; // malformed or unknown version: drop
  }
  const found = await companyForTopic(wakeup.t);
  if (!found) return { accepted: false }; // topic not currently followed
  const { company, binding } = found;

  const authorization = topicAuthorization(company.targets, binding);
  if (!authorization || !verifyWakeup(wakeup, authorization.keys, authorization.threshold)) {
    // The worker does no TUF metadata or content work (design/notifications.md,
    // "Worker-side processing"): a failed verification may just mean the cached
    // metadata is stale after a key rotation, so record the wake-up for the page —
    // which re-verifies with the full TUF state and its recovery allowance — and
    // never notify on it here.
    await markPendingRecovery({ origin: company.origin, topic: wakeup.t, seq: wakeup.seq, at: Date.now() });
    return { accepted: false, pendingRecovery: true };
  }

  // Only after successful verification: atomically compare and persist `seq`.
  const last = await relaySeq(company.origin, wakeup.t);
  if (wakeup.seq <= last) return { accepted: false }; // replay
  await setRelaySeq(company.origin, wakeup.t, wakeup.seq);
  await markPushReceived(company.origin, Date.now());

  // The liveness/delivery ack (§5.3): the worker keeps this one call, but it
  // never recovers — the page's foreground check owns recovery.
  void ackRelayReceipt(company.origin);

  // The page owns the metadata recovery and the content sync: this message
  // makes it sync content; with no page open, the next open catches up.
  await notifyClients(company.origin);

  const name = company.targets.signed.custom?.company_name ?? company.origin;
  return {
    accepted: true,
    origin: company.origin,
    topic: wakeup.t,
    title: name,
    // Locally authored generic notice: it never claims a publisher message
    // exists and never includes unverified content (§6.2). The channel label is
    // the locally verified display_name, so it leaks nothing the app does not
    // already know.
    body: `New update in ${binding.displayName}`,
  };
}

/** Parse a §4.3 self-test payload, or null when it is not one. */
function testPayloadOf(data: string | ArrayBuffer | Uint8Array): { v: number; test: true; nonce: string } | null {
  const text = typeof data === 'string' ? data : new TextDecoder().decode(data);
  if (hasDuplicateKeys(text)) return null;
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch {
    return null;
  }
  if (typeof parsed !== 'object' || parsed === null) return null;
  const keys = Object.keys(parsed);
  if (keys.length !== 3 || !keys.every((k) => ['v', 'test', 'nonce'].includes(k))) return null;
  const p = parsed as { v?: unknown; test?: unknown; nonce?: unknown };
  if (p.v !== 1 || p.test !== true || typeof p.nonce !== 'string' || p.nonce.length !== 43) return null;
  return { v: 1, test: true, nonce: p.nonce };
}

/**
 * Handle a §4.3 self-test payload: accept it only while it matches the
 * pending test for its relay, then record receipt. Never a wake-up.
 */
export async function handleTestPayload(payload: unknown): Promise<boolean> {
  const p = payload as { v?: number; test?: boolean; nonce?: string };
  if (p?.v !== 1 || p.test !== true || typeof p.nonce !== 'string') return false;
  const registrations = await getRegistrations();
  for (const reg of registrations) {
    const pending = await pendingTest(reg.baseUrl);
    if (!pending || pending.nonce !== p.nonce) continue;
    if (Date.now() > pending.expiresAt) {
      await clearPendingTest(reg.baseUrl);
      return false;
    }
    await putPendingTest({ ...pending, receivedAt: Date.now() });
    return true;
  }
  return false;
}

/**
 * Tell every open window that a wake-up synced content, so the UI re-reads
 * the store and shows the new item without a manual reload. Best-effort.
 */
async function notifyClients(origin: string): Promise<void> {
  if (typeof self === 'undefined' || !('clients' in self)) return;
  const windows = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
  for (const client of windows) client.postMessage({ type: 'keryx-sync', origin });
}

/** The union of every followed company's topic bindings. */export function unionTopics(companies: CompanyRecord[]): Record<string, TopicBinding> {
  const topics: Record<string, TopicBinding> = {};
  for (const company of companies) Object.assign(topics, topicBindings(company));
  return topics;
}

/**
 * Ensure the installation's registration matches every followed company's topics
 * (relay/SPECIFICATION.md §5.3). One record per relay holds the union; a `409`
 * or `401` means the local token is stale, so the app obtains a fresh browser
 * subscription and registers it. Returns the registration, or undefined when the
 * relay is unconfigured or the attempt failed (best-effort: independent content
 * sync remains available).
 */
export async function ensureRelayRegistration(
  companies: CompanyRecord[],
  fresh = false,
): Promise<RelayRegistration | undefined> {
  const base = relayBaseUrl();
  const vapid = vapidPublicKey();
  if (!base || !vapid) return undefined;
  const topics = unionTopics(companies);
  const topicList = Object.keys(topics);
  if (topicList.length === 0) {
    const existing = await getRegistration(base);
    if (existing) await deleteRegistration(base, existing.id, existing.managementToken);
    await deleteRegistrationRecord(base);
    return undefined;
  }
  if (fresh) return recoverRelayRegistration(base);
  let relay = await getRegistration(base);
  try {
    if (!relay) {
      const sub = await activeSource().subscribe(vapid);
      const created = await createRegistration(base, sub, topicList);
      relay = {
        baseUrl: base,
        id: created.id,
        managementToken: created.managementToken,
        topics,
        endpoint: sub.endpoint,
      };
    } else {
      // a changed endpoint (a re-registered distributor) needs a fresh record:
      // the old one can never receive a wake-up again
      const current = await activeSource().current();
      if (current && relay.endpoint && current.endpoint !== relay.endpoint) {
        await deleteRegistration(base, relay.id, relay.managementToken).catch(() => undefined);
        await deleteRegistrationRecord(base);
        const created = await createRegistration(base, current, topicList);
        relay = {
          baseUrl: base,
          id: created.id,
          managementToken: created.managementToken,
          topics,
          endpoint: current.endpoint,
        };
      } else {
        await updateRegistration(base, relay.id, relay.managementToken, topicList);
        relay = { ...relay, topics, endpoint: current?.endpoint ?? relay.endpoint };
      }
    }
  } catch (err) {
    // the relay no longer knows the registration: recover with a fresh one;
    // a transport failure must never destroy a working subscription (§5.3)
    if (!(err instanceof RelayGone)) return undefined;
    return recoverRelayRegistration(base);
  }
  await putRegistration(relay);
  return relay;
}

/**
 * The active service worker registration: the worker's own registration when
 * this runs in the service worker, the page's controller registration
 * otherwise. The recovery runs in both contexts (§5.3).
 */
async function pushManagerRegistration(): Promise<ServiceWorkerRegistration> {
  if (typeof self !== 'undefined' && 'registration' in self) return self.registration;
  return navigator.serviceWorker.ready;
}

/**
 * Recover a registration the relay no longer knows: obtain a fresh browser
 * subscription and POST it with the current topic union (§5.3). Shared by the
 * page's ensure/foreground check and the service worker's wake-up heartbeat.
 */
export async function recoverRelayRegistration(base: string): Promise<RelayRegistration | undefined> {
  const vapid = vapidPublicKey();
  if (!vapid) return undefined;
  const topics = unionTopics(await getAllCompanies());
  try {
    await activeSource().unsubscribe();
  } catch {
    // best-effort: a failed unsubscribe must not block the fresh one
  }
  await deleteRegistrationRecord(base);
  try {
    const sub = await activeSource().subscribe(vapid);
    const created = await createRegistration(base, sub, Object.keys(topics));
    const relay = {
      baseUrl: base,
      id: created.id,
      managementToken: created.managementToken,
      topics,
      endpoint: sub.endpoint,
    };
    await putRegistration(relay);
    return relay;
  } catch {
    return undefined;
  }
}

/** The browser's current push subscription, or null. */
async function pushManagerSubscription(): Promise<PushSubscription | null> {
  if (typeof self === 'undefined' || !('registration' in self)) {
    if (!('serviceWorker' in navigator)) return null;
  }
  const registration = await pushManagerRegistration();
  return registration.pushManager.getSubscription();
}

/** Liveness ack after an accepted wake-up (§5.3). */
export async function heartbeatRelay(origin: string): Promise<void> {
  const base = relayBaseUrl();
  if (!base) return;
  const relay = await getRegistration(base);
  if (!relay) return;
  try {
    await relayHeartbeat(relay.baseUrl, relay.id, relay.managementToken);
  } catch (err) {
    // the relay no longer knows this registration: obtain a fresh one (§5.3).
    // A transport failure is best-effort and never destroys a subscription.
    if (err instanceof RelayGone) void recoverRelayRegistration(relay.baseUrl);
  }
}

/**
 * The worker's liveness/delivery ack (§5.3: "Sent by the service worker on
 * wake-up receipt"): one POST, no recovery. A gone registration is left to the
 * page's foreground check (checkRelayRegistration), which owns recovery.
 */
export async function ackRelayReceipt(origin: string): Promise<void> {
  const base = relayBaseUrl();
  if (!base) return;
  const relay = await getRegistration(base);
  if (!relay) return;
  try {
    await relayHeartbeat(relay.baseUrl, relay.id, relay.managementToken);
  } catch {
    // best-effort: the page's foreground check recovers a gone registration
  }
}

/** Foreground re-check cadence: tab switches must not spam the relay. */
export const FOREGROUND_CHECK_INTERVAL_MS = 5 * 60 * 1000;

/**
 * The foreground check (§5.3): a liveness ack that extends `last_seen`, and
 * the app's only way to learn that the relay no longer knows the registration.
 * Returns 'ok' when the registration is current or was recovered, 'failed' when
 * the relay says gone and the recovery could not re-register, and undefined
 * when there is nothing to check or the relay was unreachable.
 */
export async function checkRelayRegistration(): Promise<'ok' | 'failed' | undefined> {
  const base = relayBaseUrl();
  if (!base) return undefined;
  const relay = await getRegistration(base);
  if (!relay) return undefined;
  if (!(await activeSource().current())) return (await recoverRelayRegistration(base)) ? 'ok' : 'failed';
  try {
    await relayHeartbeat(base, relay.id, relay.managementToken);
    return 'ok';
  } catch (err) {
    if (!(err instanceof RelayGone)) return undefined;
    return (await recoverRelayRegistration(base)) ? 'ok' : 'failed';
  }
}

/** Run one content reconciliation and persist its outcome. */
export async function sync(company: CompanyRecord): Promise<void> {
  const all = await getAllItems();
  const existing = new Map(all.filter((i) => i.origin === company.origin).map((i) => [i.id, i]));
  const outcome = await syncCompany(company, fetch, existing);
  await putCompany(outcome.company);
  if (outcome.toPut.length > 0) await putItems(outcome.toPut);
  if (outcome.toDelete.length > 0) await deleteItems(outcome.toDelete);
}
