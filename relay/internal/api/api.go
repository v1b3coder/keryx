// Package api implements the relay's HTTP surface
// (relay/SPECIFICATION.md §5): the signature-authorized publish API, the
// unsigned company synchronization endpoint, the dispatch-status probe, and the
// UnifiedPush/WebPush registration API, plus the isolated transport-debug mode
// (§5.7).
package api

import (
	"bytes"
	"crypto/ecdh"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/v1b3coder/keryx/relay/internal/company"
	"github.com/v1b3coder/keryx/relay/internal/companytuf"
	"github.com/v1b3coder/keryx/relay/internal/netpolicy"
	"github.com/v1b3coder/keryx/relay/internal/push"
	"github.com/v1b3coder/keryx/relay/internal/ratelimit"
	"github.com/v1b3coder/keryx/relay/internal/relay"
	"github.com/v1b3coder/keryx/relay/internal/scope"
	"github.com/v1b3coder/keryx/relay/internal/store"
	"github.com/v1b3coder/keryx/relay/internal/topic"
	"github.com/v1b3coder/keryx/relay/internal/wakeup"
)

// Options configure a Server.
type Options struct {
	Debug       bool
	DebugAPIKey string
	BodyMax     int64

	PublishPerMin     int           // per-company publish budget, default 60
	PublishBurst      int           // default 120
	IPPerMin          int           // unauthenticated publish per IP, default 120
	IPBurst           int           // default 240
	RegPerMin         int           // registration per IP, default 30
	RegBurst          int           // default 60
	ProbePerMin       int           // status probe per IP, default 120
	ProbeBurst        int           // default 240
	GlobalProbePerMin int           // default 600
	GlobalProbeBurst  int           // default 1200
	TestIPPerMin      int           // self-test per IP, default 10
	TestIPBurst       int           // default 20
	TestPerMin        int           // self-test per registration, default 3
	TestBurst         int           // default 5
	TestTTL           time.Duration // self-test capability lifetime, default 5m

	SeqFutureTolerance   time.Duration // default 5m
	ApprovedPushOrigins  []string      // additional approved push-service origins; "scheme://*.host" wildcards allowed
	PushOriginsAny       bool          // accept any public HTTPS endpoint (opt-in §5.6 mode)
	TrustedProxyIPHeader string        // client-IP header from a trusted platform proxy (e.g. "Fly-Client-IP")
	CORSOrigins          []string      // allowed PWA origins (cross-origin API)
	Policy               *netpolicy.Policy
	Logger               *slog.Logger
}

// Server serves the relay HTTP API.
type Server struct {
	store      *store.Store
	dispatcher *relay.Dispatcher
	companies  *companytuf.Manager
	logger     *slog.Logger
	opts       Options
	policy     *netpolicy.Policy

	ipLimiter   *ratelimit.Limiter
	regLimiter  *ratelimit.Limiter
	probeIP     *ratelimit.Limiter
	probeGlobal *ratelimit.Limiter
	testIP      *ratelimit.Limiter
	testMu      sync.Mutex
	testReg     map[string]*ratelimit.Limiter

	fcmMu    sync.Mutex
	fcmTests map[string]fcmTest

	pubMu       sync.Mutex
	pubLimiters map[string]*ratelimit.Limiter

	pushOrigins    map[string]bool
	pushOriginsAny bool
	clientIPHeader string
	corsOrigins    map[string]bool
	lastActivity   atomic.Int64
}

