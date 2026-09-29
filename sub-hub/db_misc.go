package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"
)

func (d *DB) LegacyNodeURIs(ctx context.Context, nodesFile string) ([]string, error) {
	enabled, err := d.Setting(ctx, "legacy_fallback")
	if err != nil {
		return nil, err
	}
	if enabled == "false" {
		return nil, ErrNoNodes
	}

	uris := []string{}
	seen := map[string]struct{}{}
	appendRaw := func(raw string) {
		for _, uri := range splitNodeLines(raw) {
			if _, ok := seen[uri]; ok {
				continue
			}
			seen[uri] = struct{}{}
			uris = append(uris, uri)
		}
	}
	appendRaw(os.Getenv("NODES"))
	if strings.TrimSpace(nodesFile) != "" {
		data, readErr := os.ReadFile(nodesFile)
		if readErr == nil {
			appendRaw(string(data))
		} else if !os.IsNotExist(readErr) {
			return nil, readErr
		}
	}
	if len(uris) == 0 {
		return nil, ErrNoNodes
	}
	return uris, nil
}

func (d *DB) Overview(ctx context.Context) (Overview, error) {
	var overview Overview
	if err := d.sql.QueryRowContext(
		ctx,
		`SELECT COUNT(*), COALESCE(SUM(enabled), 0) FROM nodes`,
	).Scan(&overview.NodeCount, &overview.EnabledNodes); err != nil {
		return overview, err
	}
	if err := d.sql.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM collections`,
	).Scan(&overview.CollectionCount); err != nil {
		return overview, err
	}
	if err := d.sql.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM master_tokens`,
	).Scan(&overview.MasterCount); err != nil {
		return overview, err
	}
	if err := d.sql.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM child_tokens`,
	).Scan(&overview.ChildCount); err != nil {
		return overview, err
	}
	now := formatTime(time.Now())
	if err := d.sql.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM child_tokens
		 WHERE enabled = 1
		   AND (expires_at IS NULL OR expires_at = '' OR expires_at > ?)
		   AND (max_uses = 0 OR use_count < max_uses)`,
		now,
	).Scan(&overview.ActiveChildren); err != nil {
		return overview, err
	}
	if err := d.sql.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM access_logs`,
	).Scan(&overview.RequestCount); err != nil {
		return overview, err
	}
	start := time.Now().UTC().Truncate(24 * time.Hour)
	if err := d.sql.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM access_logs WHERE created_at >= ?`,
		formatTime(start),
	).Scan(&overview.TodayRequests); err != nil {
		return overview, err
	}
	return overview, nil
}

func (d *DB) LogAccess(ctx context.Context, entry AccessLog, maxRecords int) error {
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now()
	}
	if _, err := d.sql.ExecContext(
		ctx,
		`INSERT INTO access_logs(
			token_kind, token_id, token_name, token_prefix, collection_slug, target,
			ip, user_agent, status, node_count, created_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.TokenKind,
		entry.TokenID,
		entry.TokenName,
		entry.TokenPrefix,
		entry.CollectionSlug,
		entry.Target,
		entry.IP,
		entry.UserAgent,
		entry.Status,
		entry.NodeCount,
		formatTime(entry.CreatedAt),
	); err != nil {
		return err
	}
	if maxRecords > 0 {
		_, _ = d.sql.ExecContext(
			ctx,
			`DELETE FROM access_logs WHERE id NOT IN (
				SELECT id FROM access_logs ORDER BY id DESC LIMIT ?
			)`,
			maxRecords,
		)
	}
	return nil
}

func (d *DB) ListLogs(ctx context.Context, limit int) ([]AccessLog, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := d.sql.QueryContext(
		ctx,
		`SELECT id, token_kind, token_id, token_name, token_prefix, collection_slug,
		        target, ip, user_agent, status, node_count, created_at
		 FROM access_logs ORDER BY id DESC LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	logs := []AccessLog{}
	for rows.Next() {
		var entry AccessLog
		var created string
		if err := rows.Scan(
			&entry.ID,
			&entry.TokenKind,
			&entry.TokenID,
			&entry.TokenName,
			&entry.TokenPrefix,
			&entry.CollectionSlug,
			&entry.Target,
			&entry.IP,
			&entry.UserAgent,
			&entry.Status,
			&entry.NodeCount,
			&created,
		); err != nil {
			return nil, err
		}
		entry.CreatedAt = parseTime(created)
		logs = append(logs, entry)
	}
	return logs, rows.Err()
}

func (d *DB) ClearLogs(ctx context.Context) error {
	_, err := d.sql.ExecContext(ctx, `DELETE FROM access_logs`)
	return err
}

