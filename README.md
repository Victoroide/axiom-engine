# AxiomEngine

[![CI](https://github.com/Victoroide/axiom-engine/actions/workflows/ci.yml/badge.svg)](https://github.com/Victoroide/axiom-engine/actions/workflows/ci.yml)
[![CodeQL](https://github.com/Victoroide/axiom-engine/actions/workflows/codeql.yml/badge.svg)](https://github.com/Victoroide/axiom-engine/actions/workflows/codeql.yml)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/Victoroide/axiom-engine/badge)](https://scorecard.dev/viewer/?uri=github.com/Victoroide/axiom-engine)
[![Go Version](https://img.shields.io/badge/go-1.27.3-blue.svg)](https://golang.org)
[![License: MIT](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)

AxiomEngine is a Go microservice that generates personalized, prerequisite-ordered adaptive study plans from student assessment deficits. It combines an in-memory curriculum knowledge graph with Amazon Bedrock model inference executed through a generated BAML client. The service exposes HTTP endpoints for study plan generation, cognitive shadow graph visualization, and system health checks.

## Features

- **Knowledge-graph guided reasoning**: Extracts prerequisite chains and local subgraphs using depth-first and breadth-first search over an immutable graph.
- **VARK-aligned resource mapping**: Generates study recommendations tailored to visual, auditory, read-write, or kinesthetic learning modalities.
- **Concurrent bounded fan-out**: Dispatches parallel per-topic LLM calls bounded per request and per process, with circuit breaker protection.
- **Hallucination validation**: Verifies model outputs against known graph nodes, dropping unrecognized concepts and concepts outside the requested topics and their prerequisites before assigning priorities.
- **Cognitive shadow graphs**: Computes graph neighborhoods classifying concepts into failed, learning, and mastered states for frontend visualization.
- **Configurable token authentication**: Restricts API access using constant-time verification of internal shared tokens.
- **Optional resource link validation**: Verifies recommended resource URLs with outbound HEAD/GET checks and SSRF protections when enabled (`RESOURCE_LINK_CHECK=true`), dropping unreachable links.

## Request Path

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/diagrams/request-path-dark.webp">
  <img src="docs/assets/diagrams/request-path-light.webp" width="100%" alt="Request path: a client calls the router and middleware, which serves the public routes and passes /api/v1 requests through token authentication and the rate limiter to the adaptive plan and cognitive graph handlers. The plan handler runs the reasoning pipeline, which fans out per topic through the circuit breaker to the BAML client and Amazon Bedrock; the pipeline and the cognitive graph handler read the in-memory graph.">
</picture>

## Quick Start

### Docker

1. Copy the example environment file:

```sh
cp .env.example .env
```

Generate an internal token (for example `openssl rand -hex 32`) and set `AXIOM_INTERNAL_TOKEN=<generated value>` in `.env`.

2. Build and run the container with Docker Compose:

```sh
docker compose up --build
```

The container starts with non-root UID/GID `65532:65532` and publishes port 8080 on loopback (`http://127.0.0.1:8080`).

### Local Development

Prerequisites: Go 1.27.3, CGO toolchain, and AWS credentials.

1. Set the required AWS credentials and service token:

```sh
export AWS_REGION=us-east-1
export AWS_ACCESS_KEY_ID="your-access-key-id"
export AWS_SECRET_ACCESS_KEY="your-secret-access-key"
export AXIOM_INTERNAL_TOKEN="$(openssl rand -hex 32)"
```

2. Start the application:

```sh
make run
```

Running `./bin/axiom-server` directly requires `BAML_LIBRARY_PATH` and `BAML_LIBRARY_DISABLE_DOWNLOAD=true`. Without them, BAML loads a user-cache copy or downloads an unverified one. Prefer `make run`.

The server binds `:8080`, accepting connections on all interfaces (`http://0.0.0.0:8080`). When `SWAGGER_ENABLED=true` (the default), interactive documentation is available at `http://127.0.0.1:8080/swagger/index.html`.

## Minimal API Examples

### Health Check

The health endpoint is unauthenticated and reports process liveness.

```sh
curl -i http://localhost:8080/health
```

Response:

```json
{"status":"ok"}
```

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

## Limitations

- **In-Memory Rate Limiting**: When `REDIS_URL` is unset, rate limiting operates per replica in memory, keyed by client IP (masked to `/64` for IPv6). Setting `REDIS_URL` shares rate-limit budgets and plan idempotency records across all replicas. For multi-tenant platform limits, gateway enforcement remains recommended.
- **Client Disconnects**: Client HTTP disconnects do not cancel in-flight Bedrock reasoning. Work continues until `REQUEST_TIMEOUT` expires.
- **Single Shared Secret**: Authentication relies on a single shared secret without caller identity or per-tenant authorization. Fine-grained authorization and multi-tenant isolation belong at the platform or gateway tier.
- **Native Library Initialization**: The BAML CFFI dynamic library is loaded during package initialization before `main` executes; missing or mismatched libraries panic directly to `stderr` in plain text prior to structured JSON logger initialization.
- **Region Resolution and Caching**: Bedrock regions resolved dynamically from IMDS cannot be confirmed at startup, and the first resolved region is cached for the process lifetime.
- **Resource Link Reachability**: Recommended resource URLs are not checked for network reachability by default. Setting `RESOURCE_LINK_CHECK=true` enables outbound HTTP reachability checks that verify URLs and drop non-resolving or refused resources before returning plans.
- **Scorecard Governance**: Repository OpenSSF Scorecard branch protection and code review scores reflect a single-maintainer project with zero required external approvals.

For diagnostic runbooks and operational troubleshooting, refer to the [Operations Runbook](docs/operations.md).

## Documentation

| Document | Description |
|---|---|
| [Architecture](docs/architecture.md) | Package layout, component topology, sequence diagrams, resilience parameters, and design decisions |
| [API Reference](docs/api.md) | Authentication schemes, common headers, error envelopes, and endpoint specifications |
| [Configuration](docs/configuration.md) | Exhaustive environment variable table and AWS credential resolution mechanics |
| [Knowledge Graph](docs/knowledge-graph.md) | Subject-Relation-Object data model, relation semantics, seed graph, file schemas, and shadow computation |
| [Deployment](docs/deployment.md) | Multi-stage Docker build, non-root execution, compose configuration, and operating instructions |
| [Development](docs/development.md) | Prerequisites, make targets, BAML and Swagger generation, and test suite execution |
| [Operations Runbook](docs/operations.md) | Diagnostic runbooks for open breakers, throttling, credential errors, native library panics, and production metrics |

For project governance and contribution standards, refer to [CONTRIBUTING.md](CONTRIBUTING.md), [SECURITY.md](SECURITY.md), [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md), and [CHANGELOG.md](CHANGELOG.md).

## License

This project is licensed under the MIT License. See [LICENSE](LICENSE) for details.
