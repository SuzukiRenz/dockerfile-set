use crate::db::Credential;
use crate::util::{err, esc, scope_bucket, xml};
use crate::{crypto, db, storage, telegram, App};
use axum::{
    body::Body,
    http::{HeaderMap, Method, StatusCode, Uri},
    response::{IntoResponse, Response},
};
use chrono::Utc;
use http_body_util::BodyExt;
use sha2::{Digest, Sha256};
use tracing::error;

pub async fn create(a: App, cred: Credential, h: HeaderMap, bucket: String, real_key: String, client_key: String) -> Response {
    if let Err(e) = scope_bucket(&cred, &bucket) { return e; }
    let sse = match crypto::parse_put_headers(&h) { Ok(v) => v, Err(e) => return err(StatusCode::BAD_REQUEST, "InvalidArgument", &e.to_string()) };
    if matches!(sse, crypto::SseRequest::S3) && a.cfg.sse_s3_master_key.is_none() {
        return err(StatusCode::NOT_IMPLEMENTED, "NotImplemented", "SSE-S3 requested but SSE_S3_MASTER_KEY is not configured on this server");
    }
    let (sse_alg, sse_md5) = match &sse {
        crypto::SseRequest::None => (None, None),
        crypto::SseRequest::S3 => (Some("AES256".to_owned()), None),
        crypto::SseRequest::Customer { key_md5, .. } => (Some("AES256".to_owned()), Some(key_md5.clone())),
    };
    let ct = h.get("content-type").and_then(|v| v.to_str().ok()).unwrap_or("application/octet-stream").to_owned();
    let upload_id = uuid::Uuid::new_v4().to_string();
    let p = a.cfg.database_path.clone();
    let (b2, k2, ct2, a2, m2, u2) = (bucket.clone(), real_key, ct, sse_alg, sse_md5, upload_id.clone());
    match crate::db_call(move || db::mp_create(&p, &u2, &b2, &k2, &ct2, a2.as_deref(), m2.as_deref())).await {
        Ok(_) => xml(format!("<?xml version=\"1.0\" encoding=\"UTF-8\"?><InitiateMultipartUploadResult><Bucket>{}</Bucket><Key>{}</Key><UploadId>{}</UploadId></InitiateMultipartUploadResult>", esc(&bucket), esc(&client_key), esc(&upload_id))),
        Err(e) => e,
    }
}

/// Resolve the raw key bytes an SSE-C/SSE-S3 multipart upload should use for this
/// part, validating the customer key (if any) against what CreateMultipartUpload
/// recorded. Each physical chunk is encrypted independently (own GCM nonce), so
/// there's no ordering dependency between parts for encryption purposes.
fn part_key(a: &App, mp: &db::MultipartUpload, h: &HeaderMap) -> Result<Option<[u8; 32]>, Response> {
    if mp.sse_algorithm.is_none() { return Ok(None); }
    if let Some(stored_md5) = &mp.sse_customer_key_md5 {
        match crypto::parse_get_headers(h) {
            Ok(Some((k, md5))) if &md5 == stored_md5 => Ok(Some(k)),
            Ok(Some(_)) => Err(err(StatusCode::FORBIDDEN, "AccessDenied", "customer key does not match the key used at CreateMultipartUpload")),
            Ok(None) => Err(err(StatusCode::BAD_REQUEST, "InvalidRequest", "this upload is SSE-C encrypted; supply x-amz-server-side-encryption-customer-* headers on every UploadPart")),
            Err(e) => Err(err(StatusCode::BAD_REQUEST, "InvalidArgument", &e.to_string())),
        }
    } else {
        match a.cfg.sse_s3_master_key {
            Some(k) => Ok(Some(k)),
            None => Err(err(StatusCode::INTERNAL_SERVER_ERROR, "InternalError", "SSE-S3 master key not configured")),
        }
    }
}

