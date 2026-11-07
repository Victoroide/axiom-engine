# Operations Runbook

This guide covers operational monitoring, diagnosis, and incident response procedures for production deployments of AxiomEngine.

---

## Circuit Breaker Trips

AxiomEngine wraps calls to Amazon Bedrock with an in-memory circuit breaker (`gobreaker`). After three consecutive failed calls the breaker trips from `Closed` to `Open`. Failures that count: throttling (429, `ThrottlingException`, `ModelNotReadyException`), `LLM_TIMEOUT` deadlines, 408, 5xx and transport errors, and credential failures (`Unspecified error code: 2`).

### Symptoms and Behavior

- The breaker enters the `Open` state for 15 seconds.
- While open, incoming plan requests fail immediately without invoking Bedrock, returning HTTP 503 `service_unavailable` with header `Retry-After: 15`.
- Four outcomes do not count: client cancellation (`context.Canceled`), unusable model output, a planner panic (`planner.panic`, HTTP 500), and upstream 4xx rejections other than 429 (`ValidationException`, `AccessDeniedException`, `ResourceNotFoundException`, `ServiceQuotaExceededException`, `ModelErrorException`).
- When the 15-second open interval elapses, the breaker enters `Half-Open`, allowing two probe requests. If both probe requests succeed, the breaker returns to `Closed`. If either fails, the breaker trips to `Open` for another 15 seconds.

### Telemetry Signals

- **Metric**: `axiom_circuit_breaker_state` gauge transitions: `0` (Closed), `1` (Half-Open), `2` (Open).
- **Metric**: `http_server_requests_total{route="/api/v1/adaptive-plan",status="503"}` increments rapidly.
- **Log**: Structured warning record with message `circuit_breaker.state_change`, logging `name="llm"`, `from`, and `to`.

### Diagnostic and Mitigation Steps

