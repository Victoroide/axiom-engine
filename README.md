# AxiomEngine

AxiomEngine is a Go microservice that transforms a student's list of failed assessment topics into a personalized, prerequisite-ordered adaptive study plan. It combines an in-memory CS curriculum knowledge graph (GraphRAG) with Amazon Nova Micro via BAML to generate VARK-aligned learning resources. The service is called exclusively by the Django service over HTTP.

## Quick Start

```bash
# Generate BAML Go client (required before first build)
baml-cli generate

# Set AWS credentials
export AWS_REGION=us-east-1
export AWS_ACCESS_KEY_ID=<your-key>
export AWS_SECRET_ACCESS_KEY=<your-secret>

# Build and run
go build -o bin/axiom-server ./cmd/server
./bin/axiom-server
```

Or with Docker Compose:

```bash
docker compose up --build
```

## API

Full API reference: [docs/reference/03_api_reference.md](docs/reference/03_api_reference.md)

Swagger UI is available at `http://localhost:8080/swagger/index.html` when the service is running.

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/health` | Liveness probe — returns `{"status":"ok"}` |
| `POST` | `/api/v1/adaptive-plan` | Generate adaptive study plan from failed topics and VARK profile |
| `GET` | `/api/v1/tutor/student/:student_id/cognitive-graph?topics=...` | Cognitive shadow graph for teacher dashboard |

## Documentation

| Document | Description |
|----------|-------------|
| [docs/reference/01_vision.md](docs/reference/01_vision.md) | Purpose, scope, and quality attribute targets |
| [docs/reference/02_architecture.md](docs/reference/02_architecture.md) | Component diagram, request lifecycle, pipeline stages, resilience, rate limiting |
| [docs/reference/03_api_reference.md](docs/reference/03_api_reference.md) | Endpoint specifications, request/response schemas, error codes, curl examples |
| [docs/reference/04_concept_topology.md](docs/reference/04_concept_topology.md) | In-memory knowledge graph structure, operations, and seed data |
| [docs/reference/05_testing.md](docs/reference/05_testing.md) | Test strategy, test function inventory, coverage report |
| [docs/reference/06_deployment.md](docs/reference/06_deployment.md) | Environment variables, Dockerfile, Railway config, local dev commands |

## Environment Variables

See [docs/reference/06_deployment.md](docs/reference/06_deployment.md) for the full table.

| Variable | Required | Description |
|----------|----------|-------------|
| `AWS_REGION` | Yes | AWS region for Amazon Bedrock (e.g., `us-east-1`) |
| `AWS_ACCESS_KEY_ID` | Yes | IAM key with `bedrock:InvokeModel` permission |
| `AWS_SECRET_ACCESS_KEY` | Yes | IAM secret for the above key |

## Testing

```bash
go test ./... -v -count=1
```
