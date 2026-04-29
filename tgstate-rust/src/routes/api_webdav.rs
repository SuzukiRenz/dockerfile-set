use std::sync::Arc;

use axum::body::{to_bytes, Body};
use axum::extract::{Path, State};
use axum::http::{header, HeaderMap, HeaderValue, Method, StatusCode};
use axum::response::{IntoResponse, Response};
use axum::routing::any;
use axum::Router;
use base64::{engine::general_purpose, Engine as _};
use serde::Serialize;

use crate::config;
use crate::constants;
use crate::database;
use crate::error::http_error;
use crate::routes::api_upload;
use crate::state::AppState;
use crate::storage;
use crate::telegram::service::TelegramService;

#[derive(Serialize)]
struct WebDavListItem {
    href: String,
    name: String,
    size: i64,
    modified: String,
    is_collection: bool,
}

fn xml_escape(input: &str) -> String {
    input
        .replace('&', "&amp;")
        .replace('<', "&lt;")
        .replace('>', "&gt;")
        .replace('"', "&quot;")
        .replace('\'', "&apos;")
}

fn parse_bool(value: &str) -> bool {
    matches!(
        value.trim().to_ascii_lowercase().as_str(),
        "1" | "true" | "yes" | "on"
    )
}

fn is_enabled(state: &AppState) -> bool {
    let app_settings = config::get_app_settings(&state.settings, &state.db_pool);
    app_settings
        .get("WEBDAV_ENABLED")
        .and_then(|v| v.as_deref())
        .map(parse_bool)
        .unwrap_or(false)
}

fn is_readonly(state: &AppState) -> bool {
    let app_settings = config::get_app_settings(&state.settings, &state.db_pool);
    app_settings
        .get("WEBDAV_READONLY")
        .and_then(|v| v.as_deref())
        .map(parse_bool)
        .unwrap_or(false)
}

fn webdav_href(name: &str) -> String {
    let encoded = name
        .split('/')
        .filter(|segment| !segment.is_empty())
        .map(|segment| {
            percent_encoding::utf8_percent_encode(segment, percent_encoding::NON_ALPHANUMERIC)
                .to_string()
        })
        .collect::<Vec<_>>()
        .join("/");
    format!("/webdav/{}", encoded)
}

fn unauthorized_response() -> Response {
    let mut headers = HeaderMap::new();
    headers.insert(
        header::WWW_AUTHENTICATE,
        HeaderValue::from_static("Basic realm=\"WebDAV\"")
    );
    (StatusCode::UNAUTHORIZED, headers, "Unauthorized").into_response()
}

fn check_webdav_auth(state: &AppState, headers: &HeaderMap) -> bool {
    let app_settings = config::get_app_settings(&state.settings, &state.db_pool);
    let username = app_settings
        .get("WEBDAV_USERNAME")
        .and_then(|v| v.as_deref())
        .filter(|v| !v.trim().is_empty())
        .unwrap_or("admin")
        .trim();
    let password = match config::get_active_password(&state.settings, &state.db_pool) {
        Some(v) if !v.trim().is_empty() => v,
        _ => return false,
    };

    let auth_value = match headers
        .get(header::AUTHORIZATION)
        .and_then(|v| v.to_str().ok())
    {
        Some(v) if v.starts_with("Basic ") => &v[6..],
        _ => return false,
    };

    let decoded = match general_purpose::STANDARD.decode(auth_value) {
        Ok(v) => v,
        Err(_) => return false,
    };
    let decoded = match String::from_utf8(decoded) {
        Ok(v) => v,
        Err(_) => return false,
    };
    let (provided_username, provided_password) = match decoded.split_once(':') {
        Some(v) => v,
        None => return false,
    };

    crate::auth::secure_compare(provided_username, username)
        && crate::auth::verify_password_auto(provided_password, password.trim())
}

