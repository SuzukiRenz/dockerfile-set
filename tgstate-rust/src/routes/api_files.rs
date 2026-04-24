use std::sync::Arc;

use axum::body::Body;
use axum::extract::{Path, Query, State};
use axum::http::{HeaderMap, StatusCode};
use axum::response::{IntoResponse, Response};
use axum::routing::{delete, get, post};
use axum::{Json, Router};
use futures::StreamExt;
use serde::Deserialize;

use crate::config;
use crate::database;
use crate::error::http_error;
use crate::state::AppState;
use crate::storage;
use crate::telegram::service::TelegramService;

#[derive(Deserialize)]
pub struct DownloadQuery {
    download: Option<String>,
}

#[derive(Deserialize)]
pub struct BatchDeleteRequest {
    file_ids: Vec<String>,
}

fn get_telegram_service(state: &AppState) -> Result<TelegramService, impl IntoResponse> {
    let app_settings = config::get_app_settings(&state.settings, &state.db_pool);
    let token = app_settings
        .get("BOT_TOKEN")
        .and_then(|v| v.as_deref())
        .unwrap_or("")
        .to_string();
    let channel = app_settings
        .get("CHANNEL_NAME")
        .and_then(|v| v.as_deref())
        .unwrap_or("")
        .to_string();

    if token.is_empty() || channel.is_empty() {
        return Err(http_error(
            StatusCode::SERVICE_UNAVAILABLE,
            "Bot 未配置",
            "bot_not_configured",
        ));
    }

    Ok(TelegramService::new(
        token,
        channel,
        state.http_client.clone(),
    ))
}

fn guess_content_type(filename: &str) -> String {
    let mime = mime_guess::from_path(filename).first_or_octet_stream();
    let mime_str = mime.to_string();

    if mime_str.starts_with("text/") && !mime_str.contains("charset") {
        format!("{}; charset=utf-8", mime_str)
    } else {
        mime_str
    }
}

fn content_disposition(filename: &str, force_download: bool) -> String {
    let preview_extensions = [
        "jpg", "jpeg", "png", "gif", "webp", "bmp", "ico", "tiff", "mp4", "webm", "ogg", "mp3",
        "wav", "flac", "pdf", "txt", "json", "csv", "md", "log",
    ];

    let ext = filename.rsplit('.').next().unwrap_or("").to_lowercase();
    let is_inline = !force_download && preview_extensions.contains(&ext.as_str());
    let encoded_name =
        percent_encoding::utf8_percent_encode(filename, percent_encoding::NON_ALPHANUMERIC)
            .to_string();

    if is_inline {
        format!("inline; filename*=UTF-8''{}", encoded_name)
    } else {
        format!("attachment; filename*=UTF-8''{}", encoded_name)
    }
}

fn chunk_download_failed_response(chunk_id: &str) -> Response {
    tracing::error!("chunk download failed: {}", chunk_id);
    http_error(
        StatusCode::BAD_GATEWAY,
        "文件下载失败",
        "chunk_download_failed",
    )
    .into_response()
}

