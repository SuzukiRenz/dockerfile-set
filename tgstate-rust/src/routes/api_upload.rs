use std::sync::Arc;

use axum::body::Body;
use axum::extract::{Multipart, State};
use axum::http::HeaderMap;
use axum::response::IntoResponse;
use axum::routing::post;
use axum::{Json, Router};
use bytes::{Bytes, BytesMut};
use futures::StreamExt;

use crate::auth::{self, COOKIE_NAME};
use crate::config;
use crate::constants;
use crate::database;
use crate::error::http_error;
use crate::state::AppState;
use crate::storage;
use crate::telegram::service::TelegramService;
use crate::telegram::types::Message;

#[derive(Debug, Default)]
struct UploadAuthProgress {
    auth_verified: bool,
}

#[derive(Debug, PartialEq, Eq)]
enum UploadFieldError {
    FileBeforeAuth,
}

fn current_storage_backend(app_settings: &config::AppSettingsMap) -> &str {
    app_settings
        .get("STORAGE_BACKEND")
        .and_then(|v| v.as_deref())
        .unwrap_or(constants::STORAGE_BACKEND_TELEGRAM)
}

fn advance_upload_auth_state(
    mut state: UploadAuthProgress,
    prechecked_auth: bool,
    auth_optional: bool,
    field_name: &str,
    _field_value: Option<&str>,
) -> Result<UploadAuthProgress, UploadFieldError> {
    if prechecked_auth || auth_optional {
        state.auth_verified = true;
        return Ok(state);
    }

    if field_name == "key" {
        state.auth_verified = true;
        return Ok(state);
    }

    if field_name == "file" && !state.auth_verified {
        return Err(UploadFieldError::FileBeforeAuth);
    }

    Ok(state)
}

pub(crate) fn sanitize_filename(raw: &str) -> String {
    let name = std::path::Path::new(raw)
        .file_name()
        .and_then(|n| n.to_str())
        .unwrap_or("upload");
    let clean: String = name
        .chars()
        .filter(|c| !c.is_control() && *c != '\0')
        .collect();
    if clean.is_empty() {
        return "upload".to_string();
    }
    if clean.len() <= 255 {
        return clean;
    }

    let mut cutoff = 0;
    for (idx, _) in clean.char_indices() {
        if idx > 255 {
            break;
        }
        cutoff = idx;
    }
    clean[..cutoff].to_string()
}

