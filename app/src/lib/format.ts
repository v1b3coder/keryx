/**
 * Display helpers: dates, domains, and the local filter (language + tags —
 * stored locally, never sent; spec/feeds.md §1.1).
 */

import type { FeedItem } from './item';

export function formatDate(iso: string): string {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return '';
  return new Intl.DateTimeFormat(undefined, { day: 'numeric', month: 'short', year: 'numeric' }).format(new Date(t));
}

export function formatRelative(iso: string): string {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return '';
  const diff = Date.now() - t;
  const min = Math.floor(diff / 60000);
  if (min < 1) return 'just now';
  if (min < 60) return `${min}m ago`;
  const h = Math.floor(min / 60);
  if (h < 24) return `${h}h ago`;
  const d = Math.floor(h / 24);
  if (d < 7) return `${d}d ago`;
  return formatDate(iso);
}

/** The real destination domain of a link (shown transparently, spec/feeds.md §1.2). */
export function domainOf(url: string): string {
  try {
    return new URL(url).hostname.replace(/^www\./, '');
  } catch {
    return '';
  }
}

/** Absolute URL or empty — relative URLs inside content_html are not allowed by the spec. */
export function absolutize(url: string, base: string): string {
  try {
    return new URL(url, base).toString();
  } catch {
    return '';
  }
}

export interface FilterPrefs {
  languages: string[];
  tags: string[];
}

/** Local filtering: selected languages and/or tags; empty selection = everything. */
export function matchesFilter(item: FeedItem, prefs: FilterPrefs): boolean {
  if (prefs.languages.length > 0 && item.language && !prefs.languages.includes(item.language)) return false;
  if (prefs.tags.length > 0) {
    const tags = item.tags ?? [];
    if (!prefs.tags.some((t) => tags.includes(t))) return false;
  }
  return true;
}