// New builds the API server.
func New(st *store.Store, d *relay.Dispatcher, companies *companytuf.Manager, opts Options) *Server {
	if opts.BodyMax <= 0 {
		opts.BodyMax = 1 << 20
	}
	if opts.PublishPerMin <= 0 {
		opts.PublishPerMin = 60
	}
	if opts.PublishBurst <= 0 {
		opts.PublishBurst = 120
	}
	if opts.IPPerMin <= 0 {
		opts.IPPerMin = 120
	}
	if opts.IPBurst <= 0 {
		opts.IPBurst = 240
	}
	if opts.RegPerMin <= 0 {
		opts.RegPerMin = 30
	}
	if opts.RegBurst <= 0 {
		opts.RegBurst = 60
	}
	if opts.ProbePerMin <= 0 {
		opts.ProbePerMin = 120
	}
	if opts.ProbeBurst <= 0 {
		opts.ProbeBurst = 240
	}
	if opts.GlobalProbePerMin <= 0 {
		opts.GlobalProbePerMin = 600
	}
	if opts.GlobalProbeBurst <= 0 {
		opts.GlobalProbeBurst = 1200
	}
	if opts.TestIPPerMin <= 0 {
		opts.TestIPPerMin = 10
	}
	if opts.TestIPBurst <= 0 {
		opts.TestIPBurst = 20
	}
	if opts.TestPerMin <= 0 {
		opts.TestPerMin = 3
	}
	if opts.TestBurst <= 0 {
		opts.TestBurst = 5
	}
	if opts.TestTTL <= 0 {
		opts.TestTTL = 5 * time.Minute
	}
	if opts.SeqFutureTolerance <= 0 {
		opts.SeqFutureTolerance = 5 * time.Minute
	}
	if opts.Policy == nil {
		opts.Policy = netpolicy.New()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	s := &Server{
		store:          st,
		dispatcher:     d,
		companies:      companies,
		logger:         opts.Logger,
		opts:           opts,
		policy:         opts.Policy,
		ipLimiter:      ratelimit.New(opts.IPPerMin, opts.IPBurst),
		regLimiter:     ratelimit.New(opts.RegPerMin, opts.RegBurst),
		probeIP:        ratelimit.New(opts.ProbePerMin, opts.ProbeBurst),
		probeGlobal:    ratelimit.New(opts.GlobalProbePerMin, opts.GlobalProbeBurst),
		testIP:         ratelimit.New(opts.TestIPPerMin, opts.TestIPBurst),
		testReg:        map[string]*ratelimit.Limiter{},
		fcmTests:       map[string]fcmTest{},
		pubLimiters:    map[string]*ratelimit.Limiter{},
		pushOrigins:    defaultPushOrigins(),
		pushOriginsAny: opts.PushOriginsAny,
		clientIPHeader: opts.TrustedProxyIPHeader,
		corsOrigins:    map[string]bool{},
	}
	for _, origin := range opts.ApprovedPushOrigins {
		s.pushOrigins[origin] = true
	}
	for _, origin := range opts.CORSOrigins {
		s.corsOrigins[origin] = true
	}
	s.lastActivity.Store(time.Now().UnixNano())
	return s
}

// Handler returns the http.Handler with the routes for the selected mode.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/registrations", s.handleRegistrationCreate)
	mux.HandleFunc("PUT /v1/registrations/{id}", s.handleRegistrationUpdate)
	mux.HandleFunc("DELETE /v1/registrations/{id}", s.handleRegistrationDelete)
	mux.HandleFunc("POST /v1/registrations/{id}/heartbeat", s.handleRegistrationHeartbeat)
	mux.HandleFunc("POST /v1/registrations/{id}/test", s.handleRegistrationTest)
	mux.HandleFunc("POST /v1/fcm/test", s.handleFCMTest)
	mux.HandleFunc("POST /v1/fcm/test/{test_id}/ready", s.handleFCMTestReady)
	mux.HandleFunc("GET /healthz", s.handleHealth)
	if s.opts.Debug {
		mux.HandleFunc("POST /debug/v1/publish", s.handleDebugPublish)
		mux.HandleFunc("GET /debug/v1/publishes/{request_id}", s.handleProbe)
	} else {
		mux.HandleFunc("POST /v1/publish", s.handlePublish)
		mux.HandleFunc("GET /v1/publishes/{request_id}", s.handleProbe)
		mux.HandleFunc("POST /v1/companies/{company_id}/refresh", s.handleCompanyRefresh)
	}
	return s.touchActivity(s.logRequests(s.cors(mux)))
}

