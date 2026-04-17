# 01 – Vision

## Purpose

AxiomEngine is a Go microservice that transforms a student's list of failed assessment topics into a personalized, prerequisite-ordered adaptive study plan. It solves the problem of generic remediation recommendations by combining an in-memory knowledge graph (GraphRAG) with Amazon Nova Micro via BAML to generate VARK-aligned learning resources for each concept the student has not mastered. A dedicated microservice is justified over embedding this logic in the core-lms-backend Django monolith because the reasoning pipeline is computationally distinct (graph traversal + parallel LLM fan-out with a 20-second deadline), requires a circuit breaker to isolate LLM backend failures, and benefits from an independent deployment lifecycle on Railway using a CGO-enabled Docker build that the Django runtime cannot host.

## Scope

**In scope**

- In-memory directed knowledge graph of CS curriculum prerequisite relationships (20 seed triples, subject–relation–object format)
- BFS subgraph extraction from student failed topics (depth-2 bidirectional traversal)
- Topological prerequisite sort per failed topic (DFS post-order)
- Parallel BAML fan-out to Amazon Nova Micro (`amazon.nova-micro-v1:0`) via `baml_client`
- Merge, deduplication, and hallucination validation of LLM-generated study items
- Cognitive shadow graph generation (all graph nodes annotated with `failed` / `learning` / `mastered` status per student)
- REST API exposure over Fiber v2 with rate limiting, security headers, and structured logging

**Out of scope**

- Authentication and authorization (the service is internal; core-lms-backend is the trust boundary)
- User management, student record persistence, or course data storage
- Frontend rendering (the cognitive graph response is Cytoscape.js/D3.js-compatible but rendering is the caller's responsibility)
- LLM prompt management UI or A/B testing infrastructure
- Database connections of any kind

## Quality Attributes

| Attribute     | Target                                    | How It Is Achieved                                                                                                                                   |
|---------------|-------------------------------------------|------------------------------------------------------------------------------------------------------------------------------------------------------|
| Latency       | p95 plan generation ≤ 20 s               | Handler enforces a 20 s `context.WithTimeout`; per-LLM-call timeout is 15 s; parallel fan-out via `errgroup` bounds wall-clock time to the slowest single call |
| Resilience    | Graceful degradation on LLM backend failures | `sony/gobreaker` circuit breaker trips after 3 consecutive failures; half-open state allows 2 probe requests every 15 s; open state returns HTTP 503 immediately without hitting Bedrock |
| Throughput    | 50 requests / minute per IP               | Sliding-window rate limiter (`limiter.SlidingWindow{}`) configured in `cmd/server/main.go` with `Max: 50, Expiration: 1 * time.Minute`               |
| Correctness   | No hallucinated topic names in responses  | Hallucination-guard stage (`validateAgainstGraph` in `internal/service/pipeline.go`) drops any LLM-generated `PlanItem` whose `Topic` field is absent from `InMemoryGraph.NodeExists` |
| Portability   | No external database dependency           | All graph state is held in `InMemoryGraph` (Go maps, no CGO data layer); the only external I/O is Amazon Bedrock via BAML's native FFI library       |
