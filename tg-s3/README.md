# tg-s3-bot

Telegram Bot-backed, S3-compatible object storage gateway written in Rust. It has no web administration panel: configuration is supplied through environment variables, `.env`, or the terminal.

**Current release scope**

- AWS Signature Version 4 header authentication for ordinary signed requests; request bodies are consumed incrementally and never buffered as one in-memory `Bytes` value.
- `ListBuckets`, bucket create/head, `ListObjects` and `ListObjectsV2` with prefix/delimiter, `PutObject`, `GetObject`, `HeadObject`, and `DeleteObject`.
- SQLite WAL metadata index; each object maps to a Telegram `file_id`.
- Telegram Bot API upload/download, configurable with `TELEGRAM_API_BASE` for a Local Bot API Server.
- XML S3 errors, ETag, content type, content length, request-body limit, and basic path-style routing. PUT writes chunks to `TEMP_DIR` while hashing, then Telegram download responses are forwarded as a streaming HTTP body.
- Dockerfile and Compose deployment with a persistent data volume and dropped Linux capabilities.

The gateway stores one uploaded object as one Telegram document. Telegram's Bot API limits therefore apply; use a Local Bot API Server if your deployment needs larger files. Multipart upload, ranged GET, CopyObject, DeleteObjects, tagging, lifecycle, and presigned URLs are deliberately not advertised as implemented yet.

## Quick start

```sh
cp .env.example .env
# Fill TELEGRAM_BOT_TOKEN, TELEGRAM_CHAT_ID, S3_ACCESS_KEY_ID and S3_SECRET_ACCESS_KEY.
mkdir -p data
# The image runs as UID 10001:
chown -R 10001:10001 data
docker compose up -d --build
```

Example AWS CLI calls (the client signs requests with SigV4):

```sh
aws --endpoint-url http://127.0.0.1:8090 s3api list-buckets
aws --endpoint-url http://127.0.0.1:8090 s3api put-object --bucket demo --key hello.txt --body ./hello.txt
aws --endpoint-url http://127.0.0.1:8090 s3api head-object --bucket demo --key hello.txt
aws --endpoint-url http://127.0.0.1:8090 s3api get-object --bucket demo --key hello.txt ./downloaded.txt
aws --endpoint-url http://127.0.0.1:8090 s3api delete-object --bucket demo --key hello.txt
```

The gateway uses path-style URLs (`/<bucket>/<key>`). Keep the endpoint behind TLS and a reverse proxy in production.

## Environment

- `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID`: required Telegram credentials/target.
- `TELEGRAM_API_BASE`: defaults to `https://api.telegram.org`.
- `DATABASE_PATH`, `TEMP_DIR`: persistent SQLite and temporary-file paths.
- `S3_ACCESS_KEY_ID`, `S3_SECRET_ACCESS_KEY`, `S3_REGION`: required S3 identity and signing region.
- `S3_REQUIRE_SIGNATURE`: defaults to `true`; only set `false` for isolated development.
- `S3_SIGNATURE_SKEW_SECONDS`: accepted `x-amz-date` clock skew, default 900.
- `MAX_OBJECT_SIZE`: maximum request/object size in bytes.

Secrets are never printed by the application. Do not commit `.env`, SQLite files, Telegram sessions, or production logs.

## Architecture

```text
S3 client -- SigV4 --> Axum router --> SQLite object index
                                      |
                                      +--> Telegram Bot API --> channel/supergroup
```

SQLite keeps bucket/object metadata, Telegram `file_id`, ETag, content type, and timestamps. Telegram keeps the payload.

## Validation

Local validation should include:

```sh
cargo fmt --check
cargo check
cargo test
```

The repository does not require a local Docker daemon for source validation. The supplied Dockerfile performs a reproducible Rust release build and the Compose file persists `/data`.

## Security and operational notes

