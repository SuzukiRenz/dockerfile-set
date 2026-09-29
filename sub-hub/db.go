package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type DB struct {
	sql  *sql.DB
	path string
}

func OpenDB(path, bootstrapToken string) (*DB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("database path is empty")
	}
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}

	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	store := &DB{sql: db, path: path}
	if err := store.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.seed(bootstrapToken); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (d *DB) Close() error {
	return d.sql.Close()
}

func (d *DB) migrate() error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS nodes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL DEFAULT '',
			uri TEXT NOT NULL UNIQUE,
			enabled INTEGER NOT NULL DEFAULT 1,
			sort_order INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS collections (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			slug TEXT NOT NULL UNIQUE,
			description TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 1,
			sort_order INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS collection_nodes (
			collection_id INTEGER NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
			node_id INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
			position INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (collection_id, node_id)
		)`,
		`CREATE TABLE IF NOT EXISTS master_tokens (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL DEFAULT '主 Token',
			token TEXT NOT NULL UNIQUE,
			token_hash TEXT NOT NULL UNIQUE,
			token_prefix TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at TEXT NOT NULL,
			last_used_at TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS child_tokens (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			token TEXT NOT NULL UNIQUE,
			token_hash TEXT NOT NULL UNIQUE,
			token_prefix TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			expires_at TEXT,
			max_uses INTEGER NOT NULL DEFAULT 0,
			use_count INTEGER NOT NULL DEFAULT 0,
			note TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			last_used_at TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS master_collections (
			master_token_id INTEGER NOT NULL REFERENCES master_tokens(id) ON DELETE CASCADE,
			collection_id INTEGER NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
			PRIMARY KEY (master_token_id, collection_id)
		)`,
		`CREATE TABLE IF NOT EXISTS child_collections (
			child_token_id INTEGER NOT NULL REFERENCES child_tokens(id) ON DELETE CASCADE,
			collection_id INTEGER NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
			PRIMARY KEY (child_token_id, collection_id)
		)`,
		`CREATE TABLE IF NOT EXISTS access_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			token_kind TEXT NOT NULL,
			token_id INTEGER NOT NULL,
			token_name TEXT NOT NULL DEFAULT '',
			token_prefix TEXT NOT NULL DEFAULT '',
			collection_slug TEXT NOT NULL DEFAULT '',
			target TEXT NOT NULL DEFAULT '',
			ip TEXT NOT NULL DEFAULT '',
			user_agent TEXT NOT NULL DEFAULT '',
			status INTEGER NOT NULL DEFAULT 0,
			node_count INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_nodes_sort ON nodes(sort_order, id)`,
		`CREATE INDEX IF NOT EXISTS idx_collection_nodes_position ON collection_nodes(collection_id, position)`,
		`CREATE INDEX IF NOT EXISTS idx_master_collections_token ON master_collections(master_token_id)`,
		`CREATE INDEX IF NOT EXISTS idx_child_collections_token ON child_collections(child_token_id)`,
		`CREATE INDEX IF NOT EXISTS idx_access_logs_created ON access_logs(created_at DESC)`,
	}
	for _, statement := range statements {
		if _, err := d.sql.Exec(statement); err != nil {
			return fmt.Errorf("migrate database: %w", err)
		}
	}
	return nil
}

func (d *DB) seed(bootstrapToken string) error {
	defaults := map[string]string{
		"sub_name":        "Sub Hub",
		"subconfig_url":   "",
		"update_interval": "24",
		"userinfo_header": "upload=0; download=0; total=0; expire=0",
		"legacy_fallback": "true",
	}
	for key, value := range defaults {
		if _, err := d.sql.Exec(`INSERT OR IGNORE INTO settings(key, value) VALUES(?, ?)`, key, value); err != nil {
			return fmt.Errorf("seed setting: %w", err)
		}
	}

	var count int
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM master_tokens`).Scan(&count); err != nil {
		return fmt.Errorf("count master tokens: %w", err)
	}
	if count > 0 {
		return nil
	}
	if strings.TrimSpace(bootstrapToken) == "" {
		return errors.New("no master token exists and MASTER_TOKEN is empty")
	}
	_, err := d.sql.Exec(
		`INSERT INTO master_tokens(name, token, token_hash, token_prefix, enabled, created_at)
		 VALUES(?, ?, ?, ?, 1, ?)`,
		"环境变量主 Token", bootstrapToken, tokenFingerprint(bootstrapToken), tokenPrefix(bootstrapToken), formatTime(time.Now()),
	)
	if err != nil {
		return fmt.Errorf("seed master token: %w", err)
	}
	return nil
}

func (d *DB) Setting(ctx context.Context, key string) (string, error) {
	var value string
	err := d.sql.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

func (d *DB) Settings(ctx context.Context) (map[string]string, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT key, value FROM settings ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	settings := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		settings[key] = value
	}
	return settings, rows.Err()
}

func (d *DB) UpdateSettings(ctx context.Context, values map[string]string) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for key, value := range values {
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO settings(key, value) VALUES(?, ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
			key, value,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func scanNode(scanner interface{ Scan(...any) error }) (*Node, error) {
	var node Node
	var enabled int
	var created, updated string
	if err := scanner.Scan(&node.ID, &node.Name, &node.URI, &enabled, &node.SortOrder, &created, &updated); err != nil {
		return nil, err
	}
	node.Enabled = enabled == 1
	node.CreatedAt = parseTime(created)
	node.UpdatedAt = parseTime(updated)
	return &node, nil
}

func isUniqueError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique constraint") || strings.Contains(message, "constraint failed")
}
