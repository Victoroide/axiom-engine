# API Reference

AxiomEngine provides HTTP endpoints for generating adaptive study plans, visualizing student cognitive states, inspecting API schemas, and verifying process health.

When `SWAGGER_ENABLED=true` (the default), interactive documentation is available at `/swagger/index.html` and the specification is served at `/swagger/doc.json`. Machine-readable operation schemas are maintained in [docs/swagger.yaml](swagger.yaml).

---

## Authentication

API routes use shared-token authentication by default. Set `AXIOM_AUTH_MODE=verified` for signed access tokens. With the mode unset, `AXIOM_AUTH_DISABLED=true` makes API routes public and emits the startup warning `auth.disabled`. The server validates these settings before binding. [Configuration reference](configuration.md#verified-http-authentication)

### Shared mode

Callers may supply the internal token using either of two headers:
- `X-Internal-Token: <token>`
- `Authorization: Bearer <token>`

During rotation the service also accepts the token configured in `AXIOM_INTERNAL_TOKEN_PREVIOUS` (see [Token rotation](deployment.md#token-rotation)). The presented token is compared with every configured token.

Header verification checks `X-Internal-Token` first, then falls back to `Authorization: Bearer <token>` with a case-insensitive `Bearer` scheme check. Verification computes SHA-256 digests and performs a constant-time comparison (`crypto/subtle.ConstantTimeCompare`) in `internalTokenMiddleware` (`internal/server/server.go`). Requests that omit the token or provide a mismatched value receive HTTP 401 Unauthorized:

```json
{
  "error": "unauthorized",
  "details": "missing or invalid internal token"
}
```

### Verified mode

Send exactly one `Authorization: Bearer <access-token>` header. The service returns HTTP 401 `unauthorized` with a Bearer challenge for missing, invalid, duplicate, combined or conflicting credentials, including any `X-Internal-Token` header. It takes tenant, caller and scopes from the verified claims and ignores caller-supplied identity headers. [Bearer challenges](https://www.rfc-editor.org/rfc/rfc6750.html#section-3)

| Required scope | Operations |
|---|---|
| `graph:read` | `GET` and `HEAD` for `/api/v1/topics`, `/api/v1/topics/{topic}/path` and `/api/v1/tutor/student/{student_id}/cognitive-graph` |
| `plan:generate` | `POST /api/v1/adaptive-plan` and `POST /api/v1/adaptive-plan/stream` |

Scope matching is exact and case-sensitive. A valid token that lacks the required scope receives HTTP 403 `insufficient_scope` and a Bearer challenge naming that scope. The server rejects credentials or scopes before charging the IP budget, checking replay or opening an event stream. The calling platform authorizes access to each learner.

A received `X-Internal-Token` trailer also counts as a conflicting credential. If verification ends when the client disconnects, the server handles it as HTTP 408 `request_timeout`. If shutdown cancels verification, it returns HTTP 503 `service_unavailable` with `Retry-After: 1`.

In Swagger UI, use `InternalToken` with the shared-token header or enter the full `Bearer <token>` value under `BearerAuth`. Verified mode uses `BearerAuth` alone.

The `/livez`, `/readyz` and `/health` routes, enabled Swagger and metrics routes, and CORS preflight for allowed origins bypass authentication.

---

## Rate Limiting

API routes use a sliding window keyed by client IP with a default budget of 50 requests per minute. With `REDIS_URL`, Redis tracks whole-second windows atomically using server time. Otherwise each replica tracks its own window. The server checks credentials and any required scopes before the limiter. Limit unauthenticated traffic at a reverse proxy or WAF if needed. [Configuration reference](configuration.md#environment-variables)

Rate limits are evaluated per process replica by default. When `REDIS_URL` is configured, rate-limit buckets are stored in Redis under key prefix `axiom:rate:` and shared across all replicas (see [Replicas and shared storage](deployment.md#replicas-and-shared-storage)). If Redis becomes unavailable at runtime, the limiter fails closed: requests return HTTP 503 `service_unavailable` with `Retry-After: 1`.

When `REDIS_URL` is unset, rate limits are evaluated per process replica and budgets are never shared across instances:
- **IPv4**: Individual IPv4 addresses maintain separate rate-limit buckets. IPv4-mapped IPv6 addresses (`::ffff:x.x.x.x`) are unmapped and share the canonical IPv4 bucket.
- **IPv6**: Client IPv6 addresses are masked to their `/64` subnet prefix, grouping callers within the same `/64` subnet into a shared bucket.
- **Reverse proxies**: Client IP addresses are resolved from `X-Forwarded-For` only when the remote peer is recognized as a trusted proxy via `TRUSTED_PROXIES`. Untrusted proxy headers are ignored.

When a client exceeds `RATE_LIMIT_MAX` requests within `RATE_LIMIT_WINDOW`, the server returns HTTP 429 Too Many Requests:

```json
{
  "error": "rate_limit_exceeded",
  "details": "too many requests"
}
```

Responses that pass IP admission carry `X-RateLimit-Limit`, `X-RateLimit-Remaining` and `X-RateLimit-Reset`. Both kinds of HTTP 429 response carry `Retry-After` in seconds. The server returns HTTP 401 and 403 before admission.

With [shared tenant quotas](configuration.md#shared-tenant-quotas) enabled, each admitted graph read, JSON plan attempt (including replay), or SSE plan request consumes one tenant unit. Each stream or multi-topic plan consumes a single unit. An IP rejection consumes zero tenant units. A tenant rejection retains the IP charge and IP headers. Once admitted, client errors, cancellation and provider failures retain their charges. Tenant exhaustion returns the same HTTP 429 envelope. Its positive `Retry-After` is the time to the next whole-second window boundary, where the previous window can still contribute to the budget. A tenant storage failure returns HTTP 503 with `Retry-After: 1` and the admitted IP headers.

Public health probes, Swagger documentation, enabled Prometheus metrics and CORS preflight bypass both budgets.

---

## Common Headers

- `X-Request-ID`: Client-provided correlation identifier. The server accepts and echoes client-supplied values matching `^[A-Za-z0-9._:-]{1,128}$`. Missing or invalid values—including identifiers exceeding 128 characters, containing whitespace, punctuation outside that character set, or non-ASCII characters—are replaced by a generated 43-character URL-safe random token when request-ID middleware runs. Responses produced by the application, including 400, 404, 405, 413, 431 and 501 errors, echo the resolved `X-Request-ID`, and access and error logs use the same value. Requests whose method the router does not implement (for example `PROPFIND`) return HTTP 501 with `Content-Type: application/json; charset=utf-8`, the body `{"error":"not_implemented","details":"Not Implemented"}`, `X-Request-ID`, and the same security headers as other responses (such as `X-Content-Type-Options: nosniff` and `X-Frame-Options: SAMEORIGIN`); valid client IDs are echoed, otherwise a fallback UUID is generated.
- `Content-Type`: Required for requests with a payload. Endpoints expecting JSON require a media type of `application/json` parsed via MIME media type rules; optional parameters such as `charset=utf-8` are accepted. Missing, malformed, or non-JSON media types return HTTP 415 `unsupported_media_type` with details `Content-Type must be application/json`. Standard API responses return `application/json; charset=utf-8`; accepted streaming requests (`POST /api/v1/adaptive-plan/stream`) return `text/event-stream`, while errors occurring before streaming begins return standard `application/json; charset=utf-8` error envelopes.
- `Idempotency-Key`: Optional client request header on `POST /api/v1/adaptive-plan` containing 1 to 255 visible ASCII characters (`0x21`–`0x7E`) enabling idempotent execution and response replay.
- `Idempotent-Replayed`: Response header with value `true` present when an adaptive plan response is served from the idempotency cache rather than newly generated.

---

## Error Envelope

All non-2xx responses adhere to the standard `domain.ErrorResponse` envelope:

```json
{
  "error": "error_code",
  "details": "human-readable description"
}
```

The `details` field provides context when available and is omitted when empty. Unknown topic values echoed in validation error details are quoted using Go string escaping (`strconv.Quote`), which escapes non-printable characters, format characters, and invalid bytes while leaving printable Unicode intact. Each echoed value is capped to a 128-rune prefix, followed by `…` after the closing quote if truncated. Validation messages display at most ten unknown topics, appending ` (+N more)` when more remain. The same quoting and 128-rune boundary apply to unknown `target_topic` error details. All HTTP 503 `service_unavailable` responses include an integer `Retry-After` header indicating the backoff duration in seconds.

### Error Codes and HTTP Statuses

| HTTP Status | Error Code (`error`) | Description | Trigger |
|---|---|---|---|
| `400 Bad Request` | `invalid_request_body` | Request body fails JSON parsing or strict schema decoding | Malformed JSON, non-object body, trailing tokens, unknown or duplicate fields, case mismatches, or null array elements |
| `400 Bad Request` | `invalid_idempotency_key` | Invalid `Idempotency-Key` header | Key contains non-visible characters, whitespace, or is outside 1–255 characters |
| `400 Bad Request` | `validation_error` | Request payload fails schema validation | Missing required fields, invalid VARK profile, too many topics, or contradictory topic history |
| `400 Bad Request` | `bad_request` | Generic client error | Malformed HTTP request, query parameters, or path encoding |
| `401 Unauthorized` | `unauthorized` | Token authentication failure | Missing or invalid credentials for the configured mode. Verified mode also rejects duplicate, combined or conflicting headers. |
| `403 Forbidden` | `insufficient_scope` | The verified caller lacks the required scope | A valid access token omits the required `graph:read` or `plan:generate` scope. |
| `404 Not Found` | `not_found` | Resource does not exist | Unmatched route path |
| `405 Method Not Allowed` | `method_not_allowed` | HTTP method not supported | Unsupported HTTP verb on an existing path |
| `408 Request Timeout` | `request_timeout` | Request read deadline exceeded | Request not read within the 10 s read timeout |
| `409 Conflict` | `idempotency_key_in_progress` | Concurrent request with same idempotency key | A request with this `Idempotency-Key` is currently in progress (`Retry-After: 1`) |
| `413 Payload Too Large` | `payload_too_large` | Request body exceeds limit | Inbound payload exceeds the 64 KiB (65,536 bytes) limit |
| `415 Unsupported Media Type` | `unsupported_media_type` | Media type not supported | Missing, malformed, unsupported charset, or non-JSON `Content-Type` header on `POST /api/v1/adaptive-plan` requests |
| `422 Unprocessable Entity` | `validation_error` | Concept not found in knowledge graph | One or more topics in `failed_topics`, `mastered_topics`, `topics`, `mastered`, or `target_topic` do not exist, or unresolvable topic in `GET /api/v1/topics/{topic}/path` |
| `422 Unprocessable Entity` | `curriculum_context_too_large` | Curriculum context exceeds the prompt budget | The complete neighborhood or prerequisite order exceeds an edge, entry, per-topic byte or request byte limit |
| `422 Unprocessable Entity` | `idempotency_key_mismatch` | Idempotency key reused with different payload | The same `Idempotency-Key` was submitted with a different request body fingerprint |
| `429 Too Many Requests` | `rate_limit_exceeded` | Request rate limit exceeded | The IP budget or the enabled tenant budget is exhausted |
| `431 Request Header Fields Too Large` | `request_header_fields_too_large` | Request header fields exceed limit | Inbound request headers exceed the server header limit |
| `500 Internal Server Error` | `internal_error` | Unhandled server error | Unexpected internal failure or recovered panic |
| `501 Not Implemented` | `not_implemented` | HTTP method not implemented | Method outside the router's configured method set, such as `PROPFIND` |
| `502 Bad Gateway` | `bad_gateway` | Upstream model failure or missing topic item | Amazon Bedrock invocation error, unparseable model response, or no usable item returned for a requested topic |
| `503 Service Unavailable` | `service_unavailable` | Circuit breaker open | Upstream planner circuit tripped due to consecutive failures (`Retry-After: 15`, details `reasoning temporarily unavailable`; `Retry-After: 1` while the circuit is half-open and its probe requests are in flight) |
| `503 Service Unavailable` | `service_unavailable` | Concurrency limit reached | No upstream slot became free within `LLM_QUEUE_TIMEOUT` (`Retry-After: 1`, details `reasoning temporarily unavailable`) |
| `503 Service Unavailable` | `service_unavailable` | Upstream throttling | Upstream capacity limit or rate throttling (`Retry-After: 5`, details `upstream throttled`) |
| `503 Service Unavailable` | `service_unavailable` | Server shutting down | In-flight request canceled during graceful shutdown (`Retry-After: 1`, details `server shutting down`) |
| `504 Gateway Timeout` | `gateway_timeout` | Processing deadline exceeded | Pipeline execution exceeded `REQUEST_TIMEOUT` or `LLM_TIMEOUT` |

404, 405 and 501 can occur on any path. Swagger 2.0 attaches responses only to declared operations, so these three appear here but not in `docs/swagger.yaml`.

The opt-in HTTP/1 listener can reject headers before the application runs. Native outcomes include plain-text 400 and 431, an empty 417 for an unsupported `Expect` value, and a connection close when incomplete headers reach the read deadline. Body read errors use the application JSON envelope. See [HTTP Listener](configuration.md#http-listener).

---

## Endpoints

### GET /livez

Returns process liveness status, including during shutdown draining. Does not require authentication or evaluate downstream dependencies.

- **Method**: `GET`
- **Path**: `/livez`
- **Headers**: None required
- **Query Parameters**: None
- **Request Body**: None

#### Responses

- **200 OK**: Process is alive and operational.
- **400 Bad Request**: Malformed HTTP request.
- **408 Request Timeout**: Request not read within the 10 s read timeout.
- **413 Payload Too Large**: Request body exceeds 64 KiB.
- **431 Request Header Fields Too Large**: Inbound request headers exceed the server limit.

#### Example

```sh
curl -i http://localhost:8080/livez
```

Response:

```http
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8
X-Request-ID: wBCIxn6xjS9w_vQuSjlaA0USEVwvW0UpBwrpQiyQh4c

{"status":"ok"}
```

---

### GET /readyz

Returns local process readiness without authorization or upstream calls. During shutdown draining, returns HTTP 503 with `{"status":"draining"}`.

- **Method**: `GET`
- **Path**: `/readyz`
- **Headers**: None required
- **Query Parameters**: None
- **Request Body**: None

#### Responses

- **200 OK**: Process is ready to receive traffic.
- **400 Bad Request**: Malformed HTTP request.
- **408 Request Timeout**: Request not read within the 10 s read timeout.
- **413 Payload Too Large**: Request body exceeds 64 KiB.
- **431 Request Header Fields Too Large**: Inbound request headers exceed the server limit.
- **503 Service Unavailable**: During shutdown draining, returns HTTP 503 with `{"status":"draining"}`.

#### Examples

Normal response:

```sh
curl -i http://localhost:8080/readyz
```

```http
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8
X-Request-ID: wBCIxn6xjS9w_vQuSjlaA0USEVwvW0UpBwrpQiyQh4c

{"status":"ok"}
```

Shutdown draining response:

```http
HTTP/1.1 503 Service Unavailable
Content-Type: application/json; charset=utf-8
X-Request-ID: wBCIxn6xjS9w_vQuSjlaA0USEVwvW0UpBwrpQiyQh4c

{"status":"draining"}
```

---

### GET /health

Alias of `/livez` for backwards compatibility. Returns process liveness without requiring authorization.

- **Method**: `GET`
- **Path**: `/health`
- **Headers**: None required
- **Query Parameters**: None
- **Request Body**: None

#### Responses

- **200 OK**: Process is alive and operational.
- **400 Bad Request**: Malformed HTTP request.
- **408 Request Timeout**: Request not read within the 10 s read timeout.
- **413 Payload Too Large**: Request body exceeds 64 KiB.
- **431 Request Header Fields Too Large**: Inbound request headers exceed the server limit.

#### Example

```sh
curl -i http://localhost:8080/health
```

Response:

```http
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8
X-Request-ID: wBCIxn6xjS9w_vQuSjlaA0USEVwvW0UpBwrpQiyQh4c

{"status":"ok"}
```

---

### POST /api/v1/adaptive-plan

Generates a personalized, prerequisite-ordered study plan for a student based on failed assessment topics and learning modality.

- **Method**: `POST`
- **Path**: `/api/v1/adaptive-plan`
- **Headers**:
  - `Content-Type: application/json` (optional parameters such as `charset=utf-8` allowed)
  - [Credential for the configured authentication mode](#authentication)
  - `Idempotency-Key: <key>` optionally enables replay of successful responses for `IDEMPOTENCY_TTL` (default `24h`). With verified tokens, the server checks `plan:generate` on every request and uses a versioned digest of the issuer, tenant and caller. A replacement token for the same principal can replay an existing response if it has the current scope. Shared mode uses the presented credential digest in either supported header. Disabled authentication uses one anonymous namespace. Each mode uses its own entries in memory or Redis.
  - Repeat requests in the same namespace with the same key and exact body bytes replay the original status, body and `Content-Type` with `Idempotent-Replayed: true`, fresh `X-Request-ID` and fresh rate-limit headers. A body mismatch returns HTTP 422 `idempotency_key_mismatch`. A matching request in progress returns HTTP 409 `idempotency_key_in_progress` with `Retry-After: 1`. Only HTTP 200 responses are cached. Response TTL starts on completion. CORS allows `Idempotency-Key` and exposes `Idempotent-Replayed`.
  - New replay records store the plan items and metadata. The server restores `student_id` and `course_id` from the authorized retry after matching its body fingerprint. Earlier Redis records retain their original response fields until their existing TTL expires.
- **Validation Rules**:
  - Strict JSON: Request body must be a single JSON object matching the schema. Missing or invalid JSON syntax (`request body must be valid JSON`), non-object bodies (`request body must be a JSON object`), trailing data (`request body must contain exactly one JSON value`), schema type mismatches and out-of-range numbers (`request body must match the JSON schema`), unknown fields (`unknown field "KEY"`), duplicate keys at any depth (`duplicate field "KEY"`), and case mismatches return HTTP 400 `invalid_request_body`. Field names in error details are bounded to 64 runes plus an ellipsis. If a `charset` parameter is present on `Content-Type`, only case-insensitive `utf-8` is accepted; other or empty charsets return HTTP 415.
  - `Idempotency-Key`: Optional. When provided, must contain between 1 and 255 visible ASCII characters (`0x21`–`0x7E`) without surrounding or embedded whitespace. Invalid keys return HTTP 400 `invalid_idempotency_key` with details `Idempotency-Key must contain 1 to 255 visible ASCII characters`.
  - `student_id`: Optional. If provided, must match regex `^[A-Za-z0-9._:-]{1,128}$`.
  - `course_id`: Optional. If provided, must match regex `^[A-Za-z0-9._:-]{1,128}$`.
  - `failed_topics`: Required. Array of strings. Must contain at least 1 and at most `MAX_FAILED_TOPICS` (default 10) unique items. Elements must not be null (`failed_topics must not contain null elements`). Topics resolve canonically across case, whitespace, and Unicode composition differences (see [Node Naming](knowledge-graph.md#node-naming)). Inbound topics are deduplicated by canonical form while preserving request order before validating the `MAX_FAILED_TOPICS` limit. Responses return authoritative stored node names. Upstream model recommendations are also matched canonically, deduplicated, and mapped to authoritative stored graph node names. Topics that do not resolve to a graph node return HTTP 422 `validation_error`.
  - `mastered_topics`: Optional. Array of strings. Lists concepts already mastered by the learner. Must contain at most `MAX_MASTERED_TOPICS` (default 100) unique items. Elements must not be null (`mastered_topics must not contain null elements`). Topics resolve canonically across case, whitespace, and Unicode composition differences and are deduplicated by canonical form while preserving request order before validating the `MAX_MASTERED_TOPICS` limit. Topics that do not resolve to a graph node return HTTP 422 `validation_error`. If any topic appears in both `failed_topics` and `mastered_topics`, the request returns HTTP 400 `validation_error` (`a topic must not be both failed and mastered: "TOPIC"`). Exceeding `MAX_MASTERED_TOPICS` returns HTTP 400 `validation_error`. Mastered prerequisites are excluded from the prerequisite order passed upstream to the model and from returned items; requested failed topics always remain. Returned items retain their full graph prerequisite chain (`prerequisite_chain`).
  - `vark_profile`: Required. String enum. Must be one of `visual`, `auditory`, `read_write`, or `kinesthetic`.
  - `telemetry`: Optional object containing client telemetry (`session_id`, `timestamp_unix`, `duration_ms`, `client_version`). Unknown nested fields are rejected.

#### Responses

- **200 OK**: Successfully generated adaptive plan. Every requested canonical topic is guaranteed to retain a study item. Response metadata (`_meta`) reports execution statistics: `items_after_validation` counts study items retained within the requested topics and their prerequisite closure, while `items_dropped` counts distinct items outside this closure, absent from the graph, or matching mastered topics removed during validation (also emitted in `stage.validation` warning logs). `items_skipped_mastered` counts distinct mastered prerequisites across the requested topics' prerequisite closures omitted from study; it is omitted when zero. Duplicate items removed during merging are not counted.
- **400 Bad Request**: Malformed body, missing required fields, contradictory topic history, identifier syntax error, or invalid `Idempotency-Key` header (`invalid_idempotency_key`).
- **401 Unauthorized**: Missing or invalid authentication token.
- **403 Forbidden**. A verified token lacks `plan:generate`.
- **408 Request Timeout**: Request not read within the 10 s read timeout.
- **409 Conflict**: A request with this `Idempotency-Key` is currently in progress (`idempotency_key_in_progress`, `Retry-After: 1`).
- **413 Payload Too Large**: Payload exceeds 64 KiB.
- **415 Unsupported Media Type**: Request `Content-Type` is missing, malformed, or not `application/json`.
- **422 Unprocessable Entity**: One or more topics are not found in the knowledge graph (`validation_error`), or `Idempotency-Key` was used with a different request body (`idempotency_key_mismatch`).
- **422 `curriculum_context_too_large`** returns `{"error":"curriculum_context_too_large","details":"curriculum context exceeds the prompt budget"}` before any topic is dispatched. See [Curriculum prompt budget](configuration.md#curriculum-prompt-budget).
- **429 Too Many Requests**: Rate limit exceeded.
- **431 Request Header Fields Too Large**: Inbound request headers exceed the server limit.
- **500 Internal Server Error**: Unexpected internal failure or recovered panic.
- **502 Bad Gateway**: details `model returned no usable item` when the model reply cannot be parsed or has no item for a requested topic; `upstream reasoning failed` for other Bedrock failures.
- **503 Service Unavailable**: Shared rate limit or idempotency storage unavailable (`service_unavailable`, `Retry-After: 1`), upstream circuit breaker open, concurrency limit reached, upstream throttling, or server shutdown.
- **504 Gateway Timeout**: Request exceeded pipeline deadline.

With `HTTP_LISTENER=net-http`, the server cancels reasoning when it observes an HTTP/1 connection close or an HTTP/2 stream reset. On Linux and macOS the server also detects an HTTP/1 close with pipelined requests waiting. A caller that closes only the TCP write side may still read HTTP 408 with `request_timeout` and details `request canceled`. The commit point for an idempotent success is the final cancellation check after a validated HTTP 200 has been prepared. Cancellation observed before that check releases the pending reservation. After the check, the server attempts publication with a one-second storage context, so a successful commit can be replayed even if the original caller disconnected before receiving it. Cleanup can release only the pending reservation owned by that request.

#### Response Contract

Adaptive plan items and recommended learning resources adhere to an output validation and sanitization policy:

- **Resource URLs**: Surrounding whitespace is trimmed. A URL must be absolute `http` or `https`, without userinfo or Unicode control or format characters, and its host must be an IP address or an ASCII DNS name (internationalized names must be given as `xn--` A-labels). The service returns the URL re-encoded: characters that are not allowed in a path, query or fragment are percent-encoded (for example `"` becomes `%22`, `<` becomes `%3C` and a space becomes `%20`), and existing `%XX` escapes are kept. The 2048-character limit applies to the re-encoded URL, so a long path with many non-ASCII characters can be dropped. Resources whose URL fails any rule are dropped.
- **Resource Types**: Normalized to lowercase and restricted to modalities matching the student's `vark_profile`:
  - `visual`: `video`, `diagram`, `infographic`
  - `auditory`: `podcast`, `lecture`, `audio-course`
  - `read_write`: `article`, `textbook`, `documentation`
  - `kinesthetic`: `exercise`, `lab`, `coding-challenge`
  Resources with mismatched types or unrecognized values are dropped.
- **Resource Count**: Each plan item contains between 0 and 3 learning resources (`maxItems: 3`). When more than 3 valid resources are returned for an item, only the first 3 are retained and excess resources are dropped. Discarded resources emit a `stage.output` warning log record with `request_id` and `resources_dropped`.
- **Resource Link Reachability.** When enabled (`RESOURCE_LINK_CHECK=true`), the service checks each distinct sanitized URL once per plan request before graph validation. It tries `HEAD` first and falls back to `GET` with `Range: bytes=0-0` on HTTP 405 or 501. A final HTTP 2xx response retains every occurrence in its original order. Other statuses, timeouts (including while queued), connection refusals, excess redirects (>3), and invalid or non-public destinations drop every occurrence. Items remain usable with an empty `resources` list. The optional `_meta.resources_unreachable` field counts individual occurrences removed by reachability checks and is omitted when zero. Output-policy removals are counted separately. The default (`RESOURCE_LINK_CHECK=false`) makes no outbound requests and preserves the response bytes.
- **Titles and explanations.** Whitespace runs become one space and leading and trailing whitespace is removed. Invalid UTF-8 bytes become U+FFFD. Titles are limited to 200 Unicode code points and explanations to 1,000. The default strips other controls and format characters (categories `Cc` and `Cf`) and truncates at code-point boundaries.
- **Opt-in display joins.** Set `PRESERVE_DISPLAY_JOINS=true` to preserve the contextual joiners below. The service removes other controls and format characters, including bidi controls, and returns the longest prefix of complete extended grapheme clusters within the same code-point limit that ends in neither a space nor a ZWNJ. If no eligible cluster fits, the field is the empty string. A title or explanation that cleans to empty is returned as an empty string, and the item or resource still follows the other validation rules. The implementation uses Unicode 17.0.0 data. [Unicode Text Segmentation](https://www.unicode.org/reports/tr29/tr29-47.html)
  - U+200C (ZWNJ) is kept between an Arabic-script left- or dual-joining character and an immediately following right- or dual-joining character. Nonspacing or enclosing marks may precede the joiner. This covers words such as `می‌خواهم`.
  - U+200D (ZWJ) is kept after a virama that follows a same-script letter, with optional marks between the letter and virama. U+200C is also kept after that virama when a letter of the same script immediately follows the joiner. [Unicode Security Mechanisms](https://www.unicode.org/reports/tr39/tr39-32.html#Limited_Contexts_for_Joining_Controls)
  - U+200D is kept between extended pictographs, with optional grapheme extenders such as a skin tone modifier or variation selector before the joiner.
  - Intervening whitespace and removed controls break joiner contexts. When a cluster would end the field with a space or with a ZWNJ whose following letter is outside the limit, the service cuts before that cluster.
- **Client Text-Rendering Duty**: Client applications must render returned text fields (`title`, `explanation`) as plain text rather than raw HTML or unescaped markup to protect against injection and cross-site scripting vulnerabilities. Escape URLs when placing them in HTML attributes; RFC 3986 sub-delimiters such as `'` are returned unencoded.

#### Example

```sh
curl -i -X POST http://localhost:8080/api/v1/adaptive-plan \
  -H "X-Internal-Token: $AXIOM_INTERNAL_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "student_id": "student-42",
    "course_id": "cs101",
    "failed_topics": [
      "Polymorphism"
    ],
    "vark_profile": "visual"
  }'
```

Response:

```http
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8
X-Request-ID: k9m7Lx4fPUH5TzEerTGLjuuPm2qLVSzmkFDEe6UxwJ0
X-RateLimit-Limit: 50
X-RateLimit-Remaining: 49
X-RateLimit-Reset: 60

{
  "student_id": "student-42",
  "course_id": "cs101",
  "items": [
    {
      "topic": "Inheritance",
      "priority": 1,
      "prerequisite_chain": [
        "Variables",
        "Objects",
        "Classes"
      ],
      "explanation": "Understanding class hierarchies is required before applying dynamic dispatch.",
      "resources": [
        {
          "title": "Visual Guide to Class Inheritance",
          "url": "https://example.com/inheritance-diagram",
          "resource_type": "diagram"
        }
      ]
    },
    {
      "topic": "Polymorphism",
      "priority": 2,
      "prerequisite_chain": [
        "Variables",
        "Objects",
        "Classes",
        "Inheritance"
      ],
      "explanation": "Mastering method overriding and dynamic polymorphism allows flexible design.",
      "resources": [
        {
          "title": "Polymorphism Animation Walkthrough",
          "url": "https://example.com/polymorphism-video",
          "resource_type": "video"
        }
      ]
    }
  ],
  "_meta": {
    "subgraph_tuples": 8,
    "topics_processed": 1,
    "items_generated": 2,
    "items_after_validation": 2,
    "items_dropped": 0,
    "llm_latency_ms": 1120,
    "total_latency_ms": 1135
  }
}
```

---

### POST /api/v1/adaptive-plan/stream

Streams real-time adaptive study plan generation progress using Server-Sent Events (SSE). Emits topic completion events as individual concept plans finish, followed by a final validated study plan or error event.

- **Method**: `POST`
- **Path**: `/api/v1/adaptive-plan/stream`
- **Headers**:
  - `Content-Type: application/json` (optional parameters such as `charset=utf-8` allowed)
  - [Credential for the configured authentication mode](#authentication)
- **Validation Rules**:
  - Accepts the identical JSON payload and schema as `POST /api/v1/adaptive-plan`.
  - Authentication, scope, request body syntax, schema, media type, topic validation and curriculum budget checks run before streaming begins. The route returns a JSON error envelope for these failures, including HTTP 401 for invalid credentials and HTTP 403 for a verified token that lacks `plan:generate`. Oversized curriculum context returns HTTP 422 `curriculum_context_too_large` before any event or keepalive. A deadline during preparation returns HTTP 504 as JSON.
  - The endpoint operates under the same client IP rate limit budget as other `/api/v1` routes.
  - This endpoint does not support `Idempotency-Key` response replay.

#### Connection Lifetime and Keepalive

- **Headers**: Accepted requests return HTTP 200 with:
  - `Content-Type: text/event-stream`
  - `Cache-Control: no-cache`
  - `X-Accel-Buffering: no`
  - `X-Request-ID` and rate-limit headers (`X-RateLimit-Limit`, `X-RateLimit-Remaining`, `X-RateLimit-Reset`)
- **Keepalive Comments**: An initial `: keepalive` SSE comment is sent immediately upon connection open, and periodic keepalive comments are sent every 2 seconds thereafter while awaiting reasoning events. Each comment and event frame is flushed immediately.
- The opt-in listener cancels the pipeline on an HTTP/1 close or HTTP/2 reset. Either listener cancels on a failed stream write or flush. With Fiber, the two-second keepalive helps surface failed writes. An in-flight call releases its concurrency permit when the planner returns. The access log records `client_disconnected: true`.
- **Timeout**: The pipeline execution is bounded by `REQUEST_TIMEOUT` (default `20s`), matching the JSON route.

#### Event Format

Every event frame terminates with an empty line (`\n\n`) and flushes immediately. Once streaming starts, the HTTP status code remains 200; terminal outcomes are communicated via SSE event types:

1. `event: topic`: Emitted as each topic completes downstream generation. Completion order reflects asynchronous execution and may differ from request order.
   - Payload (`data`): JSON object containing:
     - `topic`: Canonical topic name string.
     - `index`: Zero-based index of the topic's first occurrence in the original request `failed_topics` array after canonical comparison. Duplicate topics in the request produce a single topic call and event.
     - `items_generated`: Integer count of study items generated for this topic (0 if topic generation failed).
   - Example frame:
     ```text
     event: topic
     data: {"topic":"Recursion","index":0,"items_generated":1}

     ```

2. `event: plan`: Emitted on successful pipeline completion after all topics are processed, merged, sanitized, and validated against the knowledge graph.
   - Payload (`data`): Full validated `PlanResponse` JSON object, identical to the payload returned by `POST /api/v1/adaptive-plan` (including `student_id`, `course_id`, `items`, and `_meta`).
   - Example frame:
     ```text
     event: plan
     data: {"student_id":"student-42","course_id":"cs101","items":[{"topic":"Functions","priority":1,"prerequisite_chain":[],"explanation":"Understanding functions is fundamental.","resources":[]}],"_meta":{"subgraph_tuples":8,"topics_processed":1,"items_generated":1,"items_after_validation":1,"items_dropped":0,"llm_latency_ms":900,"total_latency_ms":920}}

     ```

3. `event: error`: Emitted if the reasoning pipeline fails after streaming has begun (e.g. upstream failure, invalid model output, throttling, circuit breaker trip, or deadline expiration).
   - Payload (`data`): JSON object containing the standard error envelope plus the equivalent numeric HTTP status:
     - `error`: Error code string (such as `bad_gateway`, `service_unavailable`, or `gateway_timeout`).
     - `details`: Human-readable error description string.
     - `status`: Integer status code equivalent to the non-streaming HTTP status (such as `502`, `503`, or `504`).
   - Example frame:
     ```text
     event: error
     data: {"error":"bad_gateway","details":"upstream reasoning failed","status":502}

     ```

#### Example

```sh
curl -N -i -X POST http://localhost:8080/api/v1/adaptive-plan/stream \
  -H "X-Internal-Token: $AXIOM_INTERNAL_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "student_id": "student-42",
    "course_id": "cs101",
    "failed_topics": [
      "Recursion"
    ],
    "vark_profile": "visual"
  }'
```

Response stream:

```http
HTTP/1.1 200 OK
Content-Type: text/event-stream
Cache-Control: no-cache
X-Accel-Buffering: no
X-Request-ID: s8H21kNm3_Qwe874KLvOpq123
X-RateLimit-Limit: 50
X-RateLimit-Remaining: 49
X-RateLimit-Reset: 60

: keepalive

event: topic
data: {"topic":"Recursion","index":0,"items_generated":1}

event: plan
data: {"student_id":"student-42","course_id":"cs101","items":[{"topic":"Functions","priority":1,"prerequisite_chain":[],"explanation":"Reviewing pure functions and call stacks precedes recursion.","resources":[{"title":"Functions Visualized","url":"https://example.com/functions","resource_type":"diagram"}]},{"topic":"Recursion","priority":2,"prerequisite_chain":["Functions"],"explanation":"Mastering base cases and recurrence relations.","resources":[{"title":"Recursive Tracing","url":"https://example.com/recursion","resource_type":"video"}]}],"_meta":{"subgraph_tuples":4,"topics_processed":1,"items_generated":2,"items_after_validation":2,"items_dropped":0,"llm_latency_ms":840,"total_latency_ms":855}}

```

---

### GET /api/v1/tutor/student/:student_id/cognitive-graph

Computes a depth-1 concept neighborhood around failed topics for a learner, classifying each concept into `failed`, `learning`, or `mastered` states (or `unassessed` when learner history is supplied).

- **Method**: `GET`
- **Path**: `/api/v1/tutor/student/:student_id/cognitive-graph`
- **Headers**:
  - [Credential for the configured authentication mode](#authentication)
- **Path Parameters**:
  - `student_id`: Required string. Must match regex `^[A-Za-z0-9._:-]{1,128}$`. This operation validates the path parameter and computes the graph from the supplied topics and curriculum alone.
- **Query Parameters**:
  - `topics`: Required string. Comma-separated list of failed topic names; may also be supplied across repeated query parameters (for example, `topics=Recursion&topics=Polymorphism`), and comma-separated lists and repeated parameters may be mixed. All supplied values form a unified topic collection. Canonical resolution and deduplication occur before validating that the union contains between 1 and `MAX_FAILED_TOPICS` unique topics; every topic in the union must exist in the knowledge graph.
  - `mastered`: Optional string. Comma-separated list of mastered topic names; may also be supplied across repeated query parameters (for example, `mastered=Functions&mastered=Variables`), and comma-separated lists and repeated parameters may be mixed. Canonical resolution and deduplication occur before validating that the union contains at most `MAX_MASTERED_TOPICS` unique topics. Every topic in the union must exist in the knowledge graph; unknown topics return HTTP 422 `validation_error` naming `mastered`. A topic present in both `topics` and `mastered` returns HTTP 400 `validation_error` (`a topic must not be both failed and mastered: "TOPIC"`). Exceeding `MAX_MASTERED_TOPICS` returns HTTP 400 `validation_error` naming `mastered`. An explicit empty `mastered=` or `mastered=,` parameter supplies empty history. When `mastered` is supplied, concept states follow precedence `failed` > `mastered` > `learning` > `unassessed`; without `mastered`, legacy states (`failed`, `learning`, `mastered`) are returned unchanged.
  - `target_topic`: Optional string. At most one occurrence is allowed, including empty occurrences; repeated `target_topic` parameters return HTTP 400 `validation_error` (`target_topic must not be repeated`). When a single non-empty value is provided, it centers the depth-1 subgraph around this specific topic instead of all failed topics. It resolves canonically across case, whitespace, and Unicode composition variants (see [Node Naming](knowledge-graph.md#node-naming)) and must be a member of the resolved `topics` union. A missing or single empty value leaves the graph centered on all requested topics. If non-empty and not present in the knowledge graph, returns HTTP 422 with a bounded quoted echo; if present in the knowledge graph but outside the resolved `topics` union, returns HTTP 400 `validation_error` with `target_topic must be one of topics`.

#### Responses

- **200 OK**: Cognitive shadow graph calculated and pruned of orphan nodes.
- **400 Bad Request**: Invalid `student_id` (does not match identifier format); no non-empty topics supplied; more than `MAX_FAILED_TOPICS` unique topics across all `topics` values; more than `MAX_MASTERED_TOPICS` unique topics across `mastered` values; topic present in both `topics` and `mastered`; repeated `target_topic` query parameter; or known `target_topic` outside the resolved `topics` union (`target_topic must be one of topics`).
- **401 Unauthorized**: Missing or invalid authentication token.
- **403 Forbidden**. A verified token lacks `graph:read`.
- **408 Request Timeout**: Request not read within the 10 s read timeout.
- **413 Payload Too Large**: Request body exceeds 64 KiB.
- **422 Unprocessable Entity**: Unrecognized topic in `topics`, `mastered`, or `target_topic`.
- **429 Too Many Requests**: Rate limit exceeded.
- **431 Request Header Fields Too Large**: Inbound request headers exceed the server limit.
- **500 Internal Server Error**: Unexpected internal failure or recovered panic.
- **503 Service Unavailable**: Shared rate limit storage unavailable (`service_unavailable`, `Retry-After: 1`).

#### Example

```sh
curl -i "http://localhost:8080/api/v1/tutor/student/student-42/cognitive-graph?topics=Recursion&target_topic=Recursion" \
  -H "X-Internal-Token: $AXIOM_INTERNAL_TOKEN"
```

Response:

```http
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8
X-Request-ID: fNcA126ed_poawgiDblg4oCmy_nm1WQGdCL5NjEGiB8
X-RateLimit-Limit: 50
X-RateLimit-Remaining: 49
X-RateLimit-Reset: 60

{
  "nodes": [
    {
      "id": "Dynamic Programming",
      "label": "Dynamic Programming",
      "cognitive_state": "mastered"
    },
    {
      "id": "Functions",
      "label": "Functions",
      "cognitive_state": "learning"
    },
    {
      "id": "Merge Sort",
      "label": "Merge Sort",
      "cognitive_state": "mastered"
    },
    {
      "id": "Recursion",
      "label": "Recursion",
      "cognitive_state": "failed"
    }
  ],
  "edges": [
    {
      "source": "Dynamic Programming",
      "target": "Recursion",
      "relation": "depends_on"
    },
    {
      "source": "Merge Sort",
      "target": "Recursion",
      "relation": "depends_on"
    },
    {
      "source": "Recursion",
      "target": "Functions",
      "relation": "depends_on"
    }
  ]
}
```

---

### GET /api/v1/topics

Lists curriculum topics sorted by concept name, with direct prerequisites and direct dependents. Does not perform model provider calls.

- **Method**: `GET`
- **Path**: `/api/v1/topics`
- **Headers**:
  - [Credential for the configured authentication mode](#authentication)
- **Query Parameters**:
  - `q`: Optional string. Canonical substring filter. When provided, filters topics whose canonical concept name contains the canonical filter string. When omitted or empty, all curriculum topics are returned. If no topics match the filter, an empty array is returned.
- **Request Body**: None

#### Responses

- **200 OK**: Topics retrieved successfully. Direct prerequisites and dependents are distinct and sorted, including both `depends_on` and `is_a` relations.
- **401 Unauthorized**: Missing or invalid authentication token.
- **403 Forbidden**. A verified token lacks `graph:read`.
- **408 Request Timeout**: Request not read within the 10 s read timeout.
- **413 Payload Too Large**: Request body exceeds 64 KiB.
- **429 Too Many Requests**: Rate limit exceeded.
- **431 Request Header Fields Too Large**: Inbound request headers exceed the server limit.
- **500 Internal Server Error**: Unexpected internal failure or recovered panic.
- **503 Service Unavailable**: Shared rate limit storage unavailable (`service_unavailable`, `Retry-After: 1`).

#### Example

```sh
curl -i "http://localhost:8080/api/v1/topics?q=sort" \
  -H "X-Internal-Token: $AXIOM_INTERNAL_TOKEN"
```

Response:

```http
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8
X-Request-ID: m1Z8vN2pQRk0TsVeRtGLjuuPm2qLVSzmkFDEe6UxwJ1
X-RateLimit-Limit: 50
X-RateLimit-Remaining: 49
X-RateLimit-Reset: 60

{
  "topics": [
    {
      "name": "Merge Sort",
      "prerequisites": [
        "Recursion"
      ],
      "dependents": []
    }
  ]
}
```

---

### GET /api/v1/topics/{topic}/path

Returns the full prerequisite chain in topological study order and direct dependents for a single topic. Does not perform model provider calls.

- **Method**: `GET`
- **Path**: `/api/v1/topics/{topic}/path`
- **Headers**:
  - [Credential for the configured authentication mode](#authentication)
- **Path Parameters**:
  - `topic`: Required string. Topic name to inspect. Resolves canonically across case, whitespace, and Unicode composition differences (see [Node Naming](knowledge-graph.md#node-naming)). Path-escaped characters are unescaped before canonical resolution.
- **Query Parameters**: None
- **Request Body**: None

#### Responses

- **200 OK**: Prerequisite path and dependents retrieved successfully. `prerequisite_chain` contains all transitive prerequisites in topological study order, excluding the target topic itself. `dependents` lists direct dependents in alphabetical order.
- **401 Unauthorized**: Missing or invalid authentication token.
- **403 Forbidden**. A verified token lacks `graph:read`.
- **408 Request Timeout**: Request not read within the 10 s read timeout.
- **413 Payload Too Large**: Request body exceeds 64 KiB.
- **422 Unprocessable Entity**: Topic is not found in the knowledge graph. Returns error code `validation_error` with quoted topic name details.
- **429 Too Many Requests**: Rate limit exceeded.
- **431 Request Header Fields Too Large**: Inbound request headers exceed the server limit.
- **500 Internal Server Error**: Unexpected internal failure or recovered panic.
- **503 Service Unavailable**: Shared rate limit storage unavailable (`service_unavailable`, `Retry-After: 1`).

#### Example

```sh
curl -i http://localhost:8080/api/v1/topics/Recursion/path \
  -H "X-Internal-Token: $AXIOM_INTERNAL_TOKEN"
```

Response:

```http
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8
X-Request-ID: p2Y7wM1oPQj9SrUeQsFKittOl1pKVRyljECDd5TwvI0
X-RateLimit-Limit: 50
X-RateLimit-Remaining: 49
X-RateLimit-Reset: 60

{
  "topic": "Recursion",
  "prerequisite_chain": [
    "Functions"
  ],
  "dependents": [
    "Dynamic Programming",
    "Merge Sort"
  ]
}
```

---

### GET /swagger/*

Serves the Swagger 2.0 interactive user interface and asset files.

- **Method**: `GET`
- **Path**: `/swagger/index.html`
- **Auth**: None (public)

The raw OpenAPI specification rendered at startup is available at `/swagger/doc.json`.
