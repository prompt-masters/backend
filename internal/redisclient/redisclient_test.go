package redisclient

import (
	"context"
	"strings"
	"testing"
	"time"
)

func validConfig() Config {
	return Config{
		Addr:          "localhost:6379",
		PoolSize:      10,
		DialTimeout:   time.Second,
		ReadTimeout:   time.Second,
		WriteTimeout:  time.Second,
		OpTimeout:     time.Second,
		KeyPrefix:     "promptgame",
		ActiveGameTTL: 2 * time.Hour,
		EndedGameTTL:  15 * time.Minute,
	}
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{name: "valid", mutate: func(*Config) {}},
		{name: "missing address", mutate: func(c *Config) { c.Addr = "" }, wantErr: "REDIS_ADDR"},
		{name: "negative db", mutate: func(c *Config) { c.DB = -1 }, wantErr: "REDIS_DB"},
		{name: "zero pool", mutate: func(c *Config) { c.PoolSize = 0 }, wantErr: "REDIS_POOL_SIZE"},
		{name: "zero op timeout", mutate: func(c *Config) { c.OpTimeout = 0 }, wantErr: "REDIS_OP_TIMEOUT"},
		{name: "zero active ttl", mutate: func(c *Config) { c.ActiveGameTTL = 0 }, wantErr: "GAME_STATE_ACTIVE_TTL"},
		{name: "prefix with a colon", mutate: func(c *Config) { c.KeyPrefix = "prompt:game" }, wantErr: "REDIS_KEY_PREFIX"},
		{name: "prefix with a brace", mutate: func(c *Config) { c.KeyPrefix = "{game}" }, wantErr: "REDIS_KEY_PREFIX"},
		{name: "empty prefix", mutate: func(c *Config) { c.KeyPrefix = "" }, wantErr: "REDIS_KEY_PREFIX"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.mutate(&cfg)
			err := cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() = %v, want error mentioning %s", err, tt.wantErr)
			}
		})
	}
}

func TestNewAppliesTLSOnlyWhenEnabled(t *testing.T) {
	cfg := validConfig()
	plain := New(cfg)
	defer plain.Close()
	if plain.Options().TLSConfig != nil {
		t.Error("TLS configured although REDIS_TLS is off")
	}

	cfg.TLS = true
	secure := New(cfg)
	defer secure.Close()
	if secure.Options().TLSConfig == nil {
		t.Error("TLS not configured although REDIS_TLS is on")
	}
}

func TestPingUnreachableRedisReturnsErrorWithinTimeout(t *testing.T) {
	cfg := validConfig()
	cfg.Addr = "127.0.0.1:1" // nothing listens here
	client := New(cfg)
	defer client.Close()

	start := time.Now()
	err := Ping(context.Background(), client, 500*time.Millisecond)
	if err == nil {
		t.Fatal("Ping() = nil, want an error for an unreachable server")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("Ping() took %s, want it bounded by the timeout", elapsed)
	}
}
