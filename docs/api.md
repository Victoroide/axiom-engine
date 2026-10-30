# API Reference

AxiomEngine provides HTTP endpoints for generating adaptive study plans, visualizing student cognitive states, inspecting API schemas, and verifying process health.

Interactive documentation is available at `/swagger/index.html`. Complete machine-readable schemas are maintained in [docs/swagger.yaml](swagger.yaml) and served dynamically at `/swagger/doc.json`.

---

## Authentication

Requests to `/api/v1` routes require authentication by default using the configured internal token. Startup fails if a token is not provided unless authentication is explicitly disabled by setting `AXIOM_AUTH_DISABLED=true` (see [configuration reference](configuration.md)). When authentication is disabled, API routes are publicly accessible and the server emits the startup warning `auth.disabled`.

Callers may supply the internal token using either of two headers:
- `X-Internal-Token: <token>`
- `Authorization: Bearer <token>`

Header verification checks `X-Internal-Token` first, then falls back to `Authorization: Bearer <token>` with a case-insensitive `Bearer` scheme check. Verification computes SHA-256 digests and performs a constant-time comparison (`crypto/subtle.ConstantTimeCompare`) in `internalTokenMiddleware` (`internal/server/server.go`). Requests that omit the token or provide a mismatched value receive HTTP 401 Unauthorized:

```json
{
  "error": "unauthorized",
  "details": "missing or invalid internal token"
}
```

The `/health`, `/swagger/*`, and `/swagger/doc.json` routes are always public and do not evaluate authorization headers. These public routes are also exempt from rate limiting.

---

## Common Headers

- `X-Request-ID`: Client-provided correlation identifier. If omitted, the server generates a 43-character URL-safe random token (with a UUID v4 fallback for parser-level errors). Every HTTP response echoes `X-Request-ID`.
- `Content-Type`: Required for requests with a payload. Endpoints expecting JSON require a media type of `application/json` parsed via MIME media type rules; optional parameters such as `charset=utf-8` are accepted. Missing, malformed, or non-JSON media types return HTTP 415 `unsupported_media_type` with details `Content-Type must be application/json`. All API responses return `application/json; charset=utf-8`.

---

## Error Envelope

All non-2xx responses adhere to the standard `domain.ErrorResponse` envelope:

```json
{
  "error": "error_code",
  "details": "human-readable description"
}
```

The `details` field provides context when available and is omitted when empty.

### Error Codes and HTTP Statuses

| HTTP Status | Error Code (`error`) | Description | Trigger |
|---|---|---|---|
| `400 Bad Request` | `invalid_request_body` | Request body fails JSON parsing or strict schema decoding | Malformed JSON, non-object body, trailing tokens, unknown or duplicate fields, case mismatches, or null array elements |
| `400 Bad Request` | `validation_error` | Request payload fails schema validation | Missing required fields, invalid VARK profile, or too many topics |
| `400 Bad Request` | `bad_request` | Generic client error | Malformed query parameters or invalid path encoding |
| `401 Unauthorized` | `unauthorized` | Token authentication failure | Missing or incorrect `X-Internal-Token` or `Authorization: Bearer` |
| `404 Not Found` | `not_found` | Resource does not exist | Unmatched route path |
| `405 Method Not Allowed` | `method_not_allowed` | HTTP method not supported | Unsupported HTTP verb on an existing path |
| `408 Request Timeout` | `request_timeout` | Processing context canceled | Request context canceled during pipeline processing |
| `413 Payload Too Large` | `payload_too_large` | Request body exceeds limit | Inbound payload exceeds the 64 KiB (65,536 bytes) limit |
| `415 Unsupported Media Type` | `unsupported_media_type` | Media type not supported | Missing, malformed, or non-JSON `Content-Type` header on requests with a body |
| `422 Unprocessable Entity` | `validation_error` | Concept not found in knowledge graph | One or more topics in `failed_topics`, `topics`, or `target_topic` do not exist |
| `429 Too Many Requests` | `rate_limit_exceeded` | Client rate limit exceeded | Inbound requests exceed `RATE_LIMIT_MAX` within `RATE_LIMIT_WINDOW` |
| `500 Internal Server Error` | `internal_error` | Unhandled server error | Unexpected internal failure or recovered panic |
| `502 Bad Gateway` | `bad_gateway` | Upstream model failure | Amazon Bedrock invocation error or unparseable model response |
| `503 Service Unavailable` | `service_unavailable` | Circuit breaker open | Upstream planner circuit tripped due to consecutive failures |
| `504 Gateway Timeout` | `gateway_timeout` | Processing deadline exceeded | Pipeline execution exceeded `REQUEST_TIMEOUT` or `LLM_TIMEOUT` |

---

## Endpoints

### GET /health

