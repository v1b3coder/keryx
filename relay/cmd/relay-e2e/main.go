// Command relay-e2e is a TEST-ONLY harness for the browser end-to-end run: it
// serves a resealed copy of the demo repository over local HTTPS, starts the relay
// in-process, and exposes /test/info and /test/publish so a real browser (the PWA)
// can complete the relay → push → notification path. Never deploy it.
package main

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sigstore/sigstore/pkg/signature"
	"github.com/theupdateframework/go-tuf/v2/metadata"

	"github.com/v1b3coder/keryx/relay/internal/api"
	"github.com/v1b3coder/keryx/relay/internal/companytuf"
	"github.com/v1b3coder/keryx/relay/internal/netpolicy"
	"github.com/v1b3coder/keryx/relay/internal/push"
	"github.com/v1b3coder/keryx/relay/internal/relay"
	"github.com/v1b3coder/keryx/relay/internal/scope"
	"github.com/v1b3coder/keryx/relay/internal/store"
	"github.com/v1b3coder/keryx/relay/internal/topic"
	"github.com/v1b3coder/keryx/relay/internal/tufclient"
	"github.com/v1b3coder/keryx/relay/internal/wakeup"
)

type config struct {
	demoDir      string
	keysDir      string
	relayListen  string
	httpsListen  string
	vapidPrivate string
	vapidSub     string
	companyID    string
	channel      string
}

type harness struct {
	cfg        config
	store      *store.Store
	companies  *companytuf.Manager
	dispatcher *relay.Dispatcher
	handler    http.Handler
	serveDir   string
	repoBase   string
	httpsURL   string
	joinURL    string
	scopeID    string
	h          string
	topic      string
	key        ed25519.PrivateKey
	keyID      string
	vapidPub   string
}

func main() {
	var cfg config
	flag.StringVar(&cfg.demoDir, "demo", "../../keryx-demo", "demo repository source")
	flag.StringVar(&cfg.keysDir, "keys", os.Getenv("KERYX_KEYSTORE"), "key store directory (outside the demo repo)")
	flag.StringVar(&cfg.relayListen, "relay-listen", "127.0.0.1:18099", "relay listen address")
	flag.StringVar(&cfg.httpsListen, "https-listen", "127.0.0.1:8443", "demo HTTPS listen address")
	flag.StringVar(&cfg.vapidPrivate, "vapid-private", "", "VAPID private key (base64url)")
	flag.StringVar(&cfg.vapidSub, "vapid-sub", "mailto:ops@example.com", "VAPID sub contact")
	flag.StringVar(&cfg.companyID, "company", "127.0.0.1", "canonical company_id")
	flag.StringVar(&cfg.channel, "channel", "security", "channel to publish on")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(cfg, logger); err != nil {
		logger.Error("relay-e2e", "err", err)
		os.Exit(1)
	}
}

