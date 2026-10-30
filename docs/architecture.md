# Architecture

## System Components

AxiomEngine organizes responsibilities into distinct Go packages under `cmd/` and `internal/`, wired together at startup in `cmd/server/main.go`.

```mermaid
flowchart TD
    Client["Client / Upstream Caller"] -->|"HTTP Request"| Router["Fiber v3 Router & Middleware<br/>(RequestID, Logger, Recover, Helmet, Limiter, Auth)"]

    subgraph ServerRoutes ["Server Boundary (internal/server)"]
        Router --> H_Health["Health Handler<br/>GET /health"]
        Router --> H_Swagger["Swagger UI<br/>GET /swagger/*"]
        Router --> H_Doc["Swagger Spec<br/>GET /swagger/doc.json"]
    end

    subgraph API ["Transport Boundary (internal/api)"]
        Router --> H_Cognitive["Cognitive Graph Handler<br/>GET /api/v1/tutor/student/:id/cognitive-graph"]
        Router --> H_Plan["Adaptive Plan Handler<br/>POST /api/v1/adaptive-plan"]
    end

    subgraph Service ["Reasoning Orchestration (internal/service)"]
        H_Plan --> S_Validate["Request Validation<br/>(Identifiers, VARK, Max Topics)"]
        S_Validate --> S_Reasoning["ReasoningService.GeneratePlan"]
        S_Reasoning --> S_Fanout["Parallel Fan-Out (errgroup)<br/>plans[i] = items"]
        S_Fanout --> S_Merge["mergeAndDeduplicate"]
        S_Merge --> S_Filter["validateAgainstGraph<br/>(Prerequisite Ordering & Hallucination Guard)"]
        S_Fanout -.-> Breaker["Circuit Breaker<br/>(bedrock-nova-micro)"]
    end

    subgraph Graph ["Graph Engine (internal/graph)"]
        H_Cognitive --> G_Shadow["GenerateCognitiveShadow<br/>(depth 1, state classification)"]
        S_Validate -.-> G_Exists["NodeExists"]
        S_Reasoning -.-> G_Subgraph["GetLocalSubgraph (depth 2 BFS)"]
        S_Fanout -.-> G_Chain["GetPrerequisiteChain (DFS)"]
        S_Filter -.-> G_Exists
        G_Shadow -.-> G_Store[("InMemoryGraph<br/>(Forward & Reverse Maps)")]
        G_Subgraph -.-> G_Store
        G_Chain -.-> G_Store
    end

    subgraph LLM ["Model Adapter (internal/llm)"]
        Breaker --> Adapter["BAML Adapter (service.Planner)"]
        Adapter --> BAMLClient["baml.GenerateTopicPlan<br/>(baml_client)"]
        BAMLClient --> NativeLib["BAML CFFI Engine<br/>(libbaml_cffi.so)"]
        NativeLib --> Bedrock["Amazon Bedrock<br/>(amazon.nova-micro-v1:0)"]
    end
```

### Package Layout

| Package / Directory | Responsibility |
|---|---|
| `cmd/server` | Entry point, healthcheck command, configuration loading, graph initialization, signal handling, and server execution. |
| `internal/api` | HTTP handler registration, request binding, parameter validation, and status code translation. |
| `internal/config` | Environment variable parsing, validation bounds, and fallback defaults. |
| `internal/domain` | Canonical data transfer objects (DTOs), API models, graph tuples, telemetry structures, and error envelopes. |
| `internal/graph` | In-memory directed knowledge graph, forward and reverse adjacency indexes, seed loader, BFS subgraph extraction, DFS prerequisite chains, and cognitive shadow computation. |
| `internal/llm` | Adapter converting domain structures to and from generated BAML types and invoking model execution. |
| `internal/server` | Fiber v3 application initialization, middleware chain, private Swagger spec registration, and graceful shutdown lifecycle. |
| `internal/service` | Reasoning orchestration pipeline, input validation, circuit breaker wrapping, parallel topic fan-out, item deduplication, and hallucination filtering. |
| `baml_client` | Generated Go client code for Boundary ML BAML model invocations and data types. |
| `baml_src` | BAML schema templates, prompt functions, and client configuration for Amazon Bedrock. |
| `docs` | Swagger 2.0 specifications (`swagger.json`, `swagger.yaml`), generated Go doc registration, and technical documentation. |
| `test/e2e` | Headless browser end-to-end tests using Go and Rod verifying Swagger UI operations and interactive execution. |

