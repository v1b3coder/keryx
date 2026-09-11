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
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func hkdfExtract(secret, salt []byte) []byte {
	out, err := hkdf.Extract(sha256.New, secret, salt)
	if err != nil {
		panic(err)
	}
	return out
}

func hkdfExpand(prk []byte, info string, n int) []byte {
	out, err := hkdf.Expand(sha256.New, prk, info, n)
	if err != nil {
		panic(err)
	}
	return out
}

func aesGCMOpen(t *testing.T, cek, nonce, ct []byte) []byte {
	t.Helper()
	block, err := aes.NewCipher(cek)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	out, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func bytesToBigInt(t *testing.T, b []byte) *big.Int {
	t.Helper()
	return new(big.Int).SetBytes(b)
}

func b64d(t *testing.T, s string) []byte {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestEncryptRFC8291Vector validates the encryption against the RFC 8291
// Appendix A intermediate values (fixed ephemeral key and salt).
func TestEncryptRFC8291Vector(t *testing.T) {
	asPrivate := b64d(t, "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw")
	uaPublic := b64d(t, "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4")
	salt := b64d(t, "DGv6ra1nlYgDCS1FRnbzlw")
	auth := b64d(t, "BTBZMqHH6r4Tts7J_aSIgg")
	plaintext := []byte("When I grow up, I want to be a watermelon")

	eph, err := ecdh.P256().NewPrivateKey(asPrivate)
	if err != nil {
		t.Fatal(err)
	}
	out, err := encryptWith(uaPublic, auth, eph, salt, plaintext)
	if err != nil {
		t.Fatal(err)
	}

	wantHeader := "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8"
	wantCipher := "8pfeW0KbunFT06SuDKoJH9Ql87S1QUrdirN6GcG7sFz1y1sqLgVi1VhjVkHsUoEsbI_0LpXMuGvnzQ"
	want := append(b64d(t, wantHeader), b64d(t, wantCipher)...)
	if string(out) != string(want) {
		t.Fatalf("encryptWith mismatch:\n got  %x\n want %x", out, want)
	}
	// The encrypted body is 86 (header) + len(plaintext)+1 (padding) + 16 (tag).
	if len(out) != 86+len(plaintext)+1+16 {
		t.Fatalf("unexpected length %d", len(out))
	}
}

func TestEncryptRoundtrip(t *testing.T) {
	uaPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := b64d(t, "BTBZMqHH6r4Tts7J_aSIgg")
	plaintext := []byte(`{"v":1,"t":"n-b-abc","n":3}`)
	body, err := Encrypt(uaPriv.PublicKey().Bytes(), auth, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	// Decrypt with the UA private key to prove the ciphertext is valid
	// (independent reimplementation using the same derived secrets).
	salt := body[:16]
	rs := body[16:20]
	idlen := body[20]
	if rs[0] != 0 || rs[1] != 0 || rs[2] != 0x10 || rs[3] != 0 || idlen != 65 {
		t.Fatalf("bad header: rs=%x idlen=%d", rs, idlen)
	}
	asPub := body[21 : 21+65]
	ct := body[21+65:]

	asPoint, err := ecdh.P256().NewPublicKey(asPub)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := uaPriv.ECDH(asPoint)
	if err != nil {
		t.Fatal(err)
	}
	prkKey := hkdfExtract(shared, auth)
	keyInfo := append([]byte("WebPush: info\x00"), uaPriv.PublicKey().Bytes()...)
	keyInfo = append(keyInfo, asPub...)
	ikm := hkdfExpand(prkKey, string(keyInfo), 32)
	prk := hkdfExtract(ikm, salt)
	cek := hkdfExpand(prk, "Content-Encoding: aes128gcm\x00", 16)
	nonce := hkdfExpand(prk, "Content-Encoding: nonce\x00", 12)
	got := aesGCMOpen(t, cek, nonce, ct)
	if !strings.HasSuffix(string(got), "\x02") {
		t.Fatalf("missing padding delimiter: %q", got)
	}
	if string(got[:len(got)-1]) != string(plaintext) {
		t.Fatalf("roundtrip mismatch: %q", got[:len(got)-1])
	}
}

func TestVapidJWT(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0)
	token, err := vapidJWT(key, "https://push.example.com", "mailto:ops@example.com", now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token parts = %d", len(parts))
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	var header map[string]string
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		t.Fatal(err)
	}
	if header["typ"] != "JWT" || header["alg"] != "ES256" {
		t.Fatalf("header = %v", header)
	}
	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		t.Fatal(err)
	}
	if claims["aud"] != "https://push.example.com" {
		t.Fatalf("aud = %v", claims["aud"])
	}
	if claims["sub"] != "mailto:ops@example.com" {
		t.Fatalf("sub = %v", claims["sub"])
	}
	if exp := int64(claims["exp"].(float64)); exp != now.Add(12*time.Hour).Unix() {
		t.Fatalf("exp = %d", exp)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(&key.PublicKey, digest[:], bytesToBigInt(t, sig[:32]), bytesToBigInt(t, sig[32:])) {
		t.Fatal("JWT signature does not verify")
	}
}

func TestWebPushSend(t *testing.T) {
	const privB64 = "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"
	uaPriv, _ := ecdh.P256().GenerateKey(rand.Reader)
	sub := struct {
		Endpoint string `json:"endpoint"`
		Keys     struct {
			P256DH string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
	}{}
	sub.Endpoint = "https://push.example.com/x"
	sub.Keys.P256DH = base64.RawURLEncoding.EncodeToString(uaPriv.PublicKey().Bytes())
	sub.Keys.Auth = "BTBZMqHH6r4Tts7J_aSIgg"

	var calls atomic.Int32
	var lastAuth, lastTTL, lastEncoding string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		lastAuth = r.Header.Get("Authorization")
		lastTTL = r.Header.Get("TTL")
		lastEncoding = r.Header.Get("Content-Encoding")
		if !strings.HasPrefix(lastAuth, "vapid t=") || !strings.Contains(lastAuth, ", k=") {
			t.Errorf("bad authorization header: %q", lastAuth)
		}
		if lastTTL != "3600" {
			t.Errorf("TTL = %q", lastTTL)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	wp, pubKey, err := NewWebPush(privB64, "mailto:ops@example.com", time.Hour, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if len(pubKey) != 87 {
		t.Fatalf("public key length %d", len(pubKey))
	}
	sub.Endpoint = srv.URL + "/x"
	if err := wp.Send(context.Background(), sub.Endpoint, sub.Keys.P256DH, sub.Keys.Auth, []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d", calls.Load())
	}
	if lastEncoding != "aes128gcm" {
		t.Fatalf("content-encoding = %q", lastEncoding)
	}
}

func TestWebPushSendErrors(t *testing.T) {
	const privB64 = "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"
	uaPriv, _ := ecdh.P256().GenerateKey(rand.Reader)
	p256dh := base64.RawURLEncoding.EncodeToString(uaPriv.PublicKey().Bytes())
	auth := "BTBZMqHH6r4Tts7J_aSIgg"

	tests := []struct {
		name      string
		status    int
		statuses  []int // if set, respond with these in order
		expectErr bool
		wantErr   error
		wantCalls int32
	}{
		{name: "gone 404", status: http.StatusNotFound, wantErr: ErrGone, wantCalls: 1},
		{name: "gone 410", status: http.StatusGone, wantErr: ErrGone, wantCalls: 1},
		{name: "retry then ok", statuses: []int{500, 503, 201}, wantCalls: 3},
		{name: "retry exhausted", statuses: []int{429, 500, 503}, expectErr: true, wantCalls: 3},
		{name: "bad request", status: http.StatusBadRequest, expectErr: true, wantCalls: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := int(calls.Add(1))
				if tc.statuses != nil {
					if n > len(tc.statuses) {
						w.WriteHeader(500)
						return
					}
					w.WriteHeader(tc.statuses[n-1])
					return
				}
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()
			wp, _, err := NewWebPush(privB64, "mailto:ops@example.com", time.Hour, srv.Client())
			if err != nil {
				t.Fatal(err)
			}
			wp.backoff = func(int) time.Duration { return 0 }
			err = wp.Send(context.Background(), srv.URL+"/x", p256dh, auth, []byte(`{"v":1}`))
			if tc.wantErr != nil && err != tc.wantErr {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.expectErr && err == nil {
				t.Fatalf("err = nil, want error")
			}
			if !tc.expectErr && tc.wantErr == nil && err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if tc.statuses == nil && calls.Load() != tc.wantCalls {
				t.Fatalf("calls = %d, want %d", calls.Load(), tc.wantCalls)
			}
			if tc.statuses != nil && calls.Load() != int32(len(tc.statuses)) {
				t.Fatalf("calls = %d, want %d", calls.Load(), len(tc.statuses))
			}
		})
	}
}

func TestNewWebPushValidation(t *testing.T) {
	if _, _, err := NewWebPush("not-a-key", "mailto:x@y", time.Hour, nil); err == nil {
		t.Fatal("accepted bad private key")
	}
	key, _ := base64.RawURLEncoding.DecodeString("yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw")
	if _, _, err := NewWebPush(string(key), "ops@example.com", time.Hour, nil); err == nil {
		t.Fatal("accepted non-mailto sub")
	}
}
