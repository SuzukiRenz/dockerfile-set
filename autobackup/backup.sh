#!/bin/bash
# =============================================================================
# backup.sh — 自动打包备份脚本
# 支持多目录打包（合并/单独/逐子目录）、循环保留、加密、通用 rclone 远程上传
# =============================================================================

set -euo pipefail

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
CYAN='\033[0;36m'; NC='\033[0m'

log_info()    { echo -e "${GREEN}[INFO]${NC}  $(date '+%Y-%m-%d %H:%M:%S') $*"; }
log_warn()    { echo -e "${YELLOW}[WARN]${NC}  $(date '+%Y-%m-%d %H:%M:%S') $*"; }
log_error()   { echo -e "${RED}[ERROR]${NC} $(date '+%Y-%m-%d %H:%M:%S') $*"; }
log_section() { echo -e "\n${CYAN}══════════════════════════════════════${NC}";
                echo -e "${CYAN}  $*${NC}";
                echo -e "${CYAN}══════════════════════════════════════${NC}"; }

# ── 环境变量 ──────────────────────────────────────────────────────────────────
BACKUP_DIRS="${BACKUP_DIRS:-/data}"
BACKUP_DEST="${BACKUP_DEST:-/backup}"
BACKUP_PREFIX="${BACKUP_PREFIX:-backup}"
BACKUP_RETENTION="${BACKUP_RETENTION:-7}"
BACKUP_COMPRESS="${BACKUP_COMPRESS:-gz}"
BACKUP_TIMESTAMP="${BACKUP_TIMESTAMP:-%Y%m%d_%H%M%S}"
BACKUP_SEPARATE="${BACKUP_SEPARATE:-false}"

# 加密配置
BACKUP_PASSWORD="${BACKUP_PASSWORD:-}"                  # 加密密码，空=不加密
BACKUP_ENCRYPT_METHOD="${BACKUP_ENCRYPT_METHOD:-gpg}"   # 加密方式: gpg | openssl

REMOTE_TYPE="${REMOTE_TYPE:-disabled}"
REMOTE_RETENTION="${REMOTE_RETENTION:-7}"

WEBDAV_URL="${WEBDAV_URL:-}"
WEBDAV_USER="${WEBDAV_USER:-}"
WEBDAV_PASS="${WEBDAV_PASS:-}"
WEBDAV_PATH="${WEBDAV_PATH:-/backups}"
WEBDAV_VENDOR="${WEBDAV_VENDOR:-other}"

S3_ENDPOINT="${S3_ENDPOINT:-}"
S3_ACCESS_KEY="${S3_ACCESS_KEY:-}"
S3_SECRET_KEY="${S3_SECRET_KEY:-}"
S3_BUCKET="${S3_BUCKET:-}"
S3_PATH="${S3_PATH:-backups}"
S3_REGION="${S3_REGION:-us-east-1}"
S3_STORAGE_CLASS="${S3_STORAGE_CLASS:-STANDARD}"
S3_PROVIDER="${S3_PROVIDER:-Other}"

# 自定义 rclone 远程（REMOTE_TYPE=custom 时生效）
# 支持任意 rclone 后端: sftp / ftp / smb / onedrive / gdrive / 本地路径等
# 配置来源二选一（文件优先）:
#   RCLONE_CUSTOM_CONF_FILE — 挂载进容器的 rclone 配置文件路径
#   RCLONE_CUSTOM_CONF      — 多行 rclone 配置内容（含 [remote] 段）
RCLONE_CUSTOM_CONF_FILE="${RCLONE_CUSTOM_CONF_FILE:-}"
RCLONE_CUSTOM_CONF="${RCLONE_CUSTOM_CONF:-}"
RCLONE_CUSTOM_REMOTE="${RCLONE_CUSTOM_REMOTE:-}"
RCLONE_CUSTOM_PATH="${RCLONE_CUSTOM_PATH:-backups}"

NOTIFY_WEBHOOK="${NOTIFY_WEBHOOK:-}"
NOTIFY_ON_SUCCESS="${NOTIFY_ON_SUCCESS:-false}"
NOTIFY_ON_FAILURE="${NOTIFY_ON_FAILURE:-true}"

TIMESTAMP=$(date +"${BACKUP_TIMESTAMP}")
BACKUP_ERRORS=0
CREATED_FILES=()
CREATED_GROUPS=()      # 轮转分组前缀（去重），用于多前缀文件独立轮转
RCLONE_REMOTE="backup_remote"
RCLONE_CONFIG="/tmp/rclone-backup.conf"

