# ─────────────────────────────────────────────────────────────────────────────
# Stage 1 - Build Go subscription server
# ─────────────────────────────────────────────────────────────────────────────
FROM golang:1.23-alpine AS go-builder

WORKDIR /build

RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY *.go ./
COPY web ./web
RUN CGO_ENABLED=0 GOOS=linux \
    go build -ldflags="-s -w" -trimpath -o sub-hub .

# ─────────────────────────────────────────────────────────────────────────────
# Stage 2 - Download subconverter binary
# ─────────────────────────────────────────────────────────────────────────────
FROM alpine:3.20 AS sub-fetcher

ARG TARGETARCH
ARG SUBCONVERTER_VERSION=0.9.0

RUN apk add --no-cache curl

RUN case "${TARGETARCH}" in \
        amd64) ARCH=linux64  ;; \
        arm64) ARCH=aarch64  ;; \
        *)     echo "Unsupported arch: ${TARGETARCH}" && exit 1 ;; \
    esac && \
    curl -fsSL \
      "https://github.com/tindy2013/subconverter/releases/download/v${SUBCONVERTER_VERSION}/subconverter_${ARCH}.tar.gz" \
      -o /tmp/sc.tar.gz && \
    mkdir -p /opt/subconverter && \
    tar -xzf /tmp/sc.tar.gz -C /opt/ && \
    chmod +x /opt/subconverter/subconverter

# ─────────────────────────────────────────────────────────────────────────────
# Stage 3 - Runtime
# ─────────────────────────────────────────────────────────────────────────────
FROM debian:bookworm-slim

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        supervisor \
        ca-certificates \
        libstdc++6 \
        libgcc-s1 \
        curl && \
    rm -rf /var/lib/apt/lists/*

COPY --from=go-builder  /build/sub-hub        /usr/local/bin/sub-hub
COPY --from=sub-fetcher /opt/subconverter     /opt/subconverter
COPY subconverter/pref.ini /opt/subconverter/pref.ini
COPY supervisord.conf /etc/supervisor/conf.d/services.conf

RUN mkdir -p /data
VOLUME ["/data"]

ENV PORT="8787"
ENV DB_PATH="/data/sub-hub.db"

EXPOSE 8787

HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD curl -sf http://localhost:8787/health || exit 1

CMD ["/usr/bin/supervisord", "-n", "-c", "/etc/supervisor/conf.d/services.conf"]
