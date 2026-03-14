#!/bin/bash
# =============================================================
#  WARP-GO + SOCKS5 容器启动脚本 v5
#  参考 fscarmen/warp-go.sh + yonggekkk/CFwarp.sh 深度优化
#
#  核心改进：
#    - 注册 API 使用 warp.cloudflare.nyc.mn（fscarmen 公共服务）
#    - MTU 二分查找（比线性搜索快 5-10 倍）
#    - IP 信息/WARP 状态/国家 单次 API 调用获取（ip.cloudflare.nyc.mn）
#    - 地区筛选通过 WARP 接口直接查询（不走非WARP出口）
#    - 重注册前自动注销旧设备（避免设备数堆积）
#    - warp.conf 格式与官方 fscarmen 保持一致（含 [Device] 节）
#    - Endpoint 使用 DNS 域名（engage.cloudflareclient.com），不硬编码 IP
#    - 账户类型变更自动检测并强制重注册
# =============================================================
set -euo pipefail

red()    { echo -e "\033[31m[WARP] $*\033[0m"; }
green()  { echo -e "\033[32m[WARP] $*\033[0m"; }
yellow() { echo -e "\033[33m[WARP] $*\033[0m"; }
blue()   { echo -e "\033[36m[WARP] $*\033[0m"; }

# ════════════════════════════════════════════════════════════════
#  环境变量
# ════════════════════════════════════════════════════════════════
SOCKS5_PORT="${SOCKS5_PORT:-1080}"
SOCKS5_USER="${SOCKS5_USER:-}"
SOCKS5_PASS="${SOCKS5_PASS:-}"

# 账户类型：free | plus | teams
WARP_ACCOUNT_TYPE="${WARP_ACCOUNT_TYPE:-free}"
# WARP+ 许可证（plus 时用，留空自动获取）
WARP_LICENSE_KEY="${WARP_LICENSE_KEY:-}"
# Zero Trust Token（teams 时用，留空自动获取）
WARP_TEAMS_TOKEN="${WARP_TEAMS_TOKEN:-}"

# 地区筛选：ISO 2字母代码，逗号分隔，留空不限
# 例："US" 或 "US,JP,SG,HK"
WARP_COUNTRIES="${WARP_COUNTRIES:-}"
WARP_COUNTRY_RETRY="${WARP_COUNTRY_RETRY:-10}"

WARP_DEVICE_NAME="${WARP_DEVICE_NAME:-warp-docker}"
FORCE_REGISTER="${FORCE_REGISTER:-false}"

# ── 路径 ─────────────────────────────────────────────────────────
WARPGO_BIN="/usr/local/bin/warp-go"
WARPGO_CONF="/warp/data/warp.conf"
WARPGO_LOG="/warp/data/warp-go.log"    # warp-go 运行日志，出问题直接 cat
WARP_PUBKEY="bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo="
WARP_ENDPOINT="engage.cloudflareclient.com:2408"

# TUN 接口名：必须与 write_conf 里 [Device].Name 完全一致
# warp-go 会用该名字创建 TUN 接口；所有 ip link / --interface 都引用此变量
WARP_IF="WARP"

# fscarmen 公共 API（注册 / pluskey）
WARP_REG_API="https://warp.cloudflare.nyc.mn"
# IP 信息 API（单次返回 warp状态+IP+国家+ISP）
WARP_IP_API="http://ip.cloudflare.nyc.mn"

mkdir -p /warp/data
SOCKS5_PID=""
WARPGO_PID=""

# ════════════════════════════════════════════════════════════════
#  参数校验 & 账户类型变更检测
# ════════════════════════════════════════════════════════════════
validate_args() {
    case "$WARP_ACCOUNT_TYPE" in
        free|plus|teams) ;;
        *)
            red "WARP_ACCOUNT_TYPE 无效：'${WARP_ACCOUNT_TYPE}'（可选：free | plus | teams）"
            exit 1 ;;
    esac

    # 账户类型变更 → 强制重注册
    if [[ -s "$WARPGO_CONF" ]] && grep -q '^Type' "$WARPGO_CONF"; then
        local PREV_TYPE
        PREV_TYPE=$(awk -F'[= ]+' '/^Type/{print $NF}' "$WARPGO_CONF" | tr -d '[:space:]')
        if [[ "$PREV_TYPE" != "$WARP_ACCOUNT_TYPE" ]]; then
            yellow "账户类型变更：${PREV_TYPE} → ${WARP_ACCOUNT_TYPE}，强制重注册"
            FORCE_REGISTER=true
        fi
    fi

    # 国家代码规范化（大写、去空格）
    WARP_COUNTRIES=$(echo "$WARP_COUNTRIES" | tr '[:lower:]' '[:upper:]' | tr -s ' ,;' ',' | sed 's/^,//;s/,$//')
}

