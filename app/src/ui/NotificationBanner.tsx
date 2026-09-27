/**
 * The app-wide notification state banner (design/notifications.md):
 * shown only when wake-ups need attention or while a test is in flight.
 * A healthy install shows nothing; the green tail is the enable flow's.
 */
import { Platform, StyleSheet, View } from 'react-native';
import type { NotificationState } from '../lib/notify';
import { openExternal, NTFY_INSTALL_URL } from '../lib/push';
import { Alert, Body, Button, Small } from './components';
import { spacing, usePalette } from '../theme';

/** The failing-leg wording: a leg that could not be set up is retryable. */
function failedMessage(leg: NotificationState['leg']): string {
  if (leg === 'topic') return 'Notifications could not be set up. Try again.';
  if (leg === 'registration') return 'Notifications are not set up on this device. Try again.';
  return 'Notifications are not reaching this device. Try again.';
}

export function NotificationBanner({
  state,
  freshTest,
  onEnable,
  onCheck,
  onRetry,
}: {
  state: NotificationState;
  freshTest: boolean;
  onEnable: () => void;
  onCheck: () => void;
  onRetry: () => void;
}) {
  const c = usePalette();

  if (state.kind === 'checking') return null;
  // the green tail replaces a healthy state only: a failure that appeared while
  // the tail is on screen is never hidden behind it
  if (state.kind === 'ok' && freshTest) {
    return (
      <View style={styles.wrap}>
        <Alert>
          <Body>Notifications are working.</Body>
        </Alert>
      </View>
    );
  }
  if (state.kind === 'ok') return null;
  if (state.kind === 'pending') {
    return (
      <View style={styles.wrap}>
        <Alert>
          <Small>
            {Platform.OS === 'web'
              ? 'Notifications are on. The first test is still on its way — it can take a minute.'
              : 'Notifications are on. The first notice is still on its way.'}
          </Small>
        </Alert>
      </View>
    );
  }
  if (state.kind === 'unsupported') {
    return (
      <View style={styles.wrap}>
        <Alert>
          <Small muted>
            Timely updates are unavailable on this device; the app checks for new messages when open.
          </Small>
        </Alert>
      </View>
    );
  }
  if (state.kind === 'no-transport') {
    return (
      <View style={styles.wrap}>
        <Alert danger>
          <Small>Notifications need ntfy on this device.</Small>
          <Button title="Install ntfy" onPress={() => void openExternal(NTFY_INSTALL_URL)} />
          <Button title="Check again" variant="secondary" onPress={onCheck} />
        </Alert>
      </View>
    );
  }

  // every non-ok state below needs attention: red like the reference client,
  // not informational
  const failed = state.kind === 'failed';
  const attention =
    state.kind === 'default' ||
    state.kind === 'denied' ||
    state.kind === 'no-subscription' ||
    state.kind === 'unregistered' ||
    failed;
  return (
    <View style={styles.wrap}>
      <Alert danger={attention}>
        <Small>
          {state.kind === 'default'
            ? 'Turn on notifications for timely updates.'
            : state.kind === 'denied'
              ? 'Notifications are off. Allow them in your system settings.'
              : state.kind === 'no-subscription'
                ? 'Notifications are not set up on this device.'
                : state.kind === 'unregistered'
                  ? 'Notifications need to be refreshed.'
                  : failedMessage(state.leg)}
        </Small>
        {state.kind === 'default' ? (
          <Button title="Turn on" onPress={onEnable} />
        ) : state.kind === 'no-subscription' || state.kind === 'unregistered' ? (
          // a missing or out-of-sync registration is recoverable: re-enable
          // re-registers and re-subscribes this install
          <Button title="Re-subscribe" onPress={onEnable} />
        ) : state.kind === 'failed' ? (
          <Button title="Try again" onPress={onRetry} />
        ) : (
          <Button title="Check again" onPress={onCheck} />
        )}
      </Alert>
    </View>
  );
}

const styles = StyleSheet.create({
  wrap: { paddingVertical: spacing(1) },
});