func (d *DB) Backup(ctx context.Context) (*BackupData, error) {
	nodes, err := d.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	collections, err := d.ListCollections(ctx)
	if err != nil {
		return nil, err
	}
	masters, err := d.ListMasterTokens(ctx)
	if err != nil {
		return nil, err
	}
	children, err := d.ListChildTokens(ctx)
	if err != nil {
		return nil, err
	}
	settings, err := d.Settings(ctx)
	if err != nil {
		return nil, err
	}

	backup := &BackupData{
		Version:     1,
		ExportedAt:  time.Now().UTC(),
		Settings:    settings,
		Nodes:       []BackupNode{},
		Collections: []BackupCollection{},
		Masters:     []BackupMaster{},
		Children:    []BackupChild{},
	}
	uriByNodeID := map[int64]string{}
	for _, node := range nodes {
		uriByNodeID[node.ID] = node.URI
		createdAt := node.CreatedAt
		backup.Nodes = append(backup.Nodes, BackupNode{
			Name:      node.Name,
			URI:       node.URI,
			Enabled:   node.Enabled,
			SortOrder: node.SortOrder,
			CreatedAt: &createdAt,
		})
	}
	slugByCollectionID := map[int64]string{}
	for _, collection := range collections {
		slugByCollectionID[collection.ID] = collection.Slug
		item := BackupCollection{
			Name:        collection.Name,
			Slug:        collection.Slug,
			Description: collection.Description,
			Enabled:     collection.Enabled,
			SortOrder:   collection.SortOrder,
			NodeURIs:    []string{},
		}
		for _, nodeID := range collection.NodeIDs {
			if uri := uriByNodeID[nodeID]; uri != "" {
				item.NodeURIs = append(item.NodeURIs, uri)
			}
		}
		backup.Collections = append(backup.Collections, item)
	}
	for _, token := range masters {
		item := BackupMaster{
			Name:            token.Name,
			Token:           token.Token,
			Enabled:         token.Enabled,
			CollectionSlugs: []string{},
		}
		for _, collectionID := range token.CollectionIDs {
			if slug := slugByCollectionID[collectionID]; slug != "" {
				item.CollectionSlugs = append(item.CollectionSlugs, slug)
			}
		}
		backup.Masters = append(backup.Masters, item)
	}
	for _, token := range children {
		item := BackupChild{
			Name:            token.Name,
			Token:           token.Token,
			Enabled:         token.Enabled,
			ExpiresAt:       token.ExpiresAt,
			MaxUses:         token.MaxUses,
			UseCount:        token.UseCount,
			Note:            token.Note,
			CollectionSlugs: []string{},
		}
		for _, collectionID := range token.CollectionIDs {
			if slug := slugByCollectionID[collectionID]; slug != "" {
				item.CollectionSlugs = append(item.CollectionSlugs, slug)
			}
		}
		backup.Children = append(backup.Children, item)
	}
	return backup, nil
}

