# =============================================================================
# Dockerfile — 定时备份工具
# 基础镜像: debian-slim + rclone（替代 AWS CLI v2，体积从 300MB+ 降至 ~90MB）
# =============================================================================

FROM debian:bookworm-slim

LABEL maintainer="backup-tool"
LABEL description="定时打包备份工具，支持 WebDAV / S3 上传"

# ─── 安装系统依赖 ─────────────────────────────────────────────────────────────
RUN apt-get update && apt-get install -y --no-install-recommends \
    bash \
    curl \
    cron \
    ca-certificates \
    tzdata \
    tar \
    gzip \
    bzip2 \
    xz-utils \
    zstd \
    findutils \
    procps \
    && rm -rf /var/lib/apt/lists/*

# ─── 安装 rclone（单二进制，同时支持 WebDAV + S3 及几十种后端）────────────────
# 官方安装脚本自动识别架构（amd64 / arm64 等）
RUN curl -fsSL https://rclone.org/install.sh | bash \
    && rm -rf /tmp/rclone*

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
