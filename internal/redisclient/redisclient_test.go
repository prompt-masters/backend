package redisclient

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
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
	plain, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer plain.Close()
	if plain.Options().TLSConfig != nil {
		t.Error("TLS configured although REDIS_TLS is off")
	}

	cfg.TLS = true
	secure, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer secure.Close()
	tlsCfg := secure.Options().TLSConfig
	if tlsCfg == nil || tlsCfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("TLS config = %+v, want TLS 1.2 or later", tlsCfg)
	}
	if tlsCfg.InsecureSkipVerify {
		t.Error("certificate verification is disabled")
	}
	if tlsCfg.RootCAs != nil {
		t.Error("RootCAs set without REDIS_TLS_CA_FILE; want the system roots")
	}
}

func TestNewWithCustomCA(t *testing.T) {
	caPEM, _ := selfSignedCA(t)
	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := validConfig()
	cfg.TLS = true
	cfg.TLSCAFile = caFile
	cfg.TLSServerName = "redis.internal"

	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer client.Close()
	tlsCfg := client.Options().TLSConfig
	if tlsCfg.RootCAs == nil {
		t.Error("RootCAs not loaded from REDIS_TLS_CA_FILE")
	}
	if tlsCfg.ServerName != "redis.internal" {
		t.Errorf("ServerName = %q, want the configured override", tlsCfg.ServerName)
	}

	badFile := filepath.Join(dir, "bad.pem")
	if err := os.WriteFile(badFile, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{"missing file": filepath.Join(dir, "nope.pem"), "not a certificate": badFile} {
		cfg.TLSCAFile = path
		if _, err := New(cfg); err == nil {
			t.Errorf("New() with a %s error = nil, want a failure", name)
		}
	}
}

// selfSignedCA returns a PEM certificate and its key.
func selfSignedCA(t *testing.T) ([]byte, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), key
}

func TestPingUnreachableRedisReturnsErrorWithinTimeout(t *testing.T) {
	cfg := validConfig()
	cfg.Addr = "127.0.0.1:1" // nothing listens here
	client, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	start := time.Now()
	err = Ping(context.Background(), client, 500*time.Millisecond)
	if err == nil {
		t.Fatal("Ping() = nil, want an error for an unreachable server")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("Ping() took %s, want it bounded by the timeout", elapsed)
	}
}
