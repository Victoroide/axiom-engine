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

Dependabot checks the `/tools` module weekly with a three-day cooldown. Its allowlist selects the modules providing actionlint, swag, goimports, and govulncheck, including their indirect Go requirements. Transitive libraries are resolved with tool updates instead of receiving independent version-update PRs. Keep the allowlist synchronized with the `tool` directives when adding or removing a tool.

| Tool package | Dependabot module name |
| --- | --- |
| `github.com/rhysd/actionlint/cmd/actionlint` | `github.com/rhysd/actionlint` |
| `github.com/swaggo/swag/cmd/swag` | `github.com/swaggo/swag` |
| `golang.org/x/tools/cmd/goimports` | `golang.org/x/tools` |
| `golang.org/x/vuln/cmd/govulncheck` | `golang.org/x/vuln` |

Run pinned tools from the repository root using `go tool`:

```sh
go tool -modfile=tools/go.mod <tool> [args...]
```

To update a pinned tool version:

```sh
go -C tools get -tool <package>@<version>
go -C tools mod tidy
```

Go marks the four provider requirements as indirect, so each named allow rule retains `dependency-type: all`. Check the resolved dependency graph, update the documented tool version, run `go -C tools tool govulncheck -scan package -show verbose tool`, `make tidy-check`, `make generate-check` and `make lint`, and commit any justified generated output with that tool upgrade. If an affected transitive dependency cannot be fixed through a parent-tool release, document an explicit override and inspect it before committing; do not suppress alerts with permanent ignore comments.

`tools/go.mod` explicitly requires `golang.org/x/net v0.62.0` to fix GO-2026-6860 and GO-2026-6866. Swag reaches this module through `go-openapi/spec`, `go-openapi/jsonreference`, and `purell`. The tools Dependabot allowlist selects only the four tool-provider modules, so this transitive security update needs an explicit requirement. After updating the parent tools, check the selected version with `go -C tools list -m golang.org/x/net` and repeat the tools vulnerability scan before changing the override.

`golangci-lint` (`v2.15.0`) intentionally remains executed via `go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0` in accordance with upstream guidance. `baml-cli` is installed into `bin/` via `make tools-baml` at the version declared in `baml_src/generators.baml`. CI caches modules by both `go.sum` and `tools/go.sum`.

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
| `fuzz` | Fuzz parsers and output policy across five sequential targets without the native library (`FUZZTIME` defaults to 30s). |
| `lint` | Vet build variants and check Go code and workflow files. |
| `vuln` | Check reachable Go vulnerabilities. |
| `fmt` | Format maintained Go source files. |
| `tidy-check` | Fail if module metadata is not tidy. |
| `toolchain-check` | Check Go versions in modules, the image and documentation. |
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
2. Runs `make baml-check` to verify that `baml_src/generators.baml`, `ARG BAML_LIB_VERSION` in [Dockerfile](../Dockerfile), and `github.com/boundaryml/baml` in [go.mod](../go.mod) all specify `<version>` from `baml_src/generators.baml`.
3. Compiles `baml-cli` into `bin/baml-cli-<version>` via `make tools-baml`.
4. Executes `bin/baml-cli-<version> generate --from baml_src`.
5. Formats generated source files with `gofmt -w baml_client`.
6. Cleans and sorts import statements using `goimports -w baml_client`.

### BAML upgrade

Dependabot surfaces BAML updates in a dedicated first-match `baml` group evaluated before general version grouping rules, with a three-day default cooldown (`cooldown.default-days: 3`) across all ecosystems. The previous blanket ignore directive for `github.com/boundaryml/baml` is removed so upstream releases trigger automated pull requests. Dependabot updates only `go.mod` and `go.sum`, so its pull request fails every job that checks the pins: `make baml-check` in `Test with race detection`, `Check generated code` and `Test API docs in browser`, and the Dockerfile version check in `Build service and runtime image`. Do not push to the Dependabot branch. Open a separate pull request with the coordinated update below and state `Supersedes #<number>` in its description. Leave the Dependabot pull request open until the replacement merges; Dependabot closes it on its next run. Close it by hand, linking the merged pull request, only if it is still open after the next weekly run.

