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

const B64_ALPHABET = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_';

/** base64url without padding (RFC 4648 §5), portable (no btoa/atob). */
export function bytesToBase64url(bytes: Uint8Array): string {
  let out = '';
  for (let i = 0; i < bytes.length; i += 3) {
    const b0 = bytes[i];
    const b1 = bytes[i + 1];
    const b2 = bytes[i + 2];
    out += B64_ALPHABET[b0 >> 2];
    out += B64_ALPHABET[((b0 & 3) << 4) | ((b1 ?? 0) >> 4)];
    if (b1 === undefined) break;
    out += B64_ALPHABET[((b1 & 15) << 2) | ((b2 ?? 0) >> 6)];
    if (b2 === undefined) break;
    out += B64_ALPHABET[b2 & 63];
  }
  return out;
}

export function base64urlToBytes(s: string): Uint8Array {
  if (!/^[A-Za-z0-9_-]*$/.test(s) || s.length % 4 === 1) {
    throw new Error('invalid base64url string');
  }
  const out = new Uint8Array(Math.floor((s.length * 6) / 8));
  let bits = 0;
  let value = 0;
  let j = 0;
  for (const ch of s) {
    value = (value << 6) | B64_ALPHABET.indexOf(ch);
    bits += 6;
    if (bits >= 8) {
      bits -= 8;
      out[j++] = (value >> bits) & 0xff;
    }
  }
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
