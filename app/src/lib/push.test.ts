import { describe, it, expect } from 'vitest';
import { pushTransport } from './push';

describe('push transport selection', () => {
  it('prefers FCM when Google services are present', () => {
    expect(
      pushTransport({ fcm: true, unifiedPush: { available: true, distributors: ['io.heckel.ntfy'] } }),
    ).toBe('fcm');
  });

  it('falls back to UnifiedPush (ntfy) without Google services', () => {
    expect(
      pushTransport({ fcm: false, unifiedPush: { available: true, distributors: ['io.heckel.ntfy'] } }),
    ).toBe('unifiedpush');
  });

  it('reports none when neither is available', () => {
    expect(pushTransport({ fcm: false, unifiedPush: { available: false, distributors: [] } })).toBe(
      'none',
    );
  });
});