// LastActivity returns the time of the last request that was not a health
// check, for a platform idle-exit policy.
func (s *Server) LastActivity() time.Time {
	return time.Unix(0, s.lastActivity.Load())
}

// touchActivity records the last non-health request so an idle-exit can tell a
// quiet relay from a busy one. Health checks must not keep the relay awake.
func (s *Server) touchActivity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			s.lastActivity.Store(time.Now().UnixNano())
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// cors answers preflight and adds the configured PWA origins. The relay is a
// separate origin from the app, so the app's API calls are cross-origin; an
// origin that is not listed gets no CORS headers.
func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && s.corsOrigins[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Cleanup drops idle rate-limit state.
func (s *Server) Cleanup(idle time.Duration) {
	s.ipLimiter.Cleanup(idle)
	s.regLimiter.Cleanup(idle)
	s.probeIP.Cleanup(idle)
	s.probeGlobal.Cleanup(idle)
	s.testIP.Cleanup(idle)
	s.testMu.Lock()
	for id, l := range s.testReg {
		l.Cleanup(idle)
		_ = id
	}
	s.testMu.Unlock()
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	for id, l := range s.pubLimiters {
		l.Cleanup(idle)
		_ = id
	}
}

// --- publish ---

type publishRequest struct {
	V         int          `json:"v"`
	CompanyID string       `json:"company_id"`
	ScopeID   string       `json:"scope_id"`
	H         string       `json:"h"`
	Seq       int64        `json:"seq"`
	Sig       []wakeup.Sig `json:"sig"`
}

func (s *Server) handlePublish(w http.ResponseWriter, r *http.Request) {
	s.publish(w, r, false)
}

func (s *Server) handleDebugPublish(w http.ResponseWriter, r *http.Request) {
	if !s.debugAuthorized(w, r) {
		return
	}
	s.publish(w, r, true)
}

func (s *Server) publish(w http.ResponseWriter, r *http.Request, debug bool) {
	// Unauthenticated traffic is bounded by IP and globally before any
	// signature or company-state work (§5.4).
	if !s.ipLimiter.Allow(s.remoteIP(r)) {
		writeError(w, http.StatusTooManyRequests, "publish rate limit exceeded")
		return
	}
	var req publishRequest
	if err := s.decodeStrict(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.V != 1 {
		writeError(w, http.StatusBadRequest, "v must be 1")
		return
	}
	if err := company.Validate(req.CompanyID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	companyID := req.CompanyID
	if err := topic.ValidateScopeID(req.ScopeID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := topic.ValidateH(req.H); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Seq < 1 || req.Seq > wakeup.MaxSeq {
		writeError(w, http.StatusBadRequest, "seq must be an integer from 1 through 9007199254740991")
		return
	}
	if time.Unix(req.Seq, 0).After(time.Now().Add(s.opts.SeqFutureTolerance)) {
		writeError(w, http.StatusBadRequest, "seq is implausibly far in the future")
		return
	}
	if !debug && len(req.Sig) == 0 {
		writeError(w, http.StatusBadRequest, "sig must be a nonempty array")
		return
	}

	tpc, err := topic.Derive(companyID, req.ScopeID, req.H)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	var tbl *scope.Table
	if !debug {
		tbl = s.companies.Table(companyID)
		if tbl == nil {
			if !s.companies.Known(companyID) {
				writeError(w, http.StatusNotFound, "company not known; synchronize first")
				return
			}
			s.companies.Hint(companyID)
			writeError(w, http.StatusServiceUnavailable, "company authorization unavailable; refresh pending")
			return
		}
		entry, ok := tbl.Lookup(req.ScopeID)
		if !ok {
			writeError(w, http.StatusForbidden, "unknown scope for company")
			return
		}
		if err := wakeup.Verify(int64(req.V), tpc, req.Seq, req.Sig, entry.Keys, entry.Threshold); err != nil {
			writeError(w, http.StatusForbidden, err.Error())
			return
		}
		if !s.companyLimiter(companyID).Allow("") {
			writeError(w, http.StatusTooManyRequests, "company publish rate limit exceeded")
			return
		}
	}

	wakeupJSON, err := json.Marshal(wakeup.Wakeup{V: int64(req.V), T: tpc, Seq: req.Seq, Sig: req.Sig})
	if err != nil {
		s.internalError(w, err)
		return
	}

	out, err := s.dispatcher.Publish(r.Context(), companyID, req.ScopeID, tpc, req.Seq, wakeupJSON)
	if err != nil {
		if errors.Is(err, relay.ErrQueueFull) {
			writeError(w, http.StatusServiceUnavailable, "dispatch queue saturated; retry later")
			return
		}
		s.internalError(w, err)
		return
	}
	if out.Suppressed {
		writeJSON(w, http.StatusOK, map[string]any{
			"topic":      tpc,
			"suppressed": true,
			"providers":  map[string]any{"fcm": out.FCM, "webpush": webpushCounts(out.WebPush)},
		})
		return
	}
	if out.Async {
		writeJSON(w, http.StatusAccepted, map[string]any{
			"topic":      tpc,
			"request_id": out.RequestID,
			"status":     "accepted",
			"expires_at": out.ExpiresAt.UTC().Format(time.RFC3339),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"topic":      tpc,
		"suppressed": false,
		"providers":  map[string]any{"fcm": out.FCM, "webpush": webpushCounts(out.WebPush)},
	})
}

func webpushCounts(w relay.WebPushResult) map[string]int {
	return map[string]int{"sent": w.Sent, "failed": w.Failed, "dead": w.Dead}
}

// --- dispatch status ---

func (s *Server) handleProbe(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.probeIP.Allow(s.remoteIP(r)) || !s.probeGlobal.Allow("global") {
		writeError(w, http.StatusTooManyRequests, "probe rate limit exceeded")
		return
	}
	requestID := r.PathValue("request_id")
	e, err := s.store.EventByRequestID(requestID, time.Now().UTC())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "unknown or expired request_id")
			return
		}
		s.internalError(w, err)
		return
	}
	switch e.Status {
	case store.StatusPending:
		writeJSON(w, http.StatusOK, map[string]any{
			"request_id": requestID,
			"topic":      e.Topic,
			"status":     "pending",
		})
	case store.StatusSuperseded:
		resp := map[string]any{
			"request_id": requestID,
			"topic":      e.Topic,
			"status":     "superseded",
		}
		if e.SupersededByID != nil {
			if next, err := s.store.EventByID(*e.SupersededByID); err == nil {
				resp["superseded_by"] = next.RequestID
				resp["expires_at"] = next.RequestExpiresAt.UTC().Format(time.RFC3339)
			}
		}
		writeJSON(w, http.StatusOK, resp)
	default:
		writeJSON(w, http.StatusOK, map[string]any{
			"request_id": requestID,
			"topic":      e.Topic,
			"status":     "complete",
			"providers": map[string]any{
				"fcm": e.FCM,
				"webpush": map[string]int{
					"attempted": e.WebPushAttempted,
					"sent":      e.WebPushSent,
					"failed":    e.WebPushFailed,
					"dead":      e.WebPushDead,
				},
			},
		})
	}
}

// --- company synchronization ---

func (s *Server) handleCompanyRefresh(w http.ResponseWriter, r *http.Request) {
	companyID := r.PathValue("company_id")
	if err := company.Validate(companyID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if r.ContentLength > 0 {
		writeError(w, http.StatusBadRequest, "unexpected request body")
		return
	}
	// Unknown-domain discovery has a separate, stricter admission budget
	// (§5.2); known companies keep their trust state when it is exhausted.
	if !s.companies.Known(companyID) && !s.companies.DiscoveryAllowed(s.remoteIP(r)) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "discovery rate limit exceeded")
		return
	}
	s.companies.Hint(companyID)
	writeJSON(w, http.StatusAccepted, map[string]string{"company_id": companyID, "status": "scheduled"})
}

// --- registrations ---

type registrationRequest struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256DH string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
	Topics []string `json:"topics"`
	Source string   `json:"source"`
}

