package ai_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/prompt-masters/backend/internal/ai"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	// Orchestrator stream goroutines must never outlive a call.
	goleak.VerifyTestMain(m)
}

func TestKindForStatus(t *testing.T) {
	tests := map[int]ai.ErrorKind{
		400: ai.ErrKindInvalidRequest, 401: ai.ErrKindAuth, 403: ai.ErrKindAuth, 404: ai.ErrKindInvalidRequest,
		408: ai.ErrKindTimeout, 413: ai.ErrKindInvalidRequest, 429: ai.ErrKindRateLimit,
		500: ai.ErrKindUnavailable, 502: ai.ErrKindUnavailable, 503: ai.ErrKindOverloaded, 529: ai.ErrKindOverloaded,
		418: ai.ErrKindUnknown,
	}
	for status, want := range tests {
		if got := ai.KindForStatus(status); got != want {
			t.Errorf("KindForStatus(%d) = %s, want %s", status, got, want)
		}
	}
	for kind, want := range map[ai.ErrorKind]bool{
		ai.ErrKindTimeout: true, ai.ErrKindRateLimit: true, ai.ErrKindOverloaded: true, ai.ErrKindUnavailable: true,
		ai.ErrKindCanceled: false, ai.ErrKindAuth: false, ai.ErrKindInvalidRequest: false,
		ai.ErrKindContentFiltered: false, ai.ErrKindInvalidOutput: false, ai.ErrKindUnknown: false,
	} {
		if kind.Retryable() != want {
			t.Errorf("%s.Retryable() = %v, want %v", kind, !want, want)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		header http.Header
		want   time.Duration
	}{
		{http.Header{"Retry-After": {"3"}}, 3 * time.Second},
		{http.Header{"Retry-After": {"1.5"}}, 1500 * time.Millisecond},
		{http.Header{"Retry-After": {now.Add(10 * time.Second).Format(http.TimeFormat)}}, 10 * time.Second},
		{http.Header{"Retry-After-Ms": {"250"}, "Retry-After": {"9"}}, 250 * time.Millisecond},
		{http.Header{"Retry-After": {"soon"}}, 0},
		{http.Header{}, 0},
	}
	for _, tt := range tests {
		if got := ai.ParseRetryAfter(tt.header, now); got != tt.want {
			t.Errorf("ParseRetryAfter(%v) = %s, want %s", tt.header, got, tt.want)
		}
	}
}

func TestNormalizeContextErrors(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancel2 := context.WithTimeout(context.Background(), -time.Second)
	defer cancel2()

	if err := ai.Normalize(canceled, "p", errors.New("x")); err.Kind != ai.ErrKindCanceled || !errors.Is(err, context.Canceled) {
		t.Errorf("canceled ctx → %+v", err)
	}
	if err := ai.Normalize(expired, "p", errors.New("x")); err.Kind != ai.ErrKindTimeout || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expired ctx → %+v", err)
	}
	// A provider's own timeout is overridden when the caller cancelled.
	providerTimeout := &ai.AIError{Kind: ai.ErrKindTimeout, Provider: "p"}
	if err := ai.Normalize(canceled, "p", providerTimeout); err.Kind != ai.ErrKindCanceled {
		t.Errorf("caller cancel over provider timeout → %s", err.Kind)
	}
	if err := ai.Normalize(context.Background(), "p", errors.New("boom")); err.Kind != ai.ErrKindUnknown {
		t.Errorf("plain error → %s", err.Kind)
	}
	var target *ai.AIError
	if !errors.As(fmt.Errorf("wrapped: %w", providerTimeout), &target) || target != providerTimeout {
		t.Error("errors.As does not find a wrapped AIError")
	}
}

