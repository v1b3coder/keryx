/**
 * Sandboxed rich content: DOMPurify-sanitized HTML (no scripts, no forms,
 * no iframes/embeds — the "channel never asks for a password, seed, or code"
 * promise is structural), links intercepted with their real destination
 * domain shown, media hash-verified against `_sig.resources` when present.
 */

import { useEffect, useRef, useState } from 'react';
import DOMPurify from 'dompurify';
import { domainOf } from '../lib/format';
import { loadImage } from '../lib/media';
import type { FeedItem } from '../lib/item';

const ALLOWED_TAGS = [
  'p', 'br', 'strong', 'em', 'b', 'i', 'u', 's', 'sub', 'sup', 'a', 'ul', 'ol', 'li',
  'h1', 'h2', 'h3', 'h4', 'blockquote', 'code', 'pre', 'img', 'figure', 'figcaption',
  'table', 'thead', 'tbody', 'tr', 'th', 'td', 'span', 'div', 'hr', 'small',
];

const ALLOWED_ATTR = ['href', 'src', 'alt', 'title', 'target', 'rel'];

export function sanitizeHtml(html: string): string {
  return DOMPurify.sanitize(html, {
    ALLOWED_TAGS,
    ALLOWED_ATTR,
    ALLOW_DATA_ATTR: false,
    FORBID_TAGS: ['script', 'style', 'iframe', 'form', 'input', 'button', 'object', 'embed', 'link', 'meta', 'svg', 'math'],
  });
}

/** Appends the real destination domain to external links (transparency). */
export function annotateLinks(container: HTMLElement): void {
  for (const a of Array.from(container.querySelectorAll('a[href]'))) {
    const href = a.getAttribute('href') ?? '';
    const domain = domainOf(href);
    if (!domain) continue;
    a.setAttribute('rel', 'noopener noreferrer');
    a.setAttribute('target', '_blank');
    a.setAttribute('title', `Opens ${domain}`);
  }
}

export function SanitizedHtml({
  html,
  origin,
  item,
  onLinkTap,
}: {
  html: string;
  origin: string;
  item: FeedItem;
  onLinkTap: (url: string) => void;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const [dom, setDom] = useState<string>(() => sanitizeHtml(html));

  useEffect(() => {
    setDom(sanitizeHtml(html));
  }, [html]);

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    annotateLinks(el);

    // media: verify `_sig.resources` hashes before rendering; everything
    // else is an ordinary (mutable-by-design) web resource.
    for (const img of Array.from(el.querySelectorAll('img[src]'))) {
      const src = img.getAttribute('src') ?? '';
      const want = item._sig?.resources?.[src];
      if (want) {
        img.removeAttribute('src');
        void loadImage(src, origin, want).then((objectUrl) => {
          if (objectUrl) img.setAttribute('src', objectUrl);
          else img.removeAttribute('src'); // resource unavailable — item unaffected
        });
      }
    }

    // link interception: no auto-open, real domain shown first
    const onClick = (e: MouseEvent) => {
      const a = (e.target as HTMLElement).closest('a[href]');
      if (!a) return;
      e.preventDefault();
      e.stopPropagation();
      const href = a.getAttribute('href') ?? '';
      if (href.startsWith('http://') || href.startsWith('https://')) onLinkTap(href);
    };
    el.addEventListener('click', onClick);
    return () => el.removeEventListener('click', onClick);
  }, [dom, origin, item, onLinkTap]);

  return (
    <div
      ref={ref}
      className="article-body"
      // eslint-disable-next-line react/no-danger
      dangerouslySetInnerHTML={{ __html: dom }}
    />
  );
}

/** Open a link after showing its real destination (no auto-open). */
export function LinkConfirm({ url, onConfirm, onCancel }: { url: string; onConfirm: () => void; onCancel: () => void }) {
  const domain = domainOf(url);
  return (
    <div className="sheet-backdrop" onClick={onCancel}>
      <div className="sheet" onClick={(e) => e.stopPropagation()}>
        <div className="t-section" style={{ marginBottom: 4 }}>
          Open external link?
        </div>
        <p className="t-small" style={{ marginTop: 0, wordBreak: 'break-all' }}>
          This link goes to <span className="t-mono">{domain}</span>. It is not part of
          the company's signed message.
        </p>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 10, marginTop: 12 }}>
          <button className="btn btn-primary" onClick={onConfirm}>
            Open {domain}
          </button>
          <button className="btn btn-secondary" onClick={onCancel}>
            Cancel
          </button>
        </div>
      </div>
    </div>
  );
}