---

## Adaptive Plan Sequence

The `POST /api/v1/adaptive-plan` endpoint executes a multi-stage reasoning pipeline that combines graph traversal, parallel LLM inference, and graph-guided output validation.

```mermaid
sequenceDiagram
    autonumber
    actor Caller as Client Service
    participant MW as Middleware Chain
    participant Handler as AdaptivePlanHandler
    participant Service as ReasoningService
    participant Graph as InMemoryGraph
    participant Breaker as Circuit Breaker
    participant Planner as BAML Planner
    participant Bedrock as Amazon Bedrock

    Caller->>MW: POST /api/v1/adaptive-plan
    Note over MW: Verify Token (if enabled), Body Limit (64 KiB), Rate Limit (/api/v1 only)

    MW->>Handler: Dispatch Request
    Note over Handler: Bind JSON & set request timeout deadline

    Handler->>Service: GeneratePlan(ctx, req)
    Service->>Service: validateRequest(req)
    Note over Service: Check VARK, student_id, course_id, max topics

    Service->>Graph: ValidateTopics(failedTopics)
    alt Unknown Topic Found
        Graph-->>Service: Unknown topics error
        Service-->>Handler: ValidationError
        Handler-->>Caller: 422 Unprocessable Entity
    end

    Service->>Graph: GetLocalSubgraph(failedTopics, depth=2)
    Graph-->>Service: Return local GraphTuple triples

    par Parallel per-topic fan-out via errgroup
        Service->>Graph: GetPrerequisiteChain(topic_i)
        Graph-->>Service: Return ordered prerequisite nodes
        Service->>Breaker: Execute(plannerCall)
        alt Circuit Open
            Breaker-->>Service: ErrOpenState / ErrTooManyRequests
            Service-->>Handler: ErrCircuitOpen
            Handler-->>Caller: 503 Service Unavailable
        else Circuit Closed / Half-Open
            Breaker->>Planner: GenerateTopic(TopicRequest)
            Planner->>Bedrock: GenerateTopicPlan (via BAML CFFI)
            alt Upstream Error
                Bedrock-->>Planner: Inference failure
                Planner-->>Breaker: Upstream error
                Breaker-->>Service: ErrUpstream
                Service-->>Handler: ErrUpstream
                Handler-->>Caller: 502 Bad Gateway
            else Upstream Success
                Bedrock-->>Planner: Study plan items
                Planner-->>Breaker: Return items
                Breaker-->>Service: Record success & return items
                Note over Service: Assign to plans[i] (preserves request index)
            end
        end
    end

    Service->>Service: mergeAndDeduplicate(plans)
    Service->>Graph: validateAgainstGraph(mergedItems)
    Note over Service: Drop hallucinated topics, replace chains from graph, sort prerequisites before dependents, assign sequential priorities (1..N)

    Service-->>Handler: Return PlanResponse with PipelineMeta
    Handler-->>Caller: 200 OK (PlanResponse)
```

### Pipeline Execution Stages

1. **Request Binding and Validation**: Incoming JSON is decoded. The handler validates that `failed_topics` is non-empty and contains at most `MAX_FAILED_TOPICS` unique items (`internal/service/validation.go`). `vark_profile` is checked against allowed values (`visual`, `auditory`, `read_write`, `kinesthetic`). `student_id` and `course_id` are validated against `^[A-Za-z0-9._:-]{1,128}$` when provided (`internal/service/validation.go`).
2. **Topic Existence Verification**: All topics in `failed_topics` are verified against the in-memory graph (`internal/service/validation.go`). If any topic is absent from the graph, execution stops immediately and returns HTTP 422 `validation_error` with the list of unknown topics.
3. **Subgraph Extraction**: The service extracts a depth-2 bidirectional subgraph centered on the failed topics using breadth-first search (`GetLocalSubgraph` in `internal/graph/memory.go`). This context bounds the concept neighborhood provided to the LLM.
4. **Parallel Topic Fan-Out**: An `errgroup.Group` spawns a goroutine for each failed topic (`internal/service/reasoning.go`). Each goroutine:
   - Computes the topological prerequisite chain for its topic via post-order depth-first search (`GetPrerequisiteChain` in `internal/graph/memory.go`). Cyclic inputs are rejected at load time; the traversal visited set handles shared prerequisites.
   - Executes through a shared circuit breaker with an individual timeout bounded by `LLM_TIMEOUT` (`internal/service/reasoning.go`).
   - Invokes Amazon Bedrock Nova Micro through the BAML adapter (`internal/llm/baml.go`).
   - Stores the generated items directly into `plans[i]`, where `i` is the original request index. This ensures deterministic ordering regardless of goroutine completion timing.
