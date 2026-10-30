.DEFAULT_GOAL := help

GOLANGCI_LINT_VERSION := v2.15.0
BAML_VERSION := $(shell sed -n 's/^[[:space:]]*version[[:space:]]*"\([^"]*\)".*/\1/p' baml_src/generators.baml)
BIN_DIR := $(CURDIR)/bin
BAML_CLI := $(BIN_DIR)/baml-cli-$(BAML_VERSION)
BAML_LIB_VERSION := $(shell sed -n 's/^ARG BAML_LIB_VERSION=//p' Dockerfile)
BAML_HOST_OS := $(shell go env GOHOSTOS)
BAML_HOST_ARCH := $(shell go env GOHOSTARCH)
BAML_LIB_EXT := $(if $(filter darwin,$(BAML_HOST_OS)),dylib,so)
export BAML_LIBRARY_PATH := $(BIN_DIR)/libbaml_cffi.$(BAML_LIB_EXT)
export BAML_LIBRARY_DISABLE_DOWNLOAD := true
export BAML_LOG ?= off
IMAGE ?= axiom-engine:local
VULN_FLAGS ?=

.PHONY: help generate swagger build run test cover test-e2e lint vuln fmt tidy-check generate-check docker-build container-smoke container-smoke-test clean tools-baml baml-check native-baml

help: ## List available targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z0-9_-]+:.*## / {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

$(BAML_CLI):
	@test -n "$(BAML_VERSION)"
	@mkdir -p "$(BIN_DIR)"
	GOBIN="$(BIN_DIR)" go install github.com/boundaryml/baml/baml-cli@v$(BAML_VERSION)
	mv "$(BIN_DIR)/baml-cli" "$@"

tools-baml: $(BAML_CLI) ## Install the generator version declared in generators.baml

baml-check: ## Check generator, module and native-library pins agree
	@test "$(BAML_VERSION)" = "$(BAML_LIB_VERSION)"
	@test "$$(go list -m -f '{{.Version}}' github.com/boundaryml/baml)" = "v$(BAML_VERSION)"

native-baml: baml-check ## Download and verify the pinned native library for this host
	@set -eu; \
	case "$(BAML_HOST_OS)/$(BAML_HOST_ARCH)" in \
	  linux/amd64) asset=libbaml_cffi-x86_64-unknown-linux-gnu.so; pin=AMD64 ;; \
	  linux/arm64) asset=libbaml_cffi-aarch64-unknown-linux-gnu.so; pin=ARM64 ;; \
	  darwin/amd64) asset=libbaml_cffi-x86_64-apple-darwin.dylib; pin=DARWIN_AMD64 ;; \
	  darwin/arm64) asset=libbaml_cffi-aarch64-apple-darwin.dylib; pin=DARWIN_ARM64 ;; \
	  *) echo "Unsupported BAML host: $(BAML_HOST_OS)/$(BAML_HOST_ARCH)" >&2; exit 1 ;; \
	esac; \
	checksum="$$(sed -n "s/^ARG BAML_LIB_SHA256_$${pin}=//p" Dockerfile)"; \
	test "$${#checksum}" = 64; \
	mkdir -p "$(BIN_DIR)"; \
	if test ! -f "$(BAML_LIBRARY_PATH)"; then \
	  tmp=$$(mktemp "$(BIN_DIR)/.baml-native.XXXXXX"); \
	  trap 'rm -f "$$tmp"' EXIT HUP INT TERM; \
	  curl --fail --silent --show-error --location --retry 3 --connect-timeout 10 \
	    --speed-limit 1024 --speed-time 30 \
	    "https://github.com/boundaryml/baml/releases/download/$(BAML_LIB_VERSION)/$$asset" \
	    --output "$$tmp"; \
	  printf '%s  %s\n' "$$checksum" "$$tmp" | shasum -a 256 -c -; \
	  mv "$$tmp" "$(BAML_LIBRARY_PATH)"; \
	fi; \
	printf '%s  %s\n' "$$checksum" "$(BAML_LIBRARY_PATH)" | shasum -a 256 -c -

generate: native-baml $(BAML_CLI) ## Regenerate and format the BAML client
	"$(BAML_CLI)" generate --from baml_src
	gofmt -w baml_client
	go tool -modfile=tools/go.mod goimports -w baml_client

swagger: ## Regenerate the Swagger specification and embedded docs
	go tool -modfile=tools/go.mod swag init -g cmd/server/main.go -o docs --parseInternal
	gofmt -w docs/docs.go

build: ## Build the stripped service binary with CGO
	@mkdir -p "$(BIN_DIR)"
	CGO_ENABLED=1 go build -trimpath -ldflags='-s -w' -o "$(BIN_DIR)/axiom-server" ./cmd/server

run: native-baml ## Run the service using the current environment
	CGO_ENABLED=1 go run ./cmd/server

test: native-baml ## Run unit tests with race detection
	CGO_ENABLED=1 go test -race -count=1 ./...

cover: native-baml ## Run race tests and print total statement coverage
	CGO_ENABLED=1 go test -race -count=1 -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -func=coverage.out

test-e2e: native-baml ## Test the API documentation in Chrome or Chromium
	CGO_ENABLED=1 go test -tags e2e -count=1 ./test/e2e/...

lint: ## Vet build variants and check Go code and workflow files
	@command -v shellcheck >/dev/null || { echo "shellcheck is required for workflow linting; install it and add it to PATH" >&2; exit 1; }
	go vet ./...
	go vet -tags e2e ./...
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run ./...
	go tool -modfile=tools/go.mod actionlint

vuln: ## Check reachable Go vulnerabilities
	go tool -modfile=tools/go.mod govulncheck $(VULN_FLAGS) ./...

fmt: ## Format maintained Go source files
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) fmt

tidy-check: ## Fail if module metadata is not tidy
	go mod tidy
	go -C tools mod tidy
	git diff --exit-code -- go.mod go.sum tools/go.mod tools/go.sum

generate-check: generate swagger ## Fail if committed generated code is stale
	git diff --exit-code
	@test -z "$$(git ls-files --others --exclude-standard -- baml_client docs)"

docker-build: ## Build the non-root runtime image
	docker build --target runtime --build-arg BAML_VERSION=$(BAML_VERSION) -t $(IMAGE) .

container-smoke: ## Check runtime responses, log privacy and shutdown
	./scripts/container-smoke.sh "$(IMAGE)"

container-smoke-test: ## Verify rejection of unsafe runtime images
	./scripts/test-container-smoke.sh "$(IMAGE)"

clean: ## Remove local binaries and coverage artifacts
	rm -rf "$(BIN_DIR)" coverage.out coverage.html
