#!/bin/bash
set -e

REPO_DIR="/app"
REPO_URL="https://github.com/HKUDS/nanobot.git"

echo "=== [nanobot] Pulling latest code... ==="

# 如果不是 git 仓库，先转换为 git 仓库
if [ ! -d "$REPO_DIR/.git" ]; then
  echo "=== [nanobot] No git repo found, initializing... ==="
  cd "$REPO_DIR"
  git init
  git remote add origin "$REPO_URL"
  git fetch origin main
  git reset --hard origin/main
  OLD_HEAD=""
else
  cd "$REPO_DIR"
  OLD_HEAD=$(git rev-parse HEAD)
  git fetch origin main
  git reset --hard origin/main
fi

if [ "$OLD_HEAD" != "$NEW_HEAD" ]; then
  echo "=== [nanobot] Reinstalling Python package... ==="
  uv pip install --system --no-cache .

  # bridge 有变动才重新编译
  if git diff --name-only "$OLD_HEAD" "$NEW_HEAD" | grep -q "^bridge/"; then
    echo "=== [nanobot] Bridge changed, rebuilding... ==="
    cd /app/bridge && npm install && npm run build && cd /app
  fi
fi

NEW_HEAD=$(git rev-parse HEAD)

if [ "$OLD_HEAD" != "$NEW_HEAD" ]; then
  echo "=== [nanobot] Code updated ($OLD_HEAD -> $NEW_HEAD), reinstalling... ==="
  uv pip install --system --no-cache .
else
  echo "=== [nanobot] Already up to date, skipping reinstall. ==="
fi

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
        print(f"Already patched: {p}")
        continue
    if OLD not in src:
        print(f"WARNING: Pattern not found in {p} — upstream may have changed loader!")
        continue
    src = src.replace(OLD, NEW)
    loader.write_text(src)
    print(f"Patched OK: {p}")
PYEOF

echo "=== [nanobot] Starting... ==="
exec nanobot "$@"
