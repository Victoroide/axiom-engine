# Deployment

## Container image

The service is packaged using a multi-stage [Dockerfile](../Dockerfile) that produces a minimal container based on Google Distroless Debian 13 (`gcr.io/distroless/cc-debian13:nonroot`, pinned by digest).

### Build stages

1. **`builder`**:
   - Copies module definitions and downloads Go dependencies.
   - Verifies that BAML version pins match across configuration and module files.
   - Downloads the precompiled BAML CFFI native library (`libbaml_cffi.so`) for the target architecture (`linux/amd64` or `linux/arm64`) from GitHub releases and verifies its SHA256 checksum against build arguments declared in [Dockerfile](../Dockerfile).
   - Compiles the stripped service binary with CGO enabled.

2. **`runtime`**:
   - Uses base image `gcr.io/distroless/cc-debian13:nonroot`, pinned by digest in the [Dockerfile](../Dockerfile).
   - Copies the compiled binary and native library to `/app`.
   - Sets environment variables:
     - `PORT=8080`
     - `BAML_LIBRARY_PATH=/app/libbaml_cffi.so`
     - `BAML_LIBRARY_DISABLE_DOWNLOAD=true`
     - `BAML_LOG=off`
   - Executes under non-root numeric user and group `65532:65532`.
   - Supports execution with a read-only root filesystem and `/tmp` mounted as tmpfs.
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
  --read-only \
  --tmpfs /tmp \
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

Tagging a commit with `v*` or manually dispatching `.github/workflows/release.yml` initiates the release pipeline.

The release workflow performs the following steps:
1. **Verification**: executes `make lint test docker-build container-smoke` and confirms that the release commit is an ancestor of `origin/main` (`git merge-base --is-ancestor "$GITHUB_SHA" origin/main`).
2. **Native platform builds**: builds each image natively on `ubuntu-latest` (`linux/amd64`) and `ubuntu-24.04-arm` (`linux/arm64`) using explicitly installed Docker CE `v29.9.2` without QEMU, loads it into a containerd image store and runs the container smoke test against it. A dry run (`dry_run: true`) then exports an OCI archive and publishes nothing. A release pushes the tested image as `ghcr.io/victoroide/axiom-engine:<commit SHA>-<arch>`, checks that the pushed digest equals the built one, and uploads the digest as an artifact. The two candidate tags stay in the registry.
3. **Platform merge**: downloads the platform digests and creates the multi-architecture manifest list using `docker buildx imagetools create` with semantic version tags (`:<tag>`, `:<version>`, `:<major>.<minor>` and `:latest`).
4. **Provenance attestation**: generates and signs SLSA build provenance attestations for the merged index digest using `actions/attest-build-provenance`.
5. **Cosign signing**: signs the merged index digest using keyless signing with Sigstore Cosign and GitHub Actions OIDC identity.
6. **Release**: creates the GitHub release corresponding to the tag and generates release notes.

Publishing jobs target the protected `release` environment, ensuring registry credentials and OIDC signing permissions are restricted to verified tag references.

### Verifying images

Published container images include SLSA build provenance attestations and Cosign signatures attached to the multi-architecture index digest.

To verify build provenance using the GitHub CLI:

```sh
gh attestation verify oci://ghcr.io/victoroide/axiom-engine:<tag> --repo Victoroide/axiom-engine
```

Use cosign v3 or later; the workflow signs with cosign v3.0.6.

To verify the Cosign signature against the GitHub Actions OIDC issuer and workflow identity:

```sh
cosign verify ghcr.io/victoroide/axiom-engine:<tag> \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github.com/Victoroide/axiom-engine/.github/workflows/release.yml@refs/tags/v'
```

### Supply chain

Release builds attach SLSA build provenance and an SPDX Software Bill of Materials (SBOM) to published container images. The precompiled BAML CFFI native library (`libbaml_cffi.so`) is verified at container build time using the SHA256 checksums pinned in the [Dockerfile](../Dockerfile) rather than upstream cryptographic signatures, and is not listed in the generated Go SBOM. Its version parity is verified using `make baml-check`.