fn lookup_file(state: &AppState, identifier: &str) -> Option<database::FileMetadata> {
    if let Ok(Some(file)) = database::get_file_by_id(&state.db_pool, identifier) {
        return Some(file);
    }

    if let Ok(Some(file)) = database::get_file_by_webdav_path(&state.db_pool, identifier) {
        return Some(file);
    }

    database::get_all_files(&state.db_pool)
        .ok()?
        .into_iter()
        .find(|f| f.filename == identifier)
}

fn split_webdav_path(path: &str) -> Vec<&str> {
    path.split('/').filter(|segment| !segment.is_empty()).collect()
}

fn file_virtual_path(file: &database::FileMetadata) -> String {
    if file.folder_path.is_empty() {
        file.filename.clone()
    } else {
        format!("{}/{}", file.folder_path, file.filename)
    }
}

fn list_folder_entries(state: &AppState, current_path: &str) -> Vec<WebDavListItem> {
    let current = database::normalize_folder_path(current_path);
    let files = database::get_all_files(&state.db_pool).unwrap_or_default();
    let mut folders = std::collections::BTreeSet::new();
    let mut items = Vec::new();

    for file in files {
        let virtual_path = file_virtual_path(&file);
        let parts = split_webdav_path(&virtual_path);
        if parts.is_empty() {
            continue;
        }

        let current_parts = split_webdav_path(&current);
        if parts.len() <= current_parts.len() || parts[..current_parts.len()] != current_parts[..] {
            continue;
        }

        let remaining = &parts[current_parts.len()..];
        if remaining.len() == 1 {
            items.push(WebDavListItem {
                href: webdav_href(&virtual_path),
                name: file.filename.clone(),
                size: file.filesize,
                modified: if file.upload_date.is_empty() {
                    chrono::Utc::now().to_rfc2822()
                } else {
                    file.upload_date.clone()
                },
                is_collection: false,
            });
        } else {
            let folder_rel = remaining[0];
            let folder_full = if current.is_empty() {
                folder_rel.to_string()
            } else {
                format!("{}/{}", current, folder_rel)
            };
            folders.insert(folder_full);
        }
    }

    let mut folder_items = folders
        .into_iter()
        .map(|folder| WebDavListItem {
            href: webdav_href(&folder),
            name: folder.rsplit('/').next().unwrap_or(&folder).to_string(),
            size: 0,
            modified: chrono::Utc::now().to_rfc2822(),
            is_collection: true,
        })
        .collect::<Vec<_>>();

    folder_items.extend(items);
    folder_items
}

fn build_multistatus(base: &str, items: &[WebDavListItem]) -> String {
    let mut xml = String::from(
        "<?xml version=\"1.0\" encoding=\"utf-8\"?><d:multistatus xmlns:d=\"DAV:\">\n",
    );
    for item in items {
        let href = format!(
            "{}/{}",
            base.trim_end_matches('/'),
            item.href.trim_start_matches('/')
        );
        let resource_type = if item.is_collection {
            "<d:collection/>"
        } else {
            ""
        };
        xml.push_str(&format!(
            "<d:response><d:href>{}</d:href><d:propstat><d:prop><d:displayname>{}</d:displayname><d:getcontentlength>{}</d:getcontentlength><d:getlastmodified>{}</d:getlastmodified><d:resourcetype>{}</d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>",
            xml_escape(&href),
            xml_escape(&item.name),
            item.size,
            xml_escape(&item.modified),
            resource_type,
        ));
    }
    xml.push_str("</d:multistatus>");
    xml
}

fn options_response(allow: &'static str) -> Response {
    let mut headers = HeaderMap::new();
    headers.insert("DAV", HeaderValue::from_static("1"));
    headers.insert("Allow", HeaderValue::from_static(allow));
    headers.insert("MS-Author-Via", HeaderValue::from_static("DAV"));
    (StatusCode::NO_CONTENT, headers).into_response()
}

fn readonly_response() -> Response {
    http_error(StatusCode::FORBIDDEN, "webdav is read-only", "webdav_readonly").into_response()
}

fn get_telegram_service(state: &AppState) -> Result<TelegramService, Response> {
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
        return Err(
            http_error(StatusCode::SERVICE_UNAVAILABLE, "bot not configured", "bot_not_configured")
                .into_response(),
        );
    }

    Ok(TelegramService::new(
        token,
        channel,
        state.http_client.clone(),
    ))
}