When upgrading BAML dependencies or native CFFI binaries:
1. Update `baml_src/generators.baml`, `ARG BAML_LIB_VERSION` in [Dockerfile](../Dockerfile), and all four platform native library SHA256 checksums (`ARG BAML_LIB_SHA256_AMD64`, `ARG BAML_LIB_SHA256_ARM64`, `ARG BAML_LIB_SHA256_DARWIN_AMD64`, and `ARG BAML_LIB_SHA256_DARWIN_ARM64`) in [Dockerfile](../Dockerfile). Download all four native assets for the chosen release, run `shasum -a 256` on each, and cross-check each with:
   ```sh
   gh api repos/BoundaryML/baml/releases/tags/<version> --jq '.assets[] | select(.name == "<asset-name>") | .digest'
   ```
   Strip the `sha256:` prefix when comparing with Dockerfile ARGs. GitHub digests check upload/transport integrity, not publisher integrity; native CFFI assets have no published checksum sidecars or signatures.
2. Update the Go module dependency:
   ```sh
   go get github.com/boundaryml/baml@v<version>
   go mod tidy
   ```
   Run `go mod tidy` immediately after `go get github.com/boundaryml/baml@v<version>`; otherwise stale `go.sum` entries fail `make tidy-check`.
3. Clear cached native binaries, verify native library parity, and regenerate client code:
   ```sh
   rm -f bin/libbaml_cffi.*
   make native-baml
   make baml-check
   make generate
   ```
   Run `rm -f bin/libbaml_cffi.*` before `make native-baml`; an already cached library from the previous version fails checksum validation rather than being replaced automatically.
4. Execute tests and verify container builds:
   ```sh
   make test
   make docker-build container-smoke
   ```
5. Execute classifier message-table tests, fake Bedrock adapter contract tests, and retry attempt tests using the verified native library:
   ```sh
   BAML_LIBRARY_PATH="$PWD/bin/libbaml_cffi.dylib" \
   BAML_LIBRARY_DISABLE_DOWNLOAD=true BAML_LOG=off \
   go test -race -count=1 -run 'Classif|Adapter|Attempts' ./internal/llm
   ```
   (Use `bin/libbaml_cffi.so` on Linux). BAML Go exposes runtime errors as string messages wrapped in `BamlError`. The unit tests in `internal/llm/classify_test.go`, contract tests in `internal/llm/adapter_contract_test.go`, and retry tests in `internal/llm/retry_contract_test.go` pin expected error message formats, status code extractions, context cancellation normalization, and SDK attempt counts. Executing these tests verifies that upstream message format and retry behavior changes are detected before merging.

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

### Updating Go

When upgrading the Go toolchain version, update all seven declaration sites in one pull request:
1. Root module: `go` directive in [go.mod](../go.mod)
2. Tooling module: `go` directive in [tools/go.mod](../tools/go.mod)
3. Builder image tag: `golang` base image in [Dockerfile](../Dockerfile)
4. Go version badge in [README.md](../README.md)
5. Prerequisites section in [README.md](../README.md)
6. Prerequisites section in [CONTRIBUTING.md](../CONTRIBUTING.md)
7. Prerequisites section in [docs/development.md](development.md)

Run `make toolchain-check` to verify that all seven sites declare matching versions:

```sh
make toolchain-check
```

Dependabot updates to the Dockerfile builder image that bump the `golang` tag fail this check until the remaining sites are updated in the same change. Digest-only base image updates do not change the Go version tag and pass without modifying other files.

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

#### Goroutine leak detection

Unit test suites for `internal/service`, `internal/server`, and `internal/llm` enforce goroutine leak detection using `go.uber.org/goleak v1.3.0` inside `TestMain`, ensuring no unexpected background goroutines remain active after package test execution.

- In `internal/service`, `goleak.VerifyTestMain(m)` executes with no ignore options.
- In `internal/server` and `internal/llm`, `goleak.VerifyTestMain` configures four framework ignores to accommodate long-lived background goroutines from Fiber and fasthttp:
  - `goleak.IgnoreTopFunction("github.com/gofiber/fiber/v3/internal/memory.(*Storage).gc")`
  - `goleak.IgnoreTopFunction("github.com/gofiber/utils/v2.StartTimeStampUpdater.func1")`
  - `goleak.IgnoreAnyFunction("github.com/valyala/fasthttp.updateServerDate.func1")`
  - `goleak.IgnoreAnyFunction("github.com/valyala/fasthttp.(*workerPool).Start.func2")`

