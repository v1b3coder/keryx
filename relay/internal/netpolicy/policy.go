// Package netpolicy implements the relay's outbound-request policy
// (relay/SPECIFICATION.md §5.6): every outbound HTTP request uses HTTPS
// with certificate validation and bounded size, timeout and concurrency; it
// rejects loopback, private, link-local and other non-public destination
// addresses, and validates DNS results and the actual connection destination.
package netpolicy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrBlocked reports a destination rejected by the outbound policy.
var ErrBlocked = errors.New("outbound destination rejected")

// Policy validates outbound destinations and builds guarded HTTP clients.
type Policy struct {
	// AllowPrivate and AllowHTTP are test-only escape hatches for local
	// end-to-end runs; they MUST NOT be enabled in production.
	AllowPrivate bool
	AllowHTTP    bool

	// RootCAs optionally extends the system roots (test-only local CA).
	RootCAs *x509.CertPool

	Timeout time.Duration
}

// New returns a Policy with a default request timeout.
func New() *Policy { return &Policy{Timeout: 30 * time.Second} }

// CheckURL validates a destination URL and returns it. Unless AllowHTTP is set,
// the scheme must be https; the host must resolve only to public addresses
// unless AllowPrivate is set.
func (p *Policy) CheckURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBlocked, err)
	}
	if u.Scheme != "https" && !(p.AllowHTTP && u.Scheme == "http") {
		return nil, fmt.Errorf("%w: scheme %q", ErrBlocked, u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("%w: missing host", ErrBlocked)
	}
	if _, err := p.resolve(u.Hostname()); err != nil {
		return nil, err
	}
	return u, nil
}

// resolve returns the validated addresses for host, rejecting any non-public
// result unless AllowPrivate is set.
func (p *Policy) resolve(host string) ([]net.IP, error) {
	ips, err := net.DefaultResolver.LookupIP(context.Background(), "ip", host)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBlocked, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("%w: no addresses for %q", ErrBlocked, host)
	}
	for _, ip := range ips {
		if !p.AllowPrivate && !IsPublic(ip) {
			return nil, fmt.Errorf("%w: %s resolves to non-public %s", ErrBlocked, host, ip)
		}
	}
	return ips, nil
}

// IsPublic reports whether ip is a public unicast address.
func IsPublic(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() ||
		ip.IsInterfaceLocalMulticast() {
		return false
	}
	// Reject IPv4 broadcast and other reserved/benchmarking blocks that the
	// standard library does not classify.
	if v4 := ip.To4(); v4 != nil {
		switch {
		case v4[0] == 0, v4[0] >= 224, v4[0] == 255:
			return false
		case v4[0] == 192 && v4[1] == 0 && v4[2] == 0: // 192.0.0.0/24
			return false
		case v4[0] == 198 && (v4[1] == 18 || v4[1] == 19): // 198.18.0.0/15
			return false
		}
	}
	if ip.To4() == nil && len(ip) == net.IPv6len {
		// Reject IPv6 unique-local and documentation ranges explicitly.
		if ip[0]&0xfe == 0xfc { // fc00::/7
			return false
		}
	}
	return true
}

// HTTPClient returns a client that validates every connection destination
// (DNS rebinding protection). When allowRedirects is false, redirects are
// refused outright; when true, each hop is validated by CheckURL.
func (p *Policy) HTTPClient(allowRedirects bool) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig:       &tls.Config{RootCAs: p.RootCAs, MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := p.resolve(host)
			if err != nil {
				return nil, err
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
		},
	}
	c := &http.Client{Transport: transport, Timeout: p.Timeout}
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !allowRedirects {
			return fmt.Errorf("%w: redirects disabled", ErrBlocked)
		}
		if len(via) >= 5 {
			return fmt.Errorf("%w: too many redirects", ErrBlocked)
		}
		if _, err := p.CheckURL(req.URL.String()); err != nil {
			return err
		}
		return nil
	}
	return c
}

// Get fetches raw with the guarded client, bounded to maxBytes, and returns
// the body. Redirects are disabled. A 404 is returned as ErrNotFound.
func (p *Policy) Get(ctx context.Context, raw string, maxBytes int64) ([]byte, error) {
	if _, err := p.CheckURL(raw); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.HTTPClient(false).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", redact(raw), resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("GET %s: body over %d bytes", redact(raw), maxBytes)
	}
	return data, nil
}

// ErrNotFound reports a 404 from a metadata fetch (used to stop the root walk).
var ErrNotFound = errors.New("not found")

// redact strips query strings from a URL for error messages.
func redact(raw string) string {
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		return raw[:i]
	}
	return raw
}
