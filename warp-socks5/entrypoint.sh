#!/bin/bash
# =============================================================
#  WARP-GO + SOCKS5 容器启动脚本 v3
#
#  支持三种账户类型（通过 WARP_ACCOUNT_TYPE 环境变量切换）：
#    free  : 免费账户，无限流量（默认）
#    plus  : WARP+ 付费账户，需要 WARP_LICENSE_KEY
#    teams : Zero Trust 团队账户，需要 WARP_TEAMS_TOKEN
# =============================================================
set -euo pipefail

red()    { echo -e "\033[31m[WARP] $*\033[0m"; }
green()  { echo -e "\033[32m[WARP] $*\033[0m"; }
yellow() { echo -e "\033[33m[WARP] $*\033[0m"; }
blue()   { echo -e "\033[36m[WARP] $*\033[0m"; }

# ════════════════════════════════════════════════════════════════
#  环境变量
# ════════════════════════════════════════════════════════════════

# ── SOCKS5 ──────────────────────────────────────────────────────
SOCKS5_PORT="${SOCKS5_PORT:-1080}"
SOCKS5_USER="${SOCKS5_USER:-}"
SOCKS5_PASS="${SOCKS5_PASS:-}"

# ── WARP 账户类型 ────────────────────────────────────────────────
#   free  : 免费账户（默认）
#   plus  : WARP+ 账户，需配合 WARP_LICENSE_KEY
#   teams : Zero Trust，需配合 WARP_TEAMS_TOKEN
WARP_ACCOUNT_TYPE="${WARP_ACCOUNT_TYPE:-free}"

# ── WARP+ 许可证密钥（26 个字符，形如 xxxxxxxx-xxxxxxxx-xxxxxxxx）
WARP_LICENSE_KEY="${WARP_LICENSE_KEY:-}"

# ── Zero Trust 团队 Token
#   获取地址：https://web--public--warp-team-api--coia-mfs4.code.run/
WARP_TEAMS_TOKEN="${WARP_TEAMS_TOKEN:-}"

# ── 容器名（写入 warp.conf 的设备名，同时用于区分多容器实例）──
WARP_DEVICE_NAME="${WARP_DEVICE_NAME:-warp-docker}"

# ── 是否强制重新注册（换 IP / 换账户类型时设为 true）─────────
FORCE_REGISTER="${FORCE_REGISTER:-false}"

# ── 路径 ─────────────────────────────────────────────────────────
WARPGO_BIN="/usr/local/bin/warp-go"
WARPGO_CONF="/warp/data/warp.conf"
ACCOUNT_TYPE_FILE="/warp/data/account_type"
WARP_PUBKEY="bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo="
ENDPOINT_V4="162.159.192.1:2408"
ENDPOINT_V6="[2606:4700:d0::a29f:c001]:2408"

mkdir -p /warp/data
SOCKS5_PID=""

# ════════════════════════════════════════════════════════════════
#  参数校验
# ════════════════════════════════════════════════════════════════
validate_args() {
    case "$WARP_ACCOUNT_TYPE" in
        free) ;;
        plus)
            if [[ -z "$WARP_LICENSE_KEY" ]]; then
                red "WARP_ACCOUNT_TYPE=plus 时必须提供 WARP_LICENSE_KEY"
                red "  许可证密钥格式：xxxxxxxx-xxxxxxxx-xxxxxxxx（26 字符）"
                exit 1
            fi
            # 校验密钥格式（8-8-8 或纯 26 字符）
            if ! echo "$WARP_LICENSE_KEY" | grep -qE '^[A-Za-z0-9]{8}-[A-Za-z0-9]{8}-[A-Za-z0-9]{8}$|^[A-Za-z0-9]{26}$'; then
                yellow "WARP_LICENSE_KEY 格式看起来不对，请确认是否正确（继续尝试...）"
            fi
            ;;
        teams)
            if [[ -z "$WARP_TEAMS_TOKEN" ]]; then
                red "WARP_ACCOUNT_TYPE=teams 时必须提供 WARP_TEAMS_TOKEN"
                red "  Token 获取：https://web--public--warp-team-api--coia-mfs4.code.run/"
                exit 1
            fi
            ;;
        *)
            red "WARP_ACCOUNT_TYPE 无效值：'${WARP_ACCOUNT_TYPE}'"
            red "  可选值：free | plus | teams"
            exit 1
            ;;
    esac

    # 检测账户类型是否变更，变更则强制重新注册
    if [[ -f "$ACCOUNT_TYPE_FILE" ]]; then
        local PREV_TYPE; PREV_TYPE=$(cat "$ACCOUNT_TYPE_FILE")
        if [[ "$PREV_TYPE" != "$WARP_ACCOUNT_TYPE" ]]; then
            yellow "账户类型从 ${PREV_TYPE} 变更为 ${WARP_ACCOUNT_TYPE}，强制重新注册..."
            FORCE_REGISTER=true
        fi
    fi
}

