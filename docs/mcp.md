# Model Context Protocol (MCP)

AxiomEngine provides a Model Context Protocol (MCP) server running over standard input and output (`stdio`). This allows MCP-compliant clients and developer tools to inspect curriculum structures and generate adaptive study plans directly without intermediate HTTP requests.

---

## Server Execution

The MCP server is invoked via the `mcp` subcommand:

```sh
axiom-server mcp
```

### Architecture and Transport

- **Transport**: Standard I/O (`mcp.StdioTransport`) using the official Go SDK (`github.com/modelcontextprotocol/go-sdk`).
- **Standard Output (`stdout`)**: Dedicated exclusively to JSON-RPC protocol frames.
- **Standard Error (`stderr`)**: Dedicated to structured JSON logging via `log/slog`.
- **Implementation Identification**: Server name `axiom-engine`. Version is determined from runtime build information (`debug.ReadBuildInfo()`), defaulting to `dev` for local unversioned builds.
- **Trust Boundary**: `AXIOM_INTERNAL_TOKEN` is not evaluated in MCP mode. The process launching `axiom-server` serves as the trust boundary.
- **Backend Services**: Tool calls execute against the same bounded `service.ReasoningService` pipeline and `graph.InMemoryGraph` used by the HTTP API, sharing concurrency limits, circuit breaking, timeouts, and error classification.

### Shutdown Lifecycle

The server implements a bounded shutdown lifecycle over standard I/O:
- On receipt of a first `SIGINT` or `SIGTERM`, default signals are restored, new tool calls are rejected (`service_unavailable`, `"server shutting down"`), and in-flight calls drain within `SHUTDOWN_TIMEOUT` (logging `server.shutdown_started`).
- A drain that completes within the budget closes the transport, logs `server.shutdown_completed` and exits 0.
- When the budget expires, remaining tool work is canceled, pending output is abandoned, and the process logs `server.failed` with `"shutdown: context deadline exceeded"` and exits 1, as the HTTP server does. A client that stopped reading may see an incomplete final frame.
- Stdin `EOF` exits 0.
- A second signal terminates immediately.

---

## Client Configuration

To configure AxiomEngine in an MCP client (for example a desktop assistant or an editor extension), configure an entry in the client's MCP configuration file:

```json
{
  "mcpServers": {
    "axiom-engine": {
      "command": "/absolute/path/axiom-server",
      "args": ["mcp"],
      "env": {
        "AWS_REGION": "us-east-1",
        "BAML_LIBRARY_PATH": "/absolute/path/libbaml_cffi.dylib",
        "BAML_LIBRARY_DISABLE_DOWNLOAD": "true",
        "BAML_LOG": "off"
      }
    }
  }
}
```

For the default `aws-bedrock` provider, credentials resolve through the standard AWS credential chain (environment variables, profile configuration, or IAM roles). Other providers use their configured API keys and endpoints.

---

## Planner Provider

MCP uses the same startup `LLM_PROVIDER`, `LLM_MODEL`, `LLM_BASE_URL` and provider API key settings as HTTP. A single configured client registry serves all plan tool calls. Provider validation runs before the transport starts; AWS retry validation and region inspection apply only to `aws-bedrock`. Startup logs on stderr include `llm_provider` and `llm_model`.

## Tool Tracing

`TRACING_ENABLED=true` enables the same OTLP exporter and standard `OTEL_*` settings as HTTP. Each admitted `tools/call` has a server span named `tools/call <registered tool name>`; unknown tool names use `unmatched`. Plan calls contain validation, graph, topic, model-call and output spans. Tool logs (`mcp.tool`) include request, trace and span IDs. Protocol control messages do not create tool spans. Stdout remains dedicated to JSON-RPC.

The tracing provider is flushed on stdin EOF and signal shutdown. On a signal, tool draining, transport closure and trace flushing share the remaining `SHUTDOWN_TIMEOUT`; HTTP-only `SHUTDOWN_DRAIN_DELAY` is ignored.

### LangSmith

`LANGSMITH_TRACING=true` enables LangSmith independently of the generic exporter. Tool roots are annotated as `chain` and model calls as `llm`, with request-ID metadata. Only traces containing a model call are exported to LangSmith; graph-only tools and protocol control messages do not consume its export queue.

---

## Available Tools

The MCP server exposes four tools:

