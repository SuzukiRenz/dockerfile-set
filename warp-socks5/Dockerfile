FROM alpine:3.21

LABEL description="Cloudflare WARP SOCKS5 proxy via warp-go"

# ── Alpine 依赖 ─────────────────────────────────────────────────
# gcompat      : warp-go 是 glibc 编译的二进制，musl 需要兼容层
# libc6-compat : 补充 ld-linux 动态链接符号
RUN apk add --no-cache \
        gcompat \
        libc6-compat \
        libstdc++ \
        ca-certificates \
        curl \
        iproute2 \
        iputils \
        bash \
        openssl \
        gcc \
        musl-dev \
        make \
        git \
    && git clone --depth=1 https://github.com/rofl0r/microsocks.git /tmp/microsocks \
    && cd /tmp/microsocks && make && cp microsocks /usr/local/bin/ \
    && apk del gcc musl-dev make git \
    && rm -rf /tmp/microsocks /var/cache/apk/*

# ── 拷入 warp-go 二进制（两个架构文件，启动时按实际架构选用） ──
# 仓库目录下需准备：
#   warp-go-amd64    (从 gitlab 下载的 amd64 版本，重命名)
#   warp-go-arm64    (从 gitlab 下载的 arm64 版本，重命名)
COPY warp-go-amd64 warp-go-arm64 /tmp/warp-bins/

RUN ARCH=$(uname -m) \
    && case "$ARCH" in \
        x86_64)  cp /tmp/warp-bins/warp-go-amd64 /usr/local/bin/warp-go ;; \
        aarch64) cp /tmp/warp-bins/warp-go-arm64 /usr/local/bin/warp-go ;; \
        *) echo "Unsupported arch: $ARCH" && exit 1 ;; \
    esac \
    && chmod +x /usr/local/bin/warp-go \
    && rm -rf /tmp/warp-bins

# ── 数据卷（持久化 warp.conf 及账户信息，重启不重新注册）───────
WORKDIR /warp
VOLUME  ["/warp/data"]

COPY entrypoint.sh /entrypoint.sh
RUN  chmod +x /entrypoint.sh

EXPOSE 1080

ENTRYPOINT ["/entrypoint.sh"]