# ════════════════════════════════════════════════════════════════
#  TUN 设备检查 & 自动创建
# ════════════════════════════════════════════════════════════════
check_tun() {
    if [[ -e /dev/net/tun ]]; then
        green "/dev/net/tun 已存在"
        return 0
    fi

    yellow "/dev/net/tun 不存在，尝试自动创建..."

    if ! grep -q tun /proc/modules 2>/dev/null && ! modprobe tun 2>/dev/null; then
        red "内核 tun 模块不可用"
        red "  OpenVZ/LXC：请去服务商控制面板开启 TUN/TAP 支持"
        red "  KVM/物理机：请在宿主机执行 modprobe tun"
        exit 1
    fi

    mkdir -p /dev/net
    if mknod /dev/net/tun c 10 200 2>/dev/null; then
        chmod 666 /dev/net/tun
        green "/dev/net/tun 自动创建成功"
        return 0
    fi

    red "自动创建失败（缺少 SYS_MKNOD 权限），请在宿主机手动执行："
    red "  mkdir -p /dev/net && mknod /dev/net/tun c 10 200 && chmod 666 /dev/net/tun"
    exit 1
}

# ════════════════════════════════════════════════════════════════
#  获取当前 CPU 架构
# ════════════════════════════════════════════════════════════════
get_arch() {
    case $(uname -m) in
        x86_64)  echo amd64 ;;
        aarch64) echo arm64 ;;
        *)       echo amd64 ;;
    esac
}

# ════════════════════════════════════════════════════════════════
#  注册基础账户（所有类型都先走这一步拿到设备凭证）
# ════════════════════════════════════════════════════════════════
register_base_account() {
    yellow "注册 WARP 基础账户..."

    local ARCH; ARCH=$(get_arch)
    PRIV_KEY=""; DEV_ID=""; WARP_TOKEN=""

    # 方法一：gitlab 注册工具
    local API="/tmp/warpapi_$$"
    if curl -Ls --retry 3 --connect-timeout 10 \
        "https://gitlab.com/rwkgyg/CFwarp/-/raw/main/point/cpu1/${ARCH}" \
        -o "$API" 2>/dev/null && chmod +x "$API"; then
        local OUT; OUT=$("$API" 2>/dev/null) || true
        PRIV_KEY=$(echo "$OUT"  | awk -F': ' '/private_key/{print $2}')
        DEV_ID=$(echo   "$OUT"  | awk -F': ' '/device_id/{print $2}')
        WARP_TOKEN=$(echo "$OUT"| awk -F': ' '/token/{print $2}')
    fi
    rm -f "$API"

    # 方法二：Cloudflare 官方 API
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
        red "基础账户注册失败，请检查容器网络"
        exit 1
    fi

    green "基础账户注册成功 (Device: ${DEV_ID:0:8}...)"
}

