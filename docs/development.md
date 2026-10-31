# Development

For contribution workflow, commit conventions, and review procedures, see [CONTRIBUTING.md](../CONTRIBUTING.md). For security reporting, see [SECURITY.md](../SECURITY.md).

## Prerequisites

- **Go**: 1.27.3, matching `go.mod`.
- **C compiler**: A standard C compiler toolchain (such as `gcc` or `clang`) with CGO enabled. CGO is required for BAML native CFFI bindings.
- **Docker**: Engine 24+ for building runtime images and executing container health checks.
- **Chrome or Chromium**: Required for browser end-to-end tests. The test runner detects local system installations or uses the path specified in `ROD_BROWSER_BIN`.
- **ShellCheck**: required by `make lint`; actionlint uses it to check workflow `run:` steps, as the CI runners do.

<a id="pinned-tool-versions"></a>
## Tooling

Developer and CI tools are declared using Go `tool` directives in `tools/go.mod` with cryptographic checksums pinned in `tools/go.sum`. This isolated module is the single source of truth for tool versions:

| Tool | Version | Directive | Description |
| --- | --- | --- | --- |
| `govulncheck` | `v1.9.0` | `golang.org/x/vuln/cmd/govulncheck` | Go vulnerability scanner |
| `swag` | `v1.16.6` | `github.com/swaggo/swag/cmd/swag` | Swagger 2.0 generator |
| `goimports` | `v0.52.0` | `golang.org/x/tools/cmd/goimports` | Import organizer and formatter |
| `actionlint` | `v1.7.12` | `github.com/rhysd/actionlint/cmd/actionlint` | GitHub Actions workflow linter |

Run pinned tools from the repository root using `go tool`:

```sh
go tool -modfile=tools/go.mod <tool> [args...]
```

To update a pinned tool version:

```sh
go -C tools get -tool <package>@<version>
go -C tools mod tidy
```

`golangci-lint` (`v2.15.0`) intentionally remains executed via `go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0` in accordance with upstream guidance. `baml-cli` (`0.226.2`) is installed into `bin/` via `make tools-baml` matching the version in `baml_src/generators.baml`. CI caches modules by both `go.sum` and `tools/go.sum`.

## Make targets

Run `make help` to inspect available Makefile targets:

| Target | Description |
| --- | --- |
| `help` | List available targets. |
| `tools-baml` | Install the generator version declared in `generators.baml`. |
| `baml-check` | Check generator, module, and native-library pins agree. |
| `native-baml` | Download and verify the pinned native library for this host. |
| `generate` | Regenerate and format the BAML client. |
| `swagger` | Regenerate the Swagger specification and embedded docs. |
| `build` | Build the stripped service binary with CGO. |
| `run` | Run the service using the current environment. |
| `test` | Run unit tests with race detection. |
| `cover` | Run race tests and print total statement coverage. |
| `test-e2e` | Test the API documentation in Chrome or Chromium. |
| `lint` | Vet build variants and check Go code and workflow files. |
| `vuln` | Check reachable Go vulnerabilities. |
| `fmt` | Format maintained Go source files. |
| `tidy-check` | Fail if module metadata is not tidy. |
| `generate-check` | Fail if committed generated code is stale. |
| `docker-build` | Build the non-root runtime image. |
| `container-smoke` | Check runtime responses, log privacy and shutdown. |
| `container-smoke-test` | Verify rejection of unsafe runtime images. |
| `clean` | Remove local binaries and coverage artifacts. |

## Regenerating code

### BAML client

The BAML client in `baml_client/` is generated from definitions in `baml_src/`.

To regenerate the client:

```sh
make generate
```

This target performs the following actions:
1. Runs `make native-baml` to download and verify the host native library against pinned checksums declared in [Dockerfile](../Dockerfile).
2. Runs `make baml-check` to verify that `baml_src/generators.baml`, `ARG BAML_LIB_VERSION` in [Dockerfile](../Dockerfile), and `github.com/boundaryml/baml` in [go.mod](../go.mod) all specify version `0.226.2`.
3. Compiles `baml-cli` into `bin/baml-cli-0.226.2` via `make tools-baml`.
4. Executes `bin/baml-cli-0.226.2 generate --from baml_src`.
5. Formats generated source files with `gofmt -w baml_client`.
6. Cleans and sorts import statements using `goimports -w baml_client`.