async fn serve_telegram_file(
    state: &AppState,
    tg_service: &TelegramService,
    file_id: &str,
    filename: &str,
    headers: &HeaderMap,
    force_download: bool,
    is_head: bool,
) -> Response {
    let real_file_id = if let Some(pos) = file_id.find(':') {
        &file_id[pos + 1..]
    } else {
        file_id
    };

    let download_url = match tg_service.get_download_url(real_file_id).await {
        Ok(Some(url)) => url,
        Ok(None) => {
            return http_error(StatusCode::NOT_FOUND, "文件未找到或链接已过期", "not_found")
                .into_response()
        }
        Err(e) => {
            tracing::error!("获取下载链接失败: {}", e);
            return http_error(
                StatusCode::SERVICE_UNAVAILABLE,
                "无法连接到 Telegram",
                "tg_error",
            )
            .into_response();
        }
    };

    let client = &state.http_client;

    let peek_resp = match client
        .get(&download_url)
        .header("Range", "bytes=0-127")
        .send()
        .await
    {
        Ok(r) => r,
        Err(e) => {
            tracing::error!("下载失败: {}", e);
            return http_error(StatusCode::BAD_GATEWAY, "无法下载文件", "download_error")
                .into_response();
        }
    };

    let peek_bytes = match peek_resp.bytes().await {
        Ok(b) => b,
        Err(e) => {
            tracing::error!("读取文件失败: {}", e);
            return http_error(StatusCode::BAD_GATEWAY, "读取文件失败", "read_error")
                .into_response();
        }
    };

    if peek_bytes.starts_with(b"tgstate-blob\n") {
        let full_resp = match client.get(&download_url).send().await {
            Ok(r) => r,
            Err(e) => {
                tracing::error!("下载清单失败: {}", e);
                return http_error(StatusCode::BAD_GATEWAY, "下载文件失败", "download_error")
                    .into_response();
            }
        };
        let manifest_bytes = match full_resp.bytes().await {
            Ok(b) => b,
            Err(e) => {
                tracing::error!("读取清单失败: {}", e);
                return http_error(StatusCode::BAD_GATEWAY, "读取文件失败", "read_error")
                    .into_response();
            }
        };

        let manifest_str = String::from_utf8_lossy(&manifest_bytes);
        let lines: Vec<&str> = manifest_str.lines().collect();
        if lines.len() < 3 {
            return http_error(
                StatusCode::INTERNAL_SERVER_ERROR,
                "清单文件格式错误",
                "manifest_error",
            )
            .into_response();
        }

        let original_filename = lines[1];
        let chunk_ids: Vec<String> = lines[2..].iter().map(|s| s.to_string()).collect();

        let ct = guess_content_type(original_filename);
        let cd = content_disposition(original_filename, force_download);

        if is_head {
            return Response::builder()
                .header("Content-Type", ct)
                .header("Content-Disposition", cd)
                .header("Accept-Ranges", "bytes")
                .header("X-Content-Type-Options", "nosniff")
                .body(Body::empty())
                .unwrap();
        }

        let mut chunk_urls = Vec::with_capacity(chunk_ids.len());
        for chunk_composite in &chunk_ids {
            let real_id = if let Some(pos) = chunk_composite.find(':') {
                chunk_composite[pos + 1..].to_string()
            } else {
                chunk_composite.clone()
            };

            let url = match tg_service.get_download_url(&real_id).await {
                Ok(Some(u)) => u,
                _ => return chunk_download_failed_response(chunk_composite),
            };
            chunk_urls.push((chunk_composite.clone(), url));
        }

        let tg = tg_service.clone();
        let http = client.clone();
        let stream = async_stream::stream! {
            for (chunk_composite, url) in chunk_urls {
                let resp = match http.get(&url).send().await {
                    Ok(r) if r.status().is_success() => r,
                    _ => {
                        tokio::time::sleep(std::time::Duration::from_secs(1)).await;
                        let real_id = if let Some(pos) = chunk_composite.find(':') {
                            chunk_composite[pos + 1..].to_string()
                        } else {
                            chunk_composite.clone()
                        };
                        let retry_url = match tg.get_download_url(&real_id).await {
                            Ok(Some(u)) => u,
                            _ => {
                                yield Err::<bytes::Bytes, std::io::Error>(std::io::Error::other(format!(
                                    "Failed to refresh chunk URL: {}",
                                    chunk_composite
                                )));
                                return;
                            }
                        };
                        match http.get(&retry_url).send().await {
                            Ok(r) if r.status().is_success() => r,
                            _ => {
                                yield Err::<bytes::Bytes, std::io::Error>(std::io::Error::other(format!(
                                    "Chunk download retry failed: {}",
                                    chunk_composite
                                )));
                                return;
                            }
                        }
                    }
                };

                let mut stream = resp.bytes_stream();
                while let Some(chunk) = stream.next().await {
                    match chunk {
                        Ok(bytes) => yield Ok::<_, std::io::Error>(bytes),
                        Err(e) => {
                            yield Err::<bytes::Bytes, std::io::Error>(std::io::Error::other(format!(
                                "Chunk stream error for {}: {}",
                                chunk_composite, e
                            )));
                            return;
                        }
                    }
                }
            }
        };

        return Response::builder()
            .header("Content-Type", ct)
            .header("Content-Disposition", cd)
            .header("Accept-Ranges", "bytes")
            .header("X-Content-Type-Options", "nosniff")
            .body(Body::from_stream(stream))
            .unwrap();
    }

    let ct = guess_content_type(filename);
    let cd = content_disposition(filename, force_download);

    if let Some(range_header) = headers.get("range").and_then(|v| v.to_str().ok()) {
        let range_resp = match client
            .get(&download_url)
            .header("Range", range_header)
            .send()
            .await
        {
            Ok(r) => r,
            Err(e) => {
                tracing::error!("Range 请求失败: {}", e);
                return http_error(StatusCode::BAD_GATEWAY, "无法下载文件", "download_error")
                    .into_response();
            }
        };

        let status = range_resp.status();
        let mut builder = Response::builder()
            .status(status)
            .header("Content-Type", &ct)
            .header("Content-Disposition", &cd)
            .header("Accept-Ranges", "bytes")
            .header("X-Content-Type-Options", "nosniff");

        if let Some(cr) = range_resp.headers().get("content-range") {
            builder = builder.header("Content-Range", cr);
        }
        if let Some(cl) = range_resp.headers().get("content-length") {
            builder = builder.header("Content-Length", cl);
        }

        let stream = range_resp.bytes_stream();
        return builder
            .body(Body::from_stream(stream.map(|r| {
                r.map_err(std::io::Error::other)
            })))
            .unwrap();
    }

    let full_resp = match client.get(&download_url).send().await {
        Ok(r) => r,
        Err(e) => {
            tracing::error!("下载失败: {}", e);
            return http_error(StatusCode::BAD_GATEWAY, "无法下载文件", "download_error")
                .into_response();
        }
    };

    let mut builder = Response::builder()
        .header("Content-Type", ct)
        .header("Content-Disposition", cd)
        .header("Accept-Ranges", "bytes")
        .header("X-Content-Type-Options", "nosniff");

    if let Some(cl) = full_resp.headers().get("content-length") {
        builder = builder.header("Content-Length", cl);
    }

    if is_head {
        return builder.body(Body::empty()).unwrap();
    }

    let stream = full_resp.bytes_stream();
    builder
        .body(Body::from_stream(stream.map(|r| {
            r.map_err(std::io::Error::other)
        })))
        .unwrap()
}

