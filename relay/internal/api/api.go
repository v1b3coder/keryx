// Package api implements the relay's publisher and registration HTTP API
// (SPECIFICATION §5): POST /v1/publish (Bearer API key), and the WebPush
// registration endpoints (POST/PUT/DELETE /v1/registrations).
package api

import (
	"context"
	"crypto/ecdh"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/v1b3coder/keryx/relay/internal/ratelimit"
	"github.com/v1b3coder/keryx/relay/internal/relay"
	"github.com/v1b3coder/keryx/relay/internal/store"
	"github.com/v1b3coder/keryx/relay/internal/topic"
)

// Server serves the relay HTTP API.
type Server struct {
	store  *store.Store
	relay  *relay.Dispatcher
	appKey string // optional X-App-Key gate for registrations (§5.2)
	logger *slog.Logger

	regLimiter *ratelimit.Limiter // per-IP subscription throttling (§5.3)

	mu               sync.Mutex
	pubLimiters      map[int64]*ratelimit.Limiter // per-publisher publish limits
	publishBurstMult int

	sem   chan struct{} // max concurrent dispatches (sync path + workers)
	queue chan *publishJob
}

// publishJob is an accepted publish request awaiting dispatch (202 path).
type publishJob struct {
	ctx         context.Context
	publisherID int64
	kind        topic.Kind
	h           string
	n, seq      *int
}

// Options configure a Server.
type Options struct {
	AppKey           string // "" disables the registration gate
	MaxConcurrent    int    // default 64
	QueueSize        int    // default 4096
	RegPerMin        int    // per-IP registration throttle, default 30
	RegBurst         int    // default 60
	PublishBurstMult int    // burst = rate_per_min * mult, default 2
	Logger           *slog.Logger
}

// New builds the API server and starts the async publish workers.
func New(st *store.Store, d *relay.Dispatcher, opts Options) *Server {
	if opts.MaxConcurrent <= 0 {
		opts.MaxConcurrent = 64
	}
	if opts.QueueSize <= 0 {
		opts.QueueSize = 4096
	}
	if opts.RegPerMin <= 0 {
		opts.RegPerMin = 30
	}
	if opts.RegBurst <= 0 {
		opts.RegBurst = 60
	}
	if opts.PublishBurstMult <= 0 {
		opts.PublishBurstMult = 2
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	s := &Server{
		store:       st,
		relay:       d,
		appKey:      opts.AppKey,
		logger:      opts.Logger,
		regLimiter:  ratelimit.New(opts.RegPerMin, opts.RegBurst),
		pubLimiters: make(map[int64]*ratelimit.Limiter),
		sem:         make(chan struct{}, opts.MaxConcurrent),
		queue:       make(chan *publishJob, opts.QueueSize),
	}
	s.publishBurstMult = opts.PublishBurstMult
	for i := 0; i < opts.MaxConcurrent; i++ {
		go s.worker()
	}
	return s
}

// Handler returns the http.Handler with all routes registered.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/publish", s.handlePublish)
	mux.HandleFunc("POST /v1/registrations", s.handleRegistrationCreate)
	mux.HandleFunc("PUT /v1/registrations/{id}", s.handleRegistrationUpdate)
	mux.HandleFunc("DELETE /v1/registrations/{id}", s.handleRegistrationDelete)
	return s.logRequests(mux)
}

func (s *Server) worker() {
	for job := range s.queue {
		s.sem <- struct{}{}
		if _, err := s.dispatch(job.ctx, job.publisherID, job.kind, job.h, job.n, job.seq); err != nil {
			s.logger.Error("queued publish failed", "err", err)
		}
		<-s.sem
	}
}

// --- publish ---

type publishRequest struct {
	V    int    `json:"v"`
	Kind string `json:"kind"`
	H    string `json:"h"`
	N    *int   `json:"n"`
	Seq  *int   `json:"seq"`
}

func (s *Server) handlePublish(w http.ResponseWriter, r *http.Request) {
	apiKey, ok := bearerToken(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "missing or malformed Authorization header")
		return
	}
	pub, err := s.store.LookupPublisher(apiKey)
	if err != nil {
		if errors.Is(err, store.ErrUnknownKey) {
			writeError(w, http.StatusUnauthorized, "unknown API key")
			return
		}
		s.internalError(w, err)
		return
	}

	var req publishRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.V != 1 {
		writeError(w, http.StatusBadRequest, "v must be 1")
		return
	}
	kind, err := topic.ParseKind(req.Kind)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := topic.ValidateH(req.H); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.N != nil && *req.N < 0 {
		writeError(w, http.StatusBadRequest, "n must be >= 0")
		return
	}
	if req.Seq != nil && *req.Seq < 0 {
		writeError(w, http.StatusBadRequest, "seq must be >= 0")
		return
	}

	if !s.publisherLimiter(pub).Allow("") {
		writeError(w, http.StatusTooManyRequests, "publish rate limit exceeded")
		return
	}

	job := &publishJob{
		ctx:         r.Context(),
		publisherID: pub.ID,
		kind:        kind,
		h:           req.H,
		n:           req.N,
		seq:         req.Seq,
	}
	// Synchronous fan-out when capacity is available; queue (202) under load.
	select {
	case s.sem <- struct{}{}:
		res, err := s.dispatch(job.ctx, job.publisherID, job.kind, job.h, job.n, job.seq)
		<-s.sem
		if err != nil {
			s.internalError(w, err)
			return
		}
		tpc, _ := topic.Topic(job.kind, job.h)
		writeJSON(w, http.StatusOK, map[string]any{"topic": tpc, "delivered": res})
	case <-r.Context().Done():
		return
	default:
		select {
		case s.queue <- job:
			w.WriteHeader(http.StatusAccepted)
		default:
			writeError(w, http.StatusServiceUnavailable, "relay overloaded; retry later")
		}
	}
}

