package main

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

func (d *DB) Authenticate(ctx context.Context, token string) (*Principal, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, ErrUnauthorized
	}
	hash := tokenFingerprint(token)

	var master Principal
	var enabled int
	err := d.sql.QueryRowContext(
		ctx,
		`SELECT id, name, token_prefix, enabled FROM master_tokens WHERE token_hash = ?`,
		hash,
	).Scan(&master.ID, &master.Name, &master.TokenPrefix, &enabled)
	if err == nil {
		if enabled != 1 {
			return nil, ErrTokenDisabled
		}
		master.Kind = "master"
		master.IsMaster = true
		master.CollectionIDs, err = d.tokenCollectionIDs(ctx, "master", master.ID)
		if err != nil {
			return nil, err
		}
		if err := d.touchToken(ctx, "master", master.ID); err != nil {
			return nil, err
		}
		return &master, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	var child Principal
	var expires sql.NullString
	err = d.sql.QueryRowContext(
		ctx,
		`SELECT id, name, token_prefix, enabled, expires_at, max_uses, use_count
		 FROM child_tokens WHERE token_hash = ?`,
		hash,
	).Scan(
		&child.ID,
		&child.Name,
		&child.TokenPrefix,
		&enabled,
		&expires,
		&child.MaxUses,
		&child.UseCount,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnauthorized
	}
	if err != nil {
		return nil, err
	}
	if enabled != 1 {
		return nil, ErrTokenDisabled
	}
	child.Kind = "child"
	child.ExpiresAt = parseNullableTime(expires.String)
	if err := child.validateSubscriptionUse(time.Now()); err != nil {
		return nil, err
	}
	child.CollectionIDs, err = d.tokenCollectionIDs(ctx, "child", child.ID)
	if err != nil {
		return nil, err
	}
	return &child, nil
}

func (d *DB) touchToken(ctx context.Context, kind string, id int64) error {
	table := "master_tokens"
	if kind == "child" {
		table = "child_tokens"
	}
	_, err := d.sql.ExecContext(ctx, `UPDATE `+table+` SET last_used_at = ? WHERE id = ?`, formatTime(time.Now()), id)
	return err
}

func (d *DB) ConsumeChildUse(ctx context.Context, id int64) error {
	now := formatTime(time.Now())
	result, err := d.sql.ExecContext(
		ctx,
		`UPDATE child_tokens
		 SET use_count = use_count + 1, last_used_at = ?
		 WHERE id = ?
		   AND enabled = 1
		   AND (expires_at IS NULL OR expires_at = '' OR expires_at > ?)
		   AND (max_uses = 0 OR use_count < max_uses)`,
		now, id, now,
	)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrUsageLimit
	}
	return nil
}

func (d *DB) tokenCollectionIDs(ctx context.Context, kind string, id int64) ([]int64, error) {
	query := `SELECT collection_id FROM master_collections WHERE master_token_id = ? ORDER BY collection_id`
	if kind == "child" {
		query = `SELECT collection_id FROM child_collections WHERE child_token_id = ? ORDER BY collection_id`
	}
	rows, err := d.sql.QueryContext(ctx, query, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var collectionID int64
		if err := rows.Scan(&collectionID); err != nil {
			return nil, err
		}
		ids = append(ids, collectionID)
	}
	return ids, rows.Err()
}

