//! S3 Compatible Storage Backend
//! Supports AWS S3, Cloudflare R2, MinIO and other S3-compatible services.

use std::sync::Arc;

use crate::config;
use crate::constants;
use crate::database;
use crate::error::AppErrorKind;
use crate::state::AppState;

use s3::bucket::Bucket;
use s3::creds::Credentials;
use s3::region::Region;

fn app_cfg(state: &Arc<AppState>) -> config::AppSettingsMap {
    config::get_app_settings(&state.settings, &state.db_pool)
}

fn app_cfg_ref(state: &AppState) -> config::AppSettingsMap {
    config::get_app_settings(&state.settings, &state.db_pool)
}

fn parse_bool_setting(v: Option<&str>) -> bool {
    matches!(
        v.unwrap_or("").trim().to_ascii_lowercase().as_str(),
        "1" | "true" | "yes" | "on"
    )
}

fn generate_object_key(filename: &str) -> String {
    let object_id = database::generate_public_id(16);
    let clean_name = std::path::Path::new(filename)
        .file_name()
        .and_then(|s| s.to_str())
        .unwrap_or("upload.bin");
    let now = chrono::Utc::now();
    format!(
        "uploads/{}/{}-{}",
        now.format("%Y/%m/%d"),
        object_id,
        clean_name
    )
}

fn build_region(endpoint: &str, region_str: &str) -> Region {
    if endpoint.is_empty() {
        Region::Custom {
            region: region_str.to_string(),
            endpoint: format!("https://s3.{}.amazonaws.com", region_str),
        }
    } else {
        Region::Custom {
            region: region_str.to_string(),
            endpoint: endpoint.to_string(),
        }
    }
}

fn build_bucket(
    bucket_name: &str,
    endpoint: &str,
    region_str: &str,
    access_key: &str,
    secret_key: &str,
    path_style: bool,
) -> Result<Bucket, String> {
    let credentials = Credentials::new(
        Some(access_key),
        Some(secret_key),
        None,
        None,
        None,
    )
    .map_err(|e| format!("S3 凭证错误: {}", e))?;

    let region = build_region(endpoint, region_str);
    let bucket = Bucket::new(bucket_name, region, credentials)
        .map_err(|e| format!("S3 Bucket 创建失败: {}", e))?;

    if path_style {
        Ok(bucket.with_path_style())
    } else {
        Ok(bucket)
    }
}

fn get_public_base_url(cfg: &config::AppSettingsMap) -> Option<String> {
    cfg.get("S3_PUBLIC_BASE_URL")
        .and_then(|v| v.as_deref())
        .filter(|v| !v.is_empty())
        .map(|s| s.to_string())
}

pub async fn upload_bytes(
    state: &Arc<AppState>,
    filename: &str,
    data: Vec<u8>,
    size: i64,
) -> Result<String, String> {
    let cfg = app_cfg(state);

    let endpoint = cfg
        .get("S3_ENDPOINT")
        .and_then(|v| v.as_deref())
        .unwrap_or("");
    let bucket_name = cfg
        .get("S3_BUCKET")
        .and_then(|v| v.as_deref())
        .filter(|v| !v.is_empty())
        .ok_or_else(|| "S3_BUCKET 未配置".to_string())?;
    let access_key = cfg
        .get("S3_ACCESS_KEY")
        .and_then(|v| v.as_deref())
        .filter(|v| !v.is_empty())
        .ok_or_else(|| "S3_ACCESS_KEY 未配置".to_string())?;
    let secret_key = cfg
        .get("S3_SECRET_KEY")
        .and_then(|v| v.as_deref())
        .filter(|v| !v.is_empty())
        .ok_or_else(|| "S3_SECRET_KEY 未配置".to_string())?;
    let region_str = cfg
        .get("S3_REGION")
        .and_then(|v| v.as_deref())
        .unwrap_or("auto");
    let path_style = parse_bool_setting(cfg.get("S3_PATH_STYLE").and_then(|v| v.as_deref()));

    let bucket = build_bucket(
        bucket_name,
        endpoint,
        region_str,
        access_key,
        secret_key,
        path_style,
    )?;

    let object_key = generate_object_key(filename);
    let file_id = object_key
        .rsplit('/')
        .next()
        .unwrap_or(&object_key)
        .to_string();
    let content_type = mime_guess::from_path(filename)
        .first_or_octet_stream()
        .to_string();

    bucket
        .put_object_with_content_type(&object_key, &data, &content_type)
        .await
        .map_err(|e| format!("S3 upload failed: {}", e))?;

    database::add_file_metadata_with_storage(
        &state.db_pool,
        filename,
        &file_id,
        size,
        constants::STORAGE_BACKEND_S3,
        Some(&object_key),
    )
    .map_err(|e| e.to_string())?;

    tracing::info!("S3 upload success: {}", object_key);
    Ok(file_id)
}

