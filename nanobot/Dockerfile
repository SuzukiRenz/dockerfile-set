FROM suzukirenz/nanobot:latest

RUN python3 - <<'EOF'
import pathlib

# 两个路径都要patch
paths = [
    "/usr/local/lib/python3.12/site-packages/nanobot/config/loader.py",
    "/app/nanobot/config/loader.py",
]

OLD = '''with open(path, encoding="utf-8") as f:
                data = json.load(f)'''

NEW = '''with open(path, encoding="utf-8") as f:
                data = json.loads(os.path.expandvars(f.read()))'''

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
        print(f"ERROR: pattern not found in {p}, check indentation!")
        print(repr(src[src.find("json.load")-50:src.find("json.load")+80]))
        continue
    src = src.replace(OLD, NEW)
    loader.write_text(src)
    print(f"Patched OK: {p}")
EOF
