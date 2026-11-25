#!/usr/bin/env bash
set -euo pipefail

image=${1:-axiom-engine:local}
include_diagnostic=${2:-0}
include_inventory=${RELEASE_NATIVE_INVENTORY:-false}
prefix="axiom-smoke-$$"
token=ci-internal-token-0123456789abcdef
backend_pid=
fixture_pid=
name=
network="$prefix-net"

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
  if (( result != 0 )); then
    docker logs "$prefix-mcp" >&2 || true
    cat "$smoke_dir/fixture-errors" >&2 || true
  fi
  docker rm -f "$prefix-ok" "$prefix-throttle" "$prefix-example" "$prefix-mcp" "$prefix-diagnostic" >/dev/null 2>&1 || true
  docker network rm "$network" >/dev/null 2>&1 || true
  if [[ -n "$fixture_pid" ]]; then
    kill "$fixture_pid" 2>/dev/null || true
    wait "$fixture_pid" 2>/dev/null || true
  fi
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
if sys.argv[2] == "true":
    expected["baml-native.cdx.json"] = (tarfile.REGTYPE, 0o644)
assert members == expected, members
' "$include_diagnostic" "$include_inventory"
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

# The default image opens only the HTTP API. Remote MCP needs its own command.
exposed=$(docker image inspect --format '{{json .Config.ExposedPorts}}' "$image")
[[ "$exposed" == '{"8080/tcp":{}}' ]] || fail "exposed ports are $exposed"

# Remote MCP: a local HTTPS issuer, Redis and synthetic tokens on the host.
python3_free_port() {
  python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])'
}

create_network() {
  local subnet
  for _ in 1 2 3 4 5 6; do
    subnet="10.$((100 + RANDOM % 100)).$((RANDOM % 250)).0/24"
    if docker network create --subnet "$subnet" "$network" >/dev/null 2>&1; then
      mcp_ip="${subnet%.0/24}.10"
      return
    fi
  done
  fail 'could not create a container network'
}

fixture_value() {
  python3 - "$smoke_dir/fixture.json" "$@" <<'PY'
import json
import sys
value = json.load(open(sys.argv[1]))
for key in sys.argv[2:]:
    value = value[key]
print(value)
PY
}

mcp_curl() {
  curl --silent --show-error --max-time 30 --cacert "$fixture_dir/ca.pem" \
    --resolve "mcp.example.test:$host_port:127.0.0.1" "$@"
}

mcp_call() {
  local token=$1 method=$2 tool=$3 params=$4
  shift 4
  local meta='"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"smoke","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}'
  local body="{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"$method\",\"params\":{${params:+$params,}$meta}}"
  local -a args=(--request POST --header 'Content-Type: application/json'
    --header 'Accept: application/json, text/event-stream'
    --header 'Mcp-Protocol-Version: 2026-07-28' --header "Mcp-Method: $method")
  if [[ -n "$tool" ]]; then args+=(--header "Mcp-Name: $tool"); fi
  if [[ -n "$token" ]]; then args+=(--header "Authorization: Bearer $token"); fi
  mcp_status=$(mcp_curl "${args[@]}" "$@" --data-binary "$body" \
    --output "$smoke_dir/mcp-body.json" --dump-header "$smoke_dir/mcp-headers" \
    --write-out '%{http_code}' "$mcp_url")
}

expect_mcp() {
  [[ "$mcp_status" == "$1" ]] || {
    printf 'Expected remote MCP status %s, got %s: ' "$1" "$mcp_status" >&2
    cat "$smoke_dir/mcp-body.json" >&2
    exit 1
  }
}

host_port=$(python3_free_port)
mcp_url="https://mcp.example.test:$host_port/mcp"
fixture_dir="$smoke_dir/mcp-fixture"
mkdir -m 755 "$fixture_dir"
go build -o "$smoke_dir/mcpfixture" ./test/mcpfixture/cmd/mcpfixture
"$smoke_dir/mcpfixture" -dir "$fixture_dir" -mcp-audience "$mcp_url" \
  > "$smoke_dir/fixture.json" 2> "$smoke_dir/fixture-errors" &
fixture_pid=$!
for _ in {1..50}; do
  if [[ -s "$smoke_dir/fixture.json" ]]; then break; fi
  kill -0 "$fixture_pid"
  sleep 0.1
