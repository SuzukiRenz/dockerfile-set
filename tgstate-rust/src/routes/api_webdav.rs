use std::sync::Arc;

use axum::body::Body;
use axum::extract::{Path, State};
use axum::http::{header, HeaderMap, HeaderName, HeaderValue, Method, StatusCode, Uri};
use axum::response::{IntoResponse, Response};
use axum::routing::any;
use axum::Router;
use base64::{engine::general_purpose, Engine as _};
use serde::Serialize;

use crate::config;
use crate::constants;
use crate::database;
use crate::error::http_error;
use crate::routes::{api_files, api_upload};
use crate::state::AppState;
use crate::telegram::service::DeleteResult;
use crate::telegram::service::TelegramService;

#[derive(Serialize)]
struct WebDavListItem {
    href: String,
    name: String,
    size: i64,
    created: String,
    modified: String,
    is_collection: bool,
    content_type: String,
}

fn guess_webdav_content_type(name: &str) -> String {
    mime_guess::from_path(name)
        .first_or_octet_stream()
        .to_string()
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
    if encoded.is_empty() {
        "/webdav/".to_string()
    } else {
        format!("/webdav/{}/", encoded)
            .trim_end_matches('/')
            .to_string()
    }
}

fn decode_webdav_identifier(identifier: &str) -> String {
    identifier
        .split('/')
        .filter(|segment| !segment.is_empty())
        .map(|segment| {
            percent_encoding::percent_decode_str(segment)
                .decode_utf8_lossy()
                .to_string()
        })
        .collect::<Vec<_>>()
        .join("/")
}

