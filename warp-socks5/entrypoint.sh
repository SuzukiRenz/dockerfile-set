#!/bin/bash
# =============================================================
#  WARP-GO + SOCKS5 容器启动脚本 v4
#
#  账户类型（WARP_ACCOUNT_TYPE）：
#    free  : 免费账户，无限流量（默认）
#    plus  : WARP+，有 key 直接用；留空则自动从公共池获取 key
#    teams : Zero Trust，有 token 直接用；留空则自动从公共池获取
#
#  地区筛选（WARP_COUNTRIES）：
#    留空  : 不限制，接受任何 WARP 出口 IP
#    单个  : "US"  → 只接受美国出口
#    多个  : "US,JP,SG" → 随机接受其中任一地区
#    每次启动自动重注册直到获得匹配地区的 IP（最多 WARP_COUNTRY_RETRY 次）
# =============================================================
set -euo pipefail

red()    { echo -e "\033[31m[WARP] $*\033[0m"; }
green()  { echo -e "\033[32m[WARP] $*\033[0m"; }
yellow() { echo -e "\033[33m[WARP] $*\033[0m"; }
blue()   { echo -e "\033[36m[WARP] $*\033[0m"; }

# ════════════════════════════════════════════════════════════════
#  环境变量配置项
# ════════════════════════════════════════════════════════════════

# ── SOCKS5 ──────────────────────────────────────────────────────
SOCKS5_PORT="${SOCKS5_PORT:-1080}"
SOCKS5_USER="${SOCKS5_USER:-}"
SOCKS5_PASS="${SOCKS5_PASS:-}"

# ── WARP 账户类型 ────────────────────────────────────────────────
#   free  | plus | teams
WARP_ACCOUNT_TYPE="${WARP_ACCOUNT_TYPE:-free}"

# ── WARP+ 许可证密钥（plus 类型时用；留空自动获取）─────────────
WARP_LICENSE_KEY="${WARP_LICENSE_KEY:-}"

# ── Zero Trust Token（teams 类型时用；留空自动获取）────────────
WARP_TEAMS_TOKEN="${WARP_TEAMS_TOKEN:-}"

# ── 地区筛选：ISO 国家代码，逗号分隔，留空不限 ─────────────────
# 例：WARP_COUNTRIES="US"  或  WARP_COUNTRIES="US,JP,SG,HK"
WARP_COUNTRIES="${WARP_COUNTRIES:-}"

# ── 地区筛选最大重试次数 ─────────────────────────────────────────
WARP_COUNTRY_RETRY="${WARP_COUNTRY_RETRY:-10}"

# ── 容器设备名 ────────────────────────────────────────────────────
WARP_DEVICE_NAME="${WARP_DEVICE_NAME:-warp-docker}"

# ── 强制重新注册 ─────────────────────────────────────────────────
FORCE_REGISTER="${FORCE_REGISTER:-false}"

# ── 路径常量 ─────────────────────────────────────────────────────
WARPGO_BIN="/usr/local/bin/warp-go"
WARPGO_CONF="/warp/data/warp.conf"
ACCOUNT_CACHE="/warp/data/account_type"
WARP_PUBKEY="bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo="
ENDPOINT_V4="162.159.192.1:2408"
ENDPOINT_V6="[2606:4700:d0::a29f:c001]:2408"

# fscarmen 公开 WARP API（免费注册 / plus key / teams token）
WARP_API="https://warp.cloudflare.now.cc"

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
    if [[ -f "$ACCOUNT_CACHE" ]]; then
        local PREV; PREV=$(cat "$ACCOUNT_CACHE")
        if [[ "$PREV" != "$WARP_ACCOUNT_TYPE" ]]; then
            yellow "账户类型 ${PREV} → ${WARP_ACCOUNT_TYPE}，强制重新注册"
            FORCE_REGISTER=true
        fi
    fi

    # 国家代码转大写
    WARP_COUNTRIES=$(echo "$WARP_COUNTRIES" | tr '[:lower:]' '[:upper:]' | tr -s ' ,;' ',')
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
        red "内核 tun 模块不可用（OpenVZ 请在服务商面板开启 TUN/TAP）"
        exit 1
    fi
    mkdir -p /dev/net
    if mknod /dev/net/tun c 10 200 2>/dev/null; then
        chmod 666 /dev/net/tun
        green "/dev/net/tun 自动创建成功"
        return 0
    fi
    red "自动创建失败，请在宿主机执行："
    red "  mkdir -p /dev/net && mknod /dev/net/tun c 10 200 && chmod 666 /dev/net/tun"
    exit 1
}