done
[[ -s "$smoke_dir/fixture.json" ]] || fail 'the issuer fixture did not start'
create_network
start_backend ok
name="$prefix-mcp"
docker run -d --name "$name" --network "$network" --ip "$mcp_ip" --no-healthcheck \
  --read-only --tmpfs /tmp --add-host host.docker.internal:host-gateway \
  -p "127.0.0.1:$host_port:8443" -v "$fixture_dir:/fixture:ro" -e SSL_CERT_FILE=/fixture/ca.pem \
  -e AWS_REGION=us-east-1 -e AWS_ACCESS_KEY_ID=dummy -e AWS_SECRET_ACCESS_KEY=dummy \
  -e AWS_EC2_METADATA_DISABLED=true -e AWS_MAX_ATTEMPTS=1 -e AWS_RETRY_MODE=standard \
  -e "AWS_ENDPOINT_URL=http://host.docker.internal:$backend_port" \
  -e AXIOM_AUTH_MODE=verified -e "AXIOM_AUTH_ISSUER=$(fixture_value issuer)" \
  -e AXIOM_AUTH_AUDIENCE=https://api.example.test/axiom -e "AXIOM_AUTH_JWKS_URL=$(fixture_value keys_url)" \
  -e AXIOM_AUTH_ALGORITHMS=ES256 -e AXIOM_TENANT_QUOTA_ENABLED=true -e AXIOM_TENANT_QUOTA_MAX=3 \
  -e "REDIS_URL=redis://host.docker.internal:$(fixture_value redis_port)" -e RATE_LIMIT_MAX=100 \
  -e "MCP_PUBLIC_URL=$mcp_url" -e "MCP_BIND_ADDR=$mcp_ip:8443" \
  -e MCP_TLS_CERT_FILE=/fixture/tls.crt -e MCP_TLS_KEY_FILE=/fixture/tls.key \
  "$image" mcp-http >/dev/null
for _ in {1..60}; do
  status=$(mcp_curl --output "$smoke_dir/metadata.json" --write-out '%{http_code}' \
    "https://mcp.example.test:$host_port/.well-known/oauth-protected-resource/mcp" 2>/dev/null || true)
  if [[ "$status" == 200 ]]; then break; fi
  [[ "$(docker inspect --format '{{.State.Running}}' "$name")" == true ]] || fail 'remote MCP container stopped'
  sleep 0.5
done
[[ "$status" == 200 ]] || fail "metadata status is $status"
python3 - "$smoke_dir/metadata.json" "$mcp_url" "$(fixture_value issuer)" <<'PY'
import json
import sys
metadata = json.load(open(sys.argv[1]))
assert metadata['resource'] == sys.argv[2], metadata
assert metadata['authorization_servers'] == [sys.argv[3]], metadata
assert metadata['scopes_supported'] == ['graph:read', 'plan:generate'], metadata
assert metadata['bearer_methods_supported'] == ['header'], metadata
PY
user=$(docker inspect --format '{{.Config.User}}' "$name")
[[ "$user" == 65532:65532 ]] || fail "remote MCP user is $user"

full=$(fixture_value tokens full)
graph_only=$(fixture_value tokens graph)
api_only=$(fixture_value tokens api)
mcp_call '' tools/list '' ''
expect_mcp 401
grep -qi "^www-authenticate: Bearer resource_metadata=\"https://mcp.example.test:$host_port/.well-known/oauth-protected-resource/mcp\"" "$smoke_dir/mcp-headers" ||
  fail 'the challenge does not name the resource metadata'
mcp_call "$api_only" tools/list '' ''
expect_mcp 401
mcp_call "$full" tools/list '' ''
expect_mcp 200
python3 - "$smoke_dir/mcp-body.json" <<'PY'
import json
import sys
tools = {tool['name'] for tool in json.load(open(sys.argv[1]))['result']['tools']}
assert tools == {'generate_study_plan', 'get_cognitive_graph', 'list_topics', 'get_topic_path'}, tools
PY
mcp_call "$full" tools/list '' '' --header 'Origin: https://evil.example'
expect_mcp 403
mcp_call "$graph_only" tools/call generate_study_plan \
  '"name":"generate_study_plan","arguments":{"failed_topics":["Recursion"],"vark_profile":"visual"}'
expect_mcp 403
grep -qi 'insufficient_scope' "$smoke_dir/mcp-headers" || fail 'the denial names no missing scope'
mcp_call "$full" tools/call generate_study_plan \
  '"name":"generate_study_plan","arguments":{"failed_topics":["Recursion"],"vark_profile":"visual"}'
expect_mcp 200
python3 - "$smoke_dir/mcp-body.json" <<'PY'
import json
import sys
result = json.load(open(sys.argv[1]))['result']
assert not result.get('isError'), result
assert result['structuredContent']['items'][0]['topic'] == 'Recursion', result
PY
mcp_call "$full" tools/call list_topics '"name":"list_topics","arguments":{}'
expect_mcp 200
mcp_call "$graph_only" tools/call get_topic_path '"name":"get_topic_path","arguments":{"topic":"Recursion"}'
expect_mcp 200
mcp_call "$full" tools/call list_topics '"name":"list_topics","arguments":{}'
expect_mcp 429
grep -qi '^retry-after: ' "$smoke_dir/mcp-headers" || fail 'the quota denial has no Retry-After'
mcp_call "$full" tools/list '' ''
expect_mcp 200

stop_image
docker logs "$name" > "$smoke_dir/mcp-logs" 2>&1
python3 - "$smoke_dir/mcp-logs" "$full" "$graph_only" "$api_only" <<'PY'
import json
import sys
text = open(sys.argv[1]).read()
assert text.strip(), 'No application logs'
for secret in ['smoke-tenant', 'smoke-caller', 'Authorization'] + sys.argv[2:]:
    assert secret not in text, 'Identity or credential in logs'
for line in text.splitlines():
    assert isinstance(json.loads(line), dict), line
PY
printf 'Remote MCP serves discovery, scopes, origin and quota checks over TLS with private logs and a clean stop\n'
