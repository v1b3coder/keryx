// Package topic implements the relay's topic derivation (SPECIFICATION §3).
//
// Derivation is two-stage and deterministic, shared by the relay (publish
// side) and by the app (subscription side), so both agree without any
// exchange:
//
//	h     = base64url_nopad(sha256(input))                 // source hash (43 chars)
//	topic = "n-" + base64url_nopad(sha256("keryx/relay/v1|" + h))
//
// The relay receives only h (§5.1) and derives the topic; it never sees the
// derivation input. There is no type marker: h is opaque — neither the relay
// nor the providers can tell what kind of wake-up it is, and they need not.
// The type is resolved client-side from the app's own subscription state.
//
// Source hashes are 43-char base64url (no padding) of the SHA-256 over the
// derivation input; topics are 45 chars ("n-" + 43).
package topic

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
)

const (
	// prefix is the relay's topic namespace on shared providers.
	prefix = "n-"
	// salt is the static, public domain separator for the topic derivation.
	// It is not secret; it keeps topics distinct from h and from other
	// SHA-256 uses in the protocol.
	salt = "keryx/relay/v1|"

	// hLen is the fixed length of the source hash: 43 chars (32 bytes, no pad).
	hLen = 43
	// topicLen is the fixed length of a derived topic: prefix (2) + 43.
	topicLen = 45
)

var (
	// ErrBadH reports an h that is not exactly 43 chars of canonical base64url.
	ErrBadH = errors.New("source hash: must be exactly 43 chars of base64url (32 bytes)")
	// ErrBadTopic reports a topic string that is not a 45-char n- topic.
	ErrBadTopic = errors.New("topic: must be a 45-char n- topic")
)

// SourceHash computes the source hash for a derivation input:
// base64url_nopad(sha256(input)). Callers (publisher tooling, order engine)
// compute it — e.g. sha256(company_id + "|" + channel) for a channel wake-up,
// sha256(order_token) for an order wake-up. The relay never computes it; it
// only ever sees the result.
func SourceHash(input string) string {
	sum := sha256.Sum256([]byte(input))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Topic derives the delivery topic from a validated source hash.
// The relay accepts only h here — never raw topic names or derivation inputs.
func Topic(h string) (string, error) {
	if err := ValidateH(h); err != nil {
		return "", err
	}
	return TopicUnchecked(h), nil
}

// TopicUnchecked derives the topic from h without validating h. Use only
// after ValidateH (or when h is known-good); Topic is the safe entry point.
func TopicUnchecked(h string) string {
	sum := sha256.Sum256([]byte(salt + h))
	return prefix + base64.RawURLEncoding.EncodeToString(sum[:])
}

// ValidateH checks that h is exactly 43 chars of canonical (no padding,
// zero trailing bits) base64url, i.e. the SHA-256 source hash.
func ValidateH(h string) error {
	if len(h) != hLen {
		return ErrBadH
	}
	raw, err := base64.RawURLEncoding.DecodeString(h)
	if err != nil || len(raw) != sha256.Size {
		return ErrBadH
	}
	// Re-encoding catches non-canonical encodings, including non-zero
	// trailing bits in the final base64url character (43 chars = 258 bits,
	// only 256 are used).
	if base64.RawURLEncoding.EncodeToString(raw) != h {
		return ErrBadH
	}
	return nil
}

// ValidateTopic checks that t is a 45-char n- topic (prefix + 43-char
// base64url). Registrations accept only these.
func ValidateTopic(t string) error {
	if len(t) != topicLen || !strings.HasPrefix(t, prefix) {
		return ErrBadTopic
	}
	if err := ValidateH(t[len(prefix):]); err != nil {
		return ErrBadTopic
	}
	return nil
}
