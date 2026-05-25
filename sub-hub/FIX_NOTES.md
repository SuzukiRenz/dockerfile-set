# sub-hub db.json node source fix

Date: 2026-05-25

## What changed

- Subscription generation now reads enabled nodes from WebUI database first: `DB_PATH` (default `/data/db.json`).
- Legacy fallback is preserved:
  1. `NODES` environment variable
  2. `NODES_FILE` (default `/data/nodes.txt`)
- `/health`, `/internal/nodes`, raw `/sub`, and converted `/sub?target=...` now share the same node source.
- Empty lines, comments, and duplicate node URIs are skipped.
- `NODES` and `NODES_FILE` can contain normal line/comma separated node lists; base64 subscription text is also accepted when it decodes into node URIs.
- Fixed WebUI enable/disable toggle: backend now supports `PATCH /api/nodes/{id}`.
- Fixed edit-node behavior so editing URI/name does not accidentally reset `enabled`, `sort_order`, or `created_at`.
- Hardened node table rendering against HTML injection from imported node names/URIs.

## Why

Previously, WebUI-imported Shadowrocket nodes were stored in `/data/db.json`, but the active subscription path only used `NODES` or `/data/nodes.txt`. If `/data/nodes.txt` did not exist, Passwall/OpenWrt and other clients received empty or invalid converted subscriptions.

## Deploy

From the `sub-hub` directory on the server:

```bash
docker compose up -d --build
```

Then verify:

```bash
curl -fsS 'http://127.0.0.1:8787/health'
curl -fsS 'http://127.0.0.1:8787/sub?token=YOUR_TOKEN' | head -c 120
```

Expected: `/health` shows the enabled node count from WebUI, and `/sub` returns a non-empty base64 subscription.

## Notes

- No plaintext secrets are included in this package.
- Local nanobot environment did not have Go/gofmt installed, so syntax/build should be validated through the Dockerfile build stage (`golang:1.22-alpine`).


## 2026-05-26 note: local subconverter listener

- `127.0.0.1:25500` is the **subconverter** service inside the same container/network namespace.
- It is only reachable **from inside the container** and is not meant to be accessed from outside.
- `waitForSubconverter()` in `go/main.go` checks readiness; actual conversion requests still retry per request.
- If you run `subconverter` as a separate container, change this to its container DNS name, for example `subconverter:25500`.
