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

For shared mode, set `AXIOM_INTERNAL_TOKEN` in `.env`. For verified mode, set the [issuer, API audience and key set URL](#verified-access-tokens). The service port binds exclusively to loopback (`127.0.0.1:8080:8080`), making it unreachable from other network hosts without modifying `ports` (see [Quick Start](../README.md#quick-start)). Compose reads configuration from an optional `.env` file in the repository root.

## Local and OpenAI-compatible providers

AxiomEngine can execute planning without AWS credentials by pointing to a local or hosted OpenAI-compatible model server (such as Ollama, vLLM, or LM Studio).

### Ollama

1. Start Ollama and pull your desired model:

```sh
ollama pull mistral:v0.3
```

2. Run AxiomEngine pointing to Ollama:

```sh
export LLM_PROVIDER=ollama
export LLM_MODEL=mistral:v0.3
# LLM_BASE_URL defaults to http://localhost:11434/v1
export AXIOM_INTERNAL_TOKEN="$(openssl rand -hex 32)"
make run
```

The default curriculum budget for `mistral:v0.3` fits a 4,096-token context. When provisioning a different context, set `LLM_OLLAMA_CONTEXT_TOKENS` to match it. Confirm the loaded context with `ollama ps`. [Ollama context settings](https://docs.ollama.com/context-length) · [Curriculum prompt budget](configuration.md#curriculum-prompt-budget)

### Generic OpenAI-compatible endpoints

For vLLM, LM Studio, or self-hosted model gateways:

```sh
export LLM_PROVIDER=openai-generic
export LLM_MODEL=meta-llama/Llama-3.1-8B-Instruct
export LLM_BASE_URL="http://localhost:8000/v1"
# Set LLM_API_KEY if the endpoint requires bearer authorization:
export LLM_API_KEY="secret-key"
export AXIOM_INTERNAL_TOKEN="$(openssl rand -hex 32)"
make run
```

When using non-Bedrock providers, AWS credentials, regions, and SDK retry options are not required and are ignored at startup.

## Release images

Tagging a commit with `v*` or manually dispatching `.github/workflows/release.yml` initiates the release pipeline.

The release workflow performs the following steps:
1. **Verification**: executes `make lint test docker-build container-smoke` and confirms that the release commit is an ancestor of `origin/main` (`git merge-base --is-ancestor "$GITHUB_SHA" origin/main`).
2. **Native platform builds**: builds each image natively on `ubuntu-latest` (`linux/amd64`) and `ubuntu-24.04-arm` (`linux/arm64`) using explicitly installed Docker CE `v29.9.2` without QEMU, loads it into a containerd image store and runs the container smoke test against it. A dry run (`dry_run: true`) then exports an OCI archive and publishes nothing. A release pushes the tested image as `ghcr.io/victoroide/axiom-engine:<commit SHA>-<arch>`, checks that the pushed digest equals the built one, and uploads the digest as an artifact. The two candidate tags stay in the registry.
3. **Platform merge**: waits for all three release archives to pass verification, downloads the platform digests and creates the multi-architecture manifest list using `docker buildx imagetools create` with semantic version tags (`:<tag>`, `:<version>`, `:<major>.<minor>` and `:latest`).
4. **Provenance attestation**: generates and signs SLSA build provenance attestations for the merged index digest using `actions/attest-build-provenance`.
5. **Cosign signing**: signs the merged index digest using keyless signing with Sigstore Cosign and GitHub Actions OIDC identity.
6. **Release**: creates the GitHub release corresponding to the tag and generates release notes.
7. **Release archives**: signs `checksums.txt` with Cosign and uploads platform archives (`axiom-engine_<version>_<os>_<arch>.tar.gz`), `checksums.txt`, and the signature bundle to the release.
8. **Provenance**: generates SLSA build provenance for the archives using `actions/attest-build-provenance` and attaches the Sigstore bundle (`axiom-engine_<version>.sigstore.json`) and DSSE envelope (`axiom-engine_<version>.intoto.jsonl`) to the release.

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

Release builds attach SLSA build provenance and an SPDX Software Bill of Materials (SBOM) to published container images. Archive and image builds check the precompiled BAML CFFI library against the SHA-256 pins in the [Dockerfile](../Dockerfile). `make baml-check` checks that the BAML generator and Go module use the pinned native library version.

Releases can include a supplemental `baml-native.cdx.json` alongside the native library in each archive and at `/app/baml-native.cdx.json` in each image. The inventory records the BAML version, upstream asset name and download URL, `axiom-engine:target` in `os/arch` form, and the SHA-256 of the shipped library. Images also carry their existing SPDX SBOM. [CycloneDX 1.7 schema](https://github.com/CycloneDX/specification/blob/1.7.2/schema/bom-1.7.schema.json).

The signed archive checksums cover the embedded inventory and library bytes. For images, the signed index digest covers the platform manifests and their layers, including both files. AxiomEngine's signatures establish the identity of the AxiomEngine release workflow. The upstream URL and pinned digest identify the bytes AxiomEngine used. Establishing upstream publisher identity requires a verifiable upstream signature or attestation for that exact BAML asset.

To inspect an image inventory, verify the release and use the verified index digest to select the platform. The following commands copy the inventory and library from a stopped container, then compare the library with the reported hash.

```sh
(
  set -eu
  image="ghcr.io/victoroide/axiom-engine@${digest:?Set digest to the verified index digest}"
  scratch=$(mktemp -d)
  container=
  trap 'if [ -n "$container" ]; then docker rm "$container" >/dev/null || true; fi; rm -rf "$scratch"' EXIT
  cd "$scratch"
  docker pull --platform linux/arm64 "$image"
  container=$(docker create --platform linux/arm64 "$image")
  docker cp "$container:/app/baml-native.cdx.json" ./baml-native.cdx.json
  docker cp "$container:/app/libbaml_cffi.so" ./libbaml_cffi.so
  jq -er '.components[0] | select(any(.properties[]; .name == "axiom-engine:target" and .value == "linux/arm64")) | .hashes[] | select(.alg == "SHA-256") | .content + "  libbaml_cffi.so"' baml-native.cdx.json > native.sha256
  sha256sum -c native.sha256
)
```

## Release archives

Published GitHub releases attach native executable archives for `linux/amd64`, `linux/arm64`, and `darwin/arm64` alongside container images.

Each release attaches:
- `axiom-engine_<version>_<os>_<arch>.tar.gz`: platform archives containing `axiom-server-launch`, `axiom-server`, the verified native BAML CFFI library, `LICENSE`, and `README.md`. `<version>` omits the release tag's leading `v`.
- `checksums.txt`: SHA-256 digests of all three archives.
- `checksums.txt.sigstore.json`: Cosign keyless signature bundle verifying `checksums.txt`.
- `axiom-engine_<version>.sigstore.json`: Sigstore bundle containing SLSA build provenance and verification material for all three archives.
- `axiom-engine_<version>.intoto.jsonl`: DSSE envelope extracted from the Sigstore provenance bundle as a single JSON line.

### Verify a release

To download the release assets using the GitHub CLI:

```sh
gh release download v1.2.3 --repo Victoroide/axiom-engine \
  --pattern 'axiom-engine_*.tar.gz' \
  --pattern 'checksums.txt*' \
  --pattern 'axiom-engine_1.2.3.sigstore.json' \
  --pattern 'axiom-engine_1.2.3.intoto.jsonl'
```

When validating against the complete checksum list, ensure all archives are downloaded, then verify their checksums:

```sh
sha256sum -c checksums.txt
```

(Use `shasum -a 256 -c checksums.txt` on macOS).

Authenticate the checksums file using Cosign keyless verification against the workflow identity, substituting the exact escaped release tag:

```sh
cosign verify-blob \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github[.]com/Victoroide/axiom-engine/[.]github/workflows/release[.]yml@refs/tags/v1[.]2[.]3$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
```

Verify build provenance for an archive against the repository attestations using the GitHub CLI:

```sh
gh attestation verify axiom-engine_1.2.3_linux_amd64.tar.gz --repo Victoroide/axiom-engine
```

Verify the archive against the attached Sigstore provenance bundle using Cosign, substituting the release tag:

```sh
cosign verify-blob-attestation \
  --bundle axiom-engine_1.2.3.sigstore.json \
  --type https://slsa.dev/provenance/v1 \
  --certificate-identity "https://github.com/Victoroide/axiom-engine/.github/workflows/release.yml@refs/tags/v1.2.3" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  axiom-engine_1.2.3_linux_amd64.tar.gz
```

Verify signatures, checksums, and provenance before extracting or running any release archive.

For an archive that contains `baml-native.cdx.json`, check that the target matches the archive platform and compare the listed SHA-256 with the native file after extraction. For example, in the extracted Linux amd64 archive run the following.

```sh
jq -er '.components[0] | select(any(.properties[]; .name == "axiom-engine:target" and .value == "linux/amd64")) | .name as $asset | .hashes[] | select(.alg == "SHA-256") | .content + "  " + $asset' baml-native.cdx.json |
  sha256sum -c -
```

On macOS use the `darwin/arm64` target and `shasum -a 256 -c -`.

### Run an archive

Extract the matching platform archive and navigate into its directory:

```sh
tar -xzf axiom-engine_1.2.3_linux_amd64.tar.gz
cd axiom-engine_1.2.3_linux_amd64
```

Set `BAML_LIBRARY_PATH` to the bundled native library for your platform:
- `linux/amd64`: `libbaml_cffi-x86_64-unknown-linux-gnu.so`
- `linux/arm64`: `libbaml_cffi-aarch64-unknown-linux-gnu.so`
- `darwin/arm64`: `libbaml_cffi-aarch64-apple-darwin.dylib`

Export the settings shared by the server and healthcheck. `BAML_LIBRARY_DISABLE_DOWNLOAD=true` disables runtime library downloads. Generate an internal token with `openssl rand -hex 32` and configure credentials for your provider.

```sh
# On linux/amd64:
export BAML_LIBRARY_PATH="$PWD/libbaml_cffi-x86_64-unknown-linux-gnu.so"
export BAML_LIBRARY_DISABLE_DOWNLOAD=true
export AXIOM_INTERNAL_TOKEN="$(openssl rand -hex 32)"
export AWS_REGION="us-east-1"
export AWS_ACCESS_KEY_ID="your-access-key-id"
export AWS_SECRET_ACCESS_KEY="your-secret-access-key"

./axiom-server-launch
```

The launcher accepts an unset `BAML_LOG` or `off`, `error`, `warn`, or `warning`, regardless of case. It rejects other values before native initialization. It sets `BAML_LOG=off` and `BAML_INTERNAL_LOG=off` and clears `BAML_TRACE_FILE`, `BOUNDARY_API_KEY`, and `BOUNDARY_SECRET`, including case variants of the `SECRET` suffix. The native library can write prompts and responses to logs and trace files or send them to its trace publisher. Use those same settings when launching `axiom-server` directly without native diagnostics. With the pinned library, an unset `BAML_LOG` or `warning` on the raw binary activates INFO. Even `warn` can expose prompts and responses when a call fails.

The binary health probe tests liveness against `/livez`, inheriting the exported environment:

```sh
./axiom-server-launch healthcheck
```

Sending `SIGTERM` requests a clean process shutdown with exit status 0.

### Check the native library before startup

Set `BAML_DIAGNOSTIC=1` when building an archive from source to include `axiom-baml-check`.

```sh
make release-archive BAML_DIAGNOSTIC=1
```

Extract that archive, export `BAML_LIBRARY_PATH` as shown above, and run this in the shell that starts the server. The shell executes the launcher only if the diagnostic succeeds.

```sh
./axiom-baml-check && exec ./axiom-server-launch
```

Build the image with the same option. Run the executable as a separate preflight or init step with the same image, library path, mounts, and user as the service. This command checks the bundled library at the image's default path. `--no-healthcheck` prevents Docker from starting the native server health probe during the preflight.

```sh
make docker-build BAML_DIAGNOSTIC=1
docker run --rm --read-only --network none --no-healthcheck --entrypoint /app/axiom-baml-check \
  axiom-engine:local
```

The archive launcher and image entrypoint start the service directly. Diagnostic exit codes and native loader checks are covered in [Native library failures](operations.md#native-library-failures).

## Operating notes

### Choosing a curriculum

By default, the server loads an embedded seed curriculum of 23 concepts. The container image bundles a larger computer science curriculum (130 concepts) located at `/usr/share/axiom-engine/graphs/computer-science.json`, owned by `root:root` and readable by the non-root runtime user.

To run the container with the bundled curriculum:

```sh
docker run --rm -p 8080:8080 \
  -e AXIOM_AUTH_DISABLED=true \
  -e AWS_REGION=us-east-1 \
  -e GRAPH_FILE=/usr/share/axiom-engine/graphs/computer-science.json \
  axiom-engine:local
```

Include AWS provider credentials (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`) when generating plans.

To validate the bundled curriculum file directly using the container binary:

```sh
docker run --rm axiom-engine:local validate-graph /usr/share/axiom-engine/graphs/computer-science.json
```

To reload a curriculum in a running container, mount its directory and set `GRAPH_FILE` to the path inside that mount. Stage and rename the replacement on the host, then send `docker kill --signal=HUP <container>` to each replica started with `GRAPH_RELOAD_ENABLED=true`. A directory mount exposes the replacement after the rename. [Curriculum rollout](operations.md#curriculum-rollout)

### Log format

The service writes structured JSON logs to standard output using the Go standard library `log/slog` package. Log level is configured through `LOG_LEVEL` (`debug`, `info`, `warn`, `error`; default is `info`).

During initialization, the structured JSON logger is configured before validating configuration, starting at `INFO` level and switching to the configured `LOG_LEVEL` once configuration is loaded. If configuration parsing, AWS retry validation, health checks, or server initialization fail, the process logs a single `ERROR` record `server.failed` containing `error` before exiting with code 1:

```json
{"time":"2026-11-02T22:40:57.539156-04:00","level":"ERROR","msg":"server.failed","error":"PORT must be an integer between 1 and 65535"}
```

A missing or mismatched BAML CFFI native library aborts during package initialization before `main` runs, resulting in a plain-text panic rather than a JSON log record.

When the service starts successfully, it emits `server.started` and `server.listening` in sequence before serving requests (subject to `LOG_LEVEL`). With curriculum reload enabled, a `graph.reload` record reports the initial revision before these records.

1. `server.started`: reports operational settings and graph metadata while omitting sensitive tokens and secret material. Fields include:
   - `port`: HTTP listener port string.
   - `auth`: authentication configuration (`token`, `token+previous`, `verified` or `disabled`).
   - `llm_provider`: configured reasoning model provider (`aws-bedrock`, `openai`, `anthropic`, `google-ai`, `ollama`, or `openai-generic`).
   - `llm_model`: configured model identifier string.
   - `swagger_enabled`: boolean indicating whether Swagger documentation routes are active.
   - `metrics_enabled`: boolean indicating whether Prometheus metrics exposition is active.
   - `rate_limit_max`: sliding-window request limit.
   - `rate_limit_window`: rate limit sliding-window duration as a Go duration string.
   - `request_timeout`: request execution timeout duration string.
   - `llm_timeout` gives the maximum wait for an individual planner result as a Go duration string.
   - `llm_queue_timeout`: queue acquisition timeout duration string.
   - `shutdown_timeout`: graceful shutdown drain timeout duration string.
   - `shutdown_drain_delay`: pre-shutdown listener drain delay duration string.
   - `llm_max_concurrency`: maximum concurrent in-flight LLM calls.
   - `max_failed_topics`: maximum accepted unique failed topics.
   - `cors_origins_count`: integer count of allowed CORS origins.
   - `trusted_proxies_count`: integer count of configured trusted proxies.
   - `graph_source`: curriculum graph origin (`embedded` or `file`).
   - `graph_tuples`: count of loaded curriculum graph tuples.
   - `baml_version`: build dependency version string for BAML, or `"unknown"`.
   - `aws_region_source` reports `AWS_REGION` when explicit region validation is enabled. In default Bedrock mode it reports `AWS_REGION`, `AWS_DEFAULT_REGION`, `profile:<name>` or `""` from static inspection. It is empty for other providers.

```json
{"level":"INFO","msg":"server.started","port":"8080","auth":"token","llm_provider":"aws-bedrock","llm_model":"amazon.nova-micro-v1:0","swagger_enabled":true,"metrics_enabled":false,"rate_limit_max":50,"rate_limit_window":"1m0s","request_timeout":"20s","llm_timeout":"15s","llm_queue_timeout":"2s","shutdown_timeout":"25s","shutdown_drain_delay":"0s","llm_max_concurrency":16,"max_failed_topics":10,"cors_origins_count":0,"trusted_proxies_count":0,"graph_source":"embedded","graph_tuples":20,"baml_version":"v0.226.3","aws_region_source":"AWS_REGION"}
```

In default Bedrock mode, an empty `aws_region_source` produces the `llm.region.unresolved` warning at `WARN`, subject to `LOG_LEVEL`. Set `BEDROCK_REQUIRE_EXPLICIT_REGION=true` to require `AWS_REGION` before startup can succeed. See [Region Resolution](configuration.md#region-resolution).

```json
{"level":"WARN","msg":"llm.region.unresolved","instance_metadata":"enabled","detail":"instance metadata may still supply a region"}
```

The `instance_metadata` field reports `disabled` when `AWS_EC2_METADATA_DISABLED=true`, or `enabled` otherwise.

2. `server.listening`: emitted after the TCP socket binds:

```json
{"level":"INFO","msg":"server.listening","address":"[::]:8080","listener":"fiber"}
```

Requests that reach the application produce an `http.access` log record with these fields.
- `request_id`: the client's `X-Request-ID` when it matches `^[A-Za-z0-9._:-]{1,128}$`, otherwise a generated 43-character URL-safe token. A fallback UUID is generated only for errors raised before the request-ID middleware runs.
- `method`: HTTP method (`GET`, `POST`, `HEAD`, `OPTIONS`).
- `path`: the matched route template or `unmatched` when no route was resolved. This applies in every authentication mode and keeps learner identifiers from request paths out of access logs.
- `status`: HTTP response status code.
- `duration_ms`: processing duration in milliseconds (for streaming routes, measures the full connection duration until stream close).
- `ip`: client IP address resolved according to proxy configuration.
- `client_disconnected` is true when the opt-in listener observes peer cancellation or an SSE write or flush fails.

A recovered HTTP handler panic emits `panic.recovered` and returns HTTP 500 `internal_error`. A recovered planner panic emits `planner.panic`. Each recovery emits one `ERROR` record with `error_class="internal_error"`, the HTTP or MCP `request_id`, and `trace_id` and `span_id` when available. Standard JSON lines for these records are at most 1024 bytes including the newline. The `request_id` is capped at 128 bytes, the recovered value is omitted, and the record contains zero stack frames.

When a recovered planner panic determines the plan failure, the JSON route returns HTTP 500 `internal_error` and stdio MCP returns an `internal_error` tool error. In that case, an open, connected SSE stream keeps HTTP 200 and sends a terminal `error` event with `error="internal_error"` and `status=500`. A deadline, cancellation, or an earlier error from another topic can determine the response first. A late planner panic still emits its correlated record and releases its permit as the call unwinds. See [Planner Panics](operations.md#planner-panics).

The `request.failed` record for pipeline and upstream failures includes `request_id`, `error_class`, and, when known, `topic` capped at 128 characters. This record omits upstream prompt content and error dumps. When hallucinated topics returned by the planner are dropped, a `stage.validation` warning record is emitted with `request_id` and `items_dropped`.

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

To rotate shared-mode credentials without downtime:

1. Generate a new token meeting complexity requirements (`openssl rand -hex 32`).
2. Deploy the service with the new token configured as `AXIOM_INTERNAL_TOKEN` and the existing token configured as `AXIOM_INTERNAL_TOKEN_PREVIOUS` (see [configuration](configuration.md)).
3. After every replica runs the new configuration, update callers to send the new token. During rotation, requests bearing either token authenticate successfully via `X-Internal-Token` or `Authorization: Bearer` (an explicit `X-Internal-Token` header takes precedence). Requests with missing or invalid credentials return HTTP 401 `unauthorized`.
4. Before removing the previous token, check that no caller still sends it. Each replica logs `auth.previous_token_used` at WARN (with the request's `request_id`, at most once per minute per process) while requests authenticate with the previous token. With `METRICS_ENABLED=true`, every such request also increments `axiom_auth_previous_token_total`. When the counter has stopped increasing and no warning has appeared for more than a minute on every replica, remove `AXIOM_INTERNAL_TOKEN_PREVIOUS` and redeploy.

### Verified access tokens

Configure each HTTP replica with the same trusted issuer, resource audience, HTTPS key set URL, signing algorithms and claim mappings. Leave the shared secrets unset and keep authentication enabled.

```sh
export AXIOM_AUTH_MODE=verified
export AXIOM_AUTH_ISSUER=https://identity.example/issuer
export AXIOM_AUTH_AUDIENCE=https://api.example/axiom
export AXIOM_AUTH_JWKS_URL=https://identity.example/keys
```

Configure the issuer to issue signed access tokens for the API audience with stable tenant and caller claims and the scopes required by each client. Allow the server to reach the configured key set URL over HTTPS. The server loads keys on demand, starts refresh attempts at least 30 seconds apart, waits at most two seconds per refresh and expires a cached set after ten minutes. Verified HTTP mode uses zero clock skew. Publish the new key with a distinct key ID before issuing tokens with it. Keep the existing public key available while clients still use tokens signed with that key.

Replay survives token rotation for the same issuer, tenant and caller when the new token retains `plan:generate`. The server checks the scopes in the token presented with each request. Previously issued tokens remain usable until expiration while their signing keys are accepted. Each authentication mode uses a separate replay namespace, including on deployments with shared Redis. [Configuration](configuration.md#verified-http-authentication) · [Route scopes](api.md#verified-mode)

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

### Replicas and shared storage

In deployments running multiple service replicas behind a load balancer, configure `REDIS_URL` with a shared Redis endpoint (`redis://` or TLS `rediss://`) reachable by all instances:

- **Shared Quotas**: Replicas share the sliding-window IP rate limit budget (`RATE_LIMIT_MAX` per `RATE_LIMIT_WINDOW`) using an atomic Redis Lua script under key prefix `axiom:rate:`. Quota evaluation uses Redis server time for consistent whole-second window rotation.
- **Shared Idempotency Records**: Replicas share plan reservations and responses under key prefix `axiom:idempotency:`. A pending reservation is leased for `REQUEST_TIMEOUT + 3s`, ensuring active reasoning executions are not timed out by response TTL while abandoned reservations expire automatically after replica failure. Successful responses are cached for `IDEMPOTENCY_TTL`.
- Upgrade Redis-backed replicas together so every replica serving traffic can read the new replay record format. Upgraded replicas can replay earlier records for the remainder of their original TTL.
- Do not roll a replica back to 0.1.1 or earlier while shared or anonymous plan records written by this version remain in Redis. The earlier reader sends the stored body unchanged, so a retry would receive only `items` and `_meta`, without `student_id` and `course_id`. Before an earlier release serves retries, let those records expire after `IDEMPOTENCY_TTL`, give the earlier release a different Redis database, or delete the keys under `axiom:idempotency:` after new keyed requests stop and pending reservations expire. Draining or isolating the records ends replay for them. A retry with the same `Idempotency-Key` then runs a new model inference.
- **Fail-Closed Availability**. If Redis is unreachable at startup, the process exits 1 after the initial ping fails. At runtime, storage errors return HTTP 503 `service_unavailable` with `Retry-After: 1`. The `storage.failed` log names the operation, such as `rate_limit.consume`, `tenant_quota.consume`, `idempotency.begin`, `idempotency.complete` or `idempotency.abort`. Admission resumes when Redis recovers.
- **Local Health Endpoints**: Health probes (`/health`, `/livez`, `/readyz`) remain strictly process-local and do not query Redis.
- **Tenant quotas**. Shared tenant quotas are off by default. With verified HTTP authentication, set `AXIOM_TENANT_QUOTA_ENABLED=true` on all replicas. Keep policy settings and the Redis database consistent across the rollout. The tenant key uses `axiom:tenant-rate:v1:` followed by a SHA-256 digest of the issuer and tenant, each prefixed with its byte length. Redis evaluates IP then tenant in one atomic operation. Each tenant bucket counts admissions in the current window and adds the previous count weighted by the remaining fraction of the current window, rounded up. This whole-second approximation expires buckets after the end of the following window. [Tenant quota settings](configuration.md#shared-tenant-quotas)

Planner permits and circuit breakers are process-local with shared Redis. See the [replica concurrency budget](operations.md#replica-concurrency-budget) for provider and gateway sizing.

### Production settings

Setting `SWAGGER_ENABLED=false` (see [configuration](configuration.md)) is recommended in production deployments to disable Swagger UI interactive documentation, OpenAPI specification endpoints, and static asset routes. When disabled, `/swagger/index.html`, `/swagger/doc.json`, and static assets are not registered and return HTTP 404 with the standard `not_found` error envelope.

When enabled (`SWAGGER_ENABLED=true`, default), Swagger routes remain public and deliver a restricted `Content-Security-Policy` header:

```http
default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; font-src 'self'; base-uri 'none'; form-action 'self'
```

This policy blocks third-party Google Fonts requests so the interface renders using native fallback fonts, while local scripts, styles, and the interactive "Try it out" feature remain fully functional. `base-uri 'none'` and `form-action 'self'` stop injected markup from changing the page's base URL or submitting forms to another origin.

### Operational runbook

For operational troubleshooting procedures, circuit breaker responses, throttling metrics, region and credential diagnostics, and production telemetry, see the [Operations Runbook](operations.md).
