/**
 * Company detail: branded sticky header (logo + name + origin), one-way
 * inbox with square previews, channel toggles + filter sheet, suspension
 * and rebranding states per spec/core.md §2, §4.
 */

import { useEffect, useMemo, useState } from 'react';
import { ArrowLeft, GearSix, ArrowClockwise, Trash, ShieldWarning } from '@phosphor-icons/react';
import type { CompanyRecord, StoredItem, ChannelState } from '../lib/store';
import { formatRelative, matchesFilter } from '../lib/format';
import { loadImage } from '../lib/media';
import { CompanyLogo } from './CompanyLogo';
import { useApp } from '../state';

export function CompanyView({
  company,
  items,
  onOpenItem,
  onBack,
  onRepair,
}: {
  company: CompanyRecord;
  items: StoredItem[];
  onOpenItem: (feedKey: string, itemId: string) => void;
  onBack: () => void;
  /** re-pair flow for a company_name change (scan a fresh QR) */
  onRepair: (origin: string) => void;
}) {
  const { actions, companies, syncing } = useApp();
  const [showSettings, setShowSettings] = useState(false);
  const [ackBanner, setAckBanner] = useState(false);

  const followed = new Set(company.channels.filter((c) => c.followed).map((c) => c.name));
  const visible = useMemo(() => {
    const list = items
      .filter((i) => {
        if (i.isPrivate) return true;
        if (!followed.has(i.channel)) return false;
        return matchesFilter(i.item, company.prefs);
      })
      .sort((a, b) => {
        const ta = Date.parse(a.published);
        const tb = Date.parse(b.published);
        if (!Number.isNaN(ta) && !Number.isNaN(tb)) return tb - ta;
        if (!Number.isNaN(ta)) return -1;
        if (!Number.isNaN(tb)) return 1;
        return (b.feedIndex ?? 0) - (a.feedIndex ?? 0);
      });
    return list;
  }, [items, followed, company.prefs]);

  const unread = visible.filter((i) => !i.read).length;
  const anyFollowed = followed.size > 0;

  // --- suspension (spec/core.md §4): warning + Remove only, no re-pair ----
  if (company.status === 'suspended') {
    return (
      <div className="screen screen-pad" style={{ paddingTop: 48 }}>
        <div className="alert alert-danger">
          <h3 style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
            <ShieldWarning size={22} /> Messages are not shown
          </h3>
          <p>
            This company's identity changed. This can mean the company's website or
            signing keys were compromised.
          </p>
          <p className="t-mono" style={{ fontSize: 12, marginTop: 8 }}>
            {company.origin}
          </p>
        </div>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 10, marginTop: 16 }}>
          <button className="btn btn-danger" onClick={() => void actions.removeCompany(company.origin)}>
            <Trash size={20} /> Remove company
          </button>
          <button className="btn btn-ghost" onClick={onBack}>
            Back
          </button>
        </div>
      </div>
    );
  }

  // --- rebranding (spec/core.md §2): company_name changed → re-pair required
  if (company.status === 'rebrand' || company.rebrandPending) {
    return (
      <div className="screen screen-pad" style={{ paddingTop: 48 }}>
        <div className="alert alert-danger">
          <h3>This company changed its name</h3>
          <p>
            A company's name can only change after you confirm it again. Scan a fresh QR
            code from the company to continue receiving its messages.
          </p>
          <p className="t-mono" style={{ fontSize: 12, marginTop: 8 }}>
            {company.origin}
          </p>
        </div>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 10, marginTop: 16 }}>
          <button className="btn btn-primary" onClick={() => onRepair(company.origin)}>
            Scan a new QR code
          </button>
          <button className="btn btn-danger" onClick={() => void actions.removeCompany(company.origin)}>
            <Trash size={20} /> Remove company
          </button>
        </div>
      </div>
    );
  }

  return (
    <div className="screen">
      <div className="appbar">
        <div className="appbar-inner">
          {companies.length > 1 && (
            <button className="iconbtn" onClick={onBack} aria-label="Back">
              <ArrowLeft size={24} />
            </button>
          )}
          <div className="companybar">
            <CompanyLogo
              url={company.targets.signed.custom?.logo}
              origin={company.origin}
              expectedSha={company.targets.signed.custom?.logo_sha256}
            />
            <div style={{ minWidth: 0 }}>
              <div className="companybar-name">
                {company.targets.signed.custom?.company_name ?? company.origin}
              </div>
              <div className="companybar-origin">{company.origin}</div>
            </div>
          </div>
          <button className="iconbtn" onClick={() => void actions.syncCompanyNow(company.origin)} aria-label="Refresh">
            <ArrowClockwise size={22} className={syncing ? 'spin' : ''} />
          </button>
          <button className="iconbtn" onClick={() => setShowSettings(true)} aria-label="Settings">
            <GearSix size={24} />
          </button>
        </div>
      </div>

      {company.logoChangePending && !ackBanner && (
        <div className="screen-pad" style={{ paddingTop: 12 }}>
          <div className="alert" style={{ display: 'flex', gap: 12, alignItems: 'center' }}>
            <div style={{ flex: 1 }}>
              <p style={{ color: 'var(--text)', margin: 0 }}>The company updated its logo.</p>
            </div>
            <button
              className="btn btn-secondary"
              style={{ minHeight: 40, padding: '0 16px', fontSize: 14 }}
              onClick={() => void actions.acknowledgeLogo(company.origin)}
            >
              Got it
            </button>
          </div>
        </div>
      )}

      {company.lastSyncErrors && company.lastSyncErrors.length > 0 && (
        <div className="screen-pad" style={{ paddingTop: 12 }}>
          <p className="t-small t-muted" style={{ margin: 0 }}>
            {company.lastSyncErrors[0]}
          </p>
        </div>
      )}

      <div className="feedlist" style={{ flex: 1 }}>
        {!anyFollowed && visible.length === 0 && (
          <div className="empty">
            <div className="t-section">No channels yet</div>
            <p className="t-small t-muted" style={{ maxWidth: 280 }}>
              Open settings to follow a channel from this company.
            </p>
          </div>
        )}
        {anyFollowed && visible.length === 0 && (
          <div className="empty">
            <div className="t-section">No messages yet</div>
            <p className="t-small t-muted" style={{ maxWidth: 280 }}>
              Messages appear here as soon as the company publishes.
            </p>
          </div>
        )}
        {visible.map((stored) => (
          <FeedRow
            key={stored.id}
            stored={stored}
            channelName={channelLabel(company, stored)}
            onOpen={() => {
              if (!stored.read) void actions.markRead(company.origin, feedKeyOf(stored), stored.item.id!, true);
              onOpenItem(feedKeyOf(stored), stored.item.id!);
            }}
          />
        ))}
      </div>

      {showSettings && (
        <SettingsSheet company={company} items={items} onClose={() => setShowSettings(false)} />
      )}
    </div>
  );
}