## Operating notes

### Log format

The service writes structured JSON logs to standard output using the Go standard library `log/slog` package. Log level is configured through `LOG_LEVEL` (`debug`, `info`, `warn`, `error`; default is `info`).

Every HTTP request produces an `http.access` log record containing:
- `request_id`: the client's `X-Request-ID` when it matches `^[A-Za-z0-9._:-]{1,128}$`, otherwise a generated 43-character URL-safe token. A fallback UUID is generated only for errors raised before the request-ID middleware runs.
- `method`: HTTP method (`GET`, `POST`, `HEAD`, `OPTIONS`).
- `path`: request route path.
- `status`: HTTP response status code.
- `duration_ms`: processing duration in milliseconds.
- `ip`: client IP address resolved according to proxy configuration.

Panics recovered by middleware generate a `panic.recovered` record with `request_id`, `error`, and stack trace, then return HTTP 500 to the caller.

Pipeline and upstream failures generate a `request.failed` record with `request_id`, `error_class`, and (when known) `topic` capped at 128 characters. Upstream prompt content and error dumps are excluded from logs. When hallucinated topics returned by the planner are dropped, a `stage.validation` warning record is emitted with `request_id` and `items_dropped`.

When a termination signal initiates graceful shutdown, the server emits an `INFO` record `server.shutdown_started` containing:
- `signal`: signal name string (`terminated` or `interrupt`).
- `timeout`: configured shutdown budget as a Go duration string (for example, `"25s"`).

```json
{"level":"INFO","msg":"server.shutdown_started","signal":"terminated","timeout":"25s"}
```

This record includes standard `time`, `level`, and `msg` fields and is suppressed when `LOG_LEVEL` is set to `warn` or `error`.

### Probes

The service provides unauthenticated health probe endpoints:
- `GET /livez`: Liveness probe. Returns HTTP 200 `{"status":"ok"}` when the HTTP server is operational, including during shutdown draining.
- `GET /readyz`: Readiness probe. Returns HTTP 200 `{"status":"ok"}` when the server is ready to accept traffic, and flips to HTTP 503 `{"status":"draining"}` immediately upon receiving a shutdown signal.
- `GET /health`: Backwards-compatible alias for `/livez`.

```sh
curl http://127.0.0.1:8080/livez
curl http://127.0.0.1:8080/readyz
```

Both probes verify local process state without calling upstream dependencies, bypass rate limiting, and do not evaluate authorization headers. For full endpoint details, see [API reference](api.md).

The CLI subcommand `axiom-server healthcheck` queries `http://127.0.0.1:<PORT>/livez` with a 3-second timeout. It exits with code 0 on status 200, or code 1 if the request fails or returns a non-200 status code. The Docker image uses this command for container health checks, remaining healthy while draining.

In Kubernetes deployments, configure `livenessProbe` to query `/livez` and `readinessProbe` to query `/readyz`. Configure `SHUTDOWN_DRAIN_DELAY` (see [configuration](configuration.md)) to allow ingress controllers and endpoint slices time to propagate pod removal before the listener closes. If a platform requires an additional pre-stop delay, use native `preStop.sleep` rather than an exec hook, because the distroless container image contains no shell or `sleep` binary.

### Graceful shutdown