5. **Merge and Deduplication**: `mergeAndDeduplicate` in `internal/service/pipeline.go` iterates sequentially through the per-topic results in request order, tracking visited topic names in a map and dropping duplicate concept recommendations.
6. **Prerequisite Ordering and Hallucination Validation**: `validateAgainstGraph` in `internal/service/pipeline.go` verifies every study item against the knowledge graph. Non-existent topics are dropped as hallucinations. When items are dropped, the service emits a WARN log record with `request_id` and `items_dropped`. Each validated item receives its trusted prerequisite chain from the graph (`GetPrerequisiteChain`) rather than accepting invented planner chains. Validated items are stable-sorted by graph prerequisite rank so that prerequisite concepts always precede dependent concepts, preserving deterministic traversal order among independent topics. Sequential priorities (1, 2, 3...) are assigned after sorting.
7. **Response Construction**: The final `domain.PlanResponse` is assembled with echoed identifiers, validated and sorted study items, and internal telemetry metrics (`subgraph_tuples`, `topics_processed`, `items_generated`, `items_after_validation`, `llm_latency_ms`, `total_latency_ms`).

---

## Resilience and Operational Limits

| Mechanism | Configuration Key | Default Value | Config Location | Implementation Location | Behavior |
|---|---|---|---|---|---|
| Transport Read Timeout | N/A | `10s` | Hardcoded in `internal/config/config.go` | `fiber.Config.ReadTimeout` in `internal/server/server.go` | Bounds duration for reading inbound request headers and body. |
| Transport Write Timeout | N/A | `30s` (derived) | Derived as `REQUEST_TIMEOUT + 10s` in `internal/config/config.go` | `fiber.Config.WriteTimeout` in `internal/server/server.go` | Bounds duration for transmitting HTTP responses. Derived write duration rejects overflow at startup. |
| Transport Idle Timeout | N/A | `1m` | Hardcoded in `internal/config/config.go` | `fiber.Config.IdleTimeout` in `internal/server/server.go` | Bounds maximum idle keep-alive connection time. |
| Request Timeout | `REQUEST_TIMEOUT` | `20s` | `internal/config/config.go` | `AdaptivePlanHandler` in `internal/api/handlers.go` | Bounds total HTTP request processing duration in the reasoning pipeline. Exceeding returns 504 `gateway_timeout`. |
| LLM Timeout | `LLM_TIMEOUT` | `15s` | `internal/config/config.go` | `ReasoningService` in `internal/service/reasoning.go` | Bounds individual model calls. Validated at startup not to exceed `REQUEST_TIMEOUT`. |
| Shutdown Timeout | `SHUTDOWN_TIMEOUT` | `10s` | `internal/config/config.go` | `Run` in `internal/server/lifecycle.go` | Maximum time allowed to drain connections during SIGINT or SIGTERM before exit code 1. |
| Healthcheck Timeout | N/A | `3s` | Hardcoded | `Healthcheck` in `internal/server/lifecycle.go` | Local CLI health check probe HTTP client deadline. |
| Circuit Breaker | N/A | Settings below | Hardcoded | `ReasoningService` in `internal/service/reasoning.go` | Wraps all model calls using `sony/gobreaker/v2`. Protects upstream Bedrock during failures. |
| Rate Limiting | `RATE_LIMIT_MAX`, `RATE_LIMIT_WINDOW` | `50` requests per `1m` | `internal/config/config.go` | `limiter.New` in `internal/server/server.go` | Sliding window IP rate limiter mounted at `/api/v1`. `/health` and `/swagger/*` are excluded. Exceeding returns 429 `rate_limit_exceeded`. |
| Request Body Limit | N/A | `64 KiB` (65,536 bytes) | Hardcoded | `fiber.Config.BodyLimit` in `internal/server/server.go` | Fiber application body limit. Exceeding returns 413 `payload_too_large`. |
| Max Failed Topics | `MAX_FAILED_TOPICS` | `10` | `internal/config/config.go` | `validateRequest` in `internal/service/validation.go` | Bounds parallel fan-out concurrency. Exceeding returns 400 `validation_error`. |
| Upstream Retries | `AWS_MAX_ATTEMPTS`, `AWS_RETRY_MODE` | `3`, `standard` | Validated at startup; evaluated by AWS SDK | AWS SDK in BAML CFFI runtime | Single retry layer managed by the AWS SDK (see [Upstream Retries](#upstream-retries)). |

### Circuit Breaker Settings

A single circuit breaker is instantiated with name `"bedrock-nova-micro"` in `internal/service/reasoning.go` and shared across topics and callers within each service instance because Bedrock availability and throttling affect the common backend:
- **Consecutive Failures Threshold (`ReadyToTrip`)**: Trips to the Open state after 3 consecutive failures (`c.ConsecutiveFailures >= 3`).
- **Open Duration (`Timeout`)**: Stays Open for `15s` before transitioning to Half-Open.
- **Half-Open Concurrency (`MaxRequests`)**: Permits 2 trial requests in Half-Open state.
- **Closed Interval (`Interval`)**: Clears internal counters every `30s` in Closed state.
- **Exclusion Predicate (`IsExcluded`)**: Configured with `IsExcluded: func(err error) bool { return errors.Is(err, context.Canceled) || errors.Is(err, ErrInvalidOutput) || errors.Is(err, ErrUpstreamRejected) }`. Direct and wrapped `context.Canceled`, `ErrInvalidOutput`, and `ErrUpstreamRejected` errors are excluded. Excluded errors count as neither success nor failure and cannot open or close the circuit; in Half-Open state, excluded errors release trial probe capacity without counting toward successful recovery. Invalid output describes unusable responses (such as parse failures or response truncation), and rejected requests describe client-side rejections (including HTTP 4xx errors, credentials, and request validation failures); neither indicates backend unavailability. Excluding them prevents request-specific failures from tripping the shared circuit while retaining protection against shared backend outages. Availability failures—including `ErrThrottled` (HTTP 429), `ErrUpstreamUnavailable` (HTTP 5xx, HTTP 408, transport drops), and `context.DeadlineExceeded`—continue to count as failures that trip the breaker.

### Upstream Retries

BAML configures no application-level retry policy; retries are owned exclusively by the embedded AWS SDK. When operating in standard mode with default settings (`AWS_MAX_ATTEMPTS=3`, `AWS_RETRY_MODE=standard`), sustained upstream throttling (HTTP 429) results in three total attempts before returning `service.ErrThrottled`. Client-side rejections (such as HTTP 403 `AccessDeniedException`) and invalid model outputs fail on the initial attempt without retry loops. For environment variable settings, see [Configuration](configuration.md#environment-variables).

### Error Handling

The service classifies upstream model failures from Boundary ML (BAML) into typed sentinel errors in `internal/llm/classify.go`. Each sentinel error is defined in `internal/service/errors.go` and wraps `service.ErrUpstream`:

- `service.ErrThrottled`: Upstream rate limiting or capacity exhaustion (HTTP status code 429 or `ThrottlingException`).
- `service.ErrUpstreamUnavailable`: Server-side failures (HTTP 5xx status codes), dispatch or transport drop errors (`DispatchFailure`, connection reset, connection refused), client code 2, or timeouts (`TimeoutError`, HTTP 408).
- `service.ErrUpstreamRejected`: Upstream client-side rejections other than throttling (HTTP 4xx status codes like 400, 401, 403, 404, 424, or SDK exceptions like `AccessDeniedException` or `ValidationException`). BAML formats upstream HTTP 400 errors as `InvalidAuthentication (401)`.
- `service.ErrInvalidOutput`: Unusable model output, including malformed 2xx payloads, parsing errors (`Parsing error:`), schema validation errors (`Validation error:`, `ValidationError`), or response truncation (`Finish reason error:` from `max_tokens` deny list).

Classified errors wrap `service.ErrUpstream` via `fmt.Errorf("%w: %w", class, err)`. This preserves the underlying error for inspection with `errors.Is`. Unrecognized errors and `AbortError` strings pass through without extra classification.

#### HTTP Status Mapping

Because `ErrThrottled`, `ErrUpstreamUnavailable`, `ErrUpstreamRejected`, and `ErrInvalidOutput` wrap `service.ErrUpstream`, the HTTP presentation layer (`internal/api/handlers.go`) maps all classified upstream errors to HTTP 502 `bad_gateway`. When `ctx.Err()` is present upon return from the adapter, it returns `ctx.Err()` directly, producing HTTP 504 `gateway_timeout` when execution deadlines expire. For complete error codes, status mappings, and client-facing response schemas, see the [API reference](api.md#error-codes-and-http-statuses).

---

## Design Decisions

- **In-memory graph storage**: The curriculum knowledge graph contains 20 tuples in seed data and fits entirely in memory. Maintaining forward and reverse adjacency maps in Go memory provides sub-millisecond BFS and DFS traversal without external database infrastructure or network overhead.
- **Fiber v3 web framework**: Fiber provides high-throughput routing with low memory allocations, built-in security middlewares (helmet, limiter, cors, recover, requestid), and standard context propagation.
- **Decoupled Planner interface**: `service.ReasoningService` depends on the `service.Planner` interface rather than directly on generated BAML functions. This allows unit and race tests to run deterministically with mock planners without requiring AWS credentials or network access.
- **Typed upstream error classification**: BAML Go returns runtime failures as unclassified string messages (`BamlError`). `internal/llm/classify.go` inspects upstream error messages and wraps them in typed sentinels (`ErrThrottled`, `ErrUpstreamUnavailable`, `ErrUpstreamRejected`, `ErrInvalidOutput`) that each wrap `service.ErrUpstream`. Downstream logic and tests can distinguish error categories using `errors.Is` while preserving existing HTTP status mapping.
- **Indexed slot assignment during fan-out**: Parallel goroutines write directly to pre-allocated slice indices (`plans[i] = items`). This prevents race conditions and ensures that curriculum item priorities depend solely on request topic order rather than goroutine completion speed.
- **Circuit breaker exclusion handling**: A single circuit breaker protects the shared Bedrock backend across all topics and callers. Sibling errgroup cancellations produce `context.Canceled`, unusable model responses produce `ErrInvalidOutput`, and client rejections produce `ErrUpstreamRejected`. Using `IsExcluded` in `gobreaker` ensures cancellations, invalid output, and rejected requests (direct or wrapped) are treated neutrally (neither success nor failure), and trial probes in half-open state are released without recording recovery. This prevents request-specific validation or parsing failures and client 4xx rejections from tripping the shared circuit and blocking unrelated topics, while preserving failure counting for genuine backend availability degradation (`ErrThrottled`, `ErrUpstreamUnavailable`, and `context.DeadlineExceeded`).
- **Server-enforced prerequisite ordering**: Included study prerequisites are ordered ahead of dependents based on knowledge graph topological order, regardless of request or planner order; independent topics retain deterministic traversal ordering. In addition, `prerequisite_chain` is populated using `graph.GetPrerequisiteChain` rather than trusted from the planner.
- **Bounded failure logging**: Request failure logging bounds error summaries to `error_class`, `request_id`, and when known, `topic` capped at 128 characters, omitting raw upstream prompts and provider error payloads. `stage.validation` logs WARN records with `request_id` and `items_dropped` only when dropped count is non-zero.
- **Post-generation hallucination filtering**: LLM responses may invent topic names not present in the curriculum. The `validateAgainstGraph` stage filters out non-existent topics before assigning priorities, maintaining graph integrity.
- **Private Swagger specification copy**: `swag.Spec.ReadDoc()` mutates its receiver during template evaluation. AxiomEngine clones the private specification structure at startup and serves the evaluated JSON statically, eliminating data races during concurrent documentation requests.