pub async fn get_download_url(
    state: &AppState,
    meta: &database::FileMetadata,
) -> Result<String, AppErrorKind> {
    let cfg = app_cfg_ref(state);
    let key = meta
        .storage_path
        .as_deref()
        .filter(|v| !v.is_empty())
        .ok_or_else(|| AppErrorKind::S3("storage_path 缺失".into()))?;

    if let Some(base) = get_public_base_url(&cfg) {
        return Ok(format!("{}/{}", base.trim_end_matches('/'), key));
    }

    let endpoint = cfg
        .get("S3_ENDPOINT")
        .and_then(|v| v.as_deref())
        .unwrap_or("");
    let bucket = cfg
        .get("S3_BUCKET")
        .and_then(|v| v.as_deref())
        .filter(|v| !v.is_empty())
        .ok_or_else(|| AppErrorKind::S3("S3_BUCKET 未配置".into()))?;
    let path_style = parse_bool_setting(cfg.get("S3_PATH_STYLE").and_then(|v| v.as_deref()));

    if endpoint.is_empty() {
        return Err(AppErrorKind::S3(
            "S3_ENDPOINT 未配置，且未提供 S3_PUBLIC_BASE_URL".into(),
        ));
    }

    let url = if path_style || !endpoint.contains("amazonaws.com") {
        format!("{}/{}/{}", endpoint.trim_end_matches('/'), bucket, key)
    } else {
        let host = endpoint
            .trim_start_matches("https://")
            .trim_start_matches("http://")
            .trim_start_matches("s3.");
        format!("https://{}.s3.{}/{}", bucket, host, key)
    };

    Ok(url)
}

pub async fn delete_object(
    state: &Arc<AppState>,
    meta: &database::FileMetadata,
) -> Result<(), AppErrorKind> {
    let cfg = app_cfg(state);

    let endpoint = cfg
        .get("S3_ENDPOINT")
        .and_then(|v| v.as_deref())
        .unwrap_or("");
    let bucket_name = cfg
        .get("S3_BUCKET")
        .and_then(|v| v.as_deref())
        .filter(|v| !v.is_empty())
        .ok_or_else(|| AppErrorKind::S3("S3_BUCKET 未配置".into()))?;
    let access_key = cfg
        .get("S3_ACCESS_KEY")
        .and_then(|v| v.as_deref())
        .filter(|v| !v.is_empty())
        .ok_or_else(|| AppErrorKind::S3("S3_ACCESS_KEY 未配置".into()))?;
    let secret_key = cfg
        .get("S3_SECRET_KEY")
        .and_then(|v| v.as_deref())
        .filter(|v| !v.is_empty())
        .ok_or_else(|| AppErrorKind::S3("S3_SECRET_KEY 未配置".into()))?;
    let region_str = cfg
        .get("S3_REGION")
        .and_then(|v| v.as_deref())
        .unwrap_or("auto");
    let path_style = parse_bool_setting(cfg.get("S3_PATH_STYLE").and_then(|v| v.as_deref()));

    let key = meta
        .storage_path
        .as_deref()
        .filter(|v| !v.is_empty())
        .ok_or_else(|| AppErrorKind::S3("storage_path 缺失".into()))?;

    let bucket = build_bucket(
        bucket_name,
        endpoint,
        region_str,
        access_key,
        secret_key,
        path_style,
    )
    .map_err(AppErrorKind::S3)?;

    bucket
        .delete_object(key)
        .await
        .map_err(|e| AppErrorKind::S3(format!("S3 delete failed: {}", e)))?;

    tracing::info!("S3 delete success: {}", key);
    Ok(())
}

pub async fn healthcheck(state: &Arc<AppState>) -> Result<bool, String> {
    let cfg = app_cfg(state);

    let endpoint = cfg
        .get("S3_ENDPOINT")
        .and_then(|v| v.as_deref())
        .unwrap_or("");
    let bucket_name = cfg
        .get("S3_BUCKET")
        .and_then(|v| v.as_deref())
        .filter(|v| !v.is_empty())
        .ok_or_else(|| "S3_BUCKET 未配置".to_string())?;
    let access_key = cfg
        .get("S3_ACCESS_KEY")
        .and_then(|v| v.as_deref())
        .filter(|v| !v.is_empty())
        .ok_or_else(|| "S3_ACCESS_KEY 未配置".to_string())?;
    let secret_key = cfg
        .get("S3_SECRET_KEY")
        .and_then(|v| v.as_deref())
        .filter(|v| !v.is_empty())
        .ok_or_else(|| "S3_SECRET_KEY 未配置".to_string())?;
    let region_str = cfg
        .get("S3_REGION")
        .and_then(|v| v.as_deref())
        .unwrap_or("auto");
    let path_style = parse_bool_setting(cfg.get("S3_PATH_STYLE").and_then(|v| v.as_deref()));

    let bucket = build_bucket(
        bucket_name,
        endpoint,
        region_str,
        access_key,
        secret_key,
        path_style,
    )?;

    bucket
        .list("".to_string(), Some("/".to_string()))
        .await
        .map(|_| true)
        .map_err(|e| format!("S3 healthcheck failed: {}", e))
}
