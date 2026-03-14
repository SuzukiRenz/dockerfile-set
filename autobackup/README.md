# 🗄️ Backup Tool — 自动定时备份工具

支持多目录打包、循环保留、WebDAV / S3 远程上传，可通过 Docker 一键部署。

## 📦 功能特性

- **多目录备份** — 同时备份多个目录，支持合并或单独打包
- **灵活命名** — 自定义前缀 + 时间戳，格式自由配置
- **自动轮转** — 本地和远程各自独立控制保留数量（默认 7 份）
- **多种压缩** — 支持 `gz` / `bz2` / `xz` / `zst`
- **WebDAV 上传** — 兼容 OpenList、Nextcloud 等 WebDAV 服务
- **S3 上传** — 兼容 AWS S3、MinIO、Cloudflare R2、阿里云 OSS 等
- **Webhook 通知** — 支持飞书、钉钉、Slack 等通知
- **Docker 部署** — 提供完整 Dockerfile 和 Compose 配置

---

## 🚀 快速开始

### 方式一：Docker Compose（推荐）

```bash
# 1. 克隆或下载项目
git clone <repo-url> backup-tool && cd backup-tool

# 2. 复制并编辑配置
cp .env.example .env
nano .env

# 3. 构建并启动
docker compose up -d

# 4. 查看运行日志
docker compose logs -f
```

### 方式二：直接运行脚本

```bash
# 赋予执行权限
chmod +x backup.sh

# 配置环境变量后执行
export BACKUP_DIRS=/data/app,/data/mysql
export BACKUP_PREFIX=myserver
export BACKUP_DEST=/backup
bash backup.sh
```

---

## ⚙️ 配置说明

所有配置通过 `.env` 文件管理，以下为关键配置项：

### 基础备份

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `BACKUP_DIRS` | `/data` | 备份源目录，多个用逗号分隔 |
| `BACKUP_DEST` | `/backup` | 本地备份存储目录 |
| `BACKUP_PREFIX` | `backup` | 压缩包文件名前缀 |
| `BACKUP_TIMESTAMP` | `%Y%m%d_%H%M%S` | 时间戳格式 |
| `BACKUP_COMPRESS` | `gz` | 压缩格式: `gz/bz2/xz/zst` |
| `BACKUP_SEPARATE` | `false` | 每目录单独打包 |
| `BACKUP_RETENTION` | `7` | 本地保留份数 |

### 定时任务

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `BACKUP_CRON` | `0 2 * * *` | Cron 表达式（每天凌晨 2 点） |
| `BACKUP_RUN_ON_START` | `false` | 容器启动时立即执行一次 |
| `TZ` | `Asia/Shanghai` | 时区 |

### 远程存储

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `REMOTE_TYPE` | `disabled` | 类型: `disabled/webdav/s3` |
| `REMOTE_RETENTION` | `7` | 远程保留份数 |

### WebDAV 配置（`REMOTE_TYPE=webdav`）

```env
WEBDAV_URL=http://your-openlist:5244/dav
WEBDAV_USER=admin
WEBDAV_PASS=your_password
WEBDAV_PATH=/backups
```

### S3 配置（`REMOTE_TYPE=s3`）

```env
S3_ENDPOINT=              # 留空=AWS官方，填写=自定义端点
S3_ACCESS_KEY=your_key
S3_SECRET_KEY=your_secret
S3_BUCKET=your-bucket
S3_PATH=backups
S3_REGION=us-east-1
```

---

## 📁 生成文件示例

```
/backup/
├── myapp_20240315_020000.tar.gz   # 最新备份
├── myapp_20240314_020000.tar.gz
├── myapp_20240313_020000.tar.gz
├── myapp_20240312_020000.tar.gz
├── myapp_20240311_020000.tar.gz
├── myapp_20240310_020000.tar.gz
└── myapp_20240309_020000.tar.gz   # 第 7 份（再备份时删除此文件）
```

---

## 🔧 常用命令

```bash
# 立即执行一次备份
docker compose exec backup /app/backup.sh

# 查看备份文件列表
docker compose exec backup ls -lh /backup

# 查看定时任务计划
docker compose exec backup crontab -l

# 查看历史日志
cat /opt/backup/logs/backup.log

# 重启服务
docker compose restart backup
```

---

## 🌐 OpenList WebDAV 配置参考

OpenList 默认 WebDAV 地址为 `http://<host>:<port>/dav`，端口默认 `5244`。

```env
REMOTE_TYPE=webdav
WEBDAV_URL=http://192.168.1.100:5244/dav
WEBDAV_USER=admin
WEBDAV_PASS=openlist_password
WEBDAV_PATH=/My Backup/server1
```

---

## ☁️ 各 S3 兼容存储配置示例

**MinIO（自建）**
```env
S3_ENDPOINT=http://192.168.1.100:9000
S3_REGION=us-east-1
```

**Cloudflare R2**
```env
S3_ENDPOINT=https://<ACCOUNT_ID>.r2.cloudflarestorage.com
S3_REGION=auto
```

**阿里云 OSS**
```env
S3_ENDPOINT=https://oss-cn-hangzhou.aliyuncs.com
S3_REGION=cn-hangzhou
```

**腾讯云 COS**
```env
S3_ENDPOINT=https://cos.ap-guangzhou.myqcloud.com
S3_REGION=ap-guangzhou
```
