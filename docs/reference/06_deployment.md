# 06 – Deployment

## Environment Variables

| Variable | Required | Description | Example |
|----------|----------|-------------|---------|
| `PORT` | No | TCP port the Fiber server listens on. Defaults to `8080` if unset. | `8080` |
| `AWS_REGION` | Yes | AWS region for the Amazon Bedrock endpoint used by BAML. | `us-east-1` |
| `AWS_ACCESS_KEY_ID` | Yes | AWS access key ID for Bedrock authentication. Consumed by the BAML native runtime, not by Go code directly. | `AKIAIOSFODNN7EXAMPLE` |
| `AWS_SECRET_ACCESS_KEY` | Yes | AWS secret access key for Bedrock authentication. Consumed by the BAML native runtime. | `wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY` |

AWS credentials are read by the BAML FFI library at runtime during `baml_client` initialization. They are not accessed via `os.Getenv` in Go application code; the BAML native runtime handles credential resolution using the standard AWS SDK credential chain.

## Dockerfile

The build uses a two-stage Dockerfile (`Dockerfile` at the repo root):

**Stage 1 — builder** (`golang:1.25-bookworm`)

1. Installs `goimports` (BAML post-generation formatter) and `baml-cli` (BAML code generator).
2. Copies `go.mod` / `go.sum` and runs `go mod download` to cache modules.
3. Copies all source files.
4. Runs `baml-cli generate` to regenerate the `baml_client/` Go package from `baml_src/`.
5. Formats the generated client with `gofmt` and `goimports`.
6. Compiles the server binary with `CGO_ENABLED=1` (required by the BAML FFI native library): `go build -o /bin/axiom-server ./cmd/server`.

**Stage 2 — runtime** (`debian:bookworm-slim`)

- Installs `ca-certificates` and `curl` for TLS and health check support.
- Copies the compiled binary to `/app/axiom-server`.
- Copies `baml_src/` to `/app/baml_src` (required by the BAML runtime at startup for prompt template resolution).
- Copies the BAML native library cache from `/root/.cache/` in the builder (avoids re-downloading the shared library on each container start).
- Exposes port `8080`. Sets `ENV PORT=8080`.
- Entry point: `CMD ["./axiom-server"]`.

## Railway Deployment

Configuration is in `railway.toml` at the repo root:

```toml
[build]
builder = "DOCKERFILE"

[deploy]
startCommand = "./axiom-server"
healthcheckPath = "/health"
healthcheckTimeout = 30
restartPolicyType = "ON_FAILURE"
restartPolicyMaxRetries = 3
```

| Setting | Value | Notes |
|---------|-------|-------|
| Builder | `DOCKERFILE` | Railway builds and runs the two-stage Dockerfile |
| Start command | `./axiom-server` | Relative to the `WORKDIR /app` set in the Dockerfile |
| Health check path | `/health` | Railway polls this endpoint to determine container readiness |
| Health check timeout | 30 s | Maximum wait time per health probe |
| Restart policy | `ON_FAILURE` | Container is restarted on non-zero exit code |
| Max retries | 3 | After 3 consecutive failures Railway marks the service as crashed |

AWS credentials must be configured as Railway environment variables (`AWS_REGION`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`).

## Local Development

**Without Docker**

```bash
# Install the BAML CLI (required once)
go install github.com/boundaryml/baml/baml-cli@latest

# Generate the BAML Go client
baml-cli generate

# Set required environment variables
export AWS_REGION=us-east-1
export AWS_ACCESS_KEY_ID=<your-key>
export AWS_SECRET_ACCESS_KEY=<your-secret>

# Build and run
go build -o bin/axiom-server ./cmd/server
./bin/axiom-server
```

Alternatively, use the Makefile:

```bash
make generate   # regenerate BAML client
make build      # build binary to bin/axiom-server
make run        # build + run
```

**With Docker Compose**

```bash
# Build the image (includes BAML generation and CGO compilation)
make docker-build   # or: docker compose build

# Start the container
make docker-up      # or: docker compose up

# Stop and remove
make docker-down    # or: docker compose down
```

Pass AWS credentials to the container via environment or a `.env` file consumed by `docker compose`.

## Health Check

| Property | Value |
|----------|-------|
| Endpoint | `GET /health` |
| Expected response body | `{"status":"ok"}` |
| Expected status code | `200 OK` |
| Railway usage | Railway polls this path every 30 s to confirm the container is alive |
| Docker usage | Add `HEALTHCHECK CMD curl -f http://localhost:8080/health` to the Dockerfile if process-level health monitoring is needed |
