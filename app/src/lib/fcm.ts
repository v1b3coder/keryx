/**
 * The FCM topic leg (relay/SPECIFICATION.md §6.1): the Android native module
 * follows the union of every followed company's topics, and the relay publishes
 * to the derived topic. The leg is registry-free and anonymous — no endpoint, no
 * relay registration, no heartbeat — so this module only syncs the topic set and
 * runs the §5.3.1 topic-leg self-test.
 */
import { KeryxPush } from './native-push';
import { clearPendingTest, getAllCompanies, pendingTest, putPendingTest } from './store';
import type { CompanyRecord } from './store';
import { fcmTestReady, relayBaseUrl, startFcmTest } from './relay';
import { unionTopics } from './relay-sw';
import type { SelfTestResult } from './notify';

/** The topic-leg self-test's wait budget (design/notifications.md). */
export const FCM_TEST_TIMEOUT_MS = 20_000;

/** The topic union of every followed company, sorted. */
function union(companies: CompanyRecord[]): string[] {
  return Object.keys(unionTopics(companies)).sort();
}

/**
 * Subscribe the native SDK to exactly the union (§6.1). Returns the applied
 * topic set, or undefined when the SDK rejected the subscription — the caller
 * then falls back to UnifiedPush (design/notifications.md).
 */
export async function ensureFcmTopics(companies: CompanyRecord[]): Promise<string[] | undefined> {
  if (!KeryxPush) return undefined;
  try {
    const { topics } = await KeryxPush.setTopics({ topics: union(companies) });
    return topics;
  } catch {
    return undefined;
  }
}

/** Whether the native SDK's topic set matches the union. */
export async function fcmTopicsSynced(companies: CompanyRecord[]): Promise<boolean> {
  if (!KeryxPush) return false;
  const wanted = union(companies);
  // A subscribed test topic is expected while its capability is valid: after a
  // slow delivery the leftover test topic must not turn the state red.
  const base = relayBaseUrl();
  const pending = base ? await pendingTest(base) : undefined;
  if (pending?.topic && Date.now() <= pending.expiresAt) wanted.push(pending.topic);
  wanted.sort();
  try {
    const { topics } = await KeryxPush.getTopics();
    const current = [...topics].sort();
    return current.length === wanted.length && current.every((t, i) => t === wanted[i]);
  } catch {
    return false;
  }
}

/**
 * Run the topic-leg self-test (§5.3.1): ask the relay for a short-lived topic
 * and nonce, subscribe the native SDK to it, tell the relay the client is
 * ready, then wait for the handler to record the matching nonce. Never a
 * wake-up. `waitMs` exists for tests; callers use the default.
 */
export async function runFcmSelfTest(
  companies: CompanyRecord[],
  waitMs = FCM_TEST_TIMEOUT_MS,
): Promise<SelfTestResult> {
  const base = relayBaseUrl();
  if (!base || !KeryxPush) return { endpoint: 'failed', leg: 'registration' };
  const topics = union(companies);
  let test;
  try {
    test = await startFcmTest(base);
  } catch {
    return { endpoint: 'failed', leg: 'registration' };
  }
  try {
    await KeryxPush.setTopics({ topics: [...topics, test.topic] });
    await putPendingTest({
      baseUrl: base,
      nonce: test.nonce,
      topic: test.topic,
      expiresAt: Date.parse(test.expiresAt),
    });
    await fcmTestReady(base, test.testId);
  } catch {
    // Restore the topic set the store follows right now: a channel toggle made
    // while the test was in flight must not be reverted by the cleanup.
    await KeryxPush.setTopics({ topics: union(await getAllCompanies()) }).catch(() => undefined);
    await clearPendingTest(base);
    return { endpoint: 'failed', leg: 'topic' };
  }
  const deadline = Date.now() + waitMs;
  while (Date.now() < deadline) {
    const pending = await pendingTest(base);
    if (pending?.receivedAt) {
      await clearPendingTest(base);
      // The test topic is dropped against the store's current union, not the
      // union captured when the test started: a channel toggled during the test
      // must survive the cleanup.
      await KeryxPush.setTopics({ topics: union(await getAllCompanies()) }).catch(() => undefined);
      return { endpoint: 'delivered', testedAt: pending.receivedAt, leg: 'topic' };
    }
    await new Promise((resolve) => setTimeout(resolve, 250));
  }
  // The nonce stays pending until its capability expires, so a late delivery
  // still upgrades the state to green (design/notifications.md); the next
  // ensureFcmTopics drops the leftover test topic from the native set.
  return { endpoint: 'pending', leg: 'topic' };
}
