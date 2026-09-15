// Command relay is the Keryx notification relay (relay/SPECIFICATION.md): a
// single binary that fans out signed wake-up signals — never content — to user
// devices over FCM topics and UnifiedPush/WebPush endpoints. Publishers are
// registration-free: publishing is authorized by signatures under each company's
// verified TUF authorization, and all provider credentials live at the relay.
//
// Usage:
//
//	relay [serve]        start the server (default action)
//	relay vapid generate generate a VAPID keypair for WebPush
package main

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/v1b3coder/keryx/relay/internal/api"
	"github.com/v1b3coder/keryx/relay/internal/companytuf"
	"github.com/v1b3coder/keryx/relay/internal/netpolicy"
	"github.com/v1b3coder/keryx/relay/internal/push"
	"github.com/v1b3coder/keryx/relay/internal/relay"
	"github.com/v1b3coder/keryx/relay/internal/store"
	"github.com/v1b3coder/keryx/relay/internal/tufclient"
)

// Config is the static server configuration (§8): flags with env fallbacks.
type Config struct {
	Listen       string
	DBPath       string
	RegistryPath string

	FCMPath      string
	VAPIDPrivate string
	VAPIDKeyFile string
	VAPIDSub     string

	WebPushTTL         time.Duration
	WebPushConcurrency int
	FCMPerMin          int
	WebPushPerMin      int

	MaxConcurrent  int
	QueueSize      int
	FastPathMax    int
	BatchSize      int
	ReplayCapacity int
	CapabilityTTL  time.Duration
	SeqFutureTol   time.Duration

	PublishPerMin     int
	PublishBurst      int
	IPPerMin          int
	IPBurst           int
	RegPerMin         int
	RegBurst          int
	ProbePerMin       int
	ProbeBurst        int
	GlobalProbePerMin int
	GlobalProbeBurst  int

	RetentionDays  int
	RegistryGCDays int

	RefreshInterval    time.Duration
	RefreshCadence     time.Duration
	RefreshConcurrency int
	DiscoveryPerMin    int
	DiscoveryBurst     int

	Debug       bool
	DebugAPIKey string

	ApprovedPushOrigins      []string
	AllowPrivateDestinations bool
	AllowHTTPDestinations    bool
	TestCAFile               string
}

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return def
}

func envBool(name string, def bool) bool {
	if v := os.Getenv(name); v != "" {
		return v == "1" || v == "true" || v == "yes"
	}
	return def
}

func envList(name string) []string {
	v := os.Getenv(name)
	if v == "" {
		return nil
	}
	return strings.Split(v, ",")
}

