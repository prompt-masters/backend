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
	AIEnabled bool
	AI        ai.Config
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
