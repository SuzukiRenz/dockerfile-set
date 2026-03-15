#!/bin/bash
# =============================================================================
# entrypoint.sh — Docker 容器入口
# =============================================================================

set -e

GREEN='\033[0;32m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; NC='\033[0m'
log()  { echo -e "${GREEN}[ENTRYPOINT]${NC} $(date '+%Y-%m-%d %H:%M:%S') $*"; }
warn() { echo -e "${YELLOW}[ENTRYPOINT]${NC} $(date '+%Y-%m-%d %H:%M:%S') $*"; }

# ─── 加载 .env 文件 ──────────────────────────────────────────────────────────
if [[ -f /app/.env ]]; then
    log "加载环境变量: /app/.env"
    set -a
    source <(grep -v '^\s*#' /app/.env | grep -v '^\s*$')
    set +a
fi

BACKUP_CRON="${BACKUP_CRON:-0 2 * * *}"
BACKUP_RUN_ON_START="${BACKUP_RUN_ON_START:-false}"
TZ="${TZ:-Asia/Shanghai}"

log "时区: ${TZ} | Cron: ${BACKUP_CRON}"

# ─── 设置时区 ────────────────────────────────────────────────────────────────
if [[ -f "/usr/share/zoneinfo/${TZ}" ]]; then
    ln -snf "/usr/share/zoneinfo/${TZ}" /etc/localtime
    echo "${TZ}" > /etc/timezone
fi

# ─── 将环境变量写成 export 格式，确保 cron 子进程能继承 ──────────────────────
# 关键修复：printenv 输出的 KEY=VALUE 在子 shell 中只是赋值不是导出
# 必须写成 export KEY="VALUE" 才能让 backup.sh 作为子进程继承到变量
log "写入 cron 环境变量 (export 格式)..."
mkdir -p /app/logs
{
    while IFS='=' read -r key value; do
        # 跳过空 key、特殊变量、以及包含非法字符的 key
        [[ -z "$key" || "$key" == "_" ]] && continue
        [[ "$key" =~ [^a-zA-Z0-9_] ]] && continue
        # 值用单引号包裹，内部单引号转义
        safe_value="${value//"'"/"'\\'''"}"
        echo "export ${key}='${safe_value}'"
    done < <(printenv)
} > /etc/cron-env
chmod 600 /etc/cron-env

log "环境变量已写入 /etc/cron-env ($(wc -l < /etc/cron-env) 条)"

# ─── 生成 crontab ────────────────────────────────────────────────────────────
CRON_FILE="/etc/cron.d/backup-job"
cat > "${CRON_FILE}" << CRONEOF
# Backup cron job — 由 entrypoint.sh 自动生成
SHELL=/bin/bash
PATH=/usr/local/sbin:/usr/local/bin:/sbin:/bin:/usr/sbin:/usr/bin

${BACKUP_CRON} root . /etc/cron-env && /app/backup.sh >> /app/logs/backup.log 2>&1

CRONEOF
chmod 0644 "${CRON_FILE}"
log "Crontab 已写入: ${CRON_FILE}"
crontab "${CRON_FILE}"

# 验证关键变量是否写入成功
if grep -q "BACKUP_PREFIX" /etc/cron-env; then
    SAVED_PREFIX=$(grep "export BACKUP_PREFIX=" /etc/cron-env | sed "s/export BACKUP_PREFIX='\(.*\)'/\1/")
    log "已确认 BACKUP_PREFIX='${SAVED_PREFIX}' 写入 cron 环境"
else
    warn "未检测到 BACKUP_PREFIX，将使用默认值 'backup'"
fi

# ─── 立即执行一次（可选）────────────────────────────────────────────────────
if [[ "${BACKUP_RUN_ON_START}" == "true" ]]; then
    warn "BACKUP_RUN_ON_START=true，立即执行一次备份..."
    /app/backup.sh 2>&1 | tee -a /app/logs/backup.log || true
fi

# ─── 启动 cron ───────────────────────────────────────────────────────────────
log "启动 cron 服务..."
service cron start || cron

log "容器就绪，等待定时任务执行..."
echo -e "${CYAN}──────────────────────────────────────────────────────${NC}"
touch /app/logs/backup.log
exec tail -f /app/logs/backup.log
