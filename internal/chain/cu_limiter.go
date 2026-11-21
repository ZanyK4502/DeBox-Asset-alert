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
	nextHolder     time.Time
	blockedUntil   time.Time
}

func newCURateLimiter(unitsPerSecond int64) *cuRateLimiter {
	if unitsPerSecond <= 0 {
		return nil
	}
	return &cuRateLimiter{unitsPerSecond: unitsPerSecond}
}

func (c *Client) waitForCU(ctx context.Context, usage NoditUsage) error {
	if c == nil || c.cuLimiter == nil || usage.CU <= 0 {
		return nil
	}
	if usage.Operation == tokenHoldersOperation && c.holderCUPerSecond > 0 {
		spacing := cuSpacing(usage.CU, c.holderCUPerSecond)
		return c.cuLimiter.waitWithHolderSpacing(ctx, usage.CU, spacing)
	}
	return c.cuLimiter.wait(ctx, usage.CU)
}

func (limiter *cuRateLimiter) wait(ctx context.Context, units int64) error {
	return limiter.waitWithHolderSpacing(ctx, units, 0)
}

func cuSpacing(units, unitsPerSecond int64) time.Duration {
	return time.Duration((units*int64(time.Second) + unitsPerSecond - 1) / unitsPerSecond)
}

func (limiter *cuRateLimiter) waitWithHolderSpacing(ctx context.Context, units int64, holderSpacing time.Duration) error {
	if limiter == nil || limiter.unitsPerSecond <= 0 || units <= 0 {
		return nil
	}

	reservation := cuSpacing(units, limiter.unitsPerSecond)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		now := time.Now()
		limiter.mu.Lock()
		start := limiter.next
		if limiter.blockedUntil.After(start) {
			start = limiter.blockedUntil
		}
		if holderSpacing > 0 && limiter.nextHolder.After(start) {
			start = limiter.nextHolder
		}
		if !start.After(now) {
			// Commit only at admission: cancelled waiters leave no phantom
			// reservations. Recheck new 429 blocks instead of releasing all
			// previously queued callers in one burst.
			limiter.next = now.Add(reservation)
			if holderSpacing > 0 {
				limiter.nextHolder = now.Add(holderSpacing)
			}
			limiter.mu.Unlock()
			return nil
		}
		limiter.mu.Unlock()
		timer := time.NewTimer(time.Until(start))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (limiter *cuRateLimiter) deferFor(delay time.Duration) {
	if limiter == nil || delay <= 0 {
		return
	}
	until := time.Now().Add(delay)
	limiter.mu.Lock()
	if until.After(limiter.blockedUntil) {
		limiter.blockedUntil = until
	}
	limiter.mu.Unlock()
}