async fn upload_file(
    State(state): State<Arc<AppState>>,
    headers: HeaderMap,
    mut multipart: Multipart,
) -> Result<impl IntoResponse, impl IntoResponse> {
    let app_settings = config::get_app_settings(&state.settings, &state.db_pool);
    let backend = current_storage_backend(&app_settings);

    let bot_token = app_settings
        .get("BOT_TOKEN")
        .and_then(|v| v.as_deref())
        .unwrap_or("");
    let channel_name = app_settings
        .get("CHANNEL_NAME")
        .and_then(|v| v.as_deref())
        .unwrap_or("");

    if backend == constants::STORAGE_BACKEND_TELEGRAM
        && (bot_token.is_empty() || channel_name.is_empty())
    {
        return Err(http_error(
            axum::http::StatusCode::SERVICE_UNAVAILABLE,
            "upload config missing",
            "cfg_missing",
        ));
    }

    let has_referer = headers.get("referer").is_some();
    let cookie_value = headers
        .get("cookie")
        .and_then(|v| v.to_str().ok())
        .and_then(|cookies| {
            cookies.split(';').find_map(|c| {
                let c = c.trim();
                c.strip_prefix(&format!("{}=", COOKIE_NAME))
                    .map(|v| v.to_string())
            })
        });

    let picgo_key = app_settings.get("PICGO_API_KEY").and_then(|v| v.as_deref());
    let pass_word = app_settings.get("PASS_WORD").and_then(|v| v.as_deref());
    let pass_word_hash_ref = pass_word;

    let header_key = headers
        .get("x-api-key")
        .and_then(|v| v.to_str().ok())
        .map(|s| s.to_string());
    let auth_optional = picgo_key.map_or(true, |k| k.is_empty())
        && pass_word_hash_ref.map_or(true, |p| p.is_empty());

    let session_token_owned = app_settings.get("SESSION_TOKEN").and_then(|v| v.clone());
    let cookie_valid = match (cookie_value.as_deref(), session_token_owned.as_deref()) {
        (Some(c), Some(t)) if !c.is_empty() && !t.is_empty() => auth::secure_compare(c, t),
        _ => false,
    };
    let prechecked_auth = cookie_valid
        || auth::ensure_upload_auth(
            has_referer,
            None,
            picgo_key,
            pass_word_hash_ref,
            session_token_owned.as_deref(),
            header_key.as_deref(),
        )
        .is_ok();

    let mut form_key: Option<String> = None;
    let mut upload_result: Option<Result<String, String>> = None;
    let mut auth_progress = UploadAuthProgress {
        auth_verified: prechecked_auth || auth_optional,
    };

    let tg_service = if backend == constants::STORAGE_BACKEND_TELEGRAM {
        Some(TelegramService::new(
            bot_token.to_string(),
            channel_name.to_string(),
            state.http_client.clone(),
        ))
    } else {
        None
    };

    while let Ok(Some(field)) = multipart.next_field().await {
        let name = field.name().unwrap_or("").to_string();
        if name == "key" {
            let key_text = field.text().await.ok();
            if !auth_progress.auth_verified {
                if let Err((_, msg, code)) = auth::ensure_upload_auth(
                    has_referer,
                    None,
                    picgo_key,
                    pass_word_hash_ref,
                    session_token_owned.as_deref(),
                    key_text.as_deref(),
                ) {
                    return Err(http_error(axum::http::StatusCode::UNAUTHORIZED, msg, code));
                }
            }
            auth_progress = advance_upload_auth_state(
                auth_progress,
                prechecked_auth,
                auth_optional,
                &name,
                key_text.as_deref(),
            )
            .map_err(|_| {
                http_error(
                    axum::http::StatusCode::UNAUTHORIZED,
                    "upload auth required before file field",
                    "file_before_auth",
                )
            })?;
            form_key = key_text;
        } else if name == "file" {
            auth_progress = advance_upload_auth_state(
                auth_progress,
                prechecked_auth,
                auth_optional,
                &name,
                None,
            )
            .map_err(|_| {
                http_error(
                    axum::http::StatusCode::UNAUTHORIZED,
                    "upload auth required before file field",
                    "file_before_auth",
                )
            })?;
            let raw_filename = field.file_name().unwrap_or("upload").to_string();
            let filename = sanitize_filename(&raw_filename);

            upload_result = Some(if backend == constants::STORAGE_BACKEND_S3 {
                let data = field.bytes().await.map_err(|e| e.to_string());
                match data {
                    Ok(bytes) => {
                        storage::s3::upload_bytes(&state, &filename, bytes.to_vec(), bytes.len() as i64)
                            .await
                    }
                    Err(e) => Err(e),
                }
            } else {
                stream_upload_to_telegram(
                    tg_service.as_ref().expect("telegram backend requires service"),
                    field,
                    &filename,
                    &state.db_pool,
                    "",
                )
                .await
            });
        }
    }

    if !prechecked_auth {
        let final_key = form_key.as_deref();
        if let Err((_, msg, code)) = auth::ensure_upload_auth(
            has_referer,
            None,
            picgo_key,
            pass_word_hash_ref,
            session_token_owned.as_deref(),
            final_key,
        ) {
            return Err(http_error(axum::http::StatusCode::UNAUTHORIZED, msg, code));
        }
    }

    let short_id = upload_result
        .ok_or_else(|| {
            http_error(
                axum::http::StatusCode::BAD_REQUEST,
                "no file provided",
                "no_file",
            )
        })?
        .map_err(|e| {
            tracing::error!("upload failed: {}", e);
            http_error(
                axum::http::StatusCode::INTERNAL_SERVER_ERROR,
                "upload failed",
                "upload_failed",
            )
        })?;

    let download_path = format!("/d/{}", short_id);
    Ok(Json(serde_json::json!({
        "file_id": short_id,
        "short_id": short_id,
        "download_path": download_path,
        "path": download_path,
        "url": download_path,
    })))
}

