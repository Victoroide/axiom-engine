# AxiomEngine

[![CI](https://github.com/Victoroide/axiom-engine/actions/workflows/ci.yml/badge.svg)](https://github.com/Victoroide/axiom-engine/actions/workflows/ci.yml)
[![CodeQL](https://github.com/Victoroide/axiom-engine/actions/workflows/codeql.yml/badge.svg)](https://github.com/Victoroide/axiom-engine/actions/workflows/codeql.yml)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/Victoroide/axiom-engine/badge)](https://scorecard.dev/viewer/?uri=github.com/Victoroide/axiom-engine)
[![Go Version](https://img.shields.io/badge/go-1.27.3-blue.svg)](https://golang.org)
[![Go Reference](https://pkg.go.dev/badge/github.com/Victoroide/axiom-engine.svg)](https://pkg.go.dev/github.com/Victoroide/axiom-engine)
[![Latest Release](https://img.shields.io/github/v/release/Victoroide/axiom-engine)](https://github.com/Victoroide/axiom-engine/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)

AxiomEngine is a Go microservice that generates personalized, prerequisite-ordered adaptive study plans from student assessment deficits. It combines an in-memory curriculum knowledge graph with inference executed through a configurable model provider (defaulting to Amazon Bedrock, with support for OpenAI, Anthropic, Google AI, Ollama, and generic OpenAI-compatible endpoints) via a generated BAML client. The service exposes HTTP endpoints for study plan generation, real-time Server-Sent Events (SSE) streaming, cognitive shadow graph visualization, curriculum topic queries, and system health probes, as well as a Model Context Protocol (MCP) server over standard input and output (`axiom-server mcp`) exposing reasoning and curriculum tools.

## Table of Contents

- [Features](#features)
- [Request Path](#request-path)
- [Installation](#installation)
  - [Container Image](#container-image)
  - [Release Archives](#release-archives)
  - [Building from Source](#building-from-source)
- [Quick Start](#quick-start)
  - [Docker Compose](#docker-compose)
  - [Local Development](#local-development)
- [Configuration Summary](#configuration-summary)
- [Usage](#usage)
  - [Health Check](#health-check)
  - [Cognitive Shadow Graph](#cognitive-shadow-graph)
  - [Adaptive Study Plan](#adaptive-study-plan)
  - [Adaptive Study Plan Streaming](#adaptive-study-plan-streaming)
  - [Curriculum Topics](#curriculum-topics)
  - [Model Context Protocol (MCP) Stdio Server](#model-context-protocol-mcp-stdio-server)
- [Limitations](#limitations)
- [Roadmap](#roadmap)
- [Documentation](#documentation)
- [Contributing and Support](#contributing-and-support)
- [Citation](#citation)
- [Acknowledgements](#acknowledgements)
- [License](#license)

## Features

- **Knowledge-graph guided reasoning**: Extracts prerequisite chains and local subgraphs using depth-first and breadth-first search over an immutable graph.
- **Configurable model providers**: Routes reasoning prompts through Amazon Bedrock (`aws-bedrock`, default), OpenAI (`openai`), Anthropic (`anthropic`), Google AI (`google-ai`), Ollama (`ollama`), or OpenAI-compatible endpoints (`openai-generic`) via BAML.
- **VARK-aligned resource mapping**: Generates study recommendations tailored to visual, auditory, read-write, or kinesthetic learning modalities.
- **Concurrent bounded fan-out**: Dispatches parallel per-topic model calls bounded per request and by a process-wide concurrency limit, protected by circuit breaking.
- **Adaptive plan streaming**: Streams concept generation progress and completed plans in real time over Server-Sent Events (SSE), cancelling downstream reasoning if the client disconnects.
- **Idempotent plan generation**: Replays stored responses for repeated requests carrying an `Idempotency-Key` header without repeating model inference.
- **Shared Redis storage**: Shares sliding-window rate limit budgets and plan idempotency records across service replicas when `REDIS_URL` is configured.
- **Curriculum topic listing**: Exposes endpoints to inspect curriculum topics and retrieve full study-ordered prerequisite chains without model provider calls.
- **Learner mastery filtering**: Incorporates known mastered concepts into requests to omit mastered prerequisites from recommendations while preserving complete prerequisite chains.
- **Cognitive shadow graphs**: Computes graph neighborhoods classifying concepts into failed, learning, mastered, and unassessed states for frontend visualization.
- **Model Context Protocol (MCP)**: Runs as an MCP server over standard input and output (`axiom-server mcp`), exposing reasoning and curriculum tools to MCP-compatible clients.
- **Distributed tracing**: Exports OpenTelemetry traces over OTLP/HTTP to generic collectors or LangSmith, supporting span hierarchy correlation and optional prompt and completion content capture.
- **Hallucination validation**: Verifies model outputs against known graph nodes, dropping unrecognized concepts and concepts outside the requested topics and their prerequisites before assigning priorities.
- **Configurable token authentication**: Restricts API access using constant-time verification of internal shared tokens, supporting zero-downtime secret rotation.
- **Optional resource link validation**: Verifies recommended resource URLs with outbound HEAD and GET checks and SSRF protections when enabled (`RESOURCE_LINK_CHECK=true`), dropping unreachable links.

## Request Path

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/diagrams/request-path-dark.svg">
  <img src="docs/assets/diagrams/request-path-light.svg" width="100%" alt="An HTTP client at the upper left points right to the Fiber HTTP boundary, which handles request IDs, logging, recovery, security headers and CORS, with token authentication and local or shared Redis rate limiting for API routes only. The boundary points right to public health, liveness, readiness and optional Swagger and metrics routes; downward to JSON and SSE adaptive-plan routes, with Idempotency-Key replay limited to JSON; and diagonally down and right into the top edge of the in-memory knowledge graph for topic listing, prerequisite paths and cognitive graphs. At the middle left, MCP over stdio, invoked with axiom-server mcp, points diagonally down and right into the top edge of the reasoning service below, bypassing HTTP middleware. Adaptive-plan requests also point down to this service, which validates, extracts graph context, fans out, merges, limits concurrency and applies circuit breaking. The service points up and right to the knowledge graph, left to the BAML provider registry with Amazon Bedrock as default and OpenAI, Anthropic, Google, Ollama and OpenAI-compatible alternatives, and right to optional OTLP and LangSmith trace export.">
</picture>

## Installation

### Container Image

Multi-architecture container images (`linux/amd64` and `linux/arm64`) are published to GitHub Packages at `ghcr.io/victoroide/axiom-engine`.

Published releases include the following tag formats:
- Full release tag: `ghcr.io/victoroide/axiom-engine:v1.2.3`
- Semantic version tag: `ghcr.io/victoroide/axiom-engine:1.2.3`
- Minor version tag: `ghcr.io/victoroide/axiom-engine:1.2`

Pull an image using Docker:

```sh
docker pull ghcr.io/victoroide/axiom-engine:v1.2.3
```

Verify SLSA build provenance using the GitHub CLI:

```sh
gh attestation verify oci://ghcr.io/victoroide/axiom-engine:v1.2.3 --repo Victoroide/axiom-engine
```

Verify the Cosign signature against the release workflow identity:

```sh
cosign verify ghcr.io/victoroide/axiom-engine:v1.2.3 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github[.]com/Victoroide/axiom-engine/[.]github/workflows/release[.]yml@refs/tags/v1[.]2[.]3$'
```

### Release Archives

Standalone native executable archives are attached to GitHub releases for `linux/amd64`, `linux/arm64`, and `darwin/arm64`. Each archive contains the stripped `axiom-server` binary, the verified native BAML CFFI shared library under its upstream filename (`libbaml_cffi-x86_64-unknown-linux-gnu.so`, `libbaml_cffi-aarch64-unknown-linux-gnu.so`, or `libbaml_cffi-aarch64-apple-darwin.dylib`), `LICENSE`, and `README.md`. To run an extracted archive, set `BAML_LIBRARY_PATH` to the bundled native library; see [Run an archive](docs/deployment.md#run-an-archive) for instructions.

Each release attaches:
- `axiom-engine_<version>_<os>_<arch>.tar.gz` (where `<version>` omits the leading `v`, for example `axiom-engine_1.2.3_linux_amd64.tar.gz`)
- `checksums.txt` (SHA-256 digests of all platform archives)
- `checksums.txt.sigstore.json` (Cosign keyless signature bundle verifying `checksums.txt`)
- `axiom-engine_<version>.sigstore.json` (Sigstore bundle containing SLSA build provenance)
- `axiom-engine_<version>.intoto.jsonl` (DSSE provenance envelope)

Download release assets using the GitHub CLI:

```sh
gh release download v1.2.3 --repo Victoroide/axiom-engine \
  --pattern 'axiom-engine_*.tar.gz' \
  --pattern 'checksums.txt*' \
  --pattern 'axiom-engine_1.2.3.sigstore.json'
```

Verify the archive checksums:

```sh
sha256sum -c checksums.txt
```

On macOS, verify checksums with `shasum -a 256 -c checksums.txt`.

Verify the signature of `checksums.txt` using Cosign:

```sh
cosign verify-blob \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github[.]com/Victoroide/axiom-engine/[.]github/workflows/release[.]yml@refs/tags/v1[.]2[.]3$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
```

Verify archive build provenance using the GitHub CLI:

```sh
gh attestation verify axiom-engine_1.2.3_linux_amd64.tar.gz --repo Victoroide/axiom-engine
```

For Cosign provenance verification instructions, see [docs/deployment.md](docs/deployment.md#release-archives).

### Building from Source

Build requirements: Go 1.27.3, a C compiler toolchain (`gcc` or `clang`) with CGO enabled.

1. Clone the repository and download Go dependencies:

```sh
git clone https://github.com/Victoroide/axiom-engine.git
cd axiom-engine
go mod download
```

2. Download and verify the native BAML CFFI library for your platform:

```sh
make native-baml
```

3. Compile the service binary:

```sh
make build
```

The compiled binary is written to `bin/axiom-server`.

## Quick Start

### Docker Compose

1. Copy the example environment file:

```sh
cp .env.example .env
```

Generate an internal token and set `AXIOM_INTERNAL_TOKEN=<generated value>` in `.env`:

```sh
openssl rand -hex 32
```

Configure credentials for your selected model provider in `.env`. For the default `aws-bedrock` provider, set `AWS_REGION`, `AWS_ACCESS_KEY_ID`, and `AWS_SECRET_ACCESS_KEY`. For other providers, set `LLM_PROVIDER`, `LLM_MODEL`, and the corresponding provider API key (such as `OPENAI_API_KEY`).

2. Build and start the container with Docker Compose:

```sh
docker compose up --build
```

The container runs as non-root UID/GID `65532:65532` and binds port 8080 to loopback (`http://127.0.0.1:8080`).

### Local Development

Prerequisites: Go 1.27.3, CGO toolchain, and provider credentials.

1. Set the required provider credentials and service token:

```sh
export AXIOM_INTERNAL_TOKEN="$(openssl rand -hex 32)"
export AWS_REGION=us-east-1
export AWS_ACCESS_KEY_ID="your-access-key-id"
export AWS_SECRET_ACCESS_KEY="your-secret-access-key"
```

2. Start the service:

```sh
make run
```

Running `./bin/axiom-server` directly requires `BAML_LIBRARY_PATH` and `BAML_LIBRARY_DISABLE_DOWNLOAD=true`. Prefer `make run`.

The server listens on `:8080` (`http://0.0.0.0:8080`). When `SWAGGER_ENABLED=true` (the default), interactive documentation is available at `http://127.0.0.1:8080/swagger/index.html`.

## Configuration Summary

The service is configured using environment variables. The table below lists the most important variables and their default values:

| Variable | Default | Description |
|---|---|---|
| `PORT` | `8080` | TCP port on which the HTTP server listens. |
| `AXIOM_INTERNAL_TOKEN` | `""` (unset) | Shared secret required in `X-Internal-Token` or `Authorization: Bearer` on `/api/v1/*` routes. Required in HTTP mode unless `AXIOM_AUTH_DISABLED=true`. |
| `AXIOM_INTERNAL_TOKEN_PREVIOUS` | `""` (unset) | Previous secret accepted during zero-downtime token rotation. |
| `AXIOM_AUTH_DISABLED` | `false` | When `true`, disables token authentication on `/api/v1/*` routes. |
| `LLM_PROVIDER` | `aws-bedrock` | Model provider: `aws-bedrock`, `openai`, `anthropic`, `google-ai`, `ollama`, or `openai-generic`. |
| `LLM_MODEL` | `amazon.nova-micro-v1:0` (`aws-bedrock`) | Model identifier passed to the provider. Required for providers other than `aws-bedrock`. |
| `LLM_BASE_URL` | `""` (`http://localhost:11434/v1` for `ollama`) | Custom API base URL for `openai`, `ollama`, or `openai-generic`. Required for `openai-generic`. |
| `REQUEST_TIMEOUT` | `20s` | Maximum deadline for servicing incoming HTTP reasoning requests. |
| `LLM_TIMEOUT` | `15s` | Timeout applied to each individual per-topic model call. |
| `LLM_MAX_CONCURRENCY` | `16` | Maximum simultaneous in-flight provider calls across the process. |
| `RATE_LIMIT_MAX` | `50` | Maximum requests allowed from a client IP within `RATE_LIMIT_WINDOW`. |
| `RATE_LIMIT_WINDOW` | `1m` | Sliding window duration for IP rate limiting. |
| `REDIS_URL` | `""` (unset) | Optional Redis URL for shared rate limiting and plan idempotency across replicas. |
| `IDEMPOTENCY_TTL` | `24h` | Retention duration for idempotent study plan replay responses (`0s` disables). |
| `RESOURCE_LINK_CHECK` | `false` | When `true`, verifies recommended resource URLs with outbound HTTP checks before returning plans. |
| `TRACING_ENABLED` | `false` | When `true`, exports OpenTelemetry traces over OTLP/HTTP to an external collector. |
| `LANGSMITH_TRACING` | `false` | When `true`, exports model traces to LangSmith (requires `LANGSMITH_API_KEY`). |
| `METRICS_ENABLED` | `false` | When `true`, serves Prometheus metrics at `GET /metrics` without authentication. |
| `SWAGGER_ENABLED` | `true` | When `true`, serves Swagger UI and OpenAPI documentation at `/swagger/*`. |
| `LOG_LEVEL` | `info` | Minimum log level for structured JSON logging (`debug`, `info`, `warn`, `error`). |
| `GRAPH_FILE` | `""` (unset) | Path to custom curriculum JSON file; defaults to embedded seed graph. |

For the complete list of environment variables, format constraints, and credential resolution details, see [docs/configuration.md](docs/configuration.md).

## Usage

### Health Check

The health endpoint is unauthenticated and reports process liveness.

```sh
curl -i http://localhost:8080/health
```

Response:

```json
{"status":"ok"}
```

The service also exposes `/livez` for process liveness and `/readyz` for traffic readiness.

### Cognitive Shadow Graph

Inspect concept mastery and prerequisite states for a learner.

```sh
curl -i "http://localhost:8080/api/v1/tutor/student/student-42/cognitive-graph?topics=Polymorphism,Recursion" \
  -H "X-Internal-Token: $AXIOM_INTERNAL_TOKEN"
```

Response:

```json
{
  "nodes": [
    {"id": "Classes", "label": "Classes", "cognitive_state": "learning"},
    {"id": "Design Patterns", "label": "Design Patterns", "cognitive_state": "mastered"},
    {"id": "Dynamic Programming", "label": "Dynamic Programming", "cognitive_state": "mastered"},
    {"id": "Functions", "label": "Functions", "cognitive_state": "learning"},
    {"id": "Generics", "label": "Generics", "cognitive_state": "mastered"},
    {"id": "Inheritance", "label": "Inheritance", "cognitive_state": "learning"},
    {"id": "Interfaces", "label": "Interfaces", "cognitive_state": "mastered"},
    {"id": "Merge Sort", "label": "Merge Sort", "cognitive_state": "mastered"},
    {"id": "Polymorphism", "label": "Polymorphism", "cognitive_state": "failed"},
    {"id": "Recursion", "label": "Recursion", "cognitive_state": "failed"}
  ],
  "edges": [
    {"source": "Design Patterns", "target": "Interfaces", "relation": "depends_on"},
    {"source": "Design Patterns", "target": "Polymorphism", "relation": "depends_on"},
    {"source": "Dynamic Programming", "target": "Recursion", "relation": "depends_on"},
    {"source": "Generics", "target": "Polymorphism", "relation": "depends_on"},
    {"source": "Inheritance", "target": "Classes", "relation": "depends_on"},
    {"source": "Merge Sort", "target": "Recursion", "relation": "depends_on"},
    {"source": "Polymorphism", "target": "Inheritance", "relation": "depends_on"},
    {"source": "Recursion", "target": "Functions", "relation": "depends_on"}
  ]
}
```

### Adaptive Study Plan

Generate a personalized study plan for failed concepts. Supplying an `Idempotency-Key` header allows safe retries; subsequent requests with the same key and payload replay the cached response (`Idempotent-Replayed: true`) without repeating model inference.

```sh
curl -i -X POST http://localhost:8080/api/v1/adaptive-plan \
  -H "X-Internal-Token: $AXIOM_INTERNAL_TOKEN" \
  -H "Idempotency-Key: plan-2026-11-06-001" \
  -H "Content-Type: application/json" \
  -d '{
    "student_id": "student-42",
    "course_id": "cs101",
    "failed_topics": ["Polymorphism"],
    "vark_profile": "visual"
  }'
```

Response:

```json
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

### Adaptive Study Plan Streaming

Stream plan generation progress in real time over Server-Sent Events (SSE). Emits `topic` events as each concept plan completes, followed by a final validated `plan` or `error` event:

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

The server sends periodic `: keepalive` comments every 2 seconds. If a client disconnects prematurely, in-flight topic reasoning is cancelled immediately to release concurrency slots and avoid unnecessary token consumption.

### Curriculum Topics

List curriculum concepts with direct prerequisites and dependents:

```sh
curl -i "http://localhost:8080/api/v1/topics?q=poly" \
  -H "X-Internal-Token: $AXIOM_INTERNAL_TOKEN"
```

Inspect the study-ordered prerequisite chain for a specific concept:

```sh
curl -i "http://localhost:8080/api/v1/topics/Polymorphism/path" \
  -H "X-Internal-Token: $AXIOM_INTERNAL_TOKEN"
```

### Model Context Protocol (MCP) Stdio Server

Run the server locally over standard input and output (`stdio`) for MCP-compatible clients:

```sh
axiom-server mcp
```

Exposes `generate_study_plan`, `get_cognitive_graph`, `list_topics`, and `get_topic_path` tools over JSON-RPC.

To enable OpenTelemetry tracing in MCP mode, set `TRACING_ENABLED=true` and configure the exporter endpoint:

```sh
export TRACING_ENABLED=true
export OTEL_EXPORTER_OTLP_ENDPOINT="http://localhost:4318"
```

To enable LangSmith trace export, set `LANGSMITH_TRACING=true` and provide an API key:

```sh
export LANGSMITH_TRACING=true
export LANGSMITH_API_KEY="your-langsmith-key"
export LANGSMITH_PROJECT="your-project" # optional
```

Prompt and completion content capture on trace spans is controlled by `TRACING_CAPTURE_CONTENT` (defaults to `true` when LangSmith is enabled, `false` otherwise).

For client configuration examples, tool schemas, and container execution details, see [docs/mcp.md](docs/mcp.md), [docs/configuration.md](docs/configuration.md), and [docs/operations.md](docs/operations.md).

## Limitations

- **In-Memory Rate Limiting**: When `REDIS_URL` is unset, rate limiting operates per replica in memory, keyed by client IP (masked to `/64` for IPv6). Setting `REDIS_URL` shares rate-limit budgets and plan idempotency records across all replicas. For multi-tenant platform limits and per-tenant quotas, gateway enforcement remains recommended.
- **Client Disconnects on Non-Streaming Requests**: On the non-streaming route (`POST /api/v1/adaptive-plan`), client HTTP disconnects do not cancel in-flight model reasoning; execution continues until `REQUEST_TIMEOUT` expires. The streaming route (`POST /api/v1/adaptive-plan/stream`) detects client disconnects through two-second keepalives and cancels in-flight reasoning promptly.
- **Single Shared Secret**: Authentication relies on a single shared secret without caller identity or per-tenant authorization. Fine-grained authorization, per-caller rate limiting, and multi-tenant isolation belong at the platform or gateway tier.
- **Native Library Initialization**: The BAML CFFI dynamic library is loaded during package initialization before `main` executes; missing or mismatched libraries panic directly to `stderr` in plain text prior to structured JSON logger initialization.
- **Bedrock Region Resolution and Caching**: When using `LLM_PROVIDER=aws-bedrock`, AWS regions resolved dynamically from IMDS cannot be confirmed at startup, and the first resolved region is cached by BAML for the lifetime of the process. Non-Bedrock providers do not perform region resolution.
- **Resource Link Reachability**: Recommended resource URLs are not checked for network reachability by default. Setting `RESOURCE_LINK_CHECK=true` enables outbound HTTP reachability checks that verify URLs and drop non-resolving or refused resources before returning plans.
- **In-Memory Knowledge Graph Immutability**: The curriculum knowledge graph is loaded into memory at startup from embedded data or `GRAPH_FILE` and cannot be modified dynamically at runtime without restarting the service process.
- **MCP Transport Scope**: The Model Context Protocol server currently implements standard input and output (`stdio`) transport exclusively; remote transports such as HTTP Server-Sent Events (SSE) or WebSocket are not supported.
- **Scorecard Governance**: Repository OpenSSF Scorecard branch protection scores reflect a single-maintainer project with zero required external approvals.

For diagnostic procedures and operational troubleshooting, refer to the [Operations Runbook](docs/operations.md).

## Roadmap

- Cancellation propagation for non-streaming HTTP requests when clients disconnect.
- Per-tenant authentication and caller identity verification.
- Dynamic curriculum knowledge graph updates without service restarts.
- Remote transport support for the Model Context Protocol server.
- Fine-grained per-tenant rate limit quotas in shared storage.

## Documentation

| Document | Description |
|---|---|
| [Architecture](docs/architecture.md) | Package layout, component topology, sequence diagrams, resilience parameters, and design decisions |
| [API Reference](docs/api.md) | Authentication schemes, common headers, error envelopes, and endpoint specifications |
| [Configuration](docs/configuration.md) | Exhaustive environment variable table and AWS credential resolution mechanics |
| [Knowledge Graph](docs/knowledge-graph.md) | Subject-Relation-Object data model, relation semantics, seed graph, file schemas, and shadow computation |
| [Model Context Protocol](docs/mcp.md) | Stdio MCP server setup, client configuration, tool specifications, error handling, and testing |
| [Deployment](docs/deployment.md) | Multi-stage Docker build, non-root execution, compose configuration, and operating instructions |
| [Development](docs/development.md) | Prerequisites, make targets, BAML and Swagger generation, and test suite execution |
| [Operations Runbook](docs/operations.md) | Diagnostic runbooks for open breakers, throttling, credential errors, native library panics, and production metrics |

## Contributing and Support

For contribution guidelines and coding standards, refer to [CONTRIBUTING.md](CONTRIBUTING.md).
For support requests, bug reports, and general questions, refer to [SUPPORT.md](SUPPORT.md).
For security policy and vulnerability disclosures, refer to [SECURITY.md](SECURITY.md).
For community standards, refer to [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).
For release history, see [CHANGELOG.md](CHANGELOG.md).

## Citation

To cite AxiomEngine in publications or research metadata, see [CITATION.cff](CITATION.cff).

## Acknowledgements

AxiomEngine is built on open-source libraries and systems:
- [Fiber](https://gofiber.io/) (`github.com/gofiber/fiber/v3`) - HTTP framework
- [BAML](https://github.com/boundaryml/baml) (`github.com/boundaryml/baml`) - structured generation runtime and client generator
- [Model Context Protocol Go SDK](https://github.com/modelcontextprotocol/go-sdk) (`github.com/modelcontextprotocol/go-sdk`) - MCP server implementation
- [OpenTelemetry Go SDK](https://opentelemetry.io/) (`go.opentelemetry.io/otel`) - distributed tracing and telemetry
- [go-redis](https://github.com/redis/go-redis) (`github.com/redis/go-redis/v9`) - Redis client for distributed state
- [gobreaker](https://github.com/sony/gobreaker) (`github.com/sony/gobreaker/v2`) - circuit breaker implementation
- [Prometheus Go client](https://prometheus.io/) (`github.com/prometheus/client_golang`) - Prometheus metrics collection and exposition
- [Swag](https://github.com/swaggo/swag) (`github.com/swaggo/swag`) - Swagger and OpenAPI specification generator

## License

This project is licensed under the MIT License. See [LICENSE](LICENSE) for details.
