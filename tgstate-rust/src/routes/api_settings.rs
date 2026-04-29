use std::io::{Read, Write};
use std::sync::Arc;

use axum::body::Body;
use axum::extract::{Multipart, State};
use axum::http::header;
use axum::http::HeaderMap;
use axum::response::IntoResponse;
use axum::routing::{get, post};
use axum::{Json, Router};
use flate2::read::GzDecoder;
use flate2::write::GzEncoder;
use flate2::Compression;
use serde::Deserialize;

use crate::auth;
use crate::config;
use crate::database;
use crate::error::http_error;
use crate::state::{self, AppState};

#[derive(Deserialize)]
pub struct PasswordRequest {
    password: String,
}

#[derive(Deserialize)]
pub struct AppConfigRequest {
    #[serde(rename = "BOT_TOKEN")]
    bot_token: Option<String>,
    #[serde(rename = "CHANNEL_NAME")]
    channel_name: Option<String>,
    #[serde(rename = "PASS_WORD")]
    pass_word: Option<String>,
    #[serde(rename = "BASE_URL")]
    base_url: Option<String>,
    #[serde(rename = "PICGO_API_KEY")]
    picgo_api_key: Option<String>,
    #[serde(rename = "STORAGE_BACKEND")]
    storage_backend: Option<String>,
    #[serde(rename = "S3_ENDPOINT")]
    s3_endpoint: Option<String>,
    #[serde(rename = "S3_REGION")]
    s3_region: Option<String>,
    #[serde(rename = "S3_BUCKET")]
    s3_bucket: Option<String>,
    #[serde(rename = "S3_ACCESS_KEY")]
    s3_access_key: Option<String>,
    #[serde(rename = "S3_SECRET_KEY")]
    s3_secret_key: Option<String>,
    #[serde(rename = "S3_PUBLIC_BASE_URL")]
    s3_public_base_url: Option<String>,
    #[serde(rename = "WEBDAV_ENABLED")]
    webdav_enabled: Option<String>,
    #[serde(rename = "WEBDAV_READONLY")]
    webdav_readonly: Option<String>,
    #[serde(rename = "WEBDAV_USERNAME")]
    webdav_username: Option<String>,
}

#[derive(Deserialize)]
pub struct VerifyRequest {
    #[serde(rename = "BOT_TOKEN")]
    bot_token: Option<String>,
    #[serde(rename = "CHANNEL_NAME")]
    channel_name: Option<String>,
}


fn validate_config(
    cfg: &std::collections::HashMap<String, Option<String>>,
) -> Result<(), (axum::http::StatusCode, &'static str, &'static str)> {
    if let Some(Some(token)) = cfg.get("BOT_TOKEN") {
        let t = token.trim();
        if !t.is_empty() && (!t.contains(':') || t.len() < 20) {
            return Err((axum::http::StatusCode::BAD_REQUEST, "BOT_TOKEN 格式不正确", "invalid_bot_token"));
        }
    }
    if let Some(Some(channel)) = cfg.get("CHANNEL_NAME") {
        let c = channel.trim();
        if !c.is_empty() && !c.starts_with('@') && !c.starts_with("-100") {
            return Err((axum::http::StatusCode::BAD_REQUEST, "CHANNEL_NAME 格式不正确（@username 或 -100...）", "invalid_channel"));
        }
    }
    if let Some(Some(url)) = cfg.get("BASE_URL") {
        let u = url.trim();
        if !u.is_empty() && !u.starts_with("http://") && !u.starts_with("https://") {
            return Err((axum::http::StatusCode::BAD_REQUEST, "BASE_URL 必须以 http:// 或 https:// 开头", "invalid_base_url"));
        }
    }
    if let Some(Some(backend)) = cfg.get("STORAGE_BACKEND") {
        let b = backend.trim();
        if !b.is_empty() && b != "telegram" && b != "s3" {
            return Err((axum::http::StatusCode::BAD_REQUEST, "STORAGE_BACKEND 仅支持 telegram 或 s3", "invalid_storage_backend"));
        }
    }
    if let Some(Some(endpoint)) = cfg.get("S3_ENDPOINT") {
        let e = endpoint.trim();
        if !e.is_empty() && !e.starts_with("http://") && !e.starts_with("https://") {
            return Err((axum::http::StatusCode::BAD_REQUEST, "S3_ENDPOINT 必须以 http:// 或 https:// 开头", "invalid_s3_endpoint"));
        }
    }
    for key in ["WEBDAV_ENABLED", "WEBDAV_READONLY"] {
        if let Some(Some(v)) = cfg.get(key) {
            let v = v.trim();
            if !v.is_empty() && !matches!(v, "0" | "1" | "true" | "false") {
                return Err((axum::http::StatusCode::BAD_REQUEST, "WebDAV 开关仅支持 0/1/true/false", "invalid_webdav_flag"));
            }
        }
    }
    if let Some(Some(username)) = cfg.get("WEBDAV_USERNAME") {
        if username.trim().is_empty() {
            return Err((axum::http::StatusCode::BAD_REQUEST, "WEBDAV_USERNAME 不能为空", "invalid_webdav_username"));
        }
    }
    Ok(())
}

