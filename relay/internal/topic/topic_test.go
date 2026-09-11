package topic

import (
	"encoding/base64"
	"strings"
	"testing"
)

// Golden vectors computed independently (Python hashlib/base64):
//
//	h_channel    = base64url(sha256("b|company.example|marketing"))
//	topic_channel = "n-b-" + base64url(sha256("keryx/relay/v1|" + h_channel))
//	h_order      = base64url(sha256("o|A9xQr5bDgWz4m2nPqK8tLc"))
//	topic_order  = "n-o-" + base64url(sha256("keryx/relay/v1|" + h_order))
const (
	goldenHChannel     = "9mcp2JZIOXiJk8HyMyS9phNoAO9kSCZQrzyfteZn1bw"
	goldenTopicChannel = "n-b-TLq4wkrsaJg7bPCnIS1kRh_-ZMGbc9OETPKtjIxU0dM"
	goldenHOrder       = "0VXBXXU1Jv8K8qM0v6e6F1eUdrHaonjR6CgHTTH8CEk"
	goldenTopicOrder   = "n-o-J9Td4mCE6kf6HMQ3IpuCxLC77p2ML1xZjf75Zxzpo54"
)

func TestSourceHashChannel(t *testing.T) {
	got := SourceHash(Channel, "company.example|marketing")
	if got != goldenHChannel {
		t.Fatalf("SourceHash(channel) = %q, want %q", got, goldenHChannel)
	}
}

func TestSourceHashOrder(t *testing.T) {
	got := SourceHash(Order, "A9xQr5bDgWz4m2nPqK8tLc")
	if got != goldenHOrder {
		t.Fatalf("SourceHash(order) = %q, want %q", got, goldenHOrder)
	}
}

func TestTopicChannel(t *testing.T) {
	got, err := Topic(Channel, goldenHChannel)
	if err != nil {
		t.Fatal(err)
	}
	if got != goldenTopicChannel {
		t.Fatalf("Topic(channel) = %q, want %q", got, goldenTopicChannel)
	}
}

func TestTopicOrder(t *testing.T) {
	got, err := Topic(Order, goldenHOrder)
	if err != nil {
		t.Fatal(err)
	}
	if got != goldenTopicOrder {
		t.Fatalf("Topic(order) = %q, want %q", got, goldenTopicOrder)
	}
}

func TestDeterministic(t *testing.T) {
	for _, tc := range []struct {
		kind Kind
		h    string
		want string
	}{
		{Channel, goldenHChannel, goldenTopicChannel},
		{Order, goldenHOrder, goldenTopicOrder},
	} {
		for i := 0; i < 5; i++ {
			got, err := Topic(tc.kind, tc.h)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("not deterministic: %q != %q", got, tc.want)
			}
		}
	}
}

func TestValidateH(t *testing.T) {
	valid := []string{goldenHChannel, goldenHOrder}
	for _, h := range valid {
		if err := ValidateH(h); err != nil {
			t.Errorf("ValidateH(%q) = %v, want nil", h, err)
		}
	}

	bad := []struct {
		name, h string
	}{
		{"empty", ""},
		{"too short", goldenHChannel[:42]},
		{"too long", goldenHChannel + "a"},
		{"standard base64 padding", goldenHChannel + "="},
		{"plus slash", strings.Replace(goldenHChannel[:10], "9", "+", 1) + goldenHChannel[10:]},
		{"non-base64url char", "!" + goldenHChannel[1:]},
	}
	for _, tc := range bad {
		if err := ValidateH(tc.h); err == nil {
			t.Errorf("ValidateH(%s) = nil, want error", tc.name)
		}
	}
}

func TestValidateHTrailingBits(t *testing.T) {
	// 43 chars = 258 bits; only 256 are used aren. Non-zero trailing bits must
	// be rejected: craft a string that decodes to the same 32 bytes but is not
	// canonical (last char with extra bits set).
	// Take the golden h, decode, then re-encode with a non-canonical final char.
	raw, err := base64.RawURLEncoding.DecodeString(goldenHChannel)
	if err != nil {
		t.Fatal(err)
	}
	// Find a non-canonical encoding: append bits by using a different last char.
	// Simplest: flip the low bits of the last character to a value that decodes
	// to the same first 30 bytes but differs in bits 256..257.
	last := goldenHChannel[42]
	// alphabet
	const alpha = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	idx := strings.IndexByte(alpha, last)
	// chars with the same top 4 bits (i.e. same 24 bits of the last 3 bytes)
	// differ in the bottom 2 bits -> same 32 bytes? Actually last char's 6 bits:
	// bits 252..257; bytes 30..31 use bits 240..255, so the last char's bottom
	// 2 bits (256,257) are dropped. Any char differing only there decodes to
	// the same bytes but re-encodes differently.
	for i := 0; i < 4; i++ {
		c := alpha[(idx&^3)|i]
		if c == last {
			continue
		}
		nonCanonical := goldenHChannel[:42] + string(c)
		decoded, err := base64.RawURLEncoding.DecodeString(nonCanonical)
		if err != nil {
			t.Fatal(err)
		}
		if string(decoded) != string(raw) {
			continue // not the same bytes; try next
		}
		if err := ValidateH(nonCanonical); err == nil {
			t.Fatalf("ValidateH accepted non-canonical %q", nonCanonical)
		}
		return
	}
	t.Fatal("no non-canonical variant found")
}

func TestValidateTopic(t *testing.T) {
	for _, tt := range []string{goldenTopicChannel, goldenTopicOrder} {
		if err := ValidateTopic(tt); err != nil {
			t.Errorf("ValidateTopic(%q) = %v, want nil", tt, err)
		}
	}
	bad := []struct {
		name, topic string
	}{
		{"empty", ""},
		{"short", goldenTopicChannel[:46]},
		{"long", goldenTopicChannel + "a"},
		{"wrong prefix", "n-x-" + goldenTopicChannel[4:]},
		{"bad inner", "n-b-" + strings.Repeat("!", 43)},
	}
	for _, tc := range bad {
		if err := ValidateTopic(tc.topic); err == nil {
			t.Errorf("ValidateTopic(%s) = nil, want error", tc.name)
		}
	}
}

func TestKindOfTopic(t *testing.T) {
	if k, err := KindOfTopic(goldenTopicChannel); err != nil || k != Channel {
		t.Errorf("KindOfTopic(channel topic) = %v, %v", k, err)
	}
	if k, err := KindOfTopic(goldenTopicOrder); err != nil || k != Order {
		t.Errorf("KindOfTopic(order topic) = %v, %v", k, err)
	}
}

func TestParseKind(t *testing.T) {
	if k, err := ParseKind("channel"); err != nil || k != Channel {
		t.Errorf("ParseKind(channel) = %v, %v", k, err)
	}
	if k, err := ParseKind("order"); err != nil || k != Order {
		t.Errorf("ParseKind(order) = %v, %v", k, err)
	}
	for _, bad := range []string{"", "Channel", "foo", "n-b-"} {
		if _, err := ParseKind(bad); err == nil {
			t.Errorf("ParseKind(%q) = nil, want error", bad)
		}
	}
}

func TestTopicRejectsBadH(t *testing.T) {
	if _, err := Topic(Channel, "not-a-hash"); err == nil {
		t.Fatal("Topic accepted bad h")
	}
}
