# ─────────────────────────────────────────────────────────────────────────────
# Stage 1 – Build Go subscription server
# ─────────────────────────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS go-builder

WORKDIR /build

RUN apk add --no-cache git

# Copy all source first, then tidy (generates go.sum) and build in one pass
COPY go/ .
RUN go mod tidy && \
    CGO_ENABLED=0 GOOS=linux \
    go build -ldflags="-s -w" -trimpath -o sub-server .

# ─────────────────────────────────────────────────────────────────────────────
# Stage 2 – Download subconverter binary  (amd64 / arm64 only)
# ─────────────────────────────────────────────────────────────────────────────
FROM alpine:3.19 AS sub-fetcher

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
# Stage 3 – Final runtime image
# ─────────────────────────────────────────────────────────────────────────────
FROM debian:bookworm-slim

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        supervisor \
        ca-certificates \
        curl && \
    rm -rf /var/lib/apt/lists/*

COPY --from=go-builder  /build/sub-server        /usr/local/bin/sub-server
COPY --from=sub-fetcher /opt/subconverter         /opt/subconverter
COPY subconverter/pref.ini  /opt/subconverter/pref.ini
COPY supervisord.conf        /etc/supervisor/conf.d/services.conf

RUN mkdir -p /data
VOLUME ["/data"]

# TOKEN/GUEST 不在此设默认值，避免 secrets 警告，统一由 docker-compose 传入
ENV PORT="8787"

EXPOSE 8787

HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD curl -sf http://localhost:8787/health || exit 1

CMD ["/usr/bin/supervisord", "-n", "-c", "/etc/supervisor/conf.d/services.conf"]
