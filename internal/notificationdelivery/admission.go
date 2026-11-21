package notificationdelivery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	AdmissionWindow       = time.Minute
	AdmissionDefaultLimit = 100
	AdmissionPathWindow   = time.Second
	AdmissionPathLimit    = 10
)

// AdmissionPermit is a short-lived reservation, not evidence of delivery.
// BotID is the public bot identity, never an API key or a destination.
type AdmissionPermit struct {
	BotID     string    `json:"bot_id"`
	Token     string    `json:"token"`
	GrantedAt time.Time `json:"granted_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Consumed  bool      `json:"consumed,omitempty"`
}

type AdmissionBackend interface {
	ReserveNotificationAdmission(context.Context, string, int) (AdmissionPermit, error)
	CheckNotificationAdmission(context.Context, AdmissionPermit) error
	ReleaseNotificationAdmission(context.Context, AdmissionPermit) error
	BlockNotificationAdmission(context.Context, string, time.Duration) error
}

// HTTPAdmissionBackend consumes the minute permit and the API-key/path second
// slot atomically. Only a digest of that request scope crosses this boundary.
type HTTPAdmissionBackend interface {
	CheckNotificationHTTPAdmission(context.Context, AdmissionPermit, string) error
}

// AdmissionDeferred proves that the admission layer has not called transport.
// It must not consume a durable network attempt or be classified as unknown.
type AdmissionDeferred struct {
	RetryAt time.Time
	Reason  string
	Cause   error
}

func (e *AdmissionDeferred) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("notification admission %s: %v", e.Reason, e.Cause)
	}
	return "notification admission " + e.Reason
}
func (e *AdmissionDeferred) Unwrap() error { return e.Cause }

func AdmissionRetryAt(err error) (time.Time, bool) {
	var deferred *AdmissionDeferred
	if errors.As(err, &deferred) {
		return deferred.RetryAt, true
	}
	return time.Time{}, false
}

// Admission keeps only one conservative deadline hint. PostgreSQL remains the
// authority for every grant, including after restart or across instances.
type Admission struct {
	backend AdmissionBackend
	botID   string
	limit   int
	mu      sync.Mutex
	waitTo  time.Time
}

func NewAdmission(backend AdmissionBackend, botID string, limit int) (*Admission, error) {
	botID = strings.TrimSpace(botID)
	if backend == nil || botID == "" || len(botID) > 128 || (limit != 5 && limit != 10 && limit != AdmissionDefaultLimit) {
		return nil, errors.New("invalid notification admission identity or verified limit")
	}
	return &Admission{backend: backend, botID: botID, limit: limit}, nil
}

func (a *Admission) BotID() string { return a.botID }

type AdmissionProvider interface{ NotificationAdmission() *Admission }
type admissionContextKey struct{}

// NextAdmissionAt is a conservative scheduling hint shared by this sender's
// runners. Reading it never grants a permit, touches storage or changes quotas.
// Acquire and the final HTTP admission remain authoritative after every wake.
func NextAdmissionAt(sender any) time.Time {
	provider, ok := sender.(AdmissionProvider)
	if !ok {
		return time.Time{}
	}
	admission := provider.NotificationAdmission()
	if admission == nil {
		return time.Time{}
	}
	admission.mu.Lock()
	defer admission.mu.Unlock()
	return admission.waitTo
}

type Reservation struct {
	admission *Admission
	permit    AdmissionPermit
	consumed  atomic.Bool
}

// Acquire runs before a worker marks its durable network attempt started.
// Legacy test senders without a provider preserve their existing behavior.
func Acquire(ctx context.Context, sender any) (context.Context, *Reservation, error) {
	provider, ok := sender.(AdmissionProvider)
	if !ok || provider.NotificationAdmission() == nil {
		return ctx, nil, nil
	}
	admission := provider.NotificationAdmission()
	reservation, err := admission.reserve(ctx)
	if err != nil {
		return ctx, nil, err
	}
	return context.WithValue(ctx, admissionContextKey{}, reservation), reservation, nil
}

func (r *Reservation) Release(ctx context.Context) error {
	if r == nil || r.consumed.Load() {
		return nil
	}
	if err := r.admission.backend.ReleaseNotificationAdmission(ctx, r.permit); err != nil {
		return err
	}
	return nil
}

func (a *Admission) remember(at time.Time) {
	a.mu.Lock()
	if at.After(a.waitTo) {
		a.waitTo = at
	}
	a.mu.Unlock()
}

func (a *Admission) deferred(err error) error {
	if _, ok := AdmissionRetryAt(err); !ok {
		err = &AdmissionDeferred{RetryAt: time.Now().UTC().Add(30 * time.Second), Reason: "store_unavailable", Cause: err}
	}
	at, _ := AdmissionRetryAt(err)
	a.remember(at)
	return err
}

func (a *Admission) reserve(ctx context.Context) (*Reservation, error) {
	if err := ctx.Err(); err != nil {
		return nil, cancelledAdmission(err)
	}
	a.mu.Lock()
	waitTo := a.waitTo
	a.mu.Unlock()
	if time.Now().Before(waitTo) {
		return nil, &AdmissionDeferred{RetryAt: waitTo, Reason: "budget_wait"}
	}
	permit, err := a.backend.ReserveNotificationAdmission(ctx, a.botID, a.limit)
	if err != nil {
		if ctx.Err() != nil {
			return nil, cancelledAdmission(ctx.Err())
		}
		return nil, a.deferred(err)
	}
	if permit.BotID != a.botID || permit.Token == "" || permit.GrantedAt.IsZero() || !permit.ExpiresAt.After(permit.GrantedAt) {
		return nil, a.deferred(errors.New("invalid admission reservation"))
	}
	return &Reservation{admission: a, permit: permit}, nil
}

func cancelledAdmission(err error) error {
	return &AdmissionDeferred{RetryAt: time.Now().UTC().Add(30 * time.Second), Reason: "cancelled_before_transport", Cause: err}
}

// BeforeHTTP belongs at the one innermost sendMessage transport boundary.
// Worker permits never sleep; ordinary bot interactions wait at most maxWait.
// Returning nil authorizes exactly one following Do, never a retry or redirect.
func (a *Admission) BeforeHTTP(ctx context.Context, maxWait time.Duration) (context.Context, error) {
	return a.beforeHTTP(ctx, maxWait, "")
}

// BeforeHTTPPath is used by the real sendMessage transport. A backend without
// durable second-level admission must fail closed rather than silently use
// only a process-local or minute-level limit.
func (a *Admission) BeforeHTTPPath(ctx context.Context, maxWait time.Duration, scopeHash string) (context.Context, error) {
	if len(scopeHash) != 64 {
		return ctx, a.deferred(errors.New("invalid notification HTTP scope"))
	}
	if _, ok := a.backend.(HTTPAdmissionBackend); !ok {
		return ctx, a.deferred(errors.New("durable notification HTTP admission unavailable"))
	}
	return a.beforeHTTP(ctx, maxWait, scopeHash)
}

func (a *Admission) beforeHTTP(ctx context.Context, maxWait time.Duration, scopeHash string) (context.Context, error) {
	if reservation, ok := ctx.Value(admissionContextKey{}).(*Reservation); ok {
		if reservation == nil || reservation.admission != a || reservation.permit.BotID != a.botID {
			return ctx, &AdmissionDeferred{RetryAt: time.Now().UTC().Add(AdmissionWindow), Reason: "foreign_permit"}
		}
		if err := ctx.Err(); err != nil {
			return ctx, cancelledAdmission(err)
		}
		if reservation.consumed.Load() {
			return ctx, &AdmissionDeferred{RetryAt: reservation.permit.ExpiresAt, Reason: "permit_already_used"}
		}
		var checkErr error
		if scopeHash != "" {
			checkErr = a.backend.(HTTPAdmissionBackend).CheckNotificationHTTPAdmission(ctx, reservation.permit, scopeHash)
		} else {
			checkErr = a.backend.CheckNotificationAdmission(ctx, reservation.permit)
		}
		if err := checkErr; err != nil {
			if ctx.Err() != nil {
				return ctx, cancelledAdmission(ctx.Err())
			}
			return ctx, a.deferred(err)
		}
		if !reservation.consumed.CompareAndSwap(false, true) {
			return ctx, &AdmissionDeferred{RetryAt: reservation.permit.ExpiresAt, Reason: "permit_already_used"}
		}
		return ctx, nil
	}
	if maxWait < 0 {
		maxWait = 0
	}
	if maxWait > 20*time.Second {
		maxWait = 20 * time.Second
	}
	waitCtx := ctx
	if maxWait > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, maxWait)
		defer cancel()
	}
	for {
		reservation, err := a.reserve(waitCtx)
		if err == nil {
			permitted := context.WithValue(ctx, admissionContextKey{}, reservation)
			_, checkErr := a.beforeHTTP(context.WithValue(waitCtx, admissionContextKey{}, reservation), 0, scopeHash)
			if checkErr != nil {
				cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
				_ = reservation.Release(cleanupCtx)
				cleanupCancel()
			}
			if checkErr == nil {
				return permitted, nil
			}
			err = checkErr
		}
		at, _ := AdmissionRetryAt(err)
		delay := time.Until(at)
		if delay <= 0 {
			delay = time.Millisecond
		}
		if maxWait == 0 {
			return ctx, err
		}
		timer := time.NewTimer(delay)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			return ctx, err
		case <-timer.C:
		}
	}
}

func (a *Admission) ObserveRetryAfter(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	// Hint-only fail-closed behavior if the durable write is unavailable.
	a.remember(time.Now().UTC().Add(delay))
	return a.backend.BlockNotificationAdmission(ctx, a.botID, delay)
}