// dispatch runs the fan-out and returns the §5.1 result.
func (s *Server) dispatch(ctx context.Context, publisherID int64, kind topic.Kind, h string, n, seq *int) (relay.Result, error) {
	return s.relay.Publish(ctx, publisherID, kind, h, n, seq)
}

func (s *Server) publisherLimiter(pub *store.Publisher) *ratelimit.Limiter {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.pubLimiters[pub.ID]
	if !ok {
		l = ratelimit.New(pub.RatePerMin, pub.RatePerMin*s.publishBurstMult)
		s.pubLimiters[pub.ID] = l
	}
	return l
}

// --- registrations ---

type registrationRequest struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256DH string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
	Topics []string `json:"topics"`
}

func (s *Server) handleRegistrationCreate(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeApp(w, r) {
		return
	}
	if !s.regLimiter.Allow(remoteIP(r)) {
		writeError(w, http.StatusTooManyRequests, "registration rate limit exceeded")
		return
	}
	var req registrationRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := validateRegistrationRequest(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	id, err := s.store.UpsertRegistration(req.Endpoint, req.Keys.P256DH, req.Keys.Auth,
		r.UserAgent(), req.Topics)
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

func (s *Server) handleRegistrationUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeApp(w, r) {
		return
	}
	if !s.regLimiter.Allow(remoteIP(r)) {
		writeError(w, http.StatusTooManyRequests, "registration rate limit exceeded")
		return
	}
	id := r.PathValue("id")
	var req struct {
		Topics []string `json:"topics"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	for _, t := range req.Topics {
		if err := topic.ValidateTopic(t); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if len(req.Topics) > store.MaxTopicsPerRegistration {
		writeError(w, http.StatusBadRequest, store.ErrTooManyTopics.Error())
		return
	}
	if err := s.store.ReplaceRegistrationTopics(id, req.Topics); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "registration not found")
			return
		}
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

func (s *Server) handleRegistrationDelete(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeApp(w, r) {
		return
	}
	if !s.regLimiter.Allow(remoteIP(r)) {
		writeError(w, http.StatusTooManyRequests, "registration rate limit exceeded")
		return
	}
	if err := s.store.DeleteRegistration(r.PathValue("id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "registration not found")
			return
		}
		s.internalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// authorizeApp checks the optional X-App-Key gate (§5.2). Always allows when
// no app key is configured.
func (s *Server) authorizeApp(w http.ResponseWriter, r *http.Request) bool {
	if s.appKey == "" {
		return true
	}
	got := r.Header.Get("X-App-Key")
	if subtle.ConstantTimeCompare([]byte(got), []byte(s.appKey)) != 1 {
		writeError(w, http.StatusUnauthorized, "invalid app key")
		return false
	}
	return true
}

// validateRegistrationRequest enforces §5.2: https endpoint, well-formed
// P-256 p256dh and 16-byte auth secret, only derived topics, ≤ 200 topics.
func validateRegistrationRequest(req *registrationRequest) error {
	u, err := url.Parse(req.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return errors.New("endpoint: must be an https:// URL")
	}
	p256dh, err := base64.RawURLEncoding.DecodeString(req.Keys.P256DH)
	if err != nil || len(p256dh) != 65 || p256dh[0] != 0x04 {
		return errors.New("keys.p256dh: must be base64url of a 65-byte uncompressed P-256 point")
	}
	if _, err := ecdh.P256().NewPublicKey(p256dh); err != nil {
		return errors.New("keys.p256dh: not a valid P-256 point")
	}
	auth, err := base64.RawURLEncoding.DecodeString(req.Keys.Auth)
	if err != nil || len(auth) != 16 {
		return errors.New("keys.auth: must be base64url of a 16-byte secret")
	}
	if len(req.Topics) > store.MaxTopicsPerRegistration {
		return store.ErrTooManyTopics
	}
	for _, t := range req.Topics {
		if err := topic.ValidateTopic(t); err != nil {
			return err
		}
	}
	return nil
}

// --- helpers ---

func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) || len(h) == len(prefix) {
		return "", false
	}
	return h[len(prefix):], true
}

func remoteIP(r *http.Request) string {
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

// Cleanup drops idle rate-limit state (called periodically by maintenance).
func (s *Server) Cleanup(idle time.Duration) {
	s.regLimiter.Cleanup(idle)
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, l := range s.pubLimiters {
		if _, err := s.store.PublisherByID(id); err != nil {
			delete(s.pubLimiters, id)
		}
		l.Cleanup(idle)
	}
}

// logRequests logs method, path, status, and duration (topics and hashes
// only — never payloads, §9).
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		s.logger.Info("request", "method", r.Method, "path", r.URL.Path,
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
