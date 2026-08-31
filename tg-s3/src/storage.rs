use crate::{config::Config, crypto, telegram};
use axum::body::Body;
use http_body_util::BodyExt;
use reqwest::Client;
use sha2::{Digest, Sha256};
use std::path::PathBuf;
use tokio::fs;

pub struct StagedChunk { pub path: PathBuf, pub plain_size: i64 }
pub struct Staged { pub chunks: Vec<StagedChunk>, pub total_size: i64, pub sha256_hex: String }

#[derive(Debug)]
pub enum StageError { TooLarge, Read, Write }

fn chunk_dir(cfg: &Config) -> PathBuf { cfg.temp_dir.join(uuid::Uuid::new_v4().to_string()) }

async fn write_chunk(dir: &std::path::Path, idx: i64, plain: &[u8], key: Option<&[u8; crypto::KEY_LEN]>) -> Result<StagedChunk, StageError> {
    let on_disk = match key { Some(k) => crypto::encrypt_chunk(k, plain), None => plain.to_vec() };
    let path = dir.join(idx.to_string());
    fs::write(&path, &on_disk).await.map_err(|_| StageError::Write)?;
    Ok(StagedChunk { path, plain_size: plain.len() as i64 })
}

/// Single-shot staging for a regular (non-multipart) PUT: read `body` fully onto local
/// disk, splitting into `chunk_size`-byte pieces and hashing the plaintext as it
/// arrives (for the ETag / SigV4 payload hash). If `key` is given, each *complete*
/// chunk is encrypted in one AES-256-GCM operation before being written to disk (see
/// crypto.rs for why per-chunk rather than a continuous stream cipher). The final,
/// possibly-partial chunk is flushed as-is -- there's no "next call" to complete it
/// with, unlike multipart parts (see `stage_part` below).
///
/// Deliberately does not touch Telegram: the SigV4 signature can only be checked once
/// the whole body has been hashed, so nothing gets pushed to the Telegram channel
/// until the caller has verified the request is authentic. Callers must delete the
/// staged files (see `cleanup`) on the auth-failure path.
pub async fn stage(mut body: Body, cfg: &Config, key: Option<&[u8; crypto::KEY_LEN]>, max_size: usize) -> Result<Staged, StageError> {
    let stage_dir = chunk_dir(cfg);
    fs::create_dir_all(&stage_dir).await.map_err(|_| StageError::Write)?;
    let mut chunks = Vec::new();
    let mut hasher = Sha256::new();
    let mut total: usize = 0;
    let mut cur_idx: i64 = 0;
    let mut buf: Vec<u8> = Vec::with_capacity(cfg.chunk_size.min(1 << 20));

    while let Some(frame) = body.frame().await {
        let frame = frame.map_err(|_| StageError::Read)?;
        let Ok(data) = frame.into_data() else { continue };
        if data.is_empty() { continue; }
        total = total.saturating_add(data.len());
        if total > max_size { return Err(StageError::TooLarge); }
        hasher.update(&data);
        buf.extend_from_slice(&data);
        while buf.len() >= cfg.chunk_size {
            let plain: Vec<u8> = buf.drain(..cfg.chunk_size).collect();
            chunks.push(write_chunk(&stage_dir, cur_idx, &plain, key).await?);
            cur_idx += 1;
        }
    }
    if !buf.is_empty() {
        chunks.push(write_chunk(&stage_dir, cur_idx, &buf, key).await?);
    }
    Ok(Staged { chunks, total_size: total as i64, sha256_hex: hex(&hasher.finalize()) })
}

/// Result of staging one multipart UploadPart request. Unlike `stage`, this never
/// flushes a trailing partial chunk on its own -- `tail` holds whatever didn't reach a
/// full `chunk_size` yet, for the caller to persist and prepend to the *next* part (or
/// flush at CompleteMultipartUpload if this was the last one). This is what lets the
/// object's on-Telegram chunk size stay pinned to `CHUNK_SIZE_BYTES` regardless of
/// what part size the S3 client chose to use.
pub struct PartStaged { pub full_chunks: Vec<StagedChunk>, pub tail: Vec<u8>, pub part_size: i64, pub sha256_hex: String }

