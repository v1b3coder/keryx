package wakeup

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"testing"
)

func TestNewTestCapability(t *testing.T) {
	a, b := NewTestCapability(), NewTestCapability()
	pattern := regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	for _, capability := range []string{a, b} {
		if !pattern.MatchString(capability) {
			t.Fatalf("capability %q is not 43-char base64url", capability)
		}
	}
	if a == b {
		t.Fatal("two capabilities are identical")
	}
}

func TestTestPayloadBytes(t *testing.T) {
	nonce, payload := NewTestPayload()
	if !bytes.Equal(payload, TestPayloadBytes(nonce)) {
		t.Fatal("TestPayloadBytes does not reproduce the payload")
	}
	var parsed TestPayload
	if err := json.Unmarshal(payload, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.V != 1 || !parsed.Test || parsed.Nonce != nonce {
		t.Fatalf("payload = %+v", parsed)
	}
}

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
