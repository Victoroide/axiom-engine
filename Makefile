.PHONY: generate build test run docker-build docker-up clean

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
