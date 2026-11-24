# Operations Runbook

This guide covers operational monitoring, diagnosis, and incident response procedures for production deployments of AxiomEngine.

---

## Circuit Breaker Trips

Each process has one in-memory circuit breaker (`gobreaker`) for its configured backend, shared by all topics and callers. After three consecutive failed calls the breaker trips from `Closed` to `Open`. Failures that count include throttling (429, `ThrottlingException`, `ModelNotReadyException`), `LLM_TIMEOUT` deadlines, 408, 5xx and transport errors, and credential failures (`Unspecified error code: 2`).

### Symptoms and Behavior

- The breaker enters the `Open` state for 15 seconds.
- Each topic acquires a concurrency permit before checking the breaker. If the breaker is still open at that point, it rejects the topic without dispatching the planner. The JSON plan route returns HTTP 503 `service_unavailable` with `Retry-After: 15`. If `LLM_QUEUE_TIMEOUT` expires first while the request is active, the route returns HTTP 503 `service_unavailable` with `Retry-After: 1` for [queue saturation](#queue-saturation).
- The breaker records four errors as exclusions. They are cancellation (`context.Canceled`), unusable model output (`ErrInvalidOutput`), provider rejection (`ErrUpstreamRejected`), and a recovered planner panic received while the call context is active (`ErrPlannerPanic`). HTTP 408 and 429 from the provider count as failures. An excluded outcome in `Half-Open` frees a probe slot without advancing recovery.
- When the 15-second open interval elapses, the breaker enters `Half-Open`, allowing two probe requests. If both probe requests succeed, the breaker returns to `Closed`. If either fails, the breaker trips to `Open` for another 15 seconds.

### Telemetry Signals

- **Metric**: `axiom_circuit_breaker_state` gauge transitions: `0` (Closed), `1` (Half-Open), `2` (Open).
- `http_server_requests_total{route="/api/v1/adaptive-plan",status="503"}` counts JSON plan responses with 503, including breaker rejections and queue timeouts.
- **Log**: Structured warning record with message `circuit_breaker.state_change`, logging `name="llm"`, `from`, and `to`.

### Diagnostic and Mitigation Steps

1. Check Amazon Bedrock service status in the configured AWS region (`AWS_REGION`).
2. Verify network egress and DNS resolution from container hosts to `bedrock-runtime.<region>.amazonaws.com`.
3. Review recent access logs (`http.access`) and upstream failure logs (`request.failed`) for error details.
4. Ensure upstream quota limits have not been reached (see [Bedrock Throttling](#bedrock-throttling)).

---

## Planner Panics

The reasoning service recovers a panic in a running planner call and logs `planner.panic` at `ERROR`. The record includes `error_class="internal_error"`, `request_id` capped at 128 bytes, and `trace_id` and `span_id` when available. Standard JSON lines are at most 1024 bytes including the newline. The recovery record omits the recovered value and contains zero stack frames.

If the service receives `ErrPlannerPanic` while the call context is active, the breaker excludes it. When the recovered panic determines the plan failure, the JSON route returns HTTP 500 `internal_error`, an open, connected SSE stream keeps HTTP 200 and sends a terminal `error` event with `internal_error` and `status=500`, and stdio MCP returns an `internal_error` tool error. The process keeps serving.

If a dispatched topic's context expires or is canceled before the service accepts the planner result, that topic returns the context error even if its panic result is ready. The breaker records a deadline as a failure and cancellation as an exclusion. The first error returned by any topic determines the plan response and cancels sibling topic contexts. If a canceled planner later panics, the service still logs the panic and releases its permit as the call unwinds.

If the process exits before serving requests and stderr shows a BAML initialization panic, see [Native Library Failures](#native-library-failures).

---

## Bedrock Throttling

When inbound request rates or concurrent token throughput exceed Amazon Bedrock service quotas, Bedrock returns HTTP 429 (`ThrottlingException`).

### Symptoms and Behavior

- The service classifies throttled Bedrock responses and returns HTTP 503 `service_unavailable` with header `Retry-After: 5`.
- Throttled calls count as breaker failures. Three in a row open the breaker. When a topic acquires a permit and reaches the open breaker, the JSON plan route returns `Retry-After: 15`.

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

AxiomEngine limits concurrent planner calls in each HTTP or stdio MCP process using a bounded semaphore (`LLM_MAX_CONCURRENCY`, default 16). When all slots are busy, incoming topic planning tasks wait in a FIFO queue.

### Symptoms and Behavior

- A planning topic that waits longer than `LLM_QUEUE_TIMEOUT` (default: `min(2s, LLM_TIMEOUT / 2)`) is evicted from the queue and fails.
- The JSON plan route returns HTTP 503 `service_unavailable` with `Retry-After: 1`.
- An active SSE stream keeps HTTP 200 and reports the queue failure in a terminal `error` event with `error` set to `service_unavailable` and `status` set to `503`. The `Retry-After` header applies to the JSON route.
- The stdio MCP plan tool sets `isError` to `true` and returns `error` set to `service_unavailable` and `details` in structured content.
- After dispatch, a deadline or cancellation can end the service's wait while the planner is still running. The slot stays occupied until the planner returns or unwinds after a panic. Ending the wait early can save the client time. The provider may continue processing and charge for the work. Use provider telemetry and billing records to assess that work and its cost.

### Telemetry Signals

- `axiom_llm_queue_wait_seconds` records wait durations. `axiom_llm_queue_timeouts_total` counts waits that exhaust the queue deadline.
- `axiom_llm_inflight` counts local planner calls occupying slots. `axiom_llm_inflight_after_cancel` counts the subset still running after cancellation or a deadline, without labels.
- `http_server_requests_total{route="/api/v1/adaptive-plan",status="503"}` increments for unavailable responses.

### Diagnostic and Mitigation Steps

1. Compare `axiom_llm_inflight` against `LLM_MAX_CONCURRENCY`. If the gauge stays at the limit, check `axiom_llm_inflight_after_cancel` to see how many slots are still occupied after callers stopped waiting.
2. Check queue wait and timeout metrics alongside `axiom_circuit_breaker_state`. Queue timeouts indicate admission pressure. An open breaker rejects work after it acquires a slot.
3. Check provider latency and quotas before increasing local limits or adding replicas.

### Replica concurrency budget

Add the `LLM_MAX_CONCURRENCY` limits of all running HTTP and stdio MCP processes to get the fleet's local planner concurrency ceiling. Two processes with limits of 2 and 3 can admit five calls at once. Provision shared provider capacity for those five calls plus any additional replicas active during a rolling update. Allow for the expected provider request and token rates, including retries.

To enforce a lower fleet-wide ceiling, use a shared outbound gateway that limits planner calls across processes through upstream completion, or a provider quota that enforces concurrent calls. One inbound plan request can fan out into multiple planner calls.

---

## HTTP Rate-Limit Storage

The `/api/v1` routes apply a sliding-window budget per client IP after authentication when enabled. Each HTTP replica keeps its counters in memory by default. Replicas that configure `REDIS_URL` to use the same Redis database share counters under `axiom:rate:`. Stdio MCP bypasses HTTP rate limiting.

If the configured Redis store cannot make a rate-limit decision, the API returns HTTP 503 `service_unavailable` with `Retry-After: 1` before dispatch. Look for `storage.failed` with `operation=rate_limit.consume`. Local health probes can still succeed during the outage. See [Replicas and shared storage](deployment.md#replicas-and-shared-storage).

---

## Curriculum Rollout

Set `GRAPH_RELOAD_ENABLED=true` and point `GRAPH_FILE` at a readable local regular file before starting each HTTP or stdio MCP replica. This is supported on Linux and macOS. Set `LOG_LEVEL=info` or `debug` to see the active revision during rollout. HTTP writes logs to stdout and stdio MCP writes them to stderr.

1. Stage the replacement beside `GRAPH_FILE` on the same filesystem and make it readable by the serving process. Run `axiom-server validate-graph <staged-file>` and record the SHA-256 of the staged file. Retain the preceding file for rollback.
2. Atomically rename the staged file over `GRAPH_FILE`. Distribute the same bytes and send `kill -HUP <pid>` to **each** enabled replica after its file is in place. Operators or the deployment system own both steps.
3. Within **60 seconds from the start of the fleet rollout**, verify that each serving replica reports the expected `graph_revision` in its `graph.reload` log. If any replica still differs at the deadline, stop the rollout and drain it, or restore the previous file by rename and signal the affected replicas.

The revision is the SHA-256 of the complete file, including whitespace. `graph.reload` reports `loaded` at startup, `published` after a successful swap, `unchanged` for identical bytes, `failed` when the replacement cannot be loaded or validated, and `canceled` when shutdown interrupts a load. Each record includes the active revision and cumulative `published_total` and `failed_total` counts. HTTP also exposes `axiom_graph_reload_success_total` and `axiom_graph_reload_failure_total` when `METRICS_ENABLED=true`. Those counters have no labels. Reload records omit the file path, curriculum contents and raw errors. Run `validate-graph` separately to diagnose validation errors.

Shutdown cancels any active reload as soon as draining starts and waits for the loader to release its file within the existing shutdown deadline.

A missed signal or invalid replacement can leave the previous revision serving indefinitely. Each request keeps the snapshot it started with, and successful JSON responses retained for idempotency can still replay their original bytes until their original TTL expires. To return to startup-only loading, disable `GRAPH_RELOAD_ENABLED` and restart the processes. A rolling restart can also activate a replacement.

## Region and Credential Resolution

AxiomEngine executes Bedrock calls via BAML's native CFFI engine, which resolves credentials through the AWS SDK for Rust provider chain. It also resolves the region through that chain unless `BEDROCK_REQUIRE_EXPLICIT_REGION=true` supplies one.

### Symptoms and Behavior

- **Startup region source**. With `BEDROCK_REQUIRE_EXPLICIT_REGION=true`, HTTP and MCP startup require a valid `AWS_REGION`. In default Bedrock mode, startup inspects `AWS_REGION`, `AWS_DEFAULT_REGION` and the selected profile. When none provides a region, startup logs `llm.region.unresolved`. Non-Bedrock providers skip AWS region inspection.
- **Region lifetime**. With `BEDROCK_REQUIRE_EXPLICIT_REGION=true`, the validated startup region is used for every call until restart. In default mode, the native chain resolves the region on each call, so a changed environment, profile file or metadata reply can change the signing region for later calls.
- **IMDS latency**. If a Bedrock call needs a region and the environment and selected profile provide none, the service tries IMDSv2. Lookup time depends on the metadata endpoint and the call deadline. The native credential chain can also contact metadata when it needs credentials. [AWS SDK support for IMDS credentials](https://docs.aws.amazon.com/sdkref/latest/guide/feature-imds-credentials.html).
- **Credential Errors**: Missing or expired credentials cause upstream Bedrock calls to fail with native engine errors (for example, `Unspecified error code: 2` or `NoCredentialsError`), returned to the client as HTTP 502 `bad_gateway`. When a topic acquires a permit and reaches the open breaker, the JSON plan route returns HTTP 503 with `Retry-After: 15`.

### Diagnostic and Mitigation Steps

1. Define `AWS_REGION` explicitly to avoid relying on IMDS for region resolution. Set `BEDROCK_REQUIRE_EXPLICIT_REGION=true` to fail startup when the region is missing or malformed and to keep that region for every call. Restart the process to change a pinned region.
2. Confirm the startup record `server.started` reports the expected `aws_region_source` (`AWS_REGION`, `AWS_DEFAULT_REGION`, `profile:<name>`, or `""`).
3. For container deployments on ECS or Kubernetes (EKS), ensure task roles or IAM Roles for Service Accounts (IRSA) provide required permissions (`bedrock:InvokeModel`) for model `amazon.nova-micro-v1:0`.
4. For detailed credential resolution mechanics, see [Configuration: AWS Credential Resolution](configuration.md#aws-credential-resolution).

---

## Native Library Failures

The BAML CFFI dynamic library (`libbaml_cffi.so` on Linux, `libbaml_cffi.dylib` on macOS) is required for application execution.

### Symptoms and Behavior

- The generated Go BAML client loads the dynamic library during package initialization (`init()`), before `main()` executes.
- If the shared library is missing from `BAML_LIBRARY_PATH`, cannot be opened, is built for another architecture, or reports another version, the process crashes immediately with a panic printed in plain text to standard error. The image and archive builds verify the native library against the pin before packaging.
- Because this failure occurs during package initialization, the structured JSON application logger has not yet been instantiated. No `server.started` or `server.failed` JSON record will be produced.

### Diagnostic and Mitigation Steps

Set `BAML_LIBRARY_PATH` to the absolute path of the native library, such as `/app/libbaml_cffi.so` in the runtime image. Set `BAML_LIBRARY_DISABLE_DOWNLOAD=true` to prevent runtime library downloads. Build with `BAML_DIAGNOSTIC=1` to include `axiom-baml-check`, then run it as the service user. It checks for a readable regular file, the expected shared-library format and CPU architecture, and the SHA-256 digest pinned for the bundled BAML version. It resolves symlinks and accepts libraries up to 256 MiB. It opens the file read-only, accesses no network, and leaves stdout free for the service protocol. [Run the diagnostic before startup](deployment.md#check-the-native-library-before-startup).

On Linux, use an absolute path or an explicit relative path such as `./libbaml_cffi.so`. The Linux loader searches its library paths for a name without a slash, so the standalone diagnostic rejects bare names.

The executable emits one JSON line on stderr with `status`, `reason`, `platform`, and `baml_version`. The line omits the path and credential values.

| Exit code | `status` | `reason` |
| --- | --- | --- |
| `0` | `ok` | `verified` |
| `1` | `error` | `missing_file`, `not_regular_file`, `unreadable_file`, `file_too_large`, `invalid_header`, `platform_mismatch`, or `digest_mismatch` |
| `2` | `error` | `path_not_set`, `path_requires_slash` (Linux), `invalid_arguments`, `unsupported_platform`, or `invalid_build_pin` |

The diagnostic verifies file bytes without loading the library. A missing transitive shared library, unresolved symbol, incompatible system ABI, or loader policy such as a `noexec` mount may still cause a startup failure. Inspect the server's native stderr for `libbaml_cffi` initialization errors. Run `make release-archive-smoke` against a built archive or `make container-smoke` against a built runtime image to exercise real native startup and shutdown. Pass `BAML_DIAGNOSTIC=1` to `container-smoke` for an image built with the diagnostic.

In local development, `make native-baml` downloads the pinned library when `bin/` has none and verifies its SHA-256 against the Dockerfile pin. If verification fails, delete the file and rerun the target. `make baml-check` checks that the generator, Go module and Dockerfile versions agree. `make build-baml-check` builds the standalone diagnostic for a supported release platform.

Use `axiom-server-launch` for extracted archives. It disables the public and internal BAML loggers and clears the native trace file and upload settings. If you enable native diagnostics on a direct binary launch, use synthetic input because those diagnostics can contain prompts and responses. [Run an archive](deployment.md#run-an-archive).

---

## HTTP authentication

Use the startup record's `auth` field to confirm the HTTP mode. Shared mode reports `token` or `token+previous`. Verified mode reports `verified`. [Shared-token rotation](deployment.md#token-rotation) · [Verified access tokens](deployment.md#verified-access-tokens)

For a verified HTTP 401, check that the client sends one Bearer header, that the issuer and audience match the configured values, and that the key ID, signing algorithm and expiration are valid. Confirm the service can reach the configured key set over HTTPS. A 403 indicates a verified access token that lacks the operation's required scope. [Route scopes](api.md#verified-mode)

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
- **`plan.queue.wait`**: Child of `plan.topic` that starts when an immediate concurrency permit is unavailable and ends when admission succeeds, times out, or is canceled. Shares the topic's request ID.
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

In every authentication mode, `http.access` records the registered route template in `path` and uses `unmatched` when routing could not select an endpoint. The template keeps learner path parameters out of the access log.

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
- **Panic privacy**: HTTP JSON errors, errors before an SSE stream opens, and MCP tool errors carry their classification on the request span. When a recovered planner panic determines the error for an open, connected SSE stream, the HTTP request span reports HTTP 200 and the terminal `error` event carries `internal_error` with equivalent status `500`. Spans that end during panic unwinding omit automatic exception events and recovered values.
- **Disabling content capture**: Set `TRACING_CAPTURE_CONTENT=false` to export span hierarchy, latency, error types, and token usage counts to LangSmith and OTLP receivers without capturing prompt, completion, or input/output text.
- **Dual-exporter privacy**: Captured content is attached to span attributes in memory; if both the generic OTLP exporter (`TRACING_ENABLED=true`) and LangSmith (`LANGSMITH_TRACING=true`) are active, captured content is delivered to both destinations. Verify collector destination policies when running both exporters.

### Exporter Error Handling and Shutdown Flushing

During runtime, exporter failures invoke an internal error handler that emits a fixed `WARN` log record with `error_class="trace_export_failed"` at most once per minute, omitting collector response bodies and upstream messages:

```json
{"level":"WARN","msg":"tracing.export_failed","error_class":"trace_export_failed"}
```

On process termination, the trace provider is shut down within the remaining graceful shutdown budget (signal time + `SHUTDOWN_DRAIN_DELAY` + `SHUTDOWN_TIMEOUT`). Pending spans are flushed after listener shutdown and connection draining. Export workers may report final batch failures through `tracing.export_failed`. If the shutdown callback reports an error or panics, `tracing.flush_failed` carries the fixed classification. Panics in either diagnostic write are contained.

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
- With `HTTP_LISTENER=net-http`, a peer closing an HTTP/1 connection or resetting an HTTP/2 stream cancels queued and running reasoning for that request. With the default Fiber listener, JSON reasoning runs to completion or its deadline after a disconnect. SSE cancels on a failed write or flush, with keepalives every two seconds. In both modes a dispatched call holds its permit until the planner returns or unwinds after a panic. See [Queue Saturation](#queue-saturation).
- **Concurrency Caps**: Configure `LLM_MAX_CONCURRENCY` to control maximum parallel model invocations and prevent traffic spikes from generating unbounded token usage.
- `LLM_TIMEOUT` and `REQUEST_TIMEOUT` end the service's wait for a result. If the provider continues processing the call, its tokens may still be billed.

---

## Access Logs and Disconnect Monitoring

Requests that reach the application produce an `http.access` structured log record. Streaming access records are emitted when the stream ends.
- `duration_ms`: Total stream duration in milliseconds from initial request reception until stream termination.
- `client_disconnected` is true when the opt-in listener observes peer cancellation or an SSE write or flush fails. For HTTP/1 it can record a TCP write-half close while the client still reads the response. A server body read timeout records its 408 status without the disconnect field.
- `http_server_request_duration_seconds`: Histogram metric records the full stream duration for streaming routes.

The opt-in HTTP/1 server can reject requests before application logging or request ID assignment. Examples include plain-text 400 or 431 for malformed or oversized headers, plain-text 400 for chunked declarations of `Content-Length`, `Trailer`, or `Transfer-Encoding`, an empty 417 for an unsupported `Expect` value, and a connection close for an incomplete header at the read deadline. Clients and ingress logs can observe those failures. When a prohibited trailer declaration reaches application error handling, the response is JSON 400 with a generated request ID and the access record shows path `unmatched`. Quota remains available for the next valid request. Application body read errors use the JSON envelope. A failed payload read leaves quota available. A rejection in the trailer block after the body retains the request ID, runs the normal quota checks, and records the application status in the access log. Check the `listener` field on `server.listening` when comparing responses. To switch back, set `HTTP_LISTENER=fiber` and restart the service. Test disconnect propagation through the deployment's proxy because buffering can delay or prevent it. See [HTTP Listener](configuration.md#http-listener).

---

## Metrics to Watch

With `METRICS_ENABLED=true`, `/metrics` exposes the series listed in [Observability](architecture.md#observability). The ones these procedures rely on:

- `axiom_circuit_breaker_state` is `2` when open. Topics acquire a concurrency permit before checking the breaker. See [Circuit Breaker Trips](#circuit-breaker-trips).
- `axiom_llm_calls_total{outcome="throttled"}` and `{outcome="unavailable"}`: upstream throttling and availability failures.
- `axiom_plan_prompt_budget_rejections_total` counts HTTP adaptive-plan requests rejected before dispatch for exceeding the curriculum prompt budget. The counter has no labels.
- `axiom_llm_inflight` and `axiom_llm_inflight_after_cancel` show occupied local slots. `axiom_llm_queue_wait_seconds` and `axiom_llm_queue_timeouts_total` show admission pressure.
- `http_server_requests_total{route="/api/v1/adaptive-plan",status="503"}`: unavailable responses seen by clients.
- `axiom_auth_previous_token_total`: callers still using the previous token during a rotation.
- `axiom_resource_links_checked_total{result="ok|status|timeout|refused|invalid"}` counts one outcome per distinct sanitized URL in each HTTP plan request when `RESOURCE_LINK_CHECK=true`. The setting also enables checks for the stdio MCP `generate_study_plan` tool. The checker shares eight permits across requests. `RESOURCE_LINK_TIMEOUT` (default `3s`) includes time spent waiting for a permit, within the overall `REQUEST_TIMEOUT`. Each occurrence of a failed URL increments `_meta.resources_unreachable`. If link checking exhausts the request deadline, HTTP reports `gateway_timeout` and MCP returns a tool error.
