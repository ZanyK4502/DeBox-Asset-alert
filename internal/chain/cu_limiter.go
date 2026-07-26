package chain

import (
	"context"
	"sync"
	"time"
)

type cuRateLimiter struct {
	mu             sync.Mutex
	unitsPerSecond int64
	next           time.Time
}

func newCURateLimiter(unitsPerSecond int64) *cuRateLimiter {
	if unitsPerSecond <= 0 {
		return nil
	}
	return &cuRateLimiter{unitsPerSecond: unitsPerSecond}
}

func (c *Client) waitForCU(ctx context.Context, units int64) error {
	if c == nil || c.cuLimiter == nil || units <= 0 {
		return nil
	}
	return c.cuLimiter.wait(ctx, units)
}

func (limiter *cuRateLimiter) wait(ctx context.Context, units int64) error {
	if limiter == nil || limiter.unitsPerSecond <= 0 || units <= 0 {
		return nil
	}

	now := time.Now()
	limiter.mu.Lock()
	start := now
	if limiter.next.After(start) {
		start = limiter.next
	}
	reservation := time.Duration(
		(units*int64(time.Second) + limiter.unitsPerSecond - 1) /
			limiter.unitsPerSecond,
	)
	limiter.next = start.Add(reservation)
	limiter.mu.Unlock()

	delay := time.Until(start)
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