async fn serve_file(
    state: &AppState,
    meta: &database::FileMetadata,
    headers: &HeaderMap,
    force_download: bool,
    is_head: bool,
) -> Response {
    if meta.storage_backend == crate::constants::STORAGE_BACKEND_S3 {
        match storage::s3::get_download_url(state, meta).await {
            Ok(url) => {
                let ct = guess_content_type(&meta.filename);
                let cd = content_disposition(&meta.filename, force_download);
                if is_head {
                    return Response::builder()
                        .header("Content-Type", ct)
                        .header("Content-Disposition", cd)
                        .header("Accept-Ranges", "bytes")
                        .header("X-Content-Type-Options", "nosniff")
                        .body(Body::empty())
                        .unwrap();
                }

                let client = &state.http_client;
                if let Some(range_header) = headers.get("range").and_then(|v| v.to_str().ok()) {
                    match client.get(&url).header("Range", range_header).send().await {
                        Ok(resp) => {
                            let mut builder = Response::builder()
                                .status(resp.status())
                                .header("Content-Type", ct)
                                .header("Content-Disposition", cd)
                                .header("Accept-Ranges", "bytes")
                                .header("X-Content-Type-Options", "nosniff");
                            if let Some(cr) = resp.headers().get("content-range") {
                                builder = builder.header("Content-Range", cr);
                            }
                            if let Some(cl) = resp.headers().get("content-length") {
                                builder = builder.header("Content-Length", cl);
                            }
                            return builder.body(Body::from_stream(resp.bytes_stream().map(|r| r.map_err(std::io::Error::other)))).unwrap();
                        }
                        Err(e) => {
                            tracing::error!("S3 range download failed: {}", e);
                            return http_error(StatusCode::BAD_GATEWAY, "S3 下载失败", "s3_download_failed").into_response();
                        }
                    }
                }

                match client.get(&url).send().await {
                    Ok(resp) => {
                        let mut builder = Response::builder()
                            .header("Content-Type", ct)
                            .header("Content-Disposition", cd)
                            .header("Accept-Ranges", "bytes")
                            .header("X-Content-Type-Options", "nosniff");
                        if let Some(cl) = resp.headers().get("content-length") {
                            builder = builder.header("Content-Length", cl);
                        }
                        builder.body(Body::from_stream(resp.bytes_stream().map(|r| r.map_err(std::io::Error::other)))).unwrap()
                    }
                    Err(e) => {
                        tracing::error!("S3 download failed: {}", e);
                        http_error(StatusCode::BAD_GATEWAY, "S3 下载失败", "s3_download_failed").into_response()
                    }
                }
            }
            Err(e) => crate::error::AppError::from(e).into_response(),
        }
    } else {
        let tg_service = match get_telegram_service(state) {
            Ok(s) => s,
            Err(e) => return e.into_response(),
        };
        serve_telegram_file(
            state,
            &tg_service,
            &meta.file_id,
            &meta.filename,
            headers,
            force_download,
            is_head,
        )
        .await
    }
}

