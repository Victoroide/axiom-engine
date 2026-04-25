# API Specification

**System:** axiom-reasoning-svc (AxiomEngine)
**Base URL:** `http://localhost:8080`

---

## Global Headers

All responses include the security headers injected by the Helmet middleware (see [architecture.md](architecture.md), Section 5.4). Rate limit headers are appended to every response:

| Header | Description |
|--------|-------------|
| `X-Ratelimit-Limit` | Maximum requests per 60-second window (50). |
| `X-Ratelimit-Remaining` | Remaining requests in the current window. |
| `X-Ratelimit-Reset` | Seconds until the rate limit window resets. |
| `Content-Type` | `application/json` for all JSON responses. |

---

## 1. Health Check

### `GET /health`

Liveness probe for container orchestrators and load balancers.

**Request:** No body or query parameters required.

**Response (200 OK):**

```json
{
  "status": "ok"
}
```

---

## 2. Adaptive Plan Generation

### `POST /api/v1/adaptive-plan`

Executes the six-stage reasoning pipeline to produce a personalized study plan.

### Request Headers

| Header | Value | Required |
|--------|-------|----------|
| `Content-Type` | `application/json` | Yes |

### Request Body Schema

```json
{
  "student_id": "string",
  "course_id": "string",
  "failed_topics": ["string", "string"],
  "vark_profile": "string",
  "telemetry": {
    "session_id": "string",
    "timestamp_unix": 0,
    "duration_ms": 0.0,
    "client_version": "string"
  }
}
```

| Field | Type | Required | Constraints |
|-------|------|----------|-------------|
| `student_id` | string | No | Propagated to LLM prompt and echoed in response. |
| `course_id` | string | No | Propagated to LLM prompt and echoed in response. |
| `failed_topics` | string[] | **Yes** | Non-empty array. Each element must be a valid knowledge-graph node name. |
| `vark_profile` | string | **Yes** | One of: `"visual"`, `"auditory"`, `"read_write"`, `"kinesthetic"`. |
| `telemetry` | object | No | Optional client-side diagnostics. Entire object may be omitted. |
| `telemetry.session_id` | string | No | Client-generated correlation identifier. |
| `telemetry.timestamp_unix` | integer | No | Unix epoch seconds. |
| `telemetry.duration_ms` | float | No | Client assessment duration in milliseconds. |
| `telemetry.client_version` | string | No | Frontend application version. |

### Response Body Schema (200 OK)

```json
{
  "student_id": "student-001",
  "course_id": "CS-201",
  "items": [
    {
      "topic": "Variables",
      "priority": 1,
      "prerequisite_chain": [],
      "explanation": "Understanding variables is fundamental for all programming concepts.",
      "resources": [
        {
          "title": "Variables Exercise",
          "url": "https://example.com/variables-exercise",
          "resource_type": "exercise"
        }
      ]
    },
    {
      "topic": "Polymorphism",
      "priority": 5,
      "prerequisite_chain": ["Variables", "Objects", "Classes", "Inheritance"],
      "explanation": "Polymorphism allows flexible and reusable code.",
      "resources": [
        {
          "title": "Polymorphism Lab",
          "url": "https://example.com/polymorphism-lab",
          "resource_type": "lab"
        }
      ]
    }
  ],
  "_meta": {
    "subgraph_tuples": 11,
    "topics_processed": 2,
    "items_generated": 26,
    "items_after_validation": 13,
    "llm_latency_ms": 4939.0,
    "total_latency_ms": 4942.0
  }
}
```

| Field | Type | Description |
|-------|------|-------------|
| `student_id` | string | Echoed from request. |
| `course_id` | string | Echoed from request. |
| `items` | PlanItem[] | Priority-sorted study items (1 = study first). |
| `items[].topic` | string | Knowledge-graph node name. Guaranteed to exist in the graph. |
| `items[].priority` | integer | Sequential priority. 1 = highest. |
| `items[].prerequisite_chain` | string[] | Topologically ordered prerequisites extracted from the graph. |
| `items[].explanation` | string | LLM-generated rationale for studying this topic. |
| `items[].resources` | Resource[] | 1-3 learning resources matching the VARK profile. |
| `items[].resources[].title` | string | Human-readable resource name. |
| `items[].resources[].url` | string | Fully-qualified URL. |
| `items[].resources[].resource_type` | string | Modality type constrained by VARK profile. |
| `_meta.subgraph_tuples` | integer | Number of graph triples extracted by BFS. |
| `_meta.topics_processed` | integer | Number of failed topics processed (parallel fan-out count). |
| `_meta.items_generated` | integer | Total study items returned by all LLM calls before dedup. |
| `_meta.items_after_validation` | integer | Items surviving the hallucination-guard filter. |
| `_meta.llm_latency_ms` | float | Wall-clock milliseconds for all parallel LLM calls. |
| `_meta.total_latency_ms` | float | End-to-end pipeline duration in milliseconds. |