# ── 工具检查 ──────────────────────────────────────────────────────────────────
check_tools() {
    local tools=("tar")
    case "${BACKUP_COMPRESS}" in
        gz)  tools+=("gzip") ;;
        bz2) tools+=("bzip2") ;;
        xz)  tools+=("xz") ;;
        zst) tools+=("zstd") ;;
    esac
    if [[ -n "${BACKUP_PASSWORD}" ]]; then
        case "${BACKUP_ENCRYPT_METHOD}" in
            gpg)     tools+=("gpg") ;;
            openssl) tools+=("openssl") ;;
        esac
    fi
    [[ "${REMOTE_TYPE}" != "disabled" ]] && tools+=("rclone")
    for t in "${tools[@]}"; do
        if ! command -v "$t" &>/dev/null; then
            log_error "缺少必要工具: $t"; exit 1
        fi
    done
}

get_compress_opts() {
    case "${BACKUP_COMPRESS}" in
        gz)  echo "-z .tar.gz" ;;
        bz2) echo "-j .tar.bz2" ;;
        xz)  echo "-J .tar.xz" ;;
        zst) echo "--zstd .tar.zst" ;;
        *)   echo "-z .tar.gz" ;;
    esac
}

send_notify() {
    local status="$1" msg="$2"
    [[ -z "${NOTIFY_WEBHOOK}" ]] && return 0
    [[ "${status}" == "success" && "${NOTIFY_ON_SUCCESS}" != "true" ]] && return 0
    [[ "${status}" == "failure" && "${NOTIFY_ON_FAILURE}" != "true" ]] && return 0
    local icon="✅"; [[ "${status}" == "failure" ]] && icon="❌"
    curl -sS -X POST "${NOTIFY_WEBHOOK}" \
        -H 'Content-Type: application/json' \
        -d "{\"msg_type\":\"text\",\"content\":{\"text\":\"${icon} [Backup] ${msg}\"}}" \
        >/dev/null 2>&1 || log_warn "Webhook 通知发送失败"
}

# ── 加密函数 ──────────────────────────────────────────────────────────────────
encrypt_file() {
    local src="$1"          # 原始压缩包路径
    local encrypted=""

    case "${BACKUP_ENCRYPT_METHOD}" in
        gpg)
            encrypted="${src}.gpg"
            log_info "GPG 加密: $(basename "$src") → $(basename "$encrypted")"
            gpg --batch \
                --yes \
                --passphrase "${BACKUP_PASSWORD}" \
                --symmetric \
                --cipher-algo AES256 \
                --output "${encrypted}" \
                "${src}"
            ;;
        openssl)
            encrypted="${src}.enc"
            log_info "OpenSSL 加密: $(basename "$src") → $(basename "$encrypted")"
            openssl enc -aes-256-cbc \
                -salt \
                -pbkdf2 \
                -iter 100000 \
                -pass "pass:${BACKUP_PASSWORD}" \
                -in "${src}" \
                -out "${encrypted}"
            ;;
    esac

    if [[ -f "${encrypted}" ]]; then
        local orig_size; orig_size=$(du -sh "$src" | cut -f1)
        local enc_size;  enc_size=$(du -sh "$encrypted" | cut -f1)
        log_info "加密完成: ${orig_size} → ${enc_size}"
        # 删除未加密的原文件，只保留加密版
        rm -f "${src}"
        echo "${encrypted}"
    else
        log_error "加密失败: ${src}"
        ((BACKUP_ERRORS++))
        echo "${src}"   # 返回原文件，不中断流程
    fi
}

# 解密命令提示（打印到日志，方便用户知道怎么解密）
print_decrypt_hint() {
    local file="$1"
    case "${BACKUP_ENCRYPT_METHOD}" in
        gpg)
            log_info "解密命令: gpg --batch --passphrase '<密码>' --decrypt $(basename "$file") > 原始文件.tar.gz"
            ;;
        openssl)
            log_info "解密命令: openssl enc -d -aes-256-cbc -pbkdf2 -iter 100000 -pass 'pass:<密码>' -in $(basename "$file") -out 原始文件.tar.gz"
            ;;
    esac
}

# ── rclone 配置 ───────────────────────────────────────────────────────────────
setup_rclone() {
    case "${REMOTE_TYPE}" in
        webdav)
            cat > "${RCLONE_CONFIG}" << EOF
