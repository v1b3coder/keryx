/**
 * Link interception (spec/feeds.md §1.4): no auto-open; the real
 * destination domain is shown before opening. The link is never part of the
 * company's signed message, so the user confirms it.
 */
import { Pressable, StyleSheet, Text, View } from 'react-native';
import { domainOf } from '../lib/format';
import { Mono, Screen, Small, Title } from './components';
import { radius, spacing, type, usePalette } from '../theme';

export function LinkConfirm({
  url,
  onConfirm,
  onCancel,
}: {
  url: string;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  const domain = domainOf(url);
  const c = usePalette();
  return (
    <Screen>
      <Pressable style={styles.backdrop} accessibilityLabel="Cancel" onPress={onCancel}>
        <Pressable
          style={[styles.sheet, { backgroundColor: c.surface }]}
          onPress={(e) => e.stopPropagation()}
        >
          <Title>Open external link?</Title>
          <Small muted>
            This link goes to <Text style={styles.domain}>{domain}</Text>. It is not part of the
            company's signed message.
          </Small>
          <Pressable
            accessibilityRole="button"
            accessibilityLabel={`Open ${domain}`}
            onPress={onConfirm}
            style={({ pressed }) => [
              styles.primary,
              { backgroundColor: c.accent },
              pressed ? styles.pressed : null,
            ]}
          >
            <Text style={[styles.primaryLabel, { color: c.onAccent }]}>Open {domain}</Text>
          </Pressable>
          <Pressable
            accessibilityRole="button"
            accessibilityLabel="Cancel"
            onPress={onCancel}
            style={({ pressed }) => [
              styles.secondary,
              { backgroundColor: c.surface2, borderColor: c.border },
              pressed ? styles.pressed : null,
            ]}
          >
            <Text style={[styles.primaryLabel, { color: c.text }]}>Cancel</Text>
          </Pressable>
        </Pressable>
      </Pressable>
    </Screen>
  );
}

const styles = StyleSheet.create({
  backdrop: { flex: 1, justifyContent: 'flex-end', backgroundColor: 'rgba(0,0,0,0.35)' },
  sheet: {
    borderTopLeftRadius: 20,
    borderTopRightRadius: 20,
    padding: spacing(2.5),
    paddingBottom: spacing(4),
    gap: spacing(1.5),
  },
  domain: { fontFamily: 'monospace', fontSize: type.small },
  primary: {
    minHeight: 50,
    borderRadius: radius.pill,
    alignItems: 'center',
    justifyContent: 'center',
  },
  secondary: {
    minHeight: 50,
    borderRadius: radius.pill,
    borderWidth: 1,
    alignItems: 'center',
    justifyContent: 'center',
  },
  primaryLabel: { fontSize: type.body, fontWeight: '600' },
  pressed: { transform: [{ scale: 0.98 }] },
});