func (s *Server) handleRegistrationCreate(w http.ResponseWriter, r *http.Request) {
	if !s.regLimiter.Allow(s.remoteIP(r)) {
		writeError(w, http.StatusTooManyRequests, "registration rate limit exceeded")
		return
	}
	var req registrationRequest
	if err := s.decodeStrict(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.validateRegistration(req.Endpoint, req.Keys.P256DH, req.Keys.Auth, req.Topics); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	source := req.Source
	if source == "" {
		source = "pwa"
	}
	if source != "pwa" && source != "android_up" {
		writeError(w, http.StatusBadRequest, "source must be pwa or android_up")
		return
	}
	id, token, err := s.store.CreateRegistration(req.Endpoint, req.Keys.P256DH, req.Keys.Auth, source, r.UserAgent(), req.Topics, time.Now().UTC())
	if err != nil {
		switch {
		case errors.Is(err, store.ErrConflict):
			writeError(w, http.StatusConflict, "endpoint already registered")
		case errors.Is(err, store.ErrTooManyTopics):
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			s.internalError(w, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "management_token": token})
}

func (s *Server) handleRegistrationUpdate(w http.ResponseWriter, r *http.Request) {
	token, ok := bearerToken(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "missing management token")
		return
	}
	if !s.regLimiter.Allow(s.remoteIP(r)) {
		writeError(w, http.StatusTooManyRequests, "registration rate limit exceeded")
		return
	}
	var req struct {
		Topics   []string `json:"topics"`
		Endpoint string   `json:"endpoint"`
		Keys     *struct {
			P256DH string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
	}
	if err := s.decodeStrict(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.validateTopics(req.Topics); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var endpoint, p256dh, auth *string
	if req.Endpoint != "" || req.Keys != nil {
		if req.Endpoint == "" || req.Keys == nil {
			writeError(w, http.StatusBadRequest, "endpoint and keys must be supplied together")
			return
		}
		if err := s.validateEndpoint(req.Endpoint, req.Keys.P256DH, req.Keys.Auth); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		endpoint, p256dh, auth = &req.Endpoint, &req.Keys.P256DH, &req.Keys.Auth
	}
	id := r.PathValue("id")
	err := s.store.UpdateRegistration(id, token, endpoint, p256dh, auth, req.Topics, time.Now().UTC())
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]string{"id": id})
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "registration not found")
	case errors.Is(err, store.ErrUnauthorized):
		writeError(w, http.StatusUnauthorized, "invalid management token")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "endpoint owned by another registration")
	case errors.Is(err, store.ErrTooManyTopics):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		s.internalError(w, err)
	}
}

