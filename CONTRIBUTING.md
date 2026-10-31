# Contributing

## Prerequisites

- Go 1.27.3, matching `go.mod`.
- A C compiler toolchain (such as `gcc` or `clang`) with CGO enabled. CGO is required for BAML native CFFI bindings.
- Docker for container builds and local verification.
- Google Chrome or Chromium for browser end-to-end tests.
- Pinned toolchain and tooling dependencies specified in the [development guide](docs/development.md#tooling).

## Development tools

Developer and CI tools (`govulncheck`, `swag`, `goimports`, and `actionlint`) are tracked in `tools/go.mod` with checksums in `tools/go.sum`. For tool invocation commands, version updates, and maintenance instructions, see the [tooling guide](docs/development.md#tooling). Dependabot checks the `/tools` module weekly, including indirect requirements (`allow: dependency-type: all`, because Go records every tool module as `// indirect`), with a three-day default cooldown.

Read [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) before participating. Report security vulnerabilities privately as described in [SECURITY.md](SECURITY.md), never in a public issue or pull request.

## Local setup

Clone the repository and install tool dependencies:

```sh
git clone https://github.com/Victoroide/axiom-engine.git
cd axiom-engine
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
- **Length**: Commit subject lines and human-authored PR titles must not exceed 72 characters. Dependabot-authored PR titles may exceed this limit because their generated titles include dependency metadata; they must still pass the same Conventional Commit type and subject validation. When squash-merging a Dependabot PR, supply a conventional commit title of at most 72 characters and a body without trailers.
- Do not add trailers of any kind (such as `Signed-off-by` or `Co-authored-by`).

### Review expectations

- Submit pull requests against the `main` branch.
- Complete the pull request template: summarize the change, state why it is needed, describe how it was implemented, and list the local checks executed.
- Ensure all automated CI workflows pass.
- Include unit tests for new functionality and regression tests for bug fixes.
- Update relevant documentation and [CHANGELOG.md](CHANGELOG.md) when user-visible behavior changes.
- Address maintainer review feedback from Victoroide before merging.

## Releasing

Repository releases are automated through `.github/workflows/release.yml`.

### Release workflow

1. **Dry-run verification**: Before tagging a release, execute a manual dry run. Trigger the `Release` workflow via `workflow_dispatch` with `dry_run: true` (the default). Verify that the `Verify release` job and both native platform builds, including their container smoke tests, succeed without publishing images or creating a GitHub release.
2. **Tag creation**: Create and push an annotated Git tag with a `v*` prefix (for example, `v0.1.0`) pointing to a commit on `main`. The release workflow verifies that the tag commit is an ancestor of `origin/main` (`git merge-base --is-ancestor "$GITHUB_SHA" origin/main`).
3. **Publication**: Tag pushes automatically initiate the release pipeline. Manual publication via `workflow_dispatch` with `dry_run: false` is also supported, but requires a `v*` tag whose commit is an ancestor of `main`; manual publishing from an unmerged branch is rejected.

### Protected settings

Publishing jobs target the protected `release` GitHub environment. Configure repository settings before publishing:
- Configure the `release` environment deployment policy to allow only selected tags matching `v*`.
- Configure repository tag protection rulesets to restrict tag creation to authorized administrators and maintainers.
