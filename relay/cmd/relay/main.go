// Command relay is the Keryx notification relay (SPECIFICATION.md): a single
// binary that fans out wake-up signals to user devices over FCM topics,
// WebPush, and ntfy topics. Publishers are registration-free (API key only);
// all provider credentials live at the relay.
//
// Usage:
//
//	relay [serve]                start the server (default action)
//	relay publishers add         provision a publisher (prints the API key once)
//	relay publishers list        list publishers
//	relay vapid generate         generate a VAPID keypair for WebPush
package main

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
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
	"github.com/v1b3coder/keryx/relay/internal/push"
	"github.com/v1b3coder/keryx/relay/internal/relay"
	"github.com/v1b3coder/keryx/relay/internal/store"
)

// Config is the static server configuration (§8): flags with env fallbacks.
type Config struct {
	Listen           string
	DBPath           string
	FCMPath          string
	VAPIDPrivate     string
	VAPIDKeyFile     string
	VAPIDSub         string
	NTFYBase         string
	WebPushTTL       time.Duration
	WebPushConc      int
	MaxConcurrent    int
	QueueSize        int
	RegPerMin        int
	RegBurst         int
	PublishBurstMult int
	AppKey           string
	RetentionDays    int
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

func loadConfig(fs *flag.FlagSet) *Config {
	cfg := &Config{
		Listen:           env("RELAY_LISTEN", ":8080"),
		DBPath:           env("RELAY_DB", "relay.db"),
		FCMPath:          env("RELAY_FCM_SERVICE_ACCOUNT", ""),
		VAPIDPrivate:     env("RELAY_VAPID_PRIVATE", ""),
		VAPIDKeyFile:     env("RELAY_VAPID_KEY_FILE", ""),
		VAPIDSub:         env("RELAY_VAPID_SUB", ""),
		NTFYBase:         env("RELAY_NTFY_BASE", ""),
		WebPushTTL:       time.Duration(envInt("RELAY_WEBPUSH_TTL", 3600)) * time.Second,
		WebPushConc:      envInt("RELAY_WEBPUSH_CONCURRENCY", 32),
		MaxConcurrent:    envInt("RELAY_MAX_CONCURRENT", 64),
		QueueSize:        envInt("RELAY_QUEUE_SIZE", 4096),
		RegPerMin:        envInt("RELAY_REG_PER_MIN", 30),
		RegBurst:         envInt("RELAY_REG_BURST", 60),
		PublishBurstMult: envInt("RELAY_PUBLISH_BURST_MULT", 2),
		AppKey:           env("RELAY_APP_KEY", ""),
		RetentionDays:    envInt("RELAY_EVENT_RETENTION_DAYS", 30),
	}
	fs.StringVar(&cfg.Listen, "listen", cfg.Listen, "listen address")
	fs.StringVar(&cfg.DBPath, "db", cfg.DBPath, "SQLite database path")
	fs.StringVar(&cfg.FCMPath, "fcm-service-account", cfg.FCMPath, "FCM service-account JSON path (empty = FCM leg disabled)")
	fs.StringVar(&cfg.VAPIDPrivate, "vapid-private", cfg.VAPIDPrivate, "VAPID private key (base64url, 32 bytes)")
	fs.StringVar(&cfg.VAPIDKeyFile, "vapid-key-file", cfg.VAPIDKeyFile, "file containing the VAPID private key (alternative to -vapid-private)")
	fs.StringVar(&cfg.VAPIDSub, "vapid-sub", cfg.VAPIDSub, "VAPID sub contact (mailto:…; Chrome requires it)")
	fs.StringVar(&cfg.NTFYBase, "ntfy-base", cfg.NTFYBase, "global ntfy base URL (empty = ntfy leg disabled)")
	fs.DurationVar(&cfg.WebPushTTL, "webpush-ttl", cfg.WebPushTTL, "WebPush TTL (default 1h)")
	fs.IntVar(&cfg.WebPushConc, "webpush-concurrency", cfg.WebPushConc, "WebPush fan-out concurrency cap")
	fs.IntVar(&cfg.MaxConcurrent, "max-concurrent", cfg.MaxConcurrent, "max concurrent publish dispatches")
	fs.IntVar(&cfg.QueueSize, "queue-size", cfg.QueueSize, "publish queue size (202 under load)")
	fs.IntVar(&cfg.RegPerMin, "reg-per-min", cfg.RegPerMin, "per-IP registration throttle (per minute)")
	fs.IntVar(&cfg.RegBurst, "reg-burst", cfg.RegBurst, "per-IP registration burst")
	fs.IntVar(&cfg.PublishBurstMult, "publish-burst-mult", cfg.PublishBurstMult, "publish burst = rate_per_min × mult")
	fs.StringVar(&cfg.AppKey, "app-key", cfg.AppKey, "shared X-App-Key gate for registration endpoints (empty = open)")
	fs.IntVar(&cfg.RetentionDays, "event-retention-days", cfg.RetentionDays, "event_log retention (days)")
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
	case "publishers":
		if err := publishersCmd(args[1:], logger); err != nil {
			logger.Error("publishers", "err", err)
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
		fatalUsage("serve | publishers add|list | vapid generate")
	}
}

func fatalUsage(usage string) {
	fmt.Fprintf(os.Stderr, "usage: relay %s\n", usage)
	os.Exit(2)
}

func serve(cfg Config, logger *slog.Logger) error {
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer st.Close()
	if n, err := st.PruneEventLog(time.Duration(cfg.RetentionDays) * 24 * time.Hour); err == nil && n > 0 {
		logger.Info("pruned event_log", "rows", n)
	}

	var fcm relay.FCMLeg
	if cfg.FCMPath != "" {
		c, err := push.NewFCM(cfg.FCMPath, nil)
		if err != nil {
			return fmt.Errorf("fcm: %w", err)
		}
		fcm = c
	}
	var ntfy relay.NtfyLeg
	if cfg.NTFYBase != "" {
		n, err := push.NewNtfy(cfg.NTFYBase, nil)
		if err != nil {
			return fmt.Errorf("ntfy: %w", err)
		}
		ntfy = n
	}
	var wp relay.WebPushLeg
	if cfg.VAPIDPrivate == "" && cfg.VAPIDKeyFile != "" {
		raw, err := os.ReadFile(cfg.VAPIDKeyFile)
		if err != nil {
			return fmt.Errorf("vapid key file: %w", err)
		}
		cfg.VAPIDPrivate = strings.TrimSpace(string(raw))
	}
	if cfg.VAPIDPrivate != "" && cfg.VAPIDSub != "" {
		c, pub, err := push.NewWebPush(cfg.VAPIDPrivate, cfg.VAPIDSub, cfg.WebPushTTL, nil)
		if err != nil {
			return fmt.Errorf("webpush: %w", err)
		}
		logger.Info("webpush enabled", "vapid_public", pub)
		wp = c
	} else if cfg.VAPIDPrivate != "" || cfg.VAPIDSub != "" {
		return errors.New("vapid-private and vapid-sub must both be set (or both empty)")
	}

	d := relay.New(st, fcm, ntfy, wp, cfg.WebPushConc, logger)
	srv := api.New(st, d, api.Options{
		AppKey:           cfg.AppKey,
		MaxConcurrent:    cfg.MaxConcurrent,
		QueueSize:        cfg.QueueSize,
		RegPerMin:        cfg.RegPerMin,
		RegBurst:         cfg.RegBurst,
		PublishBurstMult: cfg.PublishBurstMult,
		Logger:           logger,
	})
	if fcm == nil {
		logger.Warn("FCM leg disabled (no service account configured)")
	}
	if ntfy == nil {
		logger.Warn("ntfy leg disabled (no base configured)")
	}
	if wp == nil {
		logger.Warn("WebPush leg disabled (no VAPID keypair configured)")
	}

	httpSrv := &http.Server{
		Addr:    cfg.Listen,
		Handler: srv.Handler(),
	}
	go maintenance(st, srv, cfg.RetentionDays, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.ListenAndServe() }()
	logger.Info("relay listening", "addr", cfg.Listen, "db", cfg.DBPath)
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

// maintenance prunes event_log and the per-IP throttle registry hourly.
func maintenance(st *store.Store, srv *api.Server, retentionDays int, logger *slog.Logger) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		if n, err := st.PruneEventLog(time.Duration(retentionDays) * 24 * time.Hour); err != nil {
			logger.Error("prune event_log", "err", err)
		} else if n > 0 {
			logger.Info("pruned event_log", "rows", n)
		}
		srv.Cleanup(2 * time.Hour)
	}
}