[${RCLONE_REMOTE}]
type = webdav
url = ${WEBDAV_URL}
vendor = ${WEBDAV_VENDOR}
user = ${WEBDAV_USER}
pass = $(rclone obscure "${WEBDAV_PASS}")
EOF
            ;;
        s3)
            local ep_line=""
            [[ -n "${S3_ENDPOINT}" ]] && ep_line="endpoint = ${S3_ENDPOINT}"
            cat > "${RCLONE_CONFIG}" << EOF
[${RCLONE_REMOTE}]
type = s3
provider = ${S3_PROVIDER}
access_key_id = ${S3_ACCESS_KEY}
secret_access_key = ${S3_SECRET_KEY}
region = ${S3_REGION}
${ep_line}
storage_class = ${S3_STORAGE_CLASS}
EOF
            ;;
        custom)
            # 通用模式：直接透传任意 rclone 后端配置（sftp/ftp/smb/onedrive/gdrive/本地路径等）
            if [[ -n "${RCLONE_CUSTOM_CONF_FILE}" && -f "${RCLONE_CUSTOM_CONF_FILE}" ]]; then
                cp "${RCLONE_CUSTOM_CONF_FILE}" "${RCLONE_CONFIG}"
                log_info "使用自定义 rclone 配置: ${RCLONE_CUSTOM_CONF_FILE}"
            elif [[ -n "${RCLONE_CUSTOM_CONF}" ]]; then
                printf '%s\n' "${RCLONE_CUSTOM_CONF}" > "${RCLONE_CONFIG}"
                log_info "使用自定义 rclone 配置 (来自 RCLONE_CUSTOM_CONF)"
            else
                log_error "custom 模式需要 RCLONE_CUSTOM_CONF_FILE 或 RCLONE_CUSTOM_CONF"
                exit 1
            fi
            ;;
    esac
    chmod 600 "${RCLONE_CONFIG}"
    log_info "rclone 配置已生成 [${REMOTE_TYPE}]"
    if [[ "${REMOTE_TYPE}" == "custom" ]]; then
        log_info "远程 remote: ${RCLONE_CUSTOM_REMOTE} | 路径: ${RCLONE_CUSTOM_PATH}"
    fi
}

rclone_cmd() { rclone --config "${RCLONE_CONFIG}" "$@"; }

get_remote_path() {
    case "${REMOTE_TYPE}" in
        webdav) echo "${RCLONE_REMOTE}:${WEBDAV_PATH}" ;;
        s3)     echo "${RCLONE_REMOTE}:${S3_BUCKET}/${S3_PATH}" ;;
        custom) echo "${RCLONE_CUSTOM_REMOTE}:${RCLONE_CUSTOM_PATH}" ;;
    esac
}

remote_upload() {
    local file="$1"
    local filename; filename=$(basename "$file")
    local remote_path; remote_path=$(get_remote_path)
    log_info "上传 [${REMOTE_TYPE}]: ${filename} → ${remote_path}/"
    if rclone_cmd copy "$file" "${remote_path}/" \
        --transfers 1 --retries 3 --retries-sleep 5s 2>&1 | tail -2; then
        log_info "远程上传成功: ${filename}"
    else
        log_error "远程上传失败: ${filename}"
        ((BACKUP_ERRORS++)); return 1
    fi
}