# ════════════════════════════════════════════════════════════════
#  TUN 设备检查 & 自动创建
# ════════════════════════════════════════════════════════════════
check_tun() {
    if [[ -e /dev/net/tun ]]; then
        # 验证 TUN 可用（读取返回 "in bad state" 说明正常）
        local TUN_STATE; TUN_STATE=$(cat /dev/net/tun 2>&1 | tr 'A-Z' 'a-z' || true)
        if [[ "$TUN_STATE" =~ 'in bad state'|'处于错误状态' ]]; then
            green "/dev/net/tun 可用"
            return 0
        fi
    fi

    yellow "/dev/net/tun 不存在或不可用，尝试创建..."

    # 确认内核模块可加载
    if ! grep -q '^tun ' /proc/modules 2>/dev/null; then
        modprobe tun 2>/dev/null || true
        sleep 1
    fi

    mkdir -p /dev/net
    if mknod /dev/net/tun c 10 200 2>/dev/null; then
        chmod 666 /dev/net/tun
        # 再次验证
        local TUN_STATE; TUN_STATE=$(cat /dev/net/tun 2>&1 | tr 'A-Z' 'a-z' || true)
        if [[ "$TUN_STATE" =~ 'in bad state'|'处于错误状态' ]]; then
            green "/dev/net/tun 创建并验证成功"
            return 0
        fi
    fi

    red "TUN 设备不可用。请检查："
    red "  1. OpenVZ/LXC：去服务商面板开启 TUN/TAP"
    red "  2. KVM 宿主机：在宿主机执行 mkdir -p /dev/net && mknod /dev/net/tun c 10 200 && chmod 666 /dev/net/tun"
    red "  3. 确认 docker-compose.yml 中有 devices: /dev/net/tun 和 cap_add: NET_ADMIN"
    exit 1
}

# ════════════════════════════════════════════════════════════════
#  MTU 二分查找（比线性搜索快 5-10 倍，来自 fscarmen）
# ════════════════════════════════════════════════════════════════
calc_mtu() {
    yellow "计算最优 MTU（二分查找）..."
    local MIN=1280 MAX=1500 BEST=1280
    local TEST_IP PING_CMD

    # 用能 ping 通的地址
    if ping -c1 -W2 162.159.192.1 &>/dev/null 2>&1; then
        TEST_IP="162.159.192.1"; PING_CMD="ping"
    elif ping6 -c1 -W2 2606:4700:d0::a29f:c001 &>/dev/null 2>&1; then
        TEST_IP="2606:4700:d0::a29f:c001"; PING_CMD="ping6"
    else
        yellow "无法 ping 通 WARP Endpoint，使用默认 MTU=1280"
        MTU=1280; return
    fi

    # 二分查找最大可用 MTU
    while [[ $MIN -le $MAX ]]; do
        local MID=$(( (MIN + MAX) / 2 ))
        if $PING_CMD -c1 -W1 -s $MID -M do "$TEST_IP" &>/dev/null 2>&1; then
            BEST=$MID; MIN=$((MID + 1))
        else
            MAX=$((MID - 1))
        fi
    done

    # 向上微调确认最大值
    local i=$((BEST + 1))
    while [[ $i -le 1420 ]]; do
        $PING_CMD -c1 -W1 -s $i -M do "$TEST_IP" &>/dev/null 2>&1 && BEST=$i || break
        i=$((i + 1))
    done

    # 减去 WireGuard 包头开销（IPv4: 60, IPv6: 80）
    [[ "$TEST_IP" == *:* ]] && MTU=$((BEST + 28 - 80)) || MTU=$((BEST + 28 - 60))

    # 安全范围
    [[ $MTU -lt 1280 ]] && MTU=1280
    [[ $MTU -gt 1420 ]] && MTU=1420
    green "MTU = $MTU"
}