fn unauthorized_response() -> Response {
    let mut headers = HeaderMap::new();
    headers.insert(
        header::WWW_AUTHENTICATE,
        HeaderValue::from_static("Basic realm=\"WebDAV\""),
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
    path.split('/')
        .filter(|segment| !segment.is_empty())
        .collect()
}

fn file_virtual_path(file: &database::FileMetadata) -> String {
    if file.folder_path.is_empty() {
        file.filename.clone()
    } else {
        format!("{}/{}", file.folder_path, file.filename)
    }
}

/// Parse whatever format `upload_date` happens to be stored in. Falls back
/// to "now" for anything unparseable rather than handing an invalid string
/// to the client - that's what was causing some WebDAV clients to fall back
/// to displaying the Unix epoch (1970-01-01) as the modified date.
fn parse_stored_timestamp(value: &str) -> chrono::DateTime<chrono::Utc> {
    let raw = value.trim();
    if raw.is_empty() {
        return chrono::Utc::now();
    }
    if let Ok(dt) = chrono::DateTime::parse_from_rfc3339(raw) {
        return dt.with_timezone(&chrono::Utc);
    }
    if let Ok(ndt) = chrono::NaiveDateTime::parse_from_str(raw, "%Y-%m-%d %H:%M:%S") {
        return chrono::DateTime::<chrono::Utc>::from_naive_utc_and_offset(ndt, chrono::Utc);
    }
    if let Ok(ndt) = chrono::NaiveDateTime::parse_from_str(raw, "%Y-%m-%dT%H:%M:%S") {
        return chrono::DateTime::<chrono::Utc>::from_naive_utc_and_offset(ndt, chrono::Utc);
    }
    chrono::Utc::now()
}

/// `<d:getlastmodified>` is specified (RFC 4918 / RFC 2068) to use the HTTP
/// `Date` header format, i.e. RFC 1123 with a literal `GMT` suffix - e.g.
/// `Wed, 09 Jul 2026 10:00:00 GMT`. `chrono`'s `to_rfc2822()` instead emits a
/// numeric offset (`+0000`) and doesn't zero-pad single-digit days, which
/// several WebDAV clients (including openlist's Go-based parser) fail to
/// parse - silently falling back to the zero value, which renders as
/// 1970-01-01. Format it by hand to stay spec-compliant.
fn http_date(dt: &chrono::DateTime<chrono::Utc>) -> String {
    dt.format("%a, %d %b %Y %H:%M:%S GMT").to_string()
}

/// `<d:creationdate>` should be ISO 8601.
fn iso8601_date(dt: &chrono::DateTime<chrono::Utc>) -> String {
    dt.to_rfc3339_opts(chrono::SecondsFormat::Secs, true)
}

fn collection_timestamps() -> (String, String) {
    let now = chrono::Utc::now();
    (iso8601_date(&now), http_date(&now))
}

fn file_item_from_meta(file: database::FileMetadata) -> WebDavListItem {
    let dt = parse_stored_timestamp(&file.upload_date);
    let content_type = guess_webdav_content_type(&file.filename);
    WebDavListItem {
        href: webdav_href(&file_virtual_path(&file)),
        name: file.filename,
        size: file.filesize,
        created: iso8601_date(&dt),
        modified: http_date(&dt),
        is_collection: false,
        content_type,
    }
}

fn folder_exists(state: &AppState, folder_path: &str) -> bool {
    let normalized = database::normalize_folder_path(folder_path);
    if normalized.is_empty() {
        return true;
    }

    database::list_folder_paths(&state.db_pool)
        .unwrap_or_default()
        .into_iter()
        .any(|path| database::normalize_folder_path(&path) == normalized)
}

fn list_folder_entries(state: &AppState, current_path: &str) -> Vec<WebDavListItem> {
    let current = database::normalize_folder_path(current_path);
    let files = database::get_all_files(&state.db_pool).unwrap_or_default();
    let mut folders = std::collections::BTreeSet::new();
    let mut items = Vec::new();

    let current_parts = split_webdav_path(&current);
    for folder_path in database::list_folder_paths(&state.db_pool).unwrap_or_default() {
        let parts = split_webdav_path(&folder_path);
        if parts.len() <= current_parts.len() || parts[..current_parts.len()] != current_parts[..] {
            continue;
        }
        let remaining = &parts[current_parts.len()..];
        if let Some(folder_rel) = remaining.first() {
            let folder_full = if current.is_empty() {
                (*folder_rel).to_string()
            } else {
                format!("{}/{}", current, folder_rel)
            };
            folders.insert(folder_full);
        }
    }

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
            items.push(file_item_from_meta(file));
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
        .map(|folder| {
            let (created, modified) = collection_timestamps();
            WebDavListItem {
                href: webdav_href(&folder),
                name: folder.rsplit('/').next().unwrap_or(&folder).to_string(),
                size: 0,
                created,
                modified,
                is_collection: true,
                content_type: "httpd/unix-directory".to_string(),
            }
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
        let mut href = format!(
            "{}/{}",
            base.trim_end_matches('/'),
            item.href.trim_start_matches('/')
        );
        if item.is_collection && !href.ends_with('/') {
            href.push('/');
        }
        let resource_type = if item.is_collection {
            "<d:collection/>"
        } else {
            ""
        };
        xml.push_str(&format!(
            "<d:response><d:href>{}</d:href><d:propstat><d:prop><d:displayname>{}</d:displayname><d:creationdate>{}</d:creationdate><d:getlastmodified>{}</d:getlastmodified><d:getcontentlength>{}</d:getcontentlength><d:getcontenttype>{}</d:getcontenttype><d:resourcetype>{}</d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>",
            xml_escape(&href),
            xml_escape(&item.name),
            xml_escape(&item.created),
            xml_escape(&item.modified),
            item.size,
            xml_escape(&item.content_type),
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
    http_error(
        StatusCode::FORBIDDEN,
        "webdav is read-only",
        "webdav_readonly",
    )
    .into_response()
}

fn lock_response(uri_path: &str) -> Response {
    let token = format!("opaquelocktoken:{}", database::generate_public_id(24));
    let body = format!(
        "<?xml version=\"1.0\" encoding=\"utf-8\"?><d:prop xmlns:d=\"DAV:\"><d:lockdiscovery><d:activelock><d:locktype><d:write/></d:locktype><d:lockscope><d:exclusive/></d:lockscope><d:depth>infinity</d:depth><d:owner>tgstate</d:owner><d:timeout>Second-604800</d:timeout><d:locktoken><d:href>{}</d:href></d:locktoken><d:lockroot><d:href>{}</d:href></d:lockroot></d:activelock></d:lockdiscovery></d:prop>",
        xml_escape(&token),
        xml_escape(uri_path),
    );
    (
        StatusCode::OK,
        [
            (
                header::CONTENT_TYPE,
                "application/xml; charset=utf-8".to_string(),
            ),
            (
                HeaderName::from_static("lock-token"),
                format!("<{}>", token),
            ),
        ],
        body,
    )
        .into_response()
}

/// Extract the WebDAV-relative, decoded destination path from a MOVE/COPY
/// request's `Destination` header. The header may be a full URL
/// (`https://host/webdav/foo/bar.png`) or a bare path (`/webdav/foo/bar.png`);
/// both are handled without pulling in a full URL-parsing crate.
fn parse_destination_identifier(headers: &HeaderMap) -> Option<String> {
    let raw = headers.get("destination").and_then(|v| v.to_str().ok())?;

    let path_part = if let Some(scheme_end) = raw.find("://") {
        // Skip "scheme://", then skip past the host up to the next '/'.
        let after_scheme = &raw[scheme_end + 3..];
        match after_scheme.find('/') {
            Some(idx) => &after_scheme[idx..],
            None => "/",
        }
    } else {
        raw
    };

    let stripped = path_part
        .strip_prefix("/webdav/")
        .or_else(|| path_part.strip_prefix("/webdav"))
        .unwrap_or(path_part);

    Some(decode_webdav_identifier(stripped))
}

fn header_overwrite_allowed(headers: &HeaderMap) -> bool {
    match headers.get("overwrite").and_then(|v| v.to_str().ok()) {
        Some(v) => !v.eq_ignore_ascii_case("F"),
        None => true,
    }
}

async fn move_entry(state: Arc<AppState>, source_identifier: String, headers: HeaderMap) -> Response {
    let Some(dest_identifier) = parse_destination_identifier(&headers) else {
        return http_error(
            StatusCode::BAD_REQUEST,
            "missing Destination header",
            "missing_destination",
        )
        .into_response();
    };

    let source_normalized = database::normalize_folder_path(&source_identifier);
    let dest_normalized = database::normalize_folder_path(&dest_identifier);

    if source_normalized.is_empty() || dest_normalized.is_empty() {
        return http_error(StatusCode::BAD_REQUEST, "invalid path", "invalid_path").into_response();
    }
    if source_normalized == dest_normalized {
        return StatusCode::NO_CONTENT.into_response();
    }

    let overwrite_allowed = header_overwrite_allowed(&headers);

    // Case 1: source is a single file.
    if let Some(source_file) = lookup_file(&state, &source_normalized) {
        let dest_parts = split_webdav_path(&dest_normalized);
        let Some(raw_dest_name) = dest_parts.last() else {
            return http_error(StatusCode::BAD_REQUEST, "invalid destination", "invalid_destination")
                .into_response();
        };
        let dest_filename = api_upload::sanitize_filename(raw_dest_name);
        if dest_filename.is_empty() {
            return http_error(StatusCode::BAD_REQUEST, "invalid destination", "invalid_destination")
                .into_response();
        }
        let dest_folder = if dest_parts.len() > 1 {
            dest_parts[..dest_parts.len() - 1].join("/")
        } else {
            String::new()
        };

        let existing_target = lookup_file(&state, &dest_normalized);
        let target_existed = existing_target.is_some();
        if target_existed && !overwrite_allowed {
            return http_error(
                StatusCode::PRECONDITION_FAILED,
                "destination exists",
                "destination_exists",
            )
            .into_response();
        }
        if let Some(target_file) = existing_target {
            // Overwriting: remove the old target (metadata + Telegram
            // message) before sliding the source into its place.
            let resp = delete_webdav_file(state.clone(), target_file).await;
            if !resp.status().is_success() {
                return resp;
            }
        }

        // `lookup_file` resolves by short_id, by file_id, by full webdav
        // path (folder_path + filename), or by bare filename - which is how
        // WebDAV clients actually address resources, since they have no
        // concept of our internal short_id. `rename_file` itself only
        // matches rows by short_id/file_id, so we must key the UPDATE off
        // `source_file.file_id` (always present) rather than the raw
        // path-based identifier the client sent - otherwise the UPDATE
        // matches zero rows and this incorrectly reports 404.
        let source_key = source_file.file_id.clone();
        return match database::rename_file(&state.db_pool, &source_key, &dest_folder, &dest_filename) {
            Ok(true) => {
                if target_existed {
                    StatusCode::NO_CONTENT.into_response()
                } else {
                    StatusCode::CREATED.into_response()
                }
            }
            Ok(false) => http_error(StatusCode::NOT_FOUND, "file not found", "not_found").into_response(),
            Err(e) => crate::error::AppError::from(e).into_response(),
        };
    }

    // Case 2: source is a folder (has entries, or was explicitly created via MKCOL).
    let has_entries = !list_folder_entries(&state, &source_normalized).is_empty();
    if has_entries || folder_exists(&state, &source_normalized) {
        // Refuse to move a folder into its own subtree.
        let dest_prefix = format!("{}/", source_normalized);
        if dest_normalized == source_normalized || dest_normalized.starts_with(&dest_prefix) {
            return http_error(
                StatusCode::CONFLICT,
                "cannot move a folder into itself",
                "invalid_move",
            )
            .into_response();
        }

        let dest_already_exists = folder_exists(&state, &dest_normalized);
        if dest_already_exists && !overwrite_allowed {
            return http_error(
                StatusCode::PRECONDITION_FAILED,
                "destination exists",
                "destination_exists",
            )
            .into_response();
        }

        return match database::rename_folder_path(&state.db_pool, &source_normalized, &dest_normalized) {
            Ok(_) => {
                if dest_already_exists {
                    StatusCode::NO_CONTENT.into_response()
                } else {
                    StatusCode::CREATED.into_response()
                }
            }
            Err(e) => crate::error::AppError::from(e).into_response(),
        };
    }

    http_error(StatusCode::NOT_FOUND, "source not found", "not_found").into_response()
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
        return Err(http_error(
            StatusCode::SERVICE_UNAVAILABLE,
            "bot not configured",
            "bot_not_configured",
        )
        .into_response());
    }

    Ok(TelegramService::new(
        token,
        channel,
        state.http_client.clone(),
    ))
}

async fn delete_webdav_file(state: Arc<AppState>, meta: database::FileMetadata) -> Response {
    let tg_service = match get_telegram_service(&state) {
        Ok(service) => service,
        Err(resp) => return resp,
    };
    let result: DeleteResult = tg_service.delete_file_with_chunks(&meta.file_id).await;

    // Telegram permanently refusing a deletion (wrong/insufficient admin
    // rights, or its 48h-old-message restriction for non-admin accounts) is
    // not something a retry will ever fix - the file would otherwise be
    // stuck in tgstate forever. In that case we still remove our own
    // metadata so the file disappears from WebDAV/the admin UI, and just
    // leave the now-orphaned message sitting in the Telegram channel (that
    // costs nothing storage-wise). A genuine transport/network failure is
    // different - it's worth surfacing as 502 so the client retries.
    let telegram_permanently_refused = result.main_delete_reason.starts_with("telegram_error:");

    if result.main_message_deleted || result.main_delete_reason == "not_found" {
        match database::delete_file_metadata(&state.db_pool, &meta.file_id) {
            Ok(true) => StatusCode::NO_CONTENT.into_response(),
            Ok(false) => {
                http_error(StatusCode::NOT_FOUND, "file not found", "not_found").into_response()
            }
            Err(e) => crate::error::AppError::from(e).into_response(),
        }
    } else if telegram_permanently_refused {
        tracing::warn!(
            "Telegram refused to delete message for '{}' ({}) - removing tgstate metadata anyway \
             and leaving the message orphaned in the channel. Reason: {}",
            meta.filename,
            meta.file_id,
            result.main_delete_reason,
        );
        match database::delete_file_metadata(&state.db_pool, &meta.file_id) {
            Ok(true) => StatusCode::NO_CONTENT.into_response(),
            Ok(false) => {
                http_error(StatusCode::NOT_FOUND, "file not found", "not_found").into_response()
            }
            Err(e) => crate::error::AppError::from(e).into_response(),
        }
    } else {
        tracing::error!("WebDAV DELETE failed: {:?}", result);
        http_error(
            StatusCode::BAD_GATEWAY,
            "delete failed",
            "webdav_delete_failed",
        )
        .into_response()
    }
}

async fn delete_folder(state: Arc<AppState>, folder_path: &str) -> Response {
    let normalized = database::normalize_folder_path(folder_path);
    if normalized.is_empty() {
        return http_error(StatusCode::BAD_REQUEST, "invalid folder", "invalid_folder")
            .into_response();
    }

    let prefix = format!("{}/", normalized);
    let files = database::get_all_files(&state.db_pool)
        .unwrap_or_default()
        .into_iter()
        .filter(|file| file.folder_path == normalized || file.folder_path.starts_with(&prefix))
        .collect::<Vec<_>>();

    // Delete files concurrently (bounded) instead of one-by-one. A folder
    // with dozens of files serialized through Telegram's API round-trip can
    // easily blow past a reverse proxy / tunnel's request timeout (e.g.
    // Cloudflare Tunnel's ~100s edge timeout), which surfaces as a 502 even
    // when every individual delete would have succeeded on its own.
    let sem = std::sync::Arc::new(tokio::sync::Semaphore::new(8));
    let mut handles = Vec::with_capacity(files.len());
    for file in files {
        let state = state.clone();
        let sem = sem.clone();
        handles.push(tokio::spawn(async move {
            let _permit = sem.acquire().await;
            let filename = file.filename.clone();
            let response = delete_webdav_file(state, file).await;
            (filename, response.status())
        }));
    }

    let mut failures = Vec::new();
    for handle in handles {
        match handle.await {
            Ok((filename, status)) if !status.is_success() => failures.push((filename, status)),
            Ok(_) => {}
            Err(e) => {
                tracing::error!("WebDAV folder delete task panicked: {:?}", e);
                failures.push((normalized.clone(), StatusCode::INTERNAL_SERVER_ERROR));
            }
        }
    }

    if !failures.is_empty() {
        tracing::error!(
            "WebDAV DELETE folder '{}' partially failed: {:?}",
            normalized,
            failures
        );
        // Some files may already be gone at this point - that's fine, a
        // retry from the client will just find fewer entries left. We
        // deliberately don't roll anything back; reporting 502 here mirrors
        // the single-file failure semantics used elsewhere in this module.
        return http_error(
            StatusCode::BAD_GATEWAY,
            "some files in this folder could not be deleted",
            "webdav_delete_partial_failure",
        )
        .into_response();
    }

    match database::delete_folder_path(&state.db_pool, &normalized) {
        Ok(_) => StatusCode::NO_CONTENT.into_response(),
        Err(e) => crate::error::AppError::from(e).into_response(),
    }
}

async fn put_file(state: Arc<AppState>, identifier: String, body: Body) -> Response {
    let normalized_path = database::normalize_folder_path(&identifier);
    let path_parts = split_webdav_path(&normalized_path);
    let Some(raw_filename) = path_parts.last() else {
        return http_error(
            StatusCode::BAD_REQUEST,
            "invalid filename",
            "invalid_filename",
        )
        .into_response();
    };

    let filename = api_upload::sanitize_filename(raw_filename);
    if filename.is_empty() {
        return http_error(
            StatusCode::BAD_REQUEST,
            "invalid filename",
            "invalid_filename",
        )
        .into_response();
    }

    let folder_path = if path_parts.len() > 1 {
        path_parts[..path_parts.len() - 1].join("/")
    } else {
        String::new()
    };

    let existing = lookup_file(&state, &normalized_path);
    let tg_service = match get_telegram_service(&state) {
        Ok(service) => service,
        Err(resp) => return resp,
    };
    let upload_result = api_upload::upload_body_to_telegram(
        &tg_service,
        body,
        &filename,
        &state.db_pool,
        &folder_path,
        constants::MAX_UPLOAD_BODY_SIZE,
    )
    .await;

    match upload_result {
        Ok(short_id) => {
            if let Some(old_meta) = existing {
                let _ = delete_webdav_file(state.clone(), old_meta).await;
            }
            (
                StatusCode::CREATED,
                [(header::ETAG, format!("\"{}\"", short_id))],
            )
                .into_response()
        }
        Err(e) => {
            tracing::error!("WebDAV PUT failed: {}", e);
            http_error(
                StatusCode::BAD_GATEWAY,
                "upload failed",
                "webdav_put_failed",
            )
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
                options_response("OPTIONS, GET, HEAD, PROPFIND, PUT, MKCOL, DELETE, MOVE, LOCK, UNLOCK")
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

            let (created, modified) = collection_timestamps();
            let mut items = vec![WebDavListItem {
                href: "/webdav".into(),
                name: "webdav".into(),
                size: 0,
                created,
                modified,
                is_collection: true,
                content_type: "httpd/unix-directory".to_string(),
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
        "MKCOL" => {
            if readonly {
                readonly_response()
            } else {
                http_error(StatusCode::BAD_REQUEST, "invalid folder", "invalid_folder")
                    .into_response()
            }
        }
        "DELETE" => {
            if readonly {
                readonly_response()
            } else {
                http_error(
                    StatusCode::FORBIDDEN,
                    "cannot delete webdav root",
                    "delete_root_forbidden",
                )
                .into_response()
            }
        }
        "MOVE" | "COPY" => {
            if readonly {
                readonly_response()
            } else {
                StatusCode::METHOD_NOT_ALLOWED.into_response()
            }
        }
        "LOCK" => {
            if readonly {
                readonly_response()
            } else {
                lock_response("/webdav/")
            }
        }
        "UNLOCK" => {
            if readonly {
                readonly_response()
            } else {
                StatusCode::NO_CONTENT.into_response()
            }
        }
        _ => StatusCode::METHOD_NOT_ALLOWED.into_response(),
    }
}

async fn entry_handler(
    State(state): State<Arc<AppState>>,
    Path(identifier): Path<String>,
    method: Method,
    uri: Uri,
    headers: HeaderMap,
    body: Body,
) -> Response {
    let identifier = decode_webdav_identifier(&identifier);
    let uri_path = uri.path().to_string();
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
                options_response("OPTIONS, PROPFIND, GET, HEAD, PUT, MKCOL, DELETE, MOVE, LOCK, UNLOCK")
            }
        }
        "PROPFIND" => {
            let normalized = database::normalize_folder_path(&identifier);
            let folder_requested = uri_path.ends_with('/');
            if let Some(f) = lookup_file(&state, &normalized)
                .filter(|_| !folder_requested || !folder_exists(&state, &normalized))
            {
                let item = file_item_from_meta(f);
                let body = build_multistatus("", &[item]);
                (
                    StatusCode::MULTI_STATUS,
                    [(header::CONTENT_TYPE, "application/xml; charset=utf-8")],
                    body,
                )
                    .into_response()
            } else {
                let entries = list_folder_entries(&state, &normalized);
                if entries.is_empty() && !folder_exists(&state, &normalized) {
                    http_error(StatusCode::NOT_FOUND, "file not found", "not_found").into_response()
                } else {
                    let current_name = normalized.rsplit('/').next().unwrap_or(&normalized);
                    let (created, modified) = collection_timestamps();
                    let mut items = vec![WebDavListItem {
                        href: webdav_href(&normalized),
                        name: current_name.to_string(),
                        size: 0,
                        created,
                        modified,
                        is_collection: true,
                        content_type: "httpd/unix-directory".to_string(),
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
        "GET" | "HEAD" => match lookup_file(&state, &database::normalize_folder_path(&identifier))
            .filter(|_| {
                !uri_path.ends_with('/')
                    || !folder_exists(&state, &database::normalize_folder_path(&identifier))
            }) {
            Some(f) => {
                api_files::serve_file(&state, &f, &headers, false, method == Method::HEAD).await
            }
            None => {
                http_error(StatusCode::NOT_FOUND, "file not found", "not_found").into_response()
            }
        },
        "PUT" => {
            if readonly {
                readonly_response()
            } else {
                put_file(state.clone(), identifier, body).await
            }
        }
        "MKCOL" => {
            if readonly {
                readonly_response()
            } else {
                let normalized = database::normalize_folder_path(&identifier);
                if normalized.is_empty() {
                    http_error(StatusCode::BAD_REQUEST, "invalid folder", "invalid_folder")
                        .into_response()
                } else if lookup_file(&state, &normalized).is_some() {
                    http_error(StatusCode::METHOD_NOT_ALLOWED, "file exists", "file_exists")
                        .into_response()
                } else if folder_exists(&state, &normalized) {
                    StatusCode::METHOD_NOT_ALLOWED.into_response()
                } else {
                    // New folders created through WebDAV default to
                    // "private" link visibility (rather than the normal
                    // inherited/public default) - files dropped into a
                    // folder you created via an OS-level file manager
                    // mount shouldn't be reachable through an
                    // unauthenticated public share link by default.
                    match database::ensure_folder_path(&state.db_pool, &normalized) {
                        Ok(true) => {
                            if let Err(e) = database::set_folder_visibility(
                                &state.db_pool,
                                &normalized,
                                "private",
                                false,
                            ) {
                                tracing::warn!(
                                    "MKCOL: failed to set default private visibility for '{}': {:?}",
                                    normalized,
                                    e
                                );
                            }
                            StatusCode::CREATED.into_response()
                        }
                        Ok(false) => StatusCode::METHOD_NOT_ALLOWED.into_response(),
                        Err(e) => crate::error::AppError::from(e).into_response(),
                    }
                }
            }
        }
        "DELETE" => {
            if readonly {
                readonly_response()
            } else {
                let normalized = database::normalize_folder_path(&identifier);
                if let Some(file) = lookup_file(&state, &normalized) {
                    delete_webdav_file(state.clone(), file).await
                } else {
                    let entries = list_folder_entries(&state, &normalized);
                    if entries.is_empty() {
                        match database::delete_folder_path(&state.db_pool, &normalized) {
                            Ok(0) => {
                                http_error(StatusCode::NOT_FOUND, "file not found", "not_found")
                                    .into_response()
                            }
                            Ok(_) => StatusCode::NO_CONTENT.into_response(),
                            Err(e) => crate::error::AppError::from(e).into_response(),
                        }
                    } else {
                        delete_folder(state.clone(), &normalized).await
                    }
                }
            }
        }
        "MOVE" => {
            if readonly {
                readonly_response()
            } else {
                move_entry(state.clone(), identifier, headers).await
            }
        }
        "COPY" => {
            // Not implemented: a real COPY would need to duplicate the
            // underlying Telegram message(s), not just the metadata row.
            StatusCode::METHOD_NOT_ALLOWED.into_response()
        }
        "LOCK" => {
            if readonly {
                readonly_response()
            } else {
                lock_response(&uri_path)
            }
        }
        "UNLOCK" => {
            if readonly {
                readonly_response()
            } else {
                StatusCode::NO_CONTENT.into_response()
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
