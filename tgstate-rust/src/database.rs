use r2d2::Pool;
use r2d2_sqlite::SqliteConnectionManager;
use rand::Rng;
use rusqlite::params;
use std::collections::HashMap;
use std::path::Path;
use tracing;

use crate::constants;
use crate::error::AppErrorKind;

pub type DbPool = Pool<SqliteConnectionManager>;

pub fn db_path(data_dir: &str) -> String {
    Path::new(data_dir)
        .join("file_metadata.db")
        .to_string_lossy()
        .to_string()
}

pub fn init_db(data_dir: &str) -> DbPool {
    if let Err(err) = std::fs::create_dir_all(data_dir) {
        panic!("Failed to create data directory '{}': {}", data_dir, err);
    }

    let path = db_path(data_dir);
    if let Some(parent) = Path::new(&path).parent() {
        if let Err(err) = std::fs::create_dir_all(parent) {
            panic!(
                "Failed to create database parent directory '{}': {}",
                parent.display(),
                err
            );
        }
    }

    let manager = SqliteConnectionManager::file(&path);
    let pool = Pool::builder()
        .max_size(10)
        .min_idle(Some(2))
        .connection_customizer(Box::new(SqliteInitializer))
        .build(manager)
        .unwrap_or_else(|err| panic!("Failed to create database pool at '{}': {}", path, err));

    let conn = pool
        .get()
        .unwrap_or_else(|err| panic!("Failed to get connection for init at '{}': {}", path, err));

    conn.execute_batch(
        "CREATE TABLE IF NOT EXISTS files (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            filename TEXT NOT NULL,
            file_id TEXT NOT NULL UNIQUE,
            filesize INTEGER NOT NULL,
            upload_date TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
            short_id TEXT UNIQUE,
            storage_backend TEXT NOT NULL DEFAULT 'telegram',
            storage_path TEXT,
            folder_path TEXT NOT NULL DEFAULT '',
            link_visibility TEXT NOT NULL DEFAULT 'public',
            expires_at TEXT
        );",
    )
    .expect("Failed to create files table");

    conn.execute_batch(
        "CREATE TABLE IF NOT EXISTS folder_settings (
            folder_path TEXT PRIMARY KEY,
            link_visibility TEXT NOT NULL DEFAULT 'public',
            updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
        );",
    )
    .expect("Failed to create folder_settings table");

    ensure_column(&conn, "files", "short_id", "ALTER TABLE files ADD COLUMN short_id TEXT");
    ensure_column(
        &conn,
        "files",
        "storage_backend",
        "ALTER TABLE files ADD COLUMN storage_backend TEXT NOT NULL DEFAULT 'telegram'",
    );
    ensure_column(&conn, "files", "storage_path", "ALTER TABLE files ADD COLUMN storage_path TEXT");
    ensure_column(&conn, "files", "folder_path", "ALTER TABLE files ADD COLUMN folder_path TEXT NOT NULL DEFAULT ''");
    ensure_column(&conn, "files", "link_visibility", "ALTER TABLE files ADD COLUMN link_visibility TEXT NOT NULL DEFAULT 'public'");
    ensure_column(&conn, "files", "expires_at", "ALTER TABLE files ADD COLUMN expires_at TEXT");
    ensure_column(&conn, "folder_settings", "link_visibility", "ALTER TABLE folder_settings ADD COLUMN link_visibility TEXT NOT NULL DEFAULT 'public'");
    ensure_column(&conn, "folder_settings", "updated_at", "ALTER TABLE folder_settings ADD COLUMN updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP");

    conn.execute_batch(
        "CREATE UNIQUE INDEX IF NOT EXISTS idx_files_short_id ON files(short_id);
         CREATE INDEX IF NOT EXISTS idx_files_upload_date ON files(upload_date DESC);
         CREATE INDEX IF NOT EXISTS idx_files_storage_backend ON files(storage_backend);
         CREATE INDEX IF NOT EXISTS idx_files_folder_path ON files(folder_path);
         CREATE UNIQUE INDEX IF NOT EXISTS idx_folder_settings_path ON folder_settings(folder_path);",
    )
    .ok();

    conn.execute_batch(
        "CREATE TABLE IF NOT EXISTS app_settings (
            id INTEGER PRIMARY KEY CHECK (id = 1),
            bot_token TEXT,
            channel_name TEXT,
            pass_word TEXT,
            picgo_api_key TEXT,
            base_url TEXT,
            webdav_username TEXT
        );",
    )
    .expect("Failed to create app_settings table");

    conn.execute("INSERT OR IGNORE INTO app_settings (id) VALUES (1)", [])
        .expect("Failed to init app_settings row");

    ensure_column(
        &conn,
        "app_settings",
        "session_token",
        "ALTER TABLE app_settings ADD COLUMN session_token TEXT",
    );
    ensure_column(
        &conn,
        "app_settings",
        "storage_backend",
        "ALTER TABLE app_settings ADD COLUMN storage_backend TEXT",
    );
    ensure_column(
        &conn,
        "app_settings",
        "s3_endpoint",
        "ALTER TABLE app_settings ADD COLUMN s3_endpoint TEXT",
    );
    ensure_column(
        &conn,
        "app_settings",
        "s3_region",
        "ALTER TABLE app_settings ADD COLUMN s3_region TEXT",
    );
    ensure_column(
        &conn,
        "app_settings",
        "s3_bucket",
        "ALTER TABLE app_settings ADD COLUMN s3_bucket TEXT",
    );
    ensure_column(
        &conn,
        "app_settings",
        "s3_access_key",
        "ALTER TABLE app_settings ADD COLUMN s3_access_key TEXT",
    );
    ensure_column(
        &conn,
        "app_settings",
        "s3_secret_key",
        "ALTER TABLE app_settings ADD COLUMN s3_secret_key TEXT",
    );
    ensure_column(
        &conn,
        "app_settings",
        "s3_public_base_url",
        "ALTER TABLE app_settings ADD COLUMN s3_public_base_url TEXT",
    );
    ensure_column(
        &conn,
        "app_settings",
        "webdav_enabled",
        "ALTER TABLE app_settings ADD COLUMN webdav_enabled TEXT",
    );
    ensure_column(
        &conn,
        "app_settings",
        "webdav_readonly",
        "ALTER TABLE app_settings ADD COLUMN webdav_readonly TEXT",
    );
    ensure_column(
        &conn,
        "app_settings",
        "webdav_username",
        "ALTER TABLE app_settings ADD COLUMN webdav_username TEXT",
    );

    conn.execute_batch(
        "CREATE TABLE IF NOT EXISTS albums (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            album_id TEXT NOT NULL UNIQUE,
            title TEXT NOT NULL,
            description TEXT,
            cover_file_id TEXT,
            is_public INTEGER NOT NULL DEFAULT 1,
            access_key TEXT,
            created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
            updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
        );
        CREATE TABLE IF NOT EXISTS album_items (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            album_id TEXT NOT NULL,
            file_id TEXT NOT NULL,
            sort_order INTEGER NOT NULL DEFAULT 0,
            created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
            UNIQUE(album_id, file_id)
        );
        CREATE INDEX IF NOT EXISTS idx_album_items_album_id ON album_items(album_id, sort_order);",
    )
    .expect("Failed to create album tables");

    tracing::info!("数据库已成功初始化");
    pool
}

