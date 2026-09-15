/**
 * Service-worker wake-up handling (relay/SPECIFICATION.md §4.2): the push
 * event carries the decrypted §4 envelope (the browser performs RFC 8291),
 * so the worker parses it strictly, resolves the locally followed topic, tries
 * the signature threshold against the company's verified TUF authorization,
 * allows at most one metadata refresh per company cooldown, persists the
 * accepted `seq`, then reconciles content and shows a notification.
 */

import {
  getAllCompanies,
  getAllItems,
  getCompany,
  putCompany,
  putItems,
  deleteItems,
  relaySeq,
  setRelaySeq,
  reserveRecovery,
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
  relayBaseUrl,
  vapidPublicKey,
  type TopicBinding,
  type Wakeup,
} from './relay';

/** Client recovery cooldown: X hours per company, persisted (§4.2). */
export const RECOVERY_COOLDOWN_MS = 6 * 60 * 60 * 1000;

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
    out[deriveTopic(companyId, scopeId, h)] = { channel: ch.name, scopeId };
  }
  for (const sub of company.privateFeeds) {
    if (sub.closed) continue;
    const token = orderTokenFromUrl(sub.url);
    if (!token) continue;
    const entry = company.targets.signed.custom?.private_feed_patterns?.find((e) => e.channel === sub.channel);
    if (!entry) continue;
    const scopeId = privateScopeId(entry.channel, entry.pattern);
    const h = sourceHash(companyId, token);
    out[deriveTopic(companyId, scopeId, h)] = { channel: entry.channel, scopeId };
  }
  return out;
}

/** Persist the derived topic bindings on the company record. */
export function withTopicBindings(company: CompanyRecord): CompanyRecord {
  if (!company.relay) return company;
  return { ...company, relay: { ...company.relay, topics: topicBindings(company) } };
}

/** Find the company whose persisted topic map contains `topic`. */
export async function companyForTopic(
  topic: string,
): Promise<{ company: CompanyRecord; binding: TopicBinding } | null> {
  for (const company of await getAllCompanies()) {
    const binding = company.relay?.topics?.[topic];
    if (binding) return { company, binding };
  }
  return null;
}

export interface PushOutcome {
  /** the wake-up verified and its `seq` was accepted */
  accepted: boolean;
  /** a metadata refresh was attempted under the recovery allowance */
  recovered?: boolean;
  title?: string;
  body?: string;
  origin?: string;
}

/**
 * Handle one push event. Returns the notification to show, or `accepted: false`
 * when the wake-up is dropped without a content fetch. Replay state is never
 * advanced from an unverified message.
 */
export async function handlePush(data: string | ArrayBuffer | Uint8Array): Promise<PushOutcome> {
  let wakeup: Wakeup;
  try {
    wakeup = parseWakeup(data);
  } catch {
    return { accepted: false }; // malformed or unknown version: drop
  }
  const found = await companyForTopic(wakeup.t);
  if (!found) return { accepted: false }; // topic not currently followed
  let { company } = found;
  const { binding } = found;

  let authorization = topicAuthorization(company.targets, binding);
  let recovered = false;
  if (!authorization || !verifyWakeup(wakeup, authorization.keys, authorization.threshold)) {
    // At most one metadata refresh per company per cooldown, reserved BEFORE
    // networking. A failed or timed-out refresh still consumes the allowance.
    if (!(await reserveRecovery(company.origin, Date.now(), RECOVERY_COOLDOWN_MS))) {
      return { accepted: false };
    }
    recovered = true;
    await sync(company);
    const refreshed = await getCompany(company.origin);
    if (!refreshed) return { accepted: false };
    company = refreshed;
    authorization = topicAuthorization(company.targets, binding);
    if (!authorization || !verifyWakeup(wakeup, authorization.keys, authorization.threshold)) {
      return { accepted: false }; // still unverifiable: never fetch content
    }
  }

  // Only after successful verification: atomically compare and persist `seq`.
  const last = await relaySeq(company.origin, wakeup.t);
  if (wakeup.seq <= last) return { accepted: false }; // replay
  await setRelaySeq(company.origin, wakeup.t, wakeup.seq);

  // The service worker acks receipt so the relay's registry TTL sweep keeps
  // the installation alive (§5.3); best-effort, never blocks the notification.
  void heartbeatRelay(company);

  if (!recovered) await sync(company);

  const name = company.targets.signed.custom?.company_name ?? company.origin;
  return {
    accepted: true,
    recovered,
    origin: company.origin,
    title: name,
    // Locally authored generic notice: it never claims a publisher message
    // exists and never includes unverified content (§6.2).
    body: 'New update available',
  };
}

/**
 * Ensure the installation's relay registration matches the followed topics
 * (relay/SPECIFICATION.md §5.3, §10). Returns the updated company record.
 * When the relay is not configured for this build the record is unchanged.
 */
export async function ensureRelayRegistration(company: CompanyRecord): Promise<CompanyRecord> {
  const base = relayBaseUrl();
  const vapid = vapidPublicKey();
  if (!base || !vapid) return company;
  const topics = topicBindings(company);
  const topicList = Object.keys(topics);
  let relay = company.relay && company.relay.baseUrl === base ? company.relay : undefined;
  try {
    if (!relay) {
      const sub = await subscribePush(vapid);
      const created = await createRegistration(base, sub, topicList);
      relay = {
        baseUrl: base,
        id: created.id,
        managementToken: created.managementToken,
        topics,
      };
    } else {
      await updateRegistration(base, relay.id, relay.managementToken, topicList);
      relay = { ...relay, topics };
    }
  } catch {
    // best-effort: independent content sync remains available
    return company;
  }
  return { ...company, relay };
}

/** Foreground liveness ack (§5.3). */
export async function heartbeatRelay(company: CompanyRecord): Promise<void> {
  if (!company.relay) return;
  try {
    await relayHeartbeat(company.relay.baseUrl, company.relay.id, company.relay.managementToken);
  } catch {
    // best-effort
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
