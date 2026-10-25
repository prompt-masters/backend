#!/usr/bin/env bash
# Runs the whole test suite against real PostgreSQL and Redis, and fails if
# any test needing them skipped, so a missing service cannot pass silently.
# TEST_DATABASE_URL and TEST_REDIS_ADDR must be set. (The Redis Cluster test
# still skips unless TEST_REDIS_CLUSTER_ADDRS is set.)
set -euo pipefail

: "${TEST_DATABASE_URL:?TEST_DATABASE_URL must point at a PostgreSQL database}"
: "${TEST_REDIS_ADDR:?TEST_REDIS_ADDR must point at a Redis server}"

log=$(mktemp)
trap 'rm -f "$log"' EXIT

if ! go test -race -count=1 -v ./... >"$log" 2>&1; then
	cat "$log"
	exit 1
fi
grep -E '^(ok|FAIL)' "$log"
if grep -E '(TEST_DATABASE_URL|TEST_REDIS_ADDR) is not set; skipping' "$log"; then
	echo "integration tests skipped: a service is not configured" >&2
	exit 1
fi
echo "integration: $(grep -c -- '--- PASS' "$log") tests passed, none skipped"
