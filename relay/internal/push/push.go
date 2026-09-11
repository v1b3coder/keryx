// Package push implements the three delivery legs of the relay
// (SPECIFICATION §6): FCM topics, WebPush (RFC 8291 encryption + VAPID), and
// ntfy topics. All legs are best-effort; wake-up payloads carry no content.
package push

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Wakeup is the §4 canonical wake-up payload.
//
// On topic-based legs (FCM, ntfy) the identifier travels in the topic itself
// and the payload carries only v and the counters. On registry legs (WebPush)
// there is no topic at delivery, so t MUST be present and the device maps it
// to the followed company/channel/order locally.
type Wakeup struct {
	V   int    `json:"v"`
	T   string `json:"t,omitempty"`
	N   *int   `json:"n,omitempty"`
	Seq *int   `json:"seq,omitempty"`
}

// JSON returns the §4 payload as JSON bytes. t is included only when set
// (WebPush); topic-based legs omit it.
func (w Wakeup) JSON() ([]byte, error) { return json.Marshal(w) }

// DataMap returns the payload as a map of string values for FCM data fields.
// Field values in FCM data are strings; the app parses them (§6.1).
func (w Wakeup) DataMap() map[string]string {
	m := map[string]string{"v": fmt.Sprint(w.V)}
	if w.T != "" {
		m["t"] = w.T
	}
	if w.N != nil {
		m["n"] = fmt.Sprint(*w.N)
	}
	if w.Seq != nil {
		m["seq"] = fmt.Sprint(*w.Seq)
	}
	return m
}

// ErrGone marks a subscription that the push service says is dead
// (404/410): the relay deletes the registration and counts it as removed.
var ErrGone = errors.New("push subscription is gone (404/410)")

// ErrCredentials marks provider credential failures (FCM 401/403): an
// operator alarm, not a per-publish error.
var ErrCredentials = errors.New("push provider credentials rejected")

// doWithRetry performs req, retrying transient failures (network errors,
// 429, 5xx) with exponential backoff, up to maxAttempts. It returns the
// final response (body consumed and closed) or the last error.
func doWithRetry(ctx context.Context, client *http.Client, req func() (*http.Request, error), maxAttempts int, backoff func(int) time.Duration) (*http.Response, error) {
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		r, err := req()
		if err != nil {
			return nil, err
		}
		r = r.WithContext(ctx)
		resp, err := client.Do(r)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = err
		} else {
			if !isTransient(resp.StatusCode) {
				return resp, nil
			}
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		if attempt < maxAttempts {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff(attempt)):
			}
		}
	}
	return nil, lastErr
}

// isTransient reports whether a status code should be retried (429, 5xx).
func isTransient(code int) bool { return code == http.StatusTooManyRequests || code >= 500 }

// ExpBackoff is the default retry backoff: 1s, 2s, 4s, ...
func ExpBackoff(attempt int) time.Duration {
	d := time.Second
	for i := 1; i < attempt; i++ {
		d *= 2
	}
	return d
}

// readBody reads and closes resp.Body (bounded).
func readBody(resp *http.Response, limit int64) ([]byte, error) {
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// postJSON builds a JSON POST request.
func postJSON(ctx context.Context, url string, body []byte, headers map[string]string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req, nil
}
