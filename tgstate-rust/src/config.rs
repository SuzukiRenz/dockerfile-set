use std::collections::HashMap;

use crate::database::{self, DbPool};

pub type AppSettingsMap = HashMap<String, Option<String>>;

#[derive(Debug, Clone)]
pub struct Settings {
    pub bot_token: Option<String>,
    pub channel_name: Option<String>,
    pub pass_word: Option<String>,
    pub picgo_api_key: Option<String>,
    pub base_url: String,
    pub _mode: String,
    pub _file_route: String,
    pub data_dir: String,
}

impl Settings {
    pub fn from_env() -> Self {
        Self {
            bot_token: std::env::var("BOT_TOKEN").ok().filter(|s| !s.is_empty()),
            channel_name: std::env::var("CHANNEL_NAME").ok().filter(|s| !s.is_empty()),
            pass_word: std::env::var("PASS_WORD").ok().filter(|s| !s.is_empty()),
            picgo_api_key: std::env::var("PICGO_API_KEY").ok().filter(|s| !s.is_empty()),
            base_url: std::env::var("BASE_URL")
                .unwrap_or_else(|_| "http://127.0.0.1:8000".into()),
            _mode: std::env::var("MODE").unwrap_or_else(|_| "p".into()),
            _file_route: std::env::var("FILE_ROUTE").unwrap_or_else(|_| "/d/".into()),
            data_dir: std::env::var("DATA_DIR").unwrap_or_else(|_| "app/data".into()),
        }
    }
}

/// Get active password: DB first, then env
pub fn get_active_password(settings: &Settings, pool: &DbPool) -> Option<String> {
    if let Ok(db_settings) = database::get_app_settings_from_db(pool) {
        if let Some(Some(pw)) = db_settings.get("PASS_WORD") {
            let pw = pw.trim().to_string();
            if !pw.is_empty() {
                return Some(pw);
            }
        }
    }
    settings.pass_word.clone()
}

/// Merge DB settings over env settings
pub fn get_app_settings(settings: &Settings, pool: &DbPool) -> AppSettingsMap {
    let mut result = HashMap::new();
    result.insert("BOT_TOKEN".into(), settings.bot_token.clone());
    result.insert("CHANNEL_NAME".into(), settings.channel_name.clone());
    result.insert("PASS_WORD".into(), settings.pass_word.clone());
    result.insert("PICGO_API_KEY".into(), settings.picgo_api_key.clone());
    result.insert("BASE_URL".into(), Some(settings.base_url.clone()));

    // storage backend defaults
    result.insert("STORAGE_BACKEND".into(), Some("telegram".into()));
    result.insert("S3_ENDPOINT".into(), None);
    result.insert("S3_REGION".into(), Some("auto".into()));
    result.insert("S3_BUCKET".into(), None);
    result.insert("S3_ACCESS_KEY".into(), None);
    result.insert("S3_SECRET_KEY".into(), None);
    result.insert("S3_PUBLIC_BASE_URL".into(), None);
    result.insert("WEBDAV_ENABLED".into(), Some("0".into()));
    result.insert("WEBDAV_READONLY".into(), Some("0".into()));

    if let Ok(db_settings) = database::get_app_settings_from_db(pool) {
        for (key, val) in db_settings {
            if let Some(v) = &val {
                let v = v.trim().to_string();
                if !v.is_empty() {
                    result.insert(key, Some(v));
                } else if result.contains_key(&key) {
                    result.insert(key, None);
                }
            } else if result.contains_key(&key) {
                result.insert(key, None);
            }
        }
    }

    result
}

pub fn is_bot_ready(app_settings: &AppSettingsMap) -> bool {
    let token = app_settings
        .get("BOT_TOKEN")
        .and_then(|v| v.as_deref())
        .unwrap_or("")
        .trim();
    let channel = app_settings
        .get("CHANNEL_NAME")
        .and_then(|v| v.as_deref())
        .unwrap_or("")
        .trim();
    !token.is_empty() && !channel.is_empty()
}
