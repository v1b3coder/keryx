package push

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// FCM delivers wake-ups over the app's shared Firebase project via FCM topics
// (SPECIFICATION §6.1). One service account for the whole relay; OAuth2
// access tokens are cached until ~5 min before expiry. The leg is enabled
// only when a service-account JSON path is configured.
type FCM struct {
	projectID   string
	clientEmail string
	privateKey  *rsa.PrivateKey
	tokenURI    string
	endpoint    string // FCM HTTP v1 base (tests override)
	client      *http.Client
	backoff     func(int) time.Duration

	mu          sync.Mutex
	accessToken string
	tokenExpiry time.Time
}

// ErrEmptyTopic marks FCM topic-level errors (404 / INVALID_ARGUMENT): an
// empty topic is not an error — the publish counts as dispatched (§6.1).
var ErrEmptyTopic = errors.New("fcm: topic not found or invalid (counted as dispatched)")

const fcmScope = "https://www.googleapis.com/auth/firebase.messaging"

// NewFCM loads the service-account JSON (path) and builds the leg.
func NewFCM(serviceAccountPath string, client *http.Client) (*FCM, error) {
	raw, err := os.ReadFile(serviceAccountPath)
	if err != nil {
		return nil, err
	}
	var sa struct {
		ProjectID   string `json:"project_id"`
		ClientEmail string `json:"client_email"`
		PrivateKey  string `json:"private_key"`
		TokenURI    string `json:"token_uri"`
	}
	if err := json.Unmarshal(raw, &sa); err != nil {
		return nil, fmt.Errorf("service account: %w", err)
	}
	if sa.ProjectID == "" || sa.ClientEmail == "" || sa.PrivateKey == "" {
		return nil, errors.New("service account: missing project_id/client_email/private_key")
	}
	if sa.TokenURI == "" {
		sa.TokenURI = "https://oauth2.googleapis.com/token"
	}
	block, _ := pem.Decode([]byte(sa.PrivateKey))
	if block == nil {
		return nil, errors.New("service account: private_key is not PEM")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("service account private key: %w", err)
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("service account private key: not RSA")
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &FCM{
		projectID:   sa.ProjectID,
		clientEmail: sa.ClientEmail,
		privateKey:  rsaKey,
		tokenURI:    sa.TokenURI,
		endpoint:    "https://fcm.googleapis.com",
		client:      client,
		backoff:     ExpBackoff,
	}, nil
}

// AccessToken returns a cached OAuth2 access token, refreshing when it is
// within 5 minutes of expiry (§6.1).
func (c *FCM) AccessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.accessToken != "" && time.Until(c.tokenExpiry) > 5*time.Minute {
		return c.accessToken, nil
	}
	token, expiry, err := c.fetchToken(ctx)
	if err != nil {
		return "", err
	}
	c.accessToken = token
	c.tokenExpiry = expiry
	return token, nil
}

// fetchToken performs the Google OAuth2 service-account JWT-bearer flow.
func (c *FCM) fetchToken(ctx context.Context) (string, time.Time, error) {
	now := time.Now()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{
		"iss":   c.clientEmail,
		"scope": fcmScope,
		"aud":   c.tokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	})
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, c.privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", time.Time{}, err
	}
	assertion := signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)

	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.client.Do(req)
	if err != nil {
		return "", time.Time{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", time.Time{}, fmt.Errorf("oauth token: HTTP %d: %s", resp.StatusCode, body)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tok); err != nil {
		return "", time.Time{}, err
	}
	if tok.AccessToken == "" {
		return "", time.Time{}, errors.New("oauth token: empty access_token")
	}
	expiry := now.Add(time.Duration(tok.ExpiresIn) * time.Second)
	return tok.AccessToken, expiry, nil
}

// Send publishes a wake-up envelope to an FCM topic. wakeup is the §4
// envelope as one JSON string. Returns nil on accepted sends (including empty
// topics), ErrEmptyTopic for 404/INVALID_ARGUMENT (counted as dispatched),
// ErrCredentials for 401/403 (operator alarm), and a plain error for other
// failures.
func (c *FCM) Send(ctx context.Context, topic string, wakeup []byte) error {
	token, err := c.AccessToken(ctx)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{
		"message": map[string]any{
			"topic": topic,
			"data":  map[string]string{"wakeup": string(wakeup)},
			"android": map[string]any{
				"priority": "normal",
			},
			"apns": map[string]any{
				"headers": map[string]string{
					"apns-push-type": "background",
					"apns-priority":  "5",
				},
				"payload": map[string]any{
					"aps": map[string]any{"content-available": 1},
				},
			},
		},
	})
	if err != nil {
		return err
	}
	resp, err := doWithRetry(ctx, c.client, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			c.endpoint+"/v1/projects/"+c.projectID+"/messages:send",
			strings.NewReader(string(body)))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	}, 3, c.backoff)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrCredentials
	case http.StatusNotFound:
		return ErrEmptyTopic
	}
	// Other 4xx: inspect FCM error status (INVALID_ARGUMENT on a topic is an
	// empty-topic signal too, per §6.1).
	var apiErr struct {
		Error struct {
			Status string `json:"status"`
		} `json:"error"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = json.Unmarshal(raw, &apiErr)
	if apiErr.Error.Status == "INVALID_ARGUMENT" || apiErr.Error.Status == "NOT_FOUND" {
		return ErrEmptyTopic
	}
	return fmt.Errorf("fcm: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
}
