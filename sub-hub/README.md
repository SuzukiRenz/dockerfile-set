# Sub Hub

基于 Go 和 SQLite 的订阅节点集合服务，保留原项目的 subconverter 转换能力，并把原来的单层节点管理升级为主 Token、子 Token、集合授权、访问日志和备份恢复。

## 核心能力

- SQLite 持久化：节点、集合、Token、授权关系、日志和设置均在数据库中。
- 主 Token：可管理全部资源，也可以限制只能访问指定集合。
- 子 Token：可单独设置授权集合、过期时间、最大请求次数和备注。
- 集合订阅：`/sub` 或 `/s/{token}/{collection}` 返回集合对应的 Base64 节点。
- 客户端转换：支持 `target=clash`、`singbox`、`surge`、`quanx`、`loon`、`surfboard` 等参数。
- 管理台：所有节点、集合、Token、链接、日志和备份按钮均有真实 API 支撑。
- 旧数据兼容：数据库没有节点时，可回退读取 `NODES` 或 `nodes.txt`。
- 旧版迁移：可直接在“设置与备份”导入原项目的 `db.json`，节点和订阅设置会写入 SQLite，现有主 Token 保留。
- 新版备份：导出的 JSON 包含节点、集合、主/子 Token、授权关系与设置。

## 快速启动

```bash
cp env.example .env
# 编辑 .env，至少修改 MASTER_TOKEN
docker compose up -d --build
```

打开：

```text
http://服务器地址:8787/admin
```

首次启动时，`MASTER_TOKEN` 会写入 SQLite 作为启动主 Token。之后可以在管理台创建、禁用或轮换更多主 Token。

## 订阅链接

管理台“订阅链接”页面会直接生成真实链接：

```text
原始订阅： http://host:8787/sub?token=TOKEN
指定集合： http://host:8787/sub?token=TOKEN&collections=hk,mobile
路径形式： http://host:8787/s/TOKEN/hk
Clash：    http://host:8787/sub?token=TOKEN&target=clash
Sing-Box： http://host:8787/sub?token=TOKEN&target=singbox
```

子 Token 只能访问其授权集合。每次成功获取订阅都会消耗一次使用次数，并记录访问日志。

## 首次使用建议

1. 登录后在“节点池”批量导入节点。
2. 在“集合”中按地区、用途或客户创建集合。
3. 在“Token”中创建子 Token，并勾选授权集合。
4. 在“订阅链接”中选择 Token 和集合，复制客户端链接。
5. 在“设置与备份”中下载 JSON 备份，保存 SQLite 业务数据；旧版 `db.json` 也可在同一处导入。

## 环境变量

| 变量 | 默认值 | 说明 |
|---|---|---|
| `MASTER_TOKEN` | `change-me-please` | 首次初始化数据库时写入的主 Token |
| `PORT` | `8787` | 服务监听端口 |
| `DB_PATH` | `/data/sub-hub.db` | SQLite 数据库文件 |
| `NODES_FILE` | `/data/nodes.txt` | 无可用数据库节点时的旧版回退文件 |
| `NODES` | 空 | 旧版回退节点，支持换行或 Base64 |
| `SUBCONVERTER_URL` | `http://127.0.0.1:25500` | 内部 subconverter 地址 |
| `LOG_MAX_RECORDS` | `1000` | 保留的最大访问日志条数 |

## 本地开发

```bash
go mod tidy
go test ./... -timeout 60s
MASTER_TOKEN=dev-token DB_PATH=./data/dev.db go run .
```

Windows PowerShell：

```powershell
$env:MASTER_TOKEN = "dev-token"
$env:DB_PATH = ".\data\dev.db"
go run .
```

## 数据与安全

- SQLite 默认启用 WAL 和外键约束。
- Token 在数据库中保存明文用于管理台展示，同时保存 SHA-256 指纹用于鉴权；如面向不受信任环境，建议只把管理台放在受保护的入口后。
- 备份 JSON 包含 Token，需要按密钥文件保管。
- 恢复备份会覆盖当前节点、集合、Token、授权和设置。
- `subconverter` 只在容器内部监听 `127.0.0.1:25500`，不对外暴露。
