/**
 * The app-wide notification state banner (design/notifications.md):
 * shown only when wake-ups need attention or while a test is in flight.
 * A healthy install shows nothing; the green tail is the enable flow's.
 */
import { Platform, StyleSheet, View } from 'react-native';
import type { NotificationState } from '../lib/notify';
import { Alert, Body, Button, Small } from './components';
import { spacing, usePalette } from '../theme';

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

  if (freshTest) {
    return (
      <View style={styles.wrap}>
        <Alert>
          <Body>Notifications are working.</Body>
        </Alert>
      </View>
    );
  }
  if (state.kind === 'checking' || state.kind === 'ok') return null;
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
          <Small>Notifications are unavailable on this device; the app checks when open.</Small>
        </Alert>
      </View>
    );
  }

  const failed = state.kind === 'failed';
  return (
    <View style={styles.wrap}>
      <Alert danger={failed}>
        <Small>
          {state.kind === 'default'
            ? 'Turn on notifications for timely updates.'
            : state.kind === 'denied'
              ? 'Notifications are off. Allow them in your system settings.'
              : state.kind === 'no-subscription'
                ? 'Notifications are not set up on this device.'
                : state.kind === 'unregistered'
                  ? 'Notifications need to be refreshed.'
                  : `Notifications failed (${state.leg ?? 'unknown'}).`}
        </Small>
        {state.kind === 'default' || state.kind === 'no-subscription' ? (
          <Button title="Turn on" onPress={onEnable} />
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