fn merge_config(
    existing: &std::collections::HashMap<String, Option<String>>,
    incoming: &AppConfigRequest,
) -> Result<std::collections::HashMap<String, Option<String>>, (axum::http::StatusCode, &'static str, &'static str)> {
    let mut result = existing.clone();

    macro_rules! set_opt {
        ($field:expr, $key:literal) => {
            if let Some(ref v) = $field {
                let v = v.trim().to_string();
                result.insert($key.into(), if v.is_empty() { None } else { Some(v) });
            }
        };
    }

    set_opt!(incoming.bot_token, "BOT_TOKEN");
    set_opt!(incoming.channel_name, "CHANNEL_NAME");

    if let Some(ref v) = incoming.pass_word {
        let v = v.trim().to_string();
        if v.is_empty() {
            result.insert("PASS_WORD".into(), None);
            result.insert("SESSION_TOKEN".into(), None);
        } else {
            match auth::hash_password(&v) {
                Ok(hashed) => {
                    result.insert("PASS_WORD".into(), Some(hashed));
                    result.insert("SESSION_TOKEN".into(), Some(auth::generate_session_token()));
                }
                Err(e) => {
                    tracing::error!("密码哈希失败: {}", e);
                    return Err((axum::http::StatusCode::INTERNAL_SERVER_ERROR, "密码哈希失败", "hash_error"));
                }
            }
        }
    }

    set_opt!(incoming.base_url, "BASE_URL");
    set_opt!(incoming.picgo_api_key, "PICGO_API_KEY");
    set_opt!(incoming.storage_backend, "STORAGE_BACKEND");
    set_opt!(incoming.s3_endpoint, "S3_ENDPOINT");
    set_opt!(incoming.s3_region, "S3_REGION");
    set_opt!(incoming.s3_bucket, "S3_BUCKET");
    set_opt!(incoming.s3_access_key, "S3_ACCESS_KEY");
    set_opt!(incoming.s3_secret_key, "S3_SECRET_KEY");
    set_opt!(incoming.s3_public_base_url, "S3_PUBLIC_BASE_URL");
    set_opt!(incoming.webdav_enabled, "WEBDAV_ENABLED");
    set_opt!(incoming.webdav_readonly, "WEBDAV_READONLY");
    set_opt!(incoming.webdav_username, "WEBDAV_USERNAME");

    Ok(result)
}

fn is_https(headers: &HeaderMap) -> bool {
    headers
        .get("x-forwarded-proto")
        .and_then(|v| v.to_str().ok())
        .is_some_and(|v| v == "https")
}

fn current_db_backup_path(state: &AppState) -> std::path::PathBuf {
    std::path::Path::new(&state.settings.data_dir).join("file_metadata.backup.db")
}

fn copy_live_database_to_backup(state: &AppState) -> Result<std::path::PathBuf, crate::error::AppError> {
    let source = state
        .db_pool
        .get()
        .map_err(|e| crate::error::AppError::from(crate::error::AppErrorKind::from(e)))?;
    let backup_path = current_db_backup_path(state);

    if let Some(parent) = backup_path.parent() {
        std::fs::create_dir_all(parent).map_err(|e| {
            crate::error::AppError::new(
                axum::http::StatusCode::INTERNAL_SERVER_ERROR,
                &format!("创建备份目录失败: {}", e),
                "db_backup_prepare_failed",
            )
        })?;
    }

    if backup_path.exists() {
        let _ = std::fs::remove_file(&backup_path);
    }

    let mut destination = rusqlite::Connection::open(&backup_path).map_err(|e| {
        crate::error::AppError::new(
            axum::http::StatusCode::INTERNAL_SERVER_ERROR,
            &format!("打开备份数据库失败: {}", e),
            "db_backup_open_failed",
        )
    })?;

    let backup = rusqlite::backup::Backup::new(&source, &mut destination).map_err(|e| {
        crate::error::AppError::new(
            axum::http::StatusCode::INTERNAL_SERVER_ERROR,
            &format!("创建数据库备份失败: {}", e),
            "db_backup_start_failed",
        )
    })?;

    backup.step(-1).map_err(|e| {
        crate::error::AppError::new(
            axum::http::StatusCode::INTERNAL_SERVER_ERROR,
            &format!("执行数据库备份失败: {}", e),
            "db_backup_step_failed",
        )
    })?;

    Ok(backup_path)
}

