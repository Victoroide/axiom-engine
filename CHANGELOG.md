# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Upstream response classification mapping malformed 2xx responses and truncated model outputs to HTTP 502 `bad_gateway`.
- BAML v0.226.2 client generation with pinned CFFI native libraries for `linux/amd64`, `linux/arm64`, `darwin/amd64`, and `darwin/arm64`.
- Host native library verification target `make native-baml` with SHA256 checksum validation and cached asset verification.
- Fiber v3 HTTP application with structured JSON logging (`http.access`), request ID tracing, panic recovery (`panic.recovered`), and helmet security headers.
- Constant-time internal token authentication via `X-Internal-Token` or `Authorization: Bearer <token>` for `/api/v1` routes with case-insensitive scheme matching.
- Topic validation against curriculum graph taxonomy, bounding maximum unique topics via `MAX_FAILED_TOPICS`, student ID validation regex, and VARK style validation.
- In-memory knowledge graph loaded from embedded `seed.json` with fallback support for an external `GRAPH_FILE`.
- Circuit breaker protection and bounded timeouts for Bedrock LLM reasoning calls, excluding `context.Canceled` sibling calls from tripping or resetting breaker state.
- Graph-derived prerequisite chain enforcement and stable prerequisite ranking for adaptive plan items.
- Server transport deadlines: `ReadTimeout` (10s), `IdleTimeout` (1m), and `WriteTimeout` (`REQUEST_TIMEOUT + 10s`).
- Swagger 2.0 interactive documentation at `/swagger/index.html` with thread-safe specification delivery at `/swagger/doc.json`.
- Browser end-to-end test suite using Rod and headless Chrome/Chromium (`make test-e2e`).
- Multi-stage Docker build producing a non-root runtime container on `distroless/cc-debian12:nonroot` with built-in CLI health probe (`axiom-server healthcheck`).
- CI workflows for linting, race-enabled tests, generated code verification, vulnerability scanning, container smoke testing, and browser end-to-end tests.
- Workflows for CodeQL security scanning, pull request dependency reviews, Conventional Commit PR title validation, SLSA build provenance attestation, and multi-architecture container releases to GitHub Container Registry.
- Repository configuration including Dependabot, issue templates, pull request template, CODEOWNERS, and MIT license.

### Changed