---

## 3. Cognitive Shadow Graph

### `GET /api/v1/tutor/student/:student_id/cognitive-graph`

Generates a **filtered** subgraph of the curriculum knowledge graph, centered on a target topic's depth-1 neighborhood. Each node is annotated with the student's cognitive state. The response is directly consumable by Cytoscape.js and D3.js — no frontend transformation required. Only nodes that participate in at least one edge are included (no orphan/isolated nodes).

### Path Parameters

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `student_id` | string | **Yes** | Platform-level student identifier. |

### Query Parameters

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `topics` | string | **Yes** | Comma-separated list of failed topic names (e.g., `Polymorphism,Recursion`). Used for cognitive state classification. |
| `target_topic` | string | No | Single topic name to center the subgraph on. When provided, only the depth-1 neighborhood of this topic is returned. When omitted, the depth-1 neighborhoods of **all** failed topics are merged. |

### Response Body Schema (200 OK)

```json
{
  "nodes": [
    {
      "id": "Recursion",
      "label": "Recursion",
      "cognitive_state": "failed"
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
      "id": "Dynamic Programming",
      "label": "Dynamic Programming",
      "cognitive_state": "mastered"
    }
  ],
  "edges": [
    {
      "source": "Recursion",
      "target": "Functions",
      "relation": "depends_on"
    },
    {
      "source": "Merge Sort",
      "target": "Recursion",
      "relation": "depends_on"
    },
    {
      "source": "Dynamic Programming",
      "target": "Recursion",
      "relation": "depends_on"
    }
  ]
}
```

| Field | Type | Description |
|-------|------|-------------|
| `nodes` | VisualNode[] | Filtered nodes in the depth-1 subgraph. Only includes nodes with at least one edge. |
| `nodes[].id` | string | Unique node identifier (equals the graph node name). |
| `nodes[].label` | string | Display label for the frontend (same as `id`). |
| `nodes[].cognitive_state` | string | One of: `"failed"`, `"learning"`, `"mastered"`. |
| `edges` | VisualEdge[] | Directed edges in the filtered subgraph. Both source and target are guaranteed to be in the node list. |
| `edges[].source` | string | ID of the dependent concept. |
| `edges[].target` | string | ID of the prerequisite concept. |
| `edges[].relation` | string | Edge label (`"depends_on"` or `"is_a"`). |

### Cognitive State Classification Logic

| State | Condition |
|-------|-----------|
| `"failed"` | The node name appears in the `topics` query parameter. |
| `"learning"` | The node is a transitive prerequisite of any failed node (determined by DFS over the full graph). |
| `"mastered"` | The node is neither failed nor a prerequisite of a failed node. |

### Subgraph Filtering Logic

The endpoint uses BFS with depth 1 from the seed topic(s), traversing both forward (`depends_on` outgoing) and reverse (`depends_on` incoming) edges. This returns:
- The target topic itself
- Its direct prerequisites (one level up)
- Topics that directly depend on it (one level down)

Nodes that exist in the full knowledge graph but have no direct edge to the seed topic(s) are excluded.

---

## 4. Error Codes

All error responses conform to the following JSON schema:

```json
{
  "error": "machine_readable_code",
  "details": "Human-readable explanation."
}
```

### Error Code Reference

| HTTP Status | `error` Value | Trigger Condition | Endpoint(s) |
|-------------|--------------|-------------------|-------------|
| 400 | `invalid_request_body` | Malformed JSON or deserialization failure. | POST adaptive-plan |
| 400 | `validation_error` | Missing `failed_topics`, empty `vark_profile`, or missing `student_id`/`topics`. | POST adaptive-plan, GET cognitive-graph |
| 429 | `rate_limit_exceeded` | Client IP exceeded 50 requests per 60-second window. | All endpoints |
| 500 | `reasoning_failed` | Unclassified pipeline error (e.g., BAML schema validation failure). | POST adaptive-plan |
| 500 | `internal_error` | Unhandled Fiber-level error (panic recovery or unmatched route). | All endpoints |
| 503 | `service_unavailable` | Circuit breaker is in Open state after 3 consecutive LLM failures. | POST adaptive-plan |
| 504 | `gateway_timeout` | Request exceeded the 20-second context deadline. | POST adaptive-plan |