fn extract_uploaded_media(message: Message, default_filename: &str) -> Result<(String, i64), String> {
    if let Some(doc) = message.document {
        return Ok((format!("{}:{}", message.message_id, doc.file_id), doc.file_size.unwrap_or(0)));
    }

    if let Some(video) = message.video {
        return Ok((format!("{}:{}", message.message_id, video.file_id), video.file_size.unwrap_or(0)));
    }

    Err(format!("No document or video in Telegram response for {}", default_filename))
}

async fn stream_upload_to_telegram(
    tg_service: &TelegramService,
    mut field: axum::extract::multipart::Field<'_>,
    filename: &str,
    db_pool: &database::DbPool,
    folder_path: &str,
) -> Result<String, String> {
    let chunk_size = constants::TELEGRAM_CHUNK_SIZE;
    let mut buffer = BytesMut::with_capacity(chunk_size);
    let mut total_size: usize = 0;
    let mut chunk_ids: Vec<String> = Vec::new();
    let mut first_message_id: Option<i64> = None;
    let mut chunk_num: u32 = 0;

    while let Ok(Some(bytes)) = field.chunk().await {
        buffer.extend_from_slice(&bytes);
        total_size += bytes.len();

        while buffer.len() >= chunk_size {
            chunk_num += 1;
            let chunk_data = buffer.split_to(chunk_size).freeze().to_vec();
            let chunk_name = format!("{}.part{}", filename, chunk_num);

            let message = tg_service
                .send_document_raw(chunk_data, &chunk_name, first_message_id)
                .await?;

            if first_message_id.is_none() {
                first_message_id = Some(message.message_id);
            }

            let (composite_id, _) = extract_uploaded_media(message, &chunk_name)?;
            chunk_ids.push(composite_id);
        }
    }

    if buffer.is_empty() && chunk_ids.is_empty() {
        return Err("empty file".into());
    }

    if chunk_ids.is_empty() {
        let data = buffer.freeze().to_vec();
        let message = tg_service.send_document_raw(data, filename, None).await?;

        let (composite_id, telegram_size) = extract_uploaded_media(message, filename)?;

        let short_id = database::add_file_metadata_in_folder(
            db_pool,
            filename,
            &composite_id,
            if telegram_size > 0 { telegram_size } else { total_size as i64 },
            folder_path,
        )
            .map_err(|e| e.to_string())?;
        return Ok(short_id);
    }

    if !buffer.is_empty() {
        chunk_num += 1;
        let chunk_data = buffer.freeze().to_vec();
        let chunk_name = format!("{}.part{}", filename, chunk_num);

        let message = tg_service
            .send_document_raw(chunk_data, &chunk_name, first_message_id)
            .await?;

        let (composite_id, _) = extract_uploaded_media(message, &chunk_name)?;
        chunk_ids.push(composite_id);
    }

    let mut manifest = String::from("tgstate-blob\n");
    manifest.push_str(filename);
    manifest.push('\n');
    for cid in &chunk_ids {
        manifest.push_str(cid);
        manifest.push('\n');
    }

    let manifest_name = format!("{}.manifest", filename);
    let message = tg_service
        .send_document_raw(manifest.into_bytes(), &manifest_name, first_message_id)
        .await?;

    let (manifest_composite, _) = extract_uploaded_media(message, &manifest_name)?;

    let short_id = database::add_file_metadata_in_folder(
        db_pool,
        filename,
        &manifest_composite,
        total_size as i64,
        folder_path,
    )
    .map_err(|e| e.to_string())?;
    Ok(short_id)
}

pub(crate) async fn upload_bytes_to_telegram(
    tg_service: &TelegramService,
    filename: &str,
    data: Vec<u8>,
    db_pool: &database::DbPool,
    folder_path: &str,
) -> Result<String, String> {
    if data.is_empty() {
        return Err("empty file".into());
    }

    upload_stream_to_telegram(
        tg_service,
        futures::stream::iter(vec![Ok(Bytes::from(data))]),
        filename,
        db_pool,
        folder_path,
        None,
    )
    .await
}

