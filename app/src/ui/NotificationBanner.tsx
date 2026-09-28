/**
 * The app-wide notification state banner (design/notifications.md):
 * shown only when wake-ups need attention or while a test is in flight.
 * A healthy install shows nothing; the green tail is the enable flow's.
 *
 * Layout follows the reference `.banner`: a single row, the text taking the
 * free space and the actions inline, not a stacked alert card.
 */
import { Pressable, StyleSheet, Text, View } from 'react-native';
import type { NotificationState } from '../lib/notify';
import { openExternal, NTFY_INSTALL_URL } from '../lib/push';
import { radius, spacing, type, usePalette } from '../theme';

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
    return <Banner tone={c.okSoft} text="Notifications are working." />;
  }
  if (state.kind === 'ok') return null;
  if (state.kind === 'pending') {
    return (
      <Banner
        tone={c.surface2}
        text={
          'Notifications are on. The first test is still on its way — it can take a minute.'
        }
      />
    );
  }
  if (state.kind === 'unsupported') {
    return (
      <Banner
        tone={c.surface2}
        muted
        text="Timely updates are unavailable on this device; the app checks for new messages when open."
      />
    );
  }
  if (state.kind === 'no-transport') {
    return (
      <Banner
        tone={c.dangerSoft}
        text="Notifications need ntfy on this device."
        actions={[
          { label: 'Install ntfy', onPress: () => void openExternal(NTFY_INSTALL_URL) },
          { label: 'Check again', onPress: onCheck, secondary: true },
        ]}
      />
    );
  }

  const failed = state.kind === 'failed';
  return (
    <Banner
      tone={failed ? c.dangerSoft : c.dangerSoft}
      text={
        state.kind === 'default'
          ? 'Turn on notifications for timely updates.'
          : state.kind === 'denied'
            ? 'Notifications are off. Allow them in your system settings.'
            : state.kind === 'no-subscription'
              ? 'Notifications are not set up on this device.'
              : state.kind === 'unregistered'
                ? 'Notifications need to be refreshed.'
                : failedMessage(state.leg)
      }
      actions={
        state.kind === 'default'
          ? [{ label: 'Turn on', onPress: onEnable }]
          : state.kind === 'no-subscription' || state.kind === 'unregistered'
            ? [{ label: 'Re-subscribe', onPress: onEnable }]
            : state.kind === 'failed'
              ? [{ label: 'Try again', onPress: onRetry }]
              : [{ label: 'Check again', onPress: onCheck }]
      }
    />
  );
}

/**
 * One banner row: the reference `.banner` is a single flex row, so the text
 * keeps `flex: 1` and the action buttons stay at their intrinsic width.
 */
function Banner({
  tone,
  muted,
  text,
  actions,
}: {
  tone: string;
  muted?: boolean;
  text: string;
  actions?: { label: string; onPress: () => void; secondary?: boolean }[];
}) {
  const c = usePalette();
  return (
    <View style={[styles.banner, { backgroundColor: tone }]}>
      <Text style={[styles.text, { color: muted ? c.text2 : c.text }]}>{text}</Text>
      {actions?.map((action) => (
        <Pressable
          key={action.label}
          accessibilityRole="button"
          accessibilityLabel={action.label}
          onPress={action.onPress}
          style={({ pressed }) => [
            styles.action,
            action.secondary
              ? { backgroundColor: c.surface2, borderColor: c.border }
              : { backgroundColor: c.accent },
            pressed ? styles.pressed : null,
          ]}
        >
          <Text style={[styles.actionLabel, { color: action.secondary ? c.text : c.onAccent }]}>
            {action.label}
          </Text>
        </Pressable>
      ))}
    </View>
  );
}

const styles = StyleSheet.create({
  banner: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: spacing(1.5),
    borderRadius: radius.card,
    paddingVertical: spacing(1.5),
    paddingHorizontal: spacing(2),
  },
  text: { flex: 1, fontSize: 14, lineHeight: 20 },
  action: {
    minHeight: 36,
    borderRadius: radius.pill,
    borderWidth: 1,
    alignItems: 'center',
    justifyContent: 'center',
    paddingHorizontal: spacing(2),
    flexShrink: 0,
  },
  actionLabel: { fontSize: 14, fontWeight: '600' },
  pressed: { transform: [{ scale: 0.98 }] },
});