fn ensure_column(conn: &rusqlite::Connection, table: &str, column: &str, ddl: &str) {
    let has_column: bool = conn
        .prepare(&format!("PRAGMA table_info({})", table))
        .unwrap()
        .query_map([], |row| row.get::<_, String>(1))
        .unwrap()
        .any(|col| col.map_or(false, |c| c == column));

    if !has_column {
        tracing::info!("Migrating database: adding {}.{}...", table, column);
        if let Err(e) = conn.execute(ddl, []) {
            tracing::error!("Migration warning: Failed to add {}.{}: {}", table, column, e);
        }
    }
}

#[derive(Debug)]
struct SqliteInitializer;

impl r2d2::CustomizeConnection<rusqlite::Connection, rusqlite::Error> for SqliteInitializer {
    fn on_acquire(&self, conn: &mut rusqlite::Connection) -> Result<(), rusqlite::Error> {
        conn.execute_batch("PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;")?;
        Ok(())
    }
}

fn generate_short_id(length: usize) -> String {
    const CHARS: &[u8] = b"abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789";
    let mut rng = rand::thread_rng();
    (0..length)
        .map(|_| CHARS[rng.gen_range(0..CHARS.len())] as char)
        .collect()
}

pub fn generate_public_id(length: usize) -> String {
    generate_short_id(length)
}

