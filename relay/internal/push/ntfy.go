package push

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Ntfy publishes wake-ups to topic-based ntfy (§6.3): one publish per topic
// to the global ntfy base; no per-publisher credentials. The leg is disabled
// when no base is configured.
type Ntfy struct {
	base    string
	client  *http.Client
	backoff func(int) time.Duration
}

// NewNtfy builds the ntfy leg for a base URL like https://ntfy.sh.
func NewNtfy(base string, client *http.Client) (*Ntfy, error) {
	base = strings.TrimSuffix(base, "/")
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("ntfy base: %q is not a valid http(s) URL", base)
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &Ntfy{base: base, client: client, backoff: ExpBackoff}, nil
}

// Send publishes the §4 payload JSON to <base>/<topic>. Returns nil on 2xx;
// a plain error otherwise (4xx are final, 5xx/timeouts are retried).
func (n *Ntfy) Send(ctx context.Context, topic string, payload []byte) error {
	resp, err := doWithRetry(ctx, n.client, func() (*http.Request, error) {
		return postJSON(ctx, n.base+"/"+topic, payload, nil)
	}, 3, n.backoff)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("ntfy: HTTP %d", resp.StatusCode)
}
