# Deployment and Operations Guide

**System:** axiom-reasoning-svc (AxiomEngine)

---

## 1. Containerization Strategy

The service uses a two-stage Docker build to minimize the runtime image size while ensuring BAML code generation occurs reproducibly within the build pipeline.

### Stage 1: Builder (`golang:1.25-bookworm`)

| Step | Command | Purpose |
|------|---------|---------|
| 1 | `go install golang.org/x/tools/cmd/goimports@latest` | Install Go import formatter required by BAML post-generation hook. |
| 2 | `go install github.com/boundaryml/baml/baml-cli@latest` | Install the BAML CLI for Go client code generation. |
| 3 | `COPY go.mod go.sum ./` then `go mod download` | Cache Go module dependencies in a separate Docker layer. |
| 4 | `COPY . .` | Copy application source code. |
| 5 | `baml-cli generate && gofmt -w baml_client && goimports -w baml_client` | Generate the `baml_client/` Go package from `.baml` source files and format the output. |
| 6 | `CGO_ENABLED=1 GOOS=linux go build -o /bin/axiom-server ./cmd/server` | Compile the server binary. `CGO_ENABLED=1` is required because the BAML Go runtime loads a native shared library (`.so`) via FFI at process startup. |

### Stage 2: Runtime (`debian:bookworm-slim`)

| Step | Command | Purpose |
|------|---------|---------|
| 1 | `apt-get install ca-certificates` | Install root CA certificates for HTTPS communication with Amazon Bedrock. |
| 2 | `COPY --from=builder /bin/axiom-server .` | Copy the compiled binary. |
| 3 | `COPY --from=builder /src/baml_src ./baml_src` | Copy BAML source files. Required at runtime because the BAML Go runtime re-reads them during `init()` to construct the internal prompt registry. |
| 4 | `COPY --from=builder /root/.cache/ /root/.cache/` | Copy the BAML native library cache (`libbaml_cffi-*.so`) downloaded during the builder stage, avoiding a runtime download. |

### Why Not `scratch` or `alpine`?

The BAML Go runtime dynamically loads a platform-specific shared library (`libbaml_cffi-x86_64-unknown-linux-gnu.so`) at process startup. This requires:

- A C library (`glibc`) available at runtime.
- The `/root/.cache/baml/libs/` directory to contain the pre-cached native binary.

`debian:bookworm-slim` provides both while maintaining a minimal image footprint (~80 MB base).

---

## 2. Docker Compose Configuration

The `docker-compose.yml` defines a single service with the following configuration:

```yaml
services:
  axiom-reasoning:
    build:
      context: .
      dockerfile: Dockerfile
    ports:
      - "8080:8080"
    env_file:
      - .env
    environment:
      - PORT=8080
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:8080/health"]
      interval: 10s
      timeout: 5s
      retries: 3
      start_period: 10s
```

| Directive | Purpose |
|-----------|---------|
| `env_file: .env` | Injects AWS credentials and optional PORT override. |
| `restart: unless-stopped` | Automatic restart on crash; does not restart if manually stopped. |
| `healthcheck` | Uses the `/health` endpoint to detect liveness failures. Docker marks the container as unhealthy after 3 consecutive probe failures. |

---

## 3. Observability

### 3.1 Structured Log Format

All application logs are emitted as single-line JSON objects to `stdout` via Go's `log/slog` package with a `slog.NewJSONHandler`. This format is directly ingestible by log aggregation systems without parsing.

**Schema:**

```json
{
  "time": "2026-04-10T01:30:25.613Z",
  "level": "INFO",
  "msg": "event.name",
  "key1": "value1",
  "key2": 42
}
```

| Field | Type | Description |
|-------|------|-------------|
| `time` | string (RFC 3339) | Timestamp of the log event. |
| `level` | string | One of: `INFO`, `WARN`, `ERROR`. |
| `msg` | string | Dot-namespaced event identifier. |
| (additional) | varies | Context-specific structured fields. |

### 3.2 Log Event Catalog

The following table enumerates every structured log event emitted by the service:

