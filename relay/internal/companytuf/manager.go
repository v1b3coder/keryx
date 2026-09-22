// Package companytuf maintains the relay's per-company TUF trust state and
// authorization tables (relay/SPECIFICATION.md §5.2, §5.5).
//
// It loads persisted trust state at startup (never reopening TOFU for a pinned
// domain), schedules synchronization through the unsigned hint endpoint and a
// background cadence, coalesces concurrent hints, and exposes the cached scope
// table to the publish path without a network call.
package companytuf

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/v1b3coder/keryx/relay/internal/ratelimit"
	"github.com/v1b3coder/keryx/relay/internal/scope"
	"github.com/v1b3coder/keryx/relay/internal/store"
	"github.com/v1b3coder/keryx/relay/internal/tufclient"
)

// Options configure a Manager.
type Options struct {
	Interval        time.Duration // per-company minimum attempt interval, default 60s
	Cadence         time.Duration // background refresh cadence, default 12h
	Concurrency     int           // concurrent fetches, default 4
	QueueSize       int           // pending hints, default 4096
	DiscoveryPerMin int           // unknown-domain admission budget, default 60
	DiscoveryBurst  int           // default 120
	Logger          *slog.Logger
}

type cached struct {
	table   *scope.Table
	expires time.Time
}

// Manager owns the cached authorization tables and the refresh scheduler.
type Manager struct {
	store  *store.Store
	client *tufclient.Client
	logger *slog.Logger

	interval time.Duration
	cadence  time.Duration

	mu     sync.RWMutex
	tables map[string]cached

	hints   chan string
	sem     chan struct{}
	stop    chan struct{}
	wg      sync.WaitGroup
	stopped bool

	schedMu sync.Mutex
	queued  map[string]bool
	timers  map[string]*time.Timer

	discMu    sync.Mutex
	discovery map[string]time.Time
	discLimit *ratelimit.Limiter
}

// New builds a Manager.
func New(st *store.Store, client *tufclient.Client, opts Options) *Manager {
	if opts.Interval <= 0 {
		opts.Interval = time.Minute
	}
	if opts.Cadence <= 0 {
		opts.Cadence = 12 * time.Hour
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = 4
	}
	if opts.QueueSize <= 0 {
		opts.QueueSize = 4096
	}
	if opts.DiscoveryPerMin <= 0 {
		opts.DiscoveryPerMin = 60
	}
	if opts.DiscoveryBurst <= 0 {
		opts.DiscoveryBurst = 120
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Manager{
		store:     st,
		client:    client,
		logger:    opts.Logger,
		interval:  opts.Interval,
		cadence:   opts.Cadence,
		tables:    map[string]cached{},
		hints:     make(chan string, opts.QueueSize),
		sem:       make(chan struct{}, opts.Concurrency),
		stop:      make(chan struct{}),
		queued:    map[string]bool{},
		timers:    map[string]*time.Timer{},
		discovery: map[string]time.Time{},
		discLimit: ratelimit.New(opts.DiscoveryPerMin, opts.DiscoveryBurst),
	}
}

// Load reads every persisted company's trusted state into the cache and
// schedules a refresh for stale entries. It never resets a pinned domain to
// TOFU.
func (m *Manager) Load() error {
	companies, err := m.store.Companies()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range companies {
		if c.TargetsExpires.After(now) && len(c.Targets) > 0 {
			if tbl, err := c.State().Table(); err == nil {
				m.tables[c.CompanyID] = cached{table: tbl, expires: c.TargetsExpires}
			}
		}
		if c.RefreshedAt.IsZero() || now.Sub(c.RefreshedAt) > m.cadence || !c.TargetsExpires.After(now) {
			m.Hint(c.CompanyID)
		}
	}
	return nil
}

// Start begins the refresh worker and the background cadence.
func (m *Manager) Start() {
	m.wg.Add(2)
	go m.run()
	go m.cadenceLoop()
}

// Stop shuts the manager down.
func (m *Manager) Stop() {
	m.schedMu.Lock()
	m.stopped = true
	m.schedMu.Unlock()
	close(m.stop)
	m.wg.Wait()
}

// Known reports whether the company has persisted trust state.
func (m *Manager) Known(companyID string) bool {
	_, err := m.store.Company(companyID)
	return err == nil
}

// Table returns the cached authorization table for a company, or nil when it is
// unknown, unrefreshed or expired (fail closed, §5.5).
func (m *Manager) Table(companyID string) *scope.Table {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.tables[companyID]
	if !ok || !c.expires.After(time.Now().UTC()) {
		return nil
	}
	return c.table
}

// Hint schedules a synchronization attempt, coalescing repeated hints without
// moving an existing reservation later.
func (m *Manager) Hint(companyID string) {
	m.schedMu.Lock()
	if m.stopped || m.queued[companyID] {
		m.schedMu.Unlock()
		return
	}
	m.queued[companyID] = true
	m.schedMu.Unlock()
	select {
	case m.hints <- companyID:
	default:
		m.schedMu.Lock()
		delete(m.queued, companyID)
		m.schedMu.Unlock()
	}
}

// DiscoveryAllowed consumes the unknown-domain admission budget for an IP.
func (m *Manager) DiscoveryAllowed(ip string) bool { return m.discLimit.Allow(ip) }

// Cleanup drops idle rate-limit state.
func (m *Manager) Cleanup(idle time.Duration) {
	m.discLimit.Cleanup(idle)
	m.schedMu.Lock()
	for id, t := range m.timers {
		if t == nil {
			delete(m.timers, id)
		}
	}
	m.schedMu.Unlock()
}

func (m *Manager) run() {
	defer m.wg.Done()
	for {
		select {
		case <-m.stop:
			return
		case id := <-m.hints:
			m.schedMu.Lock()
			delete(m.queued, id)
			m.schedMu.Unlock()
			m.attempt(id)
		}
	}
}

func (m *Manager) cadenceLoop() {
	defer m.wg.Done()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
			m.cadenceSweep()
		}
	}
}

