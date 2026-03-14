#!/bin/bash
# =============================================================================
# backup.sh — 自动打包备份脚本
# 支持多目录打包、循环保留、WebDAV / S3 远程上传
# =============================================================================

set -euo pipefail

# ─── 颜色输出 ────────────────────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
BLUE='\033[0;34m'; CYAN='\033[0;36m'; NC='\033[0m'

log_info()    { echo -e "${GREEN}[INFO]${NC}  $(date '+%Y-%m-%d %H:%M:%S') $*"; }
log_warn()    { echo -e "${YELLOW}[WARN]${NC}  $(date '+%Y-%m-%d %H:%M:%S') $*"; }
log_error()   { echo -e "${RED}[ERROR]${NC} $(date '+%Y-%m-%d %H:%M:%S') $*"; }
log_section() { echo -e "\n${CYAN}══════════════════════════════════════${NC}"; \
                echo -e "${CYAN}  $*${NC}"; \
                echo -e "${CYAN}══════════════════════════════════════${NC}"; }

# ─── 读取环境变量（带默认值）────────────────────────────────────────────────
BACKUP_DIRS="${BACKUP_DIRS:-/data}"                   # 备份源目录（逗号分隔）
BACKUP_DEST="${BACKUP_DEST:-/backup}"                 # 本地备份存储目录
BACKUP_PREFIX="${BACKUP_PREFIX:-backup}"              # 压缩包文件名前缀
BACKUP_RETENTION="${BACKUP_RETENTION:-7}"             # 本地保留份数
BACKUP_COMPRESS="${BACKUP_COMPRESS:-gz}"              # 压缩方式: gz | bz2 | xz | zst
BACKUP_TIMESTAMP="${BACKUP_TIMESTAMP:-%Y%m%d_%H%M%S}" # 时间戳格式（strftime）
BACKUP_SEPARATE="${BACKUP_SEPARATE:-false}"           # true=每个目录单独打包

REMOTE_TYPE="${REMOTE_TYPE:-disabled}"                # 远程类型: disabled | webdav | s3
REMOTE_RETENTION="${REMOTE_RETENTION:-7}"             # 远程保留份数

# WebDAV
WEBDAV_URL="${WEBDAV_URL:-}"
WEBDAV_USER="${WEBDAV_USER:-}"
WEBDAV_PASS="${WEBDAV_PASS:-}"
WEBDAV_PATH="${WEBDAV_PATH:-/backups}"

# S3
S3_ENDPOINT="${S3_ENDPOINT:-}"
S3_ACCESS_KEY="${S3_ACCESS_KEY:-}"
S3_SECRET_KEY="${S3_SECRET_KEY:-}"
S3_BUCKET="${S3_BUCKET:-}"
S3_PATH="${S3_PATH:-backups/}"
S3_REGION="${S3_REGION:-us-east-1}"
S3_STORAGE_CLASS="${S3_STORAGE_CLASS:-STANDARD}"

# 通知
NOTIFY_WEBHOOK="${NOTIFY_WEBHOOK:-}"                  # Webhook URL (飞书/钉钉/Slack 等)
NOTIFY_ON_SUCCESS="${NOTIFY_ON_SUCCESS:-false}"
NOTIFY_ON_FAILURE="${NOTIFY_ON_FAILURE:-true}"

# ─── 初始化 ──────────────────────────────────────────────────────────────────
TIMESTAMP=$(date +"${BACKUP_TIMESTAMP}")
BACKUP_ERRORS=0
CREATED_FILES=()

# ─── 工具函数 ────────────────────────────────────────────────────────────────

# 检查必要工具
check_tools() {
    local tools=("tar")
    case "${BACKUP_COMPRESS}" in
        gz)  tools+=("gzip") ;;
        bz2) tools+=("bzip2") ;;
        xz)  tools+=("xz") ;;
        zst) tools+=("zstd") ;;
    esac
    if [[ "${REMOTE_TYPE}" == "s3" ]]; then
        tools+=("aws")
    fi
    for t in "${tools[@]}"; do
        if ! command -v "$t" &>/dev/null; then
            log_error "缺少必要工具: $t"
            exit 1
        fi
    done
}

# 获取压缩扩展名和 tar 参数
get_compress_opts() {
    case "${BACKUP_COMPRESS}" in
        gz)  echo "-z .tar.gz" ;;
        bz2) echo "-j .tar.bz2" ;;
        xz)  echo "-J .tar.xz" ;;
        zst) echo "--zstd .tar.zst" ;;
        *)   echo "-z .tar.gz" ;;
    esac
}