# ════════════════════════════════════════════════════════════════
#  注销旧设备（避免换账号后设备数堆积，来自 fscarmen）
# ════════════════════════════════════════════════════════════════
cancel_old_device() {
    [[ ! -s "$WARPGO_CONF" ]] && return

    local DEV_ID TOKEN
    DEV_ID=$(awk -F'[= ]+' '/^Device/{print $NF}' "$WARPGO_CONF" | tr -d '[:space:]')
    TOKEN=$(awk -F'[= ]+' '/^Token/{print $NF}' "$WARPGO_CONF" | tr -d '[:space:]')

    [[ -z "$DEV_ID" || -z "$TOKEN" ]] && return

    # 跳过 Teams 预设账户（ID 以 t. 开头）
    if [[ "$DEV_ID" =~ ^t\. ]]; then
        yellow "Teams 账户不注销旧设备，跳过"
        return
    fi

    yellow "注销旧设备 ${DEV_ID:0:8}..."
    curl -s --max-time 5 \
        -X DELETE "https://api.cloudflareclient.com/v0a2158/reg/${DEV_ID}" \
        -H 'User-Agent: okhttp/3.12.1' \
        -H 'CF-Client-Version: a-6.10-2158' \
        -H 'Content-Type: application/json' \
        -H "Authorization: Bearer ${TOKEN}" &>/dev/null || true
    green "旧设备已注销"
}

# ════════════════════════════════════════════════════════════════
#  基础账户注册
#  优先：fscarmen 公共 API → GitLab 工具 → Cloudflare 官方 API
# ════════════════════════════════════════════════════════════════
register_base_account() {
    yellow "注册 WARP 基础账户..."
    PRIV_KEY=""; DEV_ID=""; WARP_TOKEN=""

    # 方法一：fscarmen 公共 API（最稳定）
    local RESP
    RESP=$(curl -s --retry 3 --retry-delay 1 --max-time 5 \
        "${WARP_REG_API}/?run=register" 2>/dev/null) || true

    if grep -q '"id"' <<< "$RESP"; then
        PRIV_KEY=$(awk -F'"' '/"private_key"/{print $4}' <<< "$RESP")
        DEV_ID=$(grep -m1 '"id"' <<< "$RESP" | awk -F'"' '{print $4}')
        WARP_TOKEN=$(awk -F'"' '/"token"/{print $4}' <<< "$RESP")
    fi

    # 方法二：GitLab 预编译注册工具
    if [[ -z "$PRIV_KEY" ]]; then
        local ARCH; ARCH=$(uname -m)
        [[ "$ARCH" == x86_64  ]] && ARCH=amd64
        [[ "$ARCH" == aarch64 ]] && ARCH=arm64
        local API="/tmp/warpapi_$$"
        if curl -Ls --retry 2 --connect-timeout 8 \
            "https://gitlab.com/rwkgyg/CFwarp/-/raw/main/point/cpu1/${ARCH}" \
            -o "$API" 2>/dev/null && chmod +x "$API"; then
            local OUT; OUT=$("$API" 2>/dev/null) || true
            PRIV_KEY=$(awk -F': ' '/private_key/{print $2}' <<< "$OUT")
            DEV_ID=$(awk   -F': ' '/device_id/{print $2}'  <<< "$OUT")
            WARP_TOKEN=$(awk -F': ' '/token/{print $2}'    <<< "$OUT")
        fi
        rm -f "$API"
    fi

    # 方法三：Cloudflare 官方 API 直连
    if [[ -z "$PRIV_KEY" ]]; then
        yellow "前两种方式失败，尝试 Cloudflare 官方 API..."
        local TS; TS=$(date -u +"%Y-%m-%dT%H:%M:%S.000Z")
        RESP=$(curl -s --retry 3 --connect-timeout 10 \
            -X POST "https://api.cloudflareclient.com/v0a2158/reg" \
            -H "User-Agent: okhttp/3.12.1" \
            -H "CF-Client-Version: a-6.30-2158" \
            -H "Content-Type: application/json" \
            -d "{\"key\":\"$(openssl rand -base64 32 | tr -dc 'a-zA-Z0-9' | head -c 44)\",\
\"install_id\":\"\",\"fcm_token\":\"\",\"tos\":\"${TS}\",\
\"model\":\"PC\",\"serial_number\":\"\",\"locale\":\"zh-CN\"}" 2>/dev/null) || true
        PRIV_KEY=$(grep -oP '"private_key"\s*:\s*"\K[^"]+' <<< "$RESP" || true)
        DEV_ID=$(grep -oP '"id"\s*:\s*"\K[^"]+' <<< "$RESP" | head -1 || true)
        WARP_TOKEN=$(grep -oP '"token"\s*:\s*"\K[^"]+' <<< "$RESP" || true)
    fi

    if [[ -z "$PRIV_KEY" || -z "$DEV_ID" || -z "$WARP_TOKEN" ]]; then
        red "所有注册方式均失败，请检查容器网络"; exit 1
    fi
    green "基础账户注册成功 (${DEV_ID:0:8}...)"
}