# ════════════════════════════════════════════════════════════════
#  升级为 WARP+ 账户
#  warp-go 提供 --update 子命令直接升级
# ════════════════════════════════════════════════════════════════
upgrade_to_plus() {
    yellow "升级为 WARP+ 账户 (License: ${WARP_LICENSE_KEY:0:8}...)..."

    # 先写入基础配置再升级
    write_base_conf "plus"

    local RESULT
    RESULT=$("$WARPGO_BIN" --update \
        --config="$WARPGO_CONF" \
        --license="$WARP_LICENSE_KEY" \
        --device-name="$WARP_DEVICE_NAME" 2>&1) || true

    # 验证升级结果：启动 warp-go 后检查 warp=plus
    "$WARPGO_BIN" --config="$WARPGO_CONF" &
    local TMP_PID=$!
    sleep 8

    local STATUS
    STATUS=$(curl -s4m10 https://www.cloudflare.com/cdn-cgi/trace -k 2>/dev/null \
             | grep '^warp=' | cut -d= -f2 || true)

    kill -15 $TMP_PID 2>/dev/null || true
    sleep 2

    if [[ "$STATUS" == "plus" ]]; then
        green "✓ WARP+ 升级成功！"
        # 记录 license 以便日后展示
        echo "$WARP_LICENSE_KEY" > /warp/data/plus_license.txt
        return 0
    fi

    red "WARP+ 升级失败（返回状态: ${STATUS:-无响应}）"
    red "可能原因："
    red "  1. 许可证密钥无效或已过期"
    red "  2. 密钥绑定设备数已超过上限（最多 5 台），请在手机 WARP 客户端移除旧设备"
    red "  3. 网络问题，可稍后重试"
    red "已回退为免费账户继续运行..."
    WARP_ACCOUNT_TYPE="free"
}

# ════════════════════════════════════════════════════════════════
#  注册 Zero Trust 团队账户
#  warp-go 提供 --register 子命令进行团队注册
# ════════════════════════════════════════════════════════════════
register_teams_account() {
    yellow "注册 Zero Trust 团队账户..."

    # 先写入基础配置
    write_base_conf "teams"

    local DEVICE_NAME="${WARP_DEVICE_NAME}-$(date +%s | tail -c 4)"

    local RESULT
    RESULT=$("$WARPGO_BIN" --register \
        --config="$WARPGO_CONF" \
        --team-config="$WARP_TEAMS_TOKEN" \
        --device-name="$DEVICE_NAME" 2>&1) || true

    # 验证注册结果
    "$WARPGO_BIN" --config="$WARPGO_CONF" &
    local TMP_PID=$!
    sleep 8

    local STATUS
    STATUS=$(curl -s4m10 https://www.cloudflare.com/cdn-cgi/trace -k 2>/dev/null \
             | grep '^warp=' | cut -d= -f2 || true)

    kill -15 $TMP_PID 2>/dev/null || true
    sleep 2

    if [[ "$STATUS" =~ on|plus ]]; then
        green "✓ Zero Trust 团队账户注册成功！(设备名: ${DEVICE_NAME})"
        return 0
    fi

    red "Zero Trust 注册失败（返回状态: ${STATUS:-无响应}）"
    red "可能原因："
    red "  1. WARP_TEAMS_TOKEN 无效或已过期"
    red "  2. 团队设备数量已达上限"
    red "  3. Token 获取地址：https://web--public--warp-team-api--coia-mfs4.code.run/"
    red "已回退为免费账户继续运行..."
    WARP_ACCOUNT_TYPE="free"
    register_base_account
    write_base_conf "free"
}

# ════════════════════════════════════════════════════════════════
#  写入基础 warp.conf（[Account] + [Peer] + [Script]）
# ════════════════════════════════════════════════════════════════
write_base_conf() {
    local TYPE="${1:-free}"
    {
        echo "[Account]"
        echo "Device     = ${DEV_ID}"
        echo "PrivateKey = ${PRIV_KEY}"
        echo "Token      = ${WARP_TOKEN}"
        echo "Type       = ${TYPE}"
        echo "Name       = ${WARP_DEVICE_NAME}"
        echo "MTU        = ${MTU}"
        echo ""
        echo "[Peer]"
        echo "PublicKey  = ${WARP_PUBKEY}"
        echo "Endpoint   = ${ENDPOINT_V4}"
        echo "Endpoint6  = ${ENDPOINT_V6}"
        echo "AllowedIPs = 0.0.0.0/0, ::/0"
        echo "KeepAlive  = 30"
        echo ""
        echo "[Script]"
    } > "$WARPGO_CONF"
    chmod 600 "$WARPGO_CONF"
}

