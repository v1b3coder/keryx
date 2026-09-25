/**
 * App-wide notification status banner (design/notifications.md): red when
 * wake-ups are off and neutral while a test is in flight. A healthy install
 * shows no bar: the green "Notifications are working" is only the tail of an
 * enable/retry in this session, never a persistent status row.
 */
import type { NotificationState } from '../lib/notify';
import { openNtfyInstallPage } from '../lib/push';

export function NotificationBanner({
  state,
  freshTest,
  onEnable,
  onCheck,
  onRetry,
}: {
  state: NotificationState;
  /** a self-test completed in this session: show the green enable-workflow tail */
  freshTest: boolean;
  onEnable: () => void;
  onCheck: () => void;
  onRetry: () => void;
}) {
  if (state.kind === 'checking') return null;
  if (state.kind === 'no-transport') {
    return (
      <div className="banner banner-danger">
        <span>Notifications need ntfy on this device.</span>
        <button className="btn btn-secondary" onClick={onCheck}>
          Check again
        </button>
        <button className="btn btn-primary" onClick={() => void openNtfyInstallPage()}>
          Install ntfy
        </button>
      </div>
    );
  }
  if (state.kind === 'ntfy-ready') {
    return (
      <div className="banner banner-neutral">
        ntfy is ready on this device — notification setup continues in the next step.
      </div>
    );
  }
  if (state.kind === 'unsupported') {
    return (
      <div className="banner banner-neutral">
        Notifications are unavailable in this browser — messages still arrive by polling.
      </div>
    );
  }
  if (state.kind === 'pending') {
    return (
      <div className="banner banner-neutral">
        Notifications are on — the first test is still on its way.
      </div>
    );
  }
  if (state.kind === 'ok') {
    if (!freshTest) return null;
    return <div className="banner banner-ok">Notifications are working.</div>;
  }
  if (state.kind === 'default' || state.kind === 'no-subscription') {
    return (
      <div className="banner banner-danger">
        <span>Turn on notifications to get timely updates.</span>
        <button className="btn btn-primary" onClick={onEnable}>
          Turn on
        </button>
      </div>
    );
  }
  if (state.kind === 'unregistered') {
    return (
      <div className="banner banner-danger">
        <span>Notifications need to be re-enabled.</span>
        <button className="btn btn-primary" onClick={onEnable}>
          Re-subscribe
        </button>
      </div>
    );
  }
  const message =
    state.kind === 'denied'
      ? 'Notifications are off. Allow them in your browser or system settings, then check again.'
      : state.leg === 'registration'
        ? 'Notifications could not be registered. Try again.'
        : 'The test notification did not arrive. Try again.';
  return (
    <div className="banner banner-danger">
      <span>{message}</span>
      <button
        className="btn btn-primary"
        onClick={state.kind === 'failed' ? onRetry : onCheck}
      >
        {state.kind === 'failed' ? 'Try again' : 'Check again'}
      </button>
    </div>
  );
}
