# =============================================================================
# Dockerfile — 定时备份工具
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
    python3 \
    gpg \
    && rm -rf /var/lib/apt/lists/*

# ─── 安装 rclone ──────────────────────────────────────────────────────────────
# 官方提供 rclone-current-linux-{arch}.zip 固定地址，无需查版本号
# python3 单行解压，避免 Dockerfile 多行 heredoc 解析错误
RUN set -eux && \
    ARCH=$(uname -m) && \
    case "$ARCH" in \
      x86_64)  RC_ARCH="amd64" ;; \
      aarch64) RC_ARCH="arm64" ;; \
      armv7l)  RC_ARCH="arm-v7" ;; \
      *)       echo "Unsupported arch: $ARCH" && exit 1 ;; \
    esac && \
    curl -fsSL "https://downloads.rclone.org/rclone-current-linux-${RC_ARCH}.zip" -o /tmp/rclone.zip && \
    python3 -c "import zipfile,shutil,os; z=zipfile.ZipFile('/tmp/rclone.zip'); [shutil.copyfileobj(z.open(n),open('/usr/local/bin/rclone','wb')) or os.chmod('/usr/local/bin/rclone',0o755) for n in z.namelist() if n.endswith('/rclone')]" && \
    rm -f /tmp/rclone.zip && \
    rclone version

# ─── 创建目录 ─────────────────────────────────────────────────────────────────
RUN mkdir -p /app/logs /backup /data

WORKDIR /app

# ─── 复制脚本 ─────────────────────────────────────────────────────────────────
COPY backup.sh     /app/backup.sh
COPY entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/backup.sh /app/entrypoint.sh

# ─── 健康检查 ─────────────────────────────────────────────────────────────────
HEALTHCHECK --interval=60s --timeout=10s --retries=3 \
    CMD pgrep cron > /dev/null || exit 1

VOLUME ["/backup", "/data", "/app/logs"]

ENTRYPOINT ["/app/entrypoint.sh"]