async fn get_app_config(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    let settings = config::get_app_settings(&state.settings, &state.db_pool);
    let bot = state.bot_state.lock().await;

    Json(serde_json::json!({
        "status": "ok",
        "cfg": {
            "BOT_TOKEN_SET": settings.get("BOT_TOKEN").and_then(|v| v.as_deref()).is_some_and(|v| !v.is_empty()),
            "CHANNEL_NAME": settings.get("CHANNEL_NAME").and_then(|v| v.as_deref()).unwrap_or(""),
            "PASS_WORD_SET": settings.get("PASS_WORD").and_then(|v| v.as_deref()).is_some_and(|v| !v.is_empty()),
            "BASE_URL": settings.get("BASE_URL").and_then(|v| v.as_deref()).unwrap_or(""),
            "PICGO_API_KEY_SET": settings.get("PICGO_API_KEY").and_then(|v| v.as_deref()).is_some_and(|v| !v.is_empty()),
            "STORAGE_BACKEND": settings.get("STORAGE_BACKEND").and_then(|v| v.as_deref()).unwrap_or("telegram"),
            "S3_ENDPOINT": settings.get("S3_ENDPOINT").and_then(|v| v.as_deref()).unwrap_or(""),
            "S3_REGION": settings.get("S3_REGION").and_then(|v| v.as_deref()).unwrap_or("auto"),
            "S3_BUCKET": settings.get("S3_BUCKET").and_then(|v| v.as_deref()).unwrap_or(""),
            "S3_ACCESS_KEY_SET": settings.get("S3_ACCESS_KEY").and_then(|v| v.as_deref()).is_some_and(|v| !v.is_empty()),
            "S3_SECRET_KEY_SET": settings.get("S3_SECRET_KEY").and_then(|v| v.as_deref()).is_some_and(|v| !v.is_empty()),
            "S3_PUBLIC_BASE_URL": settings.get("S3_PUBLIC_BASE_URL").and_then(|v| v.as_deref()).unwrap_or(""),
            "WEBDAV_ENABLED": settings.get("WEBDAV_ENABLED").and_then(|v| v.as_deref()).unwrap_or("0"),
            "WEBDAV_READONLY": settings.get("WEBDAV_READONLY").and_then(|v| v.as_deref()).unwrap_or("0"),
            "WEBDAV_USERNAME": settings.get("WEBDAV_USERNAME").and_then(|v| v.as_deref()).unwrap_or("admin")
        },
        "bot": {
            "ready": bot.bot_ready,
            "running": bot.bot_running,
            "error": bot.bot_error
        }
    }))
}

async fn save_config_only(
    State(state): State<Arc<AppState>>,
    Json(payload): Json<AppConfigRequest>,
) -> Result<impl IntoResponse, impl IntoResponse> {
    let existing = database::get_app_settings_from_db(&state.db_pool).unwrap_or_default();
    let merged = merge_config(&existing, &payload).map_err(|(status, msg, code)| http_error(status, msg, code))?;
    if let Err((status, msg, code)) = validate_config(&merged) {
        return Err(http_error(status, msg, code));
    }
    database::save_app_settings_to_db(&state.db_pool, &merged).map_err(|e| {
        tracing::error!("保存配置失败: {}", e);
        http_error(axum::http::StatusCode::INTERNAL_SERVER_ERROR, "保存配置失败", "save_error")
    })?;
    Ok(Json(serde_json::json!({"status": "ok", "message": "已保存（未应用）"})))
}

