package tufrepo

import (
	"strings"
	"testing"
	"time"
)

func TestValidateTargetsCustomLogoSHA256(t *testing.T) {
	tests := []struct {
		name, sum string
		ok        bool
	}{
		{"valid", strings.Repeat("0a", 32), true},
		{"missing", "", false},
		{"flag", "--workspace", false},
		{"uppercase", strings.Repeat("0A", 32), false},
		{"63 chars", strings.Repeat("a", 63), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, err := NewState(time.Now(), "https://cdn.example.com/keryx", "ACME s.r.o.")
			if err != nil {
				t.Fatal(err)
			}
			c := st.Custom()
			c["logo"] = "https://cdn.example.com/logo.png"
			c["logo_sha256"] = tt.sum
			if err := validateTargetsCustom(st.Targets); (err == nil) != tt.ok {
				t.Errorf("logo_sha256 %q: err = %v", tt.sum, err)
			}
		})
	}
}