pub fn add_file_metadata(
    pool: &DbPool,
    filename: &str,
    file_id: &str,
    filesize: i64,
) -> Result<String, AppErrorKind> {
    add_file_metadata_in_folder(pool, filename, file_id, filesize, "")
}

pub fn add_file_metadata_in_folder(
    pool: &DbPool,
    filename: &str,
    file_id: &str,
    filesize: i64,
    folder_path: &str,
) -> Result<String, AppErrorKind> {
    add_file_metadata_with_storage_in_folder(
        pool,
        filename,
        file_id,
        filesize,
        constants::STORAGE_BACKEND_TELEGRAM,
        None,
        folder_path,
    )
}

pub fn add_file_metadata_with_storage(
    pool: &DbPool,
    filename: &str,
    file_id: &str,
    filesize: i64,
    storage_backend: &str,
    storage_path: Option<&str>,
) -> Result<String, AppErrorKind> {
    add_file_metadata_with_storage_in_folder(
        pool,
        filename,
        file_id,
        filesize,
        storage_backend,
        storage_path,
        "",
    )
}

pub fn add_file_metadata_with_storage_in_folder(
    pool: &DbPool,
    filename: &str,
    file_id: &str,
    filesize: i64,
    storage_backend: &str,
    storage_path: Option<&str>,
    folder_path: &str,
) -> Result<String, AppErrorKind> {
    let conn = pool.get()?;
    let normalized_folder_path = normalize_folder_path(folder_path);

    for _ in 0..5 {
        let short_id = generate_short_id(constants::SHORT_ID_LENGTH);
        match conn.execute(
            "INSERT INTO files (filename, file_id, filesize, short_id, storage_backend, storage_path, folder_path) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7)",
            params![filename, file_id, filesize, short_id, storage_backend, storage_path, normalized_folder_path],
        ) {
            Ok(_) => {
                let short_id_preview = short_id.chars().take(2).collect::<String>();
                tracing::info!(
                    "已添加文件元数据: {}, backend: {}, short_id: {}***",
                    filename,
                    storage_backend,
                    short_id_preview
                );
                return Ok(short_id);
            }
            Err(rusqlite::Error::SqliteFailure(err, _))
                if err.code == rusqlite::ErrorCode::ConstraintViolation =>
            {
                let existing: Option<String> = conn
                    .query_row(
                        "SELECT short_id FROM files WHERE file_id = ?1",
                        params![file_id],
                        |row| row.get(0),
                    )
                    .ok();

                if let Some(existing_sid) = existing {
                    if !existing_sid.is_empty() {
                        return Ok(existing_sid);
                    }
                    let new_sid = generate_short_id(constants::SHORT_ID_LENGTH);
                    conn.execute(
                        "UPDATE files SET short_id = ?1 WHERE file_id = ?2",
                        params![new_sid, file_id],
                    )?;
                    return Ok(new_sid);
                }
                continue;
            }
            Err(e) => return Err(e.into()),
        }
    }
    Err(AppErrorKind::Other("Failed to generate unique short_id".into()))
}

#[derive(Debug, Clone, serde::Serialize)]
pub struct FileMetadata {
    pub filename: String,
    pub file_id: String,
    pub filesize: i64,
    pub upload_date: String,
    pub short_id: Option<String>,
    pub storage_backend: String,
    pub storage_path: Option<String>,
    pub folder_path: String,
    pub link_visibility: String,
    pub expires_at: Option<String>,
}

#[derive(Debug, Clone, serde::Serialize)]
pub struct FolderSetting {
    pub folder_path: String,
    pub link_visibility: String,
    pub updated_at: String,
}

#[derive(Debug, Clone, serde::Serialize)]
pub struct Album {
    pub album_id: String,
    pub title: String,
    pub description: Option<String>,
    pub cover_file_id: Option<String>,
    pub is_public: bool,
    pub access_key: Option<String>,
    pub created_at: String,
    pub updated_at: String,
}

#[derive(Debug, Clone, serde::Serialize)]
pub struct AlbumItem {
    pub album_id: String,
    pub file_id: String,
    pub sort_order: i64,
}

