FROM alpine:3.21

LABEL description="Cloudflare WARP SOCKS5 proxy via warp-go"

# ── Alpine 依赖 ─────────────────────────────────────────────────
# gcompat + libc6-compat：warp-go 是 glibc 编译的，musl 需要兼容层
RUN apk add --no-cache \
        gcompat libc6-compat libstdc++ \
        ca-certificates curl iproute2 iputils bash openssl \
        gcc musl-dev make git \
    && git clone --depth=1 https://github.com/rofl0r/microsocks.git /tmp/microsocks \
    && cd /tmp/microsocks && make && cp microsocks /usr/local/bin/ \
    && apk del gcc musl-dev make git \
    && rm -rf /tmp/microsocks /var/cache/apk/*

# ── 拷入 warp-go 二进制（两个架构文件同时放入，运行时按 uname -m 选择）
# 仓库目录下需准备：
#   warp-go-amd64  （x86_64 服务器使用）
#   warp-go-arm64  （ARM 服务器使用）
# 下载地址：
#   https://gitlab.com/ProjectWARP/warp-go/-/releases （官方最新）
#   https://gitlab.com/rwkgyg/CFwarp/-/raw/main/warp-go_1.0.8_linux_amd64 （镜像）
COPY warp-go-amd64 warp-go-arm64 /tmp/warp-bins/

RUN ARCH=$(uname -m) \
    && case "$ARCH" in \
        # x86_64 进一步检查 CPU 指令集，选择最优版本
        # 若只提供了 amd64 基础版，v2/v3/v4 会自动回退到 amd64
        x86_64) \
            CPU_FLAGS=$(grep 'flags' /proc/cpuinfo 2>/dev/null | head -1 || true); \
            if echo "$CPU_FLAGS" | grep -q 'avx512'; then WARP_ARCH=amd64v4; \
            elif echo "$CPU_FLAGS" | grep -q 'avx2';   then WARP_ARCH=amd64v3; \
            elif echo "$CPU_FLAGS" | grep -q 'sse3';   then WARP_ARCH=amd64v2; \
            else WARP_ARCH=amd64; fi ;; \
        aarch64) WARP_ARCH=arm64 ;; \
        *) echo "Unsupported arch: $ARCH" && exit 1 ;; \
    esac \
    # 尝试精确版本，找不到则回退到 amd64/arm64 基础版
    && (cp /tmp/warp-bins/warp-go-${WARP_ARCH} /usr/local/bin/warp-go 2>/dev/null \
        || cp /tmp/warp-bins/warp-go-amd64 /usr/local/bin/warp-go 2>/dev/null \
        || cp /tmp/warp-bins/warp-go-arm64 /usr/local/bin/warp-go 2>/dev/null) \
    && chmod +x /usr/local/bin/warp-go \
    && rm -rf /tmp/warp-bins \
    # 验证 glibc 兼容层是否能运行
    && /usr/local/bin/warp-go -v &>/dev/null && echo "warp-go 验证通过" \
       || echo "warp-go 验证失败（可能需要检查 gcompat）"

WORKDIR /warp
VOLUME  ["/warp/data"]

COPY entrypoint.sh /entrypoint.sh
RUN  chmod +x /entrypoint.sh

EXPOSE 1080

ENTRYPOINT ["/entrypoint.sh"]
