#!/bin/bash
# =============================================================
#  WARP-GO + SOCKS5 容器启动脚本
#
#  设计思路：
#    - warp-go 在容器内建立 WARP TUN 接口（AllowedIPs 双栈）
#    - 容器路由表默认走 WARP，无需干预宿主机
#    - microsocks 监听 SOCKS5，客户端流量经容器路由走 WARP 出口
#    - 不修改宿主机路由，纯粹作为代理服务存在
# =============================================================
set -euo pipefail

red()    { echo -e "\033[31m[WARP] $*\033[0m"; }
green()  { echo -e "\033[32m[WARP] $*\033[0m"; }
yellow() { echo -e "\033[33m[WARP] $*\033[0m"; }
blue()   { echo -e "\033[36m[WARP] $*\033[0m"; }

# ── 环境变量（可通过 docker-compose / docker run -e 覆盖）─────
SOCKS5_PORT="${SOCKS5_PORT:-1080}"
SOCKS5_USER="${SOCKS5_USER:-}"        # 留空 = 无需认证
SOCKS5_PASS="${SOCKS5_PASS:-}"
FORCE_REGISTER="${FORCE_REGISTER:-false}"

WARPGO_BIN="/usr/local/bin/warp-go"
WARPGO_CONF="/warp/data/warp.conf"
WARP_PUBKEY="bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo="

# 双栈 endpoint（IPv4 优先；若容器只有 IPv6 可达，warp-go 会自动回落）
ENDPOINT_V4="162.159.192.1:2408"
ENDPOINT_V6="[2606:4700:d0::a29f:c001]:2408"

mkdir -p /warp/data

SOCKS5_PID=""

# ════════════════════════════════════════════════════════════════
#  TUN 设备检查
# ════════════════════════════════════════════════════════════════
check_tun() {
    if [[ ! -e /dev/net/tun ]]; then
        red "/dev/net/tun 不存在"
        red "请确保容器拥有: --device=/dev/net/tun 以及 --cap-add=NET_ADMIN"
        exit 1
    fi
    green "/dev/net/tun 检查通过"
}

# ════════════════════════════════════════════════════════════════
#  注册 WARP 账户
#  - 已有配置且 FORCE_REGISTER!=true 时跳过
# ════════════════════════════════════════════════════════════════
register_account() {
    if [[ -s "$WARPGO_CONF" && "$FORCE_REGISTER" != "true" ]]; then
        yellow "已有 warp.conf，跳过注册（FORCE_REGISTER=true 可强制重新注册）"
        return
    fi

    yellow "申请新 WARP 账户..."

    # 获取当前架构
    local ARCH
    case $(uname -m) in
        x86_64)  ARCH=amd64 ;;
        aarch64) ARCH=arm64 ;;
        *)       ARCH=amd64 ;;
    esac

    PRIV_KEY=""; DEV_ID=""; WARP_TOKEN=""

    # 方法一：gitlab 注册工具
    local API="/tmp/warpapi_$$"
    if curl -Ls --retry 3 --connect-timeout 10 \
        "https://gitlab.com/rwkgyg/CFwarp/-/raw/main/point/cpu1/${ARCH}" \
        -o "$API" 2>/dev/null && chmod +x "$API"; then
        local OUT
        OUT=$("$API" 2>/dev/null) || true
        PRIV_KEY=$(echo "$OUT"  | awk -F': ' '/private_key/{print $2}')
        DEV_ID=$(echo   "$OUT"  | awk -F': ' '/device_id/{print $2}')
        WARP_TOKEN=$(echo "$OUT"| awk -F': ' '/token/{print $2}')
    fi
    rm -f "$API"

    # 方法二：直接调 Cloudflare 官方 API
    if [[ -z "$PRIV_KEY" ]]; then
        yellow "工具注册失败，改用 Cloudflare API..."
        local TS RESP
        TS=$(date -u +"%Y-%m-%dT%H:%M:%S.000Z")
        RESP=$(curl -s --retry 3 --connect-timeout 10 \
            -X POST "https://api.cloudflareclient.com/v0a2158/reg" \
            -H "User-Agent: okhttp/3.12.1" \
            -H "CF-Client-Version: a-6.30-2158" \
            -H "Content-Type: application/json" \
            -d "{\"key\":\"$(openssl rand -base64 32 | tr -dc 'a-zA-Z0-9' | head -c 44)\",\
\"install_id\":\"\",\"fcm_token\":\"\",\"tos\":\"${TS}\",\
\"model\":\"PC\",\"serial_number\":\"\",\"locale\":\"zh-CN\"}" 2>/dev/null) || true
        PRIV_KEY=$(echo  "$RESP" | grep -oP '"private_key"\s*:\s*"\K[^"]+' || true)
        DEV_ID=$(echo    "$RESP" | grep -oP '"id"\s*:\s*"\K[^"]+' | head -1 || true)
        WARP_TOKEN=$(echo "$RESP"| grep -oP '"token"\s*:\s*"\K[^"]+' || true)
    fi

    if [[ -z "$PRIV_KEY" || -z "$DEV_ID" || -z "$WARP_TOKEN" ]]; then
        red "账户注册失败，请检查容器网络后重试"
        exit 1
    fi

    green "账户注册成功 (Device: ${DEV_ID:0:8}...)"
}

