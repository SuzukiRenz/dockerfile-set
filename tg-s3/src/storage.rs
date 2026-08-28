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

/// Phase 1: read `body` fully onto local disk, splitting into `chunk_size`-byte pieces,
/// hashing the plaintext as it arrives (for the ETag / SigV4 payload hash), and -- if
/// `key` is given -- encrypting each *complete* chunk in one AES-256-GCM operation
/// before it's written to disk (see crypto.rs for why per-chunk rather than a
/// continuous stream cipher). Each chunk buffers in memory up to `chunk_size` bytes
/// (18 MiB by default) before being flushed; that's the only place this holds more
/// than one chunk's worth of the object at a time.
///
/// Deliberately does not touch Telegram: the SigV4 signature can only be checked once
/// the whole body/part has been hashed, so nothing gets pushed to the Telegram channel
/// until the caller has verified the request is authentic. Callers must delete the
/// staged files (see `cleanup`) on the auth-failure path.
pub async fn stage(mut body: Body, cfg: &Config, key: Option<&[u8; crypto::KEY_LEN]>, max_size: usize) -> Result<Staged, StageError> {
    let stage_dir = cfg.temp_dir.join(uuid::Uuid::new_v4().to_string());
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
            let on_disk = match key { Some(k) => crypto::encrypt_chunk(k, &plain), None => plain.clone() };
            let path = stage_dir.join(cur_idx.to_string());
            fs::write(&path, &on_disk).await.map_err(|_| StageError::Write)?;
            chunks.push(StagedChunk { path, plain_size: plain.len() as i64 });
            cur_idx += 1;
        }
    }
    if !buf.is_empty() {
        let on_disk = match key { Some(k) => crypto::encrypt_chunk(k, &buf), None => buf.clone() };
        let path = stage_dir.join(cur_idx.to_string());
        fs::write(&path, &on_disk).await.map_err(|_| StageError::Write)?;
        chunks.push(StagedChunk { path, plain_size: buf.len() as i64 });
    }
    Ok(Staged { chunks, total_size: total as i64, sha256_hex: hex(&hasher.finalize()) })
}

pub async fn cleanup(staged: &Staged) {
    if let Some(first) = staged.chunks.first() {
        if let Some(dir) = first.path.parent() { let _ = fs::remove_dir_all(dir).await; }
    }
}

/// Phase 2: push each staged chunk (already encrypted on disk, if SSE was requested)
/// to Telegram in order, only called after auth passes. Cleans up local files as it
/// goes so a large object never sits fully duplicated on disk for long.
pub async fn upload(client: &Client, cfg: &Config, staged: &Staged, start_idx: i64, filename: &str, content_type: &str) -> Result<Vec<crate::db::ChunkRef>, telegram::TgError> {
    let mut out = Vec::with_capacity(staged.chunks.len());
    for (i, ch) in staged.chunks.iter().enumerate() {
        let name = if staged.chunks.len() == 1 { filename.to_owned() } else { format!("{filename}.part{i:05}") };
        let up = telegram::upload(client, &cfg.bot_token, &cfg.chat_id, &ch.path, &name, content_type).await?;
        out.push(crate::db::ChunkRef { idx: start_idx + i as i64, message_id: up.message_id, file_id: up.file_id, size: ch.plain_size });
        let _ = fs::remove_file(&ch.path).await;
    }
    if let Some(dir) = staged.chunks.first().and_then(|c| c.path.parent()) { let _ = fs::remove_dir(dir).await; }
    Ok(out)
}

/// Best-effort compensation: delete chunks that were already pushed to Telegram when a
/// later step (SQLite metadata write) fails, so a failed request doesn't silently leak
/// storage in the channel. Never called on the happy path.
pub async fn rollback_uploaded(client: &Client, cfg: &Config, chunks: &[crate::db::ChunkRef]) {
    let ids: Vec<i64> = chunks.iter().map(|c| c.message_id).collect();
    if !ids.is_empty() { telegram::delete_messages(client, &cfg.bot_token, &cfg.chat_id, ids).await; }
}

fn hex(b: &[u8]) -> String { b.iter().map(|x| format!("{x:02x}")).collect() }
