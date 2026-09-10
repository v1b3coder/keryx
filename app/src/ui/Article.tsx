/**
 * Article view: sanitized rich content in a sandboxed container (no scripts,
 * no forms), transparent links (real destination shown, no auto-open),
 * footer reminder that the channel never asks for credentials.
 */

import { useState } from 'react';
import { ArrowLeft, LockSimple } from '@phosphor-icons/react';
import { Capacitor } from '@capacitor/core';
import { Browser } from '@capacitor/browser';
import type { CompanyRecord, StoredItem } from '../lib/store';
import { formatDate } from '../lib/format';
import { loadImage } from '../lib/media';
import { CompanyLogo } from './CompanyLogo';
import { SanitizedHtml, LinkConfirm } from './SanitizedHtml';
import { useEffect } from 'react';

export function ArticleView({
  company,
  stored,
  onBack,
}: {
  company: CompanyRecord;
  stored: StoredItem;
  onBack: () => void;
}) {
  const [pendingLink, setPendingLink] = useState<string | null>(null);
  const [hero, setHero] = useState<string | null>(null);
  const item = stored.item;

  useEffect(() => {
    let alive = true;
    const url = item.image;
    if (!url || !company.prefs.loadRemoteMedia) {
      setHero(null);
      return;
    }
    void loadImage(url, company.origin, item._sig?.resources?.[url]).then((objectUrl) => {
      if (alive) setHero(objectUrl);
    });
    return () => {
      alive = false;
    };
  }, [item.image, item._sig, company.origin, company.prefs.loadRemoteMedia]);

  async function openUrl(url: string) {
    if (Capacitor.isNativePlatform()) {
      await Browser.open({ url });
    } else {
      window.open(url, '_blank', 'noopener,noreferrer');
    }
  }

  const channelName = stored.isPrivate
    ? company.privateFeeds.find((f) => f.url === stored.feedUrl)?.displayName ?? 'Delivery'
    : company.channels.find((c) => c.name === stored.channel)?.displayName ?? stored.channel;

  return (
    <div className="screen">
      <div className="appbar">
        <div className="appbar-inner">
          <button className="iconbtn" onClick={onBack} aria-label="Back">
            <ArrowLeft size={24} />
          </button>
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
        </div>
      </div>

      <article className="article">
        <h1>{item.title ?? 'Untitled'}</h1>
        <div className="article-meta">
          <span className="chip">{channelName}</span>
          {item.date_published && <span className="t-small">{formatDate(item.date_published)}</span>}
        </div>

        {hero && <img className="article-hero" src={hero} alt="" />}

        {item.content_html ? (
          <SanitizedHtml html={item.content_html} origin={company.origin} item={item} onLinkTap={setPendingLink} />
        ) : (
          <div className="article-body" style={{ whiteSpace: 'pre-wrap' }}>
            {item.content_text ?? ''}
          </div>
        )}

        <div className="footer-note">
          <LockSimple size={16} weight="fill" style={{ flexShrink: 0, marginTop: 2 }} />
          <span>
            This channel will never ask you for a password, seed, or code.
          </span>
        </div>
      </article>

      {pendingLink && (
        <LinkConfirm
          url={pendingLink}
          onConfirm={() => {
            void openUrl(pendingLink);
            setPendingLink(null);
          }}
          onCancel={() => setPendingLink(null)}
        />
      )}
    </div>
  );
}