pub(crate) async fn upload_body_to_telegram(
    tg_service: &TelegramService,
    body: Body,
    filename: &str,
    db_pool: &database::DbPool,
    folder_path: &str,
    max_bytes: usize,
) -> Result<String, String> {
    let stream = body.into_data_stream().map(move |result| {
        result.map_err(|e| {
            if e.is_body_too_large() {
                format!("body exceeds {} bytes", max_bytes)
            } else {
                e.to_string()
            }
        })
    });

    upload_stream_to_telegram(tg_service, stream, filename, db_pool, folder_path, Some(max_bytes)).await
}

async fn upload_stream_to_telegram<S, E>(
    tg_service: &TelegramService,
    mut stream: S,
    filename: &str,
    db_pool: &database::DbPool,
    folder_path: &str,
    max_bytes: Option<usize>,
) -> Result<String, String>
where
    S: futures::Stream<Item = Result<Bytes, E>> + Unpin,
    E: ToString,
{
    let chunk_size = constants::TELEGRAM_CHUNK_SIZE;
    let mut buffer = BytesMut::with_capacity(chunk_size);
    let mut total_size: usize = 0;
    let mut chunk_ids: Vec<String> = Vec::new();
    let mut first_message_id: Option<i64> = None;
    let mut chunk_num: u32 = 0;

    while let Some(next_chunk) = stream.next().await {
        let bytes = next_chunk.map_err(|e| e.to_string())?;
        total_size += bytes.len();
        if let Some(limit) = max_bytes {
            if total_size > limit {
                return Err(format!("body exceeds {} bytes", limit));
            }
        }
        buffer.extend_from_slice(&bytes);

        while buffer.len() >= chunk_size {
            chunk_num += 1;
            let chunk_data = buffer.split_to(chunk_size).freeze().to_vec();
            let chunk_name = format!("{}.part{}", filename, chunk_num);

            let message = tg_service
                .send_document_raw(chunk_data, &chunk_name, first_message_id)
                .await?;

            if first_message_id.is_none() {
                first_message_id = Some(message.message_id);
            }

            let (composite_id, _) = extract_uploaded_media(message, &chunk_name)?;
            chunk_ids.push(composite_id);
        }
    }

    if buffer.is_empty() && chunk_ids.is_empty() {
        return Err("empty file".into());
    }

    if chunk_ids.is_empty() {
        let data = buffer.freeze().to_vec();
        let message = tg_service.send_document_raw(data, filename, None).await?;
        let (composite_id, telegram_size) = extract_uploaded_media(message, filename)?;
        return database::add_file_metadata_in_folder(
            db_pool,
            filename,
            &composite_id,
            if telegram_size > 0 { telegram_size } else { total_size as i64 },
            folder_path,
        )
        .map_err(|e| e.to_string());
    }

    if !buffer.is_empty() {
        chunk_num += 1;
        let chunk_data = buffer.freeze().to_vec();
        let chunk_name = format!("{}.part{}", filename, chunk_num);

        let message = tg_service
            .send_document_raw(chunk_data, &chunk_name, first_message_id)
            .await?;

        let (composite_id, _) = extract_uploaded_media(message, &chunk_name)?;
        chunk_ids.push(composite_id);
    }

    let mut manifest = String::from("tgstate-blob\n");
    manifest.push_str(filename);
    manifest.push('\n');
    for cid in &chunk_ids {
        manifest.push_str(cid);
        manifest.push('\n');
    }

    let manifest_name = format!("{}.manifest", filename);
    let message = tg_service
        .send_document_raw(manifest.into_bytes(), &manifest_name, first_message_id)
        .await?;

    let (manifest_composite, _) = extract_uploaded_media(message, &manifest_name)?;

    database::add_file_metadata_in_folder(
        db_pool,
        filename,
        &manifest_composite,
        total_size as i64,
        folder_path,
    )
    .map_err(|e| e.to_string())
}

