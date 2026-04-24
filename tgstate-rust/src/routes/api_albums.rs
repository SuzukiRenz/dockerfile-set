use std::sync::Arc;

use axum::extract::{Path, Query, State};
use axum::response::{Html, IntoResponse};
use axum::routing::{get, post};
use axum::{Json, Router};
use serde::Deserialize;

use crate::database;
use crate::error::{http_error, AppError};
use crate::state::AppState;

#[derive(Deserialize)]
struct CreateAlbumRequest {
    title: String,
    description: Option<String>,
    cover_file_id: Option<String>,
    is_public: Option<bool>,
    access_key: Option<String>,
    file_ids: Option<Vec<String>>,
}

#[derive(Deserialize)]
struct AddAlbumItemsRequest {
    file_ids: Vec<String>,
}

#[derive(Deserialize)]
struct AlbumAccessQuery {
    key: Option<String>,
}

async fn list_albums(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    match database::list_albums(&state.db_pool) {
        Ok(items) => Json(serde_json::json!({"status":"ok","items":items})).into_response(),
        Err(e) => crate::error::AppError::from(e).into_response(),
    }
}

async fn create_album(
    State(state): State<Arc<AppState>>,
    Json(payload): Json<CreateAlbumRequest>,
) -> impl IntoResponse {
    if payload.title.trim().is_empty() {
        return http_error(axum::http::StatusCode::BAD_REQUEST, "title 不能为空", "invalid_title").into_response();
    }

    if let Some(file_ids) = payload.file_ids.as_ref() {
        match database::resolve_file_ids(&state.db_pool, file_ids) {
            Ok((_, missing)) if !missing.is_empty() => {
                return AppError::with_details(
                    axum::http::StatusCode::BAD_REQUEST,
                    "部分文件不存在",
                    "album_files_not_found",
                    serde_json::json!({ "missing": missing }),
                )
                .into_response();
            }
            Err(e) => return AppError::from(e).into_response(),
            _ => {}
        }
    }

    match database::create_album(
        &state.db_pool,
        payload.title.trim(),
        payload.description.as_deref(),
        payload.cover_file_id.as_deref(),
        payload.is_public.unwrap_or(true),
        payload.access_key.as_deref(),
    ) {
        Ok(album_id) => {
            if let Some(file_ids) = payload.file_ids.as_ref() {
                if let Err(e) = database::add_album_items(&state.db_pool, &album_id, file_ids) {
                    return AppError::from(e).into_response();
                }
            }
            Json(serde_json::json!({"status":"ok","album_id":album_id})).into_response()
        }
        Err(e) => AppError::from(e).into_response(),
    }
}

async fn add_album_items(
    State(state): State<Arc<AppState>>,
    Path(album_id): Path<String>,
    Json(payload): Json<AddAlbumItemsRequest>,
) -> impl IntoResponse {
    if payload.file_ids.is_empty() {
        return http_error(axum::http::StatusCode::BAD_REQUEST, "file_ids 不能为空", "invalid_file_ids").into_response();
    }

    match database::resolve_file_ids(&state.db_pool, &payload.file_ids) {
        Ok((_, missing)) if !missing.is_empty() => AppError::with_details(
            axum::http::StatusCode::BAD_REQUEST,
            "部分文件不存在",
            "album_files_not_found",
            serde_json::json!({ "missing": missing }),
        )
        .into_response(),
        Ok(_) => match database::add_album_items(&state.db_pool, &album_id, &payload.file_ids) {
            Ok(_) => Json(serde_json::json!({"status":"ok"})).into_response(),
            Err(e) => AppError::from(e).into_response(),
        },
        Err(e) => AppError::from(e).into_response(),
    }
}

