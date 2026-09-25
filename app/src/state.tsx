/**
 * App state: companies + items from the local store, sync orchestration.
 * Everything is local — no account, no server-side state.
 */

import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { Capacitor, type PluginListenerHandle } from '@capacitor/core';
import {
  getAllCompanies,
  getAllItems,
  getCompany,
  lastPushAt,
  pendingTest,
  pendingRecoveries,
  clearPendingRecovery,
  reserveRecovery,
  putCompany,
  putItems,
  deleteItems,
  markRead,
  deleteCompany,
  type CompanyRecord,
  type StoredItem,
} from './lib/store';
import { syncCompany, applyOutcomeItems } from './lib/sync';
import {
  checkRelayRegistration,
  ensureRelayRegistration,
  FOREGROUND_CHECK_INTERVAL_MS,
  handlePush,
  heartbeatRelay,
  RECOVERY_COOLDOWN_MS,
  setSubscriptionSource,
} from './lib/relay-sw';
import { relayBaseUrl, vapidPublicKey } from './lib/relay';
import {
  notificationState,
  permissionState,
  requestNativeNotificationPermission,
  runSelfTest,
  type NotificationState,
  type SelfTestResult,
} from './lib/notify';
import { nativePushSource, ensurePushWakeups, pushSupport } from './lib/push';
import { KeryxPush } from './lib/native-push';
import { pushVerifyState } from './lib/verify-state';
import { initDebugBuild } from './lib/build';
import { safeFetch } from './lib/urlpolicy';

export interface AppActions {
  refreshAll: () => Promise<void>;
  syncCompanyNow: (origin: string) => Promise<void>;
  toggleChannel: (origin: string, channel: string, followed: boolean) => Promise<void>;
  setPrefs: (
    origin: string,
    prefs: Partial<CompanyRecord['prefs']>,
  ) => Promise<void>;
  markRead: (origin: string, feedKey: string, itemId: string, read: boolean) => Promise<void>;
  acknowledgeLogo: (origin: string) => Promise<void>;
  removeCompany: (origin: string) => Promise<void>;
  saveCompany: (company: CompanyRecord, items?: StoredItem[]) => Promise<void>;
  /** re-pair after a company_name change: update the identity snapshot and clear the warning */
  rePairCompany: (origin: string, company: CompanyRecord, newItems?: StoredItem[]) => Promise<void>;
  /** ask for permission, register, self-test; the first-company outcome */
  enableNotifications: () => Promise<SelfTestResult>;
  /** re-read permission/subscription/registration state */
  checkNotifications: () => Promise<void>;
  /** re-run the self-test without prompting */
  runNotificationSelfTest: () => Promise<SelfTestResult>;
}

interface AppContextValue {
  companies: CompanyRecord[];
  loaded: boolean;
  syncing: boolean;
  notification: NotificationState;
  /** a self-test completed in this session: show the green enable-workflow tail */
  freshTest: boolean;
  actions: AppActions;
}

const AppContext = createContext<AppContextValue | null>(null);

/** App-wide fetch: cross-origin redirects are blocked (spec/core.md §1.2). */
const netFetch = safeFetch();

