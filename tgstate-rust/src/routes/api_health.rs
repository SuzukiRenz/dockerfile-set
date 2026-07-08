use std::sync::Arc;

use axum::extract::State;
use axum::routing::get;
use axum::{Json, Router};

use crate::state::AppState;

async fn health(State(state): State<Arc<AppState>>) -> Json<serde_json::Value> {
    let bot = state.bot_state.lock().await;

    Json(serde_json::json!({
        "status": "ok",
        "service": "tgstate",
        "storage_backend": "telegram",
        "bot": {
            "ready": bot.bot_ready,
            "running": bot.bot_running,
        }
    }))
}

pub fn router() -> Router<Arc<AppState>> {
    Router::new().route("/api/health", get(health))
}