# ════════════════════════════════════════════════════════════════
#  账户注册总入口
# ════════════════════════════════════════════════════════════════
setup_account() {
    # 有现成配置且不强制重注册 → 直接跳过
    if [[ -s "$WARPGO_CONF" && "$FORCE_REGISTER" != "true" ]]; then
        local SAVED_TYPE; SAVED_TYPE=$(cat "$ACCOUNT_TYPE_FILE" 2>/dev/null || echo "free")
        yellow "已有 warp.conf (账户类型: ${SAVED_TYPE})，跳过注册"
        yellow "  如需重新注册：设置 FORCE_REGISTER=true 并重启容器"
        return
    fi

    # 所有类型都先注册基础账户
    register_base_account

    case "$WARP_ACCOUNT_TYPE" in
        free)
            green "使用免费账户（无限流量）"
            write_base_conf "free"
            ;;
        plus)
            upgrade_to_plus
            # 如果升级失败已回退为 free，conf 已写好；成功则需更新 Type
            if [[ "$WARP_ACCOUNT_TYPE" == "plus" ]]; then
                sed -i 's/^Type.*/Type       = plus/' "$WARPGO_CONF"
            fi
            ;;
        teams)
            register_teams_account
            ;;
    esac

    # 记录当前账户类型
    echo "$WARP_ACCOUNT_TYPE" > "$ACCOUNT_TYPE_FILE"

    green "warp.conf 写入完毕"
    yellow "── warp.conf ──────────────────────────────"
    cat "$WARPGO_CONF"
    yellow "───────────────────────────────────────────"
}

# ════════════════════════════════════════════════════════════════
#  计算 MTU
# ════════════════════════════════════════════════════════════════
calc_mtu() {
    yellow "计算 MTU..."
    local M=1420 S=10
    while true; do
        if ping  -c1 -W1 -s$((M-28)) -Mdo 1.1.1.1              &>/dev/null \
        || ping6 -c1 -W1 -s$((M-28)) -Mdo 2606:4700:4700::1111 &>/dev/null 2>&1; then
            S=1; M=$((M+S))
        else
            M=$((M-S)); [[ $S -eq 1 ]] && break
        fi
        [[ $M -le 1280 ]] && M=1280 && break
    done
    MTU=$((M-80))
    [[ $MTU -gt 1320 ]] && MTU=1320
    green "MTU = $MTU"
    # 如果已有配置，同步更新 MTU 值
    [[ -s "$WARPGO_CONF" ]] && sed -i "s/^MTU.*/MTU        = ${MTU}/" "$WARPGO_CONF"
}

# ════════════════════════════════════════════════════════════════
#  等待 WARP TUN 接口出现
# ════════════════════════════════════════════════════════════════
wait_tun_up() {
    yellow "等待 WARP TUN 接口就绪..."
    for i in $(seq 1 30); do
        if ip link show WARP &>/dev/null 2>&1; then
            green "WARP 接口就绪 (${i}×2s)"
            sleep 2
            return 0
        fi
        sleep 2
    done
    red "60 秒内 WARP 接口未出现，查看日志排查"
    return 1
}

# ════════════════════════════════════════════════════════════════
#  验证 WARP 连通性
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
    red "WARP 连通性验证失败，SOCKS5 仍会启动"
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
        red "microsocks 启动失败"; exit 1
    fi
    green "microsocks PID=$SOCKS5_PID"
}

# ════════════════════════════════════════════════════════════════
#  打印状态面板
# ════════════════════════════════════════════════════════════════
print_info() {
    local out_v4 out_v6 wv4 wv6
    out_v4=$(curl -s4m8 https://icanhazip.com -k 2>/dev/null | tr -d '[:space:]' || echo "N/A")
    out_v6=$(curl -s6m8 https://icanhazip.com -k 2>/dev/null | tr -d '[:space:]' || echo "N/A")
    wv4=$(curl -s4m8 https://www.cloudflare.com/cdn-cgi/trace -k 2>/dev/null \
          | grep '^warp=' | cut -d= -f2 || echo "N/A")
    wv6=$(curl -s6m8 https://www.cloudflare.com/cdn-cgi/trace -k 2>/dev/null \
          | grep '^warp=' | cut -d= -f2 || echo "N/A")

    # 账户类型显示
    local ACCT_LABEL
    case "$(cat "$ACCOUNT_TYPE_FILE" 2>/dev/null || echo free)" in
        free)  ACCT_LABEL="免费账户（无限流量）" ;;
        plus)  ACCT_LABEL="WARP+ 付费账户（$(cat /warp/data/plus_license.txt 2>/dev/null | cut -c1-8)...）" ;;
        teams) ACCT_LABEL="Zero Trust 团队账户" ;;
    esac

    echo
    blue "╔══════════════════════════════════════════════════════╗"
    blue "║            WARP SOCKS5 代理已就绪                   ║"
    blue "╠══════════════════════════════════════════════════════╣"
    blue "║  账户类型    : ${ACCT_LABEL}"
    blue "╠══════════════════════════════════════════════════════╣"
    blue "║  WARP 出口 IPv4 : ${out_v4}  [${wv4}]"
    blue "║  WARP 出口 IPv6 : ${out_v6}  [${wv6}]"
    blue "╠══════════════════════════════════════════════════════╣"
    blue "║  SOCKS5 地址 : 0.0.0.0:${SOCKS5_PORT}"
    if [[ -n "$SOCKS5_USER" ]]; then
        blue "║  认证        : ${SOCKS5_USER} / ${SOCKS5_PASS}"
    else
        blue "║  认证        : 无"
    fi
    blue "╠══════════════════════════════════════════════════════╣"
    blue "║  验证：curl -sx socks5h://127.0.0.1:${SOCKS5_PORT} \\"
    blue "║    https://www.cloudflare.com/cdn-cgi/trace          ║"
    blue "╚══════════════════════════════════════════════════════╝"
    echo
}

