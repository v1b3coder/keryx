/**
 * The build's short git commit, injected by CI (lib/build.ts). A one-line
 * footer stamp so a running install can be told apart from a stale one.
 */
import { appVersion } from '../lib/build';

export function BuildStamp() {
  return (
    <div className="t-small t-muted" style={{ textAlign: 'center', padding: '12px 0 4px' }}>
      Build {appVersion()}
    </div>
  );
}