function feedKeyOf(stored: StoredItem): string {
  return stored.isPrivate ? `private:${stored.feedUrl}` : `public:${stored.channel}`;
}

function channelLabel(company: CompanyRecord, stored: StoredItem): string {
  if (stored.isPrivate) {
    const sub = company.privateFeeds.find((f) => f.url === stored.feedUrl);
    return sub?.displayName ?? 'Delivery';
  }
  return company.channels.find((c) => c.name === stored.channel)?.displayName ?? stored.channel;
}

function FeedRow({
  stored,
  channelName,
  onOpen,
}: {
  stored: StoredItem;
  channelName: string;
  onOpen: () => void;
}) {
  const [img, setImg] = useState<string | null>(null);
  useEffect(() => {
    let alive = true;
    const url = stored.item.image;
    if (!url) return;
    void loadImage(url, stored.origin).then((objectUrl) => {
      if (alive) setImg(objectUrl);
    });
    return () => {
      alive = false;
    };
  }, [stored.item.image, stored.origin]);

  return (
    <button className="feedrow" onClick={onOpen}>
      {img ? (
        <img className="feedrow-img" src={img} alt="" loading="lazy" />
      ) : (
        <div className="feedrow-img" aria-hidden />
      )}
      <div className="feedrow-body">
        <div className="feedrow-title">{stored.item.title ?? 'Untitled'}</div>
        {stored.item.summary && <div className="feedrow-summary">{stored.item.summary}</div>}
        <div className="feedrow-meta">
          <span className="chip">{channelName}</span>
          {stored.updated && <span className="chip chip-accent">Updated</span>}
          <span>{stored.published ? formatRelative(stored.published) : ''}</span>
        </div>
      </div>
    </button>
  );
}

