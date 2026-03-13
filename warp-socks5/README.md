# WARP-GO SOCKS5 Docker

Cloudflare WARP 双栈隧道 + 本地 SOCKS5 代理，Alpine 镜像，极简部署。

## 目录结构

```
.
├── Dockerfile
├── docker-compose.yml
├── entrypoint.sh
├── warp-go-amd64        ← 从下方链接下载后重命名
└── warp-go-arm64        ← 同上
```

## 准备二进制文件

从 GitLab 下载对应架构的 warp-go，放到与 Dockerfile 相同目录：

```bash
# amd64
wget -O warp-go-amd64 \
  https://gitlab.com/rwkgyg/CFwarp/-/raw/main/warp-go_1.0.8_linux_amd64

# arm64
wget -O warp-go-arm64 \
  https://gitlab.com/rwkgyg/CFwarp/-/raw/main/warp-go_1.0.8_linux_arm64

chmod +x warp-go-amd64 warp-go-arm64
```

## 快速启动

```bash
# 构建镜像并启动
docker compose up -d --build

# 查看启动日志
docker compose logs -f

# 验证 WARP 生效（输出应含 warp=on 或 warp=plus）
curl -sx socks5h://127.0.0.1:1080 \
     https://www.cloudflare.com/cdn-cgi/trace

# 查看当前 WARP 出口 IP
curl -sx socks5h://127.0.0.1:1080 https://icanhazip.com
```

## 环境变量

| 变量             | 默认值          | 说明                                   |
|------------------|-----------------|----------------------------------------|
| `SOCKS5_PORT`    | `1080`          | SOCKS5 监听端口                        |
| `SOCKS5_USER`    | 空              | 认证用户名（留空 = 无需认证）          |
| `SOCKS5_PASS`    | 空              | 认证密码                               |
| `FORCE_REGISTER` | `false`         | `true` = 强制重新注册，获取新 WARP IP  |
| `TZ`             | `Asia/Shanghai` | 时区                                   |

## 获取新的 WARP IP

```bash
docker compose down
# 修改 docker-compose.yml 中 FORCE_REGISTER=true，或：
FORCE_REGISTER=true docker compose up -d
```

## 开启 SOCKS5 认证

编辑 `docker-compose.yml`：

```yaml
environment:
  - SOCKS5_USER=myuser
  - SOCKS5_PASS=mypassword
```

使用：
```bash
curl -sx socks5h://myuser:mypassword@127.0.0.1:1080 https://icanhazip.com
```

## 常用命令

```bash
docker compose ps                    # 查看状态
docker compose logs -f               # 实时日志
docker compose restart warp-socks5  # 重启
docker compose down                  # 停止
docker compose down -v               # 停止并清除账户数据
```

## 客户端配置参考

```bash
# curl
curl -x socks5h://127.0.0.1:1080 https://api.ipify.org

# 全局代理（当前 shell）
export ALL_PROXY=socks5h://127.0.0.1:1080

# Git
git config --global http.proxy  socks5h://127.0.0.1:1080
git config --global https.proxy socks5h://127.0.0.1:1080

# Python requests
proxies = {"http": "socks5h://127.0.0.1:1080", "https": "socks5h://127.0.0.1:1080"}
```

## 故障排查

```bash
# 进入容器检查
docker exec -it warp-socks5 bash

# 容器内验证 WARP 接口
ip link show WARP
ip route

# 直接用 curl 验证（绕过 SOCKS5）
curl -s4 https://www.cloudflare.com/cdn-cgi/trace
```

## Alpine vs Debian 选择原因

| 项目         | Alpine         | Debian Slim |
|--------------|----------------|-------------|
| 镜像体积     | ~12 MB         | ~80 MB      |
| 启动速度     | 更快           | 稍慢        |
| warp-go 兼容 | gcompat 兼容层 | 原生支持    |
| 安全面       | 攻击面更小     | 标准        |

> warp-go 是 glibc 编译的二进制，Alpine 通过 `gcompat` + `libc6-compat` 提供兼容。
> 若遇到 `Exec format error`，说明二进制架构不匹配，请检查下载的文件是否正确。