### BAML upgrade

Dependabot surfaces BAML updates in a dedicated first-match `baml` group evaluated before general version grouping rules, with a three-day default cooldown (`cooldown.default-days: 3`) across all ecosystems. The previous blanket ignore directive for `github.com/boundaryml/baml` is removed so upstream releases trigger automated pull requests. Until the coordinated update below is pushed to the same branch, a Dependabot BAML pull request fails every job that checks the pins: `make baml-check` in `Test with race detection`, `Check generated code` and `Test API docs in browser`, and the Dockerfile version check in `Build service and runtime image`.

When upgrading BAML dependencies or native CFFI binaries:
1. Update `baml_src/generators.baml`, `ARG BAML_LIB_VERSION` in [Dockerfile](../Dockerfile), and all four platform native library SHA256 checksums (`ARG BAML_LIB_SHA256_AMD64`, `ARG BAML_LIB_SHA256_ARM64`, `ARG BAML_LIB_SHA256_DARWIN_AMD64`, and `ARG BAML_LIB_SHA256_DARWIN_ARM64`) in [Dockerfile](../Dockerfile). Obtain new release assets directly from upstream releases and independently verify each SHA256 checksum.
2. Update the Go module dependency:
   ```sh
   go get github.com/boundaryml/baml@v<VERSION>
   ```
3. Regenerate client code and verify native library parity:
   ```sh
   make generate
   make native-baml
   ```
4. Execute tests and verify container builds:
   ```sh
   make test
   make docker-build
   ```
5. Execute classifier message-table tests, fake Bedrock adapter contract tests, and retry attempt tests using the verified native library:
   ```sh
   BAML_LIBRARY_PATH="$PWD/bin/libbaml_cffi.dylib" \
   BAML_LIBRARY_DISABLE_DOWNLOAD=true BAML_LOG=off \
   go test -race -count=1 -run 'Classif|Adapter|Attempts' ./internal/llm
   ```
   (Use `bin/libbaml_cffi.so` on Linux). BAML exposes runtime errors as string messages wrapped in `BamlError`. The unit tests in `internal/llm/classify_test.go`, contract tests in `internal/llm/adapter_contract_test.go`, and retry tests in `internal/llm/retry_contract_test.go` pin expected error message formats, status code extractions, context cancellation normalization, and SDK attempt counts. Executing these tests verifies that upstream message format and retry behavior changes are detected before merging.

### Swagger documentation

The OpenAPI/Swagger specification in `docs/` is generated from declarative annotations on handlers and types in `cmd/server/main.go` and `internal/`.

To regenerate the Swagger specification:

```sh
make swagger
```

This target runs:
1. `swag init -g cmd/server/main.go -o docs --parseInternal`
2. `gofmt -w docs/docs.go`

Committed files produced include `docs/docs.go`, `docs/swagger.json`, and `docs/swagger.yaml`.

## Test layers

### Unit tests

Unit tests execute in-process with race detection enabled:

```sh
make test
```

To run unit tests and report statement coverage:

```sh
make cover
```

Coverage areas include:
- `internal/config`: validation of environment variables, timeouts, port numbers, log levels, CIDR proxy allowlists, and origin URLs.
- `internal/graph`: in-memory graph construction, cycle-safe BFS traversal, topological sorting, prerequisite extraction, and JSON file parsing.
- `internal/llm`: BAML adapter translation, context cancellation propagation, and error wrapping.
- `internal/service`: reasoning pipeline orchestration, duplicate elimination, unknown topic rejection, VARK style validation, and circuit breaker state transitions.
- `internal/server` and `internal/api`: Fiber v3 routing, constant-time token verification, sliding-window rate limiting, request ID propagation, structured access logging, panic recovery, and error response formatting.

