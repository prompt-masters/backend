package ai

import (
	"context"
	"sync"
	"time"
)

// limiter is a token bucket: Burst calls at once, refilled at RPS. A waiting
// call gives up as soon as its context is done, and returns its token.
type limiter struct {
	rate  float64
	burst float64
	sleep func(context.Context, time.Duration) error

	mu     sync.Mutex
	tokens float64
	last   time.Time
}

func newLimiter(rl RateLimit, sleep func(context.Context, time.Duration) error) *limiter {
	return &limiter{rate: rl.RPS, burst: float64(rl.Burst), sleep: sleep, tokens: float64(rl.Burst), last: time.Now()}
}

func (l *limiter) Wait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	now := time.Now()
	l.tokens = min(l.burst, l.tokens+now.Sub(l.last).Seconds()*l.rate)
	l.last = now
	// Take the token now, even into debt, so waiters queue in order.
	l.tokens--
	var wait time.Duration
	if l.tokens < 0 {
		wait = time.Duration(-l.tokens / l.rate * float64(time.Second))
	}
	l.mu.Unlock()

	if wait == 0 {
		return nil
	}
	if err := l.sleep(ctx, wait); err != nil {
		l.mu.Lock()
		l.tokens++
		l.mu.Unlock()
		return err
	}
	return nil
}

// sleepCtx waits d, returning early with ctx's error once ctx is done.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
