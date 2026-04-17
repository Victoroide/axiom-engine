# 05 – Testing

## Test Strategy

Testing is organized in two layers:

**Layer 1 – Go unit tests (graph algorithms)**  
The `internal/graph` package has comprehensive table-driven tests covering every exported function: BFS subgraph extraction, DFS topological sort, node existence lookup, cognitive shadow graph generation, and graph initialization. These tests run entirely in-process with no external dependencies.

**Layer 2 – Integration boundary (Django mock)**  
The HTTP transport layer (`internal/api`) and the reasoning pipeline (`internal/service`) are exercised at the `AxiomEngineClient` boundary in core-lms-backend using a mocked HTTP server. These tests live in the Django repository and are outside this codebase. Within AxiomEngine, the service and handler packages currently have no Go test files; correctness of the pipeline integration is validated end-to-end in staging.

## Test Files

| File | Test Function | What It Covers |
|------|---------------|----------------|
| `internal/graph/memory_test.go` | `TestGetLocalSubgraph` | BFS subgraph extraction: 6 sub-cases (depth 0, depth 1, unknown topic, multiple topics, depth 2 traversal, empty input) |
| `internal/graph/memory_test.go` | `TestNewInMemoryGraph_DefaultTuples` | Seed dataset: asserts at least 15 default tuples are loaded at startup |
| `internal/graph/memory_test.go` | `TestAdd` | Tuple insertion: verifies `All()` count increments by exactly 1 |
| `internal/graph/memory_test.go` | `TestNodeExists` | Node presence lookup: 5 cases including two false cases (unknown node, empty string) |
| `internal/graph/memory_test.go` | `TestGetPrerequisiteChain` | DFS prerequisite ordering: 4 cases (deep chain, leaf node, single dependency, unknown topic) |
| `internal/graph/cognitive_test.go` | `TestGenerateCognitiveShadow` | Cognitive graph generation: 4 cases (single failed topic, multiple failed topics, no failed topics → all mastered, unknown topic → no crash) |

## How to Run

**All tests**

```bash
go test ./... -v -count=1
```

**Graph algorithm tests only**

```bash
go test ./internal/graph/... -v -count=1
```

**Run a specific test by name**

```bash
go test ./internal/graph/... -run TestGetLocalSubgraph -v
go test ./internal/graph/... -run TestGetPrerequisiteChain -v
go test ./internal/graph/... -run TestGenerateCognitiveShadow -v
```

**No environment variables are required to run the graph tests.** The graph package is self-contained and does not call AWS or any external service.

The service and BAML packages require live AWS credentials to exercise `baml_client.GenerateTopicPlan`. Set the following before running any integration test that reaches Bedrock:

```bash
export AWS_REGION=us-east-1
export AWS_ACCESS_KEY_ID=<your-key-id>
export AWS_SECRET_ACCESS_KEY=<your-secret-key>
```

## Coverage

Run with:

```bash
go test ./... -coverprofile=coverage.out -count=1
go tool cover -func=coverage.out
```

| Package | Coverage |
|---------|----------|
| `axiom-reasoning-svc/internal/graph` | **98.7%** |
| `axiom-reasoning-svc/internal/service` | 0.0% (no Go test files; tested via Django integration) |
| `axiom-reasoning-svc/internal/api` | 0.0% (no Go test files; tested via Django integration) |
| `axiom-reasoning-svc/internal/domain` | n/a (only type definitions) |
| `axiom-reasoning-svc/cmd/server` | 0.0% (entrypoint; covered by smoke tests in CI) |
| `axiom-reasoning-svc/baml_client/*` | 0.0% (generated code; covered by BAML framework tests) |
| **Total** | **8.9%** |

The 98.7% figure for `internal/graph` covers all graph traversal algorithms that are performance- and correctness-critical. The one uncovered branch in `GetPrerequisiteChain` is the cycle-guard path (the topic appears in visited on re-entry), which cannot be triggered by the acyclic seed data.
