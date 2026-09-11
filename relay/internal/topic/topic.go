// Package topic implements the relay's topic derivation (SPECIFICATION §3).
//
// Derivation is two-stage and deterministic, shared by the relay (publish
// side) and by the app (subscription side), so both agree without any
// exchange:
//
//	h     = base64url_nopad(sha256(sep + input))          // source hash (43 chars)
//	topic = "n-b-" / "n-o-" + base64url_nopad(sha256("keryx/relay/v1|" + h))
//
// The relay receives only h (§5.1) and derives the topic; it never sees the
// derivation input (company_id + channel, or the order token).
//
// Source hashes are 43-char base64url (no padding) of the SHA-256 over the
// derivation input (SPECIFICATION §3); topics are 47 chars.
package topic

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// Kind selects the domain separator and the topic prefix.
type Kind string

const (
	// Channel is a public channel wake-up: h = sha256("b|" + company_id + "|" + channel).
	Channel Kind = "channel"
	// Order is a private per-order thread wake-up: h = sha256("o|" + order_token).
	Order Kind = "order"
)

const (
	domainB = "b|"
	domainO = "o|"
	prefixB = "n-b-"
	prefixO = "n-o-"

	// Salt is the static, public domain separator for the topic derivation.
	// It is not secret; it keeps topics distinct from h and from other
	// SHA-256 uses in the protocol.
	salt = "keryx/relay/v1|"

	// hLen is the fixed length of the source hash: 43 chars (32 bytes, no pad).
	hLen = 43
	// topicLen is the fixed length of a derived topic: prefix (4) + 43.
	topicLen = 47
)

var (
	// ErrBadH reports an h that is not exactly 43 chars of canonical base64url.
	ErrBadH = errors.New("source hash: must be exactly 43 chars of base64url (32 bytes)")
	// ErrBadTopic reports a topic string that is not a 47-char n-b-/n-o- topic.
	ErrBadTopic = errors.New("topic: must be a 47-char n-b-/n-o- topic")
)

func (k Kind) sep() string {
	if k == Order {
		return domainO
	}
	return domainB
}

func (k Kind) prefix() string {
	if k == Order {
		return prefixO
	}
	return prefixB
}

// SourceHash computes the source hash for a derivation input:
// sha256(sep + input), encoded base64url without padding (43 chars).
// For Channel the input is company_id + "|" + channel; for Order it is the
// order_token. Callers (publisher tooling, order engine) compute this; the
// relay never does — it only ever sees the result.
func SourceHash(k Kind, input string) string {
	sum := sha256.Sum256([]byte(k.sep() + input))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Topic derives the delivery topic from a validated source hash.
// The relay accepts only h here — never raw topic names or derivation inputs.
func Topic(k Kind, h string) (string, error) {
	if err := ValidateH(h); err != nil {
		return "", err
	}
	return TopicUnchecked(k, h), nil
}

// TopicUnchecked derives the topic from h without validating h. Use only
// after ValidateH (or when h is known-good); Topic is the safe entry point.
func TopicUnchecked(k Kind, h string) string {
	sum := sha256.Sum256([]byte(salt + h))
	return k.prefix() + base64.RawURLEncoding.EncodeToString(sum[:])
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

// ValidateTopic checks that t is a 47-char n-b-/n-o- topic (prefix + 43-char
// base64url). Registrations accept only these.
func ValidateTopic(t string) error {
	if len(t) != topicLen || (!strings.HasPrefix(t, prefixB) && !strings.HasPrefix(t, prefixO)) {
		return ErrBadTopic
	}
	if err := ValidateH(t[len(prefixB):]); err != nil {
		return ErrBadTopic
	}
	return nil
}

// KindOfTopic returns the kind encoded in a validated topic string.
func KindOfTopic(t string) (Kind, error) {
	if err := ValidateTopic(t); err != nil {
		return "", err
	}
	if strings.HasPrefix(t, prefixO) {
		return Order, nil
	}
	return Channel, nil
}

// String implements fmt.Stringer for Kind.
func (k Kind) String() string { return string(k) }

// ParseKind validates a kind string from the API.
func ParseKind(s string) (Kind, error) {
	switch Kind(s) {
	case Channel:
		return Channel, nil
	case Order:
		return Order, nil
	default:
		return "", fmt.Errorf("kind: must be %q or %q", Channel, Order)
	}
}
