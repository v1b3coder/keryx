// Package join encodes and validates the join URL / QR payload
// (spec/core.md §3). The payload carries no metadata URL and no identity.
package join

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

// Payload is the join URL payload (spec/core.md §3).
type Payload struct {
	V            int      `json:"v"`
	Channels     []string `json:"channels"`
	PrivateFeeds []string `json:"private_feeds"`
}

// MaxPayloadBytes is the encoded payload cap (spec/core.md §3).
const MaxPayloadBytes = 512

// MaxPrivateFeeds is the private-feed cap per QR (spec/core.md §3).
const MaxPrivateFeeds = 2

// BuildPayload builds and validates a payload.
func BuildPayload(channels, privateFeeds []string) ([]byte, error) {
	return BuildPayloadOptions(channels, privateFeeds, false)
}

// BuildPayloadOptions allows plain-HTTP private feeds for local-dev origins
// when allowLocalHTTP is set (a documented dev-only exception, spec/feeds.md §3).
func BuildPayloadOptions(channels, privateFeeds []string, allowLocalHTTP bool) ([]byte, error) {
	if len(privateFeeds) > MaxPrivateFeeds {
		return nil, fmt.Errorf("at most %d private feeds per QR", MaxPrivateFeeds)
	}
	for _, c := range channels {
		if !validChannel(c) {
			return nil, fmt.Errorf("invalid channel name %q", c)
		}
	}
	for _, f := range privateFeeds {
		u, err := url.Parse(f)
		if err != nil {
			return nil, fmt.Errorf("private feed %q is not a URL", f)
		}
		if u.Scheme == "https" {
			continue
		}
		if allowLocalHTTP && u.Scheme == "http" && isLocalDevOrigin(u) {
			continue
		}
		return nil, fmt.Errorf("private feed %q must be an absolute HTTPS URL", f)
	}
	p := Payload{V: 1, Channels: channels, PrivateFeeds: privateFeeds}
	if p.Channels == nil {
		p.Channels = []string{}
	}
	if p.PrivateFeeds == nil {
		p.PrivateFeeds = []string{}
	}
	data, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	if len(data) > MaxPayloadBytes {
		return nil, fmt.Errorf("payload is %d bytes, cap is %d", len(data), MaxPayloadBytes)
	}
	return data, nil
}

// JoinURL builds the join URL for an origin + payload.
func JoinURL(origin string, payload []byte) (string, error) {
	return JoinURLOptions(origin, payload, false)
}

// JoinURLOptions allows a plain-HTTP local-dev origin when allowLocalHTTP is set.
func JoinURLOptions(origin string, payload []byte, allowLocalHTTP bool) (string, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid origin %q", origin)
	}
	if u.Scheme != "https" && !(allowLocalHTTP && u.Scheme == "http" && isLocalDevOrigin(u)) {
		return "", fmt.Errorf("join origin must be HTTPS")
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/join"
	q := u.Query()
	q.Set("p", base64.RawURLEncoding.EncodeToString(payload))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// ParseJoinURL parses a join URL back into its origin and payload.
func ParseJoinURL(raw string) (string, Payload, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", Payload{}, fmt.Errorf("invalid join URL %q", raw)
	}
	p := u.Query().Get("p")
	if p == "" {
		return u.Scheme + "://" + u.Host, Payload{V: 1, Channels: []string{}, PrivateFeeds: []string{}}, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(p)
	if err != nil {
		return "", Payload{}, fmt.Errorf("join payload: %w", err)
	}
	var payload Payload
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", Payload{}, fmt.Errorf("join payload: %w", err)
	}
	if payload.V != 1 {
		return "", Payload{}, fmt.Errorf("join payload: unknown version %d", payload.V)
	}
	return u.Scheme + "://" + u.Host, payload, nil
}

// QR renders a join URL as a PNG QR code.
func QR(joinURL string, size int) ([]byte, error) {
	if size <= 0 {
		size = 512
	}
	return qrcode.Encode(joinURL, qrcode.Medium, size)
}

func validChannel(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

// isLocalDevOrigin reports loopback / RFC 1918 hosts (the local demo).
func isLocalDevOrigin(u *url.URL) bool {
	host := u.Hostname()
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}
	var a, b int
	if n, err := fmt.Sscanf(host, "%d.%d.", &a, &b); n != 2 || err != nil {
		return false
	}
	if a == 10 {
		return true
	}
	if a == 172 && b >= 16 && b <= 31 {
		return true
	}
	return a == 192 && b == 168
}
