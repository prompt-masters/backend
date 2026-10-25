package service

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// syncLog is a log sink safe for background goroutines.
type syncLog struct {
	mu sync.Mutex
	sb strings.Builder
}

func (l *syncLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sb.Write(p)
}

func (l *syncLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sb.String()
}

// waitFor polls cond, for work that runs on its own goroutine.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}
