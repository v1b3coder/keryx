import { describe, it, expect } from 'vitest';
import { pushTransport } from './push';

describe('push transport selection', () => {
  it('uses the browser PushManager on the web/PWA', () => {
    expect(
      pushTransport({ webpush: true, fcm: false, unifiedPush: { available: false, distributors: [] }, expo: false }),
    ).toBe('webpush');
  });

  it('prefers FCM when Google services are present', () => {
    expect(
      pushTransport({ webpush: false, fcm: true, unifiedPush: { available: true, distributors: ['io.heckel.ntfy'] }, expo: false }),
    ).toBe('fcm');
  });

  it('falls back to UnifiedPush (ntfy) without Google services', () => {
    expect(
      pushTransport({ webpush: false, fcm: false, unifiedPush: { available: true, distributors: ['io.heckel.ntfy'] }, expo: false }),
    ).toBe('unifiedpush');
  });

  it('uses Expo local notifications on iOS (no relay leg)', () => {
    expect(
      pushTransport({ webpush: false, fcm: false, unifiedPush: { available: false, distributors: [] }, expo: true }),
    ).toBe('expo');
  });

  it('reports none when neither is available', () => {
    expect(
      pushTransport({ webpush: false, fcm: false, unifiedPush: { available: false, distributors: [] }, expo: false }),
    ).toBe('none');
  });
});
