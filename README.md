# axiom-reasoning-svc

## System Abstract

**axiom-reasoning-svc** (AxiomEngine) is a production-hardened Go microservice that functions as a GraphRAG Reasoning Engine for EdTech platforms. It combines an in-memory curriculum knowledge graph with Amazon Bedrock (Nova Micro) large language model inference -- orchestrated through BAML (Boundary AI Markup Language) -- to generate personalized, prerequisite-aware adaptive study plans for students.

The service exposes two primary API capabilities:

1. **Adaptive Plan Generation** -- A multi-stage reasoning pipeline that extracts a subgraph via BFS, performs topological prerequisite sorting, fans out parallel LLM calls per failed topic, merges and deduplicates results, validates against the knowledge graph to prevent hallucinations, and returns enriched JSON with pipeline telemetry.

2. **Cognitive Shadow Graph** -- A teacher-dashboard endpoint that produces a full knowledge-graph visualization annotated with per-student mastery status (failed / learning / mastered), compatible with Cytoscape.js and D3.js frontends.

### Core Technology Stack

| Layer | Technology | Version |
|-------|-----------|---------|
| Language | Go | 1.25+ |
| HTTP Framework | github.com/gofiber/fiber/v2 | 2.52.x |
| AI Orchestration | BAML (baml-cli) | 0.220.0 |
| AI Provider | Amazon Bedrock (amazon.nova-micro-v1:0) | -- |
| JSON Serialization | github.com/goccy/go-json | 0.10.x |
| Circuit Breaker | github.com/sony/gobreaker/v2 | 2.4.x |
| Concurrency | golang.org/x/sync/errgroup | -- |
| Containerization | Docker (multi-stage) + Docker Compose | -- |

---

## Repository Structure

```
axiom-reasoning-svc/
├── baml_src/                      # BAML schema and prompt definitions
│   ├── generators.baml            #   Go generator configuration
│   ├── clients.baml               #   Bedrock client + retry policy
│   └── resume.baml                #   Domain classes + LLM functions
├── baml_client/                   # Auto-generated Go client (do not edit)
├── cmd/
│   └── server/
│       └── main.go                # Application entrypoint
├── internal/
│   ├── api/
│   │   └── handlers.go            # Fiber HTTP handlers and route registration
│   ├── domain/
│   │   └── models.go              # Canonical DTOs (request, response, graph types)
│   ├── graph/
│   │   ├── memory.go              # In-memory knowledge graph (BFS, DFS, shadow)
│   │   ├── memory_test.go         # Unit tests for graph traversal
│   │   └── cognitive_test.go      # Unit tests for cognitive shadow graph
│   └── service/
│       ├── reasoning.go           # Six-stage reasoning pipeline orchestrator
│       └── pipeline.go            # Merge, dedup, validation, type mapping
├── docs/                          # Project documentation
│   ├── architecture.md            # Component and sequence diagrams
│   ├── api.md                     # Exhaustive API specification
│   └── deployment.md              # Containerization and operations
├── Dockerfile                     # Multi-stage build (BAML gen + Go build + runtime)
├── docker-compose.yml             # Single-service orchestration with .env injection
├── .env.example                   # Template for required environment variables
├── Makefile                       # Build, test, generate, and Docker targets
├── test_live.sh                   # End-to-end curl smoke test
├── test_ratelimit.sh              # Rate limiter validation script
├── go.mod / go.sum                # Go module definition and checksums
└── README.md                      # This document
```

---

## Prerequisites and Toolchain

| Tool | Minimum Version | Installation |
|------|----------------|-------------|
| Go | 1.25.0 | https://go.dev/dl/ |
| Docker | 27.x | https://docs.docker.com/get-docker/ |
| Docker Compose | 2.29+ | Bundled with Docker Desktop |
| baml-cli | 0.220.0 | `go install github.com/boundaryml/baml/baml-cli@latest` |
| goimports | latest | `go install golang.org/x/tools/cmd/goimports@latest` |

---

## Environment Configuration

Create a `.env` file in the repository root (see `.env.example`). The following variables are consumed at runtime:

| Variable | Type | Required | Default | Description |
|----------|------|----------|---------|-------------|
| `AWS_REGION` | string | Yes | -- | AWS region where Amazon Bedrock Nova Micro is enabled (e.g., `us-east-1`). |
| `AWS_ACCESS_KEY_ID` | string | Yes | -- | IAM access key with `bedrock:InvokeModel` permission. |
| `AWS_SECRET_ACCESS_KEY` | string | Yes | -- | IAM secret key corresponding to `AWS_ACCESS_KEY_ID`. |
| `PORT` | integer | No | `8080` | TCP port on which the Fiber HTTP server listens. |

---

## Build and Run Instructions

### Local Execution

```bash
# 1. Generate BAML Go client
baml-cli generate
gofmt -w baml_client
goimports -w baml_client

# 2. Build
go build -o bin/axiom-server ./cmd/server

# 3. Run (ensure .env is sourced or variables are exported)
./bin/axiom-server
```

### Docker Compose Execution

```bash
# Build and start (reads .env automatically)
docker compose up -d --build

# View structured JSON logs
docker compose logs -f axiom-reasoning

# Stop and remove containers
docker compose down
```

### Test Execution

```bash
# Unit tests (all packages)
go test ./... -v -count=1

# Live end-to-end smoke test (requires running server)
bash test_live.sh

# Rate limiter validation (requires running server)
bash test_ratelimit.sh
```

---

## API Quick Reference

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/health` | Liveness probe. Returns `{"status": "ok"}`. |
| `POST` | `/api/v1/adaptive-plan` | Generate a personalized adaptive study plan. |
| `GET` | `/api/v1/tutor/student/:student_id/cognitive-graph?topics=...` | Retrieve the Cognitive Shadow Graph for a student. |

For exhaustive request/response schemas and error codes, see [docs/api.md](docs/api.md).

---

## Documentation Index

| Document | Area | Description |
|----------|-----------|-------------|
| [docs/architecture.md](docs/architecture.md) | Elaboration | Component diagrams, pipeline sequence, data topologies, SRE patterns |
| [docs/api.md](docs/api.md) | Construction | Exhaustive endpoint specification with schemas and error codes |
| [docs/deployment.md](docs/deployment.md) | Transition | Dockerfile strategy, observability, graceful shutdown |