- **Breaking:** Require every requested topic to retain a study item after validation; return HTTP 502 `bad_gateway` with details `model returned no usable item` instead of HTTP 200 with an empty item list when model output lacks a usable item for a requested topic.
- **Breaking:** Require `AXIOM_INTERNAL_TOKEN` at startup unless `AXIOM_AUTH_DISABLED=true` is explicitly configured, and bind the Docker Compose published HTTP port to loopback (`127.0.0.1:8080:8080`).
- **Breaking:** Reject short, whitespace-containing, control-containing, and non-ASCII internal tokens at startup; `AXIOM_INTERNAL_TOKEN` must be at least 32 visible ASCII characters (`0x21`–`0x7E`) without surrounding or embedded whitespace.
- **Breaking:** Enforce strict JSON decoding for `POST /api/v1/adaptive-plan`; require media type `application/json` (returning HTTP 415 `unsupported_media_type` otherwise) and reject unknown fields, duplicate keys, case-mismatched keys, null array elements, non-object payloads, and trailing data with HTTP 400 `invalid_request_body`.
- **Breaking:** Bound concurrent Bedrock calls across requests with a process-wide semaphore (`LLM_MAX_CONCURRENCY`, default 16); wait times exceeding `LLM_QUEUE_TIMEOUT` (default 2s) return HTTP 503 `service_unavailable` with `Retry-After: 1`. An existing `LLM_TIMEOUT` of 2s or less now requires setting an explicitly smaller `LLM_QUEUE_TIMEOUT`.
- Advertise `Retry-After` headers on HTTP 503 responses, setting a 15-second backoff when the circuit breaker is open and remapping upstream throttling from HTTP 502 to HTTP 503 with a 5-second backoff.
- Migrated HTTP server framework to Fiber v3 with validated configuration and standardized JSON error envelope containing `error` and `details`.
- Replaced wildcard CORS default (`*`) with disabled CORS unless origins are explicitly configured in `CORS_ALLOWED_ORIGINS`.
- Mapped reasoning pipeline failures to HTTP 502 (`bad_gateway`), 503 (`service_unavailable`), and 504 (`gateway_timeout`), and unknown topics to HTTP 422 (`validation_error`).
- Scoped sliding-window rate limiting exclusively to `/api/v1` routes, ensuring `/health` and `/swagger/*` are unthrottled and do not consume rate limit tokens.
- Suppressed verbose BAML prompt logs in the runtime container and Make by defaulting `BAML_LOG=off`, emitting bounded failure logs (`error_class`, `request_id`, topic capped at 128 characters) and logging warning records when hallucinated items are dropped.
- Added configuration environment variables: `REQUEST_TIMEOUT`, `LLM_TIMEOUT`, `SHUTDOWN_TIMEOUT`, `RATE_LIMIT_MAX`, `RATE_LIMIT_WINDOW`, `MAX_FAILED_TOPICS`, `GRAPH_FILE`, `LOG_LEVEL`, and `TRUSTED_PROXIES`.
- Replaced unbounded fan-out with deterministic pipeline execution, deduplication, and graph-ordered topic planning.
- Enforced 64 KiB request body limit returning HTTP 413 (`payload_too_large`).
- Pinned development and compilation toolchain to versions specified in the [development guide](docs/development.md#pinned-tool-versions).
- Updated `make lint` to vet both standard and e2e build tags before running `golangci-lint`.
- Configured Dependabot to ignore `github.com/boundaryml/baml` gomod updates to maintain synchronized generator, module, and native library pins.

### Removed

- Removed shell test scripts and ad-hoc test payload files.
- Removed obsolete deployment configuration files.
- Removed obsolete documentation from `docs/reference/`.

### Fixed

- Reject cyclic `depends_on` and `is_a` prerequisite graphs during startup with a deterministic cycle path.
- Remove nested BAML retries to bound Bedrock throttle calls to the SDK attempt count and avoid retrying non-retryable upstream rejection.
- Resolve topic inputs across case, whitespace, and Unicode composition variants, returning stored graph node names consistently.
- Exclude invalid upstream output and rejected requests from circuit breaker health counts so non-availability failures do not trip the circuit for unrelated topics.
- Enforce the LLM timeout autonomously in the reasoning service when downstream calls block, returning HTTP 504 `gateway_timeout` while holding concurrency capacity until the call completes.
- Drop generated study items outside the requested topic prerequisite closure during graph validation and report the discarded count in pipeline metadata.

### Security

- Enforced constant-time token comparison using SHA-256 digests on all `/api/v1` routes unless `AXIOM_AUTH_DISABLED=true`.
- Restricted `X-Forwarded-For` evaluation to explicitly allowed IP addresses and CIDRs to prevent client IP spoofing.
- Validated all incoming topics against the curriculum graph to prevent prompt injection and unauthorized topic processing.
- Excluded canceled sibling errgroup calls from circuit breaker failure and success counters, ensuring multi-topic outages trip the breaker as expected.
- Pinned all GitHub Actions to verified immutable commit hashes.
- Executed container runtime as non-root user `65532:65532` on `distroless/cc-debian12:nonroot` with read-only root compatibility.
- Enforce an output policy on model-generated resources and text: accept only absolute HTTP(S) URLs with an IP or ASCII DNS host and no userinfo, return them re-encoded and at most 2048 characters long, collapse whitespace and remove other control and format characters in text, bound explanations to 1000 and titles to 200 characters, and restrict resource types to the learner's VARK profile with at most 3 resources per item.
