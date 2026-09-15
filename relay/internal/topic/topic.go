// Package topic implements the relay's topic derivation
// (relay/SPECIFICATION.md §3).
//
// Derivation is deterministic, shared by the relay (publish side) and by the
// app (subscription side), so both agree without any exchange:
//
//	h     = hex(sha256(company_id + "|" + subject))            // caller computes
//	topic = base64url_nopad(sha256("keryx/relay/v1|" + OLPC({company_id, scope_id, h})))
//
// The relay receives company_id, scope_id and h (§5.1) and derives the topic;
// it never sees the subject (a channel name or an order token). h is opaque to
// the relay: the publish path MUST NOT inspect the subject or branch on type.
package topic

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"

	"github.com/secure-systems-lab/go-securesystemslib/cjson"
)

const (
	// salt is the static, public domain separator for the topic derivation.
	// It is not secret; it keeps the topic distinct from h and from other
	// SHA-256 uses in the protocol.
	salt = "keryx/relay/v1|"

	// hLen is the fixed length of the source hash: 64 lowercase hex chars.
	hLen = 64
	// scopeIDLen is the fixed length of a scope identifier: 64 lowercase hex.
	scopeIDLen = 64
	// topicLen is the fixed length of a derived topic: 43 chars base64url.
	topicLen = 43
)

var (
	// ErrBadH reports an h that is not exactly 64 lowercase hex chars.
	ErrBadH = errors.New("source hash: must be exactly 64 lowercase hex chars")
	// ErrBadTopic reports a topic that is not a 43-char base64url string.
	ErrBadTopic = errors.New("topic: must be exactly 43 chars of base64url")
	// ErrBadScopeID reports a scope_id that is not 64 lowercase hex chars.
	ErrBadScopeID = errors.New("scope_id: must be exactly 64 lowercase hex chars")
)

// SourceHash computes the source hash for a derivation input:
// hex(sha256(input)). Callers (publisher tooling, order engine) compute it —
// sha256(company_id + "|" + channel) for a channel wake-up,
// sha256(company_id + "|" + order_token) for an order wake-up. The relay never
// computes it; it only ever sees the result.
func SourceHash(input string) string {
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])
}

// Derive returns the delivery topic for a verified company, scope_id and source
// hash (§3). companyID and scopeID must already be validated; h must be a
// well-formed source hash.
func Derive(companyID, scopeID, h string) (string, error) {
	if err := ValidateScopeID(scopeID); err != nil {
		return "", err
	}
	if err := ValidateH(h); err != nil {
		return "", err
	}
	canonical, err := cjson.EncodeCanonical(map[string]any{
		"company_id": companyID,
		"scope_id":   scopeID,
		"h":          h,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte(salt), canonical...))
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

// ValidateH checks that h is exactly 64 lowercase hex chars (32 bytes).
func ValidateH(h string) error {
	if len(h) != hLen {
		return ErrBadH
	}
	for i := 0; i < len(h); i++ {
		c := h[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return ErrBadH
		}
	}
	return nil
}

// ValidateScopeID checks that s is exactly 64 lowercase hex chars.
func ValidateScopeID(s string) error {
	if len(s) != scopeIDLen {
		return ErrBadScopeID
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return ErrBadScopeID
		}
	}
	return nil
}

// ValidateTopic checks that t is exactly 43 chars of canonical (no padding,
// zero trailing bits) base64url — a derived topic.
func ValidateTopic(t string) error {
	if len(t) != topicLen {
		return ErrBadTopic
	}
	raw, err := base64.RawURLEncoding.DecodeString(t)
	if err != nil || len(raw) != sha256.Size {
		return ErrBadTopic
	}
	if base64.RawURLEncoding.EncodeToString(raw) != t {
		return ErrBadTopic
	}
	return nil
}