func (s *Server) handleRegistrationDelete(w http.ResponseWriter, r *http.Request) {
	token, ok := bearerToken(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "missing management token")
		return
	}
	if !s.regLimiter.Allow(s.remoteIP(r)) {
		writeError(w, http.StatusTooManyRequests, "registration rate limit exceeded")
		return
	}
	err := s.store.DeleteRegistration(r.PathValue("id"), token)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "registration not found")
	case errors.Is(err, store.ErrUnauthorized):
		writeError(w, http.StatusUnauthorized, "invalid management token")
	default:
		s.internalError(w, err)
	}
}

func (s *Server) handleRegistrationHeartbeat(w http.ResponseWriter, r *http.Request) {
	token, ok := bearerToken(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "missing management token")
		return
	}
	err := s.store.HeartbeatRegistration(r.PathValue("id"), token, time.Now().UTC())
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "registration not found")
	case errors.Is(err, store.ErrUnauthorized):
		writeError(w, http.StatusUnauthorized, "invalid management token")
	default:
		s.internalError(w, err)
	}
}

// fcmTest is one pending §5.3.1 topic-leg self-test. The relay keeps the
// topic and nonce in memory only and never durably; it never learns the
// device's FCM token (§9).
type fcmTest struct {
	topic     string
	nonce     string
	expiresAt time.Time
}

