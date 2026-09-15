// Package push implements the relay's two delivery legs
// (relay/SPECIFICATION.md §6): FCM topics and the UnifiedPush/WebPush
// endpoint leg. All legs are best-effort; wake-up payloads carry no content.
package push

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ErrGone marks a subscription the push service reports dead (404/410):
// the dispatch counts it as dead and performs no registry write (§6.2).
var ErrGone = errors.New("push subscription is gone (404/410)")

// ErrCredentials marks provider credential failures (FCM 401/403): an
// operator alarm, not a per-publish error.
var ErrCredentials = errors.New("push provider credentials rejected")

// doWithRetry performs req, retrying transient failures (network errors,
// 429, 5xx) with exponential backoff, up to maxAttempts. It returns the
// final response or the last error.
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