# ════════════════════════════════════════════════════════════════
#  架构检测
# ════════════════════════════════════════════════════════════════
get_arch() {
    case $(uname -m) in
        x86_64)  echo amd64 ;;
        aarch64) echo arm64 ;;
        *)       echo amd64 ;;
    esac
}

# ════════════════════════════════════════════════════════════════
#  MTU 计算
# ════════════════════════════════════════════════════════════════
calc_mtu() {
    yellow "计算最优 MTU..."
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
    [[ -s "$WARPGO_CONF" ]] && sed -i "s/^MTU.*/MTU        = ${MTU}/" "$WARPGO_CONF"
}

# ════════════════════════════════════════════════════════════════
#  基础账户注册（通用，所有类型都先调这个）
#  优先用 fscarmen warp API，次选 GitLab 工具，最后 Cloudflare 直连
# ════════════════════════════════════════════════════════════════
register_base_account() {
    yellow "注册 WARP 基础账户..."
    PRIV_KEY=""; DEV_ID=""; WARP_TOKEN=""

    # 方法一：fscarmen 公开 API（返回 warp-go 格式配置）
    local RESP
    RESP=$(curl -sfL --retry 3 --connect-timeout 10 \
        "${WARP_API}/?run=register&format=warp-go" 2>/dev/null) || true

    if [[ -n "$RESP" ]]; then
        PRIV_KEY=$(echo "$RESP"  | grep -oP '(?<=PrivateKey = )\S+' || true)
        DEV_ID=$(echo   "$RESP"  | grep -oP '(?<=Device = )\S+'     || true)
        WARP_TOKEN=$(echo "$RESP"| grep -oP '(?<=Token = )\S+'      || true)
    fi

    # 方法二：GitLab 预编译注册工具
    if [[ -z "$PRIV_KEY" ]]; then
        local ARCH; ARCH=$(get_arch)
        local API="/tmp/warpapi_$$"
        curl -Ls --retry 3 --connect-timeout 10 \
            "https://gitlab.com/rwkgyg/CFwarp/-/raw/main/point/cpu1/${ARCH}" \
            -o "$API" 2>/dev/null && chmod +x "$API" || true
        if [[ -x "$API" ]]; then
            local OUT; OUT=$("$API" 2>/dev/null) || true
            PRIV_KEY=$(echo "$OUT"  | awk -F': ' '/private_key/{print $2}')
            DEV_ID=$(echo   "$OUT"  | awk -F': ' '/device_id/{print $2}')
            WARP_TOKEN=$(echo "$OUT"| awk -F': ' '/token/{print $2}')
        fi
        rm -f "$API"
    fi

    # 方法三：Cloudflare 官方 API 直连
    if [[ -z "$PRIV_KEY" ]]; then
        yellow "前两种方式失败，改用 Cloudflare 官方 API..."
        local TS
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
        red "所有注册方式均失败，请检查容器网络"
        exit 1
    fi
    green "基础账户注册成功 (${DEV_ID:0:8}...)"
}

# ════════════════════════════════════════════════════════════════
#  获取 WARP+ License Key
#  优先用用户提供的，否则尝试从公共池获取
# ════════════════════════════════════════════════════════════════
get_plus_key() {
    if [[ -n "$WARP_LICENSE_KEY" ]]; then
        green "使用用户提供的 WARP+ Key：${WARP_LICENSE_KEY:0:8}..."
        return 0
    fi

    yellow "未提供 WARP_LICENSE_KEY，尝试从公共池获取..."

    # 尝试 fscarmen warp API pluskey 接口
    local KEY
    KEY=$(curl -sfL --retry 3 --connect-timeout 15 \
        "${WARP_API}/?run=pluskey" 2>/dev/null \
        | grep -oP '[A-Za-z0-9]{8}-[A-Za-z0-9]{8}-[A-Za-z0-9]{8}' \
        | head -1 || true)

    if [[ -n "$KEY" ]]; then
        WARP_LICENSE_KEY="$KEY"
        green "公共池获取 WARP+ Key 成功：${KEY:0:8}..."
        return 0
    fi

    yellow "公共池获取 WARP+ Key 失败，将以免费账户运行"
    WARP_ACCOUNT_TYPE="free"
    return 1
}