# ════════════════════════════════════════════════════════════════
#  计算 MTU
# ════════════════════════════════════════════════════════════════
calc_mtu() {
    yellow "计算 MTU..."
    local M=1420 S=10  # 容器内起始值低一点（Docker bridge 已占 50 左右）
    while true; do
        if ping -c1 -W1 -s$((M-28)) -Mdo 1.1.1.1 &>/dev/null \
        || ping6 -c1 -W1 -s$((M-28)) -Mdo 2606:4700:4700::1111 &>/dev/null 2>&1; then
            S=1; M=$((M+S))
        else
            M=$((M-S)); [[ $S -eq 1 ]] && break
        fi
        [[ $M -le 1280 ]] && M=1280 && break
    done
    MTU=$((M-80))
    # 容器内二层封装，额外保守 -20
    [[ $MTU -gt 1320 ]] && MTU=1320
    green "MTU = $MTU"
}

# ════════════════════════════════════════════════════════════════
#  生成 warp.conf
#  AllowedIPs 双栈全覆盖：让容器内所有流量走 WARP
#  不需要 PostUp/PostDown（无需保留宿主机路由）
# ════════════════════════════════════════════════════════════════
gen_warp_conf() {
    # 仅在需要重新注册时才重新写配置
    [[ -s "$WARPGO_CONF" && "$FORCE_REGISTER" != "true" ]] && return

    {
        echo "[Account]"
        echo "Device     = ${DEV_ID}"
        echo "PrivateKey = ${PRIV_KEY}"
        echo "Token      = ${WARP_TOKEN}"
        echo "Type       = free"
        echo "Name       = WARP"
        echo "MTU        = ${MTU}"
        echo ""
        echo "[Peer]"
        echo "PublicKey  = ${WARP_PUBKEY}"
        # 双 Endpoint：warp-go 会先尝试 IPv4，不通则 IPv6
        echo "Endpoint   = ${ENDPOINT_V4}"
        echo "Endpoint6  = ${ENDPOINT_V6}"
        # 双栈全接管：IPv4 + IPv6 流量都走 WARP
        echo "AllowedIPs = 0.0.0.0/0, ::/0"
        echo "KeepAlive  = 30"
        echo ""
        # [Script] 留空：容器内不需要额外路由保护规则
        echo "[Script]"
    } > "$WARPGO_CONF"

    chmod 600 "$WARPGO_CONF"
    green "warp.conf 已写入"
    yellow "── warp.conf ──────────────────────────────"
    cat "$WARPGO_CONF"
    yellow "───────────────────────────────────────────"
}

