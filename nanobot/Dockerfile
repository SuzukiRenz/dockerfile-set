FROM suzukirenz/nanobot:latest

RUN python3 - <<'EOF'
import pathlib

paths = [
    "/usr/local/lib/python3.12/site-packages/nanobot/config/loader.py",
    "/app/nanobot/config/loader.py",
]

OLD = '''with open(path, encoding="utf-8") as f:
                data = json.load(f)'''

# 不依赖 import os，直接用 __import__ 内联调用
NEW = '''with open(path, encoding="utf-8") as f:
                data = json.loads(__import__('os').path.expandvars(f.read()))'''

for p in paths:
    loader = pathlib.Path(p)
    if not loader.exists():
        print(f"Skip (not found): {p}")
        continue
    src = loader.read_text()
    if "expandvars" in src:
        # 如果是上次错误补丁的残留，先清掉再重打
        src = src.replace(
            "data = json.loads(os.path.expandvars(f.read()))",
            "data = json.loads(__import__('os').path.expandvars(f.read()))"
        )
        loader.write_text(src)
        print(f"Fixed existing patch: {p}")
        continue
    if OLD not in src:
        print(f"ERROR: pattern not found in {p}")
        print(repr(src[src.find("json.load")-80:src.find("json.load")+100]))
        continue
    src = src.replace(OLD, NEW)
    loader.write_text(src)
    print(f"Patched OK: {p}")
EOF