async fn get_album(
    State(state): State<Arc<AppState>>,
    Path(album_id): Path<String>,
    Query(query): Query<AlbumAccessQuery>,
) -> impl IntoResponse {
    match database::get_album(&state.db_pool, &album_id) {
        Ok(Some(album)) => {
            if !album.is_public {
                let required = album.access_key.as_deref().unwrap_or("");
                let provided = query.key.as_deref().unwrap_or("");
                if !required.is_empty() && required != provided {
                    return http_error(axum::http::StatusCode::UNAUTHORIZED, "图集需要访问码", "album_access_denied").into_response();
                }
            }
            let files = database::list_album_files(&state.db_pool, &album_id).unwrap_or_default();
            Json(serde_json::json!({"status":"ok","album":album,"items":files})).into_response()
        }
        Ok(None) => http_error(axum::http::StatusCode::NOT_FOUND, "图集不存在", "album_not_found").into_response(),
        Err(e) => crate::error::AppError::from(e).into_response(),
    }
}

async fn album_page(
    State(state): State<Arc<AppState>>,
    Path(album_id): Path<String>,
    Query(query): Query<AlbumAccessQuery>,
) -> impl IntoResponse {
    match database::get_album(&state.db_pool, &album_id) {
        Ok(Some(album)) => {
            if !album.is_public {
                let required = album.access_key.as_deref().unwrap_or("");
                let provided = query.key.as_deref().unwrap_or("");
                if !required.is_empty() && required != provided {
                    return (axum::http::StatusCode::UNAUTHORIZED, "album access denied").into_response();
                }
            }
            let files = database::list_album_files(&state.db_pool, &album_id).unwrap_or_default();
            let items: Vec<_> = files.into_iter().map(|f| {
                let sid = f.short_id.clone().unwrap_or(f.file_id.clone());
                let is_image = mime_guess::from_path(&f.filename)
                    .first_raw()
                    .is_some_and(|m| m.starts_with("image/"));
                serde_json::json!({
                    "filename": f.filename,
                    "short_id": sid,
                    "url": format!("/d/{}", sid),
                    "thumbnail_url": if is_image { format!("/d/{}", sid) } else { String::new() },
                    "filesize": f.filesize,
                    "upload_date": f.upload_date,
                    "is_image": is_image,
                })
            }).collect();
            let html = format!(
                "<!doctype html><html><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width,initial-scale=1\"><title>{}</title><style>body{{font-family:system-ui;background:#111;color:#eee;padding:24px}}.grid{{display:grid;grid-template-columns:repeat(auto-fill,minmax(180px,1fr));gap:16px}}a{{color:#9cf;text-decoration:none}}img{{width:100%;height:180px;object-fit:cover;border-radius:12px;background:#222}}.ph{{width:100%;height:180px;border-radius:12px;background:#222;display:flex;align-items:center;justify-content:center;color:#888;font-size:14px}}.card{{background:#1a1a1a;border-radius:16px;padding:12px}}.meta{{margin-top:8px;word-break:break-all;font-size:13px;line-height:1.5;color:#cfcfcf}}</style></head><body><h1>{}</h1><p>{}</p><div class=\"grid\">{}</div></body></html>",
                album.title,
                album.title,
                album.description.unwrap_or_default(),
                items.iter().map(|item| {
                    let url = item["url"].as_str().unwrap_or("#");
                    let thumb = item["thumbnail_url"].as_str().unwrap_or("");
                    let name = item["filename"].as_str().unwrap_or("file");
                    let preview = if item["is_image"].as_bool().unwrap_or(false) && !thumb.is_empty() {
                        format!("<img loading=\"lazy\" src=\"{}\" alt=\"{}\">", thumb, name)
                    } else {
                        "<div class=\"ph\">Preview unavailable</div>".to_string()
                    };
                    format!("<div class=\"card\"><a href=\"{}\" target=\"_blank\">{}</a><div class=\"meta\">{}</div></div>", url, preview, name)
                }).collect::<Vec<_>>().join(""),
            );
            Html(html).into_response()
        }
        _ => (axum::http::StatusCode::NOT_FOUND, "album not found").into_response(),
    }
}

pub fn router() -> Router<Arc<AppState>> {
    Router::new()
        .route("/api/albums", get(list_albums).post(create_album))
        .route("/api/albums/:album_id", get(get_album))
        .route("/api/albums/:album_id/items", post(add_album_items))
        .route("/album/:album_id", get(album_page))
}
