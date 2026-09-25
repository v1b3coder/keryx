package wakeup

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
)

// TestPayload is the §4.3 self-test payload. It is never a wake-up: no topic,
// no seq, no signature. The relay generates it and the client accepts it only
// while it is waiting for that nonce (relay/SPECIFICATION.md §4.3, §5.3.1).
type TestPayload struct {
	V     int    `json:"v"`
	Test  bool   `json:"test"`
	Nonce string `json:"nonce"`
}

// NewTestCapability returns a fresh 43-char base64url capability from 32
// random bytes (§5.3.1): the relay-generated test topic, the test_id and the
// test nonce all use this shape.
func NewTestCapability() string {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// TestPayloadBytes returns the canonical §4.3 payload that carries nonce.
func TestPayloadBytes(nonce string) []byte {
	payload, _ := json.Marshal(TestPayload{V: 1, Test: true, Nonce: nonce})
	return payload
}

// NewTestPayload returns a fresh nonce (32 random bytes, base64url) and the
// canonical §4.3 payload that carries it.
func NewTestPayload() (nonce string, payload []byte) {
	nonce = NewTestCapability()
	return nonce, TestPayloadBytes(nonce)
}
