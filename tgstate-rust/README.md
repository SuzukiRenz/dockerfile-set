# tgState Rust 二开增强版

基于 [tgstate-rust](https://github.com/buyi06/tgstate-rust) 二次开发的私有文件存储系统。项目保留原版“以 Telegram 频道作为文件存储后端、单文件部署、网页配置、短链分享、PicGo 兼容”等核心能力，并在此基础上补充了 S3 兼容存储、WebDAV 挂载、逻辑文件夹、私密可见性、数据库备份恢复、图集分享和更完整的管理后台体验。

适合用于自建轻量网盘、图床、私有文件中转、Telegram 存储网关、WebDAV 挂载盘，以及 Cloudflare R2 / MinIO / S3 兼容对象存储前端。

## 主要功能

### 原版核心能力

- 使用 Telegram 频道作为文件存储后端。
- Web 管理后台上传、下载、删除和复制短链。
- 大文件自动按 Telegram 限制分块上传，下载时流式拼接。
- 短链接下载与在线预览，支持图片、视频、PDF、文本等常见格式。
- 图床模式，兼容 PicGo 上传 API。
- Telegram Bot 自动同步频道文件变动。
- SSE 实时推送文件列表更新。
- 首次启动网页引导配置，无需提前准备 `.env`。
- 登录认证、限流、安全头、CSP、Cookie 加固等基础安全能力。

### 本二开版本新增能力

- 新增 S3 兼容存储后端，支持 AWS S3、Cloudflare R2、MinIO 以及其他 S3-compatible 服务。
- 新增 Telegram / S3 双后端切换，可在设置页选择当前存储后端。
- 新增 WebDAV 服务，可通过 `/webdav` 挂载到系统文件管理器、OpenList、Infuse、播放器或其他 WebDAV 客户端。
- WebDAV 支持逻辑文件夹浏览、上传、下载、HEAD、PROPFIND，并支持只读模式。
- WebDAV 下载已改为直接流式返回文件内容，不再跳转到短链，提升 OpenList 网页端对私密文件的兼容性。
- WebDAV 上传到 Telegram 后端时支持流式分块处理，避免服务端先完整读入整个请求体。
- 新增逻辑文件夹管理，后台提供文件夹树、面包屑、文件夹与文件混合视图。
- 新增文件移动到逻辑文件夹功能，支持批量移动。
- 新增文件夹级可见性设置，支持公开 / 私密，并可选择是否应用到子文件夹和子文件。
- 上传或移动文件到私密文件夹时，文件会继承目标文件夹的私密可见性。
- 新增文件级公开 / 私密链接设置，私密文件短链需要登录后访问。
- 新增图集功能，可创建公开或带访问码的图集页面。
- 新增数据库备份导出与覆盖导入，支持下载 `.db.gz` 备份文件并在设置页恢复。
- 新增 S3 自定义公开访问域名配置，可用于 CDN 或对象存储自定义域名。
- 新增健康检查接口，返回服务状态、当前存储后端和 Bot 状态。
- 优化登录会话机制，使用随机 session token，而不是从密码派生 Cookie。
- 密码支持 Argon2 哈希存储，兼容旧明文配置自动校验。
- 优化文件类型图标，非图片文件在列表和实时新增项中显示更清晰的类型标识。
- 设置页新增存储后端、S3、WebDAV、数据库备份恢复等配置区域。

## 快速开始

### Docker Compose

```yaml
services:
  tgstate:
    image: your-image-name:latest
    container_name: tgstate
    ports:
      - "8000:8000"
    volumes:
      - tgstate_data:/app/data
    restart: unless-stopped
    environment:
      - BASE_URL=https://your-domain.example
      - LOG_LEVEL=info

volumes:
  tgstate_data:
```

启动后访问：

```text
http://你的服务器IP:8000
```

首次进入会显示初始化引导，设置管理员密码后进入系统设置页，填写 Telegram Bot Token 和频道名即可开始使用。

### 从源码编译

```bash
cargo build --release
./target/release/tgstate
```

项目当前 `Cargo.toml` 指定 Rust edition 2021，`rust-version = "1.88"`。如果使用较旧 Rust 工具链构建失败，请升级 Rust。

### 本地开发常用命令

```bash
# 调试运行
cargo run

# Release 构建
cargo build --release

# 运行全部测试
cargo test

# 运行单个测试，示例
cargo test upload_route_rejects_file_field_before_auth

# 检查代码是否能编译
cargo check
```

## 配置流程

启动后访问 Web 页面，先设置管理员密码。登录后进入“系统设置”，至少需要配置 Telegram Bot Token 和频道名。Bot Token 可从 [@BotFather](https://t.me/BotFather) 获取，频道名支持 `@channel_name` 或 `-100...` 形式。Bot 需要加入目标频道，并具备发送消息权限。

如果只使用 Telegram 后端，完成 Bot 配置即可使用。如果需要使用 S3 / R2 / MinIO，请在设置页切换存储后端并填写 S3 Endpoint、Region、Bucket、Access Key 和 Secret Key。如果需要 WebDAV，请在设置页开启 WebDAV，并配置 WebDAV 用户名；WebDAV 密码复用管理员密码。

## 存储后端

### Telegram 后端

Telegram 后端是默认模式。小文件会直接作为 Telegram 文档发送到频道，大文件会按约 19.5MB 分块上传，并生成 manifest 文件记录分块。下载时服务端会自动识别 manifest 并流式拼接返回。

这种模式不需要额外对象存储服务，适合个人轻量使用。需要注意 Telegram Bot API、网络质量、频道权限和 API 限速都可能影响上传下载体验。

### S3 兼容后端

S3 后端支持标准 S3-compatible API，可用于 AWS S3、Cloudflare R2、MinIO、Backblaze B2 S3 API 等服务。启用后，新上传的文件会写入对象存储，数据库记录对象 key、文件大小、短链 ID 和逻辑文件夹等元数据。

可选配置 `S3_PUBLIC_BASE_URL`，用于生成自定义公开访问域名。如果未配置公开域名，服务端会根据 Endpoint、Bucket 和 path style 设置生成下载地址，并通过服务端代理流式返回。

## WebDAV

开启 WebDAV 后，挂载地址为：

```text
https://你的域名/webdav
```

认证方式为 Basic Auth。用户名使用设置页的 WebDAV 用户名，密码复用管理员密码。

当前 WebDAV 支持：

- `OPTIONS`
- `PROPFIND`
- `GET`
- `HEAD`
- `PUT`

只读模式开启后，WebDAV 仅允许浏览和下载，不允许上传。WebDAV 的目录来自项目内的逻辑文件夹，不依赖真实磁盘目录。通过 WebDAV 上传到 `A/B/file.txt` 时，数据库会记录 `folder_path = A/B`，后台文件管理和 WebDAV 目录会保持一致。

WebDAV 下载现在直接由 `/webdav/*` 路由流式输出文件内容，不再跳转到 `/d/*` 短链。因此，私密文件夹中的文件在 OpenList 等 WebDAV 聚合工具中更容易正常访问。

如果你的域名走 Cloudflare Free 橙云代理，需要注意 Cloudflare 对单次上传请求体有 100MB 限制。普通 WebDAV 客户端通常使用单次 `PUT` 上传整个文件，因此超过 100MB 的 WebDAV 上传仍可能被 Cloudflare 在请求到达服务前拦截。推荐给 WebDAV 或大文件上传单独配置一个 DNS only 的上传域名。

## 逻辑文件夹与私密可见性

本版本新增逻辑文件夹系统。文件夹不是磁盘目录，而是保存在 SQLite 中的 `folder_path` 元数据。后台提供左侧文件夹树、当前位置面包屑、混合文件夹视图和批量移动工具。

文件夹支持公开 / 私密可见性。设置文件夹为私密后，可以选择是否应用到已有子文件夹和文件。新上传到该文件夹的文件会继承文件夹可见性。已有文件从公开文件夹移动到私密文件夹时，也会自动按目标文件夹重新计算可见性。

私密文件的短链访问需要管理员登录 session。公开文件仍可通过短链直接访问。

## 图集分享

图集功能允许把多个文件组合为一个独立分享页面。图集支持公开访问，也支持设置访问码。图集页面会对图片生成预览，对其他文件显示占位卡片并提供原文件链接。

相关接口包括：

```text
GET  /api/albums
POST /api/albums
GET  /api/albums/:album_id
POST /api/albums/:album_id/items
GET  /album/:album_id
```

## 数据库备份与恢复

设置页提供数据库备份导出和覆盖导入功能。导出会对当前 SQLite 数据库生成一致性备份，并压缩为 `.db.gz` 文件下载。导入时上传之前导出的 `.db.gz` 文件，确认后覆盖当前数据库并立即应用运行时配置。

相关接口包括：

```text
GET  /api/app-config/db/export
POST /api/app-config/db/import
```

建议在重大升级、迁移服务器、切换存储后端或批量整理文件夹前先导出备份。

## API 概览

### 文件操作

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/api/upload` | 上传文件，multipart 字段名为 `file` |
| `GET` | `/api/files` | 获取文件列表 |
| `DELETE` | `/api/files/:file_id` | 删除文件 |
| `POST` | `/api/batch_delete` | 批量删除文件 |
| `POST` | `/api/files/:file_id/move` | 移动文件到逻辑文件夹 |
| `POST` | `/api/files/:file_id/link-settings` | 更新文件公开 / 私密可见性 |
| `GET` | `/d/:short_id` | 短链下载或预览 |
| `GET` | `/api/file-updates` | SSE 实时文件更新 |

### 文件夹

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/folders` | 获取逻辑文件夹列表及继承可见性 |
| `POST` | `/api/folders` | 设置文件夹公开 / 私密可见性 |

### 认证

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/api/auth/login` | 登录 |
| `POST` | `/api/auth/logout` | 退出登录 |

### 配置

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/app-config` | 获取当前配置状态 |
| `POST` | `/api/app-config/save` | 保存配置但不应用 |
| `POST` | `/api/app-config/apply` | 保存并应用配置 |
| `POST` | `/api/reset-config` | 重置配置 |
| `POST` | `/api/set-password` | 设置管理员密码 |
| `POST` | `/api/verify/bot` | 验证 Bot Token |
| `POST` | `/api/verify/channel` | 验证频道 |
| `GET` | `/api/app-config/db/export` | 导出数据库备份 |
| `POST` | `/api/app-config/db/import` | 导入并覆盖数据库 |

### 图集

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/albums` | 获取图集列表 |
| `POST` | `/api/albums` | 创建图集 |
| `GET` | `/api/albums/:album_id` | 获取图集详情 |
| `POST` | `/api/albums/:album_id/items` | 添加图集文件 |
| `GET` | `/album/:album_id` | 访问图集分享页 |

### WebDAV

| 方法 | 路径 | 说明 |
|---|---|---|
| `OPTIONS` | `/webdav` / `/webdav/*` | WebDAV 能力探测 |
| `PROPFIND` | `/webdav` / `/webdav/*` | 列出目录或文件属性 |
| `GET` | `/webdav/*` | 直接流式下载文件 |
| `HEAD` | `/webdav/*` | 获取文件响应头 |
| `PUT` | `/webdav/*` | 上传文件到逻辑路径 |

## PicGo 兼容上传

设置 `PICGO_API_KEY` 后，可以通过请求头 `X-Api-Key` 上传文件：

```bash
curl -X POST http://your-host:8000/api/upload \
  -H "X-Api-Key: your_picgo_api_key" \
  -F "file=@image.png"
```

返回 JSON 中会包含 `url`、`path`、`short_id` 等字段，便于图床客户端使用。

## 环境变量

环境变量主要用于 Docker 或自动化部署时预配置。大部分配置也可以在网页设置页中修改，并保存到 SQLite 数据库。

| 变量 | 说明 | 默认值 |
|---|---|---|
| `BOT_TOKEN` | Telegram Bot Token | - |
| `CHANNEL_NAME` | Telegram 频道名，支持 `@name` 或 `-100xxx` | - |
| `PASS_WORD` | 管理员密码，首次配置后会写入数据库 | - |
| `PICGO_API_KEY` | PicGo 上传 API 密钥 | - |
| `BASE_URL` | 站点公开访问 URL | `http://127.0.0.1:8000` |
| `DATA_DIR` | 数据目录 | `app/data` |
| `LOG_LEVEL` | 日志级别 | `info` |
| `SESSION_MAX_AGE_SECS` | 登录 session cookie 有效期 | `604800` |
| `COOKIE_SECURE` | 强制 Cookie 使用 `Secure` 标志 | 自动推断 |
| `TRUST_FORWARDED_FOR` | 信任反向代理传入的客户端 IP | `0` |

S3、WebDAV 等配置优先建议在设置页填写。目前运行时配置会保存到 SQLite 的 `app_settings` 表。

## 反向代理与 Cloudflare 注意事项

如果部署在 HTTPS 反向代理后面，建议设置：

```bash
COOKIE_SECURE=1
TRUST_FORWARDED_FOR=1
BASE_URL=https://your-domain.example
```

如果使用 Cloudflare Free 套餐橙云代理，需要注意单次上传请求体限制为 100MB。网页端如果未来实现分片上传，可以通过每片低于 100MB 的方式绕过单请求限制；但通用 WebDAV 客户端通常一次 `PUT` 整个文件，因此 WebDAV 大文件上传推荐使用 DNS only 子域名直连源站。

## 安全设计

- 管理后台使用登录 session cookie 鉴权。
- session token 使用随机 32 字节值生成，并保存在服务端数据库中。
- 密码使用 Argon2 哈希存储，兼容旧配置自动验证。
- Cookie 使用 `HttpOnly`、`SameSite=Strict`、`Path=/` 和可配置 `Secure` 标志。
- 上传接口在配置密码或 PicGo API Key 后要求有效认证。
- 私密短链需要管理员 session 才能访问。
- WebDAV 使用 Basic Auth，密码复用管理员密码。
- 登录、上传、API 和下载路径具有限流保护。
- 响应包含 CSP、X-Frame-Options、X-Content-Type-Options 等安全头。

## 技术栈

| 组件 | 技术 |
|---|---|
| Web 框架 | Axum 0.7 |
| 异步运行时 | Tokio |
| 模板引擎 | Tera |
| 数据库 | SQLite + WAL + r2d2 连接池 |
| Telegram API | reqwest |
| S3 兼容存储 | rust-s3 |
| 压缩备份 | flate2 gzip |
| 前端 | 原生 HTML / CSS / JavaScript |
| 部署 | Docker / 单二进制 |

## 项目结构

```text
├── src/
│   ├── main.rs                 # 应用入口、路由组装、中间件挂载
│   ├── config.rs               # 环境变量与数据库配置合并
│   ├── database.rs             # SQLite 表结构、迁移和元数据操作
│   ├── auth.rs                 # 密码、session token、Cookie、上传认证
│   ├── state.rs                # AppState、Bot 运行状态、运行时配置应用
│   ├── middleware/             # 认证、限流、安全响应头
│   ├── routes/                 # 页面、上传、文件、设置、WebDAV、图集等路由
│   ├── storage/                # S3 兼容存储后端
│   └── telegram/               # Telegram Bot、文件发送、删除和轮询同步
├── app/
│   ├── templates/              # Tera HTML 模板
│   └── static/                 # 前端 JS / CSS
├── Dockerfile
└── Cargo.toml
```

## 已知限制

- Cloudflare Free 橙云代理下，单次上传请求体超过 100MB 会在到达服务端前被拦截。
- 通用 WebDAV 客户端通常不支持项目自定义分片协议，因此 WebDAV 大文件上传建议使用不经过 Cloudflare 代理的域名。
- Telegram 后端依赖 Bot API 和频道权限，大量并发或超大文件可能受 Telegram 网络和限速影响。
- S3 后端的对象生命周期、权限策略和 CDN 缓存需要在对象存储服务侧自行配置。

## License

MIT
