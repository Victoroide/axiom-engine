# 02 – Architecture

## Component Diagram

```
┌──────────────────────────────────────────────────────────────────┐
│  core-lms-backend (Django)                                     │
│  AxiomEngineClient                                               │
└───────────────────────┬──────────────────────────────────────────┘
                        │ HTTP POST /api/v1/adaptive-plan
                        │ HTTP GET  /api/v1/tutor/student/:id/cognitive-graph
                        ▼
┌──────────────────────────────────────────────────────────────────┐
│  AxiomEngine  (axiom-reasoning-svc)                              │
│                                                                  │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │  Fiber v2  (cmd/server/main.go)                         │    │
│  │  Middleware: helmet · rate-limiter · logger · recover   │    │
│  └───────────────────────┬─────────────────────────────────┘    │
│                          │                                       │
│          ┌───────────────┴──────────────┐                       │
│          ▼                              ▼                        │
│  ┌───────────────┐            ┌──────────────────────┐          │
│  │  Handler      │            │  Handler             │          │
│  │  handleAdapt- │            │  handleCognitive-    │          │
│  │  ivePlan      │            │  Graph               │          │
│  └───────┬───────┘            └──────────┬───────────┘          │
│          │                               │                       │
│          ▼                               ▼                       │
│  ┌───────────────────────┐    ┌──────────────────────┐          │
│  │  ReasoningService     │    │  InMemoryGraph        │          │
│  │  (service/reasoning)  │    │  (graph/memory)       │          │
│  │                       │    │                       │          │
│  │  Stage 1: BFS subgraph│◄───│  20 CS curriculum     │          │
│  │  Stage 2: Topo sort   │    │  triples (seed)       │          │
│  │  Stage 3: BAML fanout │    └──────────────────────┘          │
│  │  Stage 4: Merge/dedup │                                       │
│  │  Stage 5: Hallucination│                                      │
│  │           guard        │                                      │
│  │  Stage 6: Enrich resp │                                       │
│  └───────────┬───────────┘                                       │
│              │                                                    │
│              ▼                                                    │
│  ┌───────────────────────────────────────────────────────┐      │
│  │  BAML Client  (baml_client/)                          │      │
│  │  GenerateTopicPlan()                                  │      │
│  │  CircuitBreaker: sony/gobreaker "bedrock-nova-micro"  │      │
│  └───────────────────────┬───────────────────────────────┘      │
└──────────────────────────┼───────────────────────────────────────┘
                           │ HTTPS  (AWS Bedrock API)
                           ▼
              ┌────────────────────────┐
              │  Amazon Nova Micro     │
              │  amazon.nova-micro-v1  │
              └────────────────────────┘
```

## Request Lifecycle

The following steps describe a single `POST /api/v1/adaptive-plan` request from receipt to response.

1. **Fiber receives the HTTP request** — `cmd/server/main.go`: `app.Listen`; middleware chain runs: `helmet`, `limiter`, `logger`, `recover`.
2. **Rate limit check** — `cmd/server/main.go` limiter middleware (50 req/min per IP). Exceeded → 429 immediately.
3. **Route dispatch** — `internal/api/handlers.go`: `Handler.RegisterRoutes` → `handleAdaptivePlan`.
4. **Request body parsing and validation** — `handlers.go:handleAdaptivePlan`; `c.BodyParser` into `domain.AdaptivePlanRequest`; validates `failed_topics` non-empty, `vark_profile` non-empty.
5. **Context deadline set** — `handlers.go`: `context.WithTimeout(c.UserContext(), 20s)`.
6. **Stage 1 – BFS subgraph extraction** — `internal/service/reasoning.go:GeneratePlan` calls `internal/graph/memory.go:GetLocalSubgraph(req.FailedTopics, depth=2)`.
7. **Stage 2 – Topological prerequisite sort** — `reasoning.go:GeneratePlan` calls `graph/memory.go:GetPrerequisiteChain(topic)` for each failed topic (DFS post-order traversal).
8. **Stage 3 – Parallel BAML fan-out** — `reasoning.go:GeneratePlan` launches one `errgroup` goroutine per failed topic; each goroutine calls `baml_client/functions.go:GenerateTopicPlan` wrapped in `sony/gobreaker` circuit breaker.
9. **Amazon Bedrock call** — BAML native FFI invokes `amazon.nova-micro-v1:0` with graph context, topic, prerequisite chain, VARK profile.
10. **Stage 4 – Merge and deduplication** — `internal/service/pipeline.go:mergeAndDeduplicate`; dedup key is topic name; priorities renumbered 1-based.
11. **Stage 5 – Hallucination validation** — `pipeline.go:validateAgainstGraph`; drops items whose `Topic` is absent from `graph/memory.go:NodeExists`; priorities renumbered.
12. **Stage 6 – Response enrichment** — `reasoning.go:GeneratePlan` assembles `domain.PlanResponse` with `domain.PipelineMeta` telemetry (subgraph tuple count, LLM latency, total latency).
13. **HTTP response** — `handlers.go:handleAdaptivePlan` returns `c.Status(200).JSON(plan)`.

