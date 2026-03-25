#!/bin/bash
set -e

REPO_DIR="/app"
REPO_URL="https://github.com/HKUDS/nanobot.git"

echo "=== [nanobot] Pulling latest code... ==="
cd "$REPO_DIR"

OLD_HEAD=$(git rev-parse HEAD 2>/dev/null || echo "none")
git fetch origin main
git reset --hard origin/main
NEW_HEAD=$(git rev-parse HEAD)

if [ "$OLD_HEAD" != "$NEW_HEAD" ]; then
  echo "=== [nanobot] Code updated, reinstalling... ==="
  uv pip install --system --no-cache .
fi

# ✅ 每次启动都重新打 patch（幂等，有则修正，无则跳过）
echo "=== [nanobot] Applying expandvars patch to loader.py... ==="
python3 - <<'PYEOF'
import pathlib

paths = [
    "/usr/local/lib/python3.12/site-packages/nanobot/config/loader.py",
    "/app/nanobot/config/loader.py",
]
OLD = '''with open(path, encoding="utf-8") as f:
                data = json.load(f)'''
NEW = '''with open(path, encoding="utf-8") as f:
                data = json.loads(__import__('os').path.expandvars(f.read()))'''

for p in paths:
    loader = pathlib.Path(p)
    if not loader.exists():
        print(f"Skip (not found): {p}")
        continue
    src = loader.read_text()
    if "expandvars" in src:
        print(f"Already patched (or needs fix check): {p}")
        # 修正旧版错误 patch 残留
        if "os.path.expandvars" in src and "__import__" not in src:
            src = src.replace(
                "data = json.loads(os.path.expandvars(f.read()))",
                "data = json.loads(__import__('os').path.expandvars(f.read()))"
            )
            loader.write_text(src)
            print(f"  Fixed legacy patch: {p}")
        continue
    if OLD not in src:
        print(f"WARNING: Pattern not found in {p} — upstream may have changed the loader!")
        print("  Please verify loader.py manually.")
        continue
    src = src.replace(OLD, NEW)
    loader.write_text(src)
    print(f"Patched OK: {p}")
PYEOF

echo "=== [nanobot] Starting... ==="
exec nanobot "$@"
```

---

## 额外保障：检测 patch 失效时发出警告

如果官方某天重构了 `loader.py`（修改了那段 `json.load` 的写法），上面的脚本会打印：
```
WARNING: Pattern not found in /path/loader.py — upstream may have changed the loader!