// fcmTestMax bounds the in-memory capability map.
const fcmTestMax = 4096

// handleRegistrationTest delivers the §4.3 self-test payload through the
// ordinary endpoint path (§5.3.1). It never touches replay state, the registry
// or sequence state: a test is never a wake-up.
func (s *Server) handleRegistrationTest(w http.ResponseWriter, r *http.Request) {
	token, ok := bearerToken(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "missing management token")
		return
	}
	if !s.testIP.Allow(s.remoteIP(r)) {
		writeError(w, http.StatusTooManyRequests, "self-test rate limit exceeded")
		return
	}
	id := r.PathValue("id")
	if !s.testLimiter(id).Allow(id) {
		writeError(w, http.StatusTooManyRequests, "self-test rate limit exceeded")
		return
	}
	reg, err := s.store.RegistrationForManagement(id, token)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "registration not found")
		return
	case errors.Is(err, store.ErrUnauthorized):
		writeError(w, http.StatusUnauthorized, "invalid management token")
		return
	case err != nil:
		s.internalError(w, err)
		return
	}
	nonce, payload := wakeup.NewTestPayload()
	if err := s.dispatcher.SendToEndpoint(r.Context(), reg, payload); err != nil {
		switch {
		case errors.Is(err, relay.ErrEndpointLegDisabled):
			writeError(w, http.StatusServiceUnavailable, "endpoint leg disabled")
		case errors.Is(err, push.ErrGone):
			writeError(w, http.StatusGone, "endpoint reported dead")
		default:
			s.logger.Warn("self-test delivery failed", "err", err) // never the endpoint
			writeError(w, http.StatusServiceUnavailable, "provider unavailable")
		}
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"nonce":      nonce,
		"expires_at": time.Now().UTC().Add(s.opts.TestTTL).Format(time.RFC3339),
	})
}

// handleFCMTest starts one topic-leg self-test (§5.3.1): the relay generates
// a short-lived topic and nonce, keeps them in memory only, and never learns
// the device's FCM token.
func (s *Server) handleFCMTest(w http.ResponseWriter, r *http.Request) {
	if !s.testIP.Allow(s.remoteIP(r)) {
		writeError(w, http.StatusTooManyRequests, "self-test rate limit exceeded")
		return
	}
	expires := time.Now().UTC().Add(s.opts.TestTTL)
	s.fcmMu.Lock()
	s.purgeFCMTestsLocked(time.Now())
	if len(s.fcmTests) >= fcmTestMax {
		s.fcmMu.Unlock()
		writeError(w, http.StatusTooManyRequests, "self-test capacity exhausted")
		return
	}
	testID := wakeup.NewTestCapability()
	test := fcmTest{
		topic:     wakeup.NewTestCapability(),
		nonce:     wakeup.NewTestCapability(),
		expiresAt: expires,
	}
	s.fcmTests[testID] = test
	s.fcmMu.Unlock()
	writeJSON(w, http.StatusAccepted, map[string]any{
		"test_id":    testID,
		"topic":      test.topic,
		"nonce":      test.nonce,
		"expires_at": expires.Format(time.RFC3339),
	})
}