func loadConfig(fs *flag.FlagSet) *Config {
	cfg := &Config{
		Listen:                   env("RELAY_LISTEN", ":8080"),
		DBPath:                   env("RELAY_DB", "relay.db"),
		RegistryPath:             env("RELAY_REGISTRY_DB", "registry.db"),
		FCMPath:                  env("RELAY_FCM_SERVICE_ACCOUNT", ""),
		VAPIDPrivate:             env("RELAY_VAPID_PRIVATE", ""),
		VAPIDKeyFile:             env("RELAY_VAPID_KEY_FILE", ""),
		VAPIDSub:                 env("RELAY_VAPID_SUB", ""),
		WebPushTTL:               time.Duration(envInt("RELAY_WEBPUSH_TTL", 3600)) * time.Second,
		WebPushConcurrency:       envInt("RELAY_WEBPUSH_CONCURRENCY", 64),
		FCMPerMin:                envInt("RELAY_FCM_PER_MIN", 600),
		WebPushPerMin:            envInt("RELAY_WEBPUSH_PER_MIN", 600),
		MaxConcurrent:            envInt("RELAY_MAX_CONCURRENT", 64),
		QueueSize:                envInt("RELAY_QUEUE_SIZE", 4096),
		FastPathMax:              envInt("RELAY_FAST_PATH_MAX", 500),
		BatchSize:                envInt("RELAY_BATCH_SIZE", 500),
		ReplayCapacity:           envInt("RELAY_REPLAY_CAPACITY", 100000),
		CapabilityTTL:            time.Duration(envInt("RELAY_CAPABILITY_TTL_SECONDS", 3600)) * time.Second,
		SeqFutureTol:             time.Duration(envInt("RELAY_SEQ_FUTURE_TOLERANCE_SECONDS", 300)) * time.Second,
		PublishPerMin:            envInt("RELAY_PUBLISH_PER_MIN", 60),
		PublishBurst:             envInt("RELAY_PUBLISH_BURST", 120),
		IPPerMin:                 envInt("RELAY_PUBLISH_IP_PER_MIN", 120),
		IPBurst:                  envInt("RELAY_PUBLISH_IP_BURST", 240),
		RegPerMin:                envInt("RELAY_REG_PER_MIN", 30),
		RegBurst:                 envInt("RELAY_REG_BURST", 60),
		ProbePerMin:              envInt("RELAY_PROBE_PER_MIN", 120),
		ProbeBurst:               envInt("RELAY_PROBE_BURST", 240),
		GlobalProbePerMin:        envInt("RELAY_PROBE_GLOBAL_PER_MIN", 600),
		GlobalProbeBurst:         envInt("RELAY_PROBE_GLOBAL_BURST", 1200),
		RetentionDays:            envInt("RELAY_EVENT_RETENTION_DAYS", 30),
		RegistryGCDays:           envInt("RELAY_REGISTRY_GC_DAYS", 30),
		RefreshInterval:          time.Duration(envInt("RELAY_REFRESH_INTERVAL_SECONDS", 60)) * time.Second,
		RefreshCadence:           time.Duration(envInt("RELAY_REFRESH_CADENCE_SECONDS", 43200)) * time.Second,
		RefreshConcurrency:       envInt("RELAY_REFRESH_CONCURRENCY", 4),
		DiscoveryPerMin:          envInt("RELAY_DISCOVERY_PER_MIN", 60),
		DiscoveryBurst:           envInt("RELAY_DISCOVERY_BURST", 120),
		Debug:                    envBool("RELAY_DEBUG_TRANSPORT", false),
		DebugAPIKey:              env("RELAY_DEBUG_API_KEY", ""),
		ApprovedPushOrigins:      envList("RELAY_PUSH_ORIGINS"),
		AllowPrivateDestinations: envBool("RELAY_ALLOW_PRIVATE_DESTINATIONS", false),
		AllowHTTPDestinations:    envBool("RELAY_ALLOW_HTTP_DESTINATIONS", false),
		TestCAFile:               env("RELAY_TEST_CA_FILE", ""),
	}
	fs.StringVar(&cfg.Listen, "listen", cfg.Listen, "listen address")
	fs.StringVar(&cfg.DBPath, "db", cfg.DBPath, "main SQLite database path")
	fs.StringVar(&cfg.RegistryPath, "registry-db", cfg.RegistryPath, "registry SQLite database path")
	fs.StringVar(&cfg.FCMPath, "fcm-service-account", cfg.FCMPath, "FCM service-account JSON path (empty = FCM leg disabled)")
	fs.StringVar(&cfg.VAPIDPrivate, "vapid-private", cfg.VAPIDPrivate, "VAPID private key (base64url, 32 bytes)")
	fs.StringVar(&cfg.VAPIDKeyFile, "vapid-key-file", cfg.VAPIDKeyFile, "file containing the VAPID private key")
	fs.StringVar(&cfg.VAPIDSub, "vapid-sub", cfg.VAPIDSub, "VAPID sub contact (mailto:…; Chrome requires it)")
	fs.DurationVar(&cfg.WebPushTTL, "webpush-ttl", cfg.WebPushTTL, "WebPush TTL (default 1h)")
	fs.IntVar(&cfg.WebPushConcurrency, "webpush-concurrency", cfg.WebPushConcurrency, "endpoint-leg concurrency cap")
	fs.IntVar(&cfg.FCMPerMin, "fcm-per-min", cfg.FCMPerMin, "FCM outbound publish budget (0 = unpaced)")
	fs.IntVar(&cfg.WebPushPerMin, "webpush-per-min", cfg.WebPushPerMin, "endpoint-leg outbound budget (0 = unpaced)")
	fs.IntVar(&cfg.MaxConcurrent, "max-concurrent", cfg.MaxConcurrent, "max concurrent dispatches")
	fs.IntVar(&cfg.QueueSize, "queue-size", cfg.QueueSize, "dispatch queue capacity (503 when full)")
	fs.IntVar(&cfg.FastPathMax, "fast-path-max", cfg.FastPathMax, "synchronous fan-out registration threshold")
	fs.IntVar(&cfg.BatchSize, "batch-size", cfg.BatchSize, "registry streaming batch size")
	fs.IntVar(&cfg.ReplayCapacity, "replay-capacity", cfg.ReplayCapacity, "in-memory replay cache entries")
	fs.DurationVar(&cfg.CapabilityTTL, "capability-ttl", cfg.CapabilityTTL, "dispatch-status capability TTL (default 1h)")
	fs.DurationVar(&cfg.SeqFutureTol, "seq-future-tolerance", cfg.SeqFutureTol, "reject seq ahead of the clock by more than this")
	fs.IntVar(&cfg.PublishPerMin, "publish-per-min", cfg.PublishPerMin, "per-company publish budget")
	fs.IntVar(&cfg.PublishBurst, "publish-burst", cfg.PublishBurst, "per-company publish burst")
	fs.IntVar(&cfg.IPPerMin, "publish-ip-per-min", cfg.IPPerMin, "unauthenticated publish budget per IP")
	fs.IntVar(&cfg.IPBurst, "publish-ip-burst", cfg.IPBurst, "unauthenticated publish burst per IP")
	fs.IntVar(&cfg.RegPerMin, "reg-per-min", cfg.RegPerMin, "per-IP registration throttle")
	fs.IntVar(&cfg.RegBurst, "reg-burst", cfg.RegBurst, "per-IP registration burst")
	fs.IntVar(&cfg.ProbePerMin, "probe-per-min", cfg.ProbePerMin, "per-IP status probe budget")
	fs.IntVar(&cfg.ProbeBurst, "probe-burst", cfg.ProbeBurst, "per-IP status probe burst")
	fs.IntVar(&cfg.GlobalProbePerMin, "probe-global-per-min", cfg.GlobalProbePerMin, "global status probe budget")
	fs.IntVar(&cfg.GlobalProbeBurst, "probe-global-burst", cfg.GlobalProbeBurst, "global status probe burst")
	fs.IntVar(&cfg.RetentionDays, "event-retention-days", cfg.RetentionDays, "event_log retention (days)")
	fs.IntVar(&cfg.RegistryGCDays, "registry-gc-days", cfg.RegistryGCDays, "registry GC TTL (days)")
	fs.DurationVar(&cfg.RefreshInterval, "refresh-interval", cfg.RefreshInterval, "per-company sync interval (min 60s)")
	fs.DurationVar(&cfg.RefreshCadence, "refresh-cadence", cfg.RefreshCadence, "background TUF refresh cadence")
	fs.IntVar(&cfg.RefreshConcurrency, "refresh-concurrency", cfg.RefreshConcurrency, "concurrent company fetches")
	fs.IntVar(&cfg.DiscoveryPerMin, "discovery-per-min", cfg.DiscoveryPerMin, "unknown-company discovery budget per IP")
	fs.IntVar(&cfg.DiscoveryBurst, "discovery-burst", cfg.DiscoveryBurst, "unknown-company discovery burst per IP")
	fs.BoolVar(&cfg.Debug, "debug-transport", cfg.Debug, "transport-debug mode (test only; alternate publish routes)")
	fs.StringVar(&cfg.DebugAPIKey, "debug-api-key", cfg.DebugAPIKey, "debug-mode API key (required with -debug-transport)")
	fs.BoolVar(&cfg.AllowPrivateDestinations, "allow-private-destinations", cfg.AllowPrivateDestinations, "TEST ONLY: allow private/loopback outbound destinations")
	fs.BoolVar(&cfg.AllowHTTPDestinations, "allow-http-destinations", cfg.AllowHTTPDestinations, "TEST ONLY: allow http outbound destinations")
	fs.StringVar(&cfg.TestCAFile, "test-ca-file", cfg.TestCAFile, "TEST ONLY: extra CA bundle for outbound HTTPS")
	return cfg
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	args := os.Args[1:]
	if len(args) == 0 {
		args = []string{"serve"}
	}
	switch args[0] {
	case "serve":
		fs := flag.NewFlagSet("serve", flag.ExitOnError)
		cfg := loadConfig(fs)
		fs.Parse(args[1:])
		if err := serve(*cfg, logger); err != nil {
			logger.Error("relay", "err", err)
			os.Exit(1)
		}
	case "vapid":
		if len(args) < 2 || args[1] != "generate" {
			fatalUsage("vapid generate")
		}
		if err := vapidGenerate(); err != nil {
			logger.Error("vapid", "err", err)
			os.Exit(1)
		}
	default:
		fatalUsage("serve | vapid generate")
	}
}

