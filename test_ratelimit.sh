#!/usr/bin/env bash
# Rate-limiter validation: fire 60 rapid requests, expect some 429s.
# Usage: bash test_ratelimit.sh [base_url]
set -euo pipefail

BASE_URL="${1:-http://localhost:8080}"
TOTAL=60
OK=0
LIMITED=0
OTHER=0

echo "==> Sending $TOTAL rapid requests to $BASE_URL/health"
for i in $(seq 1 $TOTAL); do
  STATUS=$(curl -s -o /dev/null -w "%{http_code}" "$BASE_URL/health")
  case "$STATUS" in
    200) OK=$((OK + 1)) ;;
    429) LIMITED=$((LIMITED + 1)) ;;
    *)   OTHER=$((OTHER + 1)) ;;
  esac
done

echo ""
echo "==> Results:"
echo "    200 OK:              $OK"
echo "    429 Too Many Req:    $LIMITED"
echo "    Other:               $OTHER"
echo ""

if [ "$LIMITED" -gt 0 ]; then
  echo "✅ Rate limiter is ACTIVE — $LIMITED requests were throttled."
else
  echo "⚠️  No requests were throttled. Limiter may not be working or requests were too slow."
fi
