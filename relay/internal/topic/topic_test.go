package topic

import (
	"encoding/base64"
	"strings"
	"testing"
)

// Golden vectors computed independently (Python hashlib/base64):
//
//	h_channel     = base64url(sha256("company.example|marketing"))
//	topic_channel = "n-" + base64url(sha256("keryx/relay/v1|" + h_channel))
//	h_order       = base64url(sha256("A9xQr5bDgWz4m2nPqK8tLc"))
//	topic_order   = "n-" + base64url(sha256("keryx/relay/v1|" + h_order))
const (
	goldenHChannel     = "UMd0ghtopTVuG7XdC4x7cv_couCvOWKvxzQ_yfCClrk"
	goldenTopicChannel = "n-Z720n4ivEXWSqHWBmeGxiryrFEd0BxzY2FdcX7E-A4E"
	goldenHOrder       = "eMdc1Kp3b44hlDn2R8GKMoBX4-MSaZKBSthLbZ66jiw"
	goldenTopicOrder   = "n-1cuAlFoLL9rmC4RGqII-FFeX_KaXrbgp2384-nwGvvM"
)

func TestSourceHash(t *testing.T) {
	if got := SourceHash("company.example|marketing"); got != goldenHChannel {
		t.Fatalf("SourceHash(channel input) = %q, want %q", got, goldenHChannel)
	}
	if got := SourceHash("A9xQr5bDgWz4m2nPqK8tLc"); got != goldenHOrder {
		t.Fatalf("SourceHash(order input) = %q, want %q", got, goldenHOrder)
	}
}

func TestTopic(t *testing.T) {
	got, err := Topic(goldenHChannel)
	if err != nil {
		t.Fatal(err)
	}
	if got != goldenTopicChannel {
		t.Fatalf("Topic(channel h) = %q, want %q", got, goldenTopicChannel)
	}
	got, err = Topic(goldenHOrder)
	if err != nil {
		t.Fatal(err)
	}
	if got != goldenTopicOrder {
		t.Fatalf("Topic(order h) = %q, want %q", got, goldenTopicOrder)
	}
}

func TestDeterministic(t *testing.T) {
	for i := 0; i < 5; i++ {
		got, err := Topic(goldenHChannel)
		if err != nil {
			t.Fatal(err)
		}
		if got != goldenTopicChannel {
			t.Fatalf("not deterministic: %q != %q", got, goldenTopicChannel)
		}
	}
}

func TestValidateH(t *testing.T) {
	for _, h := range []string{goldenHChannel, goldenHOrder} {
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
		{"plus slash", strings.Replace(goldenHChannel[:10], "U", "+", 1) + goldenHChannel[10:]},
		{"non-base64url char", "!" + goldenHChannel[1:]},
	}
	for _, tc := range bad {
		if err := ValidateH(tc.h); err == nil {
			t.Errorf("ValidateH(%s) = nil, want error", tc.name)
		}
	}
}

func TestValidateHTrailingBits(t *testing.T) {
	// 43 chars = 258 bits; only 256 are used. Non-zero trailing bits must be
	// rejected: craft a string that decodes to the same 32 bytes but is not
	// canonical (last char with extra bits set).
	raw, err := base64.RawURLEncoding.DecodeString(goldenHChannel)
	if err != nil {
		t.Fatal(err)
	}
	const alpha = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	last := goldenHChannel[42]
	idx := strings.IndexByte(alpha, last)
	for i := 1; i < 4; i++ {
		c := alpha[(idx&^3)|i] // same top 4 bits: same 32 bytes, non-zero trailing bits
		if c == last {
			continue
		}
		nonCanonical := goldenHChannel[:42] + string(c)
		decoded, err := base64.RawURLEncoding.DecodeString(nonCanonical)
		if err != nil {
			t.Fatal(err)
		}
		if string(decoded) != string(raw) {
			continue
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
		name, t string
	}{
		{"empty", ""},
		{"short", goldenTopicChannel[:44]},
		{"long", goldenTopicChannel + "a"},
		{"wrong prefix", "n-x-" + goldenTopicChannel[3:]},
		{"bad inner", "n-" + strings.Repeat("!", 43)},
	}
	for _, tc := range bad {
		if err := ValidateTopic(tc.t); err == nil {
			t.Errorf("ValidateTopic(%s) = nil, want error", tc.name)
		}
	}
}

func TestTopicRejectsBadH(t *testing.T) {
	if _, err := Topic("not-a-hash"); err == nil {
		t.Fatal("Topic accepted bad h")
	}
}
