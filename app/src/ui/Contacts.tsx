/**
 * Contacts list (shown only when more than one company is added — with a
 * single company the app opens it directly). Each row: logo, name, the
 * join origin as a persistent secondary line, unread count.
 */
import { Pressable, ScrollView, StyleSheet, Text, View } from 'react-native';
import type { CompanyRecord, StoredItem } from '../lib/store';
import type { AppActions } from '../state';
import type { NotificationState } from '../lib/notify';
import { CompanyLogo } from './CompanyLogo';
import { NotificationBanner } from './NotificationBanner';
import { IconButton, Mono, Screen, Small, Title } from './components';
import { radius, size, spacing, type, usePalette } from '../theme';

export function Contacts({
  companies,
  items,
  notification,
  freshTest,
  actions,
  onOpen,
  onAdd,
}: {
  companies: CompanyRecord[];
  items: StoredItem[];
  notification: NotificationState;
  freshTest: boolean;
  actions: AppActions;
  onOpen: (origin: string) => void;
  onAdd: () => void;
}) {
  const c = usePalette();
  return (
    <Screen>
      <View style={[styles.header, { borderBottomColor: c.border }]}>
        <View style={styles.headerInner}>
          <Title style={{ flex: 1 }}>Messages</Title>
          <IconButton
            icon={<Text style={[styles.headerIcon, { color: c.accent }]}>＋</Text>}
            accessibilityLabel="Add company"
            onPress={onAdd}
          />
        </View>
      </View>
      <ScrollView contentContainerStyle={styles.list}>
        <NotificationBanner
          state={notification}
          freshTest={freshTest}
          onEnable={() => void actions.enableNotifications()}
          onCheck={() => void actions.checkNotifications()}
          onRetry={() => void actions.runNotificationSelfTest()}
        />
        {companies.map((company) => {
          const unread = items.filter((i) => i.origin === company.origin && !i.read).length;
          return (
            <Pressable
              key={company.origin}
              accessibilityRole="button"
              accessibilityLabel={`Open ${company.targets.signed.custom?.company_name ?? company.origin}`}
              onPress={() => onOpen(company.origin)}
              style={({ pressed }) => [
                styles.row,
                { borderBottomColor: c.border },
                pressed ? { backgroundColor: c.surface2 } : null,
              ]}
            >
              <CompanyLogo
                url={company.targets.signed.custom?.logo}
                origin={company.origin}
                expectedSha={company.targets.signed.custom?.logo_sha256}
                size={size.rowLogo}
              />
              <View style={{ flex: 1, minWidth: 0 }}>
                <Text numberOfLines={1} style={[styles.rowName, { color: c.text }]}>
                  {company.targets.signed.custom?.company_name ?? company.origin}
                </Text>
                <Mono numberOfLines={1}>{company.origin}</Mono>
              </View>
              {unread > 0 ? (
                <View style={[styles.unread, { backgroundColor: c.accent }]}>
                  <Text style={{ color: c.onAccent, fontSize: 12, fontWeight: '700' }}>
                    {unread > 99 ? '99+' : unread}
                  </Text>
                </View>
              ) : null}
            </Pressable>
          );
        })}
      </ScrollView>
    </Screen>
  );
}

const styles = StyleSheet.create({
  header: { borderBottomWidth: StyleSheet.hairlineWidth },
  headerInner: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: spacing(1.5),
    paddingHorizontal: spacing(2.5),
    paddingVertical: spacing(1.25),
    minHeight: 64,
  },
  headerIcon: { fontSize: 24, lineHeight: 28 },
  list: { paddingHorizontal: spacing(2.5), paddingBottom: spacing(4) },
  row: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: spacing(1.5),
    paddingVertical: spacing(1.5),
    borderBottomWidth: StyleSheet.hairlineWidth,
  },
  rowName: { fontSize: type.body, fontWeight: '600' },
  unread: {
    minWidth: 22,
    height: 22,
    borderRadius: radius.pill,
    alignItems: 'center',
    justifyContent: 'center',
    paddingHorizontal: 7,
  },
});