// handleFCMTestReady publishes the pending test's §4.3 payload to its topic
// (§5.3.1). test_id is the unguessable capability that authorizes the call.
// The relay publishes once: FCM fanout is not instantaneous, so the client's
// neutral "sent — not confirmed yet" state and its next test cover a slow
// propagation, not repeated publishes.
func (s *Server) handleFCMTestReady(w http.ResponseWriter, r *http.Request) {
	if !s.testIP.Allow(s.remoteIP(r)) {
		writeError(w, http.StatusTooManyRequests, "self-test rate limit exceeded")
		return
	}
	now := time.Now()
	s.fcmMu.Lock()
	s.purgeFCMTestsLocked(now)
	test, ok := s.fcmTests[r.PathValue("test_id")]
	s.fcmMu.Unlock()
	if !ok || now.After(test.expiresAt) {
		writeError(w, http.StatusNotFound, "test not found")
		return
	}
	switch err := s.dispatcher.SendToTopic(r.Context(), test.topic, wakeup.TestPayloadBytes(test.nonce)); {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, relay.ErrTopicLegDisabled):
		writeError(w, http.StatusServiceUnavailable, "topic leg disabled")
	default:
		s.logger.Warn("self-test publish failed", "err", err)
		writeError(w, http.StatusServiceUnavailable, "provider unavailable")
	}
}

// purgeFCMTestsLocked drops expired capabilities. The caller holds fcmMu.
func (s *Server) purgeFCMTestsLocked(now time.Time) {
	for id, test := range s.fcmTests {
		if now.After(test.expiresAt) {
			delete(s.fcmTests, id)
		}
	}
}

// testLimiter returns the per-registration self-test bucket.
func (s *Server) testLimiter(id string) *ratelimit.Limiter {
	s.testMu.Lock()
	defer s.testMu.Unlock()
	l, ok := s.testReg[id]
	if !ok {
		l = ratelimit.New(s.opts.TestPerMin, s.opts.TestBurst)
		s.testReg[id] = l
	}
	return l
}

// --- validation ---

func (s *Server) validateRegistration(endpoint, p256dh, auth string, topics []string) error {
	if err := s.validateEndpoint(endpoint, p256dh, auth); err != nil {
		return err
	}
	return s.validateTopics(topics)
}

func (s *Server) validateEndpoint(endpoint, p256dh, auth string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return errors.New("endpoint: must be a URL")
	}
	if _, err := s.policy.CheckURL(endpoint); err != nil {
		return fmt.Errorf("endpoint: %w", err)
	}
	if !s.pushOriginApproved(u.Scheme + "://" + u.Host) {
		return errors.New("endpoint: origin is not an approved push-service origin")
	}
	raw, err := base64.RawURLEncoding.DecodeString(p256dh)
	if err != nil || len(raw) != 65 || raw[0] != 0x04 {
		return errors.New("keys.p256dh: must be base64url of a 65-byte uncompressed P-256 point")
	}
	if _, err := ecdh.P256().NewPublicKey(raw); err != nil {
		return errors.New("keys.p256dh: not a valid P-256 point")
	}
	authRaw, err := base64.RawURLEncoding.DecodeString(auth)
	if err != nil || len(authRaw) != 16 {
		return errors.New("keys.auth: must be base64url of a 16-byte secret")
	}
	return nil
}

func (s *Server) validateTopics(topics []string) error {
	if len(topics) > store.MaxTopicsPerRegistration {
		return store.ErrTooManyTopics
	}
	for _, t := range topics {
		if err := topic.ValidateTopic(t); err != nil {
			return err
		}
	}
	return nil
}

// --- helpers ---

func (s *Server) companyLimiter(companyID string) *ratelimit.Limiter {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	l, ok := s.pubLimiters[companyID]
	if !ok {
		l = ratelimit.New(s.opts.PublishPerMin, s.opts.PublishBurst)
		s.pubLimiters[companyID] = l
	}
	return l
}

func (s *Server) debugAuthorized(w http.ResponseWriter, r *http.Request) bool {
	token, ok := bearerToken(r)
	if !ok || s.opts.DebugAPIKey == "" || subtle.ConstantTimeCompare([]byte(token), []byte(s.opts.DebugAPIKey)) != 1 {
		writeError(w, http.StatusUnauthorized, "invalid debug API key")
		return false
	}
	return true
}

