# sub-server

A single Docker container that combines:

- **sub-server** – a lightweight Go HTTP server that serves your self-built proxy
  nodes as a subscription, replacing the CF-Workers-SUB Cloudflare Worker.
- **subconverter** – the industry-standard converter that transforms raw proxy
  links into Clash / Surge / SingBox / Quantumult-X / … formats.

subconverter runs **internally only** (bound to `127.0.0.1:25500`). Its port is
never exposed to the host or the internet. All third-party aggregation logic from
the original CF-Workers-SUB is removed.

---

## Quick start

```bash
# 1. Clone / copy this directory
git clone … && cd sub-server

# 2. Configure
cp .env.example .env
$EDITOR .env           # set TOKEN (required) and optionally PORT / SUB_NAME

# 3. Add your nodes
$EDITOR data/nodes.txt # one proxy URI per line (vmess/vless/trojan/ss/…)

# 4. Build and run
docker compose up -d --build

# 5. Check logs
docker compose logs -f
```

---

## Subscription URLs

| Client type | URL |
|---|---|
| V2RayN / Xray / Nekoray (raw base64) | `http://HOST:PORT/?token=TOKEN` |
| Clash / Mihomo | `http://HOST:PORT/?token=TOKEN&target=clash` |
| Clash.Meta (new fields) | `http://HOST:PORT/?token=TOKEN&target=clash&new_name=true` |
| Surge 4 | `http://HOST:PORT/?token=TOKEN&target=surge&ver=4` |
| Surfboard | `http://HOST:PORT/?token=TOKEN&target=surfboard` |
| Quantumult X | `http://HOST:PORT/?token=TOKEN&target=quanx` |
| Loon | `http://HOST:PORT/?token=TOKEN&target=loon` |
| SingBox | `http://HOST:PORT/?token=TOKEN&target=singbox` |

`/sub` and `/` are both valid paths.

### Extra parameters (forwarded to subconverter)

| Parameter | Effect |
|---|---|
| `udp=true` | Enable UDP |
| `tfo=true` | TCP Fast Open |
| `scv=true` | Skip certificate verification |
| `sort=true` | Sort nodes by name |
| `expand=true` | Expand rule-sets inline |
| `exclude=keyword` | Remove nodes whose name contains keyword |
| `include=keyword` | Keep only nodes whose name contains keyword |

---

## Adding nodes

**Option A – file (recommended):**

Edit `data/nodes.txt` in the project directory. The file is re-read on every
request – no restart needed.

```
vmess://eyJ2Ij...
vless://uuid@host:443?...#NodeName
trojan://pass@host:443#NodeName
```

**Option B – environment variable:**

Set `NODES` in `.env` (newline-separated):

```dotenv
NODES=vless://...#Node1
trojan://...#Node2
```

When `NODES` is set, `nodes.txt` is ignored.

---

## Health check

```bash
curl http://localhost:8080/health
# {"nodes":3,"status":"ok"}
```

---

## Configuration reference

| Variable | Default | Description |
|---|---|---|
| `TOKEN` | `change-me-please` | Auth token appended to every subscription URL |
| `PORT` | `8080` | Host port the Go server listens on |
| `SUB_NAME` | `My Subscription` | Filename hint in Content-Disposition header |
| `NODES_FILE` | `/data/nodes.txt` | Path to nodes file inside the container |
| `NODES` | _(empty)_ | Inline nodes (overrides NODES_FILE when set) |

Subconverter configuration lives in `subconverter/pref.ini` and is baked into
the image at build time. Re-build after changing it:

```bash
docker compose up -d --build
```
