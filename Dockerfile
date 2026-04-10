# ── Stage 1: BAML generation + Go build ─────────────────────────
FROM golang:1.25-bookworm AS builder

# Install goimports for BAML post-generation formatting
RUN go install golang.org/x/tools/cmd/goimports@latest

# Install BAML CLI
RUN go install github.com/boundaryml/baml/baml-cli@latest

WORKDIR /src

# Cache Go modules
COPY go.mod go.sum ./
RUN go mod download

# Copy source
COPY . .

# Generate BAML Go client
RUN baml-cli generate \
    && gofmt -w baml_client \
    && goimports -w baml_client

# Build the server binary (CGO_ENABLED=1 required by BAML FFI runtime)
RUN CGO_ENABLED=1 GOOS=linux go build -o /bin/axiom-server ./cmd/server

# ── Stage 2: Runtime image ──────────────────────────────────────
FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Copy binary
COPY --from=builder /bin/axiom-server .

# Copy BAML source files (needed at runtime for BAML runtime init)
COPY --from=builder /src/baml_src ./baml_src

# Copy the BAML native library cache from the builder
COPY --from=builder /root/.cache/ /root/.cache/

EXPOSE 8080

ENV PORT=8080

CMD ["./axiom-server"]