# ════════════════════════════════════════════════════════════════
#  等待 WARP TUN 接口出现
# ════════════════════════════════════════════════════════════════
wait_tun_up() {
    yellow "等待 WARP TUN 接口就绪..."
    for i in $(seq 1 30); do
        if ip link show WARP &>/dev/null 2>&1; then
            green "WARP 接口已就绪 (${i}×2s)"
            sleep 2
            return 0
        fi
        sleep 2
    done
    red "60 秒内 WARP 接口未出现，检查日志："
    red "  docker logs <container>"
    return 1
}

# ════════════════════════════════════════════════════════════════
#  验证 WARP 流量连通性
# ════════════════════════════════════════════════════════════════
verify_warp() {
    yellow "验证 WARP 连通性..."
    local wv4 wv6
    for i in $(seq 1 10); do
        wv4=$(curl -s4m10 https://www.cloudflare.com/cdn-cgi/trace -k 2>/dev/null \
              | grep '^warp=' | cut -d= -f2 || true)
        wv6=$(curl -s6m10 https://www.cloudflare.com/cdn-cgi/trace -k 2>/dev/null \
              | grep '^warp=' | cut -d= -f2 || true)
        if [[ $wv4 =~ on|plus || $wv6 =~ on|plus ]]; then
            green "✓ WARP 连通 (v4=${wv4:-N/A} / v6=${wv6:-N/A})"
            return 0
        fi
        yellow "第 $i/10 次验证..."
        sleep 5
    done
    red "WARP 连通性验证失败，SOCKS5 仍会启动但流量可能不经 WARP"
    return 1
}

# ════════════════════════════════════════════════════════════════
#  启动 microsocks SOCKS5
# ════════════════════════════════════════════════════════════════
start_socks5() {
    local ARGS="-i 0.0.0.0 -p ${SOCKS5_PORT}"
    if [[ -n "$SOCKS5_USER" && -n "$SOCKS5_PASS" ]]; then
        ARGS+=" -u ${SOCKS5_USER} -P ${SOCKS5_PASS}"
        green "SOCKS5 启动（:${SOCKS5_PORT}，认证: ${SOCKS5_USER}/***）"
    else
        green "SOCKS5 启动（:${SOCKS5_PORT}，无需认证）"
    fi

    microsocks $ARGS &
    SOCKS5_PID=$!
    sleep 1

    if ! kill -0 "$SOCKS5_PID" 2>/dev/null; then
        red "microsocks 启动失败"
        exit 1
    fi
    green "microsocks PID=$SOCKS5_PID"
}

