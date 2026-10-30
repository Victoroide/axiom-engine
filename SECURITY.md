# Security policy

## Supported versions

Security fixes are maintained on the `main` branch and the latest release tag. Older commits and unmerged forks are not supported.

## Reporting a vulnerability

Use GitHub private vulnerability reporting: open the repository's **Security** tab and select **Report a vulnerability**.

If private vulnerability reporting is unavailable, use the contact details on Victoroide's GitHub profile to arrange a private report. Do not report vulnerabilities through public issues, pull requests, or public discussions.

When submitting a report, include:
- Affected commit SHA or release tag.
- Detailed steps to reproduce the issue, including request payloads.
- Expected behavior and actual observed behavior.
- Assessment of potential security impact.
- Proposed remediation or patch, if available.

Use synthetic identifiers and test data. Redact credentials, API keys, AWS secret keys, and internal service tokens. Do not execute security testing against production deployments or infrastructure you do not own.

## Response targets

Victoroide aims to meet the following response targets:
- **Acknowledgement**: Within three business days.
- **Initial assessment**: Within seven business days.
- **Remediation plan**: Within 30 days of confirmation.

Remediation timing depends on severity and operational considerations. Critical issues may receive interim mitigations before a full release. Coordinate public disclosure timing after a fix or mitigation has been published.

## Scope notes and boundaries

### Service token authentication

- Every `/api/v1` route requires authentication by default. Public routes (`/livez`, `/readyz`, `/health`, `/swagger/*` and, when `METRICS_ENABLED=true`, `/metrics`) bypass token authentication.
- Callers may authenticate using the `X-Internal-Token` header or an `Authorization: Bearer <token>` header (scheme matching is case-insensitive).
- Token validation computes SHA-256 digests of the expected and provided tokens and evaluates them using `crypto/subtle.ConstantTimeCompare` (comparing two 32-byte digests) to prevent timing side-channel attacks.
- Service startup fails closed if `AXIOM_INTERNAL_TOKEN` is unset or empty, unless `AXIOM_AUTH_DISABLED=true` is explicitly configured; configuring both simultaneously fails startup (see [configuration reference](docs/configuration.md)). When disabled, the server logs a startup warning `auth.disabled`.
- Configured tokens must satisfy the format and length requirements defined in the [configuration reference](docs/configuration.md); the startup error message does not expose the token value.

### Deployment requirements

- Provision an internal token before starting the service. Generate high-entropy secrets using `openssl rand -hex 32`. The configuration loader enforces character and minimum length rules, not entropy. Keep authentication enabled for all exposed deployments.
- Docker Compose publishes the HTTP service port exclusively to loopback (`127.0.0.1:8080:8080`) by default.

### Input validation and prompt injection defenses

- Topics supplied to `POST /api/v1/adaptive-plan` (`failed_topics`) and `GET /api/v1/tutor/student/{student_id}/cognitive-graph` (`topics` and optional `target_topic`) are trimmed, non-empty, and deduplicated in order.
- Topic lists must contain at least 1 topic and at most `MAX_FAILED_TOPICS` unique topics (default: 10). Empty or excess topic lists return HTTP 400 `validation_error`.
- Topics are validated against the node taxonomy in the loaded curriculum graph. Unknown topics return HTTP 422 `validation_error` detailing the unknown topics.
- Student and course identifiers (`student_id` and `course_id`), when present, must match regex `^[A-Za-z0-9._:-]{1,128}$`. Non-matching identifiers return HTTP 400 `validation_error`.
- VARK learning styles are restricted to `visual`, `auditory`, `read_write`, and `kinesthetic`. Other values return HTTP 400 `validation_error`.
- BAML structured generation parses model outputs into typed data. Validated plan items receive graph-derived prerequisite chains and stable-sort by graph prerequisite rank. Hallucinated or unknown topics returned by the planner are dropped and logged as a warning (`stage.validation`) with `items_dropped` and `request_id`.
- Request bodies are limited to 64 KiB. Payloads exceeding this limit return HTTP 413 `payload_too_large`. Invalid JSON returns HTTP 400 `invalid_request_body`.
- For error response formats and status code mappings, see [API reference](docs/api.md).

### LLM output handling

- Model-generated study items and learning resources are validated and sanitized in `internal/service/output.go` before response assembly (see [response contract](docs/api.md#response-contract)).
- Client rendering duty: While the service filters unsafe URL schemes and removes control characters, client applications rendering returned titles and explanations must treat them as untrusted text and render them as plain text rather than unescaped HTML to prevent cross-site scripting (XSS) and DOM injection vulnerabilities. Escape URLs when placing them in HTML attributes; RFC 3986 sub-delimiters such as `'` are returned unencoded.

### Data handling

- Learner identifiers (`student_id` and `course_id`) remain within the service boundary and are never transmitted to upstream model providers. When supplied, they are validated against format constraints and echoed in the HTTP response directly from the incoming request payload.
- Learner identifiers are excluded from BAML per-topic arguments, Bedrock prompt instructions, and upstream output schemas. Upstream model requests receive only the graph context, target topic, topological prerequisite order, and VARK learning style profile (see [Prompt Inputs](docs/architecture.md#prompt-inputs)).

### Rate limiting and network boundaries

- An in-memory sliding window rate limiter restricts traffic per client IP address (`RATE_LIMIT_MAX`, default: 50 requests; `RATE_LIMIT_WINDOW`, default: 1 minute) across `/api/v1` routes. Exceeding this limit returns HTTP 429 `rate_limit_exceeded`. Public routes (`/livez`, `/readyz`, `/health`, `/swagger/*` and, when `METRICS_ENABLED=true`, `/metrics`) bypass rate limiting and do not consume quota.
- Proxy headers (`X-Forwarded-For`) are trusted only when connections originate from addresses listed in `TRUSTED_PROXIES`. Untrusted clients cannot bypass rate limiting by spoofing proxy headers.
- Inbound HTTP transport is bounded by server timeouts: ReadTimeout (10s), IdleTimeout (1m), and WriteTimeout (`REQUEST_TIMEOUT + 10s`, default 30s).
- Pipeline reasoning execution is bounded by `REQUEST_TIMEOUT` (default: 20s), and upstream model calls are bounded by `LLM_TIMEOUT` (default: 15s, where `LLM_TIMEOUT <= REQUEST_TIMEOUT`).