Fiber's in-memory storage collector and timestamp updater, along with fasthttp's date updater, maintain persistent process-wide background loops. In addition, `fasthttp.(*workerPool)` goroutines can sleep until `MaxIdleWorkerDuration`. These ignores cover these known framework goroutines while retaining checks for application leaks.

#### Synthetic time with synctest

Six concurrency and timing tests in `internal/service/reasoning_test.go` execute inside `testing/synctest` bubbles using `synctest.Test`:
- `TestGlobalConcurrencyBound`
- `TestQueueTimeoutReturnsUnavailable`
- `TestCanceledQueueWaitPreservesCancellation`
- `TestPlannerIgnoringContextStillTimesOut`
- `TestDeadlineAndUpstreamErrors`
- `TestOrderingDedupeAndHallucinationGuard`

Virtual time eliminates real wall-clock delays and timing jitter. `TestOrderingDedupeAndHallucinationGuard` executes without waiting on random jitter delays, and `TestDeadlineAndUpstreamErrors` asserts exactly 1 ms of virtual elapsed time without real clock waits.

Server, lifecycle, and real transport tests retain wall-clock execution: constructing a Fiber application inside a `synctest` bubble leaves process-wide framework loops blocked and causes bubble-exit deadlock.

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

### Swagger contract tests

Swagger response contract tests in `test/contract/contract_test.go` validate that runtime HTTP responses conform to the committed OpenAPI specification.

Execute the contract suite directly:

```sh
go test -race -count=1 ./test/contract
```

The suite is also included in `make test` and standard CI test runs via `./...`; no separate test harness or CI workflow is required.

The contract runner loads `docs/swagger.json`, converts Swagger 2.0 to OpenAPI 3.0 via `kin-openapi` (`openapi2conv.ToV3`), routes operations with `routers/legacy`, and executes response validation via `openapi3filter.ValidateResponse` with `IncludeResponseStatus: true`. In addition to schema validation, the suite validates response headers, failing if `X-Request-ID`, `Retry-After`, or any `X-RateLimit-*` header appears on an endpoint response where it is not declared in the specification.

Coverage includes:
- Probes: HTTP 200, 400 for malformed requests, 408 on the read deadline, 413 for oversized bodies, and 431 for oversized headers on `/health`, `/livez`, and `/readyz`.
- Adaptive plan (`/api/v1/adaptive-plan`): HTTP 200; 400 for malformed JSON and schema validation errors; 401, 408, 413, 415, 422, 429, 431; 500 route-handler panic recovery; 502 when upstream returns no usable items; 503 for upstream throttling, open circuit breaker, and queue saturation; and 504 on deadline expiration.
- Cognitive graph (`/api/v1/tutor/student/:student_id/cognitive-graph`): HTTP 200; 400 for invalid student identifiers and target topics outside the resolved topic set; 401, 408, 413, 422, 429, 431, and 500.
- Router path errors: HTTP 404 (unmatched routes), 405 (unsupported methods on registered routes), and 501 (methods outside router configuration, such as `PROPFIND`), asserting the standard `domain.ErrorResponse` envelope.

Transport execution:
- Standard scenarios execute in-process using Fiber's `app.Test`.
- Parser-level HTTP 400 (`bad_request`), 408 (`request_timeout`), 413 (`payload_too_large`), and 431 (`request_header_fields_too_large`) tests run against ephemeral TCP listeners because Fiber masks low-level request parsing errors in `app.Test`. Every operation is tested with an oversized body and headers and with the configured read timeout lowered to 200 ms. Slow-client cases send incomplete requests through a raw TCP connection and read the response with `http.ReadResponse`; malformed requests exercise all three probes. Listeners bind `127.0.0.1:0` and shut down explicitly, with no fixed ports or sleeps.
- HTTP 500 test cases verify panic recovery in route handlers; planner panics originating in detached child goroutines are not contained.

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

