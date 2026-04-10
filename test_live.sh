#!/usr/bin/env bash
# Integration smoke test — requires running container with valid AWS credentials.
set -euo pipefail

BASE_URL="${1:-http://localhost:8080}"

echo "==> Health check"
curl -sf "${BASE_URL}/health" | python3 -m json.tool
echo ""

echo "==> POST /api/v1/adaptive-plan"
curl -sf -X POST "${BASE_URL}/api/v1/adaptive-plan" \
  -H "Content-Type: application/json" \
  -d '{
    "student_id": "student-001",
    "course_id": "CS-201",
    "failed_topics": ["Polymorphism", "Recursion"],
    "vark_profile": "visual",
    "telemetry": {
      "session_id": "sess-abc-123",
      "timestamp_unix": 1712700000,
      "client_version": "1.0.0"
    }
  }' | python3 -m json.tool

echo ""
echo "==> Done"