1. Check Amazon Bedrock service status in the configured AWS region (`AWS_REGION`).
2. Verify network egress and DNS resolution from container hosts to `bedrock-runtime.<region>.amazonaws.com`.
3. Review recent access logs (`http.access`) and upstream failure logs (`request.failed`) for error details.
4. Ensure upstream quota limits have not been reached (see [Bedrock Throttling](#bedrock-throttling)).

---

## Bedrock Throttling

When inbound request rates or concurrent token throughput exceed Amazon Bedrock service quotas, Bedrock returns HTTP 429 (`ThrottlingException`).

### Symptoms and Behavior

- The service classifies throttled Bedrock responses and returns HTTP 503 `service_unavailable` with header `Retry-After: 5`.
- Throttled calls count as breaker failures. Three in a row open the breaker, and clients then get `Retry-After: 15` instead of 5.

### Telemetry Signals

- **Metric**: `axiom_llm_calls_total{outcome="throttled"}` increments for each throttled invocation.
- **Metric**: `http_server_requests_total{route="/api/v1/adaptive-plan",status="503"}` increments.
- **Log**: `request.failed` with `error_class="service_unavailable"`. The log does not tell throttling apart from other 503 responses; use `axiom_llm_calls_total{outcome="throttled"}`.

### Diagnostic and Mitigation Steps

1. Inspect AWS Service Quotas in the AWS Console for Amazon Bedrock foundation model invocation quotas (Tokens Per Minute and Requests Per Minute).
2. Tune `LLM_MAX_CONCURRENCY` (default: 16) in the service configuration to cap simultaneous upstream calls below provisioned Bedrock account quotas.
3. Review `AWS_MAX_ATTEMPTS` in the environment; high retry limits inside SDK provider layers can amplify throttling backpressure.
4. If sustained workload requires higher throughput, request a Bedrock quota increase or scale workloads across additional AWS regions.

---

## Queue Saturation

AxiomEngine limits concurrent Bedrock invocations across all in-flight HTTP requests using a bounded semaphore (`LLM_MAX_CONCURRENCY`, default 16). When all slots are busy, incoming topic planning tasks wait in a FIFO queue.

### Symptoms and Behavior

- A planning topic that waits longer than `LLM_QUEUE_TIMEOUT` (default: `min(2s, LLM_TIMEOUT / 2)`) is evicted from the queue and fails.
- The request returns HTTP 503 `service_unavailable` with header `Retry-After: 1`.

### Telemetry Signals

- **Metric**: `axiom_llm_queue_wait_seconds` histogram observes wait durations before slot acquisition.
- **Metric**: `axiom_llm_inflight` gauge reaches `LLM_MAX_CONCURRENCY`.
- **Metric**: `http_server_requests_total{route="/api/v1/adaptive-plan",status="503"}` increments.

### Diagnostic and Mitigation Steps

1. Compare `axiom_llm_inflight` against `LLM_MAX_CONCURRENCY`. If concurrency is continuously pegged at the maximum, the service has reached its provisioned LLM throughput limit.
2. Check `axiom_llm_duration_seconds` to confirm if upstream Bedrock latency has increased, causing concurrency slots to be held longer.
3. Increase `LLM_MAX_CONCURRENCY` if AWS Bedrock quotas allow additional concurrent executions, or scale out replicas horizontally behind an API gateway.

---

## Region and Credential Resolution

AxiomEngine executes Bedrock calls via BAML's native CFFI engine, which resolves credentials and regions using the AWS SDK for Rust provider chain.

### Symptoms and Behavior

- **Static Region Warning**: At startup, when `LLM_PROVIDER=aws-bedrock`, the server inspects static environment configuration (`AWS_REGION`, `AWS_DEFAULT_REGION`, or the configuration profile). If no static region is found, it logs warning `llm.region.unresolved`. Non-Bedrock providers bypass static region inspection.
- **First Region Caching**: BAML initializes its AWS client upon the first call and caches the resolved AWS region for the lifetime of the process. If a region is resolved dynamically from IMDS, that region remains locked until process restart.
- **IMDS Latency**: If the service runs in an environment where the EC2 Instance Metadata Service (IMDS) is unreachable (such as off-cloud development or non-AWS containers) and static region environment variables are missing, SDK credential and region resolution will attempt to query IMDSv2 and IMDSv1, incurring a network timeout of approximately 2 seconds before falling back.
- **Credential Errors**: Missing or expired credentials cause upstream Bedrock calls to fail with native engine errors (for example, `Unspecified error code: 2` or `NoCredentialsError`), returned to the client as HTTP 502 `bad_gateway`. After three consecutive failures the breaker opens and clients get 503 with `Retry-After: 15`.

### Diagnostic and Mitigation Steps

1. Always define `AWS_REGION` explicitly in the container environment to avoid startup warnings and eliminate IMDS resolution latency.
2. Confirm the startup record `server.started` reports the expected `aws_region_source` (`AWS_REGION`, `AWS_DEFAULT_REGION`, `profile:<name>`, or `""`).
3. For container deployments on ECS or Kubernetes (EKS), ensure task roles or IAM Roles for Service Accounts (IRSA) provide required permissions (`bedrock:InvokeModel`) for model `amazon.nova-micro-v1:0`.
4. For detailed credential resolution mechanics, see [Configuration: AWS Credential Resolution](configuration.md#aws-credential-resolution).

---

## Native Library Failures

The BAML CFFI dynamic library (`libbaml_cffi.so` on Linux, `libbaml_cffi.dylib` on macOS) is required for application execution.

### Symptoms and Behavior

- The generated Go BAML client loads the dynamic library during package initialization (`init()`), before `main()` executes.
- If the shared library is missing from `BAML_LIBRARY_PATH`, cannot be opened, is built for another architecture, or reports another version, the process crashes immediately with a panic printed in plain text to standard error. BAML does not check the checksum at load time. Only the image build and `make native-baml` compare it with the pin.
- Because this failure occurs during package initialization, the structured JSON application logger has not yet been instantiated. No `server.started` or `server.failed` JSON record will be produced.

### Diagnostic and Mitigation Steps

1. Inspect process `stderr` logs for initialization panics referencing `libbaml_cffi`.
2. Ensure the container or environment sets `BAML_LIBRARY_PATH` to the absolute path of the native library (for example, `/app/libbaml_cffi.so` in runtime containers).
3. Ensure `BAML_LIBRARY_DISABLE_DOWNLOAD=true` is set to prevent unverified dynamic library downloads at runtime.
4. In local development, run `make native-baml`. It downloads the pinned library when `bin/` has none and checks its SHA-256 against the Dockerfile pin. If the check fails, delete the file and run it again. `make baml-check` only checks that the generator, Go module and Dockerfile versions agree.

---

## Token Rotation

For procedures to rotate internal service tokens (`AXIOM_INTERNAL_TOKEN`) without service downtime using `AXIOM_INTERNAL_TOKEN_PREVIOUS`, refer to [Deployment: Token rotation](deployment.md#token-rotation).

---

## Distributed Tracing

AxiomEngine exports distributed traces using OpenTelemetry and OTLP/HTTP protobuf when `TRACING_ENABLED=true` (see [configuration](configuration.md#tracing)).

### Exporter Setup

1. Deploy an OpenTelemetry collector or receiver accepting OTLP/HTTP traffic on `/v1/traces`.
2. Configure AxiomEngine:

```sh
export TRACING_ENABLED=true
export OTEL_EXPORTER_OTLP_ENDPOINT="http://otel-collector:4318"
export OTEL_SERVICE_NAME="axiom-engine"
```

W3C `traceparent` and `tracestate` headers are extracted and injected only when at least one exporter (`TRACING_ENABLED=true` or `LANGSMITH_TRACING=true`) is configured. When both are disabled, caller headers are not extracted or echoed, and access logs omit caller trace and span IDs. When an exporter is configured, an incoming remote span becomes the parent of the server request span, and outgoing responses include matching `traceparent` and `tracestate` headers to preserve distributed context.

### Trace Structure and Spans

The reasoning pipeline produces the following span hierarchy for `POST /api/v1/adaptive-plan`:

- **Server span**: Named after the HTTP method and parameterized route template (for example, `POST /api/v1/adaptive-plan` or `GET /api/v1/tutor/student/:student_id/cognitive-graph`). Tagged with `http.request.method`, `http.route`, `http.response.status_code`, and `request_id`. Unknown HTTP methods are normalized to `http.request.method="_OTHER"`, their span is named `HTTP`, and the raw method is preserved in `http.request.method_original`. HTTP 5xx responses mark the span with status `Error` and error classification.
- **`plan.validate`**: Measures incoming payload validation and topic graph membership checks. Failures record `error.type="validation_error"`.
- **`plan.subgraph`**: Measures local bidirectional subgraph extraction around failed topics.
- **`plan.topic`**: Spawned for each requested topic during concurrent fan-out. Tagged with:
  - `axiom.topic`: Name of the evaluated topic.
  - `axiom.topic.index`: Original index of the topic in the request.
  - `axiom.queue_wait_ms`: Duration in milliseconds spent waiting in the concurrency semaphore queue.
- **`chat <model>`**: Child of `plan.topic` representing downstream model execution. Conforms to OpenTelemetry GenAI semantic conventions:
  - `gen_ai.operation.name`: `"chat"`
  - `gen_ai.provider.name` & `gen_ai.system`: standardized provider name (`aws.bedrock`, `gcp.gemini`, `openai`, `anthropic`; `ollama` and `openai-generic` remain documented custom values). These convention values differ deliberately from the configuration spellings for Bedrock (`aws-bedrock`) and Google AI (`google-ai`).
  - `gen_ai.request.model`: configured model identifier.
  - `gen_ai.request.max_tokens`: `2048`
  - `gen_ai.request.temperature`: `0.1`
  - `gen_ai.usage.input_tokens`: prompt tokens counted by the BAML collector.
  - `gen_ai.usage.output_tokens`: completion tokens counted by the BAML collector.
- **`plan.merge`**: Measures deduplication and merging of parallel topic recommendations.
- **`plan.validate_output`**: Measures graph prerequisite closure filtering and coverage validation. Failures set `error.type="invalid_output"`.

For streaming plan requests, the server span remains open through generation and response streaming.

### Trace-Correlated Logs

When at least one exporter is configured, the structured JSON logger wraps every log record emitted in a traced context with `trace_id` and `span_id`:

```json
{"time":"2026-11-06T18:03:00.123456-04:00","level":"INFO","msg":"http.access","request_id":"req-abc123","method":"POST","path":"/api/v1/adaptive-plan","status":200,"duration_ms":342,"ip":"127.0.0.1","trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","span_id":"00f067aa0ba902b7"}
```

### LangSmith Integration

AxiomEngine can export model reasoning traces directly to LangSmith without running an intermediate OpenTelemetry collector.

#### Setup

Configure LangSmith environment variables:

```sh
export LANGSMITH_TRACING=true
export LANGSMITH_API_KEY="lsv2_pt_..."
# Optional project name (defaults to LangSmith default project):
export LANGSMITH_PROJECT="axiom-reasoning"
```

To target an EU-region project or a self-hosted instance, set `LANGSMITH_ENDPOINT`:
- EU region: `LANGSMITH_ENDPOINT="https://eu.api.smith.langchain.com"` (exports to `https://eu.api.smith.langchain.com/otel/v1/traces`)
- Self-hosted instance: `LANGSMITH_ENDPOINT="https://traces.example.com/api/v1"` (exports to `https://traces.example.com/api/v1/otel/v1/traces`)

#### Trace Buffering, Filtering and Run Hierarchy

LangSmith requires child spans to share their export with their parent to assemble trace trees accurately. AxiomEngine uses an in-memory trace processor (`traceProcessor`) that buffers spans for each local request until every parent and child span finishes:

- **Model trace filtering**: Only completed traces containing a downstream model call (`langsmith.span.kind=llm`) are forwarded to the LangSmith export queue. Health checks (`/livez`, `/readyz`, `/health`), authorization failures, and unauthenticated or malformed requests that do not invoke the planner model are discarded from LangSmith export and do not consume queue capacity (they remain available to the generic OTLP exporter).
- **Remote parent re-rooting**: When an incoming request includes W3C trace context from an external caller, LangSmith re-roots the local request tree by omitting the remote parent span ID from the export payload, ensuring LangSmith receives self-contained trace hierarchies. The generic OTLP exporter retains the caller's remote parent.
- **Independent local roots**: Spans are grouped by local root rather than trace ID alone. Overlapping or concurrent requests sharing a trace ID are tracked in separate groups and exported independently as each request finishes.
- **Bounded buffering**: Total buffered spans across active, finished, queued, and in-flight exports are capped process-wide at 2048 spans. Individual local request groups are capped at 256 spans, and the finished export queue holds at most 128 groups. When limits are exceeded, entire groups are dropped rather than exporting incomplete hierarchies.
- **Drop metrics**: When `METRICS_ENABLED=true`, dropped spans are counted process-wide by `axiom_langsmith_dropped_spans_total{reason}` with fixed labels `buffer_full`, `group_full`, `missing_parent`, `queue_full`, and `shutdown`. Queue overflow does not emit per-drop log records.

Exported spans include LangSmith-specific run annotations:
- `langsmith.span.kind`: set to `chain` for HTTP request and per-topic reasoning spans, and `llm` for downstream model invocation spans (`chat <model>`).
- `langsmith.trace.name`: identifies the operation name for the root run.
- `langsmith.metadata.request_id`: records the request ID for trace correlation.

### Content Capture and Privacy

By default, enabling LangSmith (`LANGSMITH_TRACING=true`) turns on content capture (`TRACING_CAPTURE_CONTENT=true`).

#### Captured Attributes

When content capture is active, spans include:
- **Downstream model calls (`chat <model>`)**:
  - `gen_ai.prompt.N.role` and `gen_ai.prompt.N.content`: Rendered prompt messages parsed from the underlying model request body across all supported providers (Bedrock, OpenAI, Anthropic, Google AI, Ollama, generic).
  - `gen_ai.completion.0.role` (`assistant`) and `gen_ai.completion.0.content`: Raw model completion payload from the Boundary ML collector.
- **Plan request spans (`POST /api/v1/adaptive-plan`)**:
  - `input.value`: JSON payload containing `failed_topics` and `vark_profile`.
  - `output.value`: JSON payload containing the validated study `items`.

#### Privacy Controls

- **Learner identification exclusion**: Student identifiers (`student_id`) and course identifiers (`course_id`) are strictly excluded from span attributes, `input.value`, and `output.value`.
- **Sensitive header isolation**: HTTP authorization headers, internal tokens, and provider API keys are never captured in span attributes or logged.
- **Attribute truncation**: Each captured prompt, completion, input, or output string is truncated to a maximum of 32 KiB on a valid UTF-8 character boundary.
- **Disabling content capture**: Set `TRACING_CAPTURE_CONTENT=false` to export span hierarchy, latency, error types, and token usage counts to LangSmith and OTLP receivers without capturing prompt, completion, or input/output text.
- **Dual-exporter privacy**: Captured content is attached to span attributes in memory; if both the generic OTLP exporter (`TRACING_ENABLED=true`) and LangSmith (`LANGSMITH_TRACING=true`) are active, captured content is delivered to both destinations. Verify collector destination policies when running both exporters.

### Exporter Error Handling and Shutdown Flushing

During runtime, exporter failures invoke an internal error handler that emits a fixed `WARN` log record with `error_class="trace_export_failed"` at most once per minute, omitting collector response bodies and upstream messages:

```json
{"level":"WARN","msg":"tracing.export_failed","error_class":"trace_export_failed"}
```

On process termination, the trace provider is shut down within the remaining graceful shutdown budget (signal time + `SHUTDOWN_DRAIN_DELAY` + `SHUTDOWN_TIMEOUT`). Pending spans are flushed to the OTLP receiver after the listener closes and Fiber drains connections. If flushing fails before the deadline, the process logs an error:

```json
{"level":"ERROR","msg":"tracing.flush_failed","error_class":"trace_export_failed"}
```

---

## Image Verification

For procedures to verify multi-architecture release container images, cryptographic Cosign signatures, and SLSA provenance attestations, refer to [Deployment: Verifying images](deployment.md#verifying-images).

---

## Cost Controls

Invocations to Amazon Bedrock incur operational costs based on input and output tokens consumed.

### Operational Cost Factors

- **Non-Idempotent Inferences**: Requests omitting `Idempotency-Key` or retried after failure trigger fresh model inferences and generate additional token consumption. Callers should supply an `Idempotency-Key` on retryable requests to replay cached responses without incurring model token costs.
- **Client Disconnects**: On the non-streaming route (`POST /api/v1/adaptive-plan`), client HTTP disconnects do not cancel in-flight Bedrock reasoning; work continues until `REQUEST_TIMEOUT` expires. On the streaming route (`POST /api/v1/adaptive-plan/stream`), closing the connection cancels in-flight topic reasoning when the response stream closes, and the slots are released; keepalive comments every 2 seconds surface a broken connection on the next write. Client timeouts on the non-streaming route should be configured conservatively.
- **Concurrency Caps**: Configure `LLM_MAX_CONCURRENCY` to control maximum parallel model invocations and prevent traffic spikes from generating unbounded token usage.
- **Aborted Calls**: Calls cut off by `LLM_TIMEOUT` or `REQUEST_TIMEOUT` close the connection, but AWS documents no billing rule for aborted Converse calls. Assume their tokens may be billed.

---

## Access Logs and Disconnect Monitoring

Every HTTP request produces an `http.access` structured log record. For streaming requests (`POST /api/v1/adaptive-plan/stream`), access records are emitted when the stream ends and measure the full connection duration:
- `duration_ms`: Total stream duration in milliseconds from initial request reception until stream termination.
- `client_disconnected`: Boolean attribute present and set to `true` when a stream terminates after a failed client write or keepalive flush.
- `http_server_request_duration_seconds`: Histogram metric records the full stream duration for streaming routes.

---

## Metrics to Watch

With `METRICS_ENABLED=true`, `/metrics` exposes the series listed in [Observability](architecture.md#observability). The ones these procedures rely on:

- `axiom_circuit_breaker_state`: 2 means open; plan requests fail fast with 503.
- `axiom_llm_calls_total{outcome="throttled"}` and `{outcome="unavailable"}`: upstream throttling and availability failures.
- `axiom_llm_inflight` against `LLM_MAX_CONCURRENCY`, with `axiom_llm_queue_wait_seconds` and `axiom_llm_queue_timeouts_total`: queue pressure.
- `http_server_requests_total{route="/api/v1/adaptive-plan",status="503"}`: unavailable responses seen by clients.
- `axiom_auth_previous_token_total`: callers still using the previous token during a rotation.
- `axiom_resource_links_checked_total{result="ok|status|timeout|refused|invalid"}`: tracks outbound resource validation when enabled via `RESOURCE_LINK_CHECK=true` (optionally configured with `RESOURCE_LINK_TIMEOUT`, default `3s`). Links are not fetched while disabled. Enabling checks can increase response latency within `REQUEST_TIMEOUT`. A failed link drops the unreachable resource rather than failing its topic or request; exhausting the request deadline returns HTTP 504 `gateway_timeout`.