### Fake Bedrock contract tests

Contract tests in `internal/llm/adapter_contract_test.go` exercise the generated BAML client and native CFFI runtime against an in-process HTTP server provided by `test/fakebedrock`.

Run `make native-baml` to download and verify the host library, then execute contract tests with the verified native library environment:

```sh
BAML_LIBRARY_PATH="$PWD/bin/libbaml_cffi.dylib" \
BAML_LIBRARY_DISABLE_DOWNLOAD=true BAML_LOG=off \
go test -race -count=1 -run Adapter ./internal/llm
```

(Use `bin/libbaml_cffi.so` on Linux). The test setup isolates AWS configuration and credentials using empty temporary files (`AWS_CONFIG_FILE` and `AWS_SHARED_CREDENTIALS_FILE`), configures service-specific endpoints, and sets AWS environment variables with `t.Setenv`:
- `AWS_ENDPOINT_URL`: points to the local HTTP test server address.
- `AWS_REGION` and `AWS_DEFAULT_REGION`: `us-east-1`.
- `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY`: `dummy`.
- `AWS_SESSION_TOKEN` and `AWS_PROFILE`: empty strings.
- `AWS_EC2_METADATA_DISABLED`: `true` to disable metadata service lookups.
- `AWS_MAX_ATTEMPTS`: `3`.
- `AWS_RETRY_MODE`: `standard`.

Tests execute serially because the BAML client reads process environment variables on each invocation. The test cases verify:
- Item translation from Bedrock Converse responses into domain items.
- Empty item handling from upstream responses.
- Rejection of invalid response payloads on the initial attempt without retry loops.
- Context deadline cancellation within the deadline plus 250 ms, verifying the fake endpoint observes client disconnection.
- Upstream failure classification into typed sentinel errors (`ErrThrottled`, `ErrUpstreamRejected`, `ErrInvalidOutput`, `context.DeadlineExceeded`) across failure modes (`throttle`, `denied`, `garbage`, `hang`).
- Transport failure classification into `ErrUpstreamUnavailable` when the endpoint connection is closed.

### Browser end-to-end tests

Browser tests verify the Swagger UI interface using a real headless browser instance:

```sh
make test-e2e
```

The browser suite is located in `test/e2e/swagger_test.go` and guarded by the `//go:build e2e` build tag.

Execution flow:
1. Starts the HTTP server on a random local port (`127.0.0.1:0`) with an in-memory graph and a deterministic mock planner.
2. Launches headless Chrome or Chromium through Rod. If Chrome is in a non-standard location, set `ROD_BROWSER_BIN=/path/to/chrome`.
3. Navigates to `http://127.0.0.1:<port>/swagger/index.html`.
3a. Records Content-Security-Policy violations and asserts that the Google Fonts stylesheet is blocked (`style-src-elem`) and that the enforced policy contains `base-uri 'none'` and `form-action 'self'`.
4. Interacts with the Swagger UI DOM:
   - Expands `GET /health`, `GET /livez`, and `GET /readyz`, clicks **Try it out**, executes each request, and verifies HTTP 200 with `{"status":"ok"}`.
   - Expands `POST /api/v1/adaptive-plan`, executes without credentials, and verifies HTTP 401 Unauthorized.
   - Opens the **Authorize** modal, enters the configured service token for the `InternalToken` (`X-Internal-Token`) scheme, and applies authorization.
   - Re-executes `POST /api/v1/adaptive-plan` with topics, asserting HTTP 200 and verifying expected topics in the response payload.
   - Expands `GET /api/v1/tutor/student/{student_id}/cognitive-graph`, provides student ID and topic parameters, executes the request, and verifies HTTP 200 with node and edge data.

### Container smoke test

The container smoke test script `scripts/container-smoke.sh` exercises the runtime container image against a local stubbed Bedrock endpoint:

```sh
make docker-build container-smoke
```

To test an existing container image, pass the `IMAGE` variable:

```sh
make container-smoke IMAGE=axiom-ci
```