pub fn router() -> Router<Arc<AppState>> {
    Router::new().route("/api/upload", post(upload_file))
}

#[cfg(test)]
mod tests {
    use super::{
        advance_upload_auth_state, current_storage_backend, UploadAuthProgress, UploadFieldError,
    };
    use crate::config::Settings;
    use crate::database;
    use crate::state::AppState;
    use axum::body::{to_bytes, Body};
    use axum::http::{header, Request, StatusCode};
    use axum::Router;
    use std::sync::Arc;
    use std::time::{SystemTime, UNIX_EPOCH};
    use tower::util::ServiceExt;

    fn test_state() -> Arc<AppState> {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let data_dir = std::env::temp_dir()
            .join(format!("tgstate-upload-test-{}", unique))
            .to_string_lossy()
            .to_string();

        let settings = Settings {
            bot_token: Some("123456:test-token".into()),
            channel_name: Some("@test_channel".into()),
            pass_word: Some("secret".into()),
            picgo_api_key: None,
            base_url: "http://127.0.0.1:8000".into(),
            _mode: "p".into(),
            _file_route: "/d/".into(),
            data_dir: data_dir.clone(),
        };

        let db_pool = database::init_db(&data_dir);
        let tera = tera::Tera::default();
        let http_client = reqwest::Client::new();
        let app_settings = crate::config::get_app_settings(&settings, &db_pool);
        Arc::new(AppState::new(
            settings,
            tera,
            http_client,
            db_pool,
            app_settings,
            true,
        ))
    }

    fn multipart_request_with_file_before_key() -> Request<Body> {
        let boundary = "X-BOUNDARY";
        let body = format!(
            "--{b}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"test.txt\"\r\nContent-Type: text/plain\r\n\r\nhello\r\n--{b}\r\nContent-Disposition: form-data; name=\"key\"\r\n\r\nsecret\r\n--{b}--\r\n",
            b = boundary
        );

        Request::builder()
            .method("POST")
            .uri("/api/upload")
            .header(
                header::CONTENT_TYPE,
                format!("multipart/form-data; boundary={}", boundary),
            )
            .body(Body::from(body))
            .unwrap()
    }

    #[test]
    fn upload_requires_key_before_file_for_api_requests() {
        let state = UploadAuthProgress::default();
        let result = advance_upload_auth_state(state, false, false, "file", None);
        assert!(matches!(result, Err(UploadFieldError::FileBeforeAuth)));
    }

    #[test]
    fn upload_accepts_key_before_file_for_api_requests() {
        let state = UploadAuthProgress::default();
        let state = advance_upload_auth_state(state, false, false, "key", Some("secret")).unwrap();
        let state = advance_upload_auth_state(state, false, false, "file", None).unwrap();
        assert!(state.auth_verified);
    }

    #[tokio::test]
    async fn upload_route_rejects_file_field_before_auth() {
        let state = test_state();
        let app = Router::new().merge(super::router()).with_state(state.clone());
        let response = app
            .oneshot(multipart_request_with_file_before_key())
            .await
            .unwrap();

        assert_eq!(response.status(), StatusCode::UNAUTHORIZED);

        let body = to_bytes(response.into_body(), usize::MAX).await.unwrap();
        let text = String::from_utf8(body.to_vec()).unwrap();
        assert!(text.contains("file_before_auth"), "unexpected body: {}", text);

        let files = database::get_all_files(&state.db_pool).unwrap();
        assert!(files.is_empty(), "unexpected files persisted: {:?}", files);
    }

    #[test]
    fn storage_backend_defaults_to_telegram() {
        let cfg = crate::config::AppSettingsMap::new();
        assert_eq!(
            current_storage_backend(&cfg),
            crate::constants::STORAGE_BACKEND_TELEGRAM
        );
    }

    #[test]
    fn storage_backend_uses_s3_when_configured() {
        let mut cfg = crate::config::AppSettingsMap::new();
        cfg.insert("STORAGE_BACKEND".into(), Some("s3".into()));
        assert_eq!(current_storage_backend(&cfg), "s3");
    }
}