func fatalUsage(usage string) {
	fmt.Fprintf(os.Stderr, "usage: relay %s\n", usage)
	os.Exit(2)
}

func serve(cfg Config, logger *slog.Logger) error {
	if cfg.Debug && cfg.DebugAPIKey == "" {
		return errors.New("debug-transport requires debug-api-key")
	}
	st, err := store.Open(cfg.DBPath, cfg.RegistryPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer st.Close()
	if n, err := st.ClosePendingEvents(time.Now().UTC()); err != nil {
		logger.Error("close pending events", "err", err)
	} else if n > 0 {
		logger.Info("closed pending events after restart", "rows", n)
	}

	policy := netpolicy.New()
	policy.AllowPrivate = cfg.AllowPrivateDestinations
	policy.AllowHTTP = cfg.AllowHTTPDestinations
	if cfg.TestCAFile != "" {
		pem, err := os.ReadFile(cfg.TestCAFile)
		if err != nil {
			return fmt.Errorf("test CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return errors.New("test CA file: no certificates found")
		}
		policy.RootCAs = pool
	}

	client := tufclient.New(policy)
	companies := companytuf.New(st, client, companytuf.Options{
		Interval:        cfg.RefreshInterval,
		Cadence:         cfg.RefreshCadence,
		Concurrency:     cfg.RefreshConcurrency,
		DiscoveryPerMin: cfg.DiscoveryPerMin,
		DiscoveryBurst:  cfg.DiscoveryBurst,
		Logger:          logger,
	})
	if err := companies.Load(); err != nil {
		return fmt.Errorf("load company trust state: %w", err)
	}
	companies.Start()
	defer companies.Stop()

	var fcm *push.FCM
	if cfg.FCMPath != "" {
		c, err := push.NewFCM(cfg.FCMPath, policy.HTTPClient(false))
		if err != nil {
			return fmt.Errorf("fcm: %w", err)
		}
		fcm = c
	}
	var wp *push.WebPush
	if cfg.VAPIDPrivate == "" && cfg.VAPIDKeyFile != "" {
		raw, err := os.ReadFile(cfg.VAPIDKeyFile)
		if err != nil {
			return fmt.Errorf("vapid key file: %w", err)
		}
		cfg.VAPIDPrivate = strings.TrimSpace(string(raw))
	}
	if cfg.VAPIDPrivate != "" && cfg.VAPIDSub != "" {
		c, pub, err := push.NewWebPush(cfg.VAPIDPrivate, cfg.VAPIDSub, cfg.WebPushTTL, policy.HTTPClient(false))
		if err != nil {
			return fmt.Errorf("webpush: %w", err)
		}
		logger.Info("webpush enabled", "vapid_public", pub)
		wp = c
	} else if cfg.VAPIDPrivate != "" || cfg.VAPIDSub != "" {
		return errors.New("vapid-private and vapid-sub must both be set (or both empty)")
	}

	dispatcher := relay.New(st, fcm, wp, relay.Options{
		FastPathMax:        cfg.FastPathMax,
		QueueSize:          cfg.QueueSize,
		MaxConcurrent:      cfg.MaxConcurrent,
		WebPushConcurrency: cfg.WebPushConcurrency,
		FCMPerMin:          cfg.FCMPerMin,
		WebPushPerMin:      cfg.WebPushPerMin,
		BatchSize:          cfg.BatchSize,
		ReplayCapacity:     cfg.ReplayCapacity,
		CapabilityTTL:      cfg.CapabilityTTL,
		Logger:             logger,
	})
	srv := api.New(st, dispatcher, companies, api.Options{
		Debug:               cfg.Debug,
		DebugAPIKey:         cfg.DebugAPIKey,
		BodyMax:             1 << 20,
		PublishPerMin:       cfg.PublishPerMin,
		PublishBurst:        cfg.PublishBurst,
		IPPerMin:            cfg.IPPerMin,
		IPBurst:             cfg.IPBurst,
		RegPerMin:           cfg.RegPerMin,
		RegBurst:            cfg.RegBurst,
		ProbePerMin:         cfg.ProbePerMin,
		ProbeBurst:          cfg.ProbeBurst,
		GlobalProbePerMin:   cfg.GlobalProbePerMin,
		GlobalProbeBurst:    cfg.GlobalProbeBurst,
		SeqFutureTolerance:  cfg.SeqFutureTol,
		ApprovedPushOrigins: cfg.ApprovedPushOrigins,
		Policy:              policy,
		Logger:              logger,
	})
	if fcm == nil {
		logger.Warn("FCM leg disabled (no service account configured)")
	}
	if wp == nil {
		logger.Warn("WebPush leg disabled (no VAPID keypair configured)")
	}
	if cfg.Debug {
		logger.Warn("transport-debug mode enabled; production publish routes are not mounted")
	}
	if cfg.AllowPrivateDestinations || cfg.AllowHTTPDestinations || cfg.TestCAFile != "" {
		logger.Warn("test-only outbound destination overrides enabled; do not use in production")
	}

	httpSrv := &http.Server{Addr: cfg.Listen, Handler: srv.Handler()}
	go maintenance(st, dispatcher, srv, cfg, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.ListenAndServe() }()
	logger.Info("relay listening", "addr", cfg.Listen, "db", cfg.DBPath, "registry_db", cfg.RegistryPath, "debug", cfg.Debug)
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutdownCtx)
	}
}

// maintenance sweeps the registry by last_seen, prunes the event_log and drops
// idle rate-limit state on a low-frequency cadence (§7).
func maintenance(st *store.Store, d *relay.Dispatcher, srv *api.Server, cfg Config, logger *slog.Logger) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		d.Sweep(time.Duration(cfg.RegistryGCDays)*24*time.Hour, time.Duration(cfg.RetentionDays)*24*time.Hour)
		srv.Cleanup(2 * time.Hour)
	}
}

// vapidGenerate prints a fresh VAPID keypair (§6.2): public for the PWA's
// applicationServerKey, private for the relay config.
func vapidGenerate() error {
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	fmt.Printf("public:  %s\n", base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()))
	fmt.Printf("private: %s\n", base64.RawURLEncoding.EncodeToString(priv.Bytes()))
	fmt.Println("# public goes into the PWA (applicationServerKey); private into relay config (RELAY_VAPID_PRIVATE)")
	return nil
}
