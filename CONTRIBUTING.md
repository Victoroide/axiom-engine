# Contributing

## Prerequisites

- Go 1.27.3, matching `go.mod`.
- A C compiler toolchain (such as `gcc` or `clang`) with CGO enabled. CGO is required for BAML native CFFI bindings.
- Docker for container builds and local verification.
- Google Chrome or Chromium for browser end-to-end tests.
- Pinned toolchain and tooling dependencies specified in the [development guide](docs/development.md#pinned-tool-versions).

Read [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) before participating. Report security vulnerabilities privately as described in [SECURITY.md](SECURITY.md), never in a public issue or pull request.

## Local setup

Clone the repository and install tool dependencies:

```sh
git clone https://github.com/Victoroide/axiom-reasoning-svc.git
cd axiom-reasoning-svc
go mod download
make tools-baml
```

Optionally copy `.env.example` to `.env`:

```sh
cp .env.example .env
```

The service binary reads process environment variables directly; it does not load `.env` files automatically. When using Docker Compose (`docker compose up`), Compose reads `.env` if present.

Run the service locally:

```sh
export PORT=8080
export AXIOM_INTERNAL_TOKEN="$(openssl rand -hex 32)"
export AWS_REGION=us-east-1
make run
```

For detailed architecture, configuration, and test information, see the [development guide](docs/development.md).

## Checks

Run these commands from the repository root before submitting a pull request:

```sh
make baml-check
make lint
make vuln
make test
make test-e2e
make generate-check
make tidy-check
make docker-build
```

Ensure that:
- `make baml-check` passes, confirming BAML generator, module, and Docker pins match.
- `make lint` reports zero issues.
- `make vuln` finds zero reachable vulnerabilities.
- `make test` runs with race detection and passes.
- `make test-e2e` completes browser end-to-end verification.
- `make generate-check` and `make tidy-check` report a clean git working tree without modified or untracked generated files.
- `make docker-build` builds the non-root container image successfully.

## Commits and pull requests

### Workflow

1. Fork the repository and create a branch from `main`.
2. Keep each branch and pull request focused on a single change.
3. Write clear, atomic commits.

### Commit message format

Commit subjects and pull request titles must follow the Conventional Commits specification:

```text
<type>(<scope>): <subject>
```

- **Types**: `build`, `ci`, `docs`, `feat`, `fix`, `perf`, `refactor`, `test`, `chore`, `revert`.
- **Scope**: Lowercase component or package name, for example `api`, `config`, `graph`, `llm`, `server`, `service`, `docker`, `make`, `ci`, or `repo`.
- **Subject**: Use imperative mood (for example, `reject invalid topic names`). Do not start with an uppercase letter. Do not append a trailing period.
- **Length**: The entire subject line must not exceed 72 characters. CI validates PR title format and length via `.github/workflows/pr-title.yml`.
- Do not add trailers of any kind (such as `Signed-off-by` or `Co-authored-by`).

### Review expectations

- Submit pull requests against the `main` branch.
- Complete the pull request template: summarize the change, state why it is needed, describe how it was implemented, and list the local checks executed.
- Ensure all automated CI workflows pass.
- Include unit tests for new functionality and regression tests for bug fixes.
- Update relevant documentation and [CHANGELOG.md](CHANGELOG.md) when user-visible behavior changes.
- Address maintainer review feedback from Victoroide before merging.
