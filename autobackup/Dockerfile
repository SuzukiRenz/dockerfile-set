# =============================================================================
# Dockerfile — 定时备份工具
# 基础镜像: debian-slim (体积小、工具齐全)
# =============================================================================

FROM debian:bookworm-slim

LABEL maintainer="backup-tool"
LABEL description="定时打包备份工具，支持 WebDAV / S3 上传"

# ─── 安装依赖 ─────────────────────────────────────────────────────────────────
RUN apt-get update && apt-get install -y --no-install-recommends \
    # 基础工具
    bash \
    curl \
    wget \
    cron \
    ca-certificates \
    tzdata \
    # 压缩工具
    tar \
    gzip \
    bzip2 \
    xz-utils \
    zstd \
    # 其他实用工具
    findutils \
    procps \
    && rm -rf /var/lib/apt/lists/*

# ─── 安装 AWS CLI v2 (用于 S3 支持) ──────────────────────────────────────────
RUN ARCH=$(uname -m) && \
    if [ "$ARCH" = "x86_64" ]; then \
        AWS_URL="https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip"; \
    elif [ "$ARCH" = "aarch64" ]; then \
        AWS_URL="https://awscli.amazonaws.com/awscli-exe-linux-aarch64.zip"; \
    else \
        echo "Unsupported architecture: $ARCH" && exit 1; \
    fi && \
    curl -fsSL "$AWS_URL" -o /tmp/awscliv2.zip && \
    apt-get update && apt-get install -y --no-install-recommends unzip && \
    unzip -q /tmp/awscliv2.zip -d /tmp && \
    /tmp/aws/install && \
    rm -rf /tmp/awscliv2.zip /tmp/aws && \
    apt-get purge -y unzip && \
    apt-get autoremove -y && \
    rm -rf /var/lib/apt/lists/*

# ─── 创建应用目录结构 ─────────────────────────────────────────────────────────
RUN mkdir -p /app/logs /backup /data

WORKDIR /app

# ─── 复制脚本 ─────────────────────────────────────────────────────────────────
COPY backup.sh     /app/backup.sh
COPY entrypoint.sh /app/entrypoint.sh

RUN chmod +x /app/backup.sh /app/entrypoint.sh

# ─── 健康检查 ─────────────────────────────────────────────────────────────────
HEALTHCHECK --interval=60s --timeout=10s --retries=3 \
    CMD pgrep cron > /dev/null || exit 1

# ─── 数据卷 ───────────────────────────────────────────────────────────────────
VOLUME ["/backup", "/data", "/app/logs"]

ENTRYPOINT ["/app/entrypoint.sh"]
