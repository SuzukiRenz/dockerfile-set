# =============================================================================
# PicoClaw — Full 版 Dockerfile（从远程仓库构建）
# 包含：Node.js、Python 3、uv、git，可运行 stdio 模式的本地 MCP 服务器
#
# 与普通版的区别：
#   普通版 (picoclaw:latest)  ~11.5MB，仅含 picoclaw 二进制
#   Full 版  (picoclaw:full)  体积较大，额外支持 npx / uvx 调用本地 MCP 工具
#
# 构建命令：
#   docker build -f Dockerfile.full -t picoclaw:full .
#
# 多平台构建（需要 buildx）：
#   docker buildx build --platform linux/amd64,linux/arm64 \
#     -f Dockerfile.full -t picoclaw:full --push .
# =============================================================================


# -----------------------------------------------------------------------------
# 阶段一：Builder
# 在容器内拉取远程仓库并编译，宿主机无需安装 Go 工具链
# -----------------------------------------------------------------------------
FROM golang:1.23-alpine AS builder

# git：从 GitHub 克隆源码；make：执行项目构建脚本
RUN apk add --no-cache git make

WORKDIR /build

# 从 GitHub 拉取最新 main 分支代码（--depth=1 只取最新提交，加速克隆）
# 如需固定版本，改为：--branch v0.2.3
RUN git clone --depth=1 --branch main \
    https://github.com/sipeed/picoclaw.git .

# 单独下载依赖以利用 Docker 层缓存（依赖不变时跳过此层）
RUN go mod download

# 静态编译：CGO_ENABLED=0 生成无外部 .so 依赖的二进制，方便在 Alpine 运行
RUN CGO_ENABLED=0 GOOS=linux make build


# -----------------------------------------------------------------------------
# 阶段二：Full Runtime
# 在精简 Alpine 基础上叠加 Node.js / Python / uv / git
# 使容器能够通过 stdio 协议执行本地 MCP 服务器进程
# -----------------------------------------------------------------------------
FROM alpine:3.21

# ── 基础运行时依赖 ──────────────────────────────────────────────────────────
# ca-certificates  HTTPS 证书链（调用外部 API 必需）
# tzdata           时区数据库（支持 TZ 环境变量）
# curl             健康检查探针 + 内部 HTTP 请求
# bash             entrypoint.sh 脚本依赖
RUN apk add --no-cache \
    ca-certificates \
    tzdata \
    curl \
    bash

# ── Full 版专属：MCP 运行时依赖 ─────────────────────────────────────────────
# nodejs / npm  运行 npx 调用 Node.js MCP 服务器（如文件系统、数据库工具）
# python3       运行 Python MCP 服务器
# uv            快速 Python 包管理器，支持 uvx 调用 Python MCP 服务器
# git           支持需要 git 的 MCP 工具及技能（skill）拉取
RUN apk add --no-cache \
    nodejs \
    npm \
    python3 \
    py3-pip \
    git \
 && pip3 install --no-cache-dir uv --break-system-packages \
 # 初始化 npm 缓存目录，减少首次 npx 调用的延迟
 && npm config set cache /root/.npm

# ── 从 Builder 阶段复制编译产物 ─────────────────────────────────────────────
COPY --from=builder /build/bin/picoclaw   /usr/local/bin/picoclaw
# entrypoint.sh：首次运行触发 onboard 初始化，后续运行启动 gateway
COPY --from=builder /build/docker/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

# ── 持久化数据目录 ───────────────────────────────────────────────────────────
# config.json、workspace（session、memory、cron、skill）均存于此
# 在 Compose 中通过 volume 映射到宿主机 ./data 目录
RUN mkdir -p /root/.picoclaw

# ── 健康检查 ─────────────────────────────────────────────────────────────────
# 要使探针从容器外可达，需设置 PICOCLAW_GATEWAY_HOST=0.0.0.0
# /health → 始终返回 200（存活探针）
# /ready  → 服务就绪后返回 200，否则 503（就绪探针）
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD curl -sf http://localhost:18790/health || exit 1

# ── 默认时区（Compose 或 docker run 可通过 -e TZ= 覆盖）──────────────────────
ENV TZ=Asia/Shanghai

# ── 端口说明（仅文档；实际映射在 Compose ports: 中声明）─────────────────────
# 18790  健康检查 /health /ready
# 18800  Web UI Launcher
# 18791  LINE webhook
# 18792  WeCom App webhook
# 18793  WeCom Bot webhook
EXPOSE 18790 18800 18791 18792 18793

ENTRYPOINT ["/entrypoint.sh"]
CMD ["picoclaw", "gateway"]
