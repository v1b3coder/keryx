// Package relay implements the relay's publish admission and fan-out
// (relay/SPECIFICATION.md §5.1, §5.4, §5.5): a bounded in-memory
// per-topic coalescing queue drained by workers paced to the per-provider
// outbound budgets, with an in-memory replay cache and an audit row per
// accepted publish.
package relay

import (
	"container/list"
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/v1b3coder/keryx/relay/internal/push"
	"github.com/v1b3coder/keryx/relay/internal/ratelimit"
	"github.com/v1b3coder/keryx/relay/internal/store"
)

// ErrQueueFull reports a saturated dispatch queue (backpressure, §5.1/§5.4).
var ErrQueueFull = errors.New("dispatch queue saturated")

// ErrEndpointLegDisabled reports that no endpoint leg is configured.
var ErrEndpointLegDisabled = errors.New("endpoint leg disabled")

// WebPushResult is the endpoint leg accounting for one dispatch (§5.1).
type WebPushResult struct {
	Attempted int
	Sent      int
	Failed    int
	Dead      int
}

// Outcome is the result of admitting one publish (§5.1).
type Outcome struct {
	Topic      string
	Suppressed bool
	Async      bool
	RequestID  string
	ExpiresAt  time.Time
	FCM        string
	WebPush    WebPushResult
}

// Options configure a Dispatcher.
type Options struct {
	FastPathMax        int           // default 500
	QueueSize          int           // default 4096
	MaxConcurrent      int           // default 64
	WebPushConcurrency int           // default 64
	FCMPerMin          int           // FCM publish budget; 0 disables pacing
	WebPushPerMin      int           // endpoint-leg budget; 0 disables pacing
	BatchSize          int           // registry streaming batch, default 500
	ReplayCapacity     int           // in-memory replay cache size, default 100000
	CapabilityTTL      time.Duration // default 1h
	Logger             *slog.Logger
}

type entry struct {
	id        int64
	requestID string
	expiresAt time.Time
	companyID string
	scopeID   string
	topic     string
	seq       int64
	wakeup    []byte
}

type topicState struct {
	running bool
	queued  bool
	pending *entry
}

// Dispatcher admits publishes and fans them out.
type Dispatcher struct {
	store   *store.Store
	fcm     *push.FCM
	webpush *push.WebPush
	logger  *slog.Logger

	fastPathMax int
	batchSize   int
	capTTL      time.Duration

	sem   chan struct{} // global dispatch concurrency
	ready chan string   // topics with pending work

	mu     sync.Mutex
	topics map[string]*topicState

	replay *lru

	fcmBudget     *ratelimit.Limiter
	webpushBudget *ratelimit.Limiter
	webpushSem    chan struct{}
}