# 发送 Webhook 通知
send_notify() {
    local status="$1"; local msg="$2"
    [[ -z "${NOTIFY_WEBHOOK}" ]] && return 0
    if [[ "${status}" == "success" && "${NOTIFY_ON_SUCCESS}" != "true" ]]; then return 0; fi
    if [[ "${status}" == "failure" && "${NOTIFY_ON_FAILURE}" != "true" ]]; then return 0; fi

    local icon="✅"; [[ "${status}" == "failure" ]] && icon="❌"
    local payload="{\"msg_type\":\"text\",\"content\":{\"text\":\"${icon} [Backup] ${msg}\"}}"

    curl -sS -X POST "${NOTIFY_WEBHOOK}" \
        -H 'Content-Type: application/json' \
        -d "${payload}" >/dev/null 2>&1 || log_warn "Webhook 通知发送失败"
}

# ─── 打包函数 ────────────────────────────────────────────────────────────────
do_backup() {
    local dirs_to_pack=("$@")
    local read_opts; read_opts=$(get_compress_opts)
    local tar_flag; tar_flag=$(echo "${read_opts}" | awk '{print $1}')
    local ext; ext=$(echo "${read_opts}" | awk '{print $2}')

    local archive_name="${BACKUP_PREFIX}_${TIMESTAMP}${ext}"
    local archive_path="${BACKUP_DEST}/${archive_name}"

    log_info "正在打包: ${dirs_to_pack[*]}"
    log_info "目标文件: ${archive_path}"

    # 过滤出存在的目录
    local valid_dirs=()
    for d in "${dirs_to_pack[@]}"; do
        if [[ -e "$d" ]]; then
            valid_dirs+=("$d")
        else
            log_warn "目录不存在，跳过: $d"
        fi
    done

    if [[ ${#valid_dirs[@]} -eq 0 ]]; then
        log_error "没有有效的备份目录，跳过本次备份"
        ((BACKUP_ERRORS++))
        return 1
    fi

    # 执行打包
    if tar "${tar_flag}" -cf "${archive_path}" "${valid_dirs[@]}" 2>/dev/null; then
        local size; size=$(du -sh "${archive_path}" | cut -f1)
        log_info "打包完成: ${archive_name} (${size})"
        CREATED_FILES+=("${archive_path}")
    else
        log_error "打包失败: ${archive_name}"
        ((BACKUP_ERRORS++))
        return 1
    fi
}

# ─── 轮转清理函数（本地）───────────────────────────────────────────────────
rotate_local() {
    local prefix="$1"
    log_info "本地轮转 (保留最新 ${BACKUP_RETENTION} 份，前缀: ${prefix})"

    local files=()
    while IFS= read -r -d $'\0' f; do
        files+=("$f")
    done < <(find "${BACKUP_DEST}" -maxdepth 1 -name "${prefix}_*" -type f -print0 | sort -z)

    local total=${#files[@]}
    local to_delete=$(( total - BACKUP_RETENTION ))

    if (( to_delete > 0 )); then
        for (( i=0; i<to_delete; i++ )); do
            log_warn "删除旧备份: ${files[$i]}"
            rm -f "${files[$i]}"
        done
        log_info "本地已清理 ${to_delete} 个旧备份"
    else
        log_info "本地备份数量 (${total}) 未超限，无需清理"
    fi
}

# ─── WebDAV 上传/删除 ────────────────────────────────────────────────────────
webdav_upload() {
    local file="$1"
    local filename; filename=$(basename "$file")
    local remote_url="${WEBDAV_URL%/}/${WEBDAV_PATH#/}/${filename}"

    log_info "WebDAV 上传: ${filename} → ${remote_url}"

    # 确保远程目录存在
    curl -sS -u "${WEBDAV_USER}:${WEBDAV_PASS}" \
        -X MKCOL "${WEBDAV_URL%/}/${WEBDAV_PATH#/}/" \
        --connect-timeout 10 >/dev/null 2>&1 || true

    if curl -sS -u "${WEBDAV_USER}:${WEBDAV_PASS}" \
        -T "$file" \
        --connect-timeout 30 \
        --max-time 3600 \
        "${remote_url}"; then
        log_info "WebDAV 上传成功: ${filename}"
    else
        log_error "WebDAV 上传失败: ${filename}"
        ((BACKUP_ERRORS++))
        return 1
    fi
}

webdav_list() {
    local prefix="$1"
    # PROPFIND 列出目录
    curl -sS -u "${WEBDAV_USER}:${WEBDAV_PASS}" \
        -X PROPFIND \
        -H "Depth: 1" \
        "${WEBDAV_URL%/}/${WEBDAV_PATH#/}/" \
        --connect-timeout 10 2>/dev/null \
    | grep -oP "(?<=<D:href>)[^<]+" \
    | grep "/${prefix}_" \
    | sed 's|.*/||' \
    | sort
}

webdav_delete() {
    local filename="$1"
    local remote_url="${WEBDAV_URL%/}/${WEBDAV_PATH#/}/${filename}"
    log_warn "WebDAV 删除旧备份: ${filename}"
    curl -sS -u "${WEBDAV_USER}:${WEBDAV_PASS}" \
        -X DELETE "${remote_url}" \
        --connect-timeout 10 >/dev/null 2>&1
}

webdav_rotate() {
    local prefix="$1"
    log_info "WebDAV 远程轮转 (保留最新 ${REMOTE_RETENTION} 份)"

    local files=()
    while IFS= read -r line; do
        [[ -n "$line" ]] && files+=("$line")
    done < <(webdav_list "$prefix")

    local total=${#files[@]}
    local to_delete=$(( total - REMOTE_RETENTION ))

    if (( to_delete > 0 )); then
        for (( i=0; i<to_delete; i++ )); do
            webdav_delete "${files[$i]}"
        done
        log_info "WebDAV 已清理 ${to_delete} 个旧备份"
    else
        log_info "WebDAV 备份数量 (${total}) 未超限，无需清理"
    fi
}

# ─── S3 上传/删除 ─────────────────────────────────────────────────────────────
s3_env_setup() {
    export AWS_ACCESS_KEY_ID="${S3_ACCESS_KEY}"
    export AWS_SECRET_ACCESS_KEY="${S3_SECRET_KEY}"
    export AWS_DEFAULT_REGION="${S3_REGION}"
}

s3_upload() {
    local file="$1"
    local filename; filename=$(basename "$file")
    local s3_key="${S3_PATH%/}/${filename}"
    local endpoint_arg=""
    [[ -n "${S3_ENDPOINT}" ]] && endpoint_arg="--endpoint-url ${S3_ENDPOINT}"

    log_info "S3 上传: ${filename} → s3://${S3_BUCKET}/${s3_key}"
    s3_env_setup

    if aws s3 cp "$file" "s3://${S3_BUCKET}/${s3_key}" \
        ${endpoint_arg} \
        --storage-class "${S3_STORAGE_CLASS}" \
        --no-progress 2>&1 | tail -1; then
        log_info "S3 上传成功: ${filename}"
    else
        log_error "S3 上传失败: ${filename}"
        ((BACKUP_ERRORS++))
        return 1
    fi
}

s3_list() {
    local prefix="$1"
    local endpoint_arg=""
    [[ -n "${S3_ENDPOINT}" ]] && endpoint_arg="--endpoint-url ${S3_ENDPOINT}"
    s3_env_setup

    aws s3 ls "s3://${S3_BUCKET}/${S3_PATH%/}/" \
        ${endpoint_arg} 2>/dev/null \
    | awk '{print $NF}' \
    | grep "^${prefix}_" \
    | sort
}

s3_delete() {
    local filename="$1"
    local s3_key="${S3_PATH%/}/${filename}"
    local endpoint_arg=""
    [[ -n "${S3_ENDPOINT}" ]] && endpoint_arg="--endpoint-url ${S3_ENDPOINT}"

    log_warn "S3 删除旧备份: ${filename}"
    s3_env_setup
    aws s3 rm "s3://${S3_BUCKET}/${s3_key}" ${endpoint_arg} >/dev/null 2>&1
}

s3_rotate() {
    local prefix="$1"
    log_info "S3 远程轮转 (保留最新 ${REMOTE_RETENTION} 份)"

    local files=()
    while IFS= read -r line; do
        [[ -n "$line" ]] && files+=("$line")
    done < <(s3_list "$prefix")

    local total=${#files[@]}
    local to_delete=$(( total - REMOTE_RETENTION ))

    if (( to_delete > 0 )); then
        for (( i=0; i<to_delete; i++ )); do
            s3_delete "${files[$i]}"
        done
        log_info "S3 已清理 ${to_delete} 个旧备份"
    else
        log_info "S3 备份数量 (${total}) 未超限，无需清理"
    fi
}

# ─── 远程上传统一入口 ────────────────────────────────────────────────────────
remote_upload() {
    local file="$1"
    case "${REMOTE_TYPE}" in
        webdav) webdav_upload "$file" ;;
        s3)     s3_upload "$file" ;;
        disabled) log_info "远程上传已禁用，跳过" ;;
        *) log_warn "未知的远程类型: ${REMOTE_TYPE}，跳过上传" ;;
    esac
}

remote_rotate() {
    local prefix="$1"
    case "${REMOTE_TYPE}" in
        webdav) webdav_rotate "$prefix" ;;
        s3)     s3_rotate "$prefix" ;;
        disabled) ;;
    esac
}

