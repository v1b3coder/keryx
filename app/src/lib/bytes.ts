/**
 * Byte helpers for the wire formats: hex for TUF metadata (keyids, sigs,
 * hashes), base64url (RFC 4648 §5, no padding) for item signatures and the
 * join payload.
 */

import { sha256 } from '@noble/hashes/sha2.js';

export function hexToBytes(hex: string): Uint8Array {
  if (hex.length % 2 !== 0 || !/^[0-9a-fA-F]*$/.test(hex)) {
    throw new Error('invalid hex string');
  }
  const out = new Uint8Array(hex.length / 2);
  for (let i = 0; i < out.length; i++) {
    out[i] = parseInt(hex.slice(i * 2, i * 2 + 2), 16);
  }
  return out;
}

export function bytesToHex(bytes: Uint8Array): string {
  let s = '';
  for (const b of bytes) s += b.toString(16).padStart(2, '0');
  return s;
}

/** base64url without padding (RFC 4648 §5). */
export function bytesToBase64url(bytes: Uint8Array): string {
  let s = '';
  for (const b of bytes) s += String.fromCharCode(b);
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

export function base64urlToBytes(s: string): Uint8Array {
  if (!/^[A-Za-z0-9_-]*$/.test(s) || s.length % 4 === 1) {
    throw new Error('invalid base64url string');
  }
  const b64 = s.replace(/-/g, '+').replace(/_/g, '/');
  const bin = atob(b64 + '='.repeat((4 - (b64.length % 4)) % 4));
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

export function base64urlToText(s: string): string {
  return new TextDecoder().decode(base64urlToBytes(s));
}

export function textToBase64url(t: string): string {
  return bytesToBase64url(new TextEncoder().encode(t));
}

/** SHA-256 as lowercase hex (sync, noble). */
export function sha256Hex(data: Uint8Array): string {
  return bytesToHex(sha256(data));
}
