# Deployment

## Container image

The service is packaged using a multi-stage [Dockerfile](../Dockerfile) that produces a minimal container based on Google Distroless Debian 12.

### Build stages

1. **`builder`**:
   - Copies module definitions and downloads Go dependencies.
   - Verifies that BAML version pins match across configuration and module files.
   - Downloads the precompiled BAML CFFI native library (`libbaml_cffi.so`) for the target architecture (`linux/amd64` or `linux/arm64`) from GitHub releases and verifies its SHA256 checksum against build arguments declared in [Dockerfile](../Dockerfile).
   - Compiles the stripped service binary with CGO enabled.

2. **`runtime`**:
   - Copies the compiled binary and native library to `/app`.
   - Sets environment variables:
     - `PORT=8080`
     - `BAML_LIBRARY_PATH=/app/libbaml_cffi.so`
     - `BAML_LIBRARY_DISABLE_DOWNLOAD=true`
     - `BAML_LOG=off`
   - Executes under non-root numeric user and group `65532:65532`.
   - Exposes port `8080`.
   - Configures container health check: `CMD ["/app/axiom-server", "healthcheck"]`.
   - Sets entrypoint to `["/app/axiom-server"]`.

To build the runtime image locally:

```sh
docker build --target runtime -t axiom-engine:local .
```

To run the container:

```sh
docker run -d \
  --name axiom-engine \
  --user 65532:65532 \
  -p 8080:8080 \
  -e AXIOM_INTERNAL_TOKEN="$(openssl rand -hex 32)" \
  -e AWS_REGION="us-east-1" \
  -e AWS_ACCESS_KEY_ID="your-access-key-id" \
  -e AWS_SECRET_ACCESS_KEY="your-secret-access-key" \
  axiom-engine:local
```

## Docker Compose

The repository includes [docker-compose.yml](../docker-compose.yml) for managing local or staging container deployments:

```sh
docker compose up -d
```

Compose requires a valid `AXIOM_INTERNAL_TOKEN` in `.env` unless authentication is explicitly disabled (see [configuration](configuration.md)). The service port binds exclusively to loopback (`127.0.0.1:8080:8080`), making it unreachable from other network hosts without modifying `ports` (see [Quick Start](../README.md#quick-start)). Compose reads configuration from an optional `.env` file in the repository root.

## Release images

Tagging a commit with `v*` triggers the `.github/workflows/release.yml` workflow.

The workflow performs the following steps:
1. Sets up QEMU for multi-architecture emulation and Docker Buildx.
2. Authenticates against GitHub Container Registry (`ghcr.io`).
3. Extracts image tags and OCI labels based on semantic versioning rules (`ghcr.io/victoroide/axiom-engine:<tag>`, `:<major>.<minor>`, `:<version>`).
4. Compiles and pushes multi-architecture images for `linux/amd64` and `linux/arm64`.
5. Attaches signed build provenance and Software Bill of Materials (SBOM).
6. Creates a GitHub release corresponding to the tag and generates release notes.

## Operating notes

### Log format

The service writes structured JSON logs to standard output using the Go standard library `log/slog` package. Log level is configured through `LOG_LEVEL` (`debug`, `info`, `warn`, `error`; default is `info`).

Every HTTP request produces an `http.access` log record containing:
- `request_id`: unique request identifier from the `X-Request-ID` header or generated 43-character URL-safe token. A fallback UUID is generated only for parser-level errors that precede request middleware execution.
- `method`: HTTP method (`GET`, `POST`, `HEAD`, `OPTIONS`).
- `path`: request route path.
- `status`: HTTP response status code.
- `duration_ms`: processing duration in milliseconds.
- `ip`: client IP address resolved according to proxy configuration.

Panics recovered by middleware generate a `panic.recovered` record with `request_id`, `error`, and stack trace, then return HTTP 500 to the caller.

Pipeline and upstream failures generate a `request.failed` record with `request_id`, `error_class`, and (when known) `topic` capped at 128 characters. Upstream prompt content and error dumps are excluded from logs. When hallucinated topics returned by the planner are dropped, a `stage.validation` warning record is emitted with `request_id` and `items_dropped`.

### Health endpoint

The service provides an unauthenticated liveness endpoint at `GET /health`.

```sh
curl http://127.0.0.1:8080/health
```

Response:

```json
{"status":"ok"}
```

HTTP status is 200 OK. The endpoint verifies that the HTTP server is listening and operational; it does not evaluate external downstream dependencies. Requests to `/health` bypass rate limiting and do not consume rate limit tokens. For full endpoint details, see [API reference](api.md).

The CLI subcommand `axiom-server healthcheck` queries `http://127.0.0.1:<PORT>/health` with a 3-second timeout. It exits with code 0 on status 200, or code 1 if the request fails or returns a non-200 status code. The Docker image uses this command for container health checks.

### Graceful shutdown

The service captures `SIGINT` and `SIGTERM` signals. On receipt of either signal:
1. The server closes the TCP listener to reject new connections.
2. Active requests drain within `SHUTDOWN_TIMEOUT` (default: 10 seconds).
3. The process exits with code 0 once all in-flight requests finish cleanly. If the shutdown deadline expires before in-flight requests complete, the server exits with code 1.

Set orchestrator container stop timeouts to at least 15 seconds to allow the shutdown drain period to complete cleanly before `SIGKILL`.
