#!/bin/bash
# =============================================================================
# backup.sh — 自动打包备份脚本
# 支持多目录打包、循环保留、WebDAV / S3 远程上传（通过 rclone）
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

# ── 环境变量（带默认值）──────────────────────────────────────────────────────
BACKUP_DIRS="${BACKUP_DIRS:-/data}"
BACKUP_DEST="${BACKUP_DEST:-/backup}"
BACKUP_PREFIX="${BACKUP_PREFIX:-backup}"
BACKUP_RETENTION="${BACKUP_RETENTION:-7}"
BACKUP_COMPRESS="${BACKUP_COMPRESS:-gz}"
BACKUP_TIMESTAMP="${BACKUP_TIMESTAMP:-%Y%m%d_%H%M%S}"
BACKUP_SEPARATE="${BACKUP_SEPARATE:-false}"

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

NOTIFY_WEBHOOK="${NOTIFY_WEBHOOK:-}"
NOTIFY_ON_SUCCESS="${NOTIFY_ON_SUCCESS:-false}"
NOTIFY_ON_FAILURE="${NOTIFY_ON_FAILURE:-true}"

TIMESTAMP=$(date +"${BACKUP_TIMESTAMP}")
BACKUP_ERRORS=0
CREATED_FILES=()
RCLONE_REMOTE="backup_remote"
RCLONE_CONFIG="/tmp/rclone-backup.conf"

# ── 工具检查 ─────────────────────────────────────────────────────────────────
check_tools() {
    local tools=("tar")
    case "${BACKUP_COMPRESS}" in
        gz)  tools+=("gzip") ;;
        bz2) tools+=("bzip2") ;;
        xz)  tools+=("xz") ;;
        zst) tools+=("zstd") ;;
    esac
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

# ── rclone 配置生成 ───────────────────────────────────────────────────────────
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
    esac
    chmod 600 "${RCLONE_CONFIG}"
    log_info "rclone 配置已生成 [${REMOTE_TYPE}]"
}

rclone_cmd() { rclone --config "${RCLONE_CONFIG}" "$@"; }

get_remote_path() {
    case "${REMOTE_TYPE}" in
        webdav) echo "${RCLONE_REMOTE}:${WEBDAV_PATH}" ;;
        s3)     echo "${RCLONE_REMOTE}:${S3_BUCKET}/${S3_PATH}" ;;
    esac
}

# ── 远程上传 ─────────────────────────────────────────────────────────────────
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

# ── 远程轮转 ─────────────────────────────────────────────────────────────────
remote_rotate() {
    local prefix="$1"
    local remote_path; remote_path=$(get_remote_path)
    log_info "远程轮转 (保留最新 ${REMOTE_RETENTION} 份)"
    local files=()
    while IFS= read -r line; do
        [[ -n "$line" ]] && files+=("$line")
    done < <(rclone_cmd lsf "${remote_path}/" --files-only 2>/dev/null | grep "^${prefix}_" | sort)
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
            [[ -z "${WEBDAV_URL}" || -z "${WEBDAV_USER}" || -z "${WEBDAV_PASS}" ]] && {
                log_error "WebDAV 配置不完整: WEBDAV_URL / WEBDAV_USER / WEBDAV_PASS"; exit 1; } ;;
        s3)
            [[ -z "${S3_ACCESS_KEY}" || -z "${S3_SECRET_KEY}" || -z "${S3_BUCKET}" ]] && {
                log_error "S3 配置不完整: S3_ACCESS_KEY / S3_SECRET_KEY / S3_BUCKET"; exit 1; } ;;
        disabled) ;;
        *) log_error "未知 REMOTE_TYPE: ${REMOTE_TYPE}"; exit 1 ;;
    esac
}

# ── 打包 ─────────────────────────────────────────────────────────────────────
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
        CREATED_FILES+=("${archive_path}")
    else
        log_error "打包失败: ${archive_name}"; ((BACKUP_ERRORS++)); return 1
    fi
}

# ── 本地轮转 ─────────────────────────────────────────────────────────────────
rotate_local() {
    local prefix="$1"
    log_info "本地轮转 (保留最新 ${BACKUP_RETENTION} 份，前缀: ${prefix})"
    local files=()
    while IFS= read -r -d $'\0' f; do files+=("$f"); done \
        < <(find "${BACKUP_DEST}" -maxdepth 1 -name "${prefix}_*" -type f -print0 | sort -z)
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

# ── 主流程 ───────────────────────────────────────────────────────────────────
main() {
    log_section "备份任务开始"
    log_info "时间戳: ${TIMESTAMP} | 压缩: ${BACKUP_COMPRESS} | 远程: ${REMOTE_TYPE}"

    check_tools
    validate_remote_config
    mkdir -p "${BACKUP_DEST}"
    [[ "${REMOTE_TYPE}" != "disabled" ]] && setup_rclone

    IFS=',' read -ra DIR_LIST <<< "${BACKUP_DIRS}"

    log_section "执行打包"
    if [[ "${BACKUP_SEPARATE}" == "true" ]]; then
        local orig_prefix="${BACKUP_PREFIX}"
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

    log_section "本地备份轮转"
    rotate_local "${BACKUP_PREFIX}"

    if [[ "${REMOTE_TYPE}" != "disabled" && ${#CREATED_FILES[@]} -gt 0 ]]; then
        log_section "远程上传"
        for f in "${CREATED_FILES[@]}"; do remote_upload "$f"; done
        remote_rotate "${BACKUP_PREFIX}"
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
