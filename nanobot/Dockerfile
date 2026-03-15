FROM suzukirenz/nanobot:latest

# 用 sed 在 load_config 函数的 json.load 之前插入 expandvars 支持
RUN python3 - <<'EOF'
import re, pathlib

path = pathlib.Path("/usr/local/lib").glob("python3*/site-packages/nanobot/config/loader.py")
loader = next(path)
src = loader.read_text()

# 确保 import os 存在
if "import os" not in src:
    src = "import os\n" + src

# 把  json.load(f)  替换为  json.loads(os.path.expandvars(f.read()))
src = src.replace(
    "with open(path) as f:\n        data = json.load(f)",
    "with open(path) as f:\n        data = json.loads(os.path.expandvars(f.read()))"
)
loader.write_text(src)
print(f"Patched: {loader}")
EOF