# ════════════════════════════════════════════════════════════════
#  获取 WARP+ License Key（用户提供 > 公共池）
# ════════════════════════════════════════════════════════════════
get_plus_key() {
    [[ -n "$WARP_LICENSE_KEY" ]] && green "使用用户提供的 WARP+ Key" && return 0

    yellow "尝试从公共池获取 WARP+ Key..."
    local KEY
    KEY=$(curl -sfL --retry 3 --max-time 10 \
        "${WARP_REG_API}/?run=pluskey" 2>/dev/null \
        | grep -oP '[A-Za-z0-9]{8}-[A-Za-z0-9]{8}-[A-Za-z0-9]{8}' \
        | head -1 || true)

    if [[ -n "$KEY" ]]; then
        WARP_LICENSE_KEY="$KEY"
        green "公共池获取 WARP+ Key 成功：${KEY:0:8}..."
        return 0
    fi

    yellow "公共池获取 WARP+ Key 失败，回退为免费账户"
    WARP_ACCOUNT_TYPE="free"; return 1
}

# ════════════════════════════════════════════════════════════════
#  获取 Zero Trust Token（用户提供 > 公共池）
# ════════════════════════════════════════════════════════════════
get_teams_token() {
    [[ -n "$WARP_TEAMS_TOKEN" ]] && green "使用用户提供的 Teams Token" && return 0

    yellow "尝试从公共池获取 Teams Token..."
    local TOKEN
    TOKEN=$(curl -sfL --retry 3 --max-time 15 \
        "${WARP_REG_API}/?run=register&team_token=&format=warp-go" 2>/dev/null \
        | grep -oP '(?<=Token = )\S+' | head -1 || true)

    if [[ -n "$TOKEN" ]]; then
        WARP_TEAMS_TOKEN="$TOKEN"
        green "公共池获取 Teams Token 成功"
        return 0
    fi

    yellow "公共池获取 Teams Token 失败，回退为免费账户"
    WARP_ACCOUNT_TYPE="free"; return 1
}

# ════════════════════════════════════════════════════════════════
#  写入 warp.conf（与 fscarmen 格式对齐，含 [Device] 节）
# ════════════════════════════════════════════════════════════════
write_conf() {
    local TYPE="${1:-free}"
    {
        echo "[Account]"
        echo "Device     = ${DEV_ID}"
        echo "PrivateKey = ${PRIV_KEY}"
        echo "Token      = ${WARP_TOKEN}"
        echo "Type       = ${TYPE}"
        echo ""
        echo "[Device]"
        echo "Name       = ${WARP_IF}"     # TUN 接口名，固定为 WARP
        echo "MTU        = ${MTU}"
        echo ""
        echo "[Peer]"
        echo "PublicKey  = ${WARP_PUBKEY}"
        echo "Endpoint   = ${WARP_ENDPOINT}"
        # 双栈：容器内全部流量经 WARP
        echo "AllowedIPs = 0.0.0.0/0, ::/0"
        echo "KeepAlive  = 30"
        echo ""
        echo "[Script]"
        echo "PostUp   ="
        echo "PostDown ="
    } > "$WARPGO_CONF"
    chmod 600 "$WARPGO_CONF"
}