async fn put_file(state: Arc<AppState>, identifier: String, body: Body) -> Response {
    let normalized_path = database::normalize_folder_path(&identifier);
    let path_parts = split_webdav_path(&normalized_path);
    let Some(raw_filename) = path_parts.last() else {
        return http_error(StatusCode::BAD_REQUEST, "invalid filename", "invalid_filename")
            .into_response();
    };

    let filename = api_upload::sanitize_filename(raw_filename);
    if filename.is_empty() {
        return http_error(StatusCode::BAD_REQUEST, "invalid filename", "invalid_filename")
            .into_response();
    }

    let folder_path = if path_parts.len() > 1 {
        path_parts[..path_parts.len() - 1].join("/")
    } else {
        String::new()
    };

    let app_settings = config::get_app_settings(&state.settings, &state.db_pool);
    let backend = app_settings
        .get("STORAGE_BACKEND")
        .and_then(|v| v.as_deref())
        .unwrap_or(constants::STORAGE_BACKEND_TELEGRAM);

    let upload_result = if backend == constants::STORAGE_BACKEND_S3 {
        let bytes = match to_bytes(body, constants::MAX_UPLOAD_BODY_SIZE).await {
            Ok(bytes) => bytes,
            Err(e) => {
                tracing::error!("WebDAV body read failed: {}", e);
                return http_error(StatusCode::BAD_REQUEST, "invalid upload body", "invalid_body")
                    .into_response();
            }
        };

        storage::s3::upload_bytes_in_folder(
            &state,
            &filename,
            bytes.to_vec(),
            bytes.len() as i64,
            &folder_path,
        )
        .await
    } else {
        let tg_service = match get_telegram_service(&state) {
            Ok(service) => service,
            Err(resp) => return resp,
        };
        api_upload::upload_body_to_telegram(
            &tg_service,
            body,
            &filename,
            &state.db_pool,
            &folder_path,
            constants::MAX_UPLOAD_BODY_SIZE,
        )
        .await
    };

    match upload_result {
        Ok(short_id) => (
            StatusCode::CREATED,
            [(header::ETAG, format!("\"{}\"", short_id))],
        )
            .into_response(),
        Err(e) => {
            tracing::error!("WebDAV PUT failed: {}", e);
            http_error(StatusCode::BAD_GATEWAY, "upload failed", "webdav_put_failed")
                .into_response()
        }
    }
}

async fn root_handler(
    State(state): State<Arc<AppState>>,
    method: Method,
    headers: HeaderMap,
) -> Response {
    if !is_enabled(&state) {
        return http_error(StatusCode::NOT_FOUND, "webdav disabled", "webdav_disabled")
            .into_response();
    }

    if !check_webdav_auth(&state, &headers) {
        return unauthorized_response();
    }

    let readonly = is_readonly(&state);

    match method.as_str() {
        "OPTIONS" => {
            if readonly {
                options_response("OPTIONS, GET, HEAD, PROPFIND")
            } else {
                options_response("OPTIONS, GET, HEAD, PROPFIND, PUT")
            }
        }
        "GET" | "HEAD" => (
            StatusCode::OK,
            [(header::CONTENT_TYPE, "text/plain; charset=utf-8")],
            "WebDAV endpoint",
        )
            .into_response(),
        "PROPFIND" => {
            let depth = headers
                .get("depth")
                .and_then(|v| v.to_str().ok())
                .unwrap_or("1");

            let mut items = vec![WebDavListItem {
                href: "/webdav".into(),
                name: "webdav".into(),
                size: 0,
                modified: chrono::Utc::now().to_rfc2822(),
                is_collection: true,
            }];

            if depth != "0" {
                items.extend(list_folder_entries(&state, ""));
            }

            let body = build_multistatus("", &items);
            (
                StatusCode::MULTI_STATUS,
                [(header::CONTENT_TYPE, "application/xml; charset=utf-8")],
                body,
            )
                .into_response()
        }
        "PUT" => {
            if readonly {
                readonly_response()
            } else {
                StatusCode::METHOD_NOT_ALLOWED.into_response()
            }
        }
        "DELETE" | "MKCOL" | "MOVE" | "COPY" => {
            if readonly {
                readonly_response()
            } else {
                StatusCode::METHOD_NOT_ALLOWED.into_response()
            }
        }
        _ => StatusCode::METHOD_NOT_ALLOWED.into_response(),
    }
}

