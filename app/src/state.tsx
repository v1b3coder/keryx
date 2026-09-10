/**
 * App state: companies + items from the local store, sync orchestration.
 * Everything is local — no account, no server-side state.
 */

import { createContext, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import {
  getAllCompanies,
  getItems,
  getCompany,
  putCompany,
  putItems,
  deleteItems,
  markRead,
  deleteCompany,
  type CompanyRecord,
  type StoredItem,
} from './lib/store';
import { syncCompany } from './lib/sync';

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
}

interface AppContextValue {
  companies: CompanyRecord[];
  loaded: boolean;
  syncing: boolean;
  actions: AppActions;
}

const AppContext = createContext<AppContextValue | null>(null);

export function AppProvider({ children }: { children: ReactNode }) {
  const [companies, setCompanies] = useState<CompanyRecord[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [syncing, setSyncing] = useState(false);
  const itemsRef = useRef<StoredItem[]>([]);

  useEffect(() => {
    void (async () => {
      const list = await getAllCompanies();
      setCompanies(list);
      setLoaded(true);
    })();
  }, []);

  const actions = useMemo<AppActions>(
    () => ({
      async refreshAll() {
        setSyncing(true);
        try {
          for (const company of companies) {
            const existing = new Map(itemsRef.current.filter((i) => i.origin === company.origin).map((i) => [i.id, i]));
            const outcome = await syncCompany(company, fetch, existing);
            await putCompany(outcome.company);
            if (outcome.toPut.length > 0) await putItems(outcome.toPut);
            if (outcome.toDelete.length > 0) await deleteItems(outcome.toDelete);
          }
          const list = await getAllCompanies();
          setCompanies(list);
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
          const outcome = await syncCompany(company, fetch, existing);
          await putCompany(outcome.company);
          if (outcome.toPut.length > 0) await putItems(outcome.toPut);
          if (outcome.toDelete.length > 0) await deleteItems(outcome.toDelete);
          const list = await getAllCompanies();
          setCompanies(list);
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
        setCompanies(await getAllCompanies());
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
        await putCompany({
          ...company,
          logoChangePending: false,
          identity: { ...company.identity, logo },
        });
        setCompanies(await getAllCompanies());
      },
      async removeCompany(origin) {
        await deleteCompany(origin);
        setCompanies(await getAllCompanies());
      },
      async saveCompany(company, newItems) {
        await putCompany(company);
        if (newItems && newItems.length > 0) await putItems(newItems);
        setCompanies(await getAllCompanies());
      },
      async rePairCompany(origin, company, newItems) {
        // the identity snapshot is refreshed to the just-confirmed values
        const logo = company.targets.signed.custom?.logo;
        const name = company.targets.signed.custom?.company_name;
        await putCompany({
          ...company,
          origin,
          rebrandPending: false,
          logoChangePending: false,
          identity: { companyName: name, logo },
        });
        if (newItems && newItems.length > 0) await putItems(newItems);
        setCompanies(await getAllCompanies());
      },
    }),
    [companies],
  );

  const value = useMemo(
    () => ({ companies, loaded, syncing, actions }),
    [companies, loaded, syncing, actions],
  );

  return <AppContext.Provider value={value}>{children}</AppContext.Provider>;
}

export function useApp(): AppContextValue {
  const ctx = useContext(AppContext);
  if (!ctx) throw new Error('useApp outside AppProvider');
  return ctx;
}