async fn save_and_apply(
    State(state): State<Arc<AppState>>,
    headers: HeaderMap,
    Json(payload): Json<AppConfigRequest>,
) -> Result<impl IntoResponse, impl IntoResponse> {
    let existing = database::get_app_settings_from_db(&state.db_pool).unwrap_or_default();
    let merged = merge_config(&existing, &payload).map_err(|(status, msg, code)| http_error(status, msg, code))?;
    if let Err((status, msg, code)) = validate_config(&merged) {
        return Err(http_error(status, msg, code));
    }
    database::save_app_settings_to_db(&state.db_pool, &merged).map_err(|e| {
        tracing::error!("保存配置失败: {}", e);
        http_error(axum::http::StatusCode::INTERNAL_SERVER_ERROR, "保存配置失败", "save_error")
    })?;

    let _ = state::apply_runtime_settings(state.clone(), true).await;
    let bot = state.bot_state.lock().await;

    let session_token = merged.get("SESSION_TOKEN").and_then(|v| v.as_deref()).unwrap_or("");
    let pwd = merged.get("PASS_WORD").and_then(|v| v.as_deref()).unwrap_or("");
    let cookie = if !pwd.is_empty() && !session_token.is_empty() {
        auth::build_cookie(session_token, is_https(&headers))
    } else {
        auth::build_clear_cookie()
    };

    Ok((
        [(axum::http::header::SET_COOKIE, cookie)],
        Json(serde_json::json!({
            "status": "ok",
            "message": "已保存并应用",
            "bot": {"ready": bot.bot_ready, "running": bot.bot_running}
        })),
    ))
}

async fn reset_config(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    database::reset_app_settings_in_db(&state.db_pool).ok();
    let _ = state::apply_runtime_settings(state.clone(), true).await;
    (
        [(axum::http::header::SET_COOKIE, crate::auth::build_clear_cookie())],
        Json(serde_json::json!({"status": "ok", "message": "配置已重置"})),
    )
}

async fn export_database_backup(
    State(state): State<Arc<AppState>>,
) -> Result<impl IntoResponse, crate::error::AppError> {
    let backup_path = copy_live_database_to_backup(&state)?;
    let db_bytes = std::fs::read(&backup_path).map_err(|e| {
        crate::error::AppError::new(
            axum::http::StatusCode::INTERNAL_SERVER_ERROR,
            &format!("读取备份文件失败: {}", e),
            "db_backup_read_failed",
        )
    })?;

    let mut encoder = GzEncoder::new(Vec::new(), Compression::default());
    encoder.write_all(&db_bytes).map_err(|e| {
        crate::error::AppError::new(
            axum::http::StatusCode::INTERNAL_SERVER_ERROR,
            &format!("压缩数据库失败: {}", e),
            "db_backup_compress_failed",
        )
    })?;
    let gzipped = encoder.finish().map_err(|e| {
        crate::error::AppError::new(
            axum::http::StatusCode::INTERNAL_SERVER_ERROR,
            &format!("完成压缩失败: {}", e),
            "db_backup_finish_failed",
        )
    })?;

    let filename = format!(
        "tgstate-db-backup-{}.db.gz",
        chrono::Local::now().format("%Y%m%d-%H%M%S")
    );

    Ok((
        [
            (header::CONTENT_TYPE, "application/gzip".to_string()),
            (
                header::CONTENT_DISPOSITION,
                format!("attachment; filename=\"{}\"", filename),
            ),
        ],
        Body::from(gzipped),
    ))
}