async fn download_file_short(
    State(state): State<Arc<AppState>>,
    Path(identifier): Path<String>,
    Query(query): Query<DownloadQuery>,
    headers: HeaderMap,
) -> Response {
    if identifier.is_empty()
        || identifier.len() > 128
        || identifier.chars().any(|c| c.is_control() || c == '\0')
    {
        return http_error(StatusCode::BAD_REQUEST, "无效的文件标识", "invalid_id").into_response();
    }

    let meta = database::get_file_by_id(&state.db_pool, &identifier);
    match meta {
        Ok(Some(f)) => {
            let force_download = query
                .download
                .as_deref()
                .map_or(false, |v| v == "1" || v == "true");
            serve_file(&state, &f, &headers, force_download, false).await
        }
        _ => http_error(StatusCode::NOT_FOUND, "文件未找到", "not_found").into_response(),
    }
}

async fn download_file_short_head(
    State(state): State<Arc<AppState>>,
    Path(identifier): Path<String>,
    Query(query): Query<DownloadQuery>,
    headers: HeaderMap,
) -> Response {
    let meta = database::get_file_by_id(&state.db_pool, &identifier);
    match meta {
        Ok(Some(f)) => {
            let force_download = query
                .download
                .as_deref()
                .map_or(false, |v| v == "1" || v == "true");
            serve_file(&state, &f, &headers, force_download, true).await
        }
        _ => http_error(StatusCode::NOT_FOUND, "文件未找到", "not_found").into_response(),
    }
}

async fn download_file_legacy(
    State(state): State<Arc<AppState>>,
    Path((file_id, filename)): Path<(String, String)>,
    Query(query): Query<DownloadQuery>,
    headers: HeaderMap,
) -> Response {
    let meta = match database::get_file_by_id(&state.db_pool, &file_id) {
        Ok(Some(meta)) => meta,
        _ => database::FileMetadata {
            filename,
            file_id,
            filesize: 0,
            upload_date: String::new(),
            short_id: None,
            storage_backend: crate::constants::STORAGE_BACKEND_TELEGRAM.to_string(),
            storage_path: None,
        },
    };
    let force_download = query
        .download
        .as_deref()
        .map_or(false, |v| v == "1" || v == "true");
    serve_file(&state, &meta, &headers, force_download, false).await
}

async fn download_file_legacy_head(
    State(state): State<Arc<AppState>>,
    Path((file_id, filename)): Path<(String, String)>,
    Query(query): Query<DownloadQuery>,
    headers: HeaderMap,
) -> Response {
    let meta = match database::get_file_by_id(&state.db_pool, &file_id) {
        Ok(Some(meta)) => meta,
        _ => database::FileMetadata {
            filename,
            file_id,
            filesize: 0,
            upload_date: String::new(),
            short_id: None,
            storage_backend: crate::constants::STORAGE_BACKEND_TELEGRAM.to_string(),
            storage_path: None,
        },
    };
    let force_download = query
        .download
        .as_deref()
        .map_or(false, |v| v == "1" || v == "true");
    serve_file(&state, &meta, &headers, force_download, true).await
}

async fn get_files_list(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    let files = database::get_all_files(&state.db_pool).unwrap_or_default();
    Json(files)
}

