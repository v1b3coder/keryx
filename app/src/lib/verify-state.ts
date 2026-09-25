/**
 * Push the native verification mirror (design/notifications.md): for every
 * followed topic, the exact scope keys + threshold from the company's verified
 * TUF metadata, and the locally persisted `seq`. Called after every change that
 * can alter the set — pairing, channel toggle, company removal, sync, and on
 * startup — and after a page-side `handlePush` advances a `seq`.
 *
 * The native worker reads only this mirror: it is derived from the JS-verified
 * state, so a malicious relay cannot forge it, only replay (dropped by `seq`).
 */
import { getAllCompanies, getRegistration, relaySeq } from './store';
import { topicBindings } from './relay-sw';
import { topicAuthorization, relayBaseUrl } from './relay';
import { KeryxPush } from './native-push';
import { bytesToHex } from './bytes';

export async function pushVerifyState(): Promise<void> {
  const topics: Record<string, unknown> = {};
  for (const company of await getAllCompanies()) {
    for (const [topic, binding] of Object.entries(topicBindings(company))) {
      const authorization = topicAuthorization(company.targets, binding);
      if (!authorization) continue;
      topics[topic] = {
        keys: authorization.keys.map((k) => ({ keyid: k.keyid, pub: bytesToHex(k.pub) })),
        threshold: authorization.threshold,
        lastSeq: await relaySeq(company.origin, topic),
      };
    }
  }
  await KeryxPush.setVerifyState({ state: { topics } });
  // the worker's liveness/delivery ack needs the registration credentials
  const base = relayBaseUrl();
  const relay = base ? await getRegistration(base) : undefined;
  await KeryxPush.setRegistration({
    registration: relay
      ? { baseUrl: relay.baseUrl, id: relay.id, managementToken: relay.managementToken }
      : null,
  });
}
