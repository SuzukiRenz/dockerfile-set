package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

func (d *DB) GetNode(ctx context.Context, id int64) (*Node, error) {
	node, err := scanNode(d.sql.QueryRowContext(
		ctx,
		`SELECT id, name, uri, enabled, sort_order, created_at, updated_at FROM nodes WHERE id = ?`,
		id,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return node, err
}

func (d *DB) ListNodes(ctx context.Context) ([]Node, error) {
	rows, err := d.sql.QueryContext(
		ctx,
		`SELECT id, name, uri, enabled, sort_order, created_at, updated_at
		 FROM nodes ORDER BY sort_order, id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	nodes := make([]Node, 0)
	for rows.Next() {
		node, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, *node)
	}
	return nodes, rows.Err()
}

func (d *DB) CreateNode(ctx context.Context, name, uri string, enabled bool) (*Node, error) {
	uri = strings.TrimSpace(uri)
	if uri == "" {
		return nil, errors.New("节点 URI 不能为空")
	}
	if strings.TrimSpace(name) == "" {
		name = extractName(uri)
	}
	var nextOrder int
	if err := d.sql.QueryRowContext(ctx, `SELECT COALESCE(MAX(sort_order), -1) + 1 FROM nodes`).Scan(&nextOrder); err != nil {
		return nil, err
	}
	now := formatTime(time.Now())
	result, err := d.sql.ExecContext(
		ctx,
		`INSERT INTO nodes(name, uri, enabled, sort_order, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?)`,
		strings.TrimSpace(name), uri, boolInt(enabled), nextOrder, now, now,
	)
	if err != nil {
		if isUniqueError(err) {
			return nil, errors.New("节点 URI 已存在")
		}
		return nil, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	return d.GetNode(ctx, id)
}

func (d *DB) BatchCreateNodes(ctx context.Context, raw string) (int, int, error) {
	uris := splitNodeLines(raw)
	if len(uris) == 0 {
		return 0, 0, errors.New("没有识别到有效节点 URI")
	}

	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()

	var nextOrder int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sort_order), -1) + 1 FROM nodes`).Scan(&nextOrder); err != nil {
		return 0, 0, err
	}
	now := formatTime(time.Now())
	added, skipped := 0, 0
	for _, uri := range uris {
		result, err := tx.ExecContext(
			ctx,
			`INSERT OR IGNORE INTO nodes(name, uri, enabled, sort_order, created_at, updated_at)
			 VALUES(?, ?, 1, ?, ?, ?)`,
			extractName(uri), uri, nextOrder, now, now,
		)
		if err != nil {
			return 0, 0, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return 0, 0, err
		}
		if affected == 0 {
			skipped++
			continue
		}
		added++
		nextOrder++
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return added, skipped, nil
}

func (d *DB) UpdateNode(ctx context.Context, node Node) (*Node, error) {
	node.URI = strings.TrimSpace(node.URI)
	if node.URI == "" {
		return nil, errors.New("节点 URI 不能为空")
	}
	node.Name = strings.TrimSpace(node.Name)
	if node.Name == "" {
		node.Name = extractName(node.URI)
	}
	result, err := d.sql.ExecContext(
		ctx,
		`UPDATE nodes SET name = ?, uri = ?, enabled = ?, updated_at = ? WHERE id = ?`,
		node.Name, node.URI, boolInt(node.Enabled), formatTime(time.Now()), node.ID,
	)
	if err != nil {
		if isUniqueError(err) {
			return nil, errors.New("节点 URI 已存在")
		}
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return nil, ErrNotFound
	}
	return d.GetNode(ctx, node.ID)
}

func (d *DB) PatchNode(ctx context.Context, id int64, updates map[string]any) (*Node, error) {
	node, err := d.GetNode(ctx, id)
	if err != nil {
		return nil, err
	}
	if value, ok := updates["name"].(string); ok {
		node.Name = value
	}
	if value, ok := updates["uri"].(string); ok {
		node.URI = value
	}
	if value, ok := updates["enabled"].(bool); ok {
		node.Enabled = value
	}
	return d.UpdateNode(ctx, *node)
}

func (d *DB) DeleteNode(ctx context.Context, id int64) error {
	result, err := d.sql.ExecContext(ctx, `DELETE FROM nodes WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (d *DB) ReorderNodes(ctx context.Context, ids []int64) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for index, id := range ids {
		if _, err := tx.ExecContext(
			ctx,
			`UPDATE nodes SET sort_order = ?, updated_at = ? WHERE id = ?`,
			index, formatTime(time.Now()), id,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (d *DB) BulkNodes(ctx context.Context, ids []int64, action string) (int, error) {
	if len(ids) == 0 {
		return 0, errors.New("未选择节点")
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	affectedTotal := 0
	for _, id := range ids {
		var result sql.Result
		switch action {
		case "enable":
			result, err = tx.ExecContext(ctx, `UPDATE nodes SET enabled = 1, updated_at = ? WHERE id = ?`, formatTime(time.Now()), id)
		case "disable":
			result, err = tx.ExecContext(ctx, `UPDATE nodes SET enabled = 0, updated_at = ? WHERE id = ?`, formatTime(time.Now()), id)
		case "delete":
			result, err = tx.ExecContext(ctx, `DELETE FROM nodes WHERE id = ?`, id)
		default:
			return 0, errors.New("不支持的批量操作")
		}
		if err != nil {
			return 0, err
		}
		if affected, _ := result.RowsAffected(); affected > 0 {
			affectedTotal += int(affected)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	if action == "delete" {
		_ = d.resequenceNodes(ctx)
	}
	return affectedTotal, nil
}

func (d *DB) resequenceNodes(ctx context.Context) error {
	nodes, err := d.ListNodes(ctx)
	if err != nil {
		return err
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].SortOrder == nodes[j].SortOrder {
			return nodes[i].ID < nodes[j].ID
		}
		return nodes[i].SortOrder < nodes[j].SortOrder
	})
	ids := make([]int64, len(nodes))
	for i, node := range nodes {
		ids[i] = node.ID
	}
	return d.ReorderNodes(ctx, ids)
}

func (d *DB) NodeURIsForIDs(ctx context.Context, ids []int64) ([]string, error) {
	ids = uniqueInt64s(ids)
	if len(ids) == 0 {
		return nil, nil
	}
	query, args := sqlInQuery(`SELECT id, uri FROM nodes WHERE enabled = 1 AND id IN (%s)`, ids)
	rows, err := d.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	uriByID := map[int64]string{}
	for rows.Next() {
		var id int64
		var uri string
		if err := rows.Scan(&id, &uri); err != nil {
			return nil, err
		}
		uriByID[id] = uri
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if uri := strings.TrimSpace(uriByID[id]); uri != "" {
			out = append(out, uri)
		}
	}
	return out, nil
}

func sqlInQuery(base string, ids []int64) (string, []any) {
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	return fmt.Sprintf(base, strings.Join(placeholders, ",")), args
}