The smoke test performs the following steps:
1. Compiles the local fake Bedrock command `test/fakebedrock/cmd/fakebedrock`.
2. Starts a local HTTP stub in `ok` response mode on an ephemeral port, serving Amazon Bedrock Converse routes.
3. Launches the runtime container image with `--read-only`, `--tmpfs /tmp`, host-gateway routing (`--add-host host.docker.internal:host-gateway`), an ephemeral published port mapped to 8080, and configured dummy AWS credentials pointing `AWS_ENDPOINT_URL` to the local stub.
4. Waits for the container health status to report `healthy`.
5. Confirms that the configured user is `65532:65532`, that every running container process (including any overlapping healthcheck probe) has UID 65532, requiring at least one process, that the root filesystem is read-only, and that `/tmp` is a tmpfs mount.
6. Sends `GET /health` and asserts HTTP 200.
7. Sends an unauthenticated `POST /api/v1/adaptive-plan` and asserts HTTP 401 Unauthorized.
8. Sends an authenticated `POST /api/v1/adaptive-plan` request with valid learner input and asserts HTTP 200 with generated plan items.
9. Stops the container with `docker stop --time 30`, verifies a clean exit code 0, and asserts that container logs are exclusively valid JSON objects containing no sensitive learner identifiers (`student-42`) and no raw native `[BAML` records.
10. Starts the stub in `throttle` mode, launches a fresh container, and sends an authenticated plan request to assert HTTP 503 `service_unavailable` with header `Retry-After: 5`.
11. Stops the container and verifies clean shutdown and structured JSON log privacy.

All temporary files, fixture processes, and test containers are cleaned up on completion or failure.

Every failed check stops the script with `container smoke: <reason>`. `make container-smoke-test IMAGE=<image>` builds two derived images, one running as root and one killed on stop, and checks that the smoke test rejects them with `container smoke: configured user is 0:0` and `container smoke: stop exit code is 137`. The derived images are removed afterwards. Both scripts need Go, Docker with `host-gateway` support, curl and Python 3.

## Static analysis and checks

### Lint

Linting checks code quality and formatting against rules defined in [.golangci.yml](../.golangci.yml):

```sh
make lint
```

The `make lint` target executes `go vet ./...` and `go vet -tags e2e ./...`, runs `golangci-lint`, and validates GitHub Actions workflow syntax, expressions, and shell steps with the tools-module-pinned `actionlint` (`go tool -modfile=tools/go.mod actionlint`). Invalid workflow expressions or syntax errors fail the existing `Lint` required check. Enabled linters include `govet`, `errcheck`, `staticcheck`, `unused`, `ineffassign`, `bodyclose`, `noctx`, `gosec`, `revive`, `gocritic`, `misspell`, `errorlint`, `contextcheck`, `nilerr`, `prealloc`, `copyloopvar`, and `usestdlibvars`. `make lint` stops early when `shellcheck` is not on PATH.

To automatically format source files:

```sh
make fmt
```

### Vulnerability scan

Reachable vulnerabilities in dependencies are checked with `govulncheck`:

```sh
make vuln
```

### Cleanliness and generation verification

CI verifies that committed generated files and module dependencies are completely up to date:

```sh
make tidy-check
make generate-check
```

`tidy-check` runs `go mod tidy` and `go -C tools mod tidy`, asserting no changes to `go.mod`, `go.sum`, `tools/go.mod`, or `tools/go.sum`. `generate-check` regenerates BAML and Swagger artifacts and asserts no modified or untracked files remain.

## Continuous integration jobs

GitHub Actions workflows are defined in `.github/workflows/`.

### `ci.yml`