export function AppProvider({ children }: { children: ReactNode }) {
  const [companies, setCompanies] = useState<CompanyRecord[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [syncing, setSyncing] = useState(false);
  const [notification, setNotification] = useState<NotificationState>({ kind: 'checking' });
  const [freshTestAt, setFreshTestAt] = useState<number | null>(null);
  const itemsRef = useRef<StoredItem[]>([]);
  // an enable/retry self-test may still be in flight: its late arrival is the
  // same green tail, while a test from before a reload is never shown
  const sessionTestPending = useRef(false);

  /** Whether this session's in-flight self-test landed at the service worker. */
  const testLandedThisSession = useCallback(async (): Promise<boolean> => {
    if (!sessionTestPending.current) return false;
    const base = relayBaseUrl();
    const pending = base ? await pendingTest(base) : undefined;
    sessionTestPending.current = false;
    return Boolean(pending?.receivedAt);
  }, []);

  /** Re-read the app-wide state; a landed self-test turns on the green tail. */
  const refreshNotificationState = useCallback(async () => {
    const next = await notificationState();
    if (next.kind === 'ok' && (await testLandedThisSession())) setFreshTestAt(Date.now());
    setNotification(next);
  }, [testLandedThisSession]);

  // the green "Notifications are working" is the enable workflow's tail: it
  // expires on its own and is never shown after a reload
  useEffect(() => {
    if (freshTestAt === null) return;
    const t = setTimeout(() => setFreshTestAt(null), 6000);
    return () => clearTimeout(t);
  }, [freshTestAt]);

  // the foreground check (§5.3): extend last_seen and recover a registration
  // the relay no longer knows; throttled so tab switches do not spam it
  const lastRelayCheck = useRef(0);
  const runRelayCheck = useCallback(async () => {
    if (Date.now() - lastRelayCheck.current < FOREGROUND_CHECK_INTERVAL_MS) return;
    lastRelayCheck.current = Date.now();
    const result = await checkRelayRegistration();
    if (result === 'failed') setNotification({ kind: 'failed', leg: 'registration' });
    else if (result === 'ok') await refreshNotificationState();
  }, [refreshNotificationState]);

  /** Replace the in-memory items of one origin with the post-sync state. */
  const applyOutcome = (origin: string, existing: Map<string, StoredItem>) => {
    itemsRef.current = applyOutcomeItems(itemsRef.current, origin, existing);
  };

  /** One content reconciliation from the page (never the worker). */
  const syncCompanyNow = useCallback(
    async (origin: string) => {
      setSyncing(true);
      try {
        const company = await getCompany(origin);
        if (!company) return;
        const existing = new Map(
          itemsRef.current.filter((i) => i.origin === origin).map((i) => [i.id, i]),
        );
        const outcome = await syncCompany(company, netFetch, existing);
        applyOutcome(origin, existing);
        await putCompany(outcome.company);
        if (outcome.toPut.length > 0) await putItems(outcome.toPut);
        if (outcome.toDelete.length > 0) await deleteItems(outcome.toDelete);
        void heartbeatRelay(outcome.company.origin);
        const list = await getAllCompanies();
        setCompanies(list);
        // the app-wide state may have changed (a test landed, a leg died)
        await refreshNotificationState();
        await pushVerifyState();
      } finally {
        setSyncing(false);
      }
    },
    [refreshNotificationState],
  );

  /**
   * The network work the worker deliberately does not do (design/notifications.md,
   * "Worker-side processing"): re-verify a wake-up the worker could not verify
   * against its cached metadata, under the page's one-refresh-per-cooldown
   * allowance.
   */
  const drainPendingRecoveries = useCallback(async () => {
    for (const pending of await pendingRecoveries()) {
      if (!(await reserveRecovery(pending.origin, Date.now(), RECOVERY_COOLDOWN_MS))) {
        await clearPendingRecovery(pending.origin);
        continue; // within the cooldown: metadata was refreshed recently
      }
      await syncCompanyNow(pending.origin);
      await clearPendingRecovery(pending.origin);
    }
  }, [syncCompanyNow]);

  /**
   * A wake-up that arrived with no page open: the worker recorded the receipt
   * and the page catches up on the next open.
   */
  const catchUpOnWakeups = useCallback(async () => {
    for (const company of await getAllCompanies()) {
      if ((await lastPushAt(company.origin)) > (company.lastSyncAt ?? 0)) {
        await syncCompanyNow(company.origin);
      }
    }
  }, [syncCompanyNow]);

  useEffect(() => {
    initDebugBuild();
    void (async () => {
      const list = await getAllCompanies();
      // seed the in-memory item map from the persistent store: syncCompany
      // needs the cached items to detect unpublished (absent) items and to keep
      // their read state (spec/feeds.md §1.3)
      itemsRef.current = await getAllItems();
      setCompanies(list);
      setLoaded(true);
      await drainPendingRecoveries();
      await catchUpOnWakeups();
    })();
    void refreshNotificationState();
    void runRelayCheck();
    // the banner re-checks when the app returns to the foreground, so an
    // in-flight test that landed in the background upgrades to green, and the
    // page drains any wake-up the worker recorded
    const onVisible = () => {
      if (document.visibilityState !== 'visible') return;
      void refreshNotificationState();
      void runRelayCheck();
      void drainPendingRecoveries();
      void catchUpOnWakeups();
    };
    document.addEventListener('visibilitychange', onVisible);
    return () => document.removeEventListener('visibilitychange', onVisible);
  }, [refreshNotificationState, runRelayCheck, drainPendingRecoveries, catchUpOnWakeups]);

  // Android wake-ups arrive through the UnifiedPush connector: install the
  // native subscription source, register on every start, drain the native queue
  // and re-verify each payload with the full TUF state (the worker only gates)
  useEffect(() => {
    if (Capacitor.getPlatform() !== 'android') return;
    let listener: PluginListenerHandle | undefined;
    void (async () => {
      setSubscriptionSource(nativePushSource);
      const support = await pushSupport();
      if (!support.fcm) {
        // the UnifiedPush connector is the endpoint leg's source; FCM needs none
        const vapid = vapidPublicKey();
        if (vapid) {
          try {
            await KeryxPush.register({ vapid });
          } catch {
            // the probe keeps the no-transport state; polling remains the backstop
          }
        }
      }
      listener = await KeryxPush.addListener('push', ({ payload }) => {
        void handlePush(payload).then(async (outcome) => {
          if (outcome.accepted && !outcome.test) {
            await KeryxPush.showNotification({
              title: outcome.title ?? 'Keryx',
              body: outcome.body ?? 'New update available',
              tag: outcome.topic ? `keryx-${outcome.topic}` : undefined,
            });
          }
          await pushVerifyState();
          if (outcome.accepted) await catchUpOnWakeups();
        });
      });
      for (const payload of (await KeryxPush.drainMessages()).messages) {
        const outcome = await handlePush(payload);
        if (outcome.accepted && !outcome.test) {
          void KeryxPush.showNotification({
            title: outcome.title ?? 'Keryx',
            body: outcome.body ?? 'New update available',
            tag: outcome.topic ? `keryx-${outcome.topic}` : undefined,
          });
        }
      }
      await ensurePushWakeups(await getAllCompanies());
      await pushVerifyState();
      await catchUpOnWakeups();
    })();
    return () => {
      void listener?.remove();
      setSubscriptionSource(null);
    };
  }, [catchUpOnWakeups]);

  // while a self-test is in flight, re-read the app-wide state so a late
  // nonce upgrades it to green without user action
  useEffect(() => {
    if (notification.kind !== 'pending') return;
    const t = setInterval(() => void refreshNotificationState(), 3000);
    return () => clearInterval(t);
  }, [notification.kind, refreshNotificationState]);

  // the service worker tells us when a wake-up was accepted: the page owns
  // the content sync (and its recovery), so sync and re-read the store
  useEffect(() => {
    if (typeof navigator === 'undefined' || !('serviceWorker' in navigator)) return;
    const onMessage = (event: MessageEvent) => {
      if ((event.data as { type?: string } | null)?.type !== 'keryx-sync') return;
      const origin = (event.data as { origin?: string } | null)?.origin;
      if (origin) void syncCompanyNow(origin);
    };
    navigator.serviceWorker.addEventListener('message', onMessage);
    return () => navigator.serviceWorker.removeEventListener('message', onMessage);
  }, [syncCompanyNow]);

  const actions = useMemo<AppActions>(
    () => ({
      async refreshAll() {
        setSyncing(true);
        try {
          for (const company of companies) {
            const existing = new Map(itemsRef.current.filter((i) => i.origin === company.origin).map((i) => [i.id, i]));
            const outcome = await syncCompany(company, netFetch, existing);
            applyOutcome(company.origin, existing);
            await putCompany(outcome.company);
            if (outcome.toPut.length > 0) await putItems(outcome.toPut);
            if (outcome.toDelete.length > 0) await deleteItems(outcome.toDelete);
            void heartbeatRelay(outcome.company.origin);
          }
          const list = await getAllCompanies();
          setCompanies(list);
          // the app-wide state may have changed (a test landed, a leg died)
          await refreshNotificationState();
          await pushVerifyState();
        } finally {
          setSyncing(false);
        }
      },
      syncCompanyNow,
      async toggleChannel(origin, channel, followed) {
        const company = await getCompany(origin);
        if (!company) return;
        const channels = company.channels.map((c) =>
          c.name === channel ? { ...c, followed, isNew: false } : c,
        );
        await putCompany({ ...company, channels });
        // keep the relay registration's followed-topic union in step (§5.3)
        await ensurePushWakeups(await getAllCompanies());
        await pushVerifyState();
        setCompanies(await getAllCompanies());
      },
      async enableNotifications() {
        if (Capacitor.getPlatform() === 'android') {
          // the native permission is the source of truth (POST_NOTIFICATIONS);
          // the WebView's Notification API is not usable
          if (!(await requestNativeNotificationPermission())) {
            setNotification(await notificationState());
            return { endpoint: 'failed', leg: 'registration' };
          }
        } else {
          if (permissionState() === 'unsupported') return { endpoint: 'failed', leg: 'registration' };
          if ((await Notification.requestPermission()) !== 'granted') {
            setNotification(await notificationState());
            return { endpoint: 'failed', leg: 'registration' };
          }
        }
        sessionTestPending.current = true;
        const result = await runSelfTest(await getAllCompanies());
        await pushVerifyState();
        if (result.endpoint === 'failed') {
          sessionTestPending.current = false;
          setNotification({ kind: 'failed', leg: result.leg });
        } else {
          // delivered now or still in flight: the green tail when it lands
          await refreshNotificationState();
        }
        return result;
      },
      async checkNotifications() {
        setNotification(await notificationState());
      },
      async runNotificationSelfTest() {
        sessionTestPending.current = true;
        const result = await runSelfTest(await getAllCompanies());
        await pushVerifyState();
        if (result.endpoint === 'failed') {
          sessionTestPending.current = false;
          setNotification({ kind: 'failed', leg: result.leg });
        } else {
          await refreshNotificationState();
        }
        return result;
      },
      async setPrefs(origin, prefs) {
        const company = await getCompany(origin);
        if (!company) return;
        await putCompany({ ...company, prefs: { ...company.prefs, ...prefs } });
        setCompanies(await getAllCompanies());
      },
      async markRead(origin, feedKey, itemId, read) {
        await markRead(origin, feedKey, itemId, read);
        itemsRef.current = itemsRef.current.map((i) =>
          i.id === `${origin}\u0000${feedKey}\u0000${itemId}` ? { ...i, read } : i,
        );
      },
      async acknowledgeLogo(origin) {
        const company = await getCompany(origin);
        if (!company) return;
        const logo = company.targets.signed.custom?.logo;
        const logoSHA256 = company.targets.signed.custom?.logo_sha256;
        await putCompany({
          ...company,
          logoChangePending: false,
          identity: { ...company.identity, logo, logoSHA256 },
        });
        setCompanies(await getAllCompanies());
      },
      async removeCompany(origin) {
        await deleteCompany(origin);
        itemsRef.current = itemsRef.current.filter((i) => i.origin !== origin);
        setCompanies(await getAllCompanies());
        // drop the relay registration when no followed topic remains (§5.3)
        await ensurePushWakeups(await getAllCompanies());
        await pushVerifyState();
      },
      async saveCompany(company, newItems) {
        await putCompany(company);
        if (newItems && newItems.length > 0) {
          await putItems(newItems);
          const ids = new Set(newItems.map((i) => i.id));
          itemsRef.current = [...itemsRef.current.filter((i) => !ids.has(i.id)), ...newItems];
        }
        setCompanies(await getAllCompanies());
        await pushVerifyState();
      },
      async rePairCompany(origin, company, newItems) {
        // the identity snapshot is refreshed to the just-confirmed values
        const logo = company.targets.signed.custom?.logo;
        const logoSHA256 = company.targets.signed.custom?.logo_sha256;
        const name = company.targets.signed.custom?.company_name;
        await putCompany({
          ...company,
          origin,
          rebrandPending: false,
          logoChangePending: false,
          identity: { companyName: name, logo, logoSHA256 },
        });
        if (newItems && newItems.length > 0) {
          await putItems(newItems);
          const ids = new Set(newItems.map((i) => i.id));
          itemsRef.current = [...itemsRef.current.filter((i) => !ids.has(i.id)), ...newItems];
        }
        setCompanies(await getAllCompanies());
      },
    }),
    [companies, refreshNotificationState, syncCompanyNow],
  );

  const value = useMemo(
    () => ({ companies, loaded, syncing, notification, freshTest: freshTestAt !== null, actions }),
    [companies, loaded, syncing, notification, freshTestAt, actions],
  );

  return <AppContext.Provider value={value}>{children}</AppContext.Provider>;
}

export function useApp(): AppContextValue {
  const ctx = useContext(AppContext);
  if (!ctx) throw new Error('useApp outside AppProvider');
  return ctx;
}
