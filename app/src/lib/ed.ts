/**
 * Single Ed25519 verification entry point. @noble/ed25519 v3 requires the
 * SHA-512 hash to be registered; we register noble-hashes' sha512 here so
 * every caller (browser, tests) gets a working verifier.
 */
import { verify, hashes } from '@noble/ed25519';
import { sha512 } from '@noble/hashes/sha2.js';

hashes.sha512 = sha512;

export function ed25519Verify(sig: Uint8Array, msg: Uint8Array, pub: Uint8Array): boolean {
  try {
    return verify(sig, msg, pub);
  } catch {
    return false;
  }
}