remote_rotate() {
    local prefix="$1"
    local remote_path; remote_path=$(get_remote_path)
    log_info "远程轮转 (保留最新 ${REMOTE_RETENTION} 份，前缀: ${prefix})"
    # 转义正则特殊字符，避免前缀含 . [] 等字符时匹配异常；时间戳锚定避免误匹配其他组
    local escaped_prefix; escaped_prefix=$(printf '%s' "$prefix" | sed 's/[][\.^$*+?(){|}]/\\&/g')
    local ts_regex; ts_regex=$(timestamp_to_regex "${BACKUP_TIMESTAMP}")
    local files=()
    while IFS= read -r line; do
        [[ -n "$line" ]] && files+=("$line")
    done < <(rclone_cmd lsf "${remote_path}/" --files-only 2>/dev/null | grep -E "^${escaped_prefix}_${ts_regex}" | sort)
    local total=${#files[@]}
    local to_delete=$(( total - REMOTE_RETENTION ))
    if (( to_delete > 0 )); then
        for (( i=0; i<to_delete; i++ )); do
            log_warn "远程删除旧备份: ${files[$i]}"
            rclone_cmd delete "${remote_path}/${files[$i]}" 2>/dev/null || true
        done
        log_info "远程已清理 ${to_delete} 个旧备份"
    else
        log_info "远程备份数量 (${total}) 未超限，无需清理"
    fi
}

validate_remote_config() {
    case "${REMOTE_TYPE}" in
        webdav)
            # 注意: 用 if 而非 [[...]] && { exit; } —— 后者在条件为假时返回码为 1，
            # 会触发 set -e 导致脚本静默退出（原项目潜伏 bug，配置齐全时同样中招）
            if [[ -z "${WEBDAV_URL}" || -z "${WEBDAV_USER}" || -z "${WEBDAV_PASS}" ]]; then
                log_error "WebDAV 配置不完整: WEBDAV_URL / WEBDAV_USER / WEBDAV_PASS"; exit 1
            fi
            ;;
        s3)
            if [[ -z "${S3_ACCESS_KEY}" || -z "${S3_SECRET_KEY}" || -z "${S3_BUCKET}" ]]; then
                log_error "S3 配置不完整: S3_ACCESS_KEY / S3_SECRET_KEY / S3_BUCKET"; exit 1
            fi
            ;;
        custom)
            if [[ -z "${RCLONE_CUSTOM_REMOTE}" || -z "${RCLONE_CUSTOM_PATH}" ]]; then
                log_error "custom 配置不完整: RCLONE_CUSTOM_REMOTE / RCLONE_CUSTOM_PATH"; exit 1
            fi
            if [[ -z "${RCLONE_CUSTOM_CONF}" && -z "${RCLONE_CUSTOM_CONF_FILE}" ]]; then
                log_error "custom 需要提供 RCLONE_CUSTOM_CONF_FILE 或 RCLONE_CUSTOM_CONF"; exit 1
            fi
            ;;
        disabled) ;;
        *) log_error "未知 REMOTE_TYPE: ${REMOTE_TYPE}"; exit 1 ;;
    esac
}