# ════════════════════════════════════════════════════════════════
#  获取 Zero Trust Teams Token
#  优先用用户提供的，否则尝试从公共池获取
# ════════════════════════════════════════════════════════════════
get_teams_token() {
    if [[ -n "$WARP_TEAMS_TOKEN" ]]; then
        green "使用用户提供的 Teams Token：${WARP_TEAMS_TOKEN:0:16}..."
        return 0
    fi

    yellow "未提供 WARP_TEAMS_TOKEN，尝试从公共池获取..."

    # 通过 fscarmen warp API 获取公共 teams token
    local TOKEN
    TOKEN=$(curl -sfL --retry 3 --connect-timeout 15 \
        "${WARP_API}/?run=register&team_token=&format=warp-go" 2>/dev/null \
        | grep -oP '(?<=Token = )\S+' || true)

    # 备用：直接注册 teams 账户（使用公共 Zero Trust 组织）
    if [[ -z "$TOKEN" ]]; then
        TOKEN=$(curl -sfL --retry 3 --connect-timeout 15 \
            "https://warp-token.cloudflare.now.cc/" 2>/dev/null \
            | grep -oP '[A-Za-z0-9._-]{20,}' | head -1 || true)
    fi

    if [[ -n "$TOKEN" ]]; then
        WARP_TEAMS_TOKEN="$TOKEN"
        green "公共池获取 Teams Token 成功"
        return 0
    fi

    yellow "公共池获取 Teams Token 失败，将以免费账户运行"
    WARP_ACCOUNT_TYPE="free"
    return 1
}