func publishersCmd(args []string, logger *slog.Logger) error {
	if len(args) == 0 {
		fatalUsage("publishers add|list")
	}
	switch args[0] {
	case "add":
		fs := flag.NewFlagSet("publishers add", flag.ExitOnError)
		name := fs.String("name", "", "publisher display name")
		company := fs.String("company", "", "company origin the key serves (informational)")
		rate := fs.Int("rate", 60, "publishes per minute")
		db := fs.String("db", env("RELAY_DB", "relay.db"), "SQLite database path")
		fs.Parse(args[1:])
		if *name == "" || *company == "" {
			fatalUsage("publishers add -name X -company company.example [-rate 60]")
		}
		st, err := store.Open(*db)
		if err != nil {
			return err
		}
		defer st.Close()
		key, err := st.CreatePublisher(*name, *company, *rate)
		if err != nil {
			return err
		}
		fmt.Printf("publisher %q (%s) created\n", *name, *company)
		fmt.Printf("API key (shown once, never stored):\n%s\n", key)
		return nil
	case "list":
		fs := flag.NewFlagSet("publishers list", flag.ExitOnError)
		db := fs.String("db", env("RELAY_DB", "relay.db"), "SQLite database path")
		fs.Parse(args[1:])
		st, err := store.Open(*db)
		if err != nil {
			return err
		}
		defer st.Close()
		ps, err := st.ListPublishers()
		if err != nil {
			return err
		}
		for _, p := range ps {
			fmt.Printf("%d\t%s\t%s\t%d/min\n", p.ID, p.Name, p.CompanyID, p.RatePerMin)
		}
		return nil
	default:
		fatalUsage("publishers add|list")
		return nil
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