# ─── 验证远程配置 ────────────────────────────────────────────────────────────
validate_remote_config() {
    case "${REMOTE_TYPE}" in
        webdav)
            if [[ -z "${WEBDAV_URL}" || -z "${WEBDAV_USER}" || -z "${WEBDAV_PASS}" ]]; then
                log_error "WebDAV 配置不完整，请检查 WEBDAV_URL / WEBDAV_USER / WEBDAV_PASS"
                exit 1
            fi
            ;;
        s3)
            if [[ -z "${S3_ACCESS_KEY}" || -z "${S3_SECRET_KEY}" || -z "${S3_BUCKET}" ]]; then
                log_error "S3 配置不完整，请检查 S3_ACCESS_KEY / S3_SECRET_KEY / S3_BUCKET"
                exit 1
            fi
            ;;
    esac
}

# ─── 主流程 ──────────────────────────────────────────────────────────────────
main() {
    log_section "备份任务开始"
    log_info "时间戳: ${TIMESTAMP}"
    log_info "压缩格式: ${BACKUP_COMPRESS}"
    log_info "远程存储: ${REMOTE_TYPE}"

    check_tools
    validate_remote_config
    mkdir -p "${BACKUP_DEST}"

    # 解析备份目录列表
    IFS=',' read -ra DIR_LIST <<< "${BACKUP_DIRS}"

    # ── 打包 ──
    log_section "执行打包"
    if [[ "${BACKUP_SEPARATE}" == "true" ]]; then
        # 每个目录单独打包
        for dir in "${DIR_LIST[@]}"; do
            dir=$(echo "$dir" | xargs)  # trim whitespace
            local_prefix="${BACKUP_PREFIX}_$(basename "$dir")"
            # 临时修改 PREFIX 和 TIMESTAMP 避免冲突
            orig_prefix="${BACKUP_PREFIX}"
            BACKUP_PREFIX="${local_prefix}"
            do_backup "$dir"
            BACKUP_PREFIX="${orig_prefix}"
        done
    else
        # 所有目录打包到一个文件
        dirs_trimmed=()
        for dir in "${DIR_LIST[@]}"; do
            dirs_trimmed+=("$(echo "$dir" | xargs)")
        done
        do_backup "${dirs_trimmed[@]}"
    fi

    # ── 本地轮转 ──
    log_section "本地备份轮转"
    rotate_local "${BACKUP_PREFIX}"

    # ── 远程上传 + 轮转 ──
    if [[ "${REMOTE_TYPE}" != "disabled" && ${#CREATED_FILES[@]} -gt 0 ]]; then
        log_section "远程上传"
        for f in "${CREATED_FILES[@]}"; do
            remote_upload "$f"
        done
        remote_rotate "${BACKUP_PREFIX}"
    fi

    # ── 结束报告 ──
    log_section "备份任务完成"
    if (( BACKUP_ERRORS == 0 )); then
        log_info "✅ 全部成功，共创建 ${#CREATED_FILES[@]} 个备份文件"
        send_notify "success" "备份完成，创建 ${#CREATED_FILES[@]} 个文件，时间戳: ${TIMESTAMP}"
    else
        log_error "⚠️  任务完成，但发生 ${BACKUP_ERRORS} 个错误，请检查日志"
        send_notify "failure" "备份完成（有错误），错误数: ${BACKUP_ERRORS}，时间戳: ${TIMESTAMP}"
        exit 1
    fi
}

main "$@"