# ════════════════════════════════════════════════════════════════
#  写入 warp.conf（核心函数）
# ════════════════════════════════════════════════════════════════
write_conf() {
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
#  升级为 WARP+ 账户
# ════════════════════════════════════════════════════════════════
upgrade_to_plus() {
    yellow "升级为 WARP+ (${WARP_LICENSE_KEY:0:8}...)..."
    write_conf "plus"

    "$WARPGO_BIN" --update \
        --config="$WARPGO_CONF" \
        --license="$WARP_LICENSE_KEY" \
        --device-name="$WARP_DEVICE_NAME" &>/dev/null || true

    # 验证升级是否成功
    "$WARPGO_BIN" --config="$WARPGO_CONF" &
    local TMP=$!
    sleep 8
    local STATUS
    STATUS=$(curl -s4m10 https://www.cloudflare.com/cdn-cgi/trace -k 2>/dev/null \
             | grep '^warp=' | cut -d= -f2 || true)
    kill -15 $TMP 2>/dev/null || true; sleep 2

    if [[ "$STATUS" == "plus" ]]; then
        green "✓ WARP+ 升级成功"
        echo "$WARP_LICENSE_KEY" > /warp/data/plus_license.txt
        return 0
    fi

    red "WARP+ 升级失败（当前状态: ${STATUS:-无响应}）"
    red "  可能原因：密钥无效、绑定设备超 5 台、密钥已过期"
    yellow "回退为免费账户..."
    WARP_ACCOUNT_TYPE="free"
    write_conf "free"
    return 1
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

    "$WARPGO_BIN" --config="$WARPGO_CONF" &
    local TMP=$!
    sleep 8
    local STATUS
    STATUS=$(curl -s4m10 https://www.cloudflare.com/cdn-cgi/trace -k 2>/dev/null \
             | grep '^warp=' | cut -d= -f2 || true)
    kill -15 $TMP 2>/dev/null || true; sleep 2

    if [[ $STATUS =~ on|plus ]]; then
        green "✓ Zero Trust 团队账户注册成功（设备: ${DNAME}）"
        return 0
    fi

    red "Teams 注册失败（状态: ${STATUS:-无响应}）"
    yellow "回退为免费账户..."
    WARP_ACCOUNT_TYPE="free"
    register_base_account
    write_conf "free"
    return 1
}

# ════════════════════════════════════════════════════════════════
#  账户注册总入口
# ════════════════════════════════════════════════════════════════
setup_account() {
    # 已有配置且不强制重注册 → 跳过
    if [[ -s "$WARPGO_CONF" && "$FORCE_REGISTER" != "true" ]]; then
        local SAVED; SAVED=$(cat "$ACCOUNT_CACHE" 2>/dev/null || echo "free")
        yellow "复用已有账户（类型: ${SAVED}），跳过注册"
        yellow "  换账户/换 IP 请设置 FORCE_REGISTER=true 并重启"
        return
    fi

    register_base_account   # 所有类型都先拿到基础凭证

    case "$WARP_ACCOUNT_TYPE" in
        free)
            green "账户类型：免费（无限流量）"
            write_conf "free"
            ;;
        plus)
            get_plus_key || true
            if [[ "$WARP_ACCOUNT_TYPE" == "plus" ]]; then
                upgrade_to_plus || true
            fi
            # 如果已回退 free，write_conf 已在上面写好
            [[ "$WARP_ACCOUNT_TYPE" == "free" ]] && : || sed -i 's/^Type.*/Type       = plus/' "$WARPGO_CONF"
            ;;
        teams)
            get_teams_token || true
            if [[ "$WARP_ACCOUNT_TYPE" == "teams" ]]; then
                register_teams || true
            fi
            ;;
    esac

    echo "$WARP_ACCOUNT_TYPE" > "$ACCOUNT_CACHE"

    green "warp.conf 就绪"
    yellow "── warp.conf ────────────────────────────"
    cat "$WARPGO_CONF"
    yellow "─────────────────────────────────────────"
}

# ════════════════════════════════════════════════════════════════
#  等待 WARP TUN 接口就绪
# ════════════════════════════════════════════════════════════════
wait_tun_up() {
    yellow "等待 WARP TUN 接口..."
    for i in $(seq 1 30); do
        ip link show WARP &>/dev/null 2>&1 && { green "WARP 接口就绪"; sleep 2; return 0; }
        sleep 2
    done
    red "WARP 接口 60s 内未出现"
    return 1
}