# ════════════════════════════════════════════════════════════════
#  升级 WARP+ 账户
# ════════════════════════════════════════════════════════════════
upgrade_to_plus() {
    yellow "升级为 WARP+ (${WARP_LICENSE_KEY:0:8}...)..."
    write_conf "plus"

    "$WARPGO_BIN" --update \
        --config="$WARPGO_CONF" \
        --license="$WARP_LICENSE_KEY" \
        --device-name="$WARP_DEVICE_NAME" &>/dev/null || true

    # 快速启动验证
    "$WARPGO_BIN" --config="$WARPGO_CONF" >> "$WARPGO_LOG" 2>&1 &
    local TMP_PID=$!
    sleep 8
    local STATUS; STATUS=$(get_warp_status)
    kill -15 $TMP_PID 2>/dev/null || true; sleep 2

    if [[ "$STATUS" == "plus" ]]; then
        green "✓ WARP+ 升级成功"
        echo "$WARP_LICENSE_KEY" > /warp/data/plus_license.txt
        return 0
    fi

    red "WARP+ 升级失败（状态: ${STATUS:-无响应}）"
    red "  可能原因：密钥无效 / 绑定超 5 台 / 密钥过期"
    yellow "回退为免费账户..."
    WARP_ACCOUNT_TYPE="free"; write_conf "free"; return 1
}

# ════════════════════════════════════════════════════════════════
#  注册 Zero Trust Teams 账户
# ════════════════════════════════════════════════════════════════
register_teams() {
    yellow "注册 Zero Trust 团队账户..."
    write_conf "teams"

    local DNAME="${WARP_DEVICE_NAME}-$(date +%s | tail -c 4)"
    "$WARPGO_BIN" --register \
        --config="$WARPGO_CONF" \
        --team-config="$WARP_TEAMS_TOKEN" \
        --device-name="$DNAME" &>/dev/null || true

    "$WARPGO_BIN" --config="$WARPGO_CONF" >> "$WARPGO_LOG" 2>&1 &
    local TMP_PID=$!
    sleep 8
    local STATUS; STATUS=$(get_warp_status)
    kill -15 $TMP_PID 2>/dev/null || true; sleep 2

    if [[ $STATUS =~ on|plus ]]; then
        green "✓ Teams 账户注册成功（设备: ${DNAME}）"
        return 0
    fi

    red "Teams 注册失败（状态: ${STATUS:-无响应}）"
    yellow "回退为免费账户..."
    WARP_ACCOUNT_TYPE="free"; register_base_account; write_conf "free"; return 1
}

# ════════════════════════════════════════════════════════════════
#  账户注册总入口
# ════════════════════════════════════════════════════════════════
setup_account() {
    if [[ -s "$WARPGO_CONF" && "$FORCE_REGISTER" != "true" ]]; then
        local SAVED_TYPE; SAVED_TYPE=$(awk -F'[= ]+' '/^Type/{print $NF}' "$WARPGO_CONF" | tr -d '[:space:]')
        yellow "复用已有账户（类型: ${SAVED_TYPE:-unknown}），跳过注册"
        yellow "  换账户/换 IP：设置 FORCE_REGISTER=true 重启"
        # 更新 MTU（可能网络环境变了）
        sed -i "s/^MTU.*/MTU        = ${MTU}/" "$WARPGO_CONF"
        return
    fi

    # 注销旧设备（仅在强制重注册时）
    [[ "$FORCE_REGISTER" == "true" ]] && cancel_old_device

    register_base_account

    case "$WARP_ACCOUNT_TYPE" in
        free)
            green "账户类型：免费（无限流量）"
            write_conf "free"
            ;;
        plus)
            get_plus_key && upgrade_to_plus || write_conf "free"
            ;;
        teams)
            get_teams_token && register_teams || true
            ;;
    esac

    yellow "── warp.conf ────────────────────────────"
    cat "$WARPGO_CONF"
    yellow "─────────────────────────────────────────"
}

# ════════════════════════════════════════════════════════════════
#  单次调用获取 IP 信息（warp状态 + IP + 国家 + ISP）
#  来自 fscarmen：ip.cloudflare.nyc.mn 接口
# ════════════════════════════════════════════════════════════════
get_ip_info() {
    local STACK="${1:-4}"  # 4 或 6
    local IFACE="${2:-}"   # 可选：WARP（通过 WARP 接口查询）
    local IFACE_ARG=""
    [[ -n "$IFACE" ]] && IFACE_ARG="--interface $IFACE"

    local JSON
    JSON=$(curl -s${STACK} $IFACE_ARG --max-time 8 \
        "${WARP_IP_API}" 2>/dev/null) || true

    echo "$JSON"
}

