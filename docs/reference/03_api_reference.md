# 03 – API Reference

## General Conventions

| Property | Value |
|----------|-------|
| Base URL (local) | `http://localhost:8080` |
| Base URL (Railway) | Configured via `PORT` environment variable |
| Content-Type | `application/json` for all request and response bodies |
| Authentication | None — this is an internal service; core-lms-backend is the trust boundary |
| Swagger UI | `GET /swagger/index.html` (available when the service is running) |
| Error envelope | All non-2xx responses use the `ErrorResponse` format (see [Error Response Format](#error-response-format)) |

---

## GET /health

**Summary**: Returns 200 if the service is running.

**Permission**: Internal only

**Response body**

| Field | Type | Description |
|-------|------|-------------|
| `status` | string | Always `"ok"` |

**Status codes**

| Code | Meaning |
|------|---------|
| 200 | Service is running |

**Example**

```bash
curl -s http://localhost:8080/health
```

```json
{"status": "ok"}
```

---

## POST /api/v1/adaptive-plan

**Summary**: Generate a personalized adaptive study plan.

**Description**: Accepts a student VARK profile and a list of failed concept IDs. Executes GraphRAG subgraph extraction (BFS depth 2), topological prerequisite sort (DFS), and parallel BAML fan-out to Amazon Nova Micro. Returns study items ordered by prerequisite depth, deduplicated, and validated against the knowledge graph.

**Permission**: Internal only

**Request body** (`Content-Type: application/json`)

| Field | Type | Required | Description | Example |
|-------|------|----------|-------------|---------|
| `student_id` | string | No | Platform-level learner identifier. Propagated into LLM prompt and echoed in response. | `"student-42"` |
| `course_id` | string | No | Course scope identifier. Propagated into LLM prompt and echoed in response. | `"cs101"` |
| `failed_topics` | []string | **Yes** | Non-empty list of knowledge-graph node names the student has not mastered. | `["Polymorphism","Recursion"]` |
| `vark_profile` | string | **Yes** | Student dominant learning modality. Accepted: `"visual"`, `"auditory"`, `"read_write"`, `"kinesthetic"`. | `"visual"` |
| `telemetry` | object | No | Optional client-side diagnostics. See sub-fields below. | — |
| `telemetry.session_id` | string | No | Client-generated correlation ID. | `"sess-a1b2c3"` |
| `telemetry.timestamp_unix` | int64 | No | Unix epoch seconds when the client created the request. | `1713340800` |
| `telemetry.duration_ms` | float64 | No | Assessment duration on the client side (ms). | `12345.6` |
| `telemetry.client_version` | string | No | Frontend application version. | `"2.4.1"` |

**Response body** — `200 OK`

| Field | Type | Description |
|-------|------|-------------|
| `student_id` | string | Echoed from request |
| `course_id` | string | Echoed from request |
| `items` | []PlanItem | Ordered study units (priority 1 = study first) |
| `items[].topic` | string | Knowledge-graph node name |
| `items[].priority` | int | 1-based study order |
| `items[].prerequisite_chain` | []string | Ordered prerequisite path (deepest first) |
| `items[].explanation` | string | LLM-generated rationale |
| `items[].resources` | []Resource | 2–3 VARK-aligned learning assets |
| `items[].resources[].title` | string | Human-readable resource name |
| `items[].resources[].url` | string | Fully-qualified URL |
| `items[].resources[].resource_type` | string | Modality: `video`, `diagram`, `infographic`, `exercise`, `lab`, `coding-challenge`, `podcast`, `lecture`, `audio-course`, `article`, `textbook`, `documentation` |
| `_meta.subgraph_tuples` | int | BFS-extracted triple count |
| `_meta.topics_processed` | int | Failed topic count sent to LLM |
| `_meta.items_generated` | int | Total items from all LLM calls before deduplication |
| `_meta.items_after_validation` | int | Items remaining after hallucination-guard |
| `_meta.llm_latency_ms` | float64 | Wall-clock time waiting for all parallel LLM calls (ms) |
| `_meta.total_latency_ms` | float64 | End-to-end pipeline duration (ms) |

**Status codes**

| Code | Error value | Cause |
|------|------------|-------|
| 200 | — | Valid plan generated |
| 400 | `"invalid_request_body"` | JSON parse failure |
| 400 | `"validation_error"` | `failed_topics` empty or `vark_profile` missing |
| 429 | `"rate_limit_exceeded"` | >50 requests/minute from same IP |
| 500 | `"reasoning_failed"` | Unrecoverable pipeline error |
| 503 | `"service_unavailable"` | Circuit breaker open (Bedrock backend unavailable) |
| 504 | `"gateway_timeout"` | LLM reasoning exceeded 20 s deadline |

**Example request**

```bash
curl -s -X POST http://localhost:8080/api/v1/adaptive-plan \
  -H "Content-Type: application/json" \
  -d '{
    "student_id": "student-42",
    "course_id": "cs101",
    "failed_topics": ["Polymorphism", "Recursion"],
    "vark_profile": "visual"
  }'
```

**Example response**

```json
{
  "student_id": "student-42",
  "course_id": "cs101",
  "items": [
    {
      "topic": "Variables",
      "priority": 1,
      "prerequisite_chain": [],
      "explanation": "Variables are the foundation of all data manipulation in programming.",
      "resources": [
        {
          "title": "Variables and Data Types – Visual Guide",
          "url": "https://example.com/variables-visual",
          "resource_type": "diagram"
        }
      ]
    }
  ],
  "_meta": {
    "subgraph_tuples": 12,
    "topics_processed": 2,
    "items_generated": 6,
    "items_after_validation": 5,
    "llm_latency_ms": 3240.5,
    "total_latency_ms": 3312.8
  }
}
```

---

## GET /api/v1/tutor/student/{student_id}/cognitive-graph

**Summary**: Get a filtered cognitive shadow subgraph for a student.

**Description**: Returns a depth-1 neighborhood of curriculum concept nodes centered on a target topic (or all failed topics), annotated with the student's cognitive state (`failed`, `learning`, `mastered`). Only nodes that participate in at least one edge are included (no orphan nodes). The response is directly consumable by Cytoscape.js or D3.js without frontend transformation.

**Permission**: Internal only

**Path parameters**

| Parameter | Type | Required | Description | Example |
|-----------|------|----------|-------------|---------|
| `student_id` | string | **Yes** | Student identifier | `student-42` |

**Query parameters**

| Parameter | Type | Required | Description | Example |
|-----------|------|----------|-------------|---------|
| `topics` | string | **Yes** | Comma-separated list of failed topic names. Used for cognitive state classification. | `Polymorphism,Recursion` |
| `target_topic` | string | No | Single topic to center the subgraph on. When omitted, all `topics` are used as BFS seeds. | `Recursion` |

**Response body** — `200 OK`

| Field | Type | Description |
|-------|------|-------------|
| `nodes` | []VisualNode | Filtered nodes in the depth-1 subgraph (only nodes with edges) |
| `nodes[].id` | string | Concept name (knowledge-graph node) |
| `nodes[].label` | string | Display label (same as `id`) |
| `nodes[].cognitive_state` | string | `"failed"`, `"learning"`, or `"mastered"` |
| `edges` | []VisualEdge | Directed edges in the filtered subgraph |
| `edges[].source` | string | Dependent concept (VisualNode id) |
| `edges[].target` | string | Prerequisite concept (VisualNode id) |
| `edges[].relation` | string | Edge type: `"depends_on"` or `"is_a"` |

**Status codes**

| Code | Error value | Cause |
|------|------------|-------|
| 200 | — | Graph generated |
| 400 | `"validation_error"` | `student_id` path param or `topics` query param missing |

**Example request**

```bash
curl -s "http://localhost:8080/api/v1/tutor/student/student-42/cognitive-graph?topics=Recursion&target_topic=Recursion"
```

---

## Error Response Format

All non-2xx responses return a JSON object conforming to `ErrorResponse`:

```json
{
  "error": "validation_error",
  "details": "failed_topics must contain at least one topic"
}
```

| Field | Type | Always present | Description |
|-------|------|---------------|-------------|
| `error` | string | Yes | Machine-readable error code |
| `details` | string | No | Human-readable explanation; omitted when empty |

**Known error codes**

| Code | HTTP status |
|------|-------------|
| `invalid_request_body` | 400 |
| `validation_error` | 400 |
| `rate_limit_exceeded` | 429 |
| `reasoning_failed` | 500 |
| `internal_error` | 500 |
| `service_unavailable` | 503 |
| `gateway_timeout` | 504 |