The service captures `SIGINT` and `SIGTERM` signals. On receipt of the initial signal:
1. Default signal behavior is restored synchronously before canceling the serving context. The server emits a `server.shutdown_started` record (see [Log format](#log-format)) and cancels the serving context.
2. The server enters the draining state. `/readyz` immediately returns HTTP 503 `{"status":"draining"}` so load balancers stop routing new traffic.
3. The TCP listener remains open and continues accepting and serving requests for the configured `SHUTDOWN_DRAIN_DELAY` duration (default `0s`). `/livez` continues returning HTTP 200 `{"status":"ok"}`.
4. After `SHUTDOWN_DRAIN_DELAY` elapses, the server closes the TCP listener to reject new connections.
5. Active requests drain within `SHUTDOWN_TIMEOUT` (see [configuration](configuration.md)). The server cancels in-flight request contexts one second before the shutdown deadline expires to reserve time to send responses. Requests that complete before cancellation retain their normal response. Requests interrupted by cancellation return HTTP 503 `service_unavailable` with `Retry-After: 1`.
6. The process exits with code 0 once all in-flight requests finish cleanly. If the shutdown deadline expires before in-flight requests complete, the server exits with code 1.

A second `SIGINT` or `SIGTERM` received after shutdown begins terminates the process immediately. In back-to-back signal scenarios (`SIGTERM` followed immediately by `SIGINT`), a second signal queued before default dispositions are restored is re-delivered after restoration, terminating the process immediately without waiting for graceful shutdown. When both signals are pending simultaneously, either signal can trigger immediate termination, and process termination can precede the `server.shutdown_started` log record.

Set container stop timeouts or Kubernetes `terminationGracePeriodSeconds` to satisfy the shutdown budget formula: `terminationGracePeriodSeconds >= SHUTDOWN_DRAIN_DELAY + SHUTDOWN_TIMEOUT + 5` seconds, plus any additional `preStop` delay.

Docker Compose sets `stop_grace_period: 30s`. With `docker run` or `docker stop`, pass `--stop-timeout 30` or `-t 30`; Docker's default of 10 seconds kills the process before the default 25-second budget ends. Raise these values when `SHUTDOWN_DRAIN_DELAY + SHUTDOWN_TIMEOUT + 5` exceeds 30 seconds.

### Token rotation

To rotate internal service credentials without downtime:

1. Generate a new token meeting complexity requirements (`openssl rand -hex 32`).
2. Deploy the service with the new token configured as `AXIOM_INTERNAL_TOKEN` and the existing token configured as `AXIOM_INTERNAL_TOKEN_PREVIOUS` (see [configuration](configuration.md)).
3. After every replica runs the new configuration, update callers to send the new token. During rotation, requests bearing either token authenticate successfully via `X-Internal-Token` or `Authorization: Bearer` (an explicit `X-Internal-Token` header takes precedence). Requests with missing or invalid credentials return HTTP 401 `unauthorized`.
4. Before removing the previous token, check that no caller still sends it. Each replica logs `auth.previous_token_used` at WARN (with the request's `request_id`, at most once per minute per process) while requests authenticate with the previous token. With `METRICS_ENABLED=true`, every such request also increments `axiom_auth_previous_token_total`. When the counter has stopped increasing and no warning has appeared for more than a minute on every replica, remove `AXIOM_INTERNAL_TOKEN_PREVIOUS` and redeploy.

### Metrics

When `METRICS_ENABLED=true` is configured (see [configuration](configuration.md)), the server exposes Prometheus text exposition at `GET /metrics`. The endpoint requires no authentication token and does not consume API rate limit budget. Restrict access to `/metrics` at the network or reverse-proxy level rather than relying on application authentication.

Scrape configuration example:

```yaml
scrape_configs:
  - job_name: axiom-engine
    metrics_path: /metrics
    static_configs:
      - targets: ['axiom-engine:8080']
```

### Production settings

Setting `SWAGGER_ENABLED=false` (see [configuration](configuration.md)) is recommended in production deployments to disable Swagger UI interactive documentation, OpenAPI specification endpoints, and static asset routes. When disabled, `/swagger/index.html`, `/swagger/doc.json`, and static assets are not registered and return HTTP 404 with the standard `not_found` error envelope.

When enabled (`SWAGGER_ENABLED=true`, default), Swagger routes remain public and deliver a restricted `Content-Security-Policy` header:

```http
default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; font-src 'self'; base-uri 'none'; form-action 'self'
```

This policy blocks third-party Google Fonts requests so the interface renders using native fallback fonts, while local scripts, styles, and the interactive "Try it out" feature remain fully functional. `base-uri 'none'` and `form-action 'self'` stop injected markup from changing the page's base URL or submitting forms to another origin.
