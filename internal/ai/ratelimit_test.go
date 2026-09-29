package ai

import (
	"context"
	"testing"
	"time"
)

func TestLimiterAllowsBurstThenPaces(t *testing.T) {
	var waits []time.Duration
	l := newLimiter(RateLimit{RPS: 10, Burst: 2}, func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		return ctx.Err()
	})
	for range 3 {
		if err := l.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(waits) != 1 || waits[0] < 90*time.Millisecond || waits[0] > 100*time.Millisecond {
		t.Errorf("waits = %v, want the burst free and the third call paced ~100ms", waits)
	}
}

func TestLimiterWaitRespectsContext(t *testing.T) {
	l := newLimiter(RateLimit{RPS: 0.001, Burst: 1}, sleepCtx)
	if err := l.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := l.Wait(ctx); err == nil {
		t.Fatal("Wait() succeeded without a token")
	}
	if time.Since(start) > time.Second {
		t.Error("Wait() ignored the context deadline")
	}
	// The abandoned token was returned, not burnt.
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.tokens < -0.01 {
		t.Errorf("tokens = %f after a cancelled wait, want the token refunded", l.tokens)
	}
}