/// Stage one multipart part. `pending_tail` is the leftover plaintext from the
/// previous part on this upload (empty if this is the first part, or if the previous
/// part happened to land exactly on a chunk boundary). The hash and `part_size`
/// returned cover *only* the bytes actually received in this HTTP request body -- not
/// `pending_tail` -- since that's what the client signed and what this part's own
/// ETag must reflect; `pending_tail` only affects how bytes get grouped into physical
/// Telegram chunks, never the part-level accounting the S3 protocol exposes.
pub async fn stage_part(mut body: Body, cfg: &Config, key: Option<&[u8; crypto::KEY_LEN]>, pending_tail: Vec<u8>, max_size: usize) -> Result<PartStaged, StageError> {
    let stage_dir = chunk_dir(cfg);
    fs::create_dir_all(&stage_dir).await.map_err(|_| StageError::Write)?;
    let mut full_chunks = Vec::new();
    let mut hasher = Sha256::new();
    let mut part_size: usize = 0;
    let mut cur_idx: i64 = 0;
    let mut buf = pending_tail;

    while let Some(frame) = body.frame().await {
        let frame = frame.map_err(|_| StageError::Read)?;
        let Ok(data) = frame.into_data() else { continue };
        if data.is_empty() { continue; }
        part_size = part_size.saturating_add(data.len());
        if part_size > max_size { return Err(StageError::TooLarge); }
        hasher.update(&data);
        buf.extend_from_slice(&data);
        while buf.len() >= cfg.chunk_size {
            let plain: Vec<u8> = buf.drain(..cfg.chunk_size).collect();
            full_chunks.push(write_chunk(&stage_dir, cur_idx, &plain, key).await?);
            cur_idx += 1;
        }
    }
    Ok(PartStaged { full_chunks, tail: buf, part_size: part_size as i64, sha256_hex: hex(&hasher.finalize()) })
}

/// Flush whatever's left in a pending tail (at CompleteMultipartUpload, once no more
/// parts are coming) as the final physical chunk. Returns None for an empty tail.
pub async fn flush_tail(cfg: &Config, key: Option<&[u8; crypto::KEY_LEN]>, tail: &[u8]) -> Result<Option<StagedChunk>, StageError> {
    if tail.is_empty() { return Ok(None); }
    let stage_dir = chunk_dir(cfg);
    fs::create_dir_all(&stage_dir).await.map_err(|_| StageError::Write)?;
    Ok(Some(write_chunk(&stage_dir, 0, tail, key).await?))
}

pub async fn cleanup(staged: &Staged) { cleanup_chunks(&staged.chunks).await }
pub async fn cleanup_chunks(chunks: &[StagedChunk]) {
    if let Some(first) = chunks.first() {
        if let Some(dir) = first.path.parent() { let _ = fs::remove_dir_all(dir).await; }
    }
}

/// Push staged chunks to Telegram in order, only called after auth passes. Cleans up
/// local files as it goes so a large object never sits fully duplicated on disk for
/// long. `filename` is always suffixed with the chunk's position in the *whole
/// object* (`start_idx + i`), not just within this call -- a multipart upload calls
/// this once per batch of newly-completed chunks, so "only one chunk in this call"
/// does not mean "only one chunk in the object"; naming by global index keeps chunks
/// visibly identifiable as fragments in the Telegram channel either way.
pub async fn upload(client: &Client, cfg: &Config, chunks: &[StagedChunk], start_idx: i64, filename: &str, content_type: &str, single_chunk_object: bool) -> Result<Vec<crate::db::ChunkRef>, telegram::TgError> {
    let mut out = Vec::with_capacity(chunks.len());
    for (i, ch) in chunks.iter().enumerate() {
        let global_idx = start_idx + i as i64;
        let name = if single_chunk_object && chunks.len() == 1 { filename.to_owned() } else { format!("{filename}.part{global_idx:05}") };
        let up = telegram::upload(client, &cfg.bot_token, &cfg.chat_id, &ch.path, &name, content_type).await?;
        out.push(crate::db::ChunkRef { idx: global_idx, message_id: up.message_id, file_id: up.file_id, size: ch.plain_size });
        let _ = fs::remove_file(&ch.path).await;
    }
    if let Some(dir) = chunks.first().and_then(|c| c.path.parent()) { let _ = fs::remove_dir(dir).await; }
    Ok(out)
}

/// Best-effort compensation: delete chunks that were already pushed to Telegram when a
/// later step (SQLite metadata write) fails, so a failed request doesn't silently leak
/// storage in the channel. Never called on the happy path.
pub async fn rollback_uploaded(client: &Client, cfg: &Config, chunks: &[crate::db::ChunkRef]) {
    let ids: Vec<i64> = chunks.iter().map(|c| c.message_id).collect();
    if !ids.is_empty() { telegram::delete_messages(client, &cfg.bot_token, &cfg.chat_id, ids).await; }
}

// ---- multipart pending-tail persistence (survives across separate HTTP requests) ----

fn pending_tail_path(cfg: &Config, upload_id: &str) -> PathBuf { cfg.temp_dir.join("mp-pending").join(format!("{upload_id}.bin")) }

pub async fn load_pending_tail(cfg: &Config, upload_id: &str) -> Vec<u8> {
    fs::read(pending_tail_path(cfg, upload_id)).await.unwrap_or_default()
}
pub async fn save_pending_tail(cfg: &Config, upload_id: &str, tail: &[u8]) -> std::io::Result<()> {
    let path = pending_tail_path(cfg, upload_id);
    if tail.is_empty() {
        let _ = fs::remove_file(&path).await;
        return Ok(());
    }
    if let Some(dir) = path.parent() { fs::create_dir_all(dir).await?; }
    fs::write(&path, tail).await
}
pub async fn discard_pending_tail(cfg: &Config, upload_id: &str) { let _ = fs::remove_file(pending_tail_path(cfg, upload_id)).await; }

fn hex(b: &[u8]) -> String { b.iter().map(|x| format!("{x:02x}")).collect() }
