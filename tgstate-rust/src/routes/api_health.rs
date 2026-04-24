use std::sync::Arc;

use axum::extract::State;
use axum::routing::get;
use axum::{Json, Router};

use crate::config;
use crate::state::AppState;

async fn health(State(state): State<Arc<AppState>>) -> Json<serde_json::Value> {
    let app_settings = config::get_app_settings(&state.settings, &state.db_pool);
    let bot = state.bot_state.lock().await;

    Json(serde_json::json!({
        "status": "ok",
        "service": "tgstate",
        "storage_backend": app_settings
            .get("STORAGE_BACKEND")
            .and_then(|v| v.as_deref())
            .unwrap_or("telegram"),
        "bot": {
            "ready": bot.bot_ready,
            "running": bot.bot_running,
        }
    }))
}

pub fn router() -> Router<Arc<AppState>> {
    Router::new().route("/api/health", get(health))
}