# 从 JSON 中提取 warp 状态
get_warp_status() {
    local J4; J4=$(get_ip_info 4)
    local J6; J6=$(get_ip_info 6)
    local W4; W4=$(awk -F'"' '/"warp"/{print $4}' <<< "$J4")
    local W6; W6=$(awk -F'"' '/"warp"/{print $4}' <<< "$J6")
    [[ "$W4" == "plus" || "$W6" == "plus" ]] && echo "plus" && return
    [[ "$W4" == "on"   || "$W6" == "on"   ]] && echo "on"   && return
    echo "off"
}

# 获取出口 IP 国家（通过 WARP 接口）
get_warp_country() {
    # 优先用 cloudflare trace 的 loc 字段（通过 WARP TUN 接口）
    local LOC
    LOC=$(curl -s4 --interface "${WARP_IF}" --max-time 8 \
        https://www.cloudflare.com/cdn-cgi/trace -k 2>/dev/null \
        | awk -F= '/^loc/{print $2}' | tr -d '[:space:]' || true)
    [[ -n "$LOC" ]] && echo "$LOC" && return

    # 备用：通过 ip.cloudflare.nyc.mn WARP 接口获取
    local JSON; JSON=$(get_ip_info 4 "${WARP_IF}")
    awk -F'"' '/"country"/{print $4}' <<< "$JSON"
}

# ════════════════════════════════════════════════════════════════
#  等待 WARP TUN 接口出现
# ════════════════════════════════════════════════════════════════
wait_tun_up() {
    yellow "等待 WARP TUN 接口..."
    for i in $(seq 1 30); do
        ip link show "${WARP_IF}" &>/dev/null 2>&1 && { green "WARP 接口就绪 (${i}×2s)"; sleep 2; return 0; }
        sleep 2
    done
    red "WARP (${WARP_IF}) 接口 60s 内未出现"
    red "  warp-go 最后 30 行日志:"
    tail -30 "$WARPGO_LOG" 2>/dev/null | sed 's/^/  /' || red "  (日志为空，warp-go 可能未能启动)"
    return 1
}

# ════════════════════════════════════════════════════════════════
#  验证 WARP + 地区筛选（核心逻辑）
#  不匹配则重新注册，最多 WARP_COUNTRY_RETRY 次
# ════════════════════════════════════════════════════════════════
verify_and_filter() {
    local MAX_TRY="$WARP_COUNTRY_RETRY"
    local TRY=0
    local COUNTRIES_ARRAY
    IFS=',' read -ra COUNTRIES_ARRAY <<< "$WARP_COUNTRIES"

    while true; do
        TRY=$((TRY + 1))
        yellow "WARP 连通性验证（第 ${TRY} 次）..."

        wait_tun_up || { red "TUN 未就绪，退出"; return 1; }

        # 等待 WARP 状态变为 on/plus
        local WARP_STATE CONNECTED=false
        for i in $(seq 1 12); do
            WARP_STATE=$(get_warp_status)
            if [[ $WARP_STATE =~ on|plus ]]; then
                CONNECTED=true; break
            fi
            yellow "  等待 WARP 流量... ($i/12)"
            sleep 5
        done

        if ! $CONNECTED; then
            red "WARP 连通失败"
            kill -15 "$(pgrep warp-go 2>/dev/null)" 2>/dev/null || true; sleep 3
            "$WARPGO_BIN" --config="$WARPGO_CONF" >> "$WARPGO_LOG" 2>&1 &
            WARPGO_PID=$!
            [[ $TRY -ge $MAX_TRY ]] && { red "超出最大重试次数"; return 1; }
            continue
        fi
        green "✓ WARP 连通 [${WARP_STATE}]"

        # 无地区限制 → 直接通过
        if [[ -z "$WARP_COUNTRIES" ]]; then
            return 0
        fi

        # 获取出口地区
        local COUNTRY; COUNTRY=$(get_warp_country)
        green "当前 WARP 出口地区：${COUNTRY:-未知}"

        # 检查是否命中目标地区
        local MATCH=false
        for C in "${COUNTRIES_ARRAY[@]}"; do
            [[ "${C// /}" == "$COUNTRY" ]] && MATCH=true && break
        done

        if $MATCH; then
            green "✓ 地区命中！(${COUNTRY}) [目标: ${WARP_COUNTRIES}]"
            return 0
        fi

        if [[ $TRY -ge $MAX_TRY ]]; then
            red "达到最大重试次数 (${MAX_TRY})，接受当前地区 ${COUNTRY:-未知}"
            return 0
        fi

        yellow "地区 ${COUNTRY:-未知} 未在 [${WARP_COUNTRIES}] 中，重新注册 (${TRY}/${MAX_TRY})..."
        kill -15 "$(pgrep warp-go 2>/dev/null)" 2>/dev/null || true; sleep 3

        # 重注册（注销旧账户 → 申请新账户 → 写配置）
        cancel_old_device
        register_base_account
        write_conf "$WARP_ACCOUNT_TYPE"

        "$WARPGO_BIN" --config="$WARPGO_CONF" >> "$WARPGO_LOG" 2>&1 &
        WARPGO_PID=$!
        sleep 5
    done
}

