pub mod api_albums;
pub mod api_auth;
pub mod api_files;
pub mod api_health;
pub mod api_settings;
pub mod api_sse;
pub mod api_upload;
pub mod api_webdav;
pub mod pages;

use std::sync::Arc;

use axum::Router;

use crate::state::AppState;

pub fn build_router(state: Arc<AppState>) -> Router {
    Router::new()
        .merge(pages::router())
        .merge(api_auth::router())
        .merge(api_files::router())
        .merge(api_health::router())
        .merge(api_upload::router())
        .merge(api_settings::router())
        .merge(api_sse::router())
        .merge(api_webdav::router())
        .merge(api_albums::router())
        .with_state(state)
}
