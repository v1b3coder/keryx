package push

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// WebPush delivers 1:1 encrypted wake-ups to PWA subscriptions
// (SPECIFICATION §6.2): one VAPID keypair at the relay, RFC 8291 encryption
// (ECDH P-256 + HKDF + AES-128-GCM), no vendor registration.
type WebPush struct {
	private *ecdsa.PrivateKey // VAPID signing key
	sub     string            // mailto: contact (Chrome requires sub)
	ttl     time.Duration     // default 1h (§6.2)
	client  *http.Client
	backoff func(int) time.Duration
}

// NewWebPush builds a WebPush leg from a VAPID private key (32-byte P-256
// scalar, base64url), the mailto: contact, and a TTL. Returns the public key
// (base64url, 65-byte uncompressed) for embedding in the PWA.
func NewWebPush(vapidPrivateB64 string, sub string, ttl time.Duration, client *http.Client) (*WebPush, string, error) {
	priv, err := decodeP256Private(vapidPrivateB64)
	if err != nil {
		return nil, "", fmt.Errorf("vapid private key: %w", err)
	}
	if !strings.HasPrefix(sub, "mailto:") {
		return nil, "", errors.New("vapid sub must be a mailto: contact")
	}
	if ttl <= 0 {
		ttl = time.Hour
	}
	if client == nil {
		client = http.DefaultClient
	}
	pub := base64.RawURLEncoding.EncodeToString(elliptic.Marshal(elliptic.P256(), priv.X, priv.Y))
	return &WebPush{private: priv, sub: sub, ttl: ttl, client: client, backoff: ExpBackoff}, pub, nil
}

// Encrypt produces the RFC 8291 aes128gcm body (86-byte header + ciphertext)
// for a subscription's p256dh (65-byte uncompressed point) and auth secret
// (16 bytes) and a fresh ephemeral key and salt.
func Encrypt(p256dh, auth []byte, plaintext []byte) ([]byte, error) {
	eph, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	return encryptWith(p256dh, auth, eph, salt, plaintext)
}

// encryptWith is Encrypt with fixed ephemeral key and salt (for test vectors).
func encryptWith(p256dh, auth []byte, eph *ecdh.PrivateKey, salt, plaintext []byte) ([]byte, error) {
	if len(auth) != 16 {
		return nil, errors.New("auth secret must be 16 bytes")
	}
	if len(salt) != 16 {
		return nil, errors.New("salt must be 16 bytes")
	}
	uaPub, err := ecdh.P256().NewPublicKey(p256dh)
	if err != nil {
		return nil, fmt.Errorf("p256dh: %w", err)
	}
	shared, err := eph.ECDH(uaPub)
	if err != nil {
		return nil, fmt.Errorf("ecdh: %w", err)
	}

	// RFC 8291 §3.4: key combining.
	prkKey, err := hkdf.Extract(sha256.New, shared, auth)
	if err != nil {
		return nil, err
	}
	keyInfo := append([]byte("WebPush: info\x00"), uaPub.Bytes()...)
	keyInfo = append(keyInfo, eph.PublicKey().Bytes()...)
	ikm, err := hkdf.Expand(sha256.New, prkKey, string(keyInfo), 32)
	if err != nil {
		return nil, err
	}

	// RFC 8188 §2.2: content encryption key and nonce.
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}

	// Header: salt(16) || rs=4096(4) || idlen=65(1) || as_public(65) = 86 bytes.
	header := make([]byte, 0, 86)
	header = append(header, salt...)
	header = append(header, 0x00, 0x00, 0x10, 0x00)
	header = append(header, 65)
	header = append(header, eph.PublicKey().Bytes()...)

	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	// Single record: plaintext + 0x02 padding delimiter.
	msg := append(append([]byte{}, plaintext...), 0x02)
	ct := gcm.Seal(nil, nonce, msg, nil)
	return append(header, ct...), nil
}

// Send encrypts payload and POSTs it to the subscription endpoint with VAPID
// authorization. Returns ErrGone on 404/410 (delete the registration);
// transient errors (429/5xx) are retried with backoff before failing.
func (c *WebPush) Send(ctx context.Context, endpoint, p256dh, auth string, payload []byte) error {
	subKey, err := base64.RawURLEncoding.DecodeString(p256dh)
	if err != nil {
		return fmt.Errorf("p256dh: %w", err)
	}
	authSecret, err := base64.RawURLEncoding.DecodeString(auth)
	if err != nil {
		return fmt.Errorf("auth: %w", err)
	}
	body, err := Encrypt(subKey, authSecret, payload)
	if err != nil {
		return err
	}

	token, pubKey, err := c.vapidToken(endpoint)
	if err != nil {
		return err
	}
	resp, err := doWithRetry(ctx, c.client, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "vapid t="+token+", k="+pubKey)
		req.Header.Set("TTL", fmt.Sprint(int(c.ttl.Seconds())))
		req.Header.Set("Urgency", "normal")
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("Content-Encoding", "aes128gcm")
		return req, nil
	}, 3, c.backoff)
	if err != nil {
		// Never include the endpoint (a capability URL) in errors or logs
		// (relay/SPECIFICATION.md §5.1.1/§9).
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("webpush: request failed")
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusAccepted:
		return nil
	case http.StatusNotFound, http.StatusGone:
		return ErrGone
	default:
		return fmt.Errorf("webpush: HTTP %d", resp.StatusCode)
	}
}

// vapidToken builds the Authorization values: the ES256 JWT and the public
// key. aud is the endpoint origin (§6.2).
func (c *WebPush) vapidToken(endpoint string) (token, pubKey string, err error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", "", err
	}
	aud := u.Scheme + "://" + u.Host
	pubKey = base64.RawURLEncoding.EncodeToString(elliptic.Marshal(elliptic.P256(), c.private.X, c.private.Y))
	token, err = vapidJWT(c.private, aud, c.sub, time.Now())
	return token, pubKey, err
}

// vapidJWT signs a VAPID ES256 JWT (RFC 8292): aud = endpoint origin,
// exp = now + 12h (≤ 24h), sub = mailto: contact.
func vapidJWT(key *ecdsa.PrivateKey, aud, sub string, now time.Time) (string, error) {
	header, _ := json.Marshal(map[string]string{"typ": "JWT", "alg": "ES256"})
	claims, _ := json.Marshal(map[string]any{
		"aud": aud,
		"exp": now.Add(12 * time.Hour).Unix(),
		"sub": sub,
	})
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// decodeP256Private parses a 32-byte base64url P-256 scalar into an ecdsa key.
func decodeP256Private(b64 string) (*ecdsa.PrivateKey, error) {
	raw, err := base64.RawURLEncoding.DecodeString(b64)
	if err != nil {
		return nil, err
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("expected 32 bytes, got %d", len(raw))
	}
	d := new(big.Int).SetBytes(raw)
	ecdhPriv, err := ecdh.P256().NewPrivateKey(raw)
	if err != nil {
		return nil, err
	}
	x, y := elliptic.Unmarshal(elliptic.P256(), ecdhPriv.PublicKey().Bytes())
	return &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y},
		D:         d,
	}, nil
}
