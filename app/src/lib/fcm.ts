/**
 * The FCM topic leg (relay/SPECIFICATION.md §6.1): the Android shell's
 * Firebase SDK follows the union of every followed company's topics, and the
 * relay publishes to the derived topic. The leg is registry-free and anonymous —
 * no endpoint, no relay registration, no heartbeat — so this module only syncs the
 * topic set and runs the §5.3.1 topic-leg self-test.
 */
import { KeryxPush } from './native-push';
import { clearPendingTest, pendingTest, putPendingTest } from './store';
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
  try {
    const { topics } = await KeryxPush.setTopics({ topics: union(companies) });
    return topics;
  } catch {
    return undefined;
  }
}

/** Whether the native SDK's topic set matches the union. */
export async function fcmTopicsSynced(companies: CompanyRecord[]): Promise<boolean> {
  const wanted = union(companies);
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
  if (!base) return { endpoint: 'failed', leg: 'registration' };
  const topics = union(companies);
  let test;
  try {
    test = await startFcmTest(base);
  } catch {
    return { endpoint: 'failed', leg: 'registration' };
  }
  try {
    await KeryxPush.setTopics({ topics: [...topics, test.topic] });
    await putPendingTest({ baseUrl: base, nonce: test.nonce, expiresAt: Date.parse(test.expiresAt) });
    await fcmTestReady(base, test.testId);
  } catch {
    await KeryxPush.setTopics({ topics }).catch(() => undefined);
    await clearPendingTest(base);
    return { endpoint: 'failed', leg: 'topic' };
  }
  const deadline = Date.now() + waitMs;
  while (Date.now() < deadline) {
    const pending = await pendingTest(base);
    if (pending?.receivedAt) {
      await clearPendingTest(base);
      await KeryxPush.setTopics({ topics }).catch(() => undefined);
      return { endpoint: 'delivered', testedAt: pending.receivedAt, leg: 'topic' };
    }
    await new Promise((resolve) => setTimeout(resolve, 250));
  }
  // The nonce stays pending until its capability expires, so a late delivery
  // still upgrades the state to green (design/notifications.md); the next
  // ensureFcmTopics drops the leftover test topic from the native set.
  return { endpoint: 'pending', leg: 'topic' };
}