function SettingsSheet({
  company,
  items,
  onClose,
}: {
  company: CompanyRecord;
  items: StoredItem[];
  onClose: () => void;
}) {
  const { actions } = useApp();
  const [languages, setLanguages] = useState<string[]>(company.prefs.languages);
  const [tags, setTags] = useState<string[]>(company.prefs.tags);
  const companyItems = items.filter((i) => i.origin === company.origin);

  const allLanguages = useMemo(
    () => [...new Set(companyItems.map((i) => i.item.language).filter((l): l is string => !!l))].sort(),
    [companyItems],
  );
  const allTags = useMemo(
    () => [...new Set(companyItems.flatMap((i) => i.item.tags ?? []))].sort(),
    [companyItems],
  );

  function toggleLanguage(lang: string) {
    const next = languages.includes(lang) ? languages.filter((l) => l !== lang) : [...languages, lang];
    setLanguages(next);
    void actions.setPrefs(company.origin, { languages: next });
  }

  function toggleTag(tag: string) {
    const next = tags.includes(tag) ? tags.filter((t) => t !== tag) : [...tags, tag];
    setTags(next);
    void actions.setPrefs(company.origin, { tags: next });
  }

  return (
    <div className="sheet-backdrop" onClick={onClose}>
      <div className="sheet" onClick={(e) => e.stopPropagation()}>
        <div className="t-section" style={{ marginBottom: 12 }}>
          Settings
        </div>

        <div className="t-small" style={{ fontWeight: 600, marginBottom: 4 }}>
          Channels
        </div>
        {company.channels.map((c) => (
          <ChannelToggle key={c.name} channel={c} onToggle={(followed) => void actions.toggleChannel(company.origin, c.name, followed)} />
        ))}

        {company.privateFeeds.length > 0 && (
          <>
            <div className="t-small" style={{ fontWeight: 600, margin: '16px 0 4px' }}>
              Orders
            </div>
            {company.privateFeeds.map((f) => (
              <div key={f.url} className="row" style={{ borderBottom: 'none' }}>
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div className="t-body" style={{ fontWeight: 600 }}>
                    {f.displayName ?? 'Delivery'}
                  </div>
                  <div className="t-small" style={{ wordBreak: 'break-all' }}>
                    {f.closed ? 'Finished' : f.expires ? `Open until ${f.expires.slice(0, 10)}` : 'Open'}
                  </div>
                </div>
              </div>
            ))}
          </>
        )}

        <div className="t-small" style={{ fontWeight: 600, margin: '16px 0 4px' }}>
          Language
        </div>
        {allLanguages.length === 0 ? (
          <p className="t-small t-muted" style={{ margin: 0 }}>
            No messages yet.
          </p>
        ) : (
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8 }}>
            {allLanguages.map((lang) => (
              <button
                key={lang}
                className={`chip ${languages.includes(lang) ? 'chip-accent' : ''}`}
                onClick={() => toggleLanguage(lang)}
              >
                {lang}
              </button>
            ))}
          </div>
        )}

        <div className="t-small" style={{ fontWeight: 600, margin: '16px 0 4px' }}>
          Tags
        </div>
        {allTags.length === 0 ? (
          <p className="t-small t-muted" style={{ margin: 0 }}>
            No messages yet.
          </p>
        ) : (
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8 }}>
            {allTags.map((tag) => (
              <button
                key={tag}
                className={`chip ${tags.includes(tag) ? 'chip-accent' : ''}`}
                onClick={() => toggleTag(tag)}
              >
                {tag}
              </button>
            ))}
          </div>
        )}

        <hr className="divider" style={{ margin: '20px 0' }} />
        <button
          className="btn btn-danger"
          onClick={() => {
            if (window.confirm(`Remove ${company.origin}? All saved messages are deleted from this device.`)) {
              void actions.removeCompany(company.origin);
            }
          }}
        >
          <Trash size={20} /> Remove company
        </button>
      </div>
    </div>
  );
}

function ChannelToggle({
  channel,
  onToggle,
}: {
  channel: ChannelState;
  onToggle: (followed: boolean) => void;
}) {
  return (
    <button className="row" onClick={() => onToggle(!channel.followed)}>
      <div style={{ flex: 1, minWidth: 0 }}>
        <div className="t-body" style={{ fontWeight: 600, display: 'flex', gap: 8, alignItems: 'center' }}>
          {channel.displayName}
          {channel.isNew && <span className="badge-new">New</span>}
        </div>
        {channel.description && <div className="t-small">{channel.description}</div>}
      </div>
      <div className={`toggle ${channel.followed ? 'toggle-on' : ''}`} aria-label={channel.displayName} />
    </button>
  );
}