async fn entry_handler(
    State(state): State<Arc<AppState>>,
    Path(identifier): Path<String>,
    method: Method,
    headers: HeaderMap,
    body: Body,
) -> Response {
    if !is_enabled(&state) {
        return http_error(StatusCode::NOT_FOUND, "webdav disabled", "webdav_disabled")
            .into_response();
    }

    if !check_webdav_auth(&state, &headers) {
        return unauthorized_response();
    }

    let readonly = is_readonly(&state);

    match method.as_str() {
        "OPTIONS" => {
            if readonly {
                options_response("OPTIONS, PROPFIND, GET, HEAD")
            } else {
                options_response("OPTIONS, PROPFIND, GET, HEAD, PUT")
            }
        }
        "PROPFIND" => {
            let normalized = database::normalize_folder_path(&identifier);
            if let Some(f) = lookup_file(&state, &normalized) {
                let item = WebDavListItem {
                    href: webdav_href(&file_virtual_path(&f)),
                    name: f.filename,
                    size: f.filesize,
                    modified: if f.upload_date.is_empty() {
                        chrono::Utc::now().to_rfc2822()
                    } else {
                        f.upload_date
                    },
                    is_collection: false,
                };
                let body = build_multistatus("", &[item]);
                (
                    StatusCode::MULTI_STATUS,
                    [(header::CONTENT_TYPE, "application/xml; charset=utf-8")],
                    body,
                )
                    .into_response()
            } else {
                let entries = list_folder_entries(&state, &normalized);
                if entries.is_empty() {
                    http_error(StatusCode::NOT_FOUND, "file not found", "not_found").into_response()
                } else {
                    let current_name = normalized.rsplit('/').next().unwrap_or(&normalized);
                    let mut items = vec![WebDavListItem {
                        href: webdav_href(&normalized),
                        name: current_name.to_string(),
                        size: 0,
                        modified: chrono::Utc::now().to_rfc2822(),
                        is_collection: true,
                    }];
                    let depth = headers
                        .get("depth")
                        .and_then(|v| v.to_str().ok())
                        .unwrap_or("1");
                    if depth != "0" {
                        items.extend(entries);
                    }
                    let body = build_multistatus("", &items);
                    (
                        StatusCode::MULTI_STATUS,
                        [(header::CONTENT_TYPE, "application/xml; charset=utf-8")],
                        body,
                    )
                        .into_response()
                }
            }
        }
        "GET" | "HEAD" => match lookup_file(&state, &database::normalize_folder_path(&identifier)) {
            Some(f) => Response::builder()
                .status(StatusCode::TEMPORARY_REDIRECT)
                .header(
                    header::LOCATION,
                    format!("/d/{}", f.short_id.unwrap_or(f.file_id)),
                )
                .body(axum::body::Body::empty())
                .unwrap(),
            None => http_error(StatusCode::NOT_FOUND, "file not found", "not_found").into_response(),
        },
        "PUT" => {
            if readonly {
                readonly_response()
            } else {
                put_file(state.clone(), identifier, body).await
            }
        }
        "DELETE" | "MKCOL" | "MOVE" | "COPY" => {
            if readonly {
                readonly_response()
            } else {
                StatusCode::METHOD_NOT_ALLOWED.into_response()
            }
        }
        _ => StatusCode::METHOD_NOT_ALLOWED.into_response(),
    }
}

pub fn router() -> Router<Arc<AppState>> {
    Router::new()
        .route("/webdav", any(root_handler))
        .route("/webdav/", any(root_handler))
        .route("/webdav/*identifier", any(entry_handler))
}
