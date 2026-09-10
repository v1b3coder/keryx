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
import { getCompany, getItems, type CompanyRecord, type StoredItem } from './lib/store';

type View =
  | { t: 'start' }
  | { t: 'contacts' }
  | { t: 'company'; origin: string }
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

  if (view.t === 'company') {
    return (
      <CompanyRoute
        origin={view.origin}
        onBackToContacts={() => setView({ t: 'contacts' })}
        onRepair={(origin) => setView({ t: 'add', from: 'contacts', repairOrigin: origin })}
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
  onBackToContacts,
  onRepair,
}: {
  origin: string;
  onBackToContacts: () => void;
  onRepair: (origin: string) => void;
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

  return (
    <CompanyView company={company} items={companyItems} onBack={onBackToContacts} onRepair={onRepair} />
  );
}

export function Root() {
  return (
    <AppProvider>
      <App />
    </AppProvider>
  );
}