/// Parts may be uploaded in any order, or concurrently -- S3 permits this and real
/// clients rely on it. Each part is staged and pushed to Telegram entirely on its
/// own; ordering is only resolved at CompleteMultipartUpload, from the client's
/// declared PartNumber list (see mp_complete), never from arrival order.
pub async fn upload_part(a: App, h: HeaderMap, uri: Uri, upload_id: String, part_number: i64, body: Body) -> Response {
    if part_number < 1 || part_number > 10000 { return err(StatusCode::BAD_REQUEST, "InvalidArgument", "part number must be between 1 and 10000"); }
    let p = a.cfg.database_path.clone();
    let uid = upload_id.clone();
    let mp = match crate::db_call(move || db::mp_get(&p, &uid)).await {
        Ok(Some(m)) => m,
        Ok(None) => return err(StatusCode::NOT_FOUND, "NoSuchUpload", "The specified multipart upload does not exist"),
        Err(e) => return e,
    };
    let key = match part_key(&a, &mp, &h) { Ok(v) => v, Err(e) => return e };
    let staged = match storage::stage(body, &a.cfg, key.as_ref(), a.cfg.max_object_size).await {
        Ok(s) => s,
        Err(storage::StageError::TooLarge) => return err(StatusCode::PAYLOAD_TOO_LARGE, "EntityTooLarge", "part exceeds MAX_OBJECT_SIZE"),
        Err(_) => return err(StatusCode::INTERNAL_SERVER_ERROR, "InternalError", "Cannot stage part body"),
    };
    if let Err(e) = crate::guard(&Method::PUT, &uri, &h, &staged.sha256_hex, &a).await {
        storage::cleanup(&staged).await;
        return e;
    }
    let n_chunks = staged.chunks.len() as i64;
    let p = a.cfg.database_path.clone();
    let uid = upload_id.clone();
    let first_idx = match crate::db_call(move || db::mp_reserve(&p, &uid, n_chunks)).await {
        Ok(v) => v,
        Err(e) => { storage::cleanup(&staged).await; return e; }
    };
    let filename = mp.key.rsplit('/').next().unwrap_or(&mp.key).to_owned();
    let chunks = match storage::upload(&a.client, &a.cfg, &staged.chunks, first_idx, &filename, &mp.content_type, false).await {
        Ok(c) => c,
        Err(e) => { error!(%e, "telegram upload (multipart part)"); return err(StatusCode::BAD_GATEWAY, "TelegramError", "Telegram upload failed"); }
    };
    for ch in &chunks {
        let (p2, u2, idx, mid, fid, sz) = (a.cfg.database_path.clone(), upload_id.clone(), ch.idx, ch.message_id, ch.file_id.clone(), ch.size);
        if let Err(e) = crate::db_call(move || db::mp_insert_chunk(&p2, &u2, idx, part_number, mid, &fid, sz)).await {
            storage::rollback_uploaded(&a.client, &a.cfg, &chunks).await;
            return e;
        }
    }
    let chunk_count = chunks.len() as i64;
    let etag = staged.sha256_hex.clone();
    let (p, u2, etag2, part_size) = (a.cfg.database_path.clone(), upload_id.clone(), etag.clone(), staged.total_size);
    match crate::db_call(move || db::mp_advance(&p, &u2, part_number, &etag2, part_size, first_idx, chunk_count)).await {
        Ok(_) => ([(axum::http::header::ETAG, format!("\"{etag}\""))], StatusCode::OK).into_response(),
        Err(e) => { storage::rollback_uploaded(&a.client, &a.cfg, &chunks).await; e }
    }
}

