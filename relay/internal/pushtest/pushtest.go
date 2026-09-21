// Package pushtest provides test-only helpers for decrypting RFC 8291 WebPush
// bodies with a UA private key, shared by the relay's end-to-end and API tests.
package pushtest

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/sha256"
	"errors"
	"fmt"
)

// DecryptRFC8291 decrypts a single-record aes128gcm body with the UA key.
func DecryptRFC8291(ua *ecdh.PrivateKey, auth, body []byte) ([]byte, error) {
	if len(body) < 86 {
		return nil, fmt.Errorf("body too short: %d", len(body))
	}
	salt := body[:16]
	if rs := body[16:20]; rs[0] != 0 || rs[1] != 0 || rs[2] != 0x10 || rs[3] != 0 {
		return nil, fmt.Errorf("bad rs: %x", rs)
	}
	idlen := body[20]
	if idlen != 65 {
		return nil, fmt.Errorf("bad keyid length: %d", idlen)
	}
	asPub := body[21 : 21+65]
	ct := body[21+65:]

	asPoint, err := ecdh.P256().NewPublicKey(asPub)
	if err != nil {
		return nil, err
	}
	shared, err := ua.ECDH(asPoint)
	if err != nil {
		return nil, err
	}
	prkKey, err := hkdf.Extract(sha256.New, shared, auth)
	if err != nil {
		return nil, err
	}
	info := append([]byte("WebPush: info\x00"), ua.PublicKey().Bytes()...)
	info = append(info, asPub...)
	ikm, err := hkdf.Expand(sha256.New, prkKey, string(info), 32)
	if err != nil {
		return nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, err
	}
	if len(plain) == 0 || plain[len(plain)-1] != 0x02 {
		return nil, errors.New("missing padding delimiter")
	}
	return plain[:len(plain)-1], nil
}
