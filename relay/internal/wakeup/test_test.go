package wakeup

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestNewTestPayload(t *testing.T) {
	nonce, payload := NewTestPayload()
	if raw, err := base64.RawURLEncoding.DecodeString(nonce); err != nil || len(raw) != 32 {
		t.Fatalf("nonce = %q (%v)", nonce, err)
	}
	var p TestPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if p.V != 1 || !p.Test || p.Nonce != nonce {
		t.Fatalf("payload = %s", payload)
	}
	if _, err := Parse(payload); err == nil {
		t.Fatal("the test payload parsed as a wake-up")
	}
}
