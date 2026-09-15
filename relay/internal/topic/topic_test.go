package topic

import (
	"strings"
	"testing"
)

func TestSourceHashUniform(t *testing.T) {
	// hex(sha256("company.example|news")) — one rule for channels and orders.
	got := SourceHash("company.example|news")
	if len(got) != 64 {
		t.Fatalf("len = %d", len(got))
	}
	if strings.ToLower(got) != got {
		t.Fatalf("not lowercase hex: %s", got)
	}
	if got != SourceHash("company.example|news") {
		t.Fatal("not deterministic")
	}
	if got == SourceHash("company.example|orders") {
		t.Fatal("different subjects collide")
	}
}

func TestDerive(t *testing.T) {
	h := SourceHash("company.example|news")
	scope := strings.Repeat("ab", 32)
	got, err := Derive("company.example", scope, h)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 43 {
		t.Fatalf("topic length = %d, want 43", len(got))
	}
	if err := ValidateTopic(got); err != nil {
		t.Fatalf("derived topic rejected: %v", err)
	}
	again, _ := Derive("company.example", scope, h)
	if got != again {
		t.Fatal("derivation not deterministic")
	}
	// Company and scope are part of the derivation: the same h under a
	// different scope or company must not reach the same topic.
	other, _ := Derive("company.example", strings.Repeat("cd", 32), h)
	if other == got {
		t.Fatal("scope does not change the topic")
	}
	other, _ = Derive("other.example", scope, h)
	if other == got {
		t.Fatal("company does not change the topic")
	}
}

func TestValidation(t *testing.T) {
	if err := ValidateH(strings.Repeat("a", 63)); err == nil {
		t.Fatal("accepted short h")
	}
	if err := ValidateH(strings.Repeat("A", 64)); err == nil {
		t.Fatal("accepted uppercase h")
	}
	if err := ValidateH(strings.Repeat("g", 64)); err == nil {
		t.Fatal("accepted non-hex h")
	}
	if err := ValidateH(strings.Repeat("a", 64)); err != nil {
		t.Fatalf("rejected valid h: %v", err)
	}
	if err := ValidateScopeID(strings.Repeat("a", 63)); err == nil {
		t.Fatal("accepted short scope_id")
	}
	if err := ValidateScopeID(strings.Repeat("a", 64)); err != nil {
		t.Fatalf("rejected valid scope_id: %v", err)
	}
	if err := ValidateTopic("n-" + strings.Repeat("a", 43)); err == nil {
		t.Fatal("accepted old prefix")
	}
	valid, _ := Derive("company.example", strings.Repeat("a", 64), strings.Repeat("b", 64))
	if err := ValidateTopic(valid); err != nil {
		t.Fatalf("rejected derived topic: %v", err)
	}
	if err := ValidateTopic(valid[:42]); err == nil {
		t.Fatal("accepted short topic")
	}
	// Non-canonical base64url (non-zero trailing bits) is rejected.
	if err := ValidateTopic(strings.Repeat("A", 42) + "B"); err == nil {
		t.Fatal("accepted non-canonical base64url")
	}
}
