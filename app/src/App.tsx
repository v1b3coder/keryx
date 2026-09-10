/**
 * Root: zero-onboarding first screen (scan a company QR code), contacts
 * list when more than one company is added, straight to the company
 * otherwise (single-source shortcut, per the product brief).
 */

import { useEffect, useState } from 'react';
import { AppProvider, useApp } from './state';
import { AddCompany } from './ui/AddCompany';
import { Contacts } from './ui/Contacts';
import { CompanyView } from './ui/Company';
import { ArticleView } from './ui/Article';
import { getCompany, getItems, type CompanyRecord, type StoredItem } from './lib/store';

type View =
  | { t: 'start' }
  | { t: 'contacts' }
  | { t: 'company'; origin: string }
  | { t: 'article'; origin: string; feedKey: string; itemId: string }
  | { t: 'add'; from: 'start' | 'contacts'; repairOrigin?: string };

export default function App() {
  const { companies, loaded } = useApp();
  const [view, setView] = useState<View>({ t: 'start' });

  // initial routing: single company → straight to it; none → start; else contacts
  useEffect(() => {
    if (!loaded) return;
    if (view.t !== 'start' && view.t !== 'contacts') return;
    if (companies.length === 1) {
      setView({ t: 'company', origin: companies[0].origin });
    } else if (companies.length > 1) {
      setView({ t: 'contacts' });
    } else {
      setView({ t: 'start' });
    }
  }, [loaded, companies.length]);

  if (!loaded) {
    return (
      <div className="screen">
        <div className="empty">
          <div className="spinner" />
        </div>
      </div>
    );
  }

  if (view.t === 'add') {
    return (
      <AddCompany
        repairOrigin={view.repairOrigin}
        onDone={(origin) => setView({ t: 'company', origin })}
        onCancel={() => {
          if (view.repairOrigin) setView({ t: 'company', origin: view.repairOrigin });
          else if (view.from === 'contacts' || companies.length > 1) setView({ t: 'contacts' });
          else setView({ t: 'start' });
        }}
      />
    );
  }

  if (view.t === 'company' || view.t === 'article') {
    return (
      <CompanyRoute
        origin={view.origin}
        view={view}
        onBackToContacts={() => setView({ t: 'contacts' })}
        onBackToCompany={(origin) => setView({ t: 'company', origin })}
        onRepair={(origin) => setView({ t: 'add', from: 'contacts', repairOrigin: origin })}
        onOpenItem={(feedKey, itemId) => setView({ t: 'article', origin: view.origin, feedKey, itemId })}
      />
    );
  }

  // start: zero companies
  return (
    <div className="screen screen-pad" style={{ paddingTop: 48 }}>
      <div className="empty" style={{ alignItems: 'stretch', textAlign: 'left', paddingTop: 0 }}>
        <h1 className="t-title" style={{ margin: '0 0 8px' }}>
          Company messages
        </h1>
        <p className="t-body t-muted" style={{ margin: '0 0 24px' }}>
          Companies you follow publish here. Messages are verified, so nothing can be
          faked.
        </p>
        <button className="btn btn-primary" onClick={() => setView({ t: 'add', from: 'start' })}>
          Add a company
        </button>
      </div>
    </div>
  );
}

function CompanyRoute({
  origin,
  view,
  onBackToContacts,
  onBackToCompany,
  onRepair,
  onOpenItem,
}: {
  origin: string;
  view: View;
  onBackToContacts: () => void;
  onBackToCompany: (origin: string) => void;
  onRepair: (origin: string) => void;
  onOpenItem: (feedKey: string, itemId: string) => void;
}) {
  const { companies } = useApp();
  const [company, setCompany] = useState<CompanyRecord | null>(null);
  const [companyItems, setCompanyItems] = useState<StoredItem[]>([]);

  useEffect(() => {
    let alive = true;
    void (async () => {
      const c = await getCompany(origin);
      if (alive && c) setCompany(c);
      const list = await getItems(origin);
      if (alive) setCompanyItems(list);
    })();
    return () => {
      alive = false;
    };
  }, [origin, companies]);

  if (!company) {
    return (
      <div className="screen">
        <div className="empty">
          <div className="spinner" />
        </div>
      </div>
    );
  }

  if (view.t === 'article') {
    const stored = companyItems.find(
      (i) => i.id === `${company.origin}\u0000${view.feedKey}\u0000${view.itemId}`,
    );
    if (stored) {
      return (
        <ArticleView company={company} stored={stored} onBack={() => onBackToCompany(origin)} />
      );
    }
  }

  return (
    <CompanyView
      company={company}
      items={companyItems}
      onOpenItem={onOpenItem}
      onBack={onBackToContacts}
      onRepair={onRepair}
    />
  );
}

export function Root() {
  return (
    <AppProvider>
      <App />
    </AppProvider>
  );
}
