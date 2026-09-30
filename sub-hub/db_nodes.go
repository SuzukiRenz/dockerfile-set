package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

	nodes := []Node{}
	for rows.Next() {
		node, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, *node)
	}
	return nodes, rows.Err()
}

func (d *DB) ListEnabledNodes(ctx context.Context) ([]Node, error) {
	rows, err := d.sql.QueryContext(
		ctx,
		`SELECT id, name, uri, enabled, sort_order, created_at, updated_at
		 FROM nodes WHERE enabled = 1 ORDER BY sort_order, id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	nodes := []Node{}
	for rows.Next() {
		node, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, *node)
	}
	return nodes, rows.Err()
}
func (d *DB) ListEnabledNodeURIs(ctx context.Context) ([]string, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT uri FROM nodes WHERE enabled = 1 ORDER BY sort_order, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanStrings(rows)
}

func (d *DB) CreateNode(ctx context.Context, name, uri string, enabled bool) (*Node, error) {
	name = strings.TrimSpace(name)
	uri = strings.TrimSpace(uri)
	if uri == "" {
		return nil, errors.New("节点 URI 不能为空")
	}
	if !importableNodeURI(uri) {
		return nil, errors.New("节点 URI 格式无效")
	}
	if name == "" {
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
		name, uri, boolInt(enabled), nextOrder, now, now,
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

func (d *DB) BatchCreateNodes(ctx context.Context, raw string) (NodeBatchResult, error) {
	importResult := parseNodeInput(raw)
	if len(importResult.URIs) == 0 {
		return NodeBatchResult{
			Format:  importResult.Format,
			Label:   formatNodeImportLabel(importResult.Format),
			Ignored: importResult.Ignored,
		}, nodeImportError(importResult.Format)
	}

	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return NodeBatchResult{Format: importResult.Format, Label: formatNodeImportLabel(importResult.Format)}, err
	}
	defer func() { _ = tx.Rollback() }()

	var nextOrder int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sort_order), -1) + 1 FROM nodes`).Scan(&nextOrder); err != nil {
		return NodeBatchResult{Format: importResult.Format, Label: formatNodeImportLabel(importResult.Format)}, err
	}
	now := formatTime(time.Now())
	added, skipped := 0, 0
	for _, uri := range importResult.URIs {
		result, err := tx.ExecContext(
			ctx,
			`INSERT OR IGNORE INTO nodes(name, uri, enabled, sort_order, created_at, updated_at)
			 VALUES(?, ?, 1, ?, ?, ?)`,
			extractName(uri), uri, nextOrder, now, now,
		)
		if err != nil {
			return NodeBatchResult{Format: importResult.Format, Label: formatNodeImportLabel(importResult.Format)}, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return NodeBatchResult{Format: importResult.Format, Label: formatNodeImportLabel(importResult.Format)}, err
		}
		if affected == 0 {
			skipped++
			continue
		}
		added++
		nextOrder++
	}
	if err := tx.Commit(); err != nil {
		return NodeBatchResult{Format: importResult.Format, Label: formatNodeImportLabel(importResult.Format)}, err
	}
	return NodeBatchResult{
		Format:   importResult.Format,
		Label:    formatNodeImportLabel(importResult.Format),
		Imported: added,
		Skipped:  skipped,
		Ignored:  importResult.Ignored,
	}, nil
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
	if !importableNodeURI(node.URI) {
		return nil, errors.New("节点 URI 格式无效")
	}
	_, err := d.sql.ExecContext(
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
	return d.GetNode(ctx, node.ID)
}

func (d *DB) PatchNode(ctx context.Context, id int64, updates map[string]any) (*Node, error) {
	node, err := d.GetNode(ctx, id)
	if err != nil {
		return nil, err
	}
	if value, ok := updates["name"]; ok {
		name, _ := value.(string)
		node.Name = strings.TrimSpace(name)
		if node.Name == "" {
			node.Name = extractName(node.URI)
		}
	}
	if value, ok := updates["uri"]; ok {
		uri, _ := value.(string)
		node.URI = strings.TrimSpace(uri)
		if node.URI == "" {
			return nil, errors.New("节点 URI 不能为空")
		}
		if !importableNodeURI(node.URI) {
			return nil, errors.New("节点 URI 格式无效")
		}
	}
	if value, ok := updates["enabled"]; ok {
		enabled, ok := value.(bool)
		if !ok {
			return nil, errors.New("enabled 必须是布尔值")
		}
		node.Enabled = enabled
	}
	return d.UpdateNode(ctx, *node)
}

func (d *DB) DeleteNode(ctx context.Context, id int64) error {
	result, err := d.sql.ExecContext(ctx, `DELETE FROM nodes WHERE id = ?`, id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (d *DB) BulkNodes(ctx context.Context, ids []int64, action string) (int, error) {
	ids = uniqueInt64s(ids)
	if len(ids) == 0 {
		return 0, errors.New("请选择节点")
	}
	var query string
	switch action {
	case "enable":
		query = `UPDATE nodes SET enabled = 1, updated_at = ? WHERE id IN (%s)`
	case "disable":
		query = `UPDATE nodes SET enabled = 0, updated_at = ? WHERE id IN (%s)`
	case "delete":
		query = `DELETE FROM nodes WHERE id IN (%s)`
	default:
		return 0, errors.New("不支持的批量操作")
	}
	args := []any{}
	if action != "delete" {
		args = append(args, formatTime(time.Now()))
	}
	for _, id := range ids {
		args = append(args, id)
	}
	result, err := d.sql.ExecContext(ctx, fmt.Sprintf(query, placeholders(len(ids))), args...)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	return int(affected), err
}

func (d *DB) ReorderNodes(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for index, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE nodes SET sort_order = ? WHERE id = ?`, index, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (d *DB) ReplaceNodeURIs(ctx context.Context, uris []string) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM nodes`); err != nil {
		return err
	}
	now := formatTime(time.Now())
	for index, uri := range uniqueStrings(uris) {
		if !importableNodeURI(uri) {
			continue
		}
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO nodes(name, uri, enabled, sort_order, created_at, updated_at)
			 VALUES(?, ?, 1, ?, ?, ?)`,
			extractName(uri), uri, index, now, now,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}