Returns process liveness status. Does not require authentication.

- **Method**: `GET`
- **Path**: `/health`
- **Headers**: None required
- **Query Parameters**: None
- **Request Body**: None

#### Responses

- **200 OK**: Process is healthy and listening.

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
  - `X-Internal-Token: <token>` (if authentication is enabled)
- **Validation Rules**:
  - Strict JSON: Request body must be a single JSON object matching the schema. Unknown fields (`unknown field "KEY"`), duplicate keys at any depth (`duplicate field "KEY"`), case mismatches, trailing data (`request body must contain exactly one JSON value`), non-object bodies (`request body must be a JSON object`), and invalid JSON syntax (`request body must be valid JSON`) return HTTP 400 `invalid_request_body`.
  - `student_id`: Optional. If provided, must match regex `^[A-Za-z0-9._:-]{1,128}$`.
  - `course_id`: Optional. If provided, must match regex `^[A-Za-z0-9._:-]{1,128}$`.
  - `failed_topics`: Required. Array of strings. Must contain at least 1 and at most `MAX_FAILED_TOPICS` (default 10) unique items. Elements must not be null (`failed_topics must not contain null elements`). Topics resolve canonically across case, whitespace, and Unicode composition differences (see [Node Naming](knowledge-graph.md#node-naming)). Inbound topics are deduplicated by canonical form while preserving request order before validating the `MAX_FAILED_TOPICS` limit. Responses return authoritative stored node names. Upstream model recommendations are also matched canonically, deduplicated, and mapped to authoritative stored graph node names. Topics that do not resolve to a graph node return HTTP 422 `validation_error`.
  - `vark_profile`: Required. String enum. Must be one of `visual`, `auditory`, `read_write`, or `kinesthetic`.
  - `telemetry`: Optional object containing client telemetry (`session_id`, `timestamp_unix`, `duration_ms`, `client_version`). Unknown nested fields are rejected.

#### Responses

- **200 OK**: Successfully generated adaptive plan.
- **400 Bad Request**: Malformed body, missing required fields, or identifier syntax error.
- **401 Unauthorized**: Missing or invalid authentication token.
- **413 Payload Too Large**: Payload exceeds 64 KiB.
- **415 Unsupported Media Type**: Request `Content-Type` is missing, malformed, or not `application/json`.
- **422 Unprocessable Entity**: One or more topics are not found in the knowledge graph.
- **429 Too Many Requests**: Rate limit exceeded.
- **502 Bad Gateway**: Upstream Amazon Bedrock inference failure.
- **503 Service Unavailable**: Upstream circuit breaker is open.
- **504 Gateway Timeout**: Request exceeded pipeline deadline.

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

{
  "student_id": "student-42",
  "course_id": "cs101",
  "items": [
    {
      "topic": "Inheritance",
      "priority": 1,
      "prerequisite_chain": [
        "Classes",
        "Objects",
        "Variables"
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
        "Inheritance",
        "Classes",
        "Objects",
        "Variables"
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
    "llm_latency_ms": 1120.0,
    "total_latency_ms": 1135.0
  }
}
```

---

### GET /api/v1/tutor/student/:student_id/cognitive-graph

Computes a depth-1 concept neighborhood around failed topics for a learner, classifying each concept into `failed`, `learning`, or `mastered` states.

- **Method**: `GET`
- **Path**: `/api/v1/tutor/student/:student_id/cognitive-graph`
- **Headers**:
  - `X-Internal-Token: <token>` (if authentication is enabled)
- **Path Parameters**:
  - `student_id`: Required string. Must match regex `^[A-Za-z0-9._:-]{1,128}$`. (Validated at the route boundary; omitted from the cognitive shadow graph response).
- **Query Parameters**:
  - `topics`: Required string. Comma-separated list of failed topics. Must contain between 1 and `MAX_FAILED_TOPICS` unique topics. Resolves topics canonically and deduplicates by canonical form before count validation; every topic must exist in the knowledge graph.
  - `target_topic`: Optional string. When provided, centers the depth-1 subgraph around this specific topic instead of all failed topics. Must exist in the knowledge graph if specified.

#### Responses

- **200 OK**: Cognitive shadow graph calculated and pruned of orphan nodes.
- **400 Bad Request**: Invalid `student_id` or query parameters.
- **401 Unauthorized**: Missing or invalid authentication token.
- **422 Unprocessable Entity**: Unrecognized topic in `topics` or `target_topic`.
- **429 Too Many Requests**: Rate limit exceeded.

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

### GET /swagger/*

Serves the Swagger 2.0 interactive user interface and asset files.

- **Method**: `GET`
- **Path**: `/swagger/index.html`
- **Auth**: None (public)

The raw OpenAPI specification rendered at startup is available at `/swagger/doc.json`.
