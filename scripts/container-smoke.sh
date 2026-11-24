#!/usr/bin/env bash
set -euo pipefail

image=${1:-axiom-engine:local}
include_diagnostic=${2:-0}
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
  docker rm -f "$prefix-ok" "$prefix-throttle" "$prefix-example" "$prefix-mcp" "$prefix-diagnostic" >/dev/null 2>&1 || true
  stop_backend
  rm -rf "$smoke_dir"
  exit "$result"
}
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
    -e "GRAPH_FILE=${2:-}" \
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

check_diagnostic() {
  local expected_exit=$1 expected_status=$2 expected_reason=$3 result=0
  local container="$prefix-diagnostic" healthcheck platform baml_version
  shift 3
  docker create --name "$container" --network none --read-only --no-healthcheck \
    --entrypoint /app/axiom-baml-check -e AWS_SECRET_ACCESS_KEY=private-container-canary "$@" "$image" >/dev/null
  healthcheck=$(docker inspect --format '{{json .Config.Healthcheck.Test}}' "$container")
  [[ "$healthcheck" == '["NONE"]' ]] || fail 'diagnostic healthcheck is enabled'
  docker start --attach "$container" \
    > "$smoke_dir/diagnostic.stdout" 2> "$smoke_dir/diagnostic.stderr" || result=$?
  docker rm "$container" >/dev/null
  [[ "$result" == "$expected_exit" ]] || fail "diagnostic exited $result: $(head -c 160 "$smoke_dir/diagnostic.stderr")"
  [[ ! -s "$smoke_dir/diagnostic.stdout" ]] || fail 'diagnostic wrote to stdout'
  platform=$(docker image inspect "$image" --format '{{.Os}}/{{.Architecture}}')
  baml_version=$(sed -n 's/^ARG BAML_LIB_VERSION=//p' Dockerfile)
  python3 -I - "$smoke_dir/diagnostic.stderr" "$expected_status" "$expected_reason" "$platform" "$baml_version" <<'PY'
import json
import sys

path, status, reason, platform, version = sys.argv[1:]
with open(path, 'rb') as stream:
    output = stream.read(161)
if len(output) > 160 or not output.endswith(b'\n') or output.count(b'\n') != 1:
    sys.exit('Invalid diagnostic stderr framing')
try:
    record = json.loads(output)
except ValueError:
    sys.exit('Invalid diagnostic JSON')
if record != dict(status=status, reason=reason, platform=platform, baml_version=version):
    sys.exit('Unexpected diagnostic record')
PY
}

check_image_contract() {
  docker image inspect "$image" --format '{{json .Config}}' | python3 -I -c '
import json, sys
config = json.load(sys.stdin)
assert config["Entrypoint"] == ["/app/axiom-server"], config["Entrypoint"]
assert config.get("Cmd") is None, config["Cmd"]
assert config["WorkingDir"] == "/app", config["WorkingDir"]
assert config["Healthcheck"]["Test"] == ["CMD", "/app/axiom-server", "healthcheck"], config["Healthcheck"]
'
  local pid executable argument_count
  read -r pid executable argument_count < <(docker inspect --format '{{.State.Pid}} {{.Path}} {{len .Args}}' "$name")
  [[ "$executable" == /app/axiom-server && "$argument_count" == 0 ]] || fail 'unexpected container launch arguments'
  # The kernel interpreter can appear in the arguments of an emulated executable.
  docker top "$name" -o pid,comm | awk -v pid="$pid" '$1 == pid && $2 == "axiom-server" && NF == 2 { found = 1 } END { exit !found }' ||
    fail 'the server is not the container main process'
  docker cp "$name:/app/." - | python3 -I -c '
import sys, tarfile
with tarfile.open(fileobj=sys.stdin.buffer, mode="r|*") as archive:
    members = {entry.name.removeprefix("./"): (entry.type, entry.mode) for entry in archive}
expected = {
    ".": (tarfile.DIRTYPE, 0o755),
    "axiom-server": (tarfile.REGTYPE, 0o755),
    "libbaml_cffi.so": (tarfile.REGTYPE, 0o644),
}
if sys.argv[1] == "1":
    expected["axiom-baml-check"] = (tarfile.REGTYPE, 0o755)
assert members == expected, members
' "$include_diagnostic"
}

