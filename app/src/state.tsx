/**
 * App state: companies + items from the local store, sync orchestration.
 * Everything is local — no account, no server-side state.
 */

import { createContext, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import {
  getAllCompanies,
  getAllItems,
  getCompany,
  putCompany,
  putItems,
  deleteItems,
  markRead,
  deleteCompany,
  type CompanyRecord,
  type StoredItem,
} from './lib/store';
import { syncCompany, applyOutcomeItems } from './lib/sync';
import { ensureRelayRegistration, heartbeatRelay } from './lib/relay-sw';
import { notificationState, permissionState, runSelfTest, type NotificationState, type SelfTestResult } from './lib/notify';
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
  actions: AppActions;
}

const AppContext = createContext<AppContextValue | null>(null);

/** App-wide fetch: cross-origin redirects are blocked (spec/core.md §1.2). */
const netFetch = safeFetch();

export function AppProvider({ children }: { children: ReactNode }) {
  const [companies, setCompanies] = useState<CompanyRecord[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [syncing, setSyncing] = useState(false);
  const [notification, setNotification] = useState<NotificationState>({ kind: 'unsupported' });
  const itemsRef = useRef<StoredItem[]>([]);

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
    })();
    void notificationState().then(setNotification);
    // the banner re-checks when the app returns to the foreground, so an
    // in-flight test that landed in the background upgrades to green
    const onVisible = () => {
      if (document.visibilityState === 'visible') void notificationState().then(setNotification);
    };
    document.addEventListener('visibilitychange', onVisible);
    return () => document.removeEventListener('visibilitychange', onVisible);
  }, []);

  /** Replace the in-memory items of one origin with the post-sync state. */
  const applyOutcome = (origin: string, existing: Map<string, StoredItem>) => {
    itemsRef.current = applyOutcomeItems(itemsRef.current, origin, existing);
  };

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
          setNotification(await notificationState());
        } finally {
          setSyncing(false);
        }
      },
      async syncCompanyNow(origin) {
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
          setNotification(await notificationState());
        } finally {
          setSyncing(false);
        }
      },
      async toggleChannel(origin, channel, followed) {
        const company = await getCompany(origin);
        if (!company) return;
        const channels = company.channels.map((c) =>
          c.name === channel ? { ...c, followed, isNew: false } : c,
        );
        await putCompany({ ...company, channels });
        // keep the relay registration's followed-topic union in step (§5.3)
        await ensureRelayRegistration(await getAllCompanies());
        setCompanies(await getAllCompanies());
      },
      async enableNotifications() {
        if (permissionState() === 'unsupported') return { endpoint: 'failed', leg: 'registration' };
        if ((await Notification.requestPermission()) !== 'granted') {
          setNotification(await notificationState());
          return { endpoint: 'failed', leg: 'registration' };
        }
        const result = await runSelfTest(await getAllCompanies());
        setNotification(result.endpoint === 'failed' ? { kind: 'failed', leg: result.leg } : await notificationState());
        return result;
      },
      async checkNotifications() {
        setNotification(await notificationState());
      },
      async runNotificationSelfTest() {
        const result = await runSelfTest(await getAllCompanies());
        setNotification(result.endpoint === 'failed' ? { kind: 'failed', leg: result.leg } : await notificationState());
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
        await ensureRelayRegistration(await getAllCompanies());
      },
      async saveCompany(company, newItems) {
        await putCompany(company);
        if (newItems && newItems.length > 0) {
          await putItems(newItems);
          const ids = new Set(newItems.map((i) => i.id));
          itemsRef.current = [...itemsRef.current.filter((i) => !ids.has(i.id)), ...newItems];
        }
        setCompanies(await getAllCompanies());
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
    [companies],
  );

  const value = useMemo(
    () => ({ companies, loaded, syncing, notification, actions }),
    [companies, loaded, syncing, notification, actions],
  );

  return <AppContext.Provider value={value}>{children}</AppContext.Provider>;
}

export function useApp(): AppContextValue {
  const ctx = useContext(AppContext);
  if (!ctx) throw new Error('useApp outside AppProvider');
  return ctx;
}