pub fn resolve_file_id(pool: &DbPool, identifier: &str) -> Result<Option<String>, AppErrorKind> {
    let conn = pool.get()?;
    let result = conn.query_row(
        "SELECT file_id FROM files WHERE short_id = ?1 OR file_id = ?1",
        params![identifier],
        |row| row.get(0),
    );

    match result {
        Ok(file_id) => Ok(Some(file_id)),
        Err(rusqlite::Error::QueryReturnedNoRows) => Ok(None),
        Err(e) => Err(e.into()),
    }
}

pub fn resolve_file_ids(
    pool: &DbPool,
    identifiers: &[String],
) -> Result<(Vec<String>, Vec<String>), AppErrorKind> {
    let mut resolved = Vec::with_capacity(identifiers.len());
    let mut missing = Vec::new();

    for identifier in identifiers {
        match resolve_file_id(pool, identifier)? {
            Some(file_id) => resolved.push(file_id),
            None => missing.push(identifier.clone()),
        }
    }

    Ok((resolved, missing))
}

pub fn get_all_files(pool: &DbPool) -> Result<Vec<FileMetadata>, AppErrorKind> {
    let conn = pool.get()?;
    let mut stmt = conn.prepare(
        "SELECT filename, file_id, filesize, upload_date, short_id, storage_backend, storage_path, folder_path, link_visibility, expires_at FROM files ORDER BY folder_path ASC, upload_date DESC",
    )?;

    let files = stmt
        .query_map([], |row| {
            Ok(FileMetadata {
                filename: row.get(0)?,
                file_id: row.get(1)?,
                filesize: row.get(2)?,
                upload_date: row.get::<_, String>(3).unwrap_or_default(),
                short_id: row.get(4).ok(),
                storage_backend: row.get::<_, String>(5).unwrap_or_else(|_| constants::STORAGE_BACKEND_TELEGRAM.to_string()),
                storage_path: row.get(6).ok(),
                folder_path: row.get::<_, String>(7).unwrap_or_default(),
                link_visibility: row.get::<_, String>(8).unwrap_or_else(|_| "public".to_string()),
                expires_at: row.get(9).ok(),
            })
        })?
        .filter_map(|r| r.ok())
        .collect();

    Ok(files)
}

pub fn get_file_by_id(pool: &DbPool, identifier: &str) -> Result<Option<FileMetadata>, AppErrorKind> {
    let conn = pool.get()?;
    let result = conn.query_row(
        "SELECT filename, filesize, upload_date, file_id, short_id, storage_backend, storage_path, folder_path, link_visibility, expires_at FROM files WHERE short_id = ?1 OR file_id = ?1",
        params![identifier],
        |row| {
            Ok(FileMetadata {
                filename: row.get(0)?,
                filesize: row.get(1)?,
                upload_date: row.get::<_, String>(2).unwrap_or_default(),
                file_id: row.get(3)?,
                short_id: row.get(4).ok(),
                storage_backend: row.get::<_, String>(5).unwrap_or_else(|_| constants::STORAGE_BACKEND_TELEGRAM.to_string()),
                storage_path: row.get(6).ok(),
                folder_path: row.get::<_, String>(7).unwrap_or_default(),
                link_visibility: row.get::<_, String>(8).unwrap_or_else(|_| "public".to_string()),
                expires_at: row.get(9).ok(),
            })
        },
    );

    match result {
        Ok(meta) => Ok(Some(meta)),
        Err(rusqlite::Error::QueryReturnedNoRows) => Ok(None),
        Err(e) => Err(e.into()),
    }
}

pub fn normalize_folder_path(folder_path: &str) -> String {
    folder_path
        .replace('\\', "/")
        .split('/')
        .filter(|segment| !segment.trim().is_empty() && *segment != "." && *segment != "..")
        .map(|segment| segment.trim())
        .collect::<Vec<_>>()
        .join("/")
}

pub fn update_file_folder(
    pool: &DbPool,
    identifier: &str,
    folder_path: &str,
) -> Result<bool, AppErrorKind> {
    let conn = pool.get()?;
    let normalized = normalize_folder_path(folder_path);
    let rows = conn.execute(
        "UPDATE files SET folder_path = ?1 WHERE short_id = ?2 OR file_id = ?2",
        params![normalized, identifier],
    )?;
    Ok(rows > 0)
}

