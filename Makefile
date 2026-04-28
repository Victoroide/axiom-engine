.PHONY: generate build test run docker-build docker-up docker-down clean fmt-check vet lint lint-install quality

GOLANGCI_LINT_VERSION := v1.64.8

generate:
	baml-cli generate
	gofmt -w baml_client
	goimports -w baml_client

build: generate
	go build -o bin/axiom-server ./cmd/server

test: generate
	go test ./... -v

run: build
	./bin/axiom-server

docker-build:
	docker compose build

docker-up:
	docker compose up

docker-down:
	docker compose down

clean:
	rm -rf bin/ baml_client/

fmt-check:
	@diff=$$(find . -name '*.go' -not -path './baml_client/*' | xargs gofmt -l); \
	if [ -n "$$diff" ]; then \
	  echo "gofmt issues in:"; echo "$$diff"; exit 1; \
	fi

vet:
	go vet $$(go list ./... | grep -v /baml_client)

lint-install:
	@which golangci-lint > /dev/null 2>&1 || { \
	  echo "Installing golangci-lint $(GOLANGCI_LINT_VERSION)..."; \
	  go install github.com/golangci/golangci-lint/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION); \
	}

lint: lint-install
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		$$(go env GOPATH)/bin/golangci-lint run || { echo "Please add $$(go env GOPATH)/bin to your PATH"; exit 1; }; \
	fi

quality: fmt-check vet lint