func (d *DB) ListMasterTokens(ctx context.Context) ([]MasterToken, error) {
	rows, err := d.sql.QueryContext(
		ctx,
		`SELECT id, name, token, token_prefix, enabled, created_at, last_used_at
		 FROM master_tokens ORDER BY id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tokens := []MasterToken{}
	for rows.Next() {
		var token MasterToken
		var enabled int
		var created string
		var lastUsed sql.NullString
		if err := rows.Scan(&token.ID, &token.Name, &token.Token, &token.TokenPrefix, &enabled, &created, &lastUsed); err != nil {
			return nil, err
		}
		token.Enabled = enabled == 1
		token.CreatedAt = parseTime(created)
		token.LastUsedAt = parseNullableTime(lastUsed.String)
		tokens = append(tokens, token)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range tokens {
		tokens[i].CollectionIDs, err = d.tokenCollectionIDs(ctx, "master", tokens[i].ID)
		if err != nil {
			return nil, err
		}
	}
	return tokens, nil
}

func (d *DB) GetMasterToken(ctx context.Context, id int64) (*MasterToken, error) {
	var token MasterToken
	var enabled int
	var created string
	var lastUsed sql.NullString
	err := d.sql.QueryRowContext(
		ctx,
		`SELECT id, name, token, token_prefix, enabled, created_at, last_used_at
		 FROM master_tokens WHERE id = ?`,
		id,
	).Scan(&token.ID, &token.Name, &token.Token, &token.TokenPrefix, &enabled, &created, &lastUsed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	token.Enabled = enabled == 1
	token.CreatedAt = parseTime(created)
	token.LastUsedAt = parseNullableTime(lastUsed.String)
	token.CollectionIDs, err = d.tokenCollectionIDs(ctx, "master", token.ID)
	if err != nil {
		return nil, err
	}
	return &token, nil
}

func (d *DB) ListChildTokens(ctx context.Context) ([]ChildToken, error) {
	rows, err := d.sql.QueryContext(
		ctx,
		`SELECT id, name, token, token_prefix, enabled, expires_at, max_uses, use_count, note, created_at, last_used_at
		 FROM child_tokens ORDER BY id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tokens := []ChildToken{}
	for rows.Next() {
		var token ChildToken
		var enabled int
		var expires, created, lastUsed sql.NullString
		if err := rows.Scan(
			&token.ID,
			&token.Name,
			&token.Token,
			&token.TokenPrefix,
			&enabled,
			&expires,
			&token.MaxUses,
			&token.UseCount,
			&token.Note,
			&created,
			&lastUsed,
		); err != nil {
			return nil, err
		}
		token.Enabled = enabled == 1
		token.ExpiresAt = parseNullableTime(expires.String)
		token.CreatedAt = parseTime(created.String)
		token.LastUsedAt = parseNullableTime(lastUsed.String)
		tokens = append(tokens, token)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range tokens {
		tokens[i].CollectionIDs, err = d.tokenCollectionIDs(ctx, "child", tokens[i].ID)
		if err != nil {
			return nil, err
		}
	}
	return tokens, nil
}

func (d *DB) GetChildToken(ctx context.Context, id int64) (*ChildToken, error) {
	var token ChildToken
	var enabled int
	var expires, created, lastUsed sql.NullString
	err := d.sql.QueryRowContext(
		ctx,
		`SELECT id, name, token, token_prefix, enabled, expires_at, max_uses, use_count, note, created_at, last_used_at
		 FROM child_tokens WHERE id = ?`,
		id,
	).Scan(
		&token.ID,
		&token.Name,
		&token.Token,
		&token.TokenPrefix,
		&enabled,
		&expires,
		&token.MaxUses,
		&token.UseCount,
		&token.Note,
		&created,
		&lastUsed,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	token.Enabled = enabled == 1
	token.ExpiresAt = parseNullableTime(expires.String)
	token.CreatedAt = parseTime(created.String)
	token.LastUsedAt = parseNullableTime(lastUsed.String)
	token.CollectionIDs, err = d.tokenCollectionIDs(ctx, "child", token.ID)
	if err != nil {
		return nil, err
	}
	return &token, nil
}

func (d *DB) CreateMasterToken(ctx context.Context, name string, enabled bool, collectionIDs []int64) (*MasterToken, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "主 Token"
	}
	token, err := randomToken()
	if err != nil {
		return nil, err
	}
	result, err := d.sql.ExecContext(
		ctx,
		`INSERT INTO master_tokens(name, token, token_hash, token_prefix, enabled, created_at)
		 VALUES(?, ?, ?, ?, ?, ?)`,
		name, token, tokenFingerprint(token), tokenPrefix(token), boolInt(enabled), formatTime(time.Now()),
	)
	if err != nil {
		return nil, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	if err := d.SetTokenCollections(ctx, "master", id, collectionIDs); err != nil {
		return nil, err
	}
	return d.GetMasterToken(ctx, id)
}

func (d *DB) CreateChildToken(ctx context.Context, name string, enabled bool, expiresAt *time.Time, maxUses int64, note string, collectionIDs []int64) (*ChildToken, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("子 Token 名称不能为空")
	}
	if len(collectionIDs) == 0 {
		return nil, errors.New("子 Token 至少需要授权一个集合")
	}
	if maxUses < 0 {
		maxUses = 0
	}
	token, err := randomToken()
	if err != nil {
		return nil, err
	}
	result, err := d.sql.ExecContext(
		ctx,
		`INSERT INTO child_tokens(name, token, token_hash, token_prefix, enabled, expires_at, max_uses, use_count, note, created_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
		name, token, tokenFingerprint(token), tokenPrefix(token), boolInt(enabled), nullableTime(expiresAt), maxUses, strings.TrimSpace(note), formatTime(time.Now()),
	)
	if err != nil {
		return nil, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	if err := d.SetTokenCollections(ctx, "child", id, collectionIDs); err != nil {
		return nil, err
	}
	return d.GetChildToken(ctx, id)
}

func (d *DB) UpdateMasterToken(ctx context.Context, id int64, name string, enabled bool, collectionIDs []int64) (*MasterToken, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "主 Token"
	}
	result, err := d.sql.ExecContext(
		ctx,
		`UPDATE master_tokens SET name = ?, enabled = ? WHERE id = ?`,
		name, boolInt(enabled), id,
	)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return nil, ErrNotFound
	}
	if err := d.SetTokenCollections(ctx, "master", id, collectionIDs); err != nil {
		return nil, err
	}
	return d.GetMasterToken(ctx, id)
}

func (d *DB) UpdateChildToken(ctx context.Context, id int64, name string, enabled bool, expiresAt *time.Time, maxUses int64, note string, collectionIDs []int64) (*ChildToken, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("子 Token 名称不能为空")
	}
	if len(collectionIDs) == 0 {
		return nil, errors.New("子 Token 至少需要授权一个集合")
	}
	if maxUses < 0 {
		maxUses = 0
	}
	result, err := d.sql.ExecContext(
		ctx,
		`UPDATE child_tokens
		 SET name = ?, enabled = ?, expires_at = ?, max_uses = ?, note = ?
		 WHERE id = ?`,
		name, boolInt(enabled), nullableTime(expiresAt), maxUses, strings.TrimSpace(note), id,
	)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return nil, ErrNotFound
	}
	if err := d.SetTokenCollections(ctx, "child", id, collectionIDs); err != nil {
		return nil, err
	}
	return d.GetChildToken(ctx, id)
}

func (d *DB) SetTokenCollections(ctx context.Context, kind string, tokenID int64, collectionIDs []int64) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	table := "master_collections"
	column := "master_token_id"
	if kind == "child" {
		table = "child_collections"
		column = "child_token_id"
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE `+column+` = ?`, tokenID); err != nil {
		return err
	}
	for _, collectionID := range uniqueInt64s(collectionIDs) {
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO `+table+`(`+column+`, collection_id) VALUES(?, ?)`,
			tokenID, collectionID,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (d *DB) RegenerateToken(ctx context.Context, kind string, id int64) error {
	token, err := randomToken()
	if err != nil {
		return err
	}
	table := "master_tokens"
	if kind == "child" {
		table = "child_tokens"
	}
	result, err := d.sql.ExecContext(
		ctx,
		`UPDATE `+table+` SET token = ?, token_hash = ?, token_prefix = ? WHERE id = ?`,
		token, tokenFingerprint(token), tokenPrefix(token), id,
	)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (d *DB) DeleteToken(ctx context.Context, kind string, id int64) error {
	table := "master_tokens"
	if kind == "child" {
		table = "child_tokens"
	}
	var count int
	if err := d.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil {
		return err
	}
	if table == "master_tokens" && count <= 1 {
		return errors.New("至少需要保留一个主 Token")
	}
	result, err := d.sql.ExecContext(ctx, `DELETE FROM `+table+` WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}
