/**
 * QR scanning on every target (iOS/Android/web) with expo-camera: the camera
 * view scans only QR codes and the first result is handed to the pairing flow.
 * The web implementation uses the browser's barcode detector where available;
 * Android and iOS use the on-device MLKit scanner (no Play Services needed).
 */
import { useState } from 'react';
import { StyleSheet, View } from 'react-native';
import { CameraView, useCameraPermissions, type BarcodeScanningResult } from 'expo-camera';
import { Button, Screen, Spinner, Title } from './components';
import { spacing } from '../theme';

export function ScanScreen({
  onScan,
  onCancel,
}: {
  onScan: (text: string) => void;
  onCancel: () => void;
}) {
  const [permission, requestPermission] = useCameraPermissions();
  const [scanned, setScanned] = useState(false);

  if (!permission) {
    return (
      <Screen>
        <Spinner label="Preparing the camera…" />
      </Screen>
    );
  }
  if (!permission.granted) {
    return (
      <Screen>
        <View style={styles.pad}>
          <Title>Camera access</Title>
          <Button title="Allow camera" onPress={() => void requestPermission()} />
          <Button title="Cancel" variant="secondary" onPress={onCancel} />
        </View>
      </Screen>
    );
  }

  function handle(result: BarcodeScanningResult) {
    if (scanned) return;
    setScanned(true);
    onScan(result.data);
  }

  return (
    <Screen>
      <View style={styles.frame}>
        <CameraView
          style={StyleSheet.absoluteFill}
          facing="back"
          barcodeScannerSettings={{ barcodeTypes: ['qr'] }}
          onBarcodeScanned={handle}
        />
      </View>
      <View style={styles.pad}>
        <Title>Scan QR code</Title>
        <Button title="Cancel" variant="secondary" onPress={onCancel} />
      </View>
    </Screen>
  );
}

const styles = StyleSheet.create({
  frame: { flex: 1, overflow: 'hidden' },
  pad: { padding: spacing(2.5), gap: spacing(1.5) },
});