func run(cfg config, logger *slog.Logger) error {
	if _, err := os.Stat(filepath.Join(cfg.demoDir, ".well-known", "keryx", "root.json")); err != nil {
		return fmt.Errorf("demo repository: %w", err)
	}
	if cfg.keysDir == "" {
		return fmt.Errorf("key store required (-keys or KERYX_KEYSTORE)")
	}
	h := &harness{cfg: cfg}

	// 1. Self-signed CA + leaf for 127.0.0.1, served over local HTTPS.
	caPEM, leaf, pool, err := selfSignedCert()
	if err != nil {
		return err
	}
	caFile := filepath.Join(os.TempDir(), "relay-e2e-ca.pem")
	if err := os.WriteFile(caFile, caPEM, 0o644); err != nil {
		return err
	}
	h.httpsURL = "https://" + cfg.httpsListen
	repoBase := h.httpsURL + "/keryx/"

	// 2. Reseal a copy of the demo repository to point at this server.
	h.serveDir, err = os.MkdirTemp("", "relay-e2e-demo")
	if err != nil {
		return err
	}
	if err := copyDir(cfg.demoDir, h.serveDir); err != nil {
		return err
	}
	if err := resealRoot(h.serveDir, cfg.keysDir, repoBase); err != nil {
		return err
	}

	// 3. The demo channel key signs the wake-ups.
	h.key, h.keyID, err = channelKey(cfg.keysDir, cfg.channel)
	if err != nil {
		return err
	}

	// 4. Relay components with the test CA and private destinations allowed.
	policy := netpolicy.New()
	policy.AllowPrivate = true
	policy.RootCAs = pool
	vapid := cfg.vapidPrivate
	if vapid == "" {
		key, err := ecdh.P256().GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		vapid = base64.RawURLEncoding.EncodeToString(key.Bytes())
	}
	webpush, vapidPub, err := push.NewWebPush(vapid, cfg.vapidSub, time.Hour, policy.HTTPClient(false))
	if err != nil {
		return err
	}
	h.vapidPub = vapidPub

	h.store, err = store.Open(filepath.Join(os.TempDir(), "relay-e2e-main.db"), filepath.Join(os.TempDir(), "relay-e2e-reg.db"))
	if err != nil {
		return err
	}
	defer h.store.Close()
	if _, err := h.store.ClosePendingEvents(time.Now().UTC()); err != nil {
		return err
	}
	client := tufclient.New(policy)
	client.TestWellKnown = map[string]string{cfg.companyID: h.httpsURL + "/.well-known/keryx/"}
	h.companies = companytuf.New(h.store, client, companytuf.Options{Logger: logger})
	if err := h.companies.Load(); err != nil {
		return err
	}
	h.companies.Start()
	defer h.companies.Stop()
	h.dispatcher = relay.New(h.store, nil, webpush, relay.Options{Logger: logger})
	apiSrv := api.New(h.store, h.dispatcher, h.companies, api.Options{
		Policy:              policy,
		CORSOrigins:         []string{"http://localhost:4173"},
		ApprovedPushOrigins: []string{"https://jmt17.google.com", "https://fcm.googleapis.com"},
		Logger:              logger,
	})
	h.handler = apiSrv.Handler()

	// 5. Serve the resealed repository over local HTTPS.
	mux := http.NewServeMux()
	mux.Handle("/", corsFiles(http.FileServer(http.Dir(h.serveDir))))
	httpsSrv := &http.Server{
		Addr:      cfg.httpsListen,
		Handler:   mux,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{leaf}},
	}
	ln, err := net.Listen("tcp", cfg.httpsListen)
	if err != nil {
		return err
	}
	go func() { _ = httpsSrv.ServeTLS(ln, "", "") }()
	defer httpsSrv.Close()

	// 6. The relay API + the test routes.
	testMux := http.NewServeMux()
	testMux.Handle("/test/info", corsFiles(http.HandlerFunc(h.info)))
	testMux.Handle("/test/publish", corsFiles(http.HandlerFunc(h.publish)))
	testMux.Handle("/", h.handler)
	relaySrv := &http.Server{Addr: cfg.relayListen, Handler: testMux}
	relayLn, err := net.Listen("tcp", cfg.relayListen)
	if err != nil {
		return err
	}
	go func() { _ = relaySrv.Serve(relayLn) }()
	defer relaySrv.Close()

	// 7. Register the company (TOFU) and wait for the scope table.
	rec := httptestPost(h.handler, "/v1/companies/"+cfg.companyID+"/refresh", "")
	if rec.Code != http.StatusAccepted {
		return fmt.Errorf("refresh: HTTP %d", rec.Code)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := h.store.Company(cfg.companyID); err == nil && !c.RefreshedAt.IsZero() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	persisted, err := h.store.Company(cfg.companyID)
	if err != nil || persisted.RefreshedAt.IsZero() {
		return errors.New("company refresh did not complete")
	}
	table, err := persisted.State().Table()
	if err != nil {
		return err
	}
	h.scopeID, err = scope.PublicScopeID(cfg.channel)
	if err != nil {
		return err
	}
	entry, ok := table.Lookup(h.scopeID)
	if !ok {
		return fmt.Errorf("scope %s not found", h.scopeID)
	}
	if _, ok := entry.Keys[h.keyID]; !ok {
		return fmt.Errorf("key %s not authorized", h.keyID)
	}
	h.h = topic.SourceHash(cfg.companyID + "|" + cfg.channel)
	h.topic, err = topic.Derive(cfg.companyID, h.scopeID, h.h)
	if err != nil {
		return err
	}

	joinPayload, _ := json.Marshal(map[string]any{"v": 1, "channels": []string{cfg.channel}})
	h.joinURL = h.httpsURL + "/join?p=" + base64.RawURLEncoding.EncodeToString(joinPayload)
	logger.Info("harness ready",
		"join_url", h.joinURL,
		"relay", "http://"+cfg.relayListen,
		"topic", h.topic,
		"scope_id", h.scopeID,
		"vapid_public", h.vapidPub,
		"ca_file", caFile)
	return relaySrv.Serve(relayLn)
}

