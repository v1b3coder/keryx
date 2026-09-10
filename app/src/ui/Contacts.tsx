/**
 * Contacts list (shown only when more than one company is added — with a
 * single company the app opens it directly). Each card: logo, name, the
 * join origin as a persistent secondary line, unread count.
 */

import { Plus } from '@phosphor-icons/react';
import type { CompanyRecord, StoredItem } from '../lib/store';
import { CompanyLogo } from './CompanyLogo';

export function Contacts({
  companies,
  items,
  onOpen,
  onAdd,
}: {
  companies: CompanyRecord[];
  items: StoredItem[];
  onOpen: (origin: string) => void;
  onAdd: () => void;
}) {
  return (
    <div className="screen">
      <div className="appbar">
        <div className="appbar-inner">
          <div className="t-section" style={{ flex: 1 }}>
            Messages
          </div>
          <button className="iconbtn" onClick={onAdd} aria-label="Add company">
            <Plus size={24} weight="bold" />
          </button>
        </div>
      </div>
      <div className="screen-pad" style={{ paddingTop: 8 }}>
        {companies.map((company) => {
          const unread = items.filter((i) => i.origin === company.origin && !i.read).length;
          return (
            <button key={company.origin} className="row" onClick={() => onOpen(company.origin)}>
              <CompanyLogo
                url={company.targets.signed.custom?.logo}
                origin={company.origin}
                expectedSha={company.targets.signed.custom?.logo_sha256}
                className="row-logo"
              />
              <div style={{ flex: 1, minWidth: 0 }}>
                <div className="t-body" style={{ fontWeight: 600 }}>
                  {company.targets.signed.custom?.company_name ?? company.origin}
                </div>
                <div className="t-mono" style={{ fontSize: 12, color: 'var(--text-2)' }}>
                  {company.origin}
                </div>
              </div>
              {unread > 0 && <span className="unread">{unread > 99 ? '99+' : unread}</span>}
            </button>
          );
        })}
      </div>
    </div>
  );
}
