# Model Context Protocol (MCP)

AxiomEngine provides a Model Context Protocol (MCP) server running over standard input and output (`stdio`). This allows MCP-compliant clients and developer tools to inspect curriculum structures and generate adaptive study plans directly without intermediate HTTP requests.

---

## Server Execution

For an extracted archive, configure the bundled library and disable runtime library downloads as shown in the archive setup. In the extracted directory, run the `mcp` subcommand through the launcher. [Run an extracted archive](../README.md#run-an-extracted-archive).

```sh
./axiom-server-launch mcp
```

For a source build, copy `scripts/axiom-server-launch` beside the built `axiom-server` and configure the native library before running the launcher.

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

### In-process Streamable HTTP

`internal/mcpserver.NewStreamableHTTPHandler` constructs an `http.Handler` for in-process use with the four curriculum tools. The handler accepts POST with `Mcp-Protocol-Version: 2026-07-28` and the corresponding JSON-RPC request metadata. It can return JSON or request-scoped SSE. The handler allows an absent `Origin` and checks that a supplied origin matches the request scheme and host. [MCP Streamable HTTP specification](https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/streamable-http)

Reuse one handler across requests to share the `LLM_MAX_CONCURRENCY` limit and `LLM_QUEUE_TIMEOUT`. Requests have a 4 MiB body limit. Closing a response cancels reasoning and graph traversal for that request. The fixed HTTP registry advertises tool discovery and invocation. The hosting `http.Server` owns graceful shutdown and drains active requests.

Response writes use the configured HTTP write timeout (`REQUEST_TIMEOUT + 10s`). Middleware that wraps the response writer must forward `SetWriteDeadline` or provide `Unwrap` so the handler can reach the underlying writer. The handler returns HTTP 500 before dispatch if it cannot set the deadline. [Go `http.ResponseController`](https://pkg.go.dev/net/http#ResponseController)

---

## Client Configuration

For an extracted release archive, configure the client to run `axiom-server-launch mcp` with the bundled library for your platform:

```json
{
  "mcpServers": {
    "axiom-engine": {
      "command": "/absolute/path/axiom-server-launch",
      "args": ["mcp"],
      "env": {
        "AWS_REGION": "us-east-1",
        "BAML_LIBRARY_PATH": "/absolute/path/libbaml_cffi-aarch64-apple-darwin.dylib",
        "BAML_LIBRARY_DISABLE_DOWNLOAD": "true"
      }
    }
  }
}
```

Use the launcher as the client command so both native loggers and the native trace file and upload settings are disabled before startup. Native diagnostics can appear on stdout and disrupt JSON-RPC framing. [Release archive library filenames](deployment.md#run-an-archive).

For the default `aws-bedrock` provider, credentials resolve through the standard AWS credential chain (environment variables, profile configuration, or IAM roles). Other providers use their configured API keys and endpoints.

---

## Planner Provider

MCP uses the same startup `LLM_PROVIDER`, `LLM_MODEL`, `LLM_BASE_URL` and provider API key settings as HTTP. A single configured client registry serves all plan tool calls. Provider validation runs before the transport starts; AWS retry validation and region inspection apply only to `aws-bedrock`. Startup logs on stderr include `llm_provider` and `llm_model`.

## Tool Tracing

`TRACING_ENABLED=true` enables the same OTLP exporter and standard `OTEL_*` settings as HTTP. Each admitted `tools/call` has a server span named `tools/call <registered tool name>`; unknown tool names use `unmatched`. Plan calls contain validation, graph, topic, model-call and output spans. Tool logs (`mcp.tool`) include request, trace and span IDs. Protocol control messages do not create tool spans. Stdout remains dedicated to JSON-RPC.

The tracing provider is flushed on stdin EOF and signal shutdown. On a signal, tool draining, transport closure and trace flushing share the remaining `SHUTDOWN_TIMEOUT`; HTTP-only `SHUTDOWN_DRAIN_DELAY` is ignored.

### LangSmith

`LANGSMITH_TRACING=true` enables LangSmith independently of the generic exporter. Tool roots are annotated as `chain` and model calls as `llm`, with request-ID metadata. Only traces containing a model call are exported to LangSmith; graph-only tools and protocol control messages do not consume its export queue.

### Captured Content

`TRACING_CAPTURE_CONTENT` uses the same default and opt-out as HTTP: true when LangSmith is enabled, false otherwise. Plan tool input captures only `failed_topics` and `vark_profile`, and output captures only validated items. Rendered prompts and raw completions come from the per-call collector. Learner identifiers are excluded, and every captured value is limited to 32 KiB on a UTF-8 boundary. Set `TRACING_CAPTURE_CONTENT=false` to retain structure, latency, usage and error classes without content.

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

The tool checks the [curriculum prompt budget](configuration.md#curriculum-prompt-budget) for all topics before dispatch.

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
- `curriculum_context_too_large` indicates the full curriculum input exceeds the prompt budget. The tool returns `isError: true` with `{"error":"curriculum_context_too_large","details":"curriculum context exceeds the prompt budget"}` in structured and text content.
- `service_unavailable`: Concurrency limit reached, circuit breaker open, upstream throttling, or new tool calls rejected during shutdown.
- `gateway_timeout`: Model reasoning exceeded deadline (`REQUEST_TIMEOUT` or `LLM_TIMEOUT`).
- `bad_gateway`: Upstream model reasoning failure or unparseable response.
- `request_timeout`: Request was canceled.
- `internal_error`: Unexpected server error or a recovered planner panic that determines the plan failure.

A recovered planner panic emits a `planner.panic` record on stderr with `error_class="internal_error"` and the tool call's request ID, including when tracing is disabled. The record omits the recovered value. If cancellation, a deadline, or an earlier error from another topic determines the plan failure, the tool error carries that outcome and a late panic still produces its recovery record.

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
npx @modelcontextprotocol/inspector /absolute/path/axiom-server-launch mcp
```

Set `BAML_LIBRARY_PATH`, `BAML_LIBRARY_DISABLE_DOWNLOAD=true`, and your provider settings in the launching shell. [Server Execution](#server-execution).
