package main

import (
	"database/sql"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ── Models ────────────────────────────────────────────────────────────────────

type Node struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	URI       string    `json:"uri"`
	Enabled   bool      `json:"enabled"`
	SortOrder int       `json:"sort_order"`
	CreatedAt time.Time `json:"created_at"`
}

// ── Init ──────────────────────────────────────────────────────────────────────

func initDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS nodes (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			name       TEXT    NOT NULL DEFAULT '',
			uri        TEXT    NOT NULL,
			enabled    INTEGER NOT NULL DEFAULT 1,
			sort_order INTEGER NOT NULL DEFAULT 0,
			created_at TEXT    NOT NULL DEFAULT (datetime('now'))
		);
		CREATE TABLE IF NOT EXISTS config (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL DEFAULT ''
		);
	`)
	if err != nil {
		return nil, err
	}

	db.Exec(`INSERT OR IGNORE INTO config (key,value) VALUES
		('sub_name',      'My Subscription'),
		('subconfig_url', '')`)

	return db, nil
}

// ── Node CRUD ─────────────────────────────────────────────────────────────────

func getAllNodes(db *sql.DB) ([]Node, error) {
	rows, err := db.Query(`
		SELECT id, name, uri, enabled, sort_order, created_at
		FROM nodes ORDER BY sort_order, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nodes []Node
	for rows.Next() {
		var n Node
		var enabled int
		var ts string
		rows.Scan(&n.ID, &n.Name, &n.URI, &enabled, &n.SortOrder, &ts)
		n.Enabled = enabled == 1
		n.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", ts)
		nodes = append(nodes, n)
	}
	if nodes == nil {
		nodes = []Node{}
	}
	return nodes, nil
}

func getEnabledNodes(db *sql.DB) ([]Node, error) {
	rows, err := db.Query(`
		SELECT id, name, uri FROM nodes
		WHERE enabled=1 ORDER BY sort_order, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nodes []Node
	for rows.Next() {
		var n Node
		rows.Scan(&n.ID, &n.Name, &n.URI)
		nodes = append(nodes, n)
	}
	return nodes, nil
}

func createNode(db *sql.DB, n *Node) error {
	var maxOrder int
	db.QueryRow(`SELECT COALESCE(MAX(sort_order),0) FROM nodes`).Scan(&maxOrder)
	n.SortOrder = maxOrder + 1
	n.Enabled = true

	res, err := db.Exec(
		`INSERT INTO nodes (name,uri,enabled,sort_order) VALUES (?,?,?,?)`,
		n.Name, n.URI, 1, n.SortOrder)
	if err != nil {
		return err
	}
	n.ID, _ = res.LastInsertId()
	return nil
}

func batchCreateNodes(db *sql.DB, raw string) (int, error) {
	var maxOrder int
	db.QueryRow(`SELECT COALESCE(MAX(sort_order),0) FROM nodes`).Scan(&maxOrder)

	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	stmt, err := tx.Prepare(
		`INSERT INTO nodes (name,uri,enabled,sort_order) VALUES (?,?,1,?)`)
	if err != nil {
		tx.Rollback()
		return 0, err
	}
	defer stmt.Close()

	count := 0
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name := ""
		if idx := strings.LastIndex(line, "#"); idx != -1 {
			name = line[idx+1:]
		}
		maxOrder++
		if _, err := stmt.Exec(name, line, maxOrder); err == nil {
			count++
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}

func updateNode(db *sql.DB, n *Node) error {
	_, err := db.Exec(
		`UPDATE nodes SET name=?,uri=?,enabled=?,sort_order=? WHERE id=?`,
		n.Name, n.URI, boolInt(n.Enabled), n.SortOrder, n.ID)
	return err
}

func deleteNode(db *sql.DB, id int64) error {
	_, err := db.Exec(`DELETE FROM nodes WHERE id=?`, id)
	return err
}

func reorderNodes(db *sql.DB, ids []int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	for i, id := range ids {
		tx.Exec(`UPDATE nodes SET sort_order=? WHERE id=?`, i, id)
	}
	return tx.Commit()
}

// ── Config ────────────────────────────────────────────────────────────────────

func getConfig(db *sql.DB, key string) (string, error) {
	var v string
	err := db.QueryRow(`SELECT value FROM config WHERE key=?`, key).Scan(&v)
	return v, err
}

func setConfig(db *sql.DB, key, value string) error {
	_, err := db.Exec(`INSERT OR REPLACE INTO config (key,value) VALUES (?,?)`, key, value)
	return err
}

func getAllConfig(db *sql.DB) (map[string]string, error) {
	rows, err := db.Query(`SELECT key,value FROM config`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := make(map[string]string)
	for rows.Next() {
		var k, v string
		rows.Scan(&k, &v)
		m[k] = v
	}
	return m, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}