# ════════════════════════════════════════════════════════════════
#  启动 microsocks SOCKS5
# ════════════════════════════════════════════════════════════════
start_socks5() {
    local ARGS="-i 0.0.0.0 -p ${SOCKS5_PORT}"
    if [[ -n "$SOCKS5_USER" && -n "$SOCKS5_PASS" ]]; then
        ARGS+=" -u ${SOCKS5_USER} -P ${SOCKS5_PASS}"
        green "SOCKS5 :${SOCKS5_PORT}（认证: ${SOCKS5_USER}/***）"
    else
        green "SOCKS5 :${SOCKS5_PORT}（无需认证）"
    fi
    microsocks $ARGS &
    SOCKS5_PID=$!
    sleep 1
    kill -0 "$SOCKS5_PID" 2>/dev/null || { red "microsocks 启动失败"; exit 1; }
    green "microsocks PID=$SOCKS5_PID"
}

# ════════════════════════════════════════════════════════════════
#  状态面板
# ════════════════════════════════════════════════════════════════
print_info() {
    local J4; J4=$(get_ip_info 4)
    local J6; J6=$(get_ip_info 6)
    local WAN4; WAN4=$(awk -F'"' '/"ip"/{print $4}'      <<< "$J4")
    local CTR4; CTR4=$(awk -F'"' '/"country"/{print $4}' <<< "$J4")
    local ISP4; ISP4=$(awk -F'"' '/"isp"/{print $4}'     <<< "$J4")
    local WRP4; WRP4=$(awk -F'"' '/"warp"/{print $4}'    <<< "$J4")
    local WAN6; WAN6=$(awk -F'"' '/"ip"/{print $4}'      <<< "$J6")
    local CTR6; CTR6=$(awk -F'"' '/"country"/{print $4}' <<< "$J6")
    local WRP6; WRP6=$(awk -F'"' '/"warp"/{print $4}'    <<< "$J6")

    local ACCT_TYPE; ACCT_TYPE=$(awk -F'[= ]+' '/^Type/{print $NF}' "$WARPGO_CONF" | tr -d '[:space:]' 2>/dev/null || echo "free")
    local ACCT_LABEL
    case "$ACCT_TYPE" in
        free)  ACCT_LABEL="免费（无限流量）" ;;
        plus)  ACCT_LABEL="WARP+ 付费 ($(cat /warp/data/plus_license.txt 2>/dev/null | cut -c1-8)...)" ;;
        teams) ACCT_LABEL="Zero Trust 团队" ;;
        *)     ACCT_LABEL="$ACCT_TYPE" ;;
    esac

    local COUNTRY_LABEL
    [[ -n "$WARP_COUNTRIES" ]] \
        && COUNTRY_LABEL="筛选 [${WARP_COUNTRIES}] → 当前 ${CTR4:-?}" \
        || COUNTRY_LABEL="不限地区（当前 ${CTR4:-?}）"

    echo
    blue "╔════════════════════════════════════════════════════════╗"
    blue "║              WARP SOCKS5 代理已就绪                   ║"
    blue "╠════════════════════════════════════════════════════════╣"
    blue "║  账户类型 : ${ACCT_LABEL}"
    blue "║  地区筛选 : ${COUNTRY_LABEL}"
    blue "╠════════════════════════════════════════════════════════╣"
    blue "║  IPv4 出口 : ${WAN4:-N/A}  ${CTR4:-?}  ${ISP4:-?}"
    blue "║  IPv4 WARP : ${WRP4:-off}"
    blue "║  IPv6 出口 : ${WAN6:-N/A}  ${CTR6:-?}"
    blue "║  IPv6 WARP : ${WRP6:-off}"
    blue "╠════════════════════════════════════════════════════════╣"
    blue "║  SOCKS5   : 0.0.0.0:${SOCKS5_PORT}"
    [[ -n "$SOCKS5_USER" ]] \
        && blue "║  认证     : ${SOCKS5_USER} / ${SOCKS5_PASS}" \
        || blue "║  认证     : 无"
    blue "╠════════════════════════════════════════════════════════╣"
    blue "║  验证: curl -sx socks5h://127.0.0.1:${SOCKS5_PORT} \\"
    blue "║    https://www.cloudflare.com/cdn-cgi/trace           ║"
    blue "╚════════════════════════════════════════════════════════╝"
    echo
}