Runs on push to `main` and on pull requests:
- **`lint`**: runs `make lint`.
- **`test`**: runs `make cover` with data race detection and uploads `coverage.out` as a workflow artifact.
- **`generated`**: runs `make tools-baml generate-check tidy-check` to enforce that generated code matches definitions.
- **`vulnerabilities`**: runs `make vuln` to verify zero reachable vulnerability findings.
- **`image`**: builds the Docker runtime container `axiom-ci` and executes `make container-smoke container-smoke-test IMAGE=axiom-ci` to verify health probes, authentication, end-to-end plan generation, upstream throttling handling, log privacy, and graceful termination, emitting container logs on failure.
- **`browser`**: verifies or installs Google Chrome stable, sets `ROD_BROWSER_BIN`, and runs `make test-e2e`.

### `codeql.yml`

Runs on push to `main`, pull requests, and a weekly schedule (`31 7 * * 1`):
- **`analyze`**: sets up CodeQL for Go (action v4.39.1), builds the binary via `make build`, and executes static security analysis.

### `dependency-review.yml`

Runs on pull requests:
- Scans proposed dependency changes for known security advisories using `actions/dependency-review-action@v5.0.0`.

### `pr-title.yml`

Runs on pull request lifecycle events:
- Validates that pull request titles adhere to Conventional Commits format using `amannn/action-semantic-pull-request@v6.1.1`.
- Allowed types (`build`, `ci`, `docs`, `feat`, `fix`, `perf`, `refactor`, `test`, `chore`, `revert`) and the prohibition on uppercase-leading subjects apply to every PR. The 72-character PR-title limit applies to every author except `dependabot[bot]` of type `Bot`; only the separate length step skips for that author. The required `Validate PR title` job continues to execute semantic validation for all PRs.

This workflow uses `pull_request_target`, so GitHub reads its definition from main. After changing the title policy, refresh existing Dependabot PRs with `@dependabot rebase` and validate a new lifecycle event. When squash-merging a Dependabot PR, supply a Conventional Commit title of at most 72 characters and a body without trailers.

### `release.yml`

Runs on a pushed `v*` tag and on manual dispatch (`workflow_dispatch`, input `dry_run`, default `true`):
- **`verify`**: runs `make lint test docker-build container-smoke IMAGE=axiom-release:verify`; publishing runs also require a `v*` tag whose commit is on `main`.
- **`dry-run`** (when not publishing): explicitly installs Docker CE `v29.9.2`, builds the runtime image natively on `ubuntu-latest` (amd64) and `ubuntu-24.04-arm` (arm64) into a containerd image store, runs `make container-smoke` against it, and exports an OCI archive with SBOM and provenance. Nothing is pushed.
- **`build`** (publishing): explicitly installs Docker CE `v29.9.2`, builds and loads each native image, runs `make container-smoke`, logs in, pushes it as `ghcr.io/victoroide/axiom-engine:<commit SHA>-<arch>`, checks that the pushed digest equals the built one, and uploads the digest as an artifact.
- **`merge`**, **`attest`**, **`sign`**, **`release`**: combine both digests into one index tagged `<tag>`, `<version>`, `<major>.<minor>` and `latest`, attest and keylessly sign that index digest, then create the GitHub release. Publishing jobs run in the `release` environment.

### `security-scan.yml`

Triggers daily on a schedule (`17 6 * * *` / 06:17 UTC) and on manual dispatch (`workflow_dispatch`) with top-level `contents: read` permissions. Each scanning job requests `security-events: write` to upload SARIF results:
- **`dependencies`**: sets up Go and runs `make vuln VULN_FLAGS="-show verbose"`, which lists module advisories and fails on reachable vulnerabilities. It then generates SARIF with `make --silent vuln VULN_FLAGS="-format sarif"` (this step always exits 0) and uploads it under the `go-dependencies` category, also when the text scan fails.
- **`tools`**: sets up Go from `tools/go.mod` and runs `go -C tools tool govulncheck -scan package -show verbose tool`, which fails when a pinned tool imports a vulnerable package and lists module advisories. It then uploads SARIF under the `go-tools` category with findings attributed to `tools/go.mod`, also after a failure.
- **`image`**: builds the `runtime` container target, scans it for fixable high and critical vulnerabilities with Anchore Grype (`anchore/scan-action`), and uploads SARIF under the `runtime-image` category even when the severity threshold fails.
