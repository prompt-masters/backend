package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/prompt-masters/backend/internal/ai"
	"github.com/prompt-masters/backend/internal/domain"
	"github.com/prompt-masters/backend/internal/mail"
	"github.com/prompt-masters/backend/internal/redisclient"
	"github.com/prompt-masters/backend/internal/ws"
)

const (
	defaultPort            = "8080"
	defaultJWTTTL          = 15 * time.Minute
	defaultRefreshTokenTTL = 30 * 24 * time.Hour
	defaultMailPort        = 587

	defaultRedisAddr         = "localhost:6379"
	defaultRedisPoolSize     = 20
	defaultRedisDialTimeout  = 2 * time.Second
	defaultRedisReadTimeout  = time.Second
	defaultRedisWriteTimeout = time.Second
	defaultRedisOpTimeout    = 2 * time.Second
	defaultRedisKeyPrefix    = "promptgame"
	defaultActiveGameTTL     = 2 * time.Hour
	defaultEndedGameTTL      = 15 * time.Minute

	defaultJudgeMaxRuns    = 3
	defaultJudgeRetryDelay = 5 * time.Second
	defaultJudgeStaleAfter = 5 * time.Minute
	defaultResultsDuration = 15 * time.Second

	defaultPromptTestMaxBytes      = 8 << 10
	defaultPromptTestMaxConcurrent = 1
	defaultPromptTestStaleAfter    = 5 * time.Minute

	defaultAIProvider          = "anthropic"
	defaultAIModel             = "claude-opus-5"
	defaultAIExecutorTimeout   = 60 * time.Second
	defaultAIExecutorMaxTokens = 4096
	defaultAIJudgeTimeout      = 90 * time.Second
	defaultAIJudgeMaxTokens    = 8192
	defaultAIRetryAttempts     = 3
	defaultAIRetryBaseDelay    = 250 * time.Millisecond
	defaultAIRetryMaxDelay     = 4 * time.Second
	defaultAIJudgeParseRetries = 1
	// Every game ai_model setting is a Claude model ID, played on Anthropic
	// unless AI_EXECUTOR_MODELS says otherwise.
	defaultAIExecutorModels = "claude-opus-5=anthropic/claude-opus-5,claude-sonnet-5=anthropic/claude-sonnet-5,claude-haiku-4-5=anthropic/claude-haiku-4-5"
)

type Config struct {
	Host            string
	Port            string
	DatabaseURL     string
	JWTSecret       string
	JWTTTL          time.Duration
	RefreshTokenTTL time.Duration
	AppBaseURL      string
	Mail            mail.Config
	Redis           redisclient.Config
	WS              WSConfig
	// AIEnabled turns on the AI layer; AI is only loaded and validated when
	// it is set.
	AIEnabled   bool
	AI          ai.Config
	PromptTests PromptTestConfig
	Judging     JudgingConfig
	Helpers     HelperConfig
	// ResultsDuration is how long a round's results are shown before the
	// game moves on by itself.
	ResultsDuration time.Duration
}

// JudgingConfig controls how rounds are judged and scored.
type JudgingConfig struct {
	// MaxRuns is how many times a round's judging runs before giving up
	// and waiting for the host to retry.
	MaxRuns int
	// RetryDelay is the wait before the second run; it doubles each run.
	RetryDelay time.Duration
	// StaleAfter is when a run still marked running is taken to have lost
	// its worker and may be claimed again.
	StaleAfter time.Duration
	Weights    domain.ScoreWeights
}

// PromptTestConfig limits the prompt tests players run during a round.
type PromptTestConfig struct {
	// MaxPromptBytes caps a tested prompt.
	MaxPromptBytes int
	// MaxConcurrent is how many unfinished tests one player may have in a
	// round.
	MaxConcurrent int
	// StaleAfter is when an unfinished test stops counting against the
	// limit and is failed at start-up: its server must have stopped.
	StaleAfter time.Duration
}

func (c PromptTestConfig) Validate() error {
	if c.MaxPromptBytes <= 0 || c.MaxConcurrent <= 0 || c.StaleAfter <= 0 {
		return errors.New("PROMPT_TEST_MAX_BYTES, PROMPT_TEST_MAX_CONCURRENT and PROMPT_TEST_STALE_AFTER must be positive")
	}
	return nil
}

