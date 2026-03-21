# ─────────────────────────────────────────────────────────────────────────────
# Stage 1 – Build the Go subscription server
# ─────────────────────────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS go-builder

WORKDIR /build

# Cache dependency downloads separately from source changes
COPY go/go.mod ./
RUN go mod download

COPY go/ .

RUN CGO_ENABLED=0 GOOS=linux \
    go build -ldflags="-s -w" -trimpath -o sub-server .

# ─────────────────────────────────────────────────────────────────────────────
# Stage 2 – Fetch subconverter binary for the target platform
# ─────────────────────────────────────────────────────────────────────────────
FROM alpine:3.19 AS sub-fetcher

ARG TARGETARCH
ARG SUBCONVERTER_VERSION=0.9.0

RUN apk add --no-cache curl tar

# Map Docker TARGETARCH → subconverter release filename suffix
RUN case "${TARGETARCH}" in \
        amd64)  ARCH=linux64    ;; \
        arm64)  ARCH=aarch64    ;; \
        arm)    ARCH=armv7      ;; \
        386)    ARCH=linux32    ;; \
        *)      ARCH=linux64    ;; \
    esac && \
    curl -fsSL \
      "https://github.com/tindy2013/subconverter/releases/download/v${SUBCONVERTER_VERSION}/subconverter_${ARCH}.tar.gz" \
      -o /tmp/subconverter.tar.gz && \
    mkdir -p /opt/subconverter && \
    tar -xzf /tmp/subconverter.tar.gz -C /opt/ && \
    chmod +x /opt/subconverter/subconverter

# ─────────────────────────────────────────────────────────────────────────────
# Stage 3 – Final minimal image
# ─────────────────────────────────────────────────────────────────────────────
FROM debian:bookworm-slim

# Install supervisor (process manager) and ca-certificates (HTTPS support)
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        supervisor \
        ca-certificates && \
    rm -rf /var/lib/apt/lists/*

# ── Binaries ──────────────────────────────────────────────────────────────────
COPY --from=go-builder   /build/sub-server              /usr/local/bin/sub-server
COPY --from=sub-fetcher  /opt/subconverter               /opt/subconverter

# ── Configuration ─────────────────────────────────────────────────────────────
# Override the bundled pref.ini with our hardened, internal-only config
COPY subconverter/pref.ini  /opt/subconverter/pref.ini
COPY supervisord.conf        /etc/supervisor/conf.d/services.conf

# ── Data directory ────────────────────────────────────────────────────────────
# Mount a volume here to persist nodes.txt across container restarts.
RUN mkdir -p /data
VOLUME ["/data"]

# ── Runtime defaults (override via environment or docker-compose) ─────────────
ENV TOKEN="change-me-please" \
    PORT="8080" \
    SUB_NAME="My Subscription" \
    NODES_FILE="/data/nodes.txt"

# Only the subscription port is exposed; subconverter (25500) stays internal.
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD curl -sf http://localhost:8080/health || exit 1

CMD ["/usr/bin/supervisord", "-n", "-c", "/etc/supervisor/conf.d/services.conf"]
