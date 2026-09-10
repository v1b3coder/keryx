/**
 * QR scanning: native (Capacitor MLKit) on Android/iOS, `BarcodeDetector`
 * or jsQR on the web. Returns the decoded text (a join URL) or null.
 */

import { Capacitor } from '@capacitor/core';
import jsQR from 'jsqr';

export async function scanQr(preview?: HTMLVideoElement, signal?: AbortSignal): Promise<string | null> {
  if (Capacitor.isNativePlatform()) {
    try {
      const { BarcodeScanner } = await import('@capacitor-mlkit/barcode-scanning');
      const { BarcodeFormat } = await import('@capacitor-mlkit/barcode-scanning');
      const { barcodes } = await BarcodeScanner.scan({
        formats: [BarcodeFormat.QrCode],
      });
      return barcodes?.[0]?.rawValue ?? null;
    } catch {
      return null;
    }
  }
  return scanWeb(preview, signal);
}

async function scanWeb(preview?: HTMLVideoElement, signal?: AbortSignal): Promise<string | null> {
  const stream = await navigator.mediaDevices.getUserMedia({
    video: { facingMode: 'environment' },
  });
  try {
    // A preview element (attached by the UI) makes the camera feed visible;
    // without one the feed is scanned invisibly (legacy behaviour).
    const video = preview ?? document.createElement('video');
    video.srcObject = stream;
    video.setAttribute('playsinline', 'true');
    await video.play();
    const canvas = document.createElement('canvas');
    const ctx = canvas.getContext('2d', { willReadFrequently: true })!;
    const BarcodeDetectorCtor = (window as { BarcodeDetector?: new (o: unknown) => {
      detect: (v: HTMLVideoElement) => Promise<{ rawValue: string }[]>;
    } }).BarcodeDetector;
    let detector: { detect: (v: HTMLVideoElement) => Promise<{ rawValue: string }[]> } | null = null;
    if (BarcodeDetectorCtor) {
      try {
        detector = new BarcodeDetectorCtor({ formats: ['qr_code'] });
      } catch {
        detector = null;
      }
    }
    const deadline = Date.now() + 20000;
    while (Date.now() < deadline) {
      if (signal?.aborted) return null;
      if (detector) {
        const codes = await detector.detect(video);
        if (codes.length > 0) return codes[0].rawValue;
      } else {
        canvas.width = video.videoWidth;
        canvas.height = video.videoHeight;
        ctx.drawImage(video, 0, 0);
        const img = ctx.getImageData(0, 0, canvas.width, canvas.height);
        const code = jsQR(img.data, img.width, img.height, { inversionAttempts: 'dontInvert' });
        if (code?.data) return code.data;
      }
      await new Promise((r) => setTimeout(r, 120));
    }
    return null;
  } finally {
    stream.getTracks().forEach((t) => t.stop());
  }
}
