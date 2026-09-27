/**
 * Contacts list (shown only when more than one company is added — with a
 * single company the app opens it directly). Each card: logo, name, the
 * join origin as a persistent secondary line, unread count.
 */
import { ScrollView, StyleSheet, Text, View } from 'react-native';
import type { CompanyRecord, StoredItem } from '../lib/store';
import type { AppActions } from '../state';
import type { NotificationState } from '../lib/notify';
import { CompanyLogo } from './CompanyLogo';
import { NotificationBanner } from './NotificationBanner';
import { Body, Button, Mono, Screen, Small, Title } from './components';
import { spacing, type, usePalette } from '../theme';

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
      <View style={styles.header}>
        <Title style={{ flex: 1 }}>Messages</Title>
        <Button title="Add" onPress={onAdd} />
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
            <View key={company.origin} style={[styles.row, { borderBottomColor: c.border }]}>
              <CompanyLogo
                url={company.targets.signed.custom?.logo}
                origin={company.origin}
                expectedSha={company.targets.signed.custom?.logo_sha256}
                size={44}
              />
              <View style={{ flex: 1, minWidth: 0 }}>
                <Button
                  title={company.targets.signed.custom?.company_name ?? company.origin}
                  variant="ghost"
                  onPress={() => onOpen(company.origin)}
                />
                <Mono>{company.origin}</Mono>
              </View>
              {unread > 0 ? (
                <View style={[styles.unread, { backgroundColor: c.accent }]}>
                  <Text style={{ color: c.onAccent, fontSize: type.small, fontWeight: '700' }}>
                    {unread > 99 ? '99+' : unread}
                  </Text>
                </View>
              ) : null}
            </View>
          );
        })}
        <Body muted style={styles.hint}>
          Each card keeps the confirmed origin visible: that is the anchor the company name and logo
          are shown against.
        </Body>
      </ScrollView>
    </Screen>
  );
}

const styles = StyleSheet.create({
  header: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: spacing(1),
    paddingHorizontal: spacing(2.5),
    paddingVertical: spacing(1),
  },
  list: { paddingHorizontal: spacing(2.5), paddingBottom: spacing(4), gap: spacing(0.5) },
  row: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: spacing(1.5),
    paddingVertical: spacing(1.5),
    borderBottomWidth: StyleSheet.hairlineWidth,
  },
  unread: {
    minWidth: 26,
    height: 26,
    borderRadius: 13,
    alignItems: 'center',
    justifyContent: 'center',
    paddingHorizontal: 6,
  },
  hint: { marginTop: spacing(3) },
});