CI requires runner-provided `google-chrome`, prints its version, and exports its path through `ROD_BROWSER_BIN`; missing Chrome emits a workflow error and fails without downloading a browser. Local browser discovery remains unchanged.

Optional `E2E_ARTIFACTS_DIR` has no default: when unset no files are saved; when set, a failed browser test saves `page.png` and `page.html` there before closing the page and browser. The directory is created with 0750 permissions and files with 0600.

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

### Fuzz and property tests

Go native fuzz targets in `internal/graph`, `internal/config`, `internal/api`, and `internal/service` test parser robustness, canonicalization, and output policy constraints against random and mutated inputs without requiring the native BAML library.

Run all five fuzz targets sequentially:

```sh
make fuzz
```

The `FUZZTIME` variable sets the fuzzing duration per target (default: `30s`):

```sh
make fuzz FUZZTIME=1m
```

Individual targets can be executed using `go test`:

```sh
go test ./internal/graph -run='^$' -fuzz='^FuzzDecode$' -fuzztime=30s
```

Fuzz targets include:
- `FuzzDecode` (`internal/graph/loader_fuzz_test.go`): decodes JSON graph definitions seeded with `seed.json`, invalid node names, cyclic graphs, and canonical collisions. Asserts that any accepted graph satisfies node naming constraints (at most 128 Unicode runes, no commas, no `Cc` or `Cf` characters, and no U+FFFD), canonical keys are unique, prerequisite graphs are acyclic, and prerequisite chains contain no self-references or duplicates.
- `FuzzCanonical` (`internal/graph/loader_fuzz_test.go`): verifies that name canonicalization is idempotent (`Canonical(Canonical(s)) == Canonical(s)`).
- `FuzzLoad` (`internal/config/config_fuzz_test.go`): mutates configuration keys and values through an environment lookup provider. Asserts that accepted configurations preserve timeout invariants (`0 < LLMQueueTimeout < LLMTimeout <= RequestTimeout`, `ShutdownTimeout >= 11s`, and `WriteTimeout == RequestTimeout + 10s`).
- `FuzzStrictJSON` (`internal/api/decode_fuzz_test.go`): tests strict JSON decoding against arbitrary request bodies. Asserts that decoding never panics, error messages remain bounded within 1024 bytes, and accepted request payloads round-trip through JSON marshaling and decoding unchanged.
- `FuzzOutputPolicy` (`internal/service/output_fuzz_test.go`): tests model output sanitation across learner VARK profiles. Asserts that retained resources have valid absolute HTTP or HTTPS URLs of at most 2048 runes with no userinfo, titles within 200 runes, explanations within 1000 runes, no control or format characters, resource types permitted for the learner profile, and at most three resources per item.

Seed corpus files in package `testdata/fuzz/` directories run as standard unit tests during `go test`. When a crasher is discovered, its reproduction corpus entry is preserved under the matching package `testdata/fuzz/<TargetName>/` directory alongside the fix.

#### Property tests

