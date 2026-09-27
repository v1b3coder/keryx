/**
 * The build stamp: the short commit and the app version, so a running install
 * can be told apart from a stale one (build.ts).
 */
import { StyleSheet, View } from 'react-native';
import { appVersion } from '../lib/build';
import { Mono } from './components';
import { spacing } from '../theme';

export function BuildStamp() {
  return (
    <View style={styles.wrap}>
      <Mono>Build {appVersion()}</Mono>
    </View>
  );
}

const styles = StyleSheet.create({
  wrap: { paddingVertical: spacing(1) },
});
