/**
 * QR scanning on every target (iOS/Android/web) with expo-camera: the camera
 * view scans only QR codes and the first result is handed to the pairing flow.
 * The web implementation uses the browser's barcode detector where available;
 * Android and iOS use the on-device MLKit scanner (no Play Services needed).
 */
import { useEffect, useState } from 'react';
import { StyleSheet, View } from 'react-native';
import { CameraView, useCameraPermissions, type BarcodeScanningResult } from 'expo-camera';
import { Alert, Body, Button, Screen, Small, Spinner, Title } from './components';
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
  const [timedOut, setTimedOut] = useState(false);

  // a camera pointed at nothing must not sit forever: the same 20 s deadline as
  // the web scanner, after which the user can retry or paste instead
  useEffect(() => {
    if (!permission?.granted || scanned) return;
    const t = setTimeout(() => setTimedOut(true), 20_000);
    return () => clearTimeout(t);
  }, [permission?.granted, scanned]);

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
          <Body muted>Keryx needs the camera to scan the company's QR code.</Body>
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
          onBarcodeScanned={scanned ? undefined : handle}
        />
      </View>
      <View style={styles.pad}>
        <Title>Scan QR code</Title>
        <Body muted>Point the camera at the company's QR code.</Body>
        {timedOut ? (
          <Alert danger>
            <Small>No QR code found. Try again or paste the link instead.</Small>
          </Alert>
        ) : null}
        <Button
          title={timedOut ? 'Try again' : 'Cancel'}
          variant={timedOut ? 'primary' : 'secondary'}
          onPress={timedOut ? () => setTimedOut(false) : onCancel}
        />
        {timedOut ? (
          <Button title="Back" variant="ghost" onPress={onCancel} />
        ) : null}
      </View>
    </Screen>
  );
}

const styles = StyleSheet.create({
  frame: { flex: 1, overflow: 'hidden' },
  pad: { padding: spacing(2.5), gap: spacing(1.5) },
});
