#!/bin/bash
# =============================================================================
# entrypoint.sh — Docker 容器入口，负责配置 cron 并启动
# =============================================================================

set -e

# 颜色
GREEN='\033[0;32m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; NC='\033[0m'
log() { echo -e "${GREEN}[ENTRYPOINT]${NC} $(date '+%Y-%m-%d %H:%M:%S') $*"; }
warn() { echo -e "${YELLOW}[ENTRYPOINT]${NC} $(date '+%Y-%m-%d %H:%M:%S') $*"; }

# ─── 加载 .env 文件（如果存在）─────────────────────────────────────────────
if [[ -f /app/.env ]]; then
    log "加载环境变量: /app/.env"
    set -a
    # 过滤注释和空行
    source <(grep -v '^\s*#' /app/.env | grep -v '^\s*$')
    set +a
fi

# ─── 默认值 ─────────────────────────────────────────────────────────────────
BACKUP_CRON="${BACKUP_CRON:-0 2 * * *}"
BACKUP_RUN_ON_START="${BACKUP_RUN_ON_START:-false}"
TZ="${TZ:-Asia/Shanghai}"

log "时区: ${TZ}"
log "Cron 计划: ${BACKUP_CRON}"

# ─── 设置时区 ────────────────────────────────────────────────────────────────
if [[ -f "/usr/share/zoneinfo/${TZ}" ]]; then
    ln -snf "/usr/share/zoneinfo/${TZ}" /etc/localtime
    echo "${TZ}" > /etc/timezone
fi

# ─── 将所有当前环境变量写入 cron 可读的环境文件 ─────────────────────────────
log "写入 cron 环境变量..."
mkdir -p /app/logs
printenv | grep -v "^_=" > /etc/cron-env

# ─── 生成 crontab ────────────────────────────────────────────────────────────
CRON_FILE="/etc/cron.d/backup-job"
cat > "${CRON_FILE}" << EOF
# Backup cron job — 由 entrypoint.sh 自动生成
SHELL=/bin/bash
PATH=/usr/local/sbin:/usr/local/bin:/sbin:/bin:/usr/sbin:/usr/bin

${BACKUP_CRON} root . /etc/cron-env; /app/backup.sh >> /app/logs/backup.log 2>&1

EOF
chmod 0644 "${CRON_FILE}"
log "Crontab 已写入: ${CRON_FILE}"
crontab "${CRON_FILE}"

# ─── 立即执行一次（可选）────────────────────────────────────────────────────
if [[ "${BACKUP_RUN_ON_START}" == "true" ]]; then
    warn "BACKUP_RUN_ON_START=true，立即执行一次备份..."
    /app/backup.sh 2>&1 | tee -a /app/logs/backup.log || true
fi

# ─── 启动 cron + 日志尾随 ────────────────────────────────────────────────────
log "启动 cron 服务..."
service cron start || cron

# 保持容器运行，并实时输出日志
log "容器就绪，等待定时任务执行..."
log "日志文件: /app/logs/backup.log"
echo -e "${CYAN}──────────────────────────────────────────────────────${NC}"

# 如果日志文件不存在则创建
touch /app/logs/backup.log

# 跟踪日志文件
exec tail -f /app/logs/backup.log
