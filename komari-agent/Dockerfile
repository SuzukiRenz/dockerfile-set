# ============================================================
# Dockerfile for komari-agent
# 说明：原安装脚本会注册 systemd/openrc 等系统服务，
#       Docker 容器内无 init system，因此直接下载二进制并
#       以前台进程方式运行，由 Docker 自身负责进程生命周期。
# ============================================================

FROM alpine:3.21

# 安装依赖（alpine 自带 musl libc，komari-agent 为静态编译二进制，无需额外 libc）
RUN apk add --no-cache curl ca-certificates libc6-compat

# 构建参数（可在 docker build 时通过 --build-arg 覆盖）
ARG INSTALL_VERSION=latest
ARG INSTALL_DIR=/opt/komari

ENV TARGET_DIR=${INSTALL_DIR}

# 下载 komari-agent 二进制
RUN set -eux; \
    OS="linux"; \
    ARCH=$(uname -m); \
    case "$ARCH" in \
        x86_64)  ARCH="amd64" ;; \
        aarch64) ARCH="arm64" ;; \
        armv7*)  ARCH="arm"   ;; \
        i386|i686) ARCH="386" ;; \
        *) echo "Unsupported architecture: $ARCH" && exit 1 ;; \
    esac; \
    FILE_NAME="komari-agent-${OS}-${ARCH}"; \
    if [ "${INSTALL_VERSION}" = "latest" ]; then \
        DOWNLOAD_URL="https://github.com/komari-monitor/komari-agent/releases/latest/download/${FILE_NAME}"; \
    else \
        DOWNLOAD_URL="https://github.com/komari-monitor/komari-agent/releases/download/${INSTALL_VERSION}/${FILE_NAME}"; \
    fi; \
    echo "Downloading from: ${DOWNLOAD_URL}"; \
    mkdir -p "${INSTALL_DIR}"; \
    curl -fsSL -o "${INSTALL_DIR}/agent" "${DOWNLOAD_URL}"; \
    chmod +x "${INSTALL_DIR}/agent"

WORKDIR ${INSTALL_DIR}

# 暴露端口（根据 komari-agent 实际端口按需修改）
# EXPOSE 9090

# 直接前台运行，Docker 负责进程管理（替代 systemd/openrc）
# 运行时通过 CMD 或 docker run 末尾追加参数传入 komari-agent 启动参数
ENTRYPOINT ["/opt/komari/agent"]
CMD []
