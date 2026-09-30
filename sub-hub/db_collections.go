package main

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"
)

func scanCollection(scanner interface{ Scan(...any) error }) (*Collection, error) {
	var collection Collection
	var enabled int
	var created, updated string
	if err := scanner.Scan(
		&collection.ID,
		&collection.Name,
		&collection.Slug,
		&collection.Description,
		&enabled,
		&collection.SortOrder,
		&created,
		&updated,
		&collection.NodeCount,
		&collection.TokenCount,
	); err != nil {
		return nil, err
	}
	collection.Enabled = enabled == 1
	collection.CreatedAt = parseTime(created)
	collection.UpdatedAt = parseTime(updated)
	collection.NodeIDs = []int64{}
	return &collection, nil
}

func (d *DB) ListCollections(ctx context.Context) ([]Collection, error) {
	rows, err := d.sql.QueryContext(
		ctx,
		`SELECT c.id, c.name, c.slug, c.description, c.enabled, c.sort_order, c.created_at, c.updated_at,
		        COUNT(DISTINCT cn.node_id) AS node_count,
		        COUNT(DISTINCT mt.master_token_id) + COUNT(DISTINCT ct.child_token_id) AS token_count
		 FROM collections c
		 LEFT JOIN collection_nodes cn ON cn.collection_id = c.id
		 LEFT JOIN master_collections mt ON mt.collection_id = c.id
		 LEFT JOIN child_collections ct ON ct.collection_id = c.id
		 GROUP BY c.id
		 ORDER BY c.sort_order, c.id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	collections := make([]Collection, 0)
	indexByID := map[int64]int{}
	for rows.Next() {
		collection, err := scanCollection(rows)
		if err != nil {
			return nil, err
		}
		indexByID[collection.ID] = len(collections)
		collections = append(collections, *collection)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	nodeRows, err := d.sql.QueryContext(
		ctx,
		`SELECT collection_id, node_id FROM collection_nodes ORDER BY collection_id, position, node_id`,
	)
	if err != nil {
		return nil, err
	}
	defer nodeRows.Close()
	for nodeRows.Next() {
		var collectionID, nodeID int64
		if err := nodeRows.Scan(&collectionID, &nodeID); err != nil {
			return nil, err
		}
		if index, ok := indexByID[collectionID]; ok {
			collections[index].NodeIDs = append(collections[index].NodeIDs, nodeID)
		}
	}
	return collections, nodeRows.Err()
}

func (d *DB) GetCollection(ctx context.Context, id int64) (*Collection, error) {
	var collection Collection
	var enabled int
	var created, updated string
	err := d.sql.QueryRowContext(
		ctx,
		`SELECT c.id, c.name, c.slug, c.description, c.enabled, c.sort_order, c.created_at, c.updated_at,
		        COUNT(DISTINCT cn.node_id) AS node_count,
		        COUNT(DISTINCT mt.master_token_id) + COUNT(DISTINCT ct.child_token_id) AS token_count
		 FROM collections c
		 LEFT JOIN collection_nodes cn ON cn.collection_id = c.id
		 LEFT JOIN master_collections mt ON mt.collection_id = c.id
		 LEFT JOIN child_collections ct ON ct.collection_id = c.id
		 WHERE c.id = ?
		 GROUP BY c.id`,
		id,
	).Scan(
		&collection.ID,
		&collection.Name,
		&collection.Slug,
		&collection.Description,
		&enabled,
		&collection.SortOrder,
		&created,
		&updated,
		&collection.NodeCount,
		&collection.TokenCount,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	collection.Enabled = enabled == 1
	collection.CreatedAt = parseTime(created)
	collection.UpdatedAt = parseTime(updated)
	collection.NodeIDs, err = d.collectionNodeIDs(ctx, id)
	if err != nil {
		return nil, err
	}
	return &collection, nil
}

func (d *DB) collectionNodeIDs(ctx context.Context, collectionID int64) ([]int64, error) {
	rows, err := d.sql.QueryContext(
		ctx,
		`SELECT node_id FROM collection_nodes WHERE collection_id = ? ORDER BY position, node_id`,
		collectionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (d *DB) CreateCollection(ctx context.Context, name, slug, description string, enabled bool, nodeIDs []int64) (*Collection, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("集合名称不能为空")
	}
	slug = strings.TrimSpace(slug)
	if slug == "" {
		slug = makeSlug(name, 0)
	}
	if !validSlug(slug) {
		return nil, errors.New("集合别名只能包含小写字母、数字和连字符")
	}

	var nextOrder int
	if err := d.sql.QueryRowContext(ctx, `SELECT COALESCE(MAX(sort_order), -1) + 1 FROM collections`).Scan(&nextOrder); err != nil {
		return nil, err
	}
	now := formatTime(time.Now())
	result, err := d.sql.ExecContext(
		ctx,
		`INSERT INTO collections(name, slug, description, enabled, sort_order, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?)`,
		name, slug, strings.TrimSpace(description), boolInt(enabled), nextOrder, now, now,
	)
	if err != nil {
		if isUniqueError(err) {
			return nil, errors.New("集合别名已存在")
		}
		return nil, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	if err := d.SetCollectionNodes(ctx, id, nodeIDs); err != nil {
		return nil, err
	}
	return d.GetCollection(ctx, id)
}

func (d *DB) UpdateCollection(ctx context.Context, collection Collection) (*Collection, error) {
	collection.Name = strings.TrimSpace(collection.Name)
	if collection.Name == "" {
		return nil, errors.New("集合名称不能为空")
	}
	collection.Slug = strings.TrimSpace(collection.Slug)
	if collection.Slug == "" {
		collection.Slug = makeSlug(collection.Name, collection.ID)
	}
	if !validSlug(collection.Slug) {
		return nil, errors.New("集合别名只能包含小写字母、数字和连字符")
	}
	result, err := d.sql.ExecContext(
		ctx,
		`UPDATE collections SET name = ?, slug = ?, description = ?, enabled = ?, updated_at = ? WHERE id = ?`,
		collection.Name, collection.Slug, strings.TrimSpace(collection.Description), boolInt(collection.Enabled), formatTime(time.Now()), collection.ID,
	)
	if err != nil {
		if isUniqueError(err) {
			return nil, errors.New("集合别名已存在")
		}
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return nil, ErrNotFound
	}
	if collection.NodeIDs != nil {
		if err := d.SetCollectionNodes(ctx, collection.ID, collection.NodeIDs); err != nil {
			return nil, err
		}
	}
	return d.GetCollection(ctx, collection.ID)
}

func (d *DB) DeleteCollection(ctx context.Context, id int64) error {
	result, err := d.sql.ExecContext(ctx, `DELETE FROM collections WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (d *DB) SetCollectionNodes(ctx context.Context, collectionID int64, nodeIDs []int64) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM collection_nodes WHERE collection_id = ?`, collectionID); err != nil {
		return err
	}
	for position, nodeID := range uniqueInt64s(nodeIDs) {
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO collection_nodes(collection_id, node_id, position) VALUES(?, ?, ?)`,
			collectionID, nodeID, position,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (d *DB) ResolveCollectionIDs(ctx context.Context, principal *Principal, requested []string) ([]int64, string, error) {
	collections, err := d.accessibleCollections(ctx, principal)
	if err != nil {
		return nil, "", err
	}
	if len(requested) == 0 {
		ids := make([]int64, len(collections))
		slugs := make([]string, len(collections))
		for i, collection := range collections {
			ids[i] = collection.ID
			slugs[i] = collection.Slug
		}
		return ids, strings.Join(slugs, ","), nil
	}

	requestedSet := map[string]struct{}{}
	for _, item := range requested {
		for _, part := range strings.Split(item, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				requestedSet[part] = struct{}{}
			}
		}
	}
	ids := []int64{}
	slugs := []string{}
	for _, collection := range collections {
		if _, ok := requestedSet[collection.Slug]; ok {
			ids = append(ids, collection.ID)
			slugs = append(slugs, collection.Slug)
			continue
		}
		if _, ok := requestedSet[strconv.FormatInt(collection.ID, 10)]; ok {
			ids = append(ids, collection.ID)
			slugs = append(slugs, collection.Slug)
		}
	}
	if len(ids) == 0 {
		return nil, "", ErrForbidden
	}
	return ids, strings.Join(slugs, ","), nil
}

func (d *DB) accessibleCollections(ctx context.Context, principal *Principal) ([]Collection, error) {
	collections, err := d.ListCollections(ctx)
	if err != nil {
		return nil, err
	}
	allowed := make([]Collection, 0, len(collections))
	for _, collection := range collections {
		if !collection.Enabled {
			continue
		}
		if principal.collectionAllowed(collection.ID) {
			allowed = append(allowed, collection)
		}
	}
	return allowed, nil
}

func (d *DB) NodeURIsForCollections(ctx context.Context, collectionIDs []int64) ([]string, error) {
	collectionIDs = uniqueInt64s(collectionIDs)
	if len(collectionIDs) == 0 {
		return d.ListEnabledNodeURIs(ctx)
	}
	query, args := sqlInQuery(
		`SELECT cn.collection_id, cn.position, n.id, n.uri
		 FROM collection_nodes cn
		 JOIN nodes n ON n.id = cn.node_id
		 WHERE cn.collection_id IN (%s) AND n.enabled = 1
		 ORDER BY cn.collection_id, cn.position, n.id`,
		collectionIDs,
	)
	rows, err := d.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	uris := []string{}
	seen := map[string]struct{}{}
	for rows.Next() {
		var collectionID, position, nodeID int64
		var uri string
		if err := rows.Scan(&collectionID, &position, &nodeID, &uri); err != nil {
			return nil, err
		}
		uri = strings.TrimSpace(uri)
		if uri == "" {
			continue
		}
		if _, ok := seen[uri]; ok {
			continue
		}
		seen[uri] = struct{}{}
		uris = append(uris, uri)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return uris, nil
}

func validSlug(slug string) bool {
	if slug == "" {
		return false
	}
	for _, r := range slug {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return false
	}
	return true
}