// New builds the dispatcher and starts its workers.
func New(st *store.Store, fcm *push.FCM, webpush *push.WebPush, opts Options) *Dispatcher {
	if opts.FastPathMax <= 0 {
		opts.FastPathMax = 500
	}
	if opts.QueueSize <= 0 {
		opts.QueueSize = 4096
	}
	if opts.MaxConcurrent <= 0 {
		opts.MaxConcurrent = 64
	}
	if opts.WebPushConcurrency <= 0 {
		opts.WebPushConcurrency = 64
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 500
	}
	if opts.ReplayCapacity <= 0 {
		opts.ReplayCapacity = 100000
	}
	if opts.CapabilityTTL <= 0 {
		opts.CapabilityTTL = time.Hour
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	d := &Dispatcher{
		store:       st,
		fcm:         fcm,
		webpush:     webpush,
		logger:      opts.Logger,
		fastPathMax: opts.FastPathMax,
		batchSize:   opts.BatchSize,
		capTTL:      opts.CapabilityTTL,
		sem:         make(chan struct{}, opts.MaxConcurrent),
		ready:       make(chan string, opts.QueueSize),
		topics:      map[string]*topicState{},
		replay:      newLRU(opts.ReplayCapacity),
		webpushSem:  make(chan struct{}, opts.WebPushConcurrency),
	}
	if opts.FCMPerMin > 0 {
		d.fcmBudget = ratelimit.New(opts.FCMPerMin, opts.FCMPerMin)
	}
	if opts.WebPushPerMin > 0 {
		d.webpushBudget = ratelimit.New(opts.WebPushPerMin, opts.WebPushPerMin)
	}
	for i := 0; i < opts.MaxConcurrent; i++ {
		go d.worker()
	}
	return d
}

// Publish admits a verified wake-up and either dispatches it synchronously
// (fast path) or queues it (async path). It returns ErrQueueFull when the
// queue is saturated; in that case replay state and queued work are unchanged.
func (d *Dispatcher) Publish(ctx context.Context, companyID, scopeID, topic string, seq int64, wakeup []byte) (*Outcome, error) {
	now := time.Now().UTC()

	d.mu.Lock()
	st := d.topics[topic]
	if st == nil {
		st = &topicState{}
		d.topics[topic] = st
	}
	if last, ok := d.replay.Get(topic); ok && seq <= last {
		d.mu.Unlock()
		return &Outcome{Topic: topic, Suppressed: true, FCM: d.fcmStatus()}, nil
	}

	// A running dispatch cannot be superseded: coalesce into a separate
	// pending entry and return the async response regardless of size.
	if st.running || st.pending != nil {
		out, err := d.enqueueLocked(st, companyID, scopeID, topic, seq, wakeup, now)
		if err != nil {
			d.mu.Unlock()
			return nil, err
		}
		d.mu.Unlock()
		return out, nil
	}

	count, err := d.store.TopicCount(topic)
	if err != nil {
		d.mu.Unlock()
		return nil, err
	}
	if count <= d.fastPathMax {
		select {
		case d.sem <- struct{}{}:
			id, _, _, err := d.store.AcceptEvent(companyID, scopeID, topic, d.capTTL, now)
			if err != nil {
				<-d.sem
				d.mu.Unlock()
				return nil, err
			}
			st.running = true
			d.replay.Put(topic, seq)
			d.mu.Unlock()

			res := d.dispatch(ctx, topic, wakeup)
			if err := d.store.CompleteEvent(id, res.FCM, res.WebPush.Attempted, res.WebPush.Sent, res.WebPush.Failed, res.WebPush.Dead, time.Now().UTC()); err != nil {
				d.logger.Error("complete event", "err", err)
			}
			d.mu.Lock()
			st.running = false
			d.mu.Unlock()
			<-d.sem
			return &Outcome{Topic: topic, FCM: res.FCM, WebPush: res.WebPush}, nil
		default:
			// No dispatch capacity: fall through to the queue.
		}
	}
	out, err := d.enqueueLocked(st, companyID, scopeID, topic, seq, wakeup, now)
	if err != nil {
		d.mu.Unlock()
		return nil, err
	}
	d.mu.Unlock()
	return out, nil
}

// enqueueLocked writes the acceptance row and makes the entry visible to the
// workers. The caller holds d.mu. Replacing a pending entry never reserves an
// additional queue slot.
func (d *Dispatcher) enqueueLocked(st *topicState, companyID, scopeID, topic string, seq int64, wakeup []byte, now time.Time) (*Outcome, error) {
	id, requestID, expires, err := d.store.AcceptEvent(companyID, scopeID, topic, d.capTTL, now)
	if err != nil {
		return nil, err
	}
	e := &entry{id: id, requestID: requestID, expiresAt: expires, companyID: companyID, scopeID: scopeID, topic: topic, seq: seq, wakeup: wakeup}
	switch {
	case st.pending != nil && seq > st.pending.seq:
		if err := d.store.SupersedeEvent(st.pending.id, id, now); err != nil {
			return nil, err
		}
		st.pending = e
	case st.pending != nil:
		if err := d.store.SupersedeEvent(id, st.pending.id, now); err != nil {
			return nil, err
		}
	default:
		st.pending = e
	}
	if !st.queued {
		select {
		case d.ready <- topic:
			st.queued = true
		default:
			// Saturated queue: release the reservation and leave replay
			// state and existing queued work unchanged.
			if st.pending != nil && st.pending.id == id {
				st.pending = nil
			}
			if err := d.store.DeleteEvent(id); err != nil {
				d.logger.Error("delete rejected event", "err", err)
			}
			return nil, ErrQueueFull
		}
	}
	d.replay.Put(topic, seq)
	return &Outcome{Topic: topic, Async: true, RequestID: requestID, ExpiresAt: expires}, nil
}

func (d *Dispatcher) worker() {
	for topic := range d.ready {
		d.sem <- struct{}{}
		d.mu.Lock()
		st := d.topics[topic]
		var e *entry
		if st != nil {
			e = st.pending
			st.pending = nil
			st.queued = false
			if e != nil {
				st.running = true
			}
		}
		d.mu.Unlock()

		if e != nil {
			res := d.dispatch(context.Background(), e.topic, e.wakeup)
			if err := d.store.CompleteEvent(e.id, res.FCM, res.WebPush.Attempted, res.WebPush.Sent, res.WebPush.Failed, res.WebPush.Dead, time.Now().UTC()); err != nil {
				d.logger.Error("complete event", "err", err)
			}
			d.mu.Lock()
			if st.pending == nil {
				st.running = false
			}
			d.mu.Unlock()
		}
		<-d.sem
	}
}

type legResult struct {
	FCM     string
	WebPush WebPushResult
}

// dispatch runs the fan-out for one wake-up (§5.1/§6).
func (d *Dispatcher) dispatch(ctx context.Context, topic string, wakeup []byte) legResult {
	var res legResult
	if d.fcm == nil {
		res.FCM = store.FCMDisabled
	} else {
		if d.fcmBudget != nil {
			_ = d.fcmBudget.Wait(ctx, "fcm")
		}
		switch err := d.fcm.Send(ctx, topic, wakeup); {
		case err == nil, errors.Is(err, push.ErrEmptyTopic):
			res.FCM = store.FCMAccepted
		default:
			d.logger.Error("fcm publish failed", "err", err)
			res.FCM = store.FCMFailed
		}
	}
	if d.webpush == nil {
		return res
	}
	err := d.store.ForEachRegistrationForTopic(ctx, topic, d.batchSize, func(r store.Registration) error {
		if d.webpushBudget != nil {
			if err := d.webpushBudget.Wait(ctx, "webpush"); err != nil {
				return err
			}
		}
		d.webpushSem <- struct{}{}
		defer func() { <-d.webpushSem }()
		res.WebPush.Attempted++
		switch err := d.webpush.Send(ctx, r.Endpoint, r.P256DH, r.Auth, wakeup); {
		case err == nil:
			res.WebPush.Sent++
		case errors.Is(err, push.ErrGone):
			// Dead endpoints contribute only to aggregate results; no
			// registry write happens during dispatch (§6.2/§7).
			res.WebPush.Dead++
		default:
			res.WebPush.Failed++
			d.logger.Warn("webpush send failed", "err", err)
		}
		return nil
	})
	if err != nil {
		d.logger.Error("endpoint fan-out", "err", err)
	}
	return res
}

func (d *Dispatcher) fcmStatus() string {
	if d.fcm == nil {
		return store.FCMDisabled
	}
	return store.FCMSuppressed
}

// SendToEndpoint delivers one payload to a single registration's endpoint
// (the §5.3.1 self-test). It is outside the dispatch queue and the replay
// cache: a test is never a wake-up.
func (d *Dispatcher) SendToEndpoint(ctx context.Context, r store.Registration, payload []byte) error {
	if d.webpush == nil {
		return ErrEndpointLegDisabled
	}
	return d.webpush.Send(ctx, r.Endpoint, r.P256DH, r.Auth, payload)
}

// Idle reports whether no dispatch is queued, running, or waiting to be
// drained. It is advisory: a publish can arrive at any time, and the platform
// autostarts the machine on the next request.
func (d *Dispatcher) Idle() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, st := range d.topics {
		if st.running || st.queued || st.pending != nil {
			return false
		}
	}
	return true
}

