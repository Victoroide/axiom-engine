ARG BAML_LIB_VERSION=0.226.2
ARG BAML_LIB_SHA256_AMD64=45a9a398da97fdce9066d19cb4ef00839738d615aa661ca33a69efcba2a793f3
ARG BAML_LIB_SHA256_ARM64=425641930fd6cc42fca5c080adab50c19f79fc997e0c0d7728818fa70fa7a911
ARG BAML_LIB_SHA256_DARWIN_AMD64=c717d3eee5a7e5e0093d4fd336066d45ad886da217b6aa9f193a6a323f48b712
ARG BAML_LIB_SHA256_DARWIN_ARM64=137e0ce934b761473e93226490b1c829190be3fcac26105340723862866ab470

FROM golang:1.27.3-bookworm@sha256:972b4110ba0f47539a975c31e52c64765e1ff19f2d74469ae4a595c6676e80a5 AS go-base

FROM go-base AS native
ARG BAML_LIB_VERSION
ARG BAML_LIB_SHA256_AMD64
ARG BAML_LIB_SHA256_ARM64
ARG TARGETARCH
RUN set -eu; \
    case "$TARGETARCH" in \
      amd64) triple=x86_64; checksum="$BAML_LIB_SHA256_AMD64" ;; \
      arm64) triple=aarch64; checksum="$BAML_LIB_SHA256_ARM64" ;; \
      *) echo "Unsupported BAML architecture: $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    mkdir -p /out; \
    curl --fail --silent --show-error --location --retry 3 --connect-timeout 10 \
      --speed-limit 1024 --speed-time 30 \
      "https://github.com/boundaryml/baml/releases/download/$BAML_LIB_VERSION/libbaml_cffi-$triple-unknown-linux-gnu.so" \
      --output /out/libbaml_cffi.so; \
    printf '%s  %s\n' "$checksum" /out/libbaml_cffi.so | sha256sum -c -

FROM go-base AS builder
WORKDIR /src
ENV CGO_ENABLED=1
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG BAML_VERSION
ARG BAML_LIB_VERSION
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    set -eu; \
    version="$(sed -n 's/^[[:space:]]*version[[:space:]]*"\([^" ]*\)".*/\1/p' baml_src/generators.baml)"; \
    test -n "$version"; \
    test "${BAML_VERSION:-$version}" = "$version"; \
    test "$BAML_LIB_VERSION" = "$version"; \
    test "$(go list -m -f '{{.Version}}' github.com/boundaryml/baml)" = "v$version"; \
    go build -trimpath -ldflags='-s -w' -o /out/axiom-server ./cmd/server

FROM gcr.io/distroless/cc-debian12:nonroot@sha256:9dac0a79194e45a7da0158a9c6da57b217585af0786db3845d1f0ec1a0dd182f AS runtime

ARG VERSION=dev
ARG VCS_REF=unknown
ARG BUILD_DATE
LABEL org.opencontainers.image.title="axiom-engine" \
    org.opencontainers.image.description="Adaptive learning reasoning service" \
    org.opencontainers.image.source="https://github.com/Victoroide/axiom-engine" \
    org.opencontainers.image.url="https://github.com/Victoroide/axiom-engine" \
    org.opencontainers.image.licenses="MIT" \
    org.opencontainers.image.version="$VERSION" \
    org.opencontainers.image.revision="$VCS_REF" \
    org.opencontainers.image.created="$BUILD_DATE"

WORKDIR /app
COPY --from=builder /out/axiom-server /app/axiom-server
COPY --from=native /out/libbaml_cffi.so /app/libbaml_cffi.so
ENV PORT=8080 \
    BAML_LIBRARY_PATH=/app/libbaml_cffi.so \
    BAML_LIBRARY_DISABLE_DOWNLOAD=true \
    BAML_LOG=off
USER 65532:65532
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/app/axiom-server", "healthcheck"]
ENTRYPOINT ["/app/axiom-server"]
