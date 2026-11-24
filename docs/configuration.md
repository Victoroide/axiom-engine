# Configuration

Process configuration is parsed at startup in `internal/config/config.go`. The service fails immediately during initialization if any variable violates format constraints, boundaries, or relational rules.

This document is the authoritative reference for all environment variables supported by AxiomEngine.

---

## Environment Variables

| Variable | Type | Default | Accepted Values | Effect |
|---|---|---|---|---|
| `PORT` | Integer | `8080` | `1` to `65535` | TCP port on which the HTTP server listens. Configured in `internal/config/config.go`. |
| `AXIOM_AUTH_DISABLED` | Boolean | `false` | `true` or `false` | When `true`, disables token authentication on `/api/v1/*` routes and logs a startup warning `auth.disabled`. Must be `false` when `AXIOM_INTERNAL_TOKEN` or `AXIOM_INTERNAL_TOKEN_PREVIOUS` is configured; configuring either simultaneously fails startup. When `false`, `AXIOM_INTERNAL_TOKEN` is required. Configured in `internal/config/config.go`. |
| `AXIOM_INTERNAL_TOKEN` | String | `""` (unset) | At least 32 visible ASCII characters (`0x21`–`0x7E`) without whitespace | Shared secret token required in `X-Internal-Token` or `Authorization: Bearer` on `/api/v1/*` routes. Required at startup unless `AXIOM_AUTH_DISABLED=true`; configuring both simultaneously fails startup. Non-empty tokens must satisfy length and character rules even when authentication is disabled. Tokens are not trimmed. Configured in `internal/config/config.go`. |
| `AXIOM_INTERNAL_TOKEN_PREVIOUS` | String | `""` (unset) | At least 32 visible ASCII characters (`0x21`–`0x7E`) without whitespace; must differ from `AXIOM_INTERNAL_TOKEN` | Optional previous secret token accepted in `X-Internal-Token` or `Authorization: Bearer` on `/api/v1/*` routes to support zero-downtime rotation (see [Token rotation](deployment.md#token-rotation)). Requires authentication to be enabled; must be empty when `AXIOM_AUTH_DISABLED=true`. Configured in `internal/config/config.go`. |
| `CORS_ALLOWED_ORIGINS` | String | `""` (unset) | `*` alone, or comma-separated list of `http://` or `https://` URLs | Configures allowed origins for cross-origin requests. URLs must not include paths, query parameters, or fragments. When unset or empty, CORS middleware is disabled. Configured in `internal/config/config.go`. |
| `RATE_LIMIT_MAX` | Integer | `50` | Positive integer (`> 0`) | Maximum number of authenticated `/api/v1` requests (every request when `AXIOM_AUTH_DISABLED=true`) allowed from a client IP within `RATE_LIMIT_WINDOW`. When `REDIS_URL` is unset, budgets are held in memory per process replica and are never shared across instances; IPv6 addresses in the same `/64` subnet share a single bucket (see [Rate Limiting](api.md#rate-limiting)). When `REDIS_URL` is configured, request quotas are tracked in Redis and shared across replicas. Exceeding requests receive HTTP 429 `rate_limit_exceeded`. Configured in `internal/config/config.go`. |
| `RATE_LIMIT_WINDOW` | Duration | `1m` | Positive Go duration string (e.g. `30s`, `1m`, `5m`) | Sliding window duration for IP rate limiting. When `REDIS_URL` is configured, Redis whole-second sliding windows enforce this duration across all replicas; when `REDIS_URL` is unset, an in-memory sliding window is evaluated per process replica. Configured in `internal/config/config.go`. |
| `IDEMPOTENCY_TTL` | Duration | `24h` | Non-negative Go duration string (`0s` disables) | Duration successful plan responses are retained for replay when an `Idempotency-Key` is supplied. `0s` disables plan replay and header handling. Response TTL starts on successful completion and does not time out a running reservation. Process-local bounded retention evicts expired entries before oldest entries. Configured in `internal/config/idempotency.go`. |
| `IDEMPOTENCY_MAX_ENTRIES` | Integer | `10000` | Positive integer (`> 0`) | Maximum number of plan responses retained in memory per process replica for idempotency replay. When capacity is reached, expired entries are evicted before oldest entries. Configured in `internal/config/idempotency.go`. |
| `REDIS_URL` | String | `""` (unset) | Valid `redis://` or `rediss://` URL | Optional shared request storage URL. When configured, sliding-window rate limit counters and adaptive plan idempotency records are stored in Redis under the `axiom:` prefix, sharing quotas and responses across replicas. Startup ping is bounded by 3 seconds, disables retries, and bounds individual runtime operations to 500 ms. Pending idempotency reservations are leased for `REQUEST_TIMEOUT + 3s`; successful responses are retained for `IDEMPOTENCY_TTL`. Connection error logs omit credentials and URLs. When unset, process-local in-memory storage is used. Configured in `internal/config/redis.go`. |
| `TRUSTED_PROXIES` | String | `""` (unset) | Comma-separated list of IPv4/IPv6 addresses or CIDRs | Proxies trusted by Fiber to extract the client IP from `X-Forwarded-For`. When unset, proxy headers are ignored. Configured in `internal/config/config.go`. |
| `REQUEST_TIMEOUT` | Duration | `20s` | Positive Go duration string | Total timeout deadline for servicing incoming HTTP requests in the reasoning pipeline. Must be greater than or equal to `LLM_TIMEOUT`. Configured in `internal/config/config.go`. |
| `LLM_PROVIDER` | String | `aws-bedrock` | `aws-bedrock`, `openai`, `anthropic`, `google-ai`, `ollama`, `openai-generic` | Selects the LLM provider for the reasoning planner. Configured in `internal/config/llm.go`. |
| `LLM_MODEL` | String | `amazon.nova-micro-v1:0` (`aws-bedrock` only) | Non-empty string without surrounding whitespace | Model identifier passed to the configured provider. Required for all providers other than `aws-bedrock`. Configured in `internal/config/llm.go`. |
| `LLM_BASE_URL` | String | `""` (except `http://localhost:11434/v1` for `ollama`) | Absolute HTTP or HTTPS URL without credentials, query, or fragment | Custom API base URL for `openai`, `ollama`, or `openai-generic`. Required for `openai-generic`, optional for `openai` and `ollama`, disallowed for other providers. Configured in `internal/config/llm.go`. |
| `OPENAI_API_KEY` | String | `""` (unset) | Non-empty string | API key for OpenAI model calls. Required when `LLM_PROVIDER=openai`. Configured in `internal/config/llm.go`. |
| `ANTHROPIC_API_KEY` | String | `""` (unset) | Non-empty string | API key for Anthropic model calls. Required when `LLM_PROVIDER=anthropic`. Configured in `internal/config/llm.go`. |
| `GOOGLE_API_KEY` | String | `""` (unset) | Non-empty string | API key for Google AI model calls. Required when `LLM_PROVIDER=google-ai`. Configured in `internal/config/llm.go`. |
| `LLM_API_KEY` | String | `""` (unset) | String | Optional API key sent to `openai-generic` endpoints. Configured in `internal/config/llm.go`. |
| `LLM_MAX_CONCURRENCY` | Integer | `16` | `1` to `1024` | Maximum simultaneous in-flight provider calls across the entire process. Configured in `internal/config/config.go`. |
| `LLM_MAX_CONTEXT_EDGES` | Integer | `256` | Positive integer | Maximum distinct tuples in the shared local neighborhood, including edges touching depth-two nodes. |
| `LLM_MAX_PREREQUISITES` | Integer | `12` | Positive integer | Maximum prerequisite entries sent per topic after excluding mastered prerequisites. |
| `LLM_MAX_TOPIC_INPUT_BYTES` | Integer | `16384` or `4480` for Ollama at `4096` context tokens | Integer at least `3094`; Ollama also applies its context ceiling | Maximum accounted input bytes for one topic. |
| `LLM_MAX_REQUEST_INPUT_BYTES` | Integer | `131072` | Integer at least `LLM_MAX_TOPIC_INPUT_BYTES` | Maximum combined accounted input bytes across topics in one plan request. |
| `LLM_OLLAMA_CONTEXT_TOKENS` | Integer | `4096` | Integer at least `2710` | Provisioned Ollama context used to bound the topic budget when `LLM_PROVIDER=ollama`. Set the same context on the Ollama server. |
| `LLM_QUEUE_TIMEOUT` | Duration | `min(2s, LLM_TIMEOUT / 2)` (`2s` with the default `LLM_TIMEOUT`) | Positive Go duration less than `LLM_TIMEOUT` when set | Maximum wait for a topic's concurrency permit. On queue timeout the JSON plan route returns HTTP 503 `service_unavailable` with `Retry-After: 1`. See [Queue Saturation](operations.md#queue-saturation). Configured in `internal/config/config.go`. |
| `LLM_TIMEOUT` | Duration | `15s` | Positive Go duration string | Maximum wait for an individual planner result after dispatch. Must fit within `REQUEST_TIMEOUT`. Configured in `internal/config/config.go`. |
| `MAX_FAILED_TOPICS` | Integer | `10` | Positive integer (`> 0`) | Maximum number of unique failed topics accepted in a single adaptive plan or cognitive graph request. Configured in `internal/config/config.go`. |
| `MAX_MASTERED_TOPICS` | Integer | `100` | Positive integer (`> 0`) | Maximum number of unique mastered topics accepted in a single adaptive plan or cognitive graph request. Configured in `internal/config/mastery.go`. |
| `PRESERVE_DISPLAY_JOINS` | Boolean | `false` | `true` or `false` | Preserves contextual joiners and truncates at extended grapheme boundaries in HTTP and MCP resource titles and explanations. Empty or unset uses the default. See the [response contract](api.md#response-contract). |
| `RESOURCE_LINK_CHECK` | Boolean | `false` | `true` or `false` | When `true`, enables outbound HTTP reachability checks on learning resource URLs before returning plans; invalid URLs, timeouts, non-2xx statuses, and refused destinations drop the resource. Accepts exactly `true` or `false`. Both `RESOURCE_LINK_CHECK` and `RESOURCE_LINK_TIMEOUT` are validated at startup even when checking is disabled. Configured in `internal/config/resource_links.go`. |
| `RESOURCE_LINK_TIMEOUT` | Duration | `3s` | Positive Go duration string | Timeout applied to each individual outbound resource link check (shared across HEAD and fallback GET attempts), bounded by the overall request deadline. Evaluated when `RESOURCE_LINK_CHECK=true`, but validated at startup regardless of checking state. Configured in `internal/config/resource_links.go`. |
| `TRACING_ENABLED` | Boolean | `false` | `true` or `false` | When `true`, enables OpenTelemetry tracing and exports traces over OTLP/HTTP protobuf to `OTEL_EXPORTER_OTLP_ENDPOINT` or `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`. When `false` (default), no generic OTLP exporter is started. Accepts exactly `true` or `false`; empty or unset defaults to `false`, and invalid values fail startup. Configured in `internal/config/tracing.go`. |
| `LANGSMITH_TRACING` | Boolean | `false` | `true` or `false` | When `true`, enables OpenTelemetry tracing and exports model traces over OTLP/HTTP protobuf to `LANGSMITH_ENDPOINT/otel/v1/traces`. Requires `LANGSMITH_API_KEY`. Accepts exactly `true` or `false`; empty or unset defaults to `false`, and invalid values fail startup. Configured in `internal/config/langsmith.go`. |
| `LANGSMITH_API_KEY` | String | `""` (unset) | Non-empty string | API key for LangSmith authentication sent in header `x-api-key`. Required when `LANGSMITH_TRACING=true`. Never logged. Configured in `internal/config/langsmith.go`. |
| `LANGSMITH_ENDPOINT` | String | `https://api.smith.langchain.com` | Absolute HTTPS URL (or HTTP loopback) | Base URL for LangSmith trace export. Trailing slashes are stripped and `/otel/v1/traces` is appended. Must be an absolute HTTPS URL without credentials, query, or fragment (HTTP permitted only for `localhost` or loopback IPs). Configured in `internal/config/langsmith.go`. |
| `LANGSMITH_PROJECT` | String | `""` (unset) | String | Optional LangSmith project name sent in header `Langsmith-Project`. When unset or empty, the header is omitted. Configured in `internal/config/langsmith.go`. |
| `TRACING_CAPTURE_CONTENT` | Boolean | `true` if `LANGSMITH_TRACING=true`, else `false` | `true` or `false` | Controls whether rendered model prompts, raw completions, and plan request input/output are captured on trace spans. Defaults to `true` when LangSmith is enabled and `false` otherwise; explicit values override this default. Values are capped at 32 KiB on UTF-8 boundaries. Configured in `internal/config/langsmith.go`. |
| `METRICS_ENABLED` | Boolean | `false` | `true` or `false` | When `true`, serves Prometheus metrics at `GET /metrics` without requiring authentication or consuming rate limit tokens. When `false` (default), `GET /metrics` returns HTTP 404 and collectors are not allocated. Accepts exactly `true` or `false`; empty or unset defaults to `false`, and invalid values fail startup. Restrict access at the network or proxy level. Configured in `internal/config/config.go`. |
| `GRAPH_FILE` | String | `""` (unset) | Path to readable JSON file within the [curriculum limits](knowledge-graph.md#curriculum-limits) | Custom curriculum knowledge graph file. When unset or empty, the service loads the embedded seed graph from `internal/graph/seed.json`. An optional larger computer science curriculum is available at `examples/graphs/computer-science.json` in source and bundled at `/usr/share/axiom-engine/graphs/computer-science.json` in the runtime container image. Configured in `internal/graph/loader.go`. |
| `LOG_LEVEL` | String | `info` | `debug`, `info`, `warn`, `error` (case-insensitive) | Minimum log level for structured JSON logging emitted to standard output via `log/slog`. Configured in `internal/config/config.go`. |
| `SHUTDOWN_DRAIN_DELAY` | Duration | `0s` | Non-negative Go duration string (≥ `0s`) | Duration to wait after receiving a shutdown signal before closing the TCP listener, during which `/readyz` returns HTTP 503 while the listener continues accepting traffic. Negative, malformed, or overflowing durations fail startup. Configured in `internal/config/config.go`. |
| `SHUTDOWN_TIMEOUT` | Duration | `max(REQUEST_TIMEOUT + 5s, 11s)` (`25s`) | Go duration of at least `11s` | Maximum duration allowed for draining in-flight HTTP connections during graceful shutdown on `SIGINT` or `SIGTERM`. Also bounds MCP shutdown, draining admitted tool calls before remaining work is canceled and the transport is closed. The HTTP drain delay precedes this window, and trace shutdown flushes within the remaining post-drain deadline. Must be at least `11s` in HTTP mode, so a client that holds an incomplete request until the 10-second read timeout cannot push shutdown past its budget. In HTTP mode, in-flight request work is cancelled 1s before this deadline to reserve time for responses. Configured in `internal/config/config.go` and `internal/config/mcp.go`. |
| `SWAGGER_ENABLED` | Boolean | `true` | `true` or `false` | When `true` (default), registers Swagger UI and OpenAPI documentation routes (`/swagger/*`, `/swagger/doc.json`) with a dedicated Content-Security-Policy header. When `false`, removes Swagger UI, specification, and asset routes so requests return HTTP 404 with the standard `not_found` error envelope. Accepts exactly `true` or `false`; empty or unset defaults to `true`, and invalid values fail startup. Configured in `internal/config/config.go`. |
| `AWS_REGION` | String | `""` | Valid AWS region identifier (e.g. `us-east-1`) | AWS region where Amazon Bedrock runtime calls are dispatched. Required when `LLM_PROVIDER=aws-bedrock` unless `AWS_DEFAULT_REGION`, the selected profile region, or instance metadata supplies one; the static check only confirms configured sources, not region availability. Evaluated by the underlying AWS SDK. Ignored for other providers. |
| `AWS_DEFAULT_REGION` | String | `""` (unset) | Valid AWS region identifier (e.g. `us-east-1`) | Fallback AWS region identifier evaluated when `AWS_REGION` is unset or empty. Evaluated by the underlying AWS SDK when `LLM_PROVIDER=aws-bedrock`. |
| `AWS_CONFIG_FILE` | String | `$HOME/.aws/config` | Filesystem path | Override path for the shared AWS configuration file. Used during region resolution when `LLM_PROVIDER=aws-bedrock`. |
| `AWS_ACCESS_KEY_ID` | String | `""` | IAM access key ID | Long-term IAM access key ID with `bedrock:InvokeModel` permissions. Used when `LLM_PROVIDER=aws-bedrock`. |
| `AWS_SECRET_ACCESS_KEY` | String | `""` | IAM secret access key | Secret access key corresponding to `AWS_ACCESS_KEY_ID`. Used when `LLM_PROVIDER=aws-bedrock`. |
| `AWS_SESSION_TOKEN` | String | `""` | STS session token string | Temporary session token when using assumed roles or temporary STS credentials. Used when `LLM_PROVIDER=aws-bedrock`. |
| `AWS_PROFILE` | String | `""` | Named profile string | Selects an AWS profile configured in `~/.aws/credentials` or `~/.aws/config`. Used when `LLM_PROVIDER=aws-bedrock`. |
| `AWS_MAX_ATTEMPTS` | Integer | `3` (SDK default) | `1` to `4294967295` | Maximum total attempts per Bedrock call, including the initial attempt. Validated before native calls begin when `LLM_PROVIDER=aws-bedrock`; explicitly empty or out-of-range values fail startup. Evaluated by the underlying AWS SDK. Ignored for other providers. |
| `AWS_RETRY_MODE` | String | `standard` | `standard`, `adaptive` | Retry classification and backoff mode evaluated by the underlying AWS SDK. Validated before native calls begin when `LLM_PROVIDER=aws-bedrock`; explicitly empty or unrecognized modes fail startup. In `adaptive` mode, backoff delays may accumulate up to `LLM_TIMEOUT`. Ignored for other providers. |
| `BAML_LOG` | String | `off` in archive launcher, container, Compose, and Make | Launcher accepts `off`, `error`, `warn`, `warning` (case-insensitive). The binary also accepts `info`, `debug`, and `trace`. | Controls native BAML logging independently of trace content capture. The archive launcher starts the binary with `off` for all accepted values and rejects other values before startup with exit status 1. On direct launches, the pinned native library uses INFO when the variable is unset or set to `warning`. Failed calls can expose prompts and responses even at `warn`. The binary emits a JSON `server.failed` record and exits 1 for empty, whitespace-padded, or unrecognized values. BAML may emit a native warning before the binary validates configuration. |
| `BAML_INTERNAL_LOG` | String | `off` in the archive launcher | Native Rust log filter, for example `off`, `debug`, or `trace` | The launcher sets this filter to `off` even when inherited from the shell. On direct launches, `debug` can expose completions and `trace` can expose rendered prompts on stderr. |
| `BAML_TRACE_FILE`, `BOUNDARY_API_KEY`, `BOUNDARY_SECRET` | String | Unset in the archive launcher | Native trace file path or native tracing credentials | The launcher removes these variables and case variants of the `SECRET` suffix before starting the binary. The native tracing publisher can write or upload prompt and response data on direct launches when enabled. |
| `BAML_LIBRARY_PATH` | String | `""` (`/app/libbaml_cffi.so` in container) | File path to dynamic shared library | Filesystem path to the compiled BAML CFFI engine binary. Set in `Dockerfile`. |
| `BAML_LIBRARY_DISABLE_DOWNLOAD` | Boolean | `false` (`true` in container) | `true` or `false` | When true, prevents the BAML runtime from attempting to download shared libraries from the internet at runtime. Set in `Dockerfile`. |

---

## Curriculum prompt budget

JSON, SSE and MCP use the configured provider's inclusive limits. Each topic includes the shared neighborhood, target, learning style and full prerequisite order after mastery filtering. Accounting includes UTF-8 bytes, JSON escaping, graph lines, numbered prerequisite lines and a 3,072-byte allowance for instructions, the output schema and chat framing. The smallest valid input uses 3,094 accounted bytes for one edge, a one-byte target and the `visual` learning style. Both HTTP and MCP reject lower caps at startup.

The general maximum is 16,384 accounted bytes per topic and 131,072 accounted bytes across the default limit of ten unique failed topics. The aggregate cap applies even when each topic fits individually. Requests that exceed a limit return `curriculum_context_too_large` before dispatch, with the graph and prerequisite orders kept intact for accepted requests.

For Ollama, the effective per-topic ceiling is 3,072 bytes plus the provisioned context in tokens minus 640 tokens for fixed text and framing and 2,048 for completion. The `mistral:v0.3` calibration measured 614 tokens for the fixed spans with framing. The remaining curriculum allowance budgets up to one token per accounted curriculum byte for this profile. At 4,096 context tokens, the default per-topic cap is 4,480 bytes and ten topics can account for up to 44,800 bytes together. The maximum-render fixture used 1,997 input tokens including framing and the leading token. With the full completion allowance it uses 4,045 of 4,096 tokens. Startup rejects topic caps above the calculated ceiling. Set `LLM_OLLAMA_CONTEXT_TOKENS` to the loaded context. For a different Ollama selection or template, verify input and completion fit in the provisioned context before setting the limits. [Ollama context settings](https://docs.ollama.com/context-length) · [Ollama Mistral v0.3 chat template](https://ollama.com/library/mistral:v0.3/blobs/1ff5b64b61b9) · [Pinned Mistral v0.3 tokenizer](https://huggingface.co/mistralai/Mistral-7B-Instruct-v0.3/blob/c170c70/tokenizer.json)

At the general per-topic cap the template can produce at most 15,466 rendered UTF-8 bytes. The fixed text is 2,167 bytes. Each edge or prerequisite line costs at least one more byte in accounting than in the render because the line break is escaped. A render with at most 12 such lines has at most 13,027 curriculum bytes, since each line is at most 1,042 bytes and the target and style add at most 523 bytes. With 13 or more lines, subtract at least 13 from the 13,312-byte curriculum allowance. A fixture with 13 edges and unescaped four-byte Unicode characters attains the resulting maximum. For the 4,480-byte Ollama cap, one edge suffices to attain the maximum of 3,574 rendered UTF-8 bytes.

The table shows separate escape-heavy fixtures at their accounted limits, with exactly one extra byte rejected before dispatch for each configuration. The JSON payload includes the provider envelope. Each dispatch requests up to 2,048 completion tokens. Fit depends on the configured context, tokenizer and chat template.

| Provider | Fixture selection | Accounted bytes | Rendered UTF-8 bytes | JSON payload bytes |
|---|---|---:|---:|---:|
| `aws-bedrock` | `amazon.nova-micro-v1:0` | 16,384 | 12,539 | 15,714 |
| `openai` | `gpt-4.1-mini` | 16,384 | 12,539 | 15,684 |
| `anthropic` | `claude-haiku-4-5` | 16,384 | 12,539 | 15,686 |
| `google-ai` | `gemini-2.5-flash` | 16,384 | 12,539 | 15,669 |
| `ollama` | `mistral:v0.3` | 4,480 | 3,281 | 3,778 |
| `openai-generic` | `meta-llama/Llama-3.1-8B-Instruct` | 16,384 | 12,539 | 15,679 |

The 12-entry prerequisite limit keeps the requested output to at most 13 study items by default. The [example curriculum](../examples/graphs/computer-science.json) has a longest path of 11 prerequisites, while a full order also includes distinct prerequisites from other branches. With the general byte limits, the default admits 113 of 130 single-topic requests without mastery and rejects 17. `Write Ahead Logging` has 13 prerequisites and `Sharding` has 24 prerequisites. Raising only `LLM_MAX_PREREQUISITES` to `24` admitted all 130 inputs in the fixture. Check completion length before raising the limit for production traffic.

## Tracing

Tracing is controlled independently for generic OTLP collectors (`TRACING_ENABLED`) and LangSmith (`LANGSMITH_TRACING`). When either variable is `true`, the service initializes the OpenTelemetry Go SDK. If both are `false` (the default), no tracer provider is registered, incurring zero telemetry export overhead.

### Generic OTLP Exporter

When `TRACING_ENABLED=true`, traces are exported over OTLP/HTTP protobuf to an external OpenTelemetry collector. Standard OpenTelemetry environment variables configure this exporter:

| Variable | Default | Description |
|---|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `http://localhost:4318` | Base URL for the generic OTLP/HTTP exporter. When neither endpoint variable is set, the exporter explicitly selects HTTP. The exporter appends `/v1/traces` automatically. |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | `""` (unset) | Overrides the complete trace export URL, including the path (for example `http://collector:4318/v1/traces`). |
| `OTEL_EXPORTER_OTLP_HEADERS` | `""` (unset) | Comma-separated list of key-value header pairs (`key=value,key2=value2`) sent with generic OTLP export requests. |
| `OTEL_EXPORTER_OTLP_TRACES_HEADERS` | `""` (unset) | Trace-specific header pairs replacing the entire `OTEL_EXPORTER_OTLP_HEADERS` map (does not extend it; users needing both sets must configure all headers here). |
| `OTEL_SERVICE_NAME` | `axiom-engine` | Service name attribute associated with all emitted trace spans. |
| `OTEL_RESOURCE_ATTRIBUTES` | `""` (unset) | Comma-separated key-value attributes attached to the OpenTelemetry resource. |
| `OTEL_TRACES_SAMPLER` | `parentbased_always_on` | Trace sampling strategy evaluated by the OpenTelemetry SDK. Supports standard sampler types (`always_on`, `always_off`, `traceidratio`, `parentbased_always_on`, `parentbased_always_off`, `parentbased_traceidratio`). |

Standard OpenTelemetry exporter timeout, TLS (`OTEL_EXPORTER_OTLP_INSECURE`, `OTEL_EXPORTER_OTLP_CERTIFICATE`), and compression (`OTEL_EXPORTER_OTLP_COMPRESSION`) settings are evaluated directly by the underlying OTel SDK. Refer to the [OpenTelemetry Specification](https://opentelemetry.io/docs/specs/otel/configuration/sdk-environment-variables/) for additional standard parameters.

### LangSmith Exporter

When `LANGSMITH_TRACING=true`, the service enables a dedicated OTLP/HTTP exporter sending traces directly to LangSmith:

- **`LANGSMITH_API_KEY`**: Required secret key sent in the `x-api-key` header. Startup fails if missing or whitespace-only. The key is never logged.
- **`LANGSMITH_ENDPOINT`**: Base URL for LangSmith trace ingestion (default `https://api.smith.langchain.com`). The exporter trims trailing slashes and appends `/otel/v1/traces` to the configured path. Must be an absolute HTTPS URL (HTTP is permitted only for loopback addresses).
- **`LANGSMITH_PROJECT`**: Optional project identifier sent in the `Langsmith-Project` header. When unset, the header is omitted.

### Exporter Isolation

The generic OTLP exporter and LangSmith exporter operate with strict isolation:
- Generic `OTEL_EXPORTER_OTLP_*` variables do not affect LangSmith's endpoint, headers, timeout (fixed 10s), or compression (disabled).
- `LANGSMITH_API_KEY` is sent exclusively to `LANGSMITH_ENDPOINT` and is never attached to generic OTLP headers or emitted to logs.
- When both `TRACING_ENABLED=true` and `LANGSMITH_TRACING=true` are configured, both exporters run concurrently under the shared OpenTelemetry SDK.

### Content Capture

Span content recording is controlled by `TRACING_CAPTURE_CONTENT`:
- When unset, it defaults to `true` if `LANGSMITH_TRACING=true`, and `false` otherwise.
- Explicitly setting `TRACING_CAPTURE_CONTENT=true` or `false` overrides the default.
- When enabled, spans capture rendered prompts (`gen_ai.prompt.N`), completions (`gen_ai.completion.0`), and privacy-filtered request inputs (`input.value`) and outputs (`output.value`), each capped at 32 KiB on UTF-8 boundaries.
- When disabled, trace trees record only structural spans, timing, error types, and token counts.

---

## Derived Transport Timeouts

Transport-level deadlines are derived from configuration in `internal/config/config.go` and enforced on the Fiber application (`internal/server/server.go`) without requiring separate environment variables:

- **Read Timeout**: `10s` (`fiber.Config.ReadTimeout`). Bounds reading of request headers and body.
- **Write Timeout**: Derived as `REQUEST_TIMEOUT + 10s` (default `30s`, `fiber.Config.WriteTimeout`). Bounds response delivery. Config loading rejects `REQUEST_TIMEOUT` values that would cause duration overflow.
- **Idle Timeout**: `1m` (`fiber.Config.IdleTimeout`). Bounds keep-alive connections.

---

## AWS Credential Resolution

When `LLM_PROVIDER=aws-bedrock`, Amazon Bedrock calls executed through BAML rely on the standard AWS credential provider chain. For detailed provider behavior and precedence, refer to the [AWS SDK Documentation](https://docs.aws.amazon.com/sdk-for-rust/latest/dg/credproviders.html). For non-Bedrock providers, no AWS credentials or SDK settings are required.

The runtime resolves credentials in the following standard order:
1. Environment variables (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, and optional `AWS_SESSION_TOKEN`).
2. Shared configuration and credentials files (`~/.aws/credentials` and `~/.aws/config` selected by `AWS_PROFILE`).
3. Web identity token (OIDC / IAM Roles for Service Accounts).
4. ECS / Container metadata credentials (`AWS_CONTAINER_CREDENTIALS_RELATIVE_URI` or `AWS_CONTAINER_CREDENTIALS_FULL_URI`).
5. EC2 Instance Profile (IMDSv2).

### Required IAM Permissions

The IAM principal executing AxiomEngine requires permissions to invoke Amazon Bedrock foundation models:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "BedrockInvokeModel",
      "Effect": "Allow",
      "Action": [
        "bedrock:InvokeModel"
      ],
      "Resource": "arn:aws:bedrock:*::foundation-model/amazon.nova-micro-v1:0"
    }
  ]
}
```

---

## Region Resolution

At startup, the service statically inspects configured AWS region sources without performing network probes or credential lookups. Sources are evaluated in the following precedence order:
1. Non-empty `AWS_REGION` environment variable.
2. Non-empty `AWS_DEFAULT_REGION` environment variable.
3. The `region` setting in the AWS configuration file specified by `AWS_CONFIG_FILE` (defaulting to `$HOME/.aws/config`), reading the `[profile <name>]` section selected by `AWS_PROFILE`. When `AWS_PROFILE` is unset or `default`, both `[default]` and `[profile default]` are accepted.

When parsing the configuration file, blank lines and whole-line comments starting with `#` or `;` are ignored. If `AWS_PROFILE` specifies a named profile that lacks a `region` setting, resolution does not fall back to `[default]`. Missing or unreadable configuration files yield no confirmed static source.

If no static region source is resolved, the native AWS SDK may still resolve a region from EC2 instance metadata (IMDS). When `LLM_PROVIDER=aws-bedrock`, the startup diagnostic emits a `WARN` record (`llm.region.unresolved`) rather than failing startup so that non-Bedrock operations (such as knowledge graph queries) remain available. An unreachable instance metadata service adds approximately 2 seconds of latency to each failing call. BAML caches the first successfully resolved region for the lifetime of the process; changing the region requires restarting the service. For non-Bedrock providers, static region inspection is bypassed.

---

## MCP Mode Configuration

When started with `axiom-server mcp`, the server runs in Model Context Protocol mode over standard I/O (`stdio`) instead of starting the HTTP server.

In MCP mode, runtime settings are loaded via `internal/config/mcp.go`:
- **Shared configuration**: `GRAPH_FILE`, `LOG_LEVEL`, `REQUEST_TIMEOUT`, `LLM_TIMEOUT`, `LLM_QUEUE_TIMEOUT`, `LLM_MAX_CONCURRENCY`, `MAX_FAILED_TOPICS`, `MAX_MASTERED_TOPICS`, `PRESERVE_DISPLAY_JOINS`, `RESOURCE_LINK_CHECK`, `RESOURCE_LINK_TIMEOUT`, and `SHUTDOWN_TIMEOUT` retain their standard default values and bounds.
- **Shutdown timeout**: `SHUTDOWN_TIMEOUT` also bounds MCP shutdown, defaulting to `max(REQUEST_TIMEOUT + 5s, 11s)` (`25s`). Admitted tool calls drain within this timeout before remaining work is canceled and the transport is closed. If explicitly configured, it must be a positive Go duration; unlike HTTP mode, the `11s` HTTP minimum is not enforced because there is no HTTP listener or connection draining.
- **Ignored HTTP settings**: The following HTTP-only variables are ignored and will not cause startup validation failures even if unset, empty, or configured with values that would be invalid for the HTTP server: `REDIS_URL`, `IDEMPOTENCY_TTL`, `IDEMPOTENCY_MAX_ENTRIES`, `PORT`, `AXIOM_AUTH_DISABLED`, `AXIOM_INTERNAL_TOKEN`, `AXIOM_INTERNAL_TOKEN_PREVIOUS`, `CORS_ALLOWED_ORIGINS`, `TRUSTED_PROXIES`, `RATE_LIMIT_MAX`, `RATE_LIMIT_WINDOW`, `METRICS_ENABLED`, `SWAGGER_ENABLED`, and `SHUTDOWN_DRAIN_DELAY`.
- **Provider selection**: `LLM_PROVIDER`, `LLM_MODEL`, `LLM_BASE_URL` and provider API keys have the same validation and defaults as HTTP; the selected client registry is shared by all plan tools. Startup records on stderr include `llm_provider` and `llm_model`.
- **Validation**: AWS retry mode (`AWS_RETRY_MODE`, `AWS_MAX_ATTEMPTS`) is validated only for `aws-bedrock`; BAML logging (`BAML_LOG`) is always validated.
- **Tracing**: `TRACING_ENABLED` and standard `OTEL_*` settings apply to MCP tool-call spans. EOF and signal shutdown flush the provider; signal shutdown uses the remaining `SHUTDOWN_TIMEOUT` after tool draining and transport closure.
- **LangSmith and content**: `LANGSMITH_TRACING`, endpoint, project, key and `TRACING_CAPTURE_CONTENT` settings apply to MCP just as HTTP. Model-bearing tool traces receive run attributes; captured plan input/output omits learner identifiers. No MCP-specific environment variables are introduced.
