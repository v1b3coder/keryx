/**
 * App-wide notification status banner (design/notifications.md): red when
 * wake-ups are off, neutral while testing, green on success. The green state
 * auto-dismisses into the app-wide card.
 */
import { useEffect, useState } from 'react';
import type { NotificationState } from '../lib/notify';

export function NotificationBanner({
  state,
  onEnable,
  onCheck,
  onRetry,
}: {
  state: NotificationState;
  onEnable: () => void;
  onCheck: () => void;
  onRetry: () => void;
}) {
  const [dismissed, setDismissed] = useState(false);
  const ok = state.kind === 'ok';
  useEffect(() => {
    if (!ok) {
      setDismissed(false);
      return;
    }
    const t = setTimeout(() => setDismissed(true), 6000);
    return () => clearTimeout(t);
  }, [ok, state.testedAt]);

  if (state.kind === 'unsupported') {
    return (
      <div className="banner banner-neutral">
        Wake-ups are unavailable in this browser — messages still arrive by polling.
      </div>
    );
  }
  if (state.kind === 'pending') {
    return (
      <div className="banner banner-neutral">
        Wake-ups are on — the first test is still on its way.
      </div>
    );
  }
  if (state.kind === 'ok') {
    if (dismissed) {
      return (
        <div className="banner banner-subtle">
          Wake-ups on{state.testedAt ? ` · tested ${new Date(state.testedAt).toLocaleTimeString()}` : ''}
        </div>
      );
    }
    return <div className="banner banner-ok">Notifications are working.</div>;
  }
  const message =
    state.kind === 'denied'
      ? 'Notifications are off. Allow them in your browser or system settings, then check again.'
      : state.kind === 'default'
        ? 'Turn on notifications to get timely updates.'
        : state.kind === 'failed'
          ? state.leg === 'registration'
            ? 'Wake-ups could not be registered. Try again.'
            : 'The test notification did not arrive. Try again.'
          : 'Wake-ups need to be re-enabled.';
  return (
    <div className="banner banner-danger">
      <span>{message}</span>
      <button
        className="btn btn-primary"
        onClick={state.kind === 'default' ? onEnable : state.kind === 'failed' ? onRetry : onCheck}
      >
        {state.kind === 'default' ? 'Turn on' : state.kind === 'failed' ? 'Try again' : 'Check again'}
      </button>
    </div>
  );
}
