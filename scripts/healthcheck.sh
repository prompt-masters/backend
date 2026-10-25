#!/usr/bin/env bash
# Usage: healthcheck.sh URL [ATTEMPTS] [FIRST_DELAY_SECONDS]
# Polls URL (the /health endpoint) until it answers 200, waiting
# FIRST_DELAY_SECONDS and doubling the wait after each failed attempt.
# /health answers 503 while PostgreSQL is unreachable.
set -euo pipefail

url=${1:?usage: healthcheck.sh URL [ATTEMPTS] [FIRST_DELAY_SECONDS]}
attempts=${2:-6}
delay=${3:-2}
body=$(mktemp)
trap 'rm -f "$body"' EXIT

for ((i = 1; i <= attempts; i++)); do
	code=$(curl -sS -o "$body" -w '%{http_code}' --max-time 5 "$url") || true
	if [ "$code" = 200 ]; then
		echo "healthy after $i attempt(s): $(cat "$body")"
		exit 0
	fi
	echo "attempt $i/$attempts: HTTP $code $(cat "$body" 2>/dev/null || true)"
	if ((i < attempts)); then
		sleep "$delay"
		delay=$((delay * 2))
	fi
done
echo "unhealthy: $url did not answer 200 after $attempts attempts" >&2
exit 1