pub fn update_file_link_settings(
    pool: &DbPool,
    identifier: &str,
    link_visibility: &str,
    _expires_at: Option<&str>,
) -> Result<bool, AppErrorKind> {
    let conn = pool.get()?;
    let visibility = match link_visibility.trim() {
        "private" => "private",
        _ => "public",
    };
    let rows = conn.execute(
        "UPDATE files SET link_visibility = ?1, expires_at = NULL WHERE short_id = ?2 OR file_id = ?2",
        params![visibility, identifier],
    )?;
    Ok(rows > 0)
}

pub fn set_folder_visibility(
    pool: &DbPool,
    folder_path: &str,
    link_visibility: &str,
    apply_to_children: bool,
) -> Result<usize, AppErrorKind> {
    let conn = pool.get()?;
    let normalized = normalize_folder_path(folder_path);
    let visibility = match link_visibility.trim() {
        "private" => "private",
        _ => "public",
    };

    if normalized.is_empty() {
        return Ok(0);
    }

    conn.execute(
        "INSERT INTO folder_settings (folder_path, link_visibility, updated_at)
         VALUES (?1, ?2, CURRENT_TIMESTAMP)
         ON CONFLICT(folder_path) DO UPDATE SET
         link_visibility = excluded.link_visibility,
         updated_at = CURRENT_TIMESTAMP",
        params![normalized, visibility],
    )?;

    let rows = if apply_to_children {
        let prefix = format!("{}/%", normalized);
        conn.execute(
            "UPDATE files
             SET link_visibility = ?1, expires_at = NULL
             WHERE folder_path = ?2 OR folder_path LIKE ?3",
            params![visibility, normalized, prefix],
        )?
    } else {
        conn.execute(
            "UPDATE files
             SET link_visibility = ?1, expires_at = NULL
             WHERE folder_path = ?2",
            params![visibility, normalized],
        )?
    };

    Ok(rows)
}

pub fn list_folder_settings(pool: &DbPool) -> Result<Vec<FolderSetting>, AppErrorKind> {
    let conn = pool.get()?;
    let mut stmt = conn.prepare(
        "SELECT folder_path, link_visibility, COALESCE(updated_at, '')
         FROM folder_settings
         ORDER BY folder_path ASC",
    )?;

    let rows = stmt
        .query_map([], |row| {
            Ok(FolderSetting {
                folder_path: row.get(0)?,
                link_visibility: row.get::<_, String>(1).unwrap_or_else(|_| "public".to_string()),
                updated_at: row.get::<_, String>(2).unwrap_or_default(),
            })
        })?
        .collect::<Result<Vec<_>, _>>()?;

    Ok(rows)
}

pub fn get_folder_visibility(pool: &DbPool, folder_path: &str) -> Result<String, AppErrorKind> {
    let normalized = normalize_folder_path(folder_path);
    if normalized.is_empty() {
        return Ok("public".to_string());
    }

    let conn = pool.get()?;
    let exact: Option<String> = conn
        .query_row(
            "SELECT link_visibility FROM folder_settings WHERE folder_path = ?1",
            params![normalized],
            |row| row.get(0),
        )
        .ok();

    if let Some(value) = exact {
        return Ok(if value == "private" { "private".to_string() } else { "public".to_string() });
    }

    let mut current = normalized.as_str();
    while let Some((parent, _)) = current.rsplit_once('/') {
        let inherited: Option<String> = conn
            .query_row(
                "SELECT link_visibility FROM folder_settings WHERE folder_path = ?1",
                params![parent],
                |row| row.get(0),
            )
            .ok();
        if let Some(value) = inherited {
            return Ok(if value == "private" { "private".to_string() } else { "public".to_string() });
        }
        current = parent;
    }

    Ok("public".to_string())
}

pub fn get_file_by_webdav_path(
    pool: &DbPool,
    webdav_path: &str,
) -> Result<Option<FileMetadata>, AppErrorKind> {
    let normalized = normalize_folder_path(webdav_path);
    let all = get_all_files(pool)?;
    Ok(all.into_iter().find(|f| {
        let full_path = if f.folder_path.is_empty() {
            f.filename.clone()
        } else {
            format!("{}/{}", f.folder_path, f.filename)
        };
        full_path == normalized
    }))
}

