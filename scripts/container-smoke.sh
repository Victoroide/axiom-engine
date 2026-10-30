#!/usr/bin/env bash
set -euo pipefail

image=${1:-axiom-engine:local}
smoke_dir=$(mktemp -d)
prefix="axiom-smoke-$$"
token=ci-internal-token-0123456789abcdef
backend_pid=
name=

fail() {
  printf 'container smoke: %s\n' "$*" >&2
  exit 1
}

stop_backend() {
  if [[ -n "$backend_pid" ]]; then
    kill "$backend_pid" 2>/dev/null || true
    wait "$backend_pid" 2>/dev/null || true
    backend_pid=
  fi
}

cleanup() {
  result=$?
  if (( result != 0 )) && [[ -n "$name" ]]; then
    docker logs "$name" >&2 || true
    cat "$smoke_dir/backend-errors" >&2 || true
  fi
  docker rm -f "$prefix-ok" "$prefix-throttle" >/dev/null 2>&1 || true
  stop_backend
  rm -rf "$smoke_dir"
  exit "$result"
}
trap cleanup EXIT

go build -o "$smoke_dir/fakebedrock" ./test/fakebedrock/cmd/fakebedrock
cat > "$smoke_dir/request.json" <<'JSON'
{"student_id":"student-42","course_id":"cs101","failed_topics":["Recursion"],"vark_profile":"visual"}
JSON

start_backend() {
  rm -f "$smoke_dir/backend-address"
  "$smoke_dir/fakebedrock" -addr 0.0.0.0:0 -mode "$1" \
    > "$smoke_dir/backend-address" 2> "$smoke_dir/backend-errors" &
  backend_pid=$!
  for _ in {1..50}; do
    if [[ -s "$smoke_dir/backend-address" ]]; then break; fi
    kill -0 "$backend_pid"
    sleep 0.1
  done
  backend_port=$(head -n 1 "$smoke_dir/backend-address")
  backend_port=${backend_port##*:}
  [[ "$backend_port" =~ ^[0-9]+$ ]] || fail "invalid backend port: $backend_port"
}

start_image() {
  name="$prefix-$1"
  docker run -d --name "$name" --read-only --tmpfs /tmp \
    --add-host host.docker.internal:host-gateway -p 127.0.0.1::8080 \
    --health-interval=1s --health-start-period=1s \
    -e AWS_REGION=us-east-1 -e AWS_ACCESS_KEY_ID=dummy -e AWS_SECRET_ACCESS_KEY=dummy \
    -e AWS_EC2_METADATA_DISABLED=true -e AWS_MAX_ATTEMPTS=1 -e AWS_RETRY_MODE=standard \
    -e "AWS_ENDPOINT_URL=http://host.docker.internal:$backend_port" \
    -e "AXIOM_INTERNAL_TOKEN=$token" "$image" >/dev/null
  for _ in {1..30}; do
    health=$(docker inspect --format '{{.State.Health.Status}}' "$name")
    if [[ "$health" == healthy ]]; then break; fi
    if [[ "$health" == unhealthy ]]; then fail "health is $health"; fi
    sleep 1
  done
  [[ "$health" == healthy ]] || fail "health is $health"
  user=$(docker inspect --format '{{.Config.User}}' "$name")
  [[ "$user" == 65532:65532 ]] || fail "configured user is $user"
  docker top "$name" -o pid,uid |
    awk 'NR > 1 { n++; if ($2 != 65532) bad = 1 } END { exit (bad || n == 0) }' ||
    fail 'a container process is not running as UID 65532'
  readonly=$(docker inspect --format '{{.HostConfig.ReadonlyRootfs}}' "$name")
  [[ "$readonly" == true ]] || fail "read-only root filesystem is $readonly"
  tmpfs=$(docker inspect --format '{{range $path, $_ := .HostConfig.Tmpfs}}{{$path}}{{"\n"}}{{end}}' "$name")
  [[ "$tmpfs" == /tmp ]] || fail '/tmp is not a tmpfs mount'
  base="http://$(docker port "$name" 8080/tcp)"
  curl --fail --silent --show-error --max-time 5 "$base/health" >/dev/null
  status=$(curl --silent --show-error --max-time 5 --output /dev/null \
    --write-out '%{http_code}' --request POST "$base/api/v1/adaptive-plan")
  [[ "$status" == 401 ]] || fail "unauthenticated status is $status"
}

request_plan() {
  status=$(curl --silent --show-error --max-time 20 \
    --output "$smoke_dir/body.json" --dump-header "$smoke_dir/headers" \
    --write-out '%{http_code}' --header 'Content-Type: application/json' \
    --header "X-Internal-Token: $token" --data-binary "@$smoke_dir/request.json" \
    "$base/api/v1/adaptive-plan")
  if [[ "$status" != "$1" ]]; then
    printf 'Expected plan status %s, got %s: ' "$1" "$status" >&2
    cat "$smoke_dir/body.json" >&2
    exit 1
  fi
}

stop_image() {
  docker stop --time 30 "$name" >/dev/null
  exit_code=$(docker inspect --format '{{.State.ExitCode}}' "$name")
  [[ "$exit_code" == 0 ]] || fail "stop exit code is $exit_code"
  docker logs "$name" > "$smoke_dir/logs" 2>&1
  python3 - "$smoke_dir/logs" <<'PY'
import json
import sys
text = open(sys.argv[1]).read()
assert text.strip(), 'No application logs'
assert 'student-42' not in text, 'Learner identifier in logs'
assert '[BAML' not in text, 'Native log output is enabled'
for line in text.splitlines():
    assert isinstance(json.loads(line), dict), line
PY
  stop_backend
}

start_backend ok
start_image ok
request_plan 200
python3 - "$smoke_dir/body.json" <<'PY'
import json
import sys
body = json.load(open(sys.argv[1]))
assert body['student_id'] == 'student-42', body
assert body['items'] and body['items'][0]['topic'] == 'Recursion', body
PY
stop_image
printf 'Success response, private JSON logs, and clean stop verified\n'

start_backend throttle
start_image throttle
request_plan 503
python3 - "$smoke_dir/headers" "$smoke_dir/body.json" <<'PY'
import json
import sys
headers = open(sys.argv[1]).read().splitlines()
retry = [line.split(':', 1)[1].strip() for line in headers
         if line.lower().startswith('retry-after:')]
assert retry == ['5'], retry
assert json.load(open(sys.argv[2]))['error'] == 'service_unavailable'
PY
stop_image
printf 'Throttling returns 503 with Retry-After: 5; private JSON logs and clean stop verified\n'
