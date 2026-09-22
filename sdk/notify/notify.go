// Package notify implements the publisher side of the relay wake-up
// (relay/SPECIFICATION.md §3–§5): the topic derivation shared with the app,
// the §4 wake-up envelope, and the publish request. The relay never holds
// publisher keys, so the signature is made here.
//
//	h     = hex(sha256(company_id + "|" + subject))
//	topic = base64url_nopad(sha256("keryx/relay/v1|" + OLPC({company_id, scope_id, h})))
//	signature = Ed25519("keryx/wakeup/v1|" + OLPC({v, t, seq}))
package notify

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/secure-systems-lab/go-securesystemslib/cjson"

	"github.com/v1b3coder/keryx/sdk/keys"
)

const (
	// topicSalt is the static, public domain separator for the topic derivation.
	topicSalt = "keryx/relay/v1|"
	// scopeSalt is the static, public domain separator for scope_id derivation.
	scopeSalt = "keryx/relay/scope/v1|"
	// wakeupDomain is the mandatory domain separator over the signed bytes.
	wakeupDomain = "keryx/wakeup/v1|"
)

// Request is one §5.1 publish request.
type Request struct {
	V         int    `json:"v"`
	CompanyID string `json:"company_id"`
	ScopeID   string `json:"scope_id"`
	H         string `json:"h"`
	Seq       int64  `json:"seq"`
	Sig       []Sig  `json:"sig"`
}

// Sig is one wake-up signature entry.
type Sig struct {
	KeyID string `json:"keyid"`
	Sig   string `json:"sig"`
}

// Result is the relay's publish response.
type Result struct {
	Topic      string `json:"topic"`
	Suppressed bool   `json:"suppressed"`
	Providers  struct {
		FCM     string `json:"fcm"`
		WebPush struct {
			Sent   int `json:"sent"`
			Failed int `json:"failed"`
			Dead   int `json:"dead"`
		} `json:"webpush"`
	} `json:"providers"`
	RequestID string `json:"request_id"`
	Status    string `json:"status"`
}

// PublicScopeID returns the scope_id for a public channel (relay §3.1):
// hex(sha256(scopeSalt + OLPC({kind: "public", channel}))).
func PublicScopeID(channel string) (string, error) {
	return descriptorID(map[string]any{"kind": "public", "channel": channel})
}

// SourceHash computes the source hash for a derivation input: hex(sha256(input)).
// A channel wake-up uses sha256(company_id + "|" + channel); an order wake-up
// uses sha256(company_id + "|" + order_token).
func SourceHash(input string) string {
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])
}

// Derive returns the delivery topic for a company, scope_id and source hash
// (relay §3).
func Derive(companyID, scopeID, h string) (string, error) {
	canonical, err := cjson.EncodeCanonical(map[string]any{
		"company_id": companyID,
		"scope_id":   scopeID,
		"h":          h,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte(topicSalt), canonical...))
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

// SignedBytes returns the exact bytes signed for a wake-up (§4.1): the domain
// separator followed by OLPC({v, t, seq}).
func SignedBytes(v int, t string, seq int64) ([]byte, error) {
	canonical, err := cjson.EncodeCanonical(map[string]any{"v": v, "t": t, "seq": seq})
	if err != nil {
		return nil, err
	}
	return append([]byte(wakeupDomain), canonical...), nil
}

// SignChannel signs one wake-up for a channel with its channel key and returns
// the §5.1 request. The publisher calls the relay's refresh endpoint before
// publishing when the metadata changed, and before the first publish.
func SignChannel(companyID, channel string, key *keys.Key, seq int64) (*Request, error) {
	if key.Role != keys.RoleChannel {
		return nil, fmt.Errorf("notify: %s is not a channel key", key.Name)
	}
	scopeID, err := PublicScopeID(channel)
	if err != nil {
		return nil, err
	}
	h := SourceHash(companyID + "|" + channel)
	topic, err := Derive(companyID, scopeID, h)
	if err != nil {
		return nil, err
	}
	signed, err := SignedBytes(1, topic, seq)
	if err != nil {
		return nil, err
	}
	sig := ed25519.Sign(key.Private(), signed)
	return &Request{
		V: 1, CompanyID: companyID, ScopeID: scopeID, H: h, Seq: seq,
		Sig: []Sig{{KeyID: key.KeyID(), Sig: base64.RawURLEncoding.EncodeToString(sig)}},
	}, nil
}

// Publish signs one wake-up and POSTs it to the relay (relay §5.1). The relay
// derives the topic from company_id/scope_id/h, verifies the signature against
// the company's verified TUF authorization, then fans out.
func Publish(ctx context.Context, relayURL, companyID, channel string, key *keys.Key, seq int64, client *http.Client) (*Result, error) {
	if seq <= 0 {
		seq = time.Now().Unix()
	}
	req, err := SignChannel(companyID, channel, key, seq)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = http.DefaultClient
	}
	url := strings.TrimSuffix(relayURL, "/") + "/v1/publish"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return nil, fmt.Errorf("relay publish: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out Result
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// descriptorID returns hex(sha256(scopeSalt + OLPC(descriptor))).
func descriptorID(descriptor map[string]any) (string, error) {
	canonical, err := cjson.EncodeCanonical(descriptor)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte(scopeSalt), canonical...))
	return hex.EncodeToString(sum[:]), nil
}
