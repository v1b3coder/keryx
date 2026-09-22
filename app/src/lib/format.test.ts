/**
 * Display helper tests: the date shown in an article, and the exact time the
 * same date reveals when tapped.
 */
import { describe, it, expect } from 'vitest';
import { formatDate, formatDateTime } from './format';

describe('date formatting', () => {
  it('formats a date, and the same date with the time on request', () => {
    const iso = '2026-09-22T14:32:00Z';
    const date = formatDate(iso);
    const dateTime = formatDateTime(iso);
    expect(date).not.toBe('');
    expect(dateTime).not.toBe('');
    // the time variant adds hour and minute on top of the same date
    expect(dateTime).not.toBe(date);
  });

  it('returns empty for an unparseable date', () => {
    expect(formatDate('nope')).toBe('');
    expect(formatDateTime('nope')).toBe('');
  });
});