func TestAPIKeyNeverPrints(t *testing.T) {
	key := ai.NewAPIKey("sk-super-secret")
	holder := struct {
		Name string
		Key  ai.APIKey
	}{"anthropic", key}

	js, _ := json.Marshal(holder)
	var logged strings.Builder
	slog.New(slog.NewTextHandler(&logged, nil)).Info("cfg", "key", key)
	for name, out := range map[string]string{
		"%v": fmt.Sprintf("%v", key), "%s": fmt.Sprintf("%s", key), "%#v": fmt.Sprintf("%#v", holder),
		"%+v": fmt.Sprintf("%+v", holder), "String": key.String(), "json": string(js), "slog": logged.String(),
	} {
		if strings.Contains(out, "sk-super-secret") || !strings.Contains(out, "[REDACTED]") {
			t.Errorf("%s printed %q", name, out)
		}
	}
	if key.Reveal() != "sk-super-secret" {
		t.Error("Reveal() lost the key")
	}
}

func TestSafeMessageRedactsAndTruncates(t *testing.T) {
	key := ai.NewAPIKey("sk-abc")
	if got := ai.SafeMessage("bad key sk-abc given", key); got != "bad key [REDACTED] given" {
		t.Errorf("SafeMessage = %q", got)
	}
	if got := ai.SafeMessage(strings.Repeat("x", 1000), key); len(got) > 310 {
		t.Errorf("SafeMessage kept %d bytes", len(got))
	}
}

func TestConfigValidate(t *testing.T) {
	cfg := testConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid config: %v", err)
	}

	missing := testConfig()
	missing.Providers = map[string]ai.ProviderSettings{"exec": {APIKey: ai.NewAPIKey(secretKey)}}
	err := missing.Validate()
	if err == nil || !strings.Contains(err.Error(), "JUDGE_API_KEY") || !strings.Contains(err.Error(), "ALT_API_KEY") {
		t.Fatalf("Validate() = %v, want the missing keys named", err)
	}
	if strings.Contains(err.Error(), secretKey) {
		t.Error("Validate() printed a key")
	}

	bad := testConfig()
	bad.Retry.MaxAttempts = 0
	bad.Judge.Timeout = 0
	bad.RateLimits = map[string]ai.RateLimit{"exec": {RPS: 0, Burst: 1}}
	if err := bad.Validate(); err == nil || strings.Count(err.Error(), "\n") < 2 {
		t.Errorf("Validate() = %v, want every problem reported", err)
	}
	if got := testConfig().ProviderNames(); strings.Join(got, ",") != "alt,exec,judge" {
		t.Errorf("ProviderNames() = %v", got)
	}
}

func TestRegistry(t *testing.T) {
	built := map[string]bool{}
	reg := ai.NewRegistry(map[string]ai.Factory{
		"exec": func(ai.ProviderConfig) (ai.AIClient, error) { built["exec"] = true; return nil, nil },
		"judge": func(cfg ai.ProviderConfig) (ai.AIClient, error) {
			built["judge"] = true
			if cfg.APIKey.Reveal() != secretKey {
				t.Error("factory did not receive the key")
			}
			return nil, nil
		},
		"alt": func(ai.ProviderConfig) (ai.AIClient, error) { built["alt"] = true; return nil, nil },
	})
	if _, err := reg.Clients(testConfig(), nil); err != nil || len(built) != 3 {
		t.Errorf("Clients() = %v, built %v", err, built)
	}
	if _, err := reg.New("nope", ai.ProviderConfig{}); err == nil {
		t.Error("New() for an unknown provider succeeded")
	}
}

func TestPricing(t *testing.T) {
	p := ai.Pricing{"m": {InputPerMTok: 3, OutputPerMTok: 15}}
	if c := p.Cost("m", ai.Usage{InputTokens: 2_000_000, OutputTokens: 100_000}); c == nil || *c != 7.5 {
		t.Errorf("Cost = %v, want 7.5", c)
	}
	if p.Cost("other", ai.Usage{InputTokens: 1}) != nil {
		t.Error("unpriced model has a cost")
	}
}
