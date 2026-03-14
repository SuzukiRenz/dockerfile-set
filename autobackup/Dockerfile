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
    python3 \
    && rm -rf /var/lib/apt/lists/*

# ─── 安装 rclone（直接下载二进制，无需 unzip，支持 amd64 / arm64）────────────
# 用 python3 解压 zip（slim 镜像自带，无需额外安装）
RUN ARCH=$(uname -m) && \
    case "$ARCH" in \
        x86_64)  RCLONE_ARCH="amd64" ;; \
        aarch64) RCLONE_ARCH="arm64" ;; \
        armv7l)  RCLONE_ARCH="arm-v7" ;; \
        *)       echo "Unsupported arch: $ARCH" && exit 1 ;; \
    esac && \
    RCLONE_VER=$(curl -fsSL "https://api.github.com/repos/rclone/rclone/releases/latest" \
        | grep '"tag_name"' | head -1 | sed 's/.*"v\([^"]*\)".*/\1/') && \
    echo "Installing rclone v${RCLONE_VER} for ${RCLONE_ARCH}" && \
    curl -fsSL "https://github.com/rclone/rclone/releases/download/v${RCLONE_VER}/rclone-v${RCLONE_VER}-linux-${RCLONE_ARCH}.zip" \
        -o /tmp/rclone.zip && \
    python3 -c "
import zipfile, shutil
with zipfile.ZipFile('/tmp/rclone.zip') as z:
    for name in z.namelist():
        if name.endswith('/rclone'):
            with z.open(name) as src, open('/usr/local/bin/rclone', 'wb') as dst:
                shutil.copyfileobj(src, dst)
            break
" && \
    chmod +x /usr/local/bin/rclone && \
    rm -f /tmp/rclone.zip && \
    rclone version

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
