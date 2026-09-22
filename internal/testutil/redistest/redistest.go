// Package redistest provides an isolated Redis namespace for tests.
package redistest

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Client connects to TEST_REDIS_ADDR (with TEST_REDIS_PASSWORD and
// TEST_REDIS_DB, default 15) and returns the client with a key prefix unique
// to this test. Every key under the prefix is deleted when the test ends. The
// test is skipped when TEST_REDIS_ADDR is unset.
func Client(t testing.TB) (*redis.Client, string) {
	t.Helper()

	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TEST_REDIS_ADDR is not set; skipping Redis test")
	}
	dbIndex := 15
	if v := os.Getenv("TEST_REDIS_DB"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("TEST_REDIS_DB: %v", err)
		}
		dbIndex = n
	}

	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: os.Getenv("TEST_REDIS_PASSWORD"),
		DB:       dbIndex,
	})
	if err := client.Ping(context.Background()).Err(); err != nil {
		client.Close()
		t.Fatalf("connecting to test Redis at %s: %v", addr, err)
	}

	prefix := "test-" + uuid.NewString()[:8]
	t.Cleanup(func() {
		Flush(t, client, prefix)
		client.Close()
	})
	return client, prefix
}

// Keys lists every key under prefix.
func Keys(t testing.TB, client *redis.Client, prefix string) []string {
	t.Helper()
	var keys []string
	iter := client.Scan(context.Background(), 0, prefix+":*", 500).Iterator()
	for iter.Next(context.Background()) {
		keys = append(keys, iter.Val())
	}
	if err := iter.Err(); err != nil {
		t.Fatalf("scanning keys: %v", err)
	}
	return keys
}

// Flush deletes every key under prefix.
func Flush(t testing.TB, client *redis.Client, prefix string) {
	t.Helper()
	if keys := Keys(t, client, prefix); len(keys) > 0 {
		if err := client.Del(context.Background(), keys...).Err(); err != nil {
			t.Errorf("deleting test keys: %v", err)
		}
	}
}