func (m *Manager) cadenceSweep() {
	companies, err := m.store.Companies()
	if err != nil {
		m.logger.Error("list companies", "err", err)
		return
	}
	now := time.Now().UTC()
	for _, c := range companies {
		if c.RefreshedAt.IsZero() || now.Sub(c.RefreshedAt) >= m.cadence {
			m.Hint(c.CompanyID)
		}
	}
}

// attempt reserves the next eligible time and either starts a refresh or
// schedules one for later. The reservation is the per-company interval (one
// attempt per minute, §5.2), not the background cadence: a hint from a
// publisher after a metadata change must not wait for the 12h sweep.
func (m *Manager) attempt(id string) {
	now := time.Now().UTC()
	known := m.Known(id)
	var reserved time.Time
	if known {
		t, err := m.store.ReserveRefresh(id, now, m.interval)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				m.logger.Error("reserve refresh", "company", id, "err", err)
			}
			return
		}
		reserved = t
	} else {
		m.discMu.Lock()
		t, ok := m.discovery[id]
		if !ok || !now.Before(t) {
			m.discovery[id] = now.Add(m.interval)
			t = now
		}
		m.discMu.Unlock()
		reserved = t
	}
	if reserved.After(now) {
		m.schedule(reserved, id)
		return
	}
	m.sem <- struct{}{}
	go func() {
		defer func() { <-m.sem }()
		m.refresh(id)
	}()
}

func (m *Manager) schedule(at time.Time, id string) {
	m.schedMu.Lock()
	defer m.schedMu.Unlock()
	if m.stopped {
		return
	}
	if t, ok := m.timers[id]; ok {
		t.Stop()
	}
	m.timers[id] = time.AfterFunc(time.Until(at), func() { m.Hint(id) })
}

func (m *Manager) refresh(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var prev *tufclient.State
	if c, err := m.store.Company(id); err == nil {
		prev = c.State()
	} else if !errors.Is(err, store.ErrNotFound) {
		m.logger.Error("load company", "company", id, "err", err)
		return
	}

	next, err := m.client.Refresh(ctx, id, prev)
	if next != nil && len(next.Root) > 0 {
		// Persist verified root progress even when a later metadata step
		// failed (§5.2).
		if prev == nil || next.RootVersion > prev.RootVersion || string(next.Root) != string(prev.Root) {
			if err := m.store.SaveRoot(id, next.Root, next.RootVersion, time.Now().UTC()); err != nil {
				m.logger.Error("save root", "company", id, "err", err)
			}
		}
	}
	if err != nil {
		m.logger.Warn("company refresh failed", "company", id, "err", err)
		return
	}
	tbl, err := next.Table()
	if err != nil {
		m.logger.Warn("company scope table", "company", id, "err", err)
		return
	}
	if err := m.store.SaveState(next, time.Now().UTC().Add(m.interval)); err != nil {
		m.logger.Error("save company state", "company", id, "err", err)
		return
	}
	m.mu.Lock()
	m.tables[id] = cached{table: tbl, expires: next.TargetsExpires}
	m.mu.Unlock()
	m.logger.Info("company synchronized", "company", id, "root", next.RootVersion, "targets", next.TargetsVersion, "scopes", tbl.Len())
}