# ════════════════════════════════════════════════════════════════
#  打印使用说明
# ════════════════════════════════════════════════════════════════
print_info() {
    local out_v4 out_v6
    out_v4=$(curl -s4m8 https://icanhazip.com -k 2>/dev/null | tr -d '[:space:]' || echo "N/A")
    out_v6=$(curl -s6m8 https://icanhazip.com -k 2>/dev/null | tr -d '[:space:]' || echo "N/A")

    echo
    blue "╔══════════════════════════════════════════════════╗"
    blue "║         WARP SOCKS5 代理已就绪                  ║"
    blue "╠══════════════════════════════════════════════════╣"
    blue "║  WARP 出口 IPv4 : ${out_v4}"
    blue "║  WARP 出口 IPv6 : ${out_v6}"
    blue "╠══════════════════════════════════════════════════╣"
    blue "║  SOCKS5 地址    : 0.0.0.0:${SOCKS5_PORT}"
    if [[ -n "$SOCKS5_USER" ]]; then
        blue "║  认证           : ${SOCKS5_USER} / ${SOCKS5_PASS}"
    else
        blue "║  认证           : 无"
    fi
    blue "╠══════════════════════════════════════════════════╣"
    blue "║  验证命令：                                      ║"
    blue "║  curl -sx socks5h://127.0.0.1:${SOCKS5_PORT} \\"
    blue "║    https://www.cloudflare.com/cdn-cgi/trace      ║"
    blue "║  # 输出应含 warp=on 或 warp=plus                ║"
    blue "╚══════════════════════════════════════════════════╝"
    echo
}

# ════════════════════════════════════════════════════════════════
#  Watchdog：定期检测 WARP 状态，掉线自动重连
# ════════════════════════════════════════════════════════════════
watchdog() {
    local FAIL=0 MAX_FAIL=5
    local CHECK_INTERVAL=300   # 正常检测间隔（秒）
    local RETRY_INTERVAL=20    # 失败重试间隔（秒）

    while true; do
        sleep $CHECK_INTERVAL

        # ── 检查 warp-go 进程 ────────────────────────────────────
        if ! pgrep -x warp-go &>/dev/null; then
            yellow "[watchdog] warp-go 进程丢失，重启..."
            $WARPGO_BIN --config="$WARPGO_CONF" &
            sleep 10
        fi

        # ── 检查 WARP 连通性 ─────────────────────────────────────
        local wv4 wv6
        wv4=$(curl -s4m10 https://www.cloudflare.com/cdn-cgi/trace -k 2>/dev/null \
              | grep '^warp=' | cut -d= -f2 || true)
        wv6=$(curl -s6m10 https://www.cloudflare.com/cdn-cgi/trace -k 2>/dev/null \
              | grep '^warp=' | cut -d= -f2 || true)

        if [[ $wv4 =~ on|plus || $wv6 =~ on|plus ]]; then
            echo "[$(date '+%H:%M:%S')] [watchdog] WARP 正常 (v4=${wv4:-N/A} v6=${wv6:-N/A})"
            FAIL=0
            CHECK_INTERVAL=300
        else
            FAIL=$((FAIL+1))
            yellow "[watchdog] WARP 掉线 (第 ${FAIL}/${MAX_FAIL} 次)，重启 warp-go..."
            kill -15 "$(pgrep warp-go)" 2>/dev/null || true
            sleep 3
            $WARPGO_BIN --config="$WARPGO_CONF" &
            sleep 15

            if [[ $FAIL -ge $MAX_FAIL ]]; then
                yellow "[watchdog] 连续 ${MAX_FAIL} 次失败，暂停 5 分钟后重试..."
                kill -15 "$(pgrep warp-go)" 2>/dev/null || true
                sleep 300
                $WARPGO_BIN --config="$WARPGO_CONF" &
                sleep 15
                FAIL=0
            fi
            CHECK_INTERVAL=$RETRY_INTERVAL
        fi

        # ── 检查 microsocks 进程 ─────────────────────────────────
        if [[ -n "$SOCKS5_PID" ]] && ! kill -0 "$SOCKS5_PID" 2>/dev/null; then
            yellow "[watchdog] microsocks 已退出，重启..."
            start_socks5
        fi
    done
}

# ════════════════════════════════════════════════════════════════
#  优雅退出
# ════════════════════════════════════════════════════════════════
cleanup() {
    yellow "收到退出信号，清理中..."
    kill -15 "$(pgrep warp-go)"  2>/dev/null || true
    [[ -n "$SOCKS5_PID" ]] && kill -15 "$SOCKS5_PID" 2>/dev/null || true
    exit 0
}
trap cleanup SIGTERM SIGINT

# ════════════════════════════════════════════════════════════════
#  主流程
# ════════════════════════════════════════════════════════════════
blue "══════════════════════════════════════════════"
blue "       WARP-GO SOCKS5 Proxy Container"
blue "══════════════════════════════════════════════"

check_tun
register_account
calc_mtu
gen_warp_conf

green "启动 warp-go..."
$WARPGO_BIN --config="$WARPGO_CONF" &
WARPGO_PID=$!

wait_tun_up
verify_warp
start_socks5
print_info

# 后台启动 watchdog
watchdog &

# 等待 warp-go 主进程（容器保持前台）
wait $WARPGO_PID