async fn delete_file(
    State(state): State<Arc<AppState>>,
    Path(file_id): Path<String>,
) -> impl IntoResponse {
    let meta = match database::get_file_by_id(&state.db_pool, &file_id) {
        Ok(Some(m)) => m,
        _ => {
            return http_error(StatusCode::NOT_FOUND, "文件不存在", "not_found").into_response();
        }
    };

    if meta.storage_backend == crate::constants::STORAGE_BACKEND_S3 {
        let delete_res = storage::s3::delete_object(&state, &meta).await;
        match delete_res {
            Ok(_) => {
                let db_deleted = database::delete_file_metadata(&state.db_pool, &meta.file_id).unwrap_or(false);
                return Json(serde_json::json!({
                    "status": "ok",
                    "message": format!("文件 {} 已删除。", file_id),
                    "details": {"db": if db_deleted {"deleted"} else {"not_found_in_db"}, "backend": "s3"}
                })).into_response();
            }
            Err(e) => return crate::error::AppError::from(e).into_response(),
        }
    }

    let tg_service = match get_telegram_service(&state) {
        Ok(s) => s,
        Err(e) => return e.into_response(),
    };

    tracing::info!("正在删除文件: {}", file_id);
    let result = tg_service.delete_file_with_chunks(&meta.file_id).await;

    if result.main_message_deleted {
        let db_deleted = database::delete_file_metadata(&state.db_pool, &meta.file_id).unwrap_or(false);
        let db_status = if db_deleted { "deleted" } else { "not_found_in_db" };

        if result.failed_chunks.is_empty() {
            return Json(serde_json::json!({
                "status": "ok",
                "message": format!("文件 {} 已删除。", file_id),
                "details": {"db": db_status, "tg": result}
            }))
            .into_response();
        } else {
            return (
                StatusCode::INTERNAL_SERVER_ERROR,
                Json(serde_json::json!({
                    "status": "error",
                    "code": "partial_failure",
                    "message": "部分分块删除失败",
                    "details": result,
                })),
            )
                .into_response();
        }
    }

    let force_deleted = database::delete_file_metadata(&state.db_pool, &meta.file_id).unwrap_or(false);
    if force_deleted {
        return Json(serde_json::json!({
            "status": "ok",
            "message": format!("文件 {} 已从数据库删除（Telegram 删除失败）。", file_id),
            "details": result,
        }))
        .into_response();
    }

    (
        StatusCode::BAD_REQUEST,
        Json(serde_json::json!({
            "status": "error",
            "code": "delete_failed",
            "message": "删除失败",
            "details": result,
        })),
    )
        .into_response()
}

async fn batch_delete_files(
    State(state): State<Arc<AppState>>,
    Json(payload): Json<BatchDeleteRequest>,
) -> impl IntoResponse {
    if payload.file_ids.len() > crate::constants::BATCH_DELETE_MAX {
        return http_error(
            StatusCode::BAD_REQUEST,
            "批量删除数量超过上限",
            "too_many_items",
        )
        .into_response();
    }

    let mut deleted = Vec::new();
    let mut failed = Vec::new();

    for fid in &payload.file_ids {
        let meta = match database::get_file_by_id(&state.db_pool, fid) {
            Ok(Some(m)) => m,
            _ => {
                failed.push(fid.clone());
                continue;
            }
        };

        if meta.storage_backend == crate::constants::STORAGE_BACKEND_S3 {
            if storage::s3::delete_object(&state, &meta).await.is_ok() {
                database::delete_file_metadata(&state.db_pool, &meta.file_id).ok();
                deleted.push(fid.clone());
            } else {
                failed.push(fid.clone());
            }
            continue;
        }

        let tg_service = match get_telegram_service(&state) {
            Ok(s) => s,
            Err(e) => return e.into_response(),
        };

        let result = tg_service.delete_file_with_chunks(&meta.file_id).await;
        if result.main_message_deleted {
            database::delete_file_metadata(&state.db_pool, &meta.file_id).ok();
            deleted.push(fid.clone());
        } else if database::delete_file_metadata(&state.db_pool, &meta.file_id).unwrap_or(false) {
            deleted.push(fid.clone());
        } else {
            failed.push(fid.clone());
        }
    }

    Json(serde_json::json!({
        "status": "completed",
        "deleted": deleted,
        "failed": failed,
    }))
    .into_response()
}

pub fn router() -> Router<Arc<AppState>> {
    Router::new()
        .route("/api/files", get(get_files_list))
        .route("/api/files/:file_id", delete(delete_file))
        .route("/api/batch_delete", post(batch_delete_files))
        .route(
            "/d/:file_id/:filename",
            get(download_file_legacy).head(download_file_legacy_head),
        )
        .route(
            "/d/:identifier",
            get(download_file_short).head(download_file_short_head),
        )
}

#[cfg(test)]
mod tests {
    use super::chunk_download_failed_response;
    use axum::http::StatusCode;

    #[test]
    fn manifest_chunk_failure_returns_bad_gateway() {
        let response = chunk_download_failed_response("chunk-1");
        assert_eq!(response.status(), StatusCode::BAD_GATEWAY);
    }
}