func (d *DB) Restore(ctx context.Context, backup *BackupData) error {
	if backup == nil {
		return errors.New("备份内容为空")
	}
	if backup.Version == 0 && backup.Config != nil {
		return d.restoreLegacy(ctx, backup)
	}
	if len(backup.Masters) == 0 {
		return errors.New("备份中至少需要一个主 Token")
	}

	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for _, table := range []string{
		"access_logs",
		"child_collections",
		"master_collections",
		"collection_nodes",
		"collections",
		"nodes",
		"child_tokens",
		"master_tokens",
	} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table); err != nil {
			return err
		}
	}
	for _, key := range []string{"sub_name", "subconfig_url", "update_interval", "userinfo_header", "legacy_fallback"} {
		if value, ok := backup.Settings[key]; ok {
			if _, err := tx.ExecContext(
				ctx,
				`INSERT INTO settings(key, value) VALUES(?, ?)
				 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
				key, value,
			); err != nil {
				return err
			}
		}
	}

	nodeIDByURI := map[string]int64{}
	now := formatTime(time.Now())
	for index, item := range backup.Nodes {
		uri := strings.TrimSpace(item.URI)
		if uri == "" {
			continue
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = extractName(uri)
		}
		order := item.SortOrder
		if order == 0 {
			order = index
		}
		created := now
		if item.CreatedAt != nil && !item.CreatedAt.IsZero() {
			created = formatTime(*item.CreatedAt)
		}
		result, err := tx.ExecContext(
			ctx,
			`INSERT INTO nodes(name, uri, enabled, sort_order, created_at, updated_at)
			 VALUES(?, ?, ?, ?, ?, ?)`,
			name, uri, boolInt(item.Enabled), order, created, now,
		)
		if err != nil {
			return err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return err
		}
		nodeIDByURI[uri] = id
	}

	collectionIDBySlug := map[string]int64{}
	for index, item := range backup.Collections {
		name := strings.TrimSpace(item.Name)
		slug := strings.TrimSpace(item.Slug)
		if name == "" || slug == "" {
			continue
		}
		order := item.SortOrder
		if order == 0 {
			order = index
		}
		result, err := tx.ExecContext(
			ctx,
			`INSERT INTO collections(name, slug, description, enabled, sort_order, created_at, updated_at)
			 VALUES(?, ?, ?, ?, ?, ?, ?)`,
			name, slug, strings.TrimSpace(item.Description), boolInt(item.Enabled), order, now, now,
		)
		if err != nil {
			return err
		}
		collectionID, err := result.LastInsertId()
		if err != nil {
			return err
		}
		collectionIDBySlug[slug] = collectionID
		for position, uri := range uniqueStrings(item.NodeURIs) {
			nodeID := nodeIDByURI[uri]
			if nodeID == 0 {
				continue
			}
			if _, err := tx.ExecContext(
				ctx,
				`INSERT INTO collection_nodes(collection_id, node_id, position) VALUES(?, ?, ?)`,
				collectionID, nodeID, position,
			); err != nil {
				return err
			}
		}
	}

	for _, item := range backup.Masters {
		tokenValue := strings.TrimSpace(item.Token)
		if tokenValue == "" {
			continue
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = "主 Token"
		}
		result, err := tx.ExecContext(
			ctx,
			`INSERT INTO master_tokens(name, token, token_hash, token_prefix, enabled, created_at)
			 VALUES(?, ?, ?, ?, ?, ?)`,
			name, tokenValue, tokenFingerprint(tokenValue), tokenPrefix(tokenValue), boolInt(item.Enabled), now,
		)
		if err != nil {
			return err
		}
		tokenID, err := result.LastInsertId()
		if err != nil {
			return err
		}
		for _, slug := range uniqueStrings(item.CollectionSlugs) {
			collectionID := collectionIDBySlug[slug]
			if collectionID == 0 {
				continue
			}
			if _, err := tx.ExecContext(
				ctx,
				`INSERT INTO master_collections(master_token_id, collection_id) VALUES(?, ?)`,
				tokenID, collectionID,
			); err != nil {
				return err
			}
		}
	}

	for _, item := range backup.Children {
		tokenValue := strings.TrimSpace(item.Token)
		if tokenValue == "" {
			continue
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = "子 Token"
		}
		result, err := tx.ExecContext(
			ctx,
			`INSERT INTO child_tokens(name, token, token_hash, token_prefix, enabled, expires_at, max_uses, use_count, note, created_at)
			 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			name,
			tokenValue,
			tokenFingerprint(tokenValue),
			tokenPrefix(tokenValue),
			boolInt(item.Enabled),
			nullableTime(item.ExpiresAt),
			item.MaxUses,
			item.UseCount,
			strings.TrimSpace(item.Note),
			now,
		)
		if err != nil {
			return err
		}
		tokenID, err := result.LastInsertId()
		if err != nil {
			return err
		}
		for _, slug := range uniqueStrings(item.CollectionSlugs) {
			collectionID := collectionIDBySlug[slug]
			if collectionID == 0 {
				continue
			}
			if _, err := tx.ExecContext(
				ctx,
				`INSERT INTO child_collections(child_token_id, collection_id) VALUES(?, ?)`,
				tokenID, collectionID,
			); err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}

func (d *DB) restoreLegacy(ctx context.Context, backup *BackupData) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM nodes`); err != nil {
		return err
	}
	now := time.Now()
	for index, item := range backup.Nodes {
		uri := strings.TrimSpace(item.URI)
		if uri == "" {
			continue
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = extractName(uri)
		}
		order := item.SortOrder
		if order == 0 {
			order = index
		}
		created := now
		if item.CreatedAt != nil && !item.CreatedAt.IsZero() {
			created = *item.CreatedAt
		}
		if _, err := tx.ExecContext(
			ctx,
			`INSERT OR IGNORE INTO nodes(name, uri, enabled, sort_order, created_at, updated_at)
			 VALUES(?, ?, ?, ?, ?, ?)`,
			name, uri, boolInt(item.Enabled), order, formatTime(created), formatTime(now),
		); err != nil {
			return err
		}
	}

	if backup.Config != nil {
		for key, value := range map[string]string{
			"sub_name":      strings.TrimSpace(backup.Config.SubName),
			"subconfig_url": strings.TrimSpace(backup.Config.SubConfigURL),
		} {
			if value == "" {
				continue
			}
			if _, err := tx.ExecContext(
				ctx,
				`INSERT INTO settings(key, value) VALUES(?, ?)
				 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
				key, value,
			); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
