// Package company canonicalizes and validates the company_id used by the relay
// (relay/SPECIFICATION.md §2): the canonical join origin — lowercase ASCII
// host, punycode for IDNs, no scheme, no port, no trailing slash.
package company

import (
	"errors"
	"strings"

	"golang.org/x/net/idna"
)

// ErrInvalid reports a noncanonical company_id.
var ErrInvalid = errors.New("company_id: must be a canonical lowercase host (no scheme, port or path)")

// Canonical returns the canonical form of a company host. Uppercase ASCII and
// Unicode IDN labels are converted; an already-canonical value is returned
// unchanged.
func Canonical(s string) (string, error) {
	if s == "" {
		return "", ErrInvalid
	}
	if strings.ContainsAny(s, "/:@ \t\r\n?#") {
		return "", ErrInvalid
	}
	ascii, err := idna.Lookup.ToASCII(s)
	if err != nil {
		return "", ErrInvalid
	}
	ascii = strings.ToLower(ascii)
	if err := Validate(ascii); err != nil {
		return "", err
	}
	return ascii, nil
}

// Validate reports whether s is already canonical.
func Validate(s string) error {
	if s == "" || len(s) > 253 {
		return ErrInvalid
	}
	if strings.ToLower(s) != s {
		return ErrInvalid
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" || len(label) > 63 {
			return ErrInvalid
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
				continue
			}
			return ErrInvalid
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return ErrInvalid
		}
	}
	return nil
}