# ════════════════════════════════════════════════════════════════
#  Watchdog
# ════════════════════════════════════════════════════════════════
watchdog() {
    local FAIL=0 MAX_FAIL=5 CHECK_INTERVAL=300 RETRY_INTERVAL=20

    while true; do
        sleep $CHECK_INTERVAL

        if ! pgrep -x warp-go &>/dev/null; then
            yellow "[watchdog] warp-go 丢失，重启..."
            "$WARPGO_BIN" --config="$WARPGO_CONF" &
            sleep 10
        fi

        local wv4 wv6
        wv4=$(curl -s4m10 https://www.cloudflare.com/cdn-cgi/trace -k 2>/dev/null \
              | grep '^warp=' | cut -d= -f2 || true)
        wv6=$(curl -s6m10 https://www.cloudflare.com/cdn-cgi/trace -k 2>/dev/null \
              | grep '^warp=' | cut -d= -f2 || true)

        if [[ $wv4 =~ on|plus || $wv6 =~ on|plus ]]; then
            echo "[$(date '+%H:%M:%S')] [watchdog] 正常 (v4=${wv4:-N/A} v6=${wv6:-N/A})"
            FAIL=0; CHECK_INTERVAL=300
        else
            FAIL=$((FAIL+1))
            yellow "[watchdog] 掉线 (第 ${FAIL}/${MAX_FAIL} 次)，重启..."
            kill -15 "$(pgrep warp-go)" 2>/dev/null || true
            sleep 3
            "$WARPGO_BIN" --config="$WARPGO_CONF" &
            sleep 15

            if [[ $FAIL -ge $MAX_FAIL ]]; then
                yellow "[watchdog] 连续失败，暂停 5 分钟..."
                kill -15 "$(pgrep warp-go)" 2>/dev/null || true
                sleep 300
                "$WARPGO_BIN" --config="$WARPGO_CONF" &
                sleep 15; FAIL=0
            fi
            CHECK_INTERVAL=$RETRY_INTERVAL
        fi

        if [[ -n "$SOCKS5_PID" ]] && ! kill -0 "$SOCKS5_PID" 2>/dev/null; then
            yellow "[watchdog] microsocks 退出，重启..."
            start_socks5
        fi
    done
}

# ════════════════════════════════════════════════════════════════
#  优雅退出
# ════════════════════════════════════════════════════════════════
cleanup() {
    yellow "退出中..."
    kill -15 "$(pgrep warp-go)" 2>/dev/null || true
    [[ -n "$SOCKS5_PID" ]] && kill -15 "$SOCKS5_PID" 2>/dev/null || true
    exit 0
}
trap cleanup SIGTERM SIGINT

# ════════════════════════════════════════════════════════════════
#  主流程
# ════════════════════════════════════════════════════════════════
blue "══════════════════════════════════════════════════"
blue "       WARP-GO SOCKS5 Proxy  [账户: ${WARP_ACCOUNT_TYPE}]"
blue "══════════════════════════════════════════════════"

validate_args
check_tun
calc_mtu
setup_account

green "启动 warp-go..."
"$WARPGO_BIN" --config="$WARPGO_CONF" &
WARPGO_PID=$!

wait_tun_up
verify_warp
start_socks5
print_info

watchdog &

wait $WARPGO_PID