async fn import_database_backup(
    State(state): State<Arc<AppState>>,
    mut multipart: Multipart,
) -> Result<impl IntoResponse, crate::error::AppError> {
    let mut uploaded: Option<Vec<u8>> = None;
    let mut confirm_overwrite = false;

    while let Ok(Some(field)) = multipart.next_field().await {
        match field.name() {
            Some("confirm_overwrite") => {
                let value = field.text().await.map_err(|e| {
                    crate::error::AppError::new(
                        axum::http::StatusCode::BAD_REQUEST,
                        &format!("读取覆盖确认失败: {}", e),
                        "db_restore_confirm_read_failed",
                    )
                })?;
                confirm_overwrite = matches!(value.trim(), "1" | "true" | "yes" | "on");
            }
            Some("file") => {
                uploaded = Some(field.bytes().await.map_err(|e| {
                    crate::error::AppError::new(
                        axum::http::StatusCode::BAD_REQUEST,
                        &format!("读取上传文件失败: {}", e),
                        "db_restore_upload_read_failed",
                    )
                })?.to_vec());
            }
            _ => {}
        }
    }

    if !confirm_overwrite {
        return Err(crate::error::AppError::new(
            axum::http::StatusCode::BAD_REQUEST,
            "需要确认覆盖后才能导入数据库",
            "confirm_overwrite_required",
        ));
    }

    let file_bytes = uploaded.ok_or_else(|| {
        crate::error::AppError::new(
            axum::http::StatusCode::BAD_REQUEST,
            "未找到要导入的备份文件",
            "db_restore_file_missing",
        )
    })?;

    let mut decoder = GzDecoder::new(file_bytes.as_slice());
    let mut restored_db = Vec::new();
    decoder.read_to_end(&mut restored_db).map_err(|e| {
        crate::error::AppError::new(
            axum::http::StatusCode::BAD_REQUEST,
            &format!("解压备份文件失败: {}", e),
            "db_restore_decompress_failed",
        )
    })?;

    let temp_restore_path = std::path::Path::new(&state.settings.data_dir).join("file_metadata.restore.tmp.db");
    std::fs::write(&temp_restore_path, &restored_db).map_err(|e| {
        crate::error::AppError::new(
            axum::http::StatusCode::INTERNAL_SERVER_ERROR,
            &format!("写入临时恢复数据库失败: {}", e),
            "db_restore_temp_write_failed",
        )
    })?;

    {
        let source = rusqlite::Connection::open(&temp_restore_path).map_err(|e| {
            let _ = std::fs::remove_file(&temp_restore_path);
            crate::error::AppError::new(
                axum::http::StatusCode::BAD_REQUEST,
                &format!("备份文件不是有效的 SQLite 数据库: {}", e),
                "db_restore_invalid_sqlite",
            )
        })?;

        let mut live_conn = state
            .db_pool
            .get()
            .map_err(|e| crate::error::AppError::from(crate::error::AppErrorKind::from(e)))?;
        live_conn
            .execute_batch("PRAGMA wal_checkpoint(FULL);")
            .map_err(|e| crate::error::AppError::new(
                axum::http::StatusCode::INTERNAL_SERVER_ERROR,
                &format!("恢复前检查点失败: {}", e),
                "db_restore_checkpoint_failed",
            ))?;

        let backup = rusqlite::backup::Backup::new(&source, &mut live_conn).map_err(|e| {
            crate::error::AppError::new(
                axum::http::StatusCode::INTERNAL_SERVER_ERROR,
                &format!("创建恢复任务失败: {}", e),
                "db_restore_start_failed",
            )
        })?;
        backup.step(-1).map_err(|e| {
            crate::error::AppError::new(
                axum::http::StatusCode::INTERNAL_SERVER_ERROR,
                &format!("覆盖数据库失败: {}", e),
                "db_restore_replace_failed",
            )
        })?;
    }

    let _ = std::fs::remove_file(&temp_restore_path);
    let _ = state::apply_runtime_settings(state.clone(), true).await;

    Ok(Json(serde_json::json!({
        "status": "ok",
        "message": "数据库已覆盖导入，建议刷新页面确认配置与文件状态"
    })))
}

async fn set_password(
    State(state): State<Arc<AppState>>,
    Json(payload): Json<PasswordRequest>,
) -> Result<Json<serde_json::Value>, crate::error::AppError> {
    let password = payload.password.trim().to_string();
    let hashed = auth::hash_password(&password).map_err(|e| {
        tracing::error!("密码哈希失败: {}", e);
        crate::error::AppError::new(axum::http::StatusCode::INTERNAL_SERVER_ERROR, "密码哈希失败", "hash_error")
    })?;

    let mut current = database::get_app_settings_from_db(&state.db_pool).unwrap_or_default();
    current.insert("PASS_WORD".into(), Some(hashed));
    current.insert("SESSION_TOKEN".into(), Some(auth::generate_session_token()));

    database::save_app_settings_to_db(&state.db_pool, &current).map_err(|_| {
        crate::error::AppError::new(axum::http::StatusCode::INTERNAL_SERVER_ERROR, "无法写入密码。", "write_password_failed")
    })?;

    let _ = state::apply_runtime_settings(state.clone(), false).await;
    Ok(Json(serde_json::json!({"status": "ok", "message": "密码已成功设置。"})))
}