| Event (`msg`) | Level | Fields | Emitter |
|---------------|-------|--------|---------|
| `graph.loaded` | INFO | `tuples` | `main.go` -- startup |
| `server.starting` | INFO | `port` | `main.go` -- startup |
| `server.shutdown_initiated` | INFO | (none) | `main.go` -- shutdown |
| `server.shutdown_complete` | INFO | (none) | `main.go` -- shutdown |
| `server.shutdown_error` | ERROR | `error` | `main.go` -- shutdown |
| `server.listen_error` | ERROR | `error` | `main.go` -- listen failure |
| `rate_limit.exceeded` | WARN | `ip` | `main.go` -- limiter middleware |
| `panic.recovered` | ERROR | `error`, `stack`, `path`, `method` | `main.go` -- recover middleware |
| `circuit_breaker.state_change` | WARN | `name`, `from`, `to` | `reasoning.go` -- gobreaker callback |
| `request.received` | INFO | `request_id`, `student_id`, `failed_topics`, `vark_profile` | `handlers.go` |
| `request.parse_failed` | WARN | `request_id`, `error` | `handlers.go` |
| `request.success` | INFO | `request_id`, `items`, `elapsed_ms` | `handlers.go` |
| `request.failed` | ERROR | `request_id`, `error`, `elapsed_ms` | `handlers.go` |
| `request.timeout` | ERROR | `request_id`, `elapsed_ms` | `handlers.go` |
| `request.circuit_open` | ERROR | `request_id`, `elapsed_ms` | `handlers.go` |
| `stage.subgraph` | INFO | `student_id`, `course_id`, `vark_profile`, `failed_topics`, `tuples` | `reasoning.go` |
| `stage.topo_sort` | INFO | `student_id`, `course_id`, `vark_profile`, `topic`, `prerequisite_chain` | `reasoning.go` |
| `stage.llm_call_ok` | INFO | `student_id`, `course_id`, `vark_profile`, `topic`, `items_returned` | `reasoning.go` |
| `stage.llm_call_failed` | ERROR | `student_id`, `course_id`, `vark_profile`, `topic`, `error` | `reasoning.go` |
| `stage.circuit_breaker_open` | ERROR | `student_id`, `course_id`, `vark_profile`, `topic` | `reasoning.go` |
| `stage.merge` | INFO | `student_id`, `course_id`, `vark_profile`, `items_before_dedup`, `items_after_dedup` | `reasoning.go` |
| `stage.validation` | WARN | `student_id`, `course_id`, `vark_profile`, `items_dropped` | `reasoning.go` |
| `pipeline.complete` | INFO | `student_id`, `course_id`, `vark_profile`, `llm_latency_ms`, `total_latency_ms`, `items_final` | `reasoning.go` |
| `cognitive_graph.requested` | INFO | `student_id`, `topics` | `handlers.go` |
| `cognitive_graph.generated` | INFO | `student_id`, `nodes`, `edges` | `handlers.go` |

### 3.3 Integration with Log Aggregators

**ELK Stack (Elasticsearch + Logstash + Kibana):**

Since logs are already JSON, Logstash requires only the `json` codec on the input plugin:

```
input {
  docker {
    codec => json
  }
}
```

No grok patterns or custom parsers are necessary. Create Kibana dashboards filtering on `msg` values such as `pipeline.complete` to visualize `llm_latency_ms` distributions.

**Datadog:**

Configure the Datadog Agent to parse JSON logs from Docker container stdout:

```yaml
logs:
  - type: docker
    service: axiom-reasoning-svc
    source: go
    processing_rules:
      - type: multi_line
        name: json_logs
        pattern: '^\{'
```

Use `@msg` as the facet key for log event filtering and create monitors on `@msg:stage.llm_call_failed` for alerting.

---

## 4. Graceful Shutdown Procedure

The shutdown sequence is designed to prevent request loss during deployments:

```
1. Orchestrator sends SIGTERM to PID 1 in the container.
2. signal.NotifyContext triggers context cancellation in main().
3. app.ShutdownWithTimeout(10s) is invoked:
   a. The Fiber listener stops accepting new TCP connections.
   b. In-flight requests (including active LLM calls) continue processing.
   c. After all handlers return (or 10s elapses), Shutdown completes.
4. If timeout is exceeded, the process exits with code 1.
5. If shutdown is clean, the process exits with code 0.
```

**Deployment Recommendation:** Configure the container orchestrator (Kubernetes, ECS) with a `terminationGracePeriodSeconds` of at least 15 seconds to account for the 10-second shutdown timeout plus network propagation delay.