// --- /test/info and /test/publish ---

func (h *harness) info(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"companyId":   h.cfg.companyID,
		"channel":     h.cfg.channel,
		"scopeId":     h.scopeID,
		"h":           h.h,
		"topic":       h.topic,
		"joinUrl":     h.joinURL,
		"vapidPublic": h.vapidPub,
	})
}

func (h *harness) publish(w http.ResponseWriter, r *http.Request) {
	seq := time.Now().Unix()
	msg, err := wakeup.SignedBytes(1, h.topic, seq)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	sig := ed25519.Sign(h.key, msg)
	body, _ := json.Marshal(map[string]any{
		"v":          1,
		"company_id": h.cfg.companyID,
		"scope_id":   h.scopeID,
		"h":          h.h,
		"seq":        seq,
		"sig": []map[string]string{
			{"keyid": h.keyID, "sig": base64.RawURLEncoding.EncodeToString(sig)},
		},
	})
	rec := httptestPost(h.handler, "/v1/publish", string(body))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(rec.Code)
	_, _ = w.Write(rec.Body.Bytes())
}

func httptestPost(h http.Handler, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// corsFiles serves the demo repository with the CORS headers the PWA needs
// (the demo site is a different origin from the app in this harness).
func corsFiles(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		next.ServeHTTP(w, r)
	})
}

// --- TLS / repository helpers ---

func selfSignedCert() (caPEM []byte, leaf tls.Certificate, pool *x509.CertPool, err error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, tls.Certificate{}, nil, err
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "relay-e2e-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, tls.Certificate{}, nil, err
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, tls.Certificate{}, nil, err
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, tls.Certificate{}, nil, err
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		return nil, tls.Certificate{}, nil, err
	}
	leaf = tls.Certificate{Certificate: [][]byte{leafDER}, PrivateKey: leafKey}
	system, err := x509.SystemCertPool()
	if err == nil {
		pool = system
	} else {
		pool = x509.NewCertPool()
	}
	pool.AddCert(caCert)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), leaf, pool, nil
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

func resealRoot(dir, keysDir, repoBase string) error {
	raw, err := os.ReadFile(filepath.Join(keysDir, "master.json"))
	if err != nil {
		return err
	}
	var record struct {
		SeedHex string `json:"seed_hex"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		return err
	}
	seed, err := hex.DecodeString(record.SeedHex)
	if err != nil || len(seed) != ed25519.SeedSize {
		return fmt.Errorf("master seed: %v", err)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	for _, name := range []string{"root.json", "1.root.json"} {
		path := filepath.Join(dir, ".well-known", "keryx", name)
		rootBytes, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var root metadata.Metadata[metadata.RootType]
		if _, err := root.FromBytes(rootBytes); err != nil {
			return err
		}
		custom, _ := root.Signed.UnrecognizedFields["custom"].(map[string]any)
		custom["repo_base"] = repoBase
		root.ClearSignatures()
		signer, err := signature.LoadSigner(priv, 0)
		if err != nil {
			return err
		}
		if _, err := root.Sign(signer); err != nil {
			return err
		}
		out, err := root.ToBytes(true)
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, out, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func channelKey(keysDir, channel string) (ed25519.PrivateKey, string, error) {
	raw, err := os.ReadFile(filepath.Join(keysDir, channel+".json"))
	if err != nil {
		return nil, "", err
	}
	var record struct {
		SeedHex string `json:"seed_hex"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, "", err
	}
	seed, err := hex.DecodeString(record.SeedHex)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, "", fmt.Errorf("channel seed: %v", err)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	key, err := metadata.KeyFromPublicKey(priv.Public())
	if err != nil {
		return nil, "", err
	}
	id, err := key.ID()
	if err != nil {
		return nil, "", err
	}
	return priv, id, nil
}