## Pipeline Stages

| Stage | Description | File | Function |
|-------|-------------|------|----------|
| Subgraph extraction | Bidirectional BFS from failed topics at depth 2; returns all reachable `GraphTuple` triples | `internal/graph/memory.go` | `GetLocalSubgraph` |
| Topological sort | DFS post-order per failed topic; produces ordered prerequisite chain | `internal/graph/memory.go` | `GetPrerequisiteChain` |
| BAML fan-out | One `GenerateTopicPlan` call per failed topic executed concurrently via `errgroup`; 15 s per-call timeout | `internal/service/reasoning.go` | `GeneratePlan` (stage 3 block) |
| Merge and deduplication | Combines all per-topic `AdaptiveStudyPlan` results; first-occurrence wins on duplicate topics; re-numbers priorities | `internal/service/pipeline.go` | `mergeAndDeduplicate` |
| Hallucination validation | Drops `PlanItem` entries whose `Topic` is absent from the knowledge graph; re-numbers priorities | `internal/service/pipeline.go` | `validateAgainstGraph` |
| Response enrichment | Assembles `domain.PlanResponse` with `PipelineMeta` fields (subgraph size, LLM latency ms, total latency ms) | `internal/service/reasoning.go` | `GeneratePlan` (stage 6 block) |

## Resilience

The `sony/gobreaker` circuit breaker wraps every call to `baml_client.GenerateTopicPlan` in `internal/service/reasoning.go:NewReasoningService`.

| Parameter | Value | Effect |
|-----------|-------|--------|
| Name | `"bedrock-nova-micro"` | Identifies breaker in log events |
| MaxRequests (half-open) | `2` | At most 2 probe requests allowed when transitioning from open to half-open |
| Interval (closed window) | `30 s` | Failure counter resets every 30 s while the breaker is closed |
| Timeout (open → half-open) | `15 s` | Breaker stays open for 15 s before allowing probe requests |
| ReadyToTrip | `ConsecutiveFailures >= 3` | Three consecutive Bedrock failures trip the breaker to open state |
| OnStateChange | `slog.Warn("circuit_breaker.state_change", ...)` | State transitions are logged with from/to labels |

When the breaker is open, `Execute` returns `gobreaker.ErrOpenState`. The handler maps this sentinel to `service.ErrCircuitOpen`, which produces HTTP 503 `{"error":"service_unavailable"}`.

## Rate Limiting

| Property | Value |
|----------|-------|
| Middleware | `github.com/gofiber/fiber/v2/middleware/limiter` |
| Algorithm | Sliding window (`limiter.SlidingWindow{}`) |
| Limit | 50 requests per minute |
| Key | Client IP (`c.IP()`) |
| Enforcement point | Global — applied before route dispatch in `cmd/server/main.go` |
| Exceeded response | HTTP 429 `{"error":"rate_limit_exceeded","details":"Too many requests. Max 50 per minute."}` |
