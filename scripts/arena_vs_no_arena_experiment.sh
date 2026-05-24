#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
USERNAME="${USERNAME:-student}"
PASSWORD="${PASSWORD:-student}"
REQUESTS="${REQUESTS:-1000}"
CONCURRENCY="${CONCURRENCY:-10}"
WIDTH="${WIDTH:-640}"
HEIGHT="${HEIGHT:-480}"

echo "Registering or logging in test user: ${USERNAME}"
curl -fsS -X POST "${BASE_URL}/api/register" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"${USERNAME}\",\"password\":\"${PASSWORD}\"}" >/dev/null || true

TOKEN="$(curl -fsS -X POST "${BASE_URL}/api/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"${USERNAME}\",\"password\":\"${PASSWORD}\"}" \
  | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')"

if [[ -z "${TOKEN}" ]]; then
  echo "Failed to obtain auth token" >&2
  exit 1
fi

echo "Running Arena vs No Arena benchmark"
cd "$(dirname "$0")/../tests/benchmarks"
go run benchmark.go \
  -url="${BASE_URL}/api/process" \
  -token="${TOKEN}" \
  -requests="${REQUESTS}" \
  -concurrency="${CONCURRENCY}" \
  -width="${WIDTH}" \
  -height="${HEIGHT}"