# ── 打包 ──────────────────────────────────────────────────────────────────────
do_backup() {
    local dirs_to_pack=("$@")
    local opts; opts=$(get_compress_opts)
    local tar_flag; tar_flag=$(echo "${opts}" | awk '{print $1}')
    local ext; ext=$(echo "${opts}" | awk '{print $2}')
    local archive_name="${BACKUP_PREFIX}_${TIMESTAMP}${ext}"
    local archive_path="${BACKUP_DEST}/${archive_name}"

    log_info "正在打包: ${dirs_to_pack[*]}"
    log_info "目标文件: ${archive_path}"

    local valid_dirs=()
    for d in "${dirs_to_pack[@]}"; do
        if [[ -e "$d" ]]; then valid_dirs+=("$d")
        else log_warn "目录不存在，跳过: $d"; fi
    done
    if [[ ${#valid_dirs[@]} -eq 0 ]]; then
        log_error "没有有效的备份目录"; ((BACKUP_ERRORS++)); return 1
    fi

    if tar "${tar_flag}" -cf "${archive_path}" "${valid_dirs[@]}" 2>/dev/null; then
        local size; size=$(du -sh "${archive_path}" | cut -f1)
        log_info "打包完成: ${archive_name} (${size})"

        # 加密处理
        local final_path="${archive_path}"
        if [[ -n "${BACKUP_PASSWORD}" ]]; then
            final_path=$(encrypt_file "${archive_path}")
            print_decrypt_hint "${final_path}"
        fi

        CREATED_FILES+=("${final_path}")
        # 记录轮转分组前缀（去重）——多前缀模式（true/children）下各前缀独立轮转
        # 注意: 用 if 避免 [[ ]] && arr+=() 作为分支最后语句时返回非零导致 set -e 误退
        local found=0 g
        for g in "${CREATED_GROUPS[@]}"; do [[ "$g" == "${BACKUP_PREFIX}" ]] && found=1; done
        if [[ ${found} -eq 0 ]]; then CREATED_GROUPS+=("${BACKUP_PREFIX}"); fi
    else
        log_error "打包失败: ${archive_name}"; ((BACKUP_ERRORS++)); return 1
    fi
}

# ── 逐子目录打包 ───────────────────────────────────────────────────────────────
# 对给定源路径的直接子文件夹逐个单独打包（如 compose 路径下每个项目文件夹一份）
# 第 3 个参数为可选的跨目录冲突子目录名列表（空格分隔），冲突时前缀附加父目录名
backup_children() {
    local dir="$1" orig_prefix="$2" conflicted_names="${3:-}"
    local children=()
    while IFS= read -r -d '' c; do
        [[ -d "$c" ]] && children+=("$c")
    done < <(find "$dir" -mindepth 1 -maxdepth 1 -type d -print0 2>/dev/null | sort -z)

    if [[ ${#children[@]} -eq 0 ]]; then
        log_warn "目录下没有子目录，直接打包该目录本身: ${dir}"
        BACKUP_PREFIX="${orig_prefix}_$(basename "$dir")"
        do_backup "$dir"
        BACKUP_PREFIX="${orig_prefix}"
        return 0
    fi

    local parent_name; parent_name=$(basename "$dir")
    local c base
    for c in "${children[@]}"; do
        base=$(basename "$c")
        if [[ " ${conflicted_names} " == *" ${base} "* ]]; then
            BACKUP_PREFIX="${orig_prefix}_${parent_name}_${base}"
        else
            BACKUP_PREFIX="${orig_prefix}_${base}"
        fi
        do_backup "$c"
    done
    BACKUP_PREFIX="${orig_prefix}"
}

# ── 本地轮转（兼容加密后缀）────────────────────────────────────────────────
# ── 时间戳格式转换 ─────────────────────────────────────────────────────────────
# 把 strftime 格式转成 find -name 可用的 glob（锚定时间戳，避免前缀较短的组
# 误匹配其他组文件，例如合并组 Oracle_ARM_backup_* 不应匹配 children 组的
# Oracle_ARM_backup_nanobot_*）
timestamp_to_glob() {
    local ts="$1" out="" ch code
    local i=0
    while (( i < ${#ts} )); do
        ch="${ts:i:1}"
        if [[ "$ch" == "%" && $((i+1)) -lt ${#ts} ]]; then
            code="${ts:i+1:1}"
            case "$code" in
                Y) out+="[0-9][0-9][0-9][0-9]" ;;
                y|m|d|H|M|S|u|w) out+="[0-9][0-9]" ;;
                j) out+="[0-9][0-9][0-9]" ;;
                e) out+="[0-9]?[0-9]" ;;
                *) out+="%${code}" ;;
            esac
            i=$((i+2))
        elif [[ "$ch" == '\\' || "$ch" == '[' || "$ch" == ']' || "$ch" == '*' || "$ch" == '?' ]]; then
            out+="\\${ch}"; i=$((i+1))
        else
            out+="$ch"; i=$((i+1))
        fi
    done
    echo "$out"
}

# strftime 格式转正则（远程轮转用）
timestamp_to_regex() {
    local ts="$1" out="" ch code
    local i=0
    while (( i < ${#ts} )); do
        ch="${ts:i:1}"
        if [[ "$ch" == "%" && $((i+1)) -lt ${#ts} ]]; then
            code="${ts:i+1:1}"
            case "$code" in
                Y) out+="[0-9]{4}" ;;
                y|m|d|H|M|S|u|w) out+="[0-9]{2}" ;;
                j) out+="[0-9]{3}" ;;
                e) out+="[0-9]?[0-9]" ;;
                *) out+="%${code}" ;;
            esac
            i=$((i+2))
        elif [[ "$ch" =~ [\\.\\^\\$\\*\\+\\?\\(\\)\\[\\]\\{\\}\\|] ]]; then
            out+="\\${ch}"; i=$((i+1))
        else
            out+="$ch"; i=$((i+1))
        fi
    done
    echo "$out"
}

rotate_local() {
    local prefix="$1"
    local ts_glob; ts_glob=$(timestamp_to_glob "${BACKUP_TIMESTAMP}")
    log_info "本地轮转 (保留最新 ${BACKUP_RETENTION} 份，前缀: ${prefix})"
    local files=()
    while IFS= read -r -d $'\0' f; do files+=("$f"); done \
        < <(find "${BACKUP_DEST}" -maxdepth 1 -name "${prefix}_${ts_glob}*" -type f -print0 | sort -z)
    local total=${#files[@]}
    local to_delete=$(( total - BACKUP_RETENTION ))
    if (( to_delete > 0 )); then
        for (( i=0; i<to_delete; i++ )); do
            log_warn "删除旧备份: ${files[$i]}"; rm -f "${files[$i]}"
        done
        log_info "本地已清理 ${to_delete} 个旧备份"
    else
        log_info "本地备份数量 (${total}) 未超限，无需清理"
    fi
}

cleanup() { [[ -f "${RCLONE_CONFIG}" ]] && rm -f "${RCLONE_CONFIG}"; }
trap cleanup EXIT

# ── 主流程 ────────────────────────────────────────────────────────────────────
main() {
    log_section "备份任务开始"
    local encrypt_status="关闭"
    [[ -n "${BACKUP_PASSWORD}" ]] && encrypt_status="${BACKUP_ENCRYPT_METHOD} 加密"
    log_info "前缀: ${BACKUP_PREFIX} | 时间戳: ${TIMESTAMP} | 压缩: ${BACKUP_COMPRESS} | 加密: ${encrypt_status} | 远程: ${REMOTE_TYPE}"

    check_tools
    validate_remote_config
    mkdir -p "${BACKUP_DEST}"
    if [[ "${REMOTE_TYPE}" != "disabled" ]]; then setup_rclone; fi

    IFS=',' read -ra DIR_LIST <<< "${BACKUP_DIRS}"

    log_section "执行打包"
    local orig_prefix="${BACKUP_PREFIX}"
    if [[ "${BACKUP_SEPARATE}" == "children" ]]; then
        # 跨目录全局统计同名子目录，避免不同源目录下的同名子目录生成相同前缀
        local -A g_seen=() g_conflicted=()
        local gd gc gbase
        for gd in "${DIR_LIST[@]}"; do
            gd=$(echo "$gd" | xargs)
            while IFS= read -r -d '' gc; do
                gbase=$(basename "$gc")
                if [[ -n "${g_seen[$gbase]+x}" ]]; then g_conflicted["$gbase"]=1; fi
                g_seen["$gbase"]=1
            done < <(find "$gd" -mindepth 1 -maxdepth 1 -type d -print0 2>/dev/null | sort -z)
        done
        local conflicted_names=""
        for gbase in "${!g_conflicted[@]}"; do conflicted_names+=" ${gbase}"; done
        # 每个源路径下的直接子文件夹逐个单独打包
        for dir in "${DIR_LIST[@]}"; do
            dir=$(echo "$dir" | xargs)
            log_info "逐子目录打包: ${dir}"
            backup_children "$dir" "${orig_prefix}" "${conflicted_names}"
        done
    elif [[ "${BACKUP_SEPARATE}" == "true" ]]; then
        for dir in "${DIR_LIST[@]}"; do
            dir=$(echo "$dir" | xargs)
            BACKUP_PREFIX="${orig_prefix}_$(basename "$dir")"
            do_backup "$dir"
        done
        BACKUP_PREFIX="${orig_prefix}"
    else
        local dirs_trimmed=()
        for dir in "${DIR_LIST[@]}"; do dirs_trimmed+=("$(echo "$dir" | xargs)"); done
        do_backup "${dirs_trimmed[@]}"
    fi

    # 本地轮转：按实际生成的文件分组前缀独立轮转，避免多前缀混计误删
    log_section "本地备份轮转"
    local g
    if [[ ${#CREATED_GROUPS[@]} -gt 0 ]]; then
        for g in "${CREATED_GROUPS[@]}"; do rotate_local "$g"; done
    else
        rotate_local "${BACKUP_PREFIX}"
    fi

    if [[ "${REMOTE_TYPE}" != "disabled" && ${#CREATED_FILES[@]} -gt 0 ]]; then
        log_section "远程上传"
        for f in "${CREATED_FILES[@]}"; do remote_upload "$f"; done
        # 远程轮转：同样按分组前缀独立处理
        if [[ ${#CREATED_GROUPS[@]} -gt 0 ]]; then
            for g in "${CREATED_GROUPS[@]}"; do remote_rotate "$g"; done
        else
            remote_rotate "${BACKUP_PREFIX}"
        fi
    fi

    log_section "备份任务完成"
    if (( BACKUP_ERRORS == 0 )); then
        log_info "✅ 全部成功，共创建 ${#CREATED_FILES[@]} 个备份文件"
        send_notify "success" "备份完成，创建 ${#CREATED_FILES[@]} 个文件，时间戳: ${TIMESTAMP}"
    else
        log_error "⚠️  完成，但发生 ${BACKUP_ERRORS} 个错误"
        send_notify "failure" "备份完成（有错误），错误数: ${BACKUP_ERRORS}，时间戳: ${TIMESTAMP}"
        exit 1
    fi
}

main "$@"