# ════════════════════════════════════════════════════════════════
#  Watchdog：掉线重连 + microsocks 守护
# ════════════════════════════════════════════════════════════════
watchdog() {
    local FAIL=0 MAX_FAIL=5 INTERVAL=300 RETRY=20

    while true; do
        sleep $INTERVAL

        # warp-go 进程丢失
        if ! pgrep -x warp-go &>/dev/null; then
            yellow "[watchdog] warp-go 丢失，重启..."
            "$WARPGO_BIN" --config="$WARPGO_CONF" >> "$WARPGO_LOG" 2>&1 &
            WARPGO_PID=$!
            sleep 10
        fi

        # 连通性检测（单次 API 调用）
        local STATUS; STATUS=$(get_warp_status)

        if [[ $STATUS =~ on|plus ]]; then
            echo "[$(date '+%H:%M:%S')] [watchdog] 正常 [${STATUS}]"
            FAIL=0; INTERVAL=300
        else
            FAIL=$((FAIL+1))
            yellow "[watchdog] 掉线 (${FAIL}/${MAX_FAIL})，重启..."
            kill -15 "$(pgrep warp-go)" 2>/dev/null || true; sleep 3
            "$WARPGO_BIN" --config="$WARPGO_CONF" >> "$WARPGO_LOG" 2>&1 &
            WARPGO_PID=$!
            sleep 15

            if [[ $FAIL -ge $MAX_FAIL ]]; then
                yellow "[watchdog] 连续失败，暂停 5 分钟..."
                kill -15 "$(pgrep warp-go)" 2>/dev/null || true
                sleep 300
                "$WARPGO_BIN" --config="$WARPGO_CONF" >> "$WARPGO_LOG" 2>&1 &
                WARPGO_PID=$!
                sleep 15
                FAIL=0
            fi
            INTERVAL=$RETRY
        fi

        # microsocks 进程守护
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
    yellow "容器退出，清理资源..."
    kill -15 "$(pgrep warp-go)" 2>/dev/null || true
    [[ -n "$SOCKS5_PID" ]] && kill -15 "$SOCKS5_PID" 2>/dev/null || true
    exit 0
}
trap cleanup SIGTERM SIGINT

# ════════════════════════════════════════════════════════════════
#  主流程
# ════════════════════════════════════════════════════════════════
blue "══════════════════════════════════════════════════════"
blue "   WARP-GO SOCKS5 Proxy  [${WARP_ACCOUNT_TYPE^^}]"
[[ -n "$WARP_COUNTRIES" ]] && blue "   目标地区: ${WARP_COUNTRIES}"
blue "══════════════════════════════════════════════════════"

validate_args
check_tun
calc_mtu
setup_account

green "启动 warp-go..."
"$WARPGO_BIN" --config="$WARPGO_CONF" >> "$WARPGO_LOG" 2>&1 &
WARPGO_PID=$!

verify_and_filter
start_socks5
print_info
watchdog &

wait $WARPGO_PID
