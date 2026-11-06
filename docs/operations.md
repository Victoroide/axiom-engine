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
- **Log**: Structured warning record with message `circuit_breaker.state_change`, logging `name="bedrock-nova-micro"`, `from`, and `to`.

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

- **Static Region Warning**: At startup, the server inspects static environment configuration (`AWS_REGION`, `AWS_DEFAULT_REGION`, or the configuration profile). If no static region is found, it logs warning `bedrock.region.unresolved`.
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

## Image Verification

For procedures to verify multi-architecture release container images, cryptographic Cosign signatures, and SLSA provenance attestations, refer to [Deployment: Verifying images](deployment.md#verifying-images).

---

## Cost Controls

Invocations to Amazon Bedrock incur operational costs based on input and output tokens consumed.

### Operational Cost Factors

- **Non-Idempotent Inferences**: Study plan generation (`POST /api/v1/adaptive-plan`) is non-deterministic and not idempotent. Retried client requests trigger fresh model inferences and generate additional token consumption. Upstream client applications should implement exponential backoff and rate-limit client retries.
- **Client Disconnects**: Inbound HTTP processing does not terminate downstream Bedrock generation if the client drops the TCP connection before receiving a response; the task runs until `REQUEST_TIMEOUT` expires. Client timeouts should be configured conservatively to avoid terminating connections while work is completing.
- **Concurrency Caps**: Configure `LLM_MAX_CONCURRENCY` to control maximum parallel model invocations and prevent traffic spikes from generating unbounded token usage.
- **Aborted Calls**: Calls cut off by `LLM_TIMEOUT` or `REQUEST_TIMEOUT` close the connection, but AWS documents no billing rule for aborted Converse calls. Assume their tokens may be billed.

---

## Metrics to Watch

With `METRICS_ENABLED=true`, `/metrics` exposes the series listed in [Observability](architecture.md#observability). The ones these procedures rely on:

- `axiom_circuit_breaker_state`: 2 means open; plan requests fail fast with 503.
- `axiom_llm_calls_total{outcome="throttled"}` and `{outcome="unavailable"}`: upstream throttling and availability failures.
- `axiom_llm_inflight` against `LLM_MAX_CONCURRENCY`, with `axiom_llm_queue_wait_seconds` and `axiom_llm_queue_timeouts_total`: queue pressure.
- `http_server_requests_total{route="/api/v1/adaptive-plan",status="503"}`: unavailable responses seen by clients.
- `axiom_auth_previous_token_total`: callers still using the previous token during a rotation.
- `axiom_resource_links_checked_total{result="ok|status|timeout|refused|invalid"}`: tracks outbound resource validation when enabled via `RESOURCE_LINK_CHECK=true` (optionally configured with `RESOURCE_LINK_TIMEOUT`, default `3s`). Links are not fetched while disabled. Enabling checks can increase response latency within `REQUEST_TIMEOUT`. A failed link drops the unreachable resource rather than failing its topic or request; exhausting the request deadline returns HTTP 504 `gateway_timeout`.
