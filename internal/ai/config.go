package ai

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Config is the AI layer's configuration, loaded from the environment by the
// config package. Providers and models are named here only, so swapping
// either is a configuration change.
type Config struct {
	Executor RoleConfig
	Judge    RoleConfig
	// ExecutorModels maps a game's ai_model setting to the provider and model
	// that play it. A game without a mapped model uses Executor.
	ExecutorModels map[string]Target
	Retry          RetryConfig
	// RateLimits holds a client-side limit per provider name; a provider
	// without one is not limited.
	RateLimits map[string]RateLimit
	Pricing    Pricing
	// Providers holds the credentials and endpoint of each provider name.
	Providers map[string]ProviderSettings
	// JudgeParseRetries is how many more times the judge is asked when its
	// output cannot be parsed.
	JudgeParseRetries int
	// LogContent logs prompts and responses. For debugging only; never
	// enable it in production.
	LogContent bool
}

// Target is a provider and one of its model IDs.
type Target struct {
	Provider string
	Model    string
}

func (t Target) String() string { return t.Provider + "/" + t.Model }

// RoleConfig configures one role's calls.
type RoleConfig struct {
	Target
	// Timeout bounds a whole call, retries included.
	Timeout time.Duration
	// Temperature is sent only when set.
	Temperature *float64
	MaxTokens   int
}

type RetryConfig struct {
	// MaxAttempts counts the first attempt; 1 disables retries.
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

type RateLimit struct {
	RPS   float64
	Burst int
}

type ProviderSettings struct {
	APIKey APIKey
	// BaseURL overrides the provider's default endpoint.
	BaseURL string
}

// Providers returns every provider name the configuration uses, sorted.
func (c Config) ProviderNames() []string {
	names := []string{c.Executor.Provider, c.Judge.Provider}
	for _, t := range c.ExecutorModels {
		names = append(names, t.Provider)
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// Validate reports every problem at once. It checks that each provider in use
// has a key, and never includes a key in its message.
func (c Config) Validate() error {
	var errs []error
	for name, role := range map[Role]RoleConfig{RoleExecutor: c.Executor, RoleJudge: c.Judge} {
		prefix := "AI_" + strings.ToUpper(string(name))
		if role.Provider == "" || role.Model == "" {
			errs = append(errs, fmt.Errorf("%s_PROVIDER and %s_MODEL are required", prefix, prefix))
		}
		if role.Timeout <= 0 {
			errs = append(errs, fmt.Errorf("%s_TIMEOUT must be positive", prefix))
		}
		if role.MaxTokens <= 0 {
			errs = append(errs, fmt.Errorf("%s_MAX_TOKENS must be positive", prefix))
		}
	}
	for model, t := range c.ExecutorModels {
		if t.Provider == "" || t.Model == "" {
			errs = append(errs, fmt.Errorf("AI_EXECUTOR_MODELS: %q must map to provider/model", model))
		}
	}
	if c.Retry.MaxAttempts < 1 {
		errs = append(errs, errors.New("AI_RETRY_MAX_ATTEMPTS must be at least 1"))
	}
	if c.Retry.BaseDelay <= 0 || c.Retry.MaxDelay < c.Retry.BaseDelay {
		errs = append(errs, errors.New("AI_RETRY_BASE_DELAY must be positive and AI_RETRY_MAX_DELAY at least as long"))
	}
	for name, rl := range c.RateLimits {
		if rl.RPS <= 0 || rl.Burst < 1 {
			errs = append(errs, fmt.Errorf("rate limit for %s needs a positive RPS and a burst of at least 1", name))
		}
	}
	if c.JudgeParseRetries < 0 {
		errs = append(errs, errors.New("AI_JUDGE_PARSE_RETRIES must not be negative"))
	}
	for _, name := range c.ProviderNames() {
		if name == "" {
			continue
		}
		if c.Providers[name].APIKey.Empty() {
			errs = append(errs, fmt.Errorf("the API key for AI provider %q is not set (%s_API_KEY)", name, strings.ToUpper(name)))
		}
	}
	return errors.Join(errs...)
}