### `generate_study_plan`

Generates a personalized, prerequisite-ordered study plan for a student based on failed assessment topics and learning modality.

- **Parameters**:
  - `mastered_topics` (array of strings, optional): Known concepts, canonically resolved and deduplicated within `MAX_MASTERED_TOPICS`; must not overlap failed topics. Known prerequisites are excluded from generated study items while prerequisite chains remain complete.
- `failed_topics` (array of strings, required): Concepts requiring remediation. Matched canonically against the knowledge graph and bounded by `MAX_FAILED_TOPICS`.
  - `vark_profile` (string, required): Learning modality (`visual`, `auditory`, `read_write`, or `kinesthetic`).
  - `student_id` (string, optional): Learner identifier.
  - `course_id` (string, optional): Course identifier.
- **Output**: Structured JSON object containing `student_id`, `course_id`, `items` (ordered study items with explanation and learning resources), and `_meta` execution metadata.

### `get_cognitive_graph`

Computes a depth-1 concept neighborhood around failed topics, classifying concepts into `failed`, `learning`, `mastered`, or `unassessed` states.

- **Parameters**:
  - `mastered` (array of strings, optional): Known concepts, with the same canonical matching, limit and overlap validation as the HTTP route. Omit it for legacy graph states; an explicit empty list marks unevidenced nodes as `unassessed`.
- `topics` (array of strings, required): List of concept names to assess.
  - `target_topic` (string, optional): Focus concept within `topics` to center the depth-1 subgraph around.
- **Output**: Structured JSON object containing `nodes` (each with `id`, `label`, and `cognitive_state`) and `edges` (`source`, `target`, and `relation`).

### `list_topics`

Lists curriculum concepts sorted by stored name, along with direct prerequisites and direct dependents.

- **Parameters**:
  - `q` (string, optional): Substring to filter concept names using canonical matching.
- **Output**: Structured JSON object containing `topics` array of objects with `name`, `prerequisites`, and `dependents`.

### `get_topic_path`

Resolves a concept canonically and returns its full prerequisite chain in study order alongside direct dependents.

- **Parameters**:
  - `topic` (string, required): Concept name to inspect.
- **Output**: Structured JSON object containing `topic`, `prerequisite_chain` (transitive prerequisites in topological study order excluding the concept itself), and `dependents` (direct dependent concepts).

---

## Tool Error Handling

Tool execution errors are returned as MCP tool errors (`isError: true` on `CallToolResult`) with structured JSON content and text representations matching the standard `domain.ErrorResponse` schema:

```json
{
  "error": "validation_error",
  "details": "unknown topic: \"Unknown\""
}
```

Error classifications mirror the HTTP API status codes:
- `validation_error`: Invalid input arguments, unsupported VARK profiles, unknown curriculum concepts, or topic limits exceeded.
- `service_unavailable`: Concurrency limit reached, circuit breaker open, upstream throttling, or new tool calls rejected during shutdown.
- `gateway_timeout`: Model reasoning exceeded deadline (`REQUEST_TIMEOUT` or `LLM_TIMEOUT`).
- `bad_gateway`: Upstream model reasoning failure or unparseable response.
- `request_timeout`: Request was canceled.
- `internal_error`: Unexpected server error or recovered panic.

---

## Running in Docker

The MCP server can be run inside the container image using standard input/output redirection without allocating a pseudoterminal:

```sh
docker run --rm -i \
  -e AWS_REGION=us-east-1 \
  -e AWS_ACCESS_KEY_ID \
  -e AWS_SECRET_ACCESS_KEY \
  axiom-engine:local mcp
```

The container image preconfigures `BAML_LIBRARY_PATH=/app/libbaml_cffi.so`, `BAML_LIBRARY_DISABLE_DOWNLOAD=true`, and `BAML_LOG=off`. Do not pass `-t` (`--tty`), as allocating a TTY corrupts JSON-RPC stream framing.

---

## Testing with MCP Inspector

Interactive inspection and manual tool execution can be performed using the official MCP Inspector:

```sh
npx @modelcontextprotocol/inspector /absolute/path/axiom-server mcp
```

Ensure environment variables (`AWS_REGION`, `BAML_LIBRARY_PATH`, `BAML_LIBRARY_DISABLE_DOWNLOAD`, `BAML_LOG`) are set in the launching shell before running the command.