check_stdio() {
  python3 -I - "$image" "$prefix-mcp" <<'PY'
import json
import select
import subprocess
import sys
import time

image, name = sys.argv[1:]
command = ['docker', 'run', '--rm', '--name', name, '-i', '--network', 'none', '--read-only',
           '-e', 'AXIOM_AUTH_DISABLED=true', image, 'mcp']
with subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, bufsize=0) as process:
    try:
        request = dict(jsonrpc='2.0', id=1, method='initialize', params=dict(
            protocolVersion='2025-11-25', capabilities={}, clientInfo=dict(name='smoke', version='1')))
        process.stdin.write(json.dumps(request).encode() + b'\n')
        deadline = time.monotonic() + 20
        response = bytearray()
        while not response.endswith(b'\n'):
            remaining = deadline - time.monotonic()
            if remaining <= 0 or not select.select([process.stdout], [], [], remaining)[0]:
                sys.exit('No complete MCP response on stdout')
            chunk = process.stdout.read(4096)
            if not chunk:
                sys.exit('MCP stdout closed before the response')
            if len(response) + len(chunk) > 16384:
                sys.exit('MCP response exceeded 16384 bytes')
            response.extend(chunk)
        assert response.count(b'\n') == 1, response
        message = json.loads(response)
        assert message['jsonrpc'] == '2.0' and message['id'] == 1, message
        assert message['result']['serverInfo']['name'] == 'axiom-engine', message
        assert message['result']['protocolVersion'] == '2025-11-25', message
        stdout, stderr = process.communicate(timeout=15)
        assert process.returncode == 0 and not stdout, (process.returncode, stdout, stderr)
    finally:
        if process.poll() is None:
            process.kill()
PY
}

stop_image() {
  docker stop --time 30 "$name" >/dev/null
  exit_code=$(docker inspect --format '{{.State.ExitCode}}' "$name")
  [[ "$exit_code" == 0 ]] || fail "stop exit code is $exit_code"
  docker logs "$name" > "$smoke_dir/logs" 2>&1
  python3 -I - "$smoke_dir/logs" <<'PY'
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

[[ "${BASH_SOURCE[0]}" == "$0" ]] || return 0

smoke_dir=$(mktemp -d)
trap cleanup EXIT
go build -o "$smoke_dir/fakebedrock" ./test/fakebedrock/cmd/fakebedrock
cat > "$smoke_dir/request.json" <<'JSON'
{"student_id":"student-42","course_id":"cs101","failed_topics":["Recursion"],"vark_profile":"visual"}
JSON

if [[ "$include_diagnostic" == 1 ]]; then
  check_diagnostic 0 ok verified
  check_diagnostic 0 ok verified -e BAML_LIBRARY_PATH=./libbaml_cffi.so
  check_diagnostic 2 error path_requires_slash -e BAML_LIBRARY_PATH=libbaml_cffi.so
  check_diagnostic 1 error missing_file -e BAML_LIBRARY_PATH=/app/private-container-canary
  printf 'Offline diagnostic verified explicit paths and rejected a bare name and a missing file\n'
fi

start_backend ok
start_image ok
check_image_contract
request_plan 200
python3 -I - "$smoke_dir/body.json" <<'PY'
import json
import sys
body = json.load(open(sys.argv[1]))
assert body['student_id'] == 'student-42', body
assert body['items'] and body['items'][0]['topic'] == 'Recursion', body
PY
stop_image
printf 'Success response, private JSON logs, and clean stop verified\n'

check_stdio
printf 'Default entrypoint, application file modes and MCP stdout framing verified\n'

start_backend throttle
start_image throttle
request_plan 503
python3 -I - "$smoke_dir/headers" "$smoke_dir/body.json" <<'PY'
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

start_backend ok
start_image example /usr/share/axiom-engine/graphs/computer-science.json
curl --fail --silent --show-error --max-time 5 --header "X-Internal-Token: $token" \
  "$base/api/v1/topics" > "$smoke_dir/topics.json"
python3 -I - "$smoke_dir/topics.json" <<'PYTHON'
import json
import sys
topics = json.load(open(sys.argv[1]))['topics']
assert len(topics) == 130, len(topics)
assert 'SQL' in {topic['name'] for topic in topics}, topics
assert 'TCP' in {topic['name'] for topic in topics}, topics
PYTHON
request_plan 200
stop_image
printf 'Bundled computer science curriculum starts and serves topics and plans\n'