pub fn list_folder_paths(pool: &DbPool) -> Result<Vec<String>, AppErrorKind> {
    let mut folders = get_all_files(pool)?
        .into_iter()
        .filter_map(|f| {
            let normalized = normalize_folder_path(&f.folder_path);
            if normalized.is_empty() {
                None
            } else {
                Some(normalized)
            }
        })
        .collect::<Vec<_>>();
    folders.sort();
    folders.dedup();
    Ok(folders)
}

pub fn delete_file_metadata(pool: &DbPool, file_id: &str) -> Result<bool, AppErrorKind> {
    let conn = pool.get()?;
    let rows = conn.execute("DELETE FROM files WHERE file_id = ?1", params![file_id])?;
    Ok(rows > 0)
}

pub fn delete_file_by_message_id(pool: &DbPool, message_id: i64) -> Result<Option<String>, AppErrorKind> {
    let conn = pool.get()?;
    let pattern = format!("{}:%", message_id);

    let file_id: Option<String> = conn
        .query_row(
            "SELECT file_id FROM files WHERE file_id LIKE ?1",
            params![pattern],
            |row| row.get(0),
        )
        .ok();

    if let Some(ref fid) = file_id {
        conn.execute("DELETE FROM files WHERE file_id = ?1", params![fid])?;
        tracing::info!("已从数据库中删除与消息ID {} 关联的文件: {}", message_id, fid);
    }

    Ok(file_id)
}

pub fn create_album(
    pool: &DbPool,
    title: &str,
    description: Option<&str>,
    cover_file_id: Option<&str>,
    is_public: bool,
    access_key: Option<&str>,
) -> Result<String, AppErrorKind> {
    let conn = pool.get()?;
    let album_id = generate_public_id(12);
    let resolved_cover_file_id = match cover_file_id {
        Some(identifier) if !identifier.trim().is_empty() => resolve_file_id(pool, identifier.trim())?,
        _ => None,
    };
    conn.execute(
        "INSERT INTO albums (album_id, title, description, cover_file_id, is_public, access_key) VALUES (?1, ?2, ?3, ?4, ?5, ?6)",
        params![album_id, title, description, resolved_cover_file_id, if is_public { 1 } else { 0 }, access_key],
    )?;
    Ok(album_id)
}

pub fn list_albums(pool: &DbPool) -> Result<Vec<Album>, AppErrorKind> {
    let conn = pool.get()?;
    let mut stmt = conn.prepare(
        "SELECT album_id, title, description, cover_file_id, is_public, access_key, created_at, updated_at FROM albums ORDER BY updated_at DESC, created_at DESC",
    )?;
    let rows = stmt
        .query_map([], |row| {
            Ok(Album {
                album_id: row.get(0)?,
                title: row.get(1)?,
                description: row.get(2).ok(),
                cover_file_id: row.get(3).ok(),
                is_public: row.get::<_, i64>(4).unwrap_or(1) != 0,
                access_key: row.get(5).ok(),
                created_at: row.get::<_, String>(6).unwrap_or_default(),
                updated_at: row.get::<_, String>(7).unwrap_or_default(),
            })
        })?
        .filter_map(|r| r.ok())
        .collect();
    Ok(rows)
}

pub fn get_album(pool: &DbPool, album_id: &str) -> Result<Option<Album>, AppErrorKind> {
    let conn = pool.get()?;
    let result = conn.query_row(
        "SELECT album_id, title, description, cover_file_id, is_public, access_key, created_at, updated_at FROM albums WHERE album_id = ?1",
        params![album_id],
        |row| {
            Ok(Album {
                album_id: row.get(0)?,
                title: row.get(1)?,
                description: row.get(2).ok(),
                cover_file_id: row.get(3).ok(),
                is_public: row.get::<_, i64>(4).unwrap_or(1) != 0,
                access_key: row.get(5).ok(),
                created_at: row.get::<_, String>(6).unwrap_or_default(),
                updated_at: row.get::<_, String>(7).unwrap_or_default(),
            })
        },
    );
    match result {
        Ok(v) => Ok(Some(v)),
        Err(rusqlite::Error::QueryReturnedNoRows) => Ok(None),
        Err(e) => Err(e.into()),
    }
}