Property-based tests using `pgregory.net/rapid v1.3.0` generate randomized inputs to verify invariants across graph traversals and reasoning pipeline transformations. Each property test runs 100 iterations by default on every `go test` invocation:
- `TestPrerequisiteOrderProperty` (`internal/graph/memory_property_test.go`): generates randomized directed acyclic graphs and asserts that prerequisite chains returned by `GetPrerequisiteChain` follow a strict topological order (deepest prerequisites first) with no duplicates.
- `TestLocalSubgraphDistanceProperty` (`internal/graph/memory_property_test.go`): verifies `GetLocalSubgraph` against an independent undirected shortest-path matrix. Breadth-first traversal at depth $d$ includes all edges incident to nodes within distance $d$ of any seed, meaning an edge's opposite endpoint may reach distance $d+1$ (including at depth 0).
- `TestMergeDeduplicateProperty` (`internal/service/pipeline_property_test.go`): asserts that plan deduplication in `mergeAndDeduplicate` is idempotent and preserves the first occurrence of each canonical topic across candidate plans.
- `TestValidateGraphOrderProperty` (`internal/service/pipeline_property_test.go`): generates linear prerequisite chains and asserts that `validateAgainstGraph` retains only items within the requested topic prerequisite closure, orders retained items strictly by prerequisite rank, assigns sequential priorities starting at 1, and regenerates complete prerequisite chains.

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
make toolchain-check
```

`tidy-check` runs `go mod tidy` and `go -C tools mod tidy`, asserting no changes to `go.mod`, `go.sum`, `tools/go.mod`, or `tools/go.sum`. `generate-check` regenerates BAML and Swagger artifacts and asserts no modified or untracked files remain. `toolchain-check` prints seven Go version sites and fails if any is absent or differs from the root `go.mod` directive.

## Continuous integration jobs

GitHub Actions workflows are defined in `.github/workflows/`.

### `ci.yml`

Runs on push to `main` and on pull requests:
- **`lint`**: runs `make lint`.
- **`test`**: runs `make cover` with data race detection and uploads `coverage.out` as a workflow artifact.
- **`generated`**: runs `make tools-baml generate-check tidy-check toolchain-check` to enforce that generated code matches definitions and verify Go version consistency across modules, the container image, and documentation.
- **`vulnerabilities`**: runs `make vuln` to verify zero reachable vulnerability findings.
- **`image`**: builds and loads runtime image `axiom-ci` through pinned `docker/setup-buildx-action` and `docker/build-push-action`, then executes `make container-smoke container-smoke-test IMAGE=axiom-ci`. Layer caching uses GitHub Actions cache with scope `ci-runtime`: pull requests read the cache, while pushes to `main` export cache layers with `mode=max`. Provenance and SBOM generation are disabled for this temporary image. Root Markdown files, `scripts`, and `tools` are excluded from the Docker build context in `.dockerignore`, enabling layer reuse for changes outside the image context. Code changes still compile cold because BuildKit cache mounts are not exported to GitHub Actions cache. Native library downloads remain uncached as their measured preparation overhead is 1 s.
- **`browser`**: verifies preinstalled `google-chrome`, exports its path through `ROD_BROWSER_BIN`, and runs `make test-e2e` with `E2E_ARTIFACTS_DIR=${{ runner.temp }}/browser-artifacts`. On test failure, uploads `browser-failure` from `${{ runner.temp }}/browser-artifacts`, warns if no files exist, and retains artifacts for 14 days. This job backs the required check named "Test API docs in browser".

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

### `scorecard.yml`

Runs on push to `main`, weekly schedule (`43 7 * * 1` / Monday 07:43 UTC), and manual dispatch (`workflow_dispatch`):
- **`scorecard`**: runs `ossf/scorecard-action` (pinned to `fa7117db83c52d787ae5a89f350999701505d78e` / v2.5.0) on `ubuntu-latest` with `persist-credentials: false` to analyze repository security posture. Publishes results publicly and uploads `scorecard.sarif` under code-scanning category `scorecard` via `github/codeql-action/upload-sarif`. Workflow-level permission is `contents: read`; the job requires `contents: read`, `security-events: write`, and `id-token: write`. This workflow does not trigger on pull requests and is not a required check. Classic branch-protection triggers are omitted because the repository uses rulesets. While `workflow_dispatch` is available for initial manual checks (experimental upstream), push to `main` and weekly schedule are the publishing paths.

### `security-scan.yml`

Triggers daily on a schedule (`17 6 * * *` / 06:17 UTC) and on manual dispatch (`workflow_dispatch`) with top-level `contents: read` permissions. Each scanning job requests `security-events: write` to upload SARIF results:
- **`dependencies`**: sets up Go and runs `make vuln VULN_FLAGS="-show verbose"`, which lists module advisories and fails on reachable vulnerabilities. It then generates SARIF with `make --silent vuln VULN_FLAGS="-format sarif"` (this step always exits 0) and uploads it under the `go-dependencies` category, also when the text scan fails.
- **`tools`**: sets up Go from `tools/go.mod` and runs `go -C tools tool govulncheck -scan package -show verbose tool`, which fails when a pinned tool imports a vulnerable package and lists module advisories. It then uploads SARIF under the `go-tools` category with findings attributed to `tools/go.mod`, also after a failure.
- **`image`**: builds the `runtime` container target, scans it for fixable high and critical vulnerabilities with Anchore Grype (`anchore/scan-action`), and uploads SARIF under the `runtime-image` category even when the severity threshold fails.