// decodeStrict decodes a bounded JSON body, rejecting unknown fields,
// duplicate member names and trailing data.
func (s *Server) decodeStrict(w http.ResponseWriter, r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, s.opts.BodyMax+1))
	if err != nil {
		return errors.New("invalid JSON body")
	}
	if int64(len(body)) > s.opts.BodyMax {
		return errors.New("request body too large")
	}
	if err := checkNoDuplicateKeys(body); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	if err := ensureEOF(dec); err != nil {
		return err
	}
	return nil
}

func checkNoDuplicateKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return walkKeys(dec)
}

func walkKeys(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return nil
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return nil
			}
			key, _ := kt.(string)
			if seen[key] {
				return fmt.Errorf("duplicate JSON member %q", key)
			}
			seen[key] = true
			if err := walkKeys(dec); err != nil {
				return err
			}
		}
		_, err := dec.Token()
		return err
	case '[':
		for dec.More() {
			if err := walkKeys(dec); err != nil {
				return err
			}
		}
		_, err := dec.Token()
		return err
	}
	return nil
}

func ensureEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("trailing data after JSON body")
	}
	return nil
}

func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) || len(h) == len(prefix) {
		return "", false
	}
	return h[len(prefix):], true
}

// remoteIP returns the client IP used for per-IP rate limits. Behind a
// platform proxy that overwrites a client-IP header (e.g. Fly.io's
// Fly-Client-IP), TrustedProxyIPHeader selects it; otherwise the transport
// peer is used. The header MUST only be trusted when the app is reachable
// exclusively through that proxy.
func (s *Server) remoteIP(r *http.Request) string {
	if s.clientIPHeader != "" {
		if v := strings.TrimSpace(r.Header.Get(s.clientIPHeader)); v != "" {
			if ip := net.ParseIP(v); ip != nil {
				return ip.String()
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (s *Server) internalError(w http.ResponseWriter, err error) {
	s.logger.Error("internal error", "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

func defaultPushOrigins() map[string]bool {
	out := map[string]bool{
		"https://ntfy.sh": true,
	}
	for _, origin := range []string{
		"https://fcm.googleapis.com",
		"https://jmt17.google.com",
		"https://updates.push.services.mozilla.com",
		"https://push.services.mozilla.com",
		"https://*.push.apple.com",
		"https://*.notify.windows.com",
	} {
		out[origin] = true
	}
	return out
}

// pushOriginApproved reports whether an endpoint origin is covered by the
// approved push-service origins (§5.6). Entries are exact origins or
// "scheme://*.host" wildcards; PushOriginsAny bypasses the list entirely
// (the outbound-request policy still applies).
func (s *Server) pushOriginApproved(origin string) bool {
	if s.pushOriginsAny {
		return true
	}
	for entry := range s.pushOrigins {
		if originMatches(entry, origin) {
			return true
		}
	}
	return false
}

// originMatches matches an approved-origin entry against an endpoint origin.
// "scheme://*.suffix" matches "scheme://suffix" and any "scheme://<sub>.suffix".
func originMatches(entry, origin string) bool {
	if entry == origin {
		return true
	}
	entryScheme, entryHost, ok := strings.Cut(entry, "://")
	if !ok || !strings.HasPrefix(entryHost, "*.") {
		return false
	}
	originScheme, originHost, ok := strings.Cut(origin, "://")
	if !ok || originScheme != entryScheme {
		return false
	}
	suffix := entryHost[len("*."):]
	return originHost == suffix || strings.HasSuffix(originHost, "."+suffix)
}

// logRequests logs method, path, status and duration (topics and hashes only —
// never payloads or capabilities, §9).
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		path := r.URL.Path
		if strings.HasPrefix(path, "/v1/publishes/") || strings.HasPrefix(path, "/debug/v1/publishes/") {
			path = "/v1/publishes/<redacted>"
		}
		s.logger.Info("request", "method", r.Method, "path", path,
			"status", rec.status, "dur", time.Since(start).Round(time.Millisecond))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}