// WSConfig configures the WebSocket hub and which browser origins may open a
// connection.
type WSConfig struct {
	Hub ws.Config
	// AllowedOrigins are extra origins accepted besides same-origin requests.
	AllowedOrigins []string
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	hubDefaults := ws.DefaultConfig()

	cfg := &Config{
		Host:        os.Getenv("HOST"),
		Port:        os.Getenv("PORT"),
		DatabaseURL: os.Getenv("DATABASE_URL"),
		JWTSecret:   os.Getenv("JWT_SECRET"),
		AppBaseURL:  os.Getenv("APP_BASE_URL"),
		Mail: mail.Config{
			Host:      os.Getenv("MAIL_HOST"),
			Port:      envInt("MAIL_PORT", defaultMailPort),
			Username:  os.Getenv("MAIL_USERNAME"),
			Password:  os.Getenv("MAIL_PASSWORD"),
			FromEmail: os.Getenv("MAIL_FROM_EMAIL"),
		},
		Redis: redisclient.Config{
			Addr:          envString("REDIS_ADDR", defaultRedisAddr),
			Username:      os.Getenv("REDIS_USERNAME"),
			Password:      os.Getenv("REDIS_PASSWORD"),
			DB:            envInt("REDIS_DB", 0),
			TLS:           envBool("REDIS_TLS", false),
			TLSCAFile:     os.Getenv("REDIS_TLS_CA_FILE"),
			TLSServerName: os.Getenv("REDIS_TLS_SERVER_NAME"),
			PoolSize:      envInt("REDIS_POOL_SIZE", defaultRedisPoolSize),
			DialTimeout:   envDuration("REDIS_DIAL_TIMEOUT", defaultRedisDialTimeout),
			ReadTimeout:   envDuration("REDIS_READ_TIMEOUT", defaultRedisReadTimeout),
			WriteTimeout:  envDuration("REDIS_WRITE_TIMEOUT", defaultRedisWriteTimeout),
			OpTimeout:     envDuration("REDIS_OP_TIMEOUT", defaultRedisOpTimeout),
			KeyPrefix:     envString("REDIS_KEY_PREFIX", defaultRedisKeyPrefix),
			ActiveGameTTL: envDuration("GAME_STATE_ACTIVE_TTL", defaultActiveGameTTL),
			EndedGameTTL:  envDuration("GAME_STATE_ENDED_TTL", defaultEndedGameTTL),
		},
		WS: WSConfig{
			Hub: ws.Config{
				PingInterval:          envDuration("WS_PING_INTERVAL", hubDefaults.PingInterval),
				PongWait:              envDuration("WS_PONG_WAIT", hubDefaults.PongWait),
				WriteWait:             envDuration("WS_WRITE_WAIT", hubDefaults.WriteWait),
				SendBuffer:            envInt("WS_SEND_BUFFER", hubDefaults.SendBuffer),
				MaxMessageBytes:       int64(envInt("WS_MAX_MESSAGE_BYTES", int(hubDefaults.MaxMessageBytes))),
				MaxConnectionsPerUser: envInt("WS_MAX_CONNECTIONS_PER_USER", hubDefaults.MaxConnectionsPerUser),
			},
			AllowedOrigins: envList("WS_ALLOWED_ORIGINS"),
		},
		PromptTests: PromptTestConfig{
			MaxPromptBytes: envInt("PROMPT_TEST_MAX_BYTES", defaultPromptTestMaxBytes),
			MaxConcurrent:  envInt("PROMPT_TEST_MAX_CONCURRENT", defaultPromptTestMaxConcurrent),
			StaleAfter:     envDuration("PROMPT_TEST_STALE_AFTER", defaultPromptTestStaleAfter),
		},
	}

	if cfg.Port == "" {
		cfg.Port = defaultPort
	}
	if cfg.AppBaseURL == "" {
		cfg.AppBaseURL = "http://localhost:" + cfg.Port
	}

	jwtTTL, err := time.ParseDuration(os.Getenv("JWT_TTL"))
	if err != nil {
		cfg.JWTTTL = defaultJWTTTL
	} else {
		cfg.JWTTTL = jwtTTL
	}

	refreshTTL, err := time.ParseDuration(os.Getenv("REFRESH_TOKEN_TTL"))
	if err != nil {
		cfg.RefreshTokenTTL = defaultRefreshTokenTTL
	} else {
		cfg.RefreshTokenTTL = refreshTTL
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.JWTSecret == "" {
		return nil, fmt.Errorf("JWT_SECRET is required")
	}
	if err := cfg.Redis.Validate(); err != nil {
		return nil, fmt.Errorf("invalid Redis config: %w", err)
	}
	if err := cfg.WS.Hub.Validate(); err != nil {
		return nil, fmt.Errorf("invalid WebSocket config: %w", err)
	}
	if err := cfg.PromptTests.Validate(); err != nil {
		return nil, err
	}
	if cfg.Judging, err = loadJudging(); err != nil {
		return nil, fmt.Errorf("invalid judging config: %w", err)
	}
	if cfg.Helpers, err = loadHelpers(); err != nil {
		return nil, fmt.Errorf("invalid helper config: %w", err)
	}
	if cfg.ResultsDuration = envDuration("RESULTS_DURATION", defaultResultsDuration); cfg.ResultsDuration <= 0 {
		return nil, errors.New("RESULTS_DURATION must be positive")
	}
	if cfg.AIEnabled = envBool("AI_ENABLED", false); cfg.AIEnabled {
		if cfg.AI, err = loadAI(); err != nil {
			return nil, fmt.Errorf("invalid AI config: %w", err)
		}
	}

	return cfg, nil
}

func envInt(key string, fallback int) int {
	v := 0
	fmt.Sscanf(os.Getenv(key), "%d", &v)
	if v == 0 {
		return fallback
	}
	return v
}

func envString(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	v, err := strconv.ParseBool(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return v
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v, err := time.ParseDuration(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return v
}

// envList reads a comma-separated list, ignoring blank entries.
func envList(key string) []string {
	return splitList(os.Getenv(key))
}

// loadAI reads the AI layer's configuration. API keys are read from
// <PROVIDER>_API_KEY for every provider in use, so a new provider needs no
// code here. Malformed values fail instead of falling back.
func loadAI() (ai.Config, error) {
	var errs []error
	role := func(prefix string, timeout time.Duration, maxTokens int) ai.RoleConfig {
		temp, err := envOptionalFloat(prefix + "_TEMPERATURE")
		errs = append(errs, err)
		return ai.RoleConfig{
			Target: ai.Target{
				Provider: envString(prefix+"_PROVIDER", defaultAIProvider),
				Model:    envString(prefix+"_MODEL", defaultAIModel),
			},
			Timeout:     envDuration(prefix+"_TIMEOUT", timeout),
			Temperature: temp,
			MaxTokens:   envInt(prefix+"_MAX_TOKENS", maxTokens),
		}
	}
	cfg := ai.Config{
		Executor: role("AI_EXECUTOR", defaultAIExecutorTimeout, defaultAIExecutorMaxTokens),
		Judge:    role("AI_JUDGE", defaultAIJudgeTimeout, defaultAIJudgeMaxTokens),
		Retry: ai.RetryConfig{
			MaxAttempts: envInt("AI_RETRY_MAX_ATTEMPTS", defaultAIRetryAttempts),
			BaseDelay:   envDuration("AI_RETRY_BASE_DELAY", defaultAIRetryBaseDelay),
			MaxDelay:    envDuration("AI_RETRY_MAX_DELAY", defaultAIRetryMaxDelay),
		},
		JudgeParseRetries: envInt("AI_JUDGE_PARSE_RETRIES", defaultAIJudgeParseRetries),
		LogContent:        envBool("AI_LOG_CONTENT", false),
		Providers:         map[string]ai.ProviderSettings{},
	}
	if v := os.Getenv("AI_JUDGE_PARSE_RETRIES"); v == "0" {
		cfg.JudgeParseRetries = 0 // envInt treats 0 as unset
	}

	var err error
	cfg.ExecutorModels, err = parseTargets(envString("AI_EXECUTOR_MODELS", defaultAIExecutorModels))
	errs = append(errs, err)
	for _, model := range domain.AIModels() {
		if _, ok := cfg.ExecutorModels[string(model)]; !ok {
			errs = append(errs, fmt.Errorf("AI_EXECUTOR_MODELS has no entry for game model %q", model))
		}
	}
	cfg.RateLimits, err = parseRateLimits(os.Getenv("AI_RATE_LIMITS"))
	errs = append(errs, err)
	cfg.Pricing, err = parsePricing(os.Getenv("AI_PRICING"))
	errs = append(errs, err)

	for _, name := range cfg.ProviderNames() {
		upper := strings.ToUpper(name)
		cfg.Providers[name] = ai.ProviderSettings{
			APIKey:  ai.NewAPIKey(os.Getenv(upper + "_API_KEY")),
			BaseURL: os.Getenv(upper + "_BASE_URL"),
		}
	}
	errs = append(errs, cfg.Validate())
	return cfg, errors.Join(errs...)
}

// parseTargets reads "game_model=provider/model,..." entries.
func parseTargets(v string) (map[string]ai.Target, error) {
	out := map[string]ai.Target{}
	for _, entry := range splitList(v) {
		name, target, ok := strings.Cut(entry, "=")
		provider, model, ok2 := strings.Cut(target, "/")
		if !ok || !ok2 || name == "" || provider == "" || model == "" {
			return nil, fmt.Errorf("AI_EXECUTOR_MODELS entry %q must be game_model=provider/model", entry)
		}
		out[strings.TrimSpace(name)] = ai.Target{Provider: strings.TrimSpace(provider), Model: strings.TrimSpace(model)}
	}
	return out, nil
}

// parseRateLimits reads "provider=rps:burst,..." entries.
func parseRateLimits(v string) (map[string]ai.RateLimit, error) {
	out := map[string]ai.RateLimit{}
	for _, entry := range splitList(v) {
		name, spec, _ := strings.Cut(entry, "=")
		rps, burst, err := parsePair(spec)
		if err != nil || name == "" {
			return nil, fmt.Errorf("AI_RATE_LIMITS entry %q must be provider=rps:burst", entry)
		}
		out[strings.TrimSpace(name)] = ai.RateLimit{RPS: rps, Burst: int(burst)}
	}
	return out, nil
}

// parsePricing reads "model=input:output,..." entries, in USD per million
// tokens.
func parsePricing(v string) (ai.Pricing, error) {
	out := ai.Pricing{}
	for _, entry := range splitList(v) {
		model, spec, _ := strings.Cut(entry, "=")
		in, outPrice, err := parsePair(spec)
		if err != nil || model == "" {
			return nil, fmt.Errorf("AI_PRICING entry %q must be model=input_per_mtok:output_per_mtok", entry)
		}
		out[strings.TrimSpace(model)] = ai.Price{InputPerMTok: in, OutputPerMTok: outPrice}
	}
	return out, nil
}

func parsePair(v string) (float64, float64, error) {
	a, b, ok := strings.Cut(v, ":")
	if !ok {
		return 0, 0, errors.New("missing ':'")
	}
	x, err := strconv.ParseFloat(strings.TrimSpace(a), 64)
	if err != nil {
		return 0, 0, err
	}
	y, err := strconv.ParseFloat(strings.TrimSpace(b), 64)
	return x, y, err
}

// envOptionalFloat reads a float that is nil when unset.
func envOptionalFloat(key string) (*float64, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return nil, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return nil, fmt.Errorf("%s must be a number", key)
	}
	return &f, nil
}

func splitList(v string) []string {
	var out []string
	for _, item := range strings.Split(v, ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// loadJudging reads the judging settings. SCORE_WEIGHTS is
// "accuracy=1,simplicity=1,creativity=1,adherence=1,efficiency=1"; omitted
// components keep the default weight of 1.
func loadJudging() (JudgingConfig, error) {
	cfg := JudgingConfig{
		MaxRuns:    envInt("JUDGE_MAX_RUNS", defaultJudgeMaxRuns),
		RetryDelay: envDuration("JUDGE_RETRY_DELAY", defaultJudgeRetryDelay),
		StaleAfter: envDuration("JUDGE_STALE_AFTER", defaultJudgeStaleAfter),
		Weights:    domain.DefaultScoreWeights,
	}
	for _, entry := range splitList(os.Getenv("SCORE_WEIGHTS")) {
		name, value, _ := strings.Cut(entry, "=")
		w, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return cfg, fmt.Errorf("SCORE_WEIGHTS entry %q must be component=weight", entry)
		}
		switch strings.TrimSpace(name) {
		case "accuracy":
			cfg.Weights.Accuracy = w
		case "simplicity":
			cfg.Weights.Simplicity = w
		case "creativity":
			cfg.Weights.Creativity = w
		case "adherence":
			cfg.Weights.Adherence = w
		case "efficiency":
			cfg.Weights.Efficiency = w
		default:
			return cfg, fmt.Errorf("SCORE_WEIGHTS has an unknown component %q", name)
		}
	}
	switch {
	case !cfg.Weights.Valid():
		return cfg, errors.New("SCORE_WEIGHTS must not be negative and must not all be zero")
	case cfg.MaxRuns < 1 || cfg.RetryDelay <= 0 || cfg.StaleAfter <= 0:
		return cfg, errors.New("JUDGE_MAX_RUNS, JUDGE_RETRY_DELAY and JUDGE_STALE_AFTER must be positive")
	}
	return cfg, nil
}
