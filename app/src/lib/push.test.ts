import { describe, it, expect } from 'vitest';
import { pushTransport } from './push';

describe('push transport selection', () => {
  it('uses the browser PushManager on the web/PWA', () => {
    expect(pushTransport({ webpush: true, expo: false })).toBe('webpush');
  });

  it('uses Expo local notifications on iOS/Android', () => {
    expect(pushTransport({ webpush: false, expo: true })).toBe('expo');
  });

  it('reports none when neither is available', () => {
    expect(pushTransport({ webpush: false, expo: false })).toBe('none');
  });
});