# ════════════════════════════════════════════════════════════════
#  获取当前出口 IP 的国家代码
# ════════════════════════════════════════════════════════════════
get_ip_country() {
    local IP COUNTRY
    # 优先取 IPv4 出口，次取 IPv6
    IP=$(curl -s4m8 https://icanhazip.com -k 2>/dev/null | tr -d '[:space:]' || true)
    [[ -z "$IP" ]] && IP=$(curl -s6m8 https://icanhazip.com -k 2>/dev/null | tr -d '[:space:]' || true)
    [[ -z "$IP" ]] && echo "" && return

    COUNTRY=$(curl -sfm8 "http://ip-api.com/json/${IP}?fields=countryCode" 2>/dev/null \
              | grep -oP '"countryCode"\s*:\s*"\K[^"]+' || true)
    echo "${COUNTRY:-}"
}

# ════════════════════════════════════════════════════════════════
#  验证 WARP 连通性 + 地区筛选
#  如果设置了 WARP_COUNTRIES，不匹配则重新注册，最多重试 N 次
# ════════════════════════════════════════════════════════════════
verify_and_filter() {
    local MAX_TRY="${WARP_COUNTRY_RETRY}"
    local TRY=0
    local COUNTRIES_LIST  # 将逗号分隔转为数组
    IFS=',' read -ra COUNTRIES_LIST <<< "$WARP_COUNTRIES"

    while true; do
        TRY=$((TRY+1))
        yellow "验证 WARP 连通性（第 ${TRY} 次）..."

        # 等待接口
        wait_tun_up || { red "TUN 接口未就绪"; return 1; }

        # 检查 warp 状态
        local wv4 wv6
        local CONNECTED=false
        for i in $(seq 1 10); do
            wv4=$(curl -s4m10 https://www.cloudflare.com/cdn-cgi/trace -k 2>/dev/null \
                  | grep '^warp=' | cut -d= -f2 || true)
            wv6=$(curl -s6m10 https://www.cloudflare.com/cdn-cgi/trace -k 2>/dev/null \
                  | grep '^warp=' | cut -d= -f2 || true)
            if [[ $wv4 =~ on|plus || $wv6 =~ on|plus ]]; then
                CONNECTED=true; break
            fi
            yellow "  等待 WARP 流量... ($i/10)"
            sleep 5
        done

        if ! $CONNECTED; then
            red "WARP 连通性验证失败"
            # 重启 warp-go 再试
            kill -15 "$(pgrep warp-go)" 2>/dev/null || true; sleep 3
            "$WARPGO_BIN" --config="$WARPGO_CONF" &
            WARPGO_PID=$!
            [[ $TRY -ge $MAX_TRY ]] && { red "超过最大重试次数，放弃"; return 1; }
            continue
        fi

        # 无地区限制 → 直接通过
        if [[ -z "$WARP_COUNTRIES" ]]; then
            green "✓ WARP 连通 (v4=${wv4:-N/A} v6=${wv6:-N/A})"
            return 0
        fi

        # 获取出口 IP 地区
        local COUNTRY
        COUNTRY=$(get_ip_country)
        green "当前出口地区：${COUNTRY:-未知}"

        # 检查是否匹配目标地区
        local MATCH=false
        for C in "${COUNTRIES_LIST[@]}"; do
            [[ "${C// /}" == "$COUNTRY" ]] && MATCH=true && break
        done

        if $MATCH; then
            green "✓ 地区匹配！(${COUNTRY}) WARP=${wv4:-N/A}/${wv6:-N/A}"
            return 0
        fi

        # 不匹配：停止 warp-go，重新注册账户，再试
        yellow "地区 ${COUNTRY:-未知} 不在目标 [${WARP_COUNTRIES}]，重新注册获取新 IP（${TRY}/${MAX_TRY}）"
        kill -15 "$(pgrep warp-go)" 2>/dev/null || true
        sleep 3

        if [[ $TRY -ge $MAX_TRY ]]; then
            red "达到最大重试次数 (${MAX_TRY})，接受当前地区 ${COUNTRY:-未知}"
            return 0
        fi

        # 强制重新注册（不影响账户类型，只换 IP）
        FORCE_REGISTER=true
        setup_account
        FORCE_REGISTER=false

        "$WARPGO_BIN" --config="$WARPGO_CONF" &
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
    local v4 v6 wv4 wv6 country
    v4=$(curl  -s4m8 https://icanhazip.com -k 2>/dev/null | tr -d '[:space:]' || echo "N/A")
    v6=$(curl  -s6m8 https://icanhazip.com -k 2>/dev/null | tr -d '[:space:]' || echo "N/A")
    wv4=$(curl -s4m8 https://www.cloudflare.com/cdn-cgi/trace -k 2>/dev/null | grep '^warp=' | cut -d= -f2 || echo "N/A")
    wv6=$(curl -s6m8 https://www.cloudflare.com/cdn-cgi/trace -k 2>/dev/null | grep '^warp=' | cut -d= -f2 || echo "N/A")
    country=$(get_ip_country)

    local ACCT_LABEL
    case "$(cat "$ACCOUNT_CACHE" 2>/dev/null || echo free)" in
        free)  ACCT_LABEL="免费账户（无限流量）" ;;
        plus)  ACCT_LABEL="WARP+ 付费账户 ($(cat /warp/data/plus_license.txt 2>/dev/null | cut -c1-8)...)" ;;
        teams) ACCT_LABEL="Zero Trust 团队账户" ;;
    esac

    local COUNTRY_LABEL
    [[ -n "$WARP_COUNTRIES" ]] && COUNTRY_LABEL="筛选 [${WARP_COUNTRIES}] → 当前 ${country:-?}" \
                                 || COUNTRY_LABEL="不限地区（当前 ${country:-?}）"

    echo
    blue "╔═══════════════════════════════════════════════════════╗"
    blue "║              WARP SOCKS5 代理已就绪                  ║"
    blue "╠═══════════════════════════════════════════════════════╣"
    blue "║  账户类型 : ${ACCT_LABEL}"
    blue "║  地区筛选 : ${COUNTRY_LABEL}"
    blue "╠═══════════════════════════════════════════════════════╣"
    blue "║  出口 IPv4 : ${v4}  [warp=${wv4}]"
    blue "║  出口 IPv6 : ${v6}  [warp=${wv6}]"
    blue "╠═══════════════════════════════════════════════════════╣"
    blue "║  SOCKS5   : 0.0.0.0:${SOCKS5_PORT}"
    [[ -n "$SOCKS5_USER" ]] \
        && blue "║  认证     : ${SOCKS5_USER} / ${SOCKS5_PASS}" \
        || blue "║  认证     : 无"
    blue "╠═══════════════════════════════════════════════════════╣"
    blue "║  验证: curl -sx socks5h://127.0.0.1:${SOCKS5_PORT} \\"
    blue "║    https://www.cloudflare.com/cdn-cgi/trace          ║"
    blue "╚═══════════════════════════════════════════════════════╝"
    echo
}

# ════════════════════════════════════════════════════════════════
#  Watchdog：掉线自动重连 + SOCKS5 守护
# ════════════════════════════════════════════════════════════════
watchdog() {
    local FAIL=0 MAX_FAIL=5 INTERVAL=300 RETRY=20

    while true; do
        sleep $INTERVAL

        # warp-go 进程检查
        if ! pgrep -x warp-go &>/dev/null; then
            yellow "[watchdog] warp-go 丢失，重启..."
            "$WARPGO_BIN" --config="$WARPGO_CONF" &
            WARPGO_PID=$!; sleep 10
        fi

        # 连通性检查
        local wv4 wv6
        wv4=$(curl -s4m10 https://www.cloudflare.com/cdn-cgi/trace -k 2>/dev/null \
              | grep '^warp=' | cut -d= -f2 || true)
        wv6=$(curl -s6m10 https://www.cloudflare.com/cdn-cgi/trace -k 2>/dev/null \
              | grep '^warp=' | cut -d= -f2 || true)

        if [[ $wv4 =~ on|plus || $wv6 =~ on|plus ]]; then
            echo "[$(date '+%H:%M:%S')] [watchdog] 正常 (v4=${wv4:-N/A} v6=${wv6:-N/A})"
            FAIL=0; INTERVAL=300
        else
            FAIL=$((FAIL+1))
            yellow "[watchdog] 掉线（第 ${FAIL}/${MAX_FAIL}），重启..."
            kill -15 "$(pgrep warp-go)" 2>/dev/null || true; sleep 3
            "$WARPGO_BIN" --config="$WARPGO_CONF" &; WARPGO_PID=$!; sleep 15

            if [[ $FAIL -ge $MAX_FAIL ]]; then
                yellow "[watchdog] 连续失败，暂停 5 分钟..."
                kill -15 "$(pgrep warp-go)" 2>/dev/null || true
                sleep 300
                "$WARPGO_BIN" --config="$WARPGO_CONF" &; WARPGO_PID=$!; sleep 15; FAIL=0
            fi
            INTERVAL=$RETRY
        fi

        # microsocks 进程检查
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
blue "══════════════════════════════════════════════════════"
blue "   WARP-GO SOCKS5 Proxy  [${WARP_ACCOUNT_TYPE}]"
[[ -n "$WARP_COUNTRIES" ]] && blue "   目标地区: ${WARP_COUNTRIES}"
blue "══════════════════════════════════════════════════════"

validate_args
check_tun
calc_mtu
setup_account

green "启动 warp-go..."
"$WARPGO_BIN" --config="$WARPGO_CONF" &
WARPGO_PID=$!

verify_and_filter
start_socks5
print_info

watchdog &

wait $WARPGO_PID