pub async fn complete(a: App, h: HeaderMap, uri: Uri, upload_id: String, body: Body) -> Response {
    let bytes = match body.collect().await { Ok(b) => b.to_bytes(), Err(_) => return err(StatusCode::BAD_REQUEST, "InvalidRequest", "cannot read request body") };
    let payload_hash = crate::util::hex(&Sha256::digest(&bytes));
    if let Err(e) = crate::guard(&Method::POST, &uri, &h, &payload_hash, &a).await { return e; }
    let p = a.cfg.database_path.clone();
    let uid = upload_id.clone();
    let mp = match crate::db_call(move || db::mp_get(&p, &uid)).await {
        Ok(Some(m)) => m,
        Ok(None) => return err(StatusCode::NOT_FOUND, "NoSuchUpload", "The specified multipart upload does not exist"),
        Err(e) => return e,
    };
    #[derive(serde::Deserialize, Default)] struct CompleteXml { #[serde(rename = "Part", default)] part: Vec<PartXml> }
    #[derive(serde::Deserialize)] struct PartXml { #[serde(rename = "PartNumber")] part_number: i64, #[serde(rename = "ETag")] etag: String }
    let body_str = String::from_utf8_lossy(&bytes);
    let claimed: CompleteXml = quick_xml::de::from_str(&body_str).unwrap_or_default();
    if claimed.part.is_empty() { return err(StatusCode::BAD_REQUEST, "MalformedXML", "CompleteMultipartUpload body must list at least one Part"); }
    let p = a.cfg.database_path.clone();
    let uid = upload_id.clone();
    let recorded = match crate::db_call(move || db::mp_list_parts(&p, &uid)).await { Ok(v) => v, Err(e) => return e };
    let mut hasher = Sha256::new();
    let mut total_size: i64 = 0;
    let mut prev_part_number = 0i64;
    let mut part_numbers = Vec::with_capacity(claimed.part.len());
    for pc in &claimed.part {
        // This validates the *declared completion order* the client sent -- required
        // by S3 -- which is independent of the order parts actually arrived at
        // UploadPart (see upload_part above; arrival order is unconstrained).
        if pc.part_number <= prev_part_number { return err(StatusCode::BAD_REQUEST, "InvalidPartOrder", "the list of parts was not in ascending PartNumber order"); }
        prev_part_number = pc.part_number;
        let claimed_etag = pc.etag.trim_matches('"');
        let Some(rec) = recorded.iter().find(|r| r.part_number == pc.part_number) else {
            return err(StatusCode::BAD_REQUEST, "InvalidPart", &format!("part {} was never uploaded", pc.part_number));
        };
        if rec.etag != claimed_etag { return err(StatusCode::BAD_REQUEST, "InvalidPart", &format!("ETag mismatch for part {}", pc.part_number)); }
        hasher.update(rec.etag.as_bytes());
        total_size += rec.size;
        part_numbers.push(pc.part_number);
    }
    let final_etag = format!("{}-{}", crate::util::hex(&hasher.finalize()), claimed.part.len());
    let now = Utc::now().timestamp();
    let p = a.cfg.database_path.clone();
    let uid = upload_id.clone();
    let (mp2, etag2) = (mp.clone(), final_etag.clone());
    match crate::db_call(move || db::mp_complete(&p, &uid, &mp2, &part_numbers, total_size, &etag2, now)).await {
        Ok(_) => xml(format!("<?xml version=\"1.0\" encoding=\"UTF-8\"?><CompleteMultipartUploadResult><Bucket>{}</Bucket><Key>{}</Key><ETag>&quot;{}&quot;</ETag></CompleteMultipartUploadResult>", esc(&mp.bucket), esc(&mp.key), esc(&final_etag))),
        Err(e) => e,
    }
}

pub async fn abort(a: App, upload_id: String) -> Response {
    let p = a.cfg.database_path.clone();
    let uid = upload_id.clone();
    let chunks = match crate::db_call(move || db::mp_abort(&p, &uid)).await { Ok(c) => c, Err(e) => return e };
    let ids: Vec<i64> = chunks.into_iter().map(|c| c.message_id).collect();
    if !ids.is_empty() { telegram::delete_messages(&a.client, &a.cfg.bot_token, &a.cfg.chat_id, ids).await; }
    StatusCode::NO_CONTENT.into_response()
}

pub async fn list_parts(a: App, upload_id: String) -> Response {
    let p = a.cfg.database_path.clone();
    match crate::db_call(move || db::mp_list_parts(&p, &upload_id)).await {
        Ok(parts) => {
            let items = parts.iter().map(|pt| format!("<Part><PartNumber>{}</PartNumber><ETag>&quot;{}&quot;</ETag><Size>{}</Size></Part>", pt.part_number, esc(&pt.etag), pt.size)).collect::<String>();
            xml(format!("<?xml version=\"1.0\" encoding=\"UTF-8\"?><ListPartsResult>{items}</ListPartsResult>"))
        }
        Err(e) => e,
    }
}

pub async fn list_uploads(a: App, bucket: String) -> Response {
    let p = a.cfg.database_path.clone();
    match crate::db_call(move || db::mp_list_uploads(&p, &bucket)).await {
        Ok(ups) => {
            let items = ups.iter().map(|u| format!("<Upload><Key>{}</Key><UploadId>{}</UploadId></Upload>", esc(&u.key), esc(&u.upload_id))).collect::<String>();
            xml(format!("<?xml version=\"1.0\" encoding=\"UTF-8\"?><ListMultipartUploadsResult>{items}</ListMultipartUploadsResult>"))
        }
        Err(e) => e,
    }
}
