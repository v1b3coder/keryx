/**
 * Shared React Native primitives for the app screens: pill buttons,
 * cards, rows and chips from the design tokens (theme.ts).
 */
import type { ReactNode } from 'react';
import {
  ActivityIndicator,
  Pressable,
  StyleSheet,
  Text,
  View,
  type StyleProp,
  type TextStyle,
  type ViewStyle,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { radius, spacing, type, usePalette } from '../theme';

export function Screen({ children }: { children: ReactNode }) {
  const c = usePalette();
  return (
    <SafeAreaView style={[styles.screen, { backgroundColor: c.bg }]} edges={['top', 'bottom']}>
      {children}
    </SafeAreaView>
  );
}

export function Title({ children, style }: { children: ReactNode; style?: StyleProp<TextStyle> }) {
  const c = usePalette();
  return <Text style={[styles.title, { color: c.text }, style]}>{children}</Text>;
}

export function Body({
  children,
  muted,
  style,
}: {
  children: ReactNode;
  muted?: boolean;
  style?: StyleProp<TextStyle>;
}) {
  const c = usePalette();
  return <Text style={[styles.body, { color: muted ? c.text2 : c.text }, style]}>{children}</Text>;
}

export function Small({
  children,
  muted,
  style,
}: {
  children: ReactNode;
  muted?: boolean;
  style?: StyleProp<TextStyle>;
}) {
  const c = usePalette();
  return <Text style={[styles.small, { color: muted ? c.text2 : c.text }, style]}>{children}</Text>;
}

export function Mono({
  children,
  numberOfLines,
  style,
}: {
  children: ReactNode;
  numberOfLines?: number;
  style?: StyleProp<TextStyle>;
}) {
  const c = usePalette();
  return (
    <Text numberOfLines={numberOfLines} style={[styles.mono, { color: c.text2 }, style]}>
      {children}
    </Text>
  );
}

export function Button({
  title,
  onPress,
  variant = 'primary',
  disabled,
  busy,
}: {
  title: string;
  onPress: () => void;
  variant?: 'primary' | 'secondary' | 'danger' | 'ghost';
  disabled?: boolean;
  busy?: boolean;
}) {
  const c = usePalette();
  const bg =
    variant === 'primary' ? c.accent : variant === 'danger' ? c.danger : variant === 'secondary' ? c.surface2 : 'transparent';
  const fg = variant === 'primary' || variant === 'danger' ? c.onAccent : c.text;
  return (
    <Pressable
      accessibilityRole="button"
      disabled={disabled || busy}
      onPress={onPress}
      style={({ pressed }) => [
        styles.btn,
        { backgroundColor: bg, borderColor: variant === 'secondary' ? c.border : 'transparent' },
        pressed && !disabled && !busy ? styles.pressed : null,
        disabled || busy ? styles.disabled : null,
      ]}
    >
      <Text style={[styles.btnLabel, { color: fg }]}>{title}</Text>
      {busy && <ActivityIndicator color={fg} style={styles.btnSpinner} />}
    </Pressable>
  );
}

export function Card({ children, style }: { children: ReactNode; style?: StyleProp<ViewStyle> }) {
  const c = usePalette();
  return <View style={[styles.card, { backgroundColor: c.surface, borderColor: c.border }, style]}>{children}</View>;
}

export function Alert({
  children,
  danger,
}: {
  children: ReactNode;
  danger?: boolean;
}) {
  const c = usePalette();
  return (
    <View
      style={[
        styles.alert,
        { backgroundColor: danger ? c.dangerSoft : c.accentSoft, borderColor: danger ? c.danger : c.accent },
      ]}
    >
      {children}
    </View>
  );
}

export function Row({
  children,
  onPress,
  style,
}: {
  children: ReactNode;
  onPress?: () => void;
  style?: StyleProp<ViewStyle>;
}) {
  const c = usePalette();
  return (
    <Pressable
      accessibilityRole={onPress ? 'button' : undefined}
      onPress={onPress}
      disabled={!onPress}
      style={({ pressed }) => [
        styles.row,
        { borderBottomColor: c.border },
        style,
        pressed ? styles.pressed : null,
      ]}
    >
      {children}
    </Pressable>
  );
}

export function Chip({ label, on, onPress }: { label: string; on?: boolean; onPress?: () => void }) {
  const c = usePalette();
  return (
    <Pressable
      accessibilityRole="button"
      onPress={onPress}
      style={[
        styles.chip,
        { borderColor: on ? c.accent : c.border, backgroundColor: on ? c.accentSoft : c.surface2 },
      ]}
    >
      <Text style={[styles.small, { color: on ? c.accent : c.text2 }]}>{label}</Text>
    </Pressable>
  );
}

export function Toggle({ on }: { on: boolean }) {
  const c = usePalette();
  return (
    <View style={[styles.toggle, { backgroundColor: on ? c.accent : c.surface2, borderColor: on ? c.accent : c.border }]}>
      <View style={[styles.knob, on ? styles.knobOn : null, { backgroundColor: on ? c.onAccent : c.text2 }]} />
    </View>
  );
}

export function Spinner({ label }: { label?: string }) {
  const c = usePalette();
  return (
    <View style={styles.spinnerWrap}>
      <ActivityIndicator size="large" color={c.accent} />
      {label ? <Small muted>{label}</Small> : null}
    </View>
  );
}

const styles = StyleSheet.create({
  screen: { flex: 1 },
  title: { fontSize: type.title, fontWeight: '700', letterSpacing: -0.5 },
  body: { fontSize: type.body, lineHeight: 24 },
  small: { fontSize: type.small, lineHeight: 18 },
  mono: { fontSize: type.mono, fontFamily: 'monospace' },
  btn: {
    minHeight: 50,
    borderRadius: radius.pill,
    borderWidth: 1,
    alignItems: 'center',
    justifyContent: 'center',
    flexDirection: 'row',
    paddingHorizontal: spacing(3),
  },
  btnLabel: { fontSize: type.body, fontWeight: '600' },
  btnSpinner: { marginLeft: spacing(1) },
  pressed: { transform: [{ scale: 0.98 }] },
  disabled: { opacity: 0.5 },
  card: { borderRadius: radius.card, borderWidth: 1, padding: spacing(2) },
  alert: { borderRadius: radius.card, borderWidth: 1, padding: spacing(2), gap: spacing(1) },
  row: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: spacing(1.5),
    paddingVertical: spacing(1.5),
    borderBottomWidth: StyleSheet.hairlineWidth,
  },
  chip: {
    borderRadius: radius.pill,
    borderWidth: 1,
    paddingHorizontal: spacing(1.5),
    paddingVertical: spacing(0.75),
  },
  toggle: {
    width: 50,
    height: 30,
    borderRadius: 15,
    borderWidth: 1,
    justifyContent: 'center',
    padding: 3,
  },
  knob: { width: 22, height: 22, borderRadius: 11 },
  knobOn: { alignSelf: 'flex-end' },
  spinnerWrap: { alignItems: 'center', gap: spacing(1), padding: spacing(3) },
});
