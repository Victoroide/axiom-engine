ARG BAML_LIB_VERSION=0.226.3
ARG BAML_LIB_SHA256_AMD64=44ffbd33d0c3b3a79a228a815c657b47c3c0151258da9f62b24d62491467d8ac
ARG BAML_LIB_SHA256_ARM64=32cac207efacbb0776479a1c0b6e02a24c56d2adecb796d00fe38ce71c335d64
ARG BAML_LIB_SHA256_DARWIN_AMD64=e19249f734b459b1c05d0b8013dc61b8608da7722769dc8ec3c4adc8ef7939b2
ARG BAML_LIB_SHA256_DARWIN_ARM64=cafd573a3ff40a207fe3999b6a4eff75c747a39269954f55d658f61a15d6e934

FROM golang:1.27.4-bookworm@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea AS go-base

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

FROM gcr.io/distroless/cc-debian13:nonroot@sha256:532c7a94f9e96ba9db75eb9388f4227751131c7cb0a411d1a8521af4096135de AS runtime

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
COPY --chown=0:0 examples/graphs/ /usr/share/axiom-engine/graphs/
ENV PORT=8080 \
    BAML_LIBRARY_PATH=/app/libbaml_cffi.so \
    BAML_LIBRARY_DISABLE_DOWNLOAD=true \
    BAML_LOG=off
USER 65532:65532
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/app/axiom-server", "healthcheck"]
ENTRYPOINT ["/app/axiom-server"]