pub fn add_album_items(pool: &DbPool, album_id: &str, file_ids: &[String]) -> Result<(), AppErrorKind> {
    let (resolved_file_ids, missing) = resolve_file_ids(pool, file_ids)?;
    if !missing.is_empty() {
        return Err(AppErrorKind::Other(format!(
            "以下文件不存在: {}",
            missing.join(", ")
        )));
    }

    let conn = pool.get()?;
    let tx = conn.unchecked_transaction()?;
    let mut max_sort: i64 = tx
        .query_row(
            "SELECT COALESCE(MAX(sort_order), -1) FROM album_items WHERE album_id = ?1",
            params![album_id],
            |row| row.get(0),
        )
        .unwrap_or(-1);

    for fid in &resolved_file_ids {
        max_sort += 1;
        tx.execute(
            "INSERT OR IGNORE INTO album_items (album_id, file_id, sort_order) VALUES (?1, ?2, ?3)",
            params![album_id, fid, max_sort],
        )?;
    }
    tx.execute(
        "UPDATE albums SET updated_at = CURRENT_TIMESTAMP WHERE album_id = ?1",
        params![album_id],
    )?;
    tx.commit()?;
    Ok(())
}

pub fn list_album_files(pool: &DbPool, album_id: &str) -> Result<Vec<FileMetadata>, AppErrorKind> {
    let conn = pool.get()?;
    let mut stmt = conn.prepare(
        "SELECT f.filename, f.file_id, f.filesize, f.upload_date, f.short_id, f.storage_backend, f.storage_path, f.folder_path, f.link_visibility, f.expires_at
         FROM album_items ai
         JOIN files f ON f.file_id = ai.file_id
         WHERE ai.album_id = ?1
         ORDER BY ai.sort_order ASC, ai.id ASC",
    )?;
    let rows = stmt
        .query_map(params![album_id], |row| {
            Ok(FileMetadata {
                filename: row.get(0)?,
                file_id: row.get(1)?,
                filesize: row.get(2)?,
                upload_date: row.get::<_, String>(3).unwrap_or_default(),
                short_id: row.get(4).ok(),
                storage_backend: row.get::<_, String>(5).unwrap_or_else(|_| constants::STORAGE_BACKEND_TELEGRAM.to_string()),
                storage_path: row.get(6).ok(),
                folder_path: row.get::<_, String>(7).unwrap_or_default(),
                link_visibility: row.get::<_, String>(8).unwrap_or_else(|_| "public".to_string()),
                expires_at: row.get(9).ok(),
            })
        })?
        .filter_map(|r| r.ok())
        .collect();
    Ok(rows)
}

pub fn get_app_settings_from_db(pool: &DbPool) -> Result<HashMap<String, Option<String>>, AppErrorKind> {
    let conn = pool.get()?;
    let result = conn.query_row(
        "SELECT bot_token, channel_name, pass_word, picgo_api_key, base_url, session_token, storage_backend, s3_endpoint, s3_region, s3_bucket, s3_access_key, s3_secret_key, s3_public_base_url, webdav_enabled, webdav_readonly, webdav_username FROM app_settings WHERE id = 1",
        [],
        |row| {
            let mut map = HashMap::new();
            map.insert("BOT_TOKEN".to_string(), row.get::<_, Option<String>>(0)?);
            map.insert("CHANNEL_NAME".to_string(), row.get::<_, Option<String>>(1)?);
            map.insert("PASS_WORD".to_string(), row.get::<_, Option<String>>(2)?);
            map.insert("PICGO_API_KEY".to_string(), row.get::<_, Option<String>>(3)?);
            map.insert("BASE_URL".to_string(), row.get::<_, Option<String>>(4)?);
            map.insert("SESSION_TOKEN".to_string(), row.get::<_, Option<String>>(5)?);
            map.insert("STORAGE_BACKEND".to_string(), row.get::<_, Option<String>>(6)?);
            map.insert("S3_ENDPOINT".to_string(), row.get::<_, Option<String>>(7)?);
            map.insert("S3_REGION".to_string(), row.get::<_, Option<String>>(8)?);
            map.insert("S3_BUCKET".to_string(), row.get::<_, Option<String>>(9)?);
            map.insert("S3_ACCESS_KEY".to_string(), row.get::<_, Option<String>>(10)?);
            map.insert("S3_SECRET_KEY".to_string(), row.get::<_, Option<String>>(11)?);
            map.insert("S3_PUBLIC_BASE_URL".to_string(), row.get::<_, Option<String>>(12)?);
            map.insert("WEBDAV_ENABLED".to_string(), row.get::<_, Option<String>>(13)?);
            map.insert("WEBDAV_READONLY".to_string(), row.get::<_, Option<String>>(14)?);
            map.insert("WEBDAV_USERNAME".to_string(), row.get::<_, Option<String>>(15)?);
            Ok(map)
        },
    );

    match result {
        Ok(map) => Ok(map),
        Err(rusqlite::Error::QueryReturnedNoRows) => Ok(HashMap::new()),
        Err(e) => Err(e.into()),
    }
}