async fn verify_bot(
    State(state): State<Arc<AppState>>,
    Json(payload): Json<VerifyRequest>,
) -> impl IntoResponse {
    let app_settings = config::get_app_settings(&state.settings, &state.db_pool);
    let token = payload.bot_token.or_else(|| app_settings.get("BOT_TOKEN").and_then(|v| v.clone())).unwrap_or_default();

    if token.is_empty() {
        return Json(serde_json::json!({"status": "ok", "ok": false, "available": false, "message": "未提供 BOT_TOKEN"}));
    }

    let url = format!("https://api.telegram.org/bot{}/getMe", token);
    let client = reqwest::Client::builder().timeout(std::time::Duration::from_secs(10)).build().unwrap();

    match client.get(&url).send().await {
        Ok(resp) => match resp.json::<serde_json::Value>().await {
            Ok(data) => {
                if data["ok"].as_bool() == Some(true) {
                    let username = data["result"]["username"].as_str().unwrap_or("unknown");
                    Json(serde_json::json!({"status": "ok", "ok": true, "available": true, "result": {"username": username}}))
                } else {
                    Json(serde_json::json!({"status": "ok", "ok": false, "available": false, "message": data["description"].as_str().unwrap_or("Unknown error")}))
                }
            }
            Err(e) => {
                tracing::warn!("verify_bot parse error: {}", e);
                Json(serde_json::json!({"status": "ok", "ok": false, "available": false, "message": "解析响应失败"}))
            }
        },
        Err(e) => {
            tracing::warn!("verify_bot connect error: {}", e);
            Json(serde_json::json!({"status": "ok", "ok": false, "available": false, "message": "连接失败"}))
        }
    }
}

async fn verify_channel(
    State(state): State<Arc<AppState>>,
    Json(payload): Json<VerifyRequest>,
) -> impl IntoResponse {
    let app_settings = config::get_app_settings(&state.settings, &state.db_pool);
    let token = payload.bot_token.or_else(|| app_settings.get("BOT_TOKEN").and_then(|v| v.clone())).unwrap_or_default();
    let channel = payload.channel_name.or_else(|| app_settings.get("CHANNEL_NAME").and_then(|v| v.clone())).unwrap_or_default();

    if token.is_empty() || channel.is_empty() {
        return Json(serde_json::json!({"status": "ok", "available": false, "message": "缺少 BOT_TOKEN 或 CHANNEL_NAME"}));
    }

    let url = format!("https://api.telegram.org/bot{}/sendMessage", token);
    let client = reqwest::Client::builder().timeout(std::time::Duration::from_secs(10)).build().unwrap();

    match client
        .post(&url)
        .json(&serde_json::json!({"chat_id": channel, "text": "tgState channel check"}))
        .send()
        .await
    {
        Ok(resp) => match resp.json::<serde_json::Value>().await {
            Ok(data) => {
                if data["ok"].as_bool() == Some(true) {
                    if let Some(msg_id) = data["result"]["message_id"].as_i64() {
                        let del_url = format!("https://api.telegram.org/bot{}/deleteMessage", token);
                        let _ = client
                            .post(&del_url)
                            .json(&serde_json::json!({"chat_id": channel, "message_id": msg_id}))
                            .send()
                            .await;
                    }
                    Json(serde_json::json!({"status": "ok", "available": true}))
                } else {
                    Json(serde_json::json!({"status": "ok", "available": false, "message": data["description"].as_str().unwrap_or("Unknown error")}))
                }
            }
            Err(e) => {
                tracing::warn!("verify_channel parse error: {}", e);
                Json(serde_json::json!({"status": "ok", "available": false, "message": "解析响应失败"}))
            }
        },
        Err(e) => {
            tracing::warn!("verify_channel connect error: {}", e);
            Json(serde_json::json!({"status": "ok", "available": false, "message": "连接失败"}))
        }
    }
}

pub fn router() -> Router<Arc<AppState>> {
    Router::new()
        .route("/api/app-config", get(get_app_config))
        .route("/api/app-config/save", post(save_config_only))
        .route("/api/app-config/apply", post(save_and_apply))
        .route("/api/app-config/db/export", get(export_database_backup))
        .route("/api/app-config/db/import", post(import_database_backup))
        .route("/api/reset-config", post(reset_config))
        .route("/api/set-password", post(set_password))
        .route("/api/verify/bot", post(verify_bot))
        .route("/api/verify/channel", post(verify_channel))
}
