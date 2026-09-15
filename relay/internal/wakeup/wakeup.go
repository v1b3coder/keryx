// Package wakeup implements the relay's wake-up envelope
// (relay/SPECIFICATION.md §4): the canonical signed bytes and the Ed25519
// threshold verification shared by the relay (at publish, §5.5) and the app (on
// delivery, §4.2).
//
//	signature = Ed25519("keryx/wakeup/v1|" + OLPC({v, t, seq}))
//
// The `sig` array is always nonempty; at least `threshold` distinct authorized
// keyids must verify over the same signed bytes. Extra signatures from other keys
// neither count nor invalidate an otherwise satisfied threshold.
package wakeup

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/secure-systems-lab/go-securesystemslib/cjson"
)

// domain is the mandatory domain separator over the signed bytes (§4.1).
const domain = "keryx/wakeup/v1|"

// MaxSeq is the largest representable `seq` value (2^53-1, §4).
const MaxSeq int64 = 9007199254740991

// Sig is one wake-up signature entry (§4.1).
type Sig struct {
	KeyID string `json:"keyid"`
	Sig   string `json:"sig"`
}

// Wakeup is the §4 wake-up envelope. On the topic leg (FCM) T may be omitted
// when the adapter recovers the delivery topic; the endpoint leg always carries
// it. T is always part of the signed bytes.
type Wakeup struct {
	V   int64  `json:"v"`
	T   string `json:"t,omitempty"`
	Seq int64  `json:"seq"`
	Sig []Sig  `json:"sig"`
}

// signed is the exact OLPC input: {v, t, seq} with the sig array excluded.
type signed struct {
	V   int64  `json:"v"`
	T   string `json:"t"`
	Seq int64  `json:"seq"`
}

// SignedBytes returns the exact bytes the publisher signs: the domain separator
// followed by OLPC({v, t, seq}) as UTF-8.
func SignedBytes(v int64, t string, seq int64) ([]byte, error) {
	canonical, err := cjson.EncodeCanonical(signed{V: v, T: t, Seq: seq})
	if err != nil {
		return nil, err
	}
	return append([]byte(domain), canonical...), nil
}

// Parse strictly parses a wake-up envelope (§4): unknown versions, unknown
// fields, duplicate JSON member names, malformed encodings and non-integer or
// out-of-range counters are rejected.
func Parse(data []byte) (*Wakeup, error) {
	if err := checkNoDuplicateKeys(data); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	var raw struct {
		V   json.Number `json:"v"`
		T   *string     `json:"t"`
		Seq json.Number `json:"seq"`
		Sig []Sig       `json:"sig"`
	}
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("wakeup: %w", err)
	}
	if err := ensureEOF(dec); err != nil {
		return nil, err
	}
	v, err := raw.V.Int64()
	if err != nil {
		return nil, fmt.Errorf("wakeup: v: %w", err)
	}
	seq, err := raw.Seq.Int64()
	if err != nil {
		return nil, fmt.Errorf("wakeup: seq: %w", err)
	}
	if v != 1 {
		return nil, fmt.Errorf("wakeup: unknown version %d", v)
	}
	if seq < 1 || seq > MaxSeq {
		return nil, fmt.Errorf("wakeup: seq out of range")
	}
	if raw.T == nil {
		return nil, errors.New("wakeup: t is required on the endpoint leg")
	}
	if len(raw.Sig) == 0 {
		return nil, errors.New("wakeup: sig must be a nonempty array")
	}
	return &Wakeup{V: v, T: *raw.T, Seq: seq, Sig: raw.Sig}, nil
}

// Verify checks that at least threshold distinct authorized keyids verify over
// the signed bytes for (v, t, seq). Duplicate keyids do not count twice; a known
// keyid whose signature is invalid rejects the wake-up; unknown keyids are
// ignored.
func Verify(v int64, t string, seq int64, sigs []Sig, keys map[string]ed25519.PublicKey, threshold int) error {
	if threshold < 1 {
		threshold = 1
	}
	if len(sigs) == 0 {
		return errors.New("wakeup: sig must be a nonempty array")
	}
	signedBytes, err := SignedBytes(v, t, seq)
	if err != nil {
		return err
	}
	seen := make(map[string]bool, len(sigs))
	valid := 0
	for _, s := range sigs {
		pub, ok := keys[s.KeyID]
		if !ok {
			continue
		}
		if seen[s.KeyID] {
			continue
		}
		seen[s.KeyID] = true
		raw, err := base64.RawURLEncoding.DecodeString(s.Sig)
		if err != nil || len(raw) != ed25519.SignatureSize || !ed25519.Verify(pub, signedBytes, raw) {
			return fmt.Errorf("wakeup: signature by key %s invalid", s.KeyID)
		}
		valid++
	}
	if valid < threshold {
		return fmt.Errorf("wakeup: %d/%d valid signatures", valid, threshold)
	}
	return nil
}

// checkNoDuplicateKeys rejects duplicate JSON member names at any nesting level
// (§4: duplicate JSON member names are rejected).
func checkNoDuplicateKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return walk(dec)
}

func walk(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return err
			}
			key, _ := kt.(string)
			if seen[key] {
				return fmt.Errorf("wakeup: duplicate JSON member %q", key)
			}
			seen[key] = true
			if err := walk(dec); err != nil {
				return err
			}
		}
		_, err := dec.Token()
		return err
	case '[':
		for dec.More() {
			if err := walk(dec); err != nil {
				return err
			}
		}
		_, err := dec.Token()
		return err
	}
	return nil
}

func ensureEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("wakeup: trailing data")
	}
	return nil
}

// SortKeyIDs returns the keyids of sigs in lexical order (stable error
// messages and deterministic tests).
func SortKeyIDs(sigs []Sig) []string {
	out := make([]string, 0, len(sigs))
	for _, s := range sigs {
		out = append(out, s.KeyID)
	}
	sort.Strings(out)
	return out
}
