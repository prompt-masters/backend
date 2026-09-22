// Package redisclient builds the Redis client used for live game state.
package redisclient

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"regexp"
	"time"

	"crypto/x509"

	"github.com/redis/go-redis/v9"
)

var keyPrefixPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

type Config struct {
	Addr     string
	Username string
	Password string
	DB       int
	// TLS enables TLS with a minimum version of 1.2, verifying the server
	// against the system root CAs.
	TLS bool
	// TLSCAFile is an optional PEM bundle to verify the server against
	// instead of the system roots, for a private CA.
	TLSCAFile string
	// TLSServerName overrides the name checked against the server
	// certificate; it defaults to the host in Addr.
	TLSServerName string
	PoolSize      int
	DialTimeout   time.Duration
	ReadTimeout   time.Duration
	WriteTimeout  time.Duration
	// OpTimeout bounds every live-state operation, including its round trips.
	OpTimeout time.Duration
	// KeyPrefix namespaces every key, e.g. "promptgame".
	KeyPrefix string
	// ActiveGameTTL is the sliding expiry for waiting and in-progress games,
	// refreshed on every write.
	ActiveGameTTL time.Duration
	// EndedGameTTL is the expiry applied once a game is finished or cancelled.
	EndedGameTTL time.Duration
}

func (c Config) Validate() error {
	var errs []error
	if c.Addr == "" {
		errs = append(errs, errors.New("REDIS_ADDR is required"))
	}
	if c.DB < 0 {
		errs = append(errs, errors.New("REDIS_DB must not be negative"))
	}
	if c.PoolSize <= 0 {
		errs = append(errs, errors.New("REDIS_POOL_SIZE must be positive"))
	}
	for name, d := range map[string]time.Duration{
		"REDIS_DIAL_TIMEOUT":    c.DialTimeout,
		"REDIS_READ_TIMEOUT":    c.ReadTimeout,
		"REDIS_WRITE_TIMEOUT":   c.WriteTimeout,
		"REDIS_OP_TIMEOUT":      c.OpTimeout,
		"GAME_STATE_ACTIVE_TTL": c.ActiveGameTTL,
		"GAME_STATE_ENDED_TTL":  c.EndedGameTTL,
	} {
		if d <= 0 {
			errs = append(errs, fmt.Errorf("%s must be positive", name))
		}
	}
	if !keyPrefixPattern.MatchString(c.KeyPrefix) {
		errs = append(errs, errors.New("REDIS_KEY_PREFIX must be lowercase letters, digits, '-' or '_'"))
	}
	return errors.Join(errs...)
}

// New creates a client. It does not connect; connections are opened lazily
// and re-established automatically after Redis becomes reachable again. It
// fails only when TLS is on and the CA file cannot be loaded.
func New(cfg Config) (*redis.Client, error) {
	opts := &redis.Options{
		Addr:         cfg.Addr,
		Username:     cfg.Username,
		Password:     cfg.Password,
		DB:           cfg.DB,
		PoolSize:     cfg.PoolSize,
		DialTimeout:  cfg.DialTimeout,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		// Honour context deadlines so OpTimeout bounds each call.
		ContextTimeoutEnabled: true,
	}
	if cfg.TLS {
		tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: cfg.TLSServerName}
		if cfg.TLSCAFile != "" {
			pool := x509.NewCertPool()
			pem, err := os.ReadFile(cfg.TLSCAFile)
			if err != nil {
				return nil, fmt.Errorf("reading REDIS_TLS_CA_FILE: %w", err)
			}
			if !pool.AppendCertsFromPEM(pem) {
				return nil, fmt.Errorf("REDIS_TLS_CA_FILE %s contains no certificate", cfg.TLSCAFile)
			}
			tlsCfg.RootCAs = pool
		}
		opts.TLSConfig = tlsCfg
	}
	return redis.NewClient(opts), nil
}

// Ping checks connectivity within timeout.
func Ping(ctx context.Context, client redis.UniversalClient, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return client.Ping(ctx).Err()
}