fn norm(v: Option<&str>) -> Option<String> {
    v.map(|s| s.trim().to_string()).filter(|s| !s.is_empty())
}

pub fn save_app_settings_to_db(
    pool: &DbPool,
    payload: &HashMap<String, Option<String>>,
) -> Result<(), AppErrorKind> {
    let conn = pool.get()?;
    conn.execute(
        "UPDATE app_settings SET bot_token = ?1, channel_name = ?2, pass_word = ?3, picgo_api_key = ?4, base_url = ?5, session_token = ?6, storage_backend = ?7, s3_endpoint = ?8, s3_region = ?9, s3_bucket = ?10, s3_access_key = ?11, s3_secret_key = ?12, s3_public_base_url = ?13, webdav_enabled = ?14, webdav_readonly = ?15, webdav_username = ?16 WHERE id = 1",
        params![
            norm(payload.get("BOT_TOKEN").and_then(|v| v.as_deref())),
            norm(payload.get("CHANNEL_NAME").and_then(|v| v.as_deref())),
            norm(payload.get("PASS_WORD").and_then(|v| v.as_deref())),
            norm(payload.get("PICGO_API_KEY").and_then(|v| v.as_deref())),
            norm(payload.get("BASE_URL").and_then(|v| v.as_deref())),
            norm(payload.get("SESSION_TOKEN").and_then(|v| v.as_deref())),
            norm(payload.get("STORAGE_BACKEND").and_then(|v| v.as_deref())),
            norm(payload.get("S3_ENDPOINT").and_then(|v| v.as_deref())),
            norm(payload.get("S3_REGION").and_then(|v| v.as_deref())),
            norm(payload.get("S3_BUCKET").and_then(|v| v.as_deref())),
            norm(payload.get("S3_ACCESS_KEY").and_then(|v| v.as_deref())),
            norm(payload.get("S3_SECRET_KEY").and_then(|v| v.as_deref())),
            norm(payload.get("S3_PUBLIC_BASE_URL").and_then(|v| v.as_deref())),
            norm(payload.get("WEBDAV_ENABLED").and_then(|v| v.as_deref())),
            norm(payload.get("WEBDAV_READONLY").and_then(|v| v.as_deref())),
            norm(payload.get("WEBDAV_USERNAME").and_then(|v| v.as_deref())),
        ],
    )?;
    Ok(())
}

pub fn reset_app_settings_in_db(pool: &DbPool) -> Result<(), AppErrorKind> {
    let mut payload = HashMap::new();
    payload.insert("BOT_TOKEN".to_string(), None);
    payload.insert("CHANNEL_NAME".to_string(), None);
    payload.insert("PASS_WORD".to_string(), None);
    payload.insert("PICGO_API_KEY".to_string(), None);
    payload.insert("BASE_URL".to_string(), None);
    payload.insert("SESSION_TOKEN".to_string(), None);
    payload.insert("STORAGE_BACKEND".to_string(), Some(constants::STORAGE_BACKEND_TELEGRAM.to_string()));
    payload.insert("S3_ENDPOINT".to_string(), None);
    payload.insert("S3_REGION".to_string(), Some("auto".to_string()));
    payload.insert("S3_BUCKET".to_string(), None);
    payload.insert("S3_ACCESS_KEY".to_string(), None);
    payload.insert("S3_SECRET_KEY".to_string(), None);
    payload.insert("S3_PUBLIC_BASE_URL".to_string(), None);
    payload.insert("WEBDAV_ENABLED".to_string(), Some("0".to_string()));
    payload.insert("WEBDAV_READONLY".to_string(), Some("0".to_string()));
    payload.insert("WEBDAV_USERNAME".to_string(), Some("admin".to_string()));
    save_app_settings_to_db(pool, &payload)
}
