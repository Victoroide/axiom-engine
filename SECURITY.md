# Security policy

## Supported versions

Security fixes are maintained on the `main` branch and the latest release tag. Older commits and unmerged forks are not supported.

## Reporting a vulnerability

Use GitHub private vulnerability reporting: [report a vulnerability](https://github.com/Victoroide/axiom-engine/security/advisories/new), or open the repository's **Security** tab and select **Report a vulnerability**.

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

- Every `/api/v1` route requires authentication by default. Public routes (`/livez`, `/readyz`, `/health`, `/swagger/*` (when `SWAGGER_ENABLED=true`, the default), and, when `METRICS_ENABLED=true`, `/metrics`) bypass token authentication.
- In shared mode, callers use `X-Internal-Token` or `Authorization: Bearer <token>`. The server compares the credential's SHA-256 digest against every configured shared token in constant time, including the optional previous token.
- In verified mode, the server accepts a single Bearer credential and checks its signature, access-token type, issuer, resource audience, time claims and configured identity claims against operator configuration. It checks the required route scope before IP limiting or replay. [Verified mode](docs/api.md#verified-mode)
- The HTTP server validates its mode and credentials before binding. Shared mode requires a current token. Verified mode requires valid issuer settings and empty shared-token settings. To disable authentication, set `AXIOM_AUTH_DISABLED=true` and leave the mode and all credential settings unset. [Configuration reference](docs/configuration.md#verified-http-authentication)

### Deployment requirements

- Keep authentication enabled for exposed deployments. In shared mode, provision a high-entropy secret using `openssl rand -hex 32`. In verified mode, provision issuer-signed access tokens with the API resource audience and the scopes each client needs.
- Docker Compose publishes the HTTP service port exclusively to loopback (`127.0.0.1:8080:8080`) by default.

### Deployment assumptions

- **Authorization boundary**. Shared credentials grant the same access to all holders. Verified mode uses signed tenant and caller claims to isolate replay and uses exact scopes to authorize operations. The calling platform decides which learners a caller may act for and enforces any tenant quotas.
- **Network protection**. Requests rejected for credentials or scopes consume no application rate-limit budget. Production deployments must enforce edge firewall filtering, gateway rate limiting or other network-level DDoS mitigation.
- **Trusted operator inputs**: The knowledge graph specified by `GRAPH_FILE` is trusted operator input. While topic identifiers are constrained to bounded, printable, and addressable strings, graph relation text directly populates LLM prompt context. Operators must verify graph file integrity and control access to configuration assets.

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
- With `RESOURCE_LINK_CHECK=true`, both HTTP plan routes and the stdio MCP `generate_study_plan` tool make outbound HTTP reachability checks on sanitized resource URLs. The dialer verifies DNS-resolved socket addresses at connection time and on every redirect, refusing loopback, private, link-local, multicast, unspecified, IPv6 unique local, IPv4-mapped forms of these addresses, and IANA special-purpose or translation ranges. Outbound requests permit only `http` and `https` on ports 80 and 443, cap redirects at three, send no cookies or credentials, and ignore proxy environment variables. Each checker shares eight concurrent checks across requests and includes queue wait in `RESOURCE_LINK_TIMEOUT`. The setting defaults to `false`.
- Client rendering duty: While the service filters unsafe URL schemes and removes control characters, client applications rendering returned titles and explanations must treat them as untrusted text and render them as plain text rather than unescaped HTML to prevent cross-site scripting (XSS) and DOM injection vulnerabilities. Escape URLs when placing them in HTML attributes; RFC 3986 sub-delimiters such as `'` are returned unencoded.

### Data handling

- Learner identifiers (`student_id` and `course_id`) remain within the service boundary and are never transmitted to upstream model providers. When supplied, they are validated against format constraints and echoed in the HTTP response directly from the incoming request payload.
- Learner identifiers are excluded from BAML per-topic arguments, Bedrock prompt instructions, and upstream output schemas. Upstream model requests receive only the graph context, target topic, topological prerequisite order, and VARK learning style profile (see [Prompt Inputs](docs/architecture.md#prompt-inputs)).
- Verified HTTP identities and bearer tokens stay within the authorization boundary. Verified replay uses a versioned SHA-256 digest over the length-framed issuer, tenant and caller. New replay records omit echoed learner and course identifiers. Access logs use route templates in every authentication mode.

### Rate limiting and network boundaries

- The HTTP sliding-window limiter restricts `/api/v1` traffic per client IP. `RATE_LIMIT_MAX` defaults to 50 requests and `RATE_LIMIT_WINDOW` defaults to 1 minute. IPv6 clients share a bucket by `/64` and IPv4-mapped IPv6 addresses use the IPv4 bucket. Counters live in memory per replica when `REDIS_URL` is unset. Replicas configured with the same Redis database share those counters. If Redis fails to supply a rate-limit decision, the request receives HTTP 503 `service_unavailable` with `Retry-After: 1` before reaching the handler.
- The server returns HTTP 401 for missing or invalid credentials and HTTP 403 for insufficient verified scopes before the limiter. Public probes, enabled Swagger and metrics, and allowed CORS preflight bypass the limiter. Stdio MCP uses the launching process as its trust boundary.
- Proxy headers (`X-Forwarded-For`) are trusted only when connections originate from addresses listed in `TRUSTED_PROXIES`. Untrusted clients cannot bypass rate limiting by spoofing proxy headers.
- Inbound HTTP transport is bounded by server timeouts: ReadTimeout (10s), IdleTimeout (1m), and WriteTimeout (`REQUEST_TIMEOUT + 10s`, default 30s).
- `REQUEST_TIMEOUT` (default `20s`) bounds the request's pipeline wait. `LLM_TIMEOUT` (default `15s`) bounds the wait for an individual planner result and must fit within `REQUEST_TIMEOUT`. After dispatch, the planner keeps its process-local concurrency permit until it returns or unwinds after a panic, even after cancellation or a deadline ends the wait.
- The reasoning service recovers a panic in a running planner call and the process continues serving. When the recovered panic determines the plan failure, the client receives `internal_error`. Cancellation, a deadline, or an earlier error from another topic can determine the response first. Even after a plan ends, recovery emits `planner.panic` at `ERROR` with `error_class="internal_error"`, `request_id` capped at 128 bytes, and trace and span IDs when available. The standard JSON line is at most 1024 bytes including the newline, omits the recovered value, and contains zero stack frames. A native BAML library initialization panic aborts startup before either transport is available. See [Planner Panics](docs/operations.md#planner-panics) and [Native Library Failures](docs/operations.md#native-library-failures).
