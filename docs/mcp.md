# Model Context Protocol (MCP)

AxiomEngine provides a Model Context Protocol (MCP) server running over standard input and output (`stdio`). This allows MCP-compliant clients and developer tools to inspect curriculum structures and generate adaptive study plans directly without intermediate HTTP requests. An opt-in command serves the same tools to authenticated remote callers over HTTPS (see [Remote endpoint](#remote-endpoint)).

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
- **Trust boundary**. The process launching `axiom-server mcp` serves as the trust boundary. Stdio ignores HTTP authentication settings and the [remote endpoint settings](#remote-endpoint).
- **Backend Services**: Tool calls use the bounded reasoning pipeline and curriculum graph implementation shared with the HTTP API. The MCP process shares its own concurrency limits and circuit breaker across tool calls. With `GRAPH_RELOAD_ENABLED=true`, a `SIGHUP` to that process activates a validated replacement for subsequent tool calls. Reload records go to stderr. [Curriculum rollout](operations.md#curriculum-rollout)

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

Hosts that manage a live curriculum can use `NewStreamableHTTPHandlerWithCurriculum` to share its snapshots across the same tool registry.

Response writes use the configured HTTP write timeout (`REQUEST_TIMEOUT + 10s`). Middleware that wraps the response writer must forward `SetWriteDeadline` or provide `Unwrap` so the handler can reach the underlying writer. The handler returns HTTP 500 before dispatch if it cannot set the deadline. [Go `http.ResponseController`](https://pkg.go.dev/net/http#ResponseController)

### Remote endpoint

`axiom-server mcp-http` serves the four tools to remote callers over the stateless Streamable HTTP binding of protocol revision 2026-07-28. The ordinary HTTP server and `axiom-server mcp` never open this listener, and stopping the command withdraws remote access. [MCP Streamable HTTP specification](https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/streamable-http) · [MCP authorization](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization)

The command checks its settings before it binds. It starts only with verified authentication, an enabled Redis tenant quota, TLS files, a public URL and a successful Redis check. Startup errors for MCP-specific settings name the setting and omit its value. [Remote endpoint settings](configuration.md#remote-mcp-endpoint)

```sh
export AXIOM_AUTH_MODE=verified AXIOM_AUTH_ISSUER=https://issuer.example/tenant
export AXIOM_AUTH_AUDIENCE=https://api.example/axiom AXIOM_AUTH_JWKS_URL=https://issuer.example/keys
export AXIOM_TENANT_QUOTA_ENABLED=true REDIS_URL=redis://redis.internal:6379
export MCP_PUBLIC_URL=https://mcp.example/mcp MCP_BIND_ADDR=192.0.2.10:8443
export MCP_TLS_CERT_FILE=/etc/axiom/tls.crt MCP_TLS_KEY_FILE=/etc/axiom/tls.key
./axiom-server-launch mcp-http
```

**Resource and audience.** `MCP_PUBLIC_URL` is both the resource identifier and the only accepted token audience. It is an absolute `https` URL with a lowercase host, no credentials, query or fragment, and the fixed path `/mcp`. `AXIOM_AUTH_AUDIENCE` stays the audience of the HTTP API and must differ, so a token issued for one cannot authorize the other. Issuer, key set, algorithms and claim mappings are the HTTP API's.

**Discovery.** `GET /.well-known/oauth-protected-resource/mcp` serves public RFC 9728 metadata with the exact `resource`, the configured issuer in `authorization_servers`, `scopes_supported` (`graph:read`, `plan:generate`) and `bearer_methods_supported` of `header`. A request with no credentials or one unsupported authentication scheme receives HTTP 401 with `WWW-Authenticate: Bearer resource_metadata="<metadata URL>"`. A Bearer token that fails verification receives the same challenge with `error="invalid_token"`. A malformed authorization header receives HTTP 400 with `invalid_request`. A client reads the metadata, requests a token for `resource` from the listed issuer and retries. [RFC 6750 §3.1](https://www.rfc-editor.org/rfc/rfc6750#section-3.1) · [RFC 9728](https://www.rfc-editor.org/rfc/rfc9728)

**Scopes.** Authentication comes first. The server then decodes the request, checks the `Mcp-*` headers against the body and requires a scope for the decoded operation:

| Operation | Required scope |
|---|---|
| `server/discover`, `tools/list` | Either `graph:read` or `plan:generate` |
| `tools/call` for `list_topics`, `get_topic_path`, `get_cognitive_graph` | `graph:read` |
| `tools/call` for `generate_study_plan` | `plan:generate` |

A caller without a sufficient scope receives HTTP 403 with `WWW-Authenticate: Bearer error="insufficient_scope", scope="<needed scope>", resource_metadata="<metadata URL>"`. Discovery and listing challenges ask for `graph:read`. Any other method or tool name, including `subscriptions/listen`, is refused with a JSON-RPC error before any work or quota use. Only POST is served at `/mcp`. Other methods return HTTP 405.

**Origin and host.** A request without an `Origin` header, as a native client sends, is allowed. A supplied `Origin` must be a single value equal to an entry of `MCP_ALLOWED_ORIGINS` (default: the origin of `MCP_PUBLIC_URL`), or the request receives HTTP 403 before authentication, including requests for metadata. The listener, the protocol handler and the SDK check the same list, and allowed origins receive CORS response headers. A CORS preflight (`OPTIONS` with `Access-Control-Request-Method`) from an allowed origin returns HTTP 204 with the permitted method and headers and cannot invoke a tool. The `Host` header must name the public authority. Forwarded host, scheme and identity headers are ignored. A TLS-terminating proxy can relay to the TLS listener when it sends the expected `Host`.

**Admission.** After the scope check, every operation passes the per-IP budget. Each tool invocation then uses exactly one unit of the issuer-and-tenant budget in the same Redis decision, so HTTP API and MCP requests from one tenant draw on one budget across all replicas. Discovery and listing use only the IP budget. A denial returns HTTP 429 with `Retry-After`; unavailable storage returns HTTP 503 with `Retry-After: 1`. The client IP is the direct peer unless `TRUSTED_PROXIES` lists it. [Shared tenant quotas](configuration.md#shared-tenant-quotas)

**Limits.** Bodies are read after authentication up to 4 MiB under the 10 second read timeout; a stalled body receives HTTP 408. Each authorized request is bounded by `REQUEST_TIMEOUT`, and response writes by `REQUEST_TIMEOUT + 10s`. A client that closes its request cancels only that request's reasoning. The response is JSON unless `Accept` ranks `text/event-stream` above `application/json`. The default Go client sends both with equal preference and receives JSON. Only protocol revision 2026-07-28 is supported; other versions receive an unsupported-version error that lists the supported one.

**Providers.** Model provider credentials stay on the host that runs the command and are never read from the caller.

**Curriculum reload.** With `GRAPH_RELOAD_ENABLED=true`, `SIGHUP` publishes a validated replacement from `GRAPH_FILE` for subsequent tool calls. [Curriculum rollout](operations.md#curriculum-rollout)

**Shutdown.** On the first `SIGINT` or `SIGTERM` the listener stops accepting connections, new tool calls are rejected, and admitted calls drain for `SHUTDOWN_TIMEOUT`. Calls still running one second before that deadline are canceled. A second signal ends the process at once.

**Privacy.** Access logs (`mcp.access`) hold the request ID, HTTP method, route, status, duration, client IP and, for authorized requests, the protocol method and tool name. Logs, errors, Redis keys and prompts never contain tokens or caller, tenant or issuer strings.

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

MCP uses the same startup `LLM_PROVIDER`, `LLM_MODEL`, `LLM_BASE_URL` and provider API key settings as HTTP. A single configured client registry serves all plan tool calls. For Bedrock, set `BEDROCK_REQUIRE_EXPLICIT_REGION=true` to require a valid `AWS_REGION` before the stdio transport starts. Startup logs on stderr include `llm_provider`, `llm_model` and `aws_region_source`. See [Region Resolution](configuration.md#region-resolution).

## Tool Tracing

`TRACING_ENABLED=true` enables the same OTLP exporter and standard `OTEL_*` settings as HTTP. Each admitted `tools/call` has a server span named `tools/call <registered tool name>`; unknown tool names use `unmatched`. Plan calls contain validation, graph, topic, model-call and output spans. Tool logs (`mcp.tool`) include request, trace and span IDs. Protocol control messages do not create tool spans. Stdout remains dedicated to JSON-RPC.

The tracing provider is flushed on stdin EOF and signal shutdown. On a signal, tool draining, transport closure and trace flushing share the remaining `SHUTDOWN_TIMEOUT`; HTTP-only `SHUTDOWN_DRAIN_DELAY` is ignored.

### LangSmith

`LANGSMITH_TRACING=true` enables LangSmith independently of the generic exporter. Tool roots are annotated as `chain` and model calls as `llm`, with request-ID metadata. Only traces containing a model call are exported to LangSmith; graph-only tools and protocol control messages do not consume its export queue.

### Captured Content

`TRACING_CONTENT_DESTINATIONS` and `TRACING_CAPTURE_CONTENT` work as they do for HTTP: no receiver gets content unless it is listed, and `TRACING_CAPTURE_CONTENT=false` removes content from every receiver. When a receiver is listed, plan tool input captures only `failed_topics` and `vark_profile`, and output captures only validated items. Rendered prompts and raw completions come from the per-call collector, and the topic name is attached to the per-topic span. Learner identifiers are excluded, and every captured value is limited to 32 KiB on a UTF-8 boundary. Without a listed receiver the traces keep structure, latency, usage and error classes. Trace export never writes to stdout, which carries only protocol frames.

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

Set `PRESERVE_DISPLAY_JOINS=true` in the server environment to preserve contextual joiners and apply grapheme-safe truncation to resource titles and explanations in the tool result. See the [HTTP response contract](api.md#response-contract) for the shared rules.

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

### Remote endpoint in Docker

The image's default invocation exposes only the HTTP API port. Run the remote endpoint with the `mcp-http` command, mount the certificate and key, bind to the container's address and publish its port. The image's health check probes the HTTP API, so disable it for this command.

```sh
docker run --rm --no-healthcheck -p 8443:8443 -v /etc/axiom:/etc/axiom:ro \
  -e MCP_BIND_ADDR=172.20.0.10:8443 -e MCP_PUBLIC_URL=https://mcp.example/mcp \
  -e MCP_TLS_CERT_FILE=/etc/axiom/tls.crt -e MCP_TLS_KEY_FILE=/etc/axiom/tls.key \
  --env-file axiom.env --network axiom --ip 172.20.0.10 axiom-engine:local mcp-http
```

---

## Testing with MCP Inspector

Interactive inspection and manual tool execution can be performed using the official MCP Inspector:

```sh
npx @modelcontextprotocol/inspector /absolute/path/axiom-server-launch mcp
```

Set `BAML_LIBRARY_PATH`, `BAML_LIBRARY_DISABLE_DOWNLOAD=true`, and your provider settings in the launching shell. [Server Execution](#server-execution).