// Sweep removes registry rows whose last_seen is older than ttl and prunes the
// event_log; both run on a low-frequency cadence (§7).
func (d *Dispatcher) Sweep(regTTL, retention time.Duration) {
	now := time.Now().UTC()
	if n, err := d.store.SweepRegistrations(regTTL, now); err != nil {
		d.logger.Error("registry sweep", "err", err)
	} else if n > 0 {
		d.logger.Info("registry sweep removed registrations", "count", n)
	}
	if n, err := d.store.PruneEventLog(retention, now); err != nil {
		d.logger.Error("event log prune", "err", err)
	} else if n > 0 {
		d.logger.Info("event log pruned", "count", n)
	}
}

// lru is a bounded in-memory topic -> highest seq cache. It is bandwidth/DoS
// protection only and MUST NOT be persisted (§5.5).
type lru struct {
	mu    sync.Mutex
	cap   int
	items map[string]*list.Element
	order *list.List
}

type lruItem struct {
	key string
	seq int64
}

func newLRU(capacity int) *lru {
	return &lru{cap: capacity, items: map[string]*list.Element{}, order: list.New()}
}

func (l *lru) Get(key string) (int64, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	el, ok := l.items[key]
	if !ok {
		return 0, false
	}
	l.order.MoveToFront(el)
	return el.Value.(*lruItem).seq, true
}

func (l *lru) Put(key string, seq int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if el, ok := l.items[key]; ok {
		el.Value.(*lruItem).seq = seq
		l.order.MoveToFront(el)
		return
	}
	el := l.order.PushFront(&lruItem{key: key, seq: seq})
	l.items[key] = el
	for l.order.Len() > l.cap {
		last := l.order.Back()
		if last == nil {
			break
		}
		l.order.Remove(last)
		delete(l.items, last.Value.(*lruItem).key)
	}
}