- Use a private Telegram channel/supergroup and restrict the bot's permissions to what is required.
- Put this service behind HTTPS, rate limiting, and an upstream request-size limit.
- Back up the SQLite database together with its WAL/checkpoint state; Telegram payloads remain in the target chat.
- Rotate any credentials that have ever been exposed outside your secret store.

## v0.2: multi-tenant, chunked streaming, backup/restore, encryption

### What changed
- **Storage model**: an object is now an ordered list of Telegram messages ("chunks"),
  not one message. PUT streams the body to local disk (needed so the SigV4 signature
  can be checked before anything reaches Telegram), splitting it into `CHUNK_SIZE_BYTES`
  pieces (default 18 MiB, safely under the public Bot API's 20MB download cap), then
  uploads each chunk in order once auth passes. GET reassembles them, and Range requests
  only fetch the chunks (and byte ranges within boundary chunks) that are actually needed.
- **Multipart Upload** (CreateMultipartUpload / UploadPart / CompleteMultipartUpload /
  AbortMultipartUpload / ListParts / ListMultipartUploads) reuses the same chunk engine:
  each client-supplied part is itself staged and chunked the same way a regular PUT is.
- **CopyObject**: metadata-only when the source isn't SSE-C (reuses the same Telegram
  messages, zero data transfer). SSE-C source copies aren't implemented yet (501).
- **DeleteObjects** (batch delete) and synchronized deletion: deleting an object now
  also deletes its Telegram message(s), with reference counting so a CopyObject-shared
  chunk isn't deleted out from under another key.
- **SSE-C / SSE-S3**: AES-256-CTR, not AES-GCM. CTR is byte-seekable with zero
  ciphertext overhead, which is what makes efficient Range GET possible on encrypted
  objects. Trade-off: confidentiality without cryptographic tamper-detection (the
  SHA-256 ETag still catches accidental corruption, not deliberate tampering).
- **Multi-tenant credentials**: `credentials` table holds a root key (full access) and
  any number of scoped keys, each pinned to one `(bucket, prefix)` "root path". Managed
  via CLI subcommands (`tg-s3-bot credential add|list|rm`), run through `docker exec`.
- **Backup / restore**: a background job takes online SQLite snapshots into
  `admin/backup/` on a schedule (`BACKUP_INTERVAL_SECS`, `BACKUP_KEEP`), reachable
  through the S3 API itself (root key only). Uploading a file to `admin/recover/`
  validates it (`PRAGMA integrity_check`), takes a safety snapshot of the live DB, and
  swaps it in.
- **Docker**: only `TELEGRAM_BOT_TOKEN` / `TELEGRAM_CHAT_ID` are required. A root S3
  key is generated on first boot and written to `$DATA_DIR/ROOT_CREDENTIALS.txt` inside
  the persisted volume (never to logs).

### Known simplifications (read before relying on this in production)
- Encrypted multipart uploads must have parts uploaded in strictly increasing
  `PartNumber` order (real S3 allows any order) -- needed so the CTR keystream offset
  for each part is well-defined without buffering the whole upload.
- CopyObject on an SSE-C source object returns 501; only plaintext and SSE-S3 objects
  can be copied today.
- Presigned URLs are still not implemented (rejected, same as before).
- This patch could not be compiled in the sandbox that produced it (only rustc 1.75
  was installable there; the crate targets 1.88 for edition2024 transitive deps). It
  has been reviewed by hand but **not** by `cargo check` -- run that first and send back
  any errors.

### CLI
```
docker compose exec tg-s3-bot tg-s3-bot root-key                       # show root key
docker compose exec tg-s3-bot tg-s3-bot credential add mybucket --prefix team-a/
docker compose exec tg-s3-bot tg-s3-bot credential list
docker compose exec tg-s3-bot tg-s3-bot credential rm <access_key>
docker compose exec tg-s3-bot tg-s3-bot backup                          # manual snapshot
docker compose exec tg-s3-bot tg-s3-bot recover /data/admin/backup/tg-s3-....sqlite
```
