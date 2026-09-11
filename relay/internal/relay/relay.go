// Package relay orchestrates the fan-out of a wake-up across the enabled
// delivery legs (SPECIFICATION §5.1, §6): FCM topics, ntfy topics, and
// WebPush registrations — concurrently across providers, best-effort, with
// the outcome recorded in event_log (hashes only).
package relay

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/v1b3coder/keryx/relay/internal/push"
	"github.com/v1b3coder/keryx/relay/internal/store"
	"github.com/v1b3coder/keryx/relay/internal/topic"
)

// FCMLeg is the FCM topic leg (implemented by *push.FCM).
type FCMLeg interface {
	Send(ctx context.Context, topic string, data map[string]string) error
}

// NtfyLeg is the ntfy topic leg (implemented by *push.Ntfy).
type NtfyLeg interface {
	Send(ctx context.Context, topic string, payload []byte) error
}

// WebPushLeg is the WebPush registry leg (implemented by *push.WebPush).
type WebPushLeg interface {
	Send(ctx context.Context, endpoint, p256dh, auth string, payload []byte) error
}

// Dispatcher fans out wake-ups. Nil legs (fcm/ntfy) are disabled.
type Dispatcher struct {
	store   *store.Store
	fcm     FCMLeg
	ntfy    NtfyLeg
	webpush WebPushLeg
	wpConc  int
	logger  *slog.Logger
}

// Result is the §5.1 delivered summary.
type Result struct {
	FCM     int           `json:"fcm"`
	Ntfy    int           `json:"ntfy"`
	WebPush WebPushResult `json:"webpush"`
}

// WebPushResult counts per-registration outcomes (§5.1).
type WebPushResult struct {
	Sent    int `json:"sent"`
	Failed  int `json:"failed"`
	Removed int `json:"removed"`
}

// New builds a Dispatcher. fcm/ntfy may be nil (leg disabled).
func New(st *store.Store, fcm FCMLeg, ntfy NtfyLeg, webpush WebPushLeg, wpConc int, logger *slog.Logger) *Dispatcher {
	if wpConc <= 0 {
		wpConc = 32
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Dispatcher{store: st, fcm: fcm, ntfy: ntfy, webpush: webpush, wpConc: wpConc, logger: logger}
}

// Publish fans out one wake-up (validated h) and records the event.
// h is opaque: the relay cannot (and need not) tell what kind of wake-up it
// is — the type is resolved client-side (§3).
func (d *Dispatcher) Publish(ctx context.Context, publisherID int64, h string, n, seq *int) (Result, error) {
	tpc, err := topic.Topic(h)
	if err != nil {
		return Result{}, err
	}
	// §4 payloads: topic legs carry no t; WebPush carries t.
	topicPayload, err := (push.Wakeup{V: 1, N: n, Seq: seq}).JSON()
	if err != nil {
		return Result{}, err
	}
	webpushPayload, err := (push.Wakeup{V: 1, T: tpc, N: n, Seq: seq}).JSON()
	if err != nil {
		return Result{}, err
	}

	var res Result
	var wg sync.WaitGroup

	// Topic legs: single calls.
	if d.fcm != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := d.fcm.Send(ctx, tpc, (push.Wakeup{V: 1, N: n, Seq: seq}).DataMap())
			switch {
			case err == nil || errors.Is(err, push.ErrEmptyTopic):
				res.FCM = 1 // empty topic is not an error (§6.1)
			case errors.Is(err, push.ErrCredentials):
				d.logger.Error("ALARM: FCM credentials rejected; check the service account",
					"topic", tpc)
			default:
				d.logger.Error("fcm publish failed", "topic", tpc, "err", err)
			}
		}()
	}
	if d.ntfy != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := d.ntfy.Send(ctx, tpc, topicPayload); err != nil {
				d.logger.Error("ntfy publish failed", "topic", tpc, "err", err)
			} else {
				res.Ntfy = 1
			}
		}()
	}

	// Registry leg: one send per registration, concurrent with a cap.
	wpRes, err := d.fanoutWebPush(ctx, tpc, webpushPayload)
	if err != nil {
		wg.Wait()
		return res, err
	}
	res.WebPush = wpRes
	wg.Wait()

	// Audit/abuse record (§7): hashes only, never payloads.
	if err := d.store.LogEvent(publisherID, tpc, res.FCM, res.Ntfy,
		wpRes.Sent, wpRes.Failed, wpRes.Removed); err != nil {
		return res, err
	}
	return res, nil
}

// fanoutWebPush sends to every registration following the topic, counting
// sent/failed/removed. Dead registrations (404/410) are deleted from the
// registry (§6.2).
func (d *Dispatcher) fanoutWebPush(ctx context.Context, tpc string, payload []byte) (WebPushResult, error) {
	if d.webpush == nil {
		return WebPushResult{}, nil
	}
	regs, err := d.store.RegistrationsForTopic(tpc)
	if err != nil {
		return WebPushResult{}, err
	}
	var res WebPushResult
	sem := make(chan struct{}, d.wpConc)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, reg := range regs {
		reg := reg
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			err := d.webpush.Send(ctx, reg.Endpoint, reg.P256DH, reg.Auth, payload)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				res.Sent++
			case errors.Is(err, push.ErrGone):
				res.Removed++
				if derr := d.store.DeleteRegistration(reg.ID); derr != nil && !errors.Is(derr, store.ErrNotFound) {
					d.logger.Error("remove dead registration", "id", reg.ID, "err", derr)
				}
			default:
				res.Failed++
				d.logger.Debug("webpush send failed", "topic", tpc, "endpoint", reg.Endpoint, "err", err)
			}
		}()
	}
	wg.Wait()
	return res, nil
}
