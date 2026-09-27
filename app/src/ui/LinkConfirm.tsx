/**
 * Link interception (spec/feeds.md §1.4): no auto-open; the real
 * destination domain is shown before opening. The link is never part of the
 * company's signed message, so the user confirms it.
 */
import { StyleSheet, View } from 'react-native';
import { domainOf } from '../lib/format';
import { Alert, Body, Button, Card, Mono, Screen, Small } from './components';
import { spacing } from '../theme';

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
  return (
    <Screen>
      <View style={styles.pad}>
        <Alert>
          <Body>Open external link?</Body>
          <Small muted>
            This link goes to {domain}. It is not part of the company's signed message.
          </Small>
          <Card>
            <Mono>{url}</Mono>
          </Card>
        </Alert>
        <Button title={`Open ${domain}`} onPress={onConfirm} />
        <Button title="Cancel" variant="secondary" onPress={onCancel} />
      </View>
    </Screen>
  );
}

const styles = StyleSheet.create({
  pad: { flex: 1, padding: spacing(2.5), gap: spacing(1.5) },
});
