package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type optionalTime struct {
	Set   bool
	Value *time.Time
}

func (value *optionalTime) UnmarshalJSON(data []byte) error {
	var parsed *time.Time
	if err := json.Unmarshal(data, &parsed); err != nil {
		return err
	}
	value.Set = true
	value.Value = parsed
	return nil
}

type optionalString struct {
	Set   bool
	Value string
}

func (value *optionalString) UnmarshalJSON(data []byte) error {
	if err := json.Unmarshal(data, &value.Value); err != nil {
		return err
	}
	value.Set = true
	return nil
}

type tokenRequest struct {
	Name          string         `json:"name"`
	Enabled       *bool          `json:"enabled"`
	CollectionIDs []int64        `json:"collection_ids"`
	ExpiresAt     optionalTime   `json:"expires_at"`
	MaxUses       *int64         `json:"max_uses"`
	Note          optionalString `json:"note"`
}

func (a *App) handleAPI(w http.ResponseWriter, r *http.Request) {
	principal, err := a.authenticate(r)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	if !principal.IsMaster {
		if r.URL.Path == "/api/me" && r.Method == http.MethodGet {
			a.handleMe(w, r, principal)
			return
		}
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "仅主 Token 可访问管理接口"})
		return
	}
	pathValue := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/"), "/")

	switch {
	case pathValue == "me" && r.Method == http.MethodGet:
		a.handleMe(w, r, principal)
	case pathValue == "overview" && r.Method == http.MethodGet:
		a.handleOverview(w, r)
	case pathValue == "settings":
		a.handleSettings(w, r)
	case pathValue == "nodes":
		a.handleNodes(w, r)
	case pathValue == "nodes/batch" && r.Method == http.MethodPost:
		a.handleNodesBatch(w, r)
	case pathValue == "nodes/reorder" && r.Method == http.MethodPost:
		a.handleNodesReorder(w, r)
	case pathValue == "nodes/bulk" && r.Method == http.MethodPost:
		a.handleNodesBulk(w, r)
	case strings.HasPrefix(pathValue, "nodes/"):
		a.handleNode(w, r, strings.TrimPrefix(pathValue, "nodes/"))
	case pathValue == "collections":
		a.handleCollections(w, r)
	case strings.HasPrefix(pathValue, "collections/"):
		a.handleCollection(w, r, strings.TrimPrefix(pathValue, "collections/"))
	case pathValue == "tokens" && r.Method == http.MethodGet:
		a.handleTokens(w, r)
	case pathValue == "tokens/master" && r.Method == http.MethodPost:
		a.handleCreateMaster(w, r)
	case strings.HasPrefix(pathValue, "tokens/master/"):
		a.handleMasterToken(w, r, strings.TrimPrefix(pathValue, "tokens/master/"))
	case pathValue == "tokens/child" && r.Method == http.MethodPost:
		a.handleCreateChild(w, r)
	case strings.HasPrefix(pathValue, "tokens/child/"):
		a.handleChildToken(w, r, strings.TrimPrefix(pathValue, "tokens/child/"))
	case pathValue == "logs":
		a.handleLogs(w, r)
	case pathValue == "backup" && r.Method == http.MethodGet:
		a.handleBackup(w, r)
	case pathValue == "restore" && r.Method == http.MethodPost:
		a.handleRestore(w, r)
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "接口不存在"})
	}
}

func (a *App) handleMe(w http.ResponseWriter, r *http.Request, principal *Principal) {
	collections, err := a.db.accessibleCollections(r.Context(), principal)
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":       principal,
		"collections": collections,
	})
}

func (a *App) handleOverview(w http.ResponseWriter, r *http.Request) {
	overview, err := a.db.Overview(r.Context())
	if err != nil {
		writeDBError(w, err)
		return
	}
	logs, err := a.db.ListLogs(r.Context(), 20)
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"overview": overview,
		"logs":     logs,
	})
}

func (a *App) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		settings, err := a.db.Settings(r.Context())
		if err != nil {
			writeDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, settings)
	case http.MethodPut, http.MethodPatch:
		var updates map[string]string
		if err := decodeJSON(r, &updates, 1<<20); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		allowed := map[string]bool{
			"sub_name":        true,
			"subconfig_url":   true,
			"update_interval": true,
			"userinfo_header": true,
			"legacy_fallback": true,
		}
		filtered := map[string]string{}
		for key, value := range updates {
			if allowed[key] {
				filtered[key] = strings.TrimSpace(value)
			}
		}
		if err := a.db.UpdateSettings(r.Context(), filtered); err != nil {
			writeDBError(w, err)
			return
		}
		settings, _ := a.db.Settings(r.Context())
		writeJSON(w, http.StatusOK, settings)
	default:
		methodNotAllowed(w)
	}
}

func (a *App) handleNodes(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		nodes, err := a.db.ListNodes(r.Context())
		if err != nil {
			writeDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, nodes)
	case http.MethodPost:
		var request struct {
			Name    string `json:"name"`
			URI     string `json:"uri"`
			Enabled *bool  `json:"enabled"`
		}
		if err := decodeJSON(r, &request, 2<<20); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		enabled := true
		if request.Enabled != nil {
			enabled = *request.Enabled
		}
		node, err := a.db.CreateNode(r.Context(), request.Name, request.URI, enabled)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, node)
	default:
		methodNotAllowed(w)
	}
}

func (a *App) handleNode(w http.ResponseWriter, r *http.Request, rawID string) {
	id, err := parseInt64(rawID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	switch r.Method {
	case http.MethodGet:
		node, err := a.db.GetNode(r.Context(), id)
		if err != nil {
			writeDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, node)
	case http.MethodPut:
		var request struct {
			Name    string `json:"name"`
			URI     string `json:"uri"`
			Enabled *bool  `json:"enabled"`
		}
		if err := decodeJSON(r, &request, 2<<20); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		existing, err := a.db.GetNode(r.Context(), id)
		if err != nil {
			writeDBError(w, err)
			return
		}
		existing.Name = request.Name
		existing.URI = request.URI
		if request.Enabled != nil {
			existing.Enabled = *request.Enabled
		}
		node, err := a.db.UpdateNode(r.Context(), *existing)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, node)
	case http.MethodPatch:
		var updates map[string]any
		if err := decodeJSON(r, &updates, 1<<20); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		node, err := a.db.PatchNode(r.Context(), id, updates)
		if err != nil {
			writeDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, node)
	case http.MethodDelete:
		if err := a.db.DeleteNode(r.Context(), id); err != nil {
			writeDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		methodNotAllowed(w)
	}
}

func (a *App) handleNodesBatch(w http.ResponseWriter, r *http.Request) {
	var request struct {
		URIs string `json:"uris"`
	}
	if err := decodeJSON(r, &request, 8<<20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	result, err := a.db.BatchCreateNodes(r.Context(), request.URIs)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *App) handleNodesReorder(w http.ResponseWriter, r *http.Request) {
	var ids []int64
	if err := decodeJSON(r, &ids, 2<<20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if err := a.db.ReorderNodes(r.Context(), ids); err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *App) handleNodesBulk(w http.ResponseWriter, r *http.Request) {
	var request struct {
		IDs    []int64 `json:"ids"`
		Action string  `json:"action"`
	}
	if err := decodeJSON(r, &request, 2<<20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	count, err := a.db.BulkNodes(r.Context(), request.IDs, request.Action)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"affected": count})
}

func (a *App) handleCollections(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		collections, err := a.db.ListCollections(r.Context())
		if err != nil {
			writeDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, collections)
	case http.MethodPost:
		var request struct {
			Name        string  `json:"name"`
			Slug        string  `json:"slug"`
			Description string  `json:"description"`
			Enabled     *bool   `json:"enabled"`
			NodeIDs     []int64 `json:"node_ids"`
		}
		if err := decodeJSON(r, &request, 2<<20); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		enabled := true
		if request.Enabled != nil {
			enabled = *request.Enabled
		}
		collection, err := a.db.CreateCollection(r.Context(), request.Name, request.Slug, request.Description, enabled, request.NodeIDs)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, collection)
	default:
		methodNotAllowed(w)
	}
}

func (a *App) handleCollection(w http.ResponseWriter, r *http.Request, rawID string) {
	id, err := parseInt64(rawID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	switch r.Method {
	case http.MethodGet:
		collection, err := a.db.GetCollection(r.Context(), id)
		if err != nil {
			writeDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, collection)
	case http.MethodPut:
		var request struct {
			Name        string  `json:"name"`
			Slug        string  `json:"slug"`
			Description string  `json:"description"`
			Enabled     *bool   `json:"enabled"`
			NodeIDs     []int64 `json:"node_ids"`
		}
		if err := decodeJSON(r, &request, 2<<20); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		existing, err := a.db.GetCollection(r.Context(), id)
		if err != nil {
			writeDBError(w, err)
			return
		}
		existing.Name = request.Name
		existing.Slug = request.Slug
		existing.Description = request.Description
		existing.NodeIDs = request.NodeIDs
		if request.Enabled != nil {
			existing.Enabled = *request.Enabled
		}
		collection, err := a.db.UpdateCollection(r.Context(), *existing)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, collection)
	case http.MethodPatch:
		var updates map[string]any
		if err := decodeJSON(r, &updates, 2<<20); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		existing, err := a.db.GetCollection(r.Context(), id)
		if err != nil {
			writeDBError(w, err)
			return
		}
		if value, ok := updates["name"].(string); ok {
			existing.Name = value
		}
		if value, ok := updates["slug"].(string); ok {
			existing.Slug = value
		}
		if value, ok := updates["description"].(string); ok {
			existing.Description = value
		}
		if value, ok := updates["enabled"].(bool); ok {
			existing.Enabled = value
		}
		if value, ok := updates["node_ids"].([]any); ok {
			existing.NodeIDs = int64SliceFromJSON(value)
		}
		collection, err := a.db.UpdateCollection(r.Context(), *existing)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, collection)
	case http.MethodDelete:
		if err := a.db.DeleteCollection(r.Context(), id); err != nil {
			writeDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		methodNotAllowed(w)
	}
}

func (a *App) handleTokens(w http.ResponseWriter, r *http.Request) {
	masters, err := a.db.ListMasterTokens(r.Context())
	if err != nil {
		writeDBError(w, err)
		return
	}
	children, err := a.db.ListChildTokens(r.Context())
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"masters":  masters,
		"children": children,
	})
}

func (a *App) handleCreateMaster(w http.ResponseWriter, r *http.Request) {
	var request tokenRequest
	if err := decodeJSON(r, &request, 1<<20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	enabled := true
	if request.Enabled != nil {
		enabled = *request.Enabled
	}
	token, err := a.db.CreateMasterToken(r.Context(), request.Name, enabled, request.CollectionIDs)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, token)
}

func (a *App) handleMasterToken(w http.ResponseWriter, r *http.Request, rest string) {
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	id, err := parseInt64(parts[0])
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if len(parts) > 1 && parts[1] == "regenerate" {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		if err := a.db.RegenerateToken(r.Context(), "master", id); err != nil {
			writeDBError(w, err)
			return
		}
		token, _ := a.db.GetMasterToken(r.Context(), id)
		writeJSON(w, http.StatusOK, token)
		return
	}
	switch r.Method {
	case http.MethodPut, http.MethodPatch:
		var request tokenRequest
		if err := decodeJSON(r, &request, 1<<20); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		existing, err := a.db.GetMasterToken(r.Context(), id)
		if err != nil {
			writeDBError(w, err)
			return
		}
		name := request.Name
		if name == "" {
			name = existing.Name
		}
		enabled := existing.Enabled
		if request.Enabled != nil {
			enabled = *request.Enabled
		}
		collectionIDs := existing.CollectionIDs
		if request.CollectionIDs != nil {
			collectionIDs = request.CollectionIDs
		}
		token, err := a.db.UpdateMasterToken(r.Context(), id, name, enabled, collectionIDs)
		if err != nil {
			writeDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, token)
	case http.MethodDelete:
		if err := a.db.DeleteToken(r.Context(), "master", id); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		methodNotAllowed(w)
	}
}

func (a *App) handleCreateChild(w http.ResponseWriter, r *http.Request) {
	var request tokenRequest
	if err := decodeJSON(r, &request, 1<<20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	enabled := true
	if request.Enabled != nil {
		enabled = *request.Enabled
	}
	maxUses := int64(0)
	if request.MaxUses != nil {
		maxUses = *request.MaxUses
	}
	var expiresAt *time.Time
	if request.ExpiresAt.Set {
		expiresAt = request.ExpiresAt.Value
	}
	note := ""
	if request.Note.Set {
		note = request.Note.Value
	}
	token, err := a.db.CreateChildToken(r.Context(), request.Name, enabled, expiresAt, maxUses, note, request.CollectionIDs)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, token)
}

func (a *App) handleChildToken(w http.ResponseWriter, r *http.Request, rest string) {
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	id, err := parseInt64(parts[0])
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if len(parts) > 1 && parts[1] == "regenerate" {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		if err := a.db.RegenerateToken(r.Context(), "child", id); err != nil {
			writeDBError(w, err)
			return
		}
		token, _ := a.db.GetChildToken(r.Context(), id)
		writeJSON(w, http.StatusOK, token)
		return
	}
	switch r.Method {
	case http.MethodPut, http.MethodPatch:
		var request tokenRequest
		if err := decodeJSON(r, &request, 1<<20); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		existing, err := a.db.GetChildToken(r.Context(), id)
		if err != nil {
			writeDBError(w, err)
			return
		}
		name := request.Name
		if name == "" {
			name = existing.Name
		}
		enabled := existing.Enabled
		if request.Enabled != nil {
			enabled = *request.Enabled
		}
		expiresAt := existing.ExpiresAt
		if request.ExpiresAt.Set {
			expiresAt = request.ExpiresAt.Value
		}
		maxUses := existing.MaxUses
		if request.MaxUses != nil {
			maxUses = *request.MaxUses
		}
		note := existing.Note
		if request.Note.Set {
			note = request.Note.Value
		}
		collectionIDs := existing.CollectionIDs
		if request.CollectionIDs != nil {
			collectionIDs = request.CollectionIDs
		}
		token, err := a.db.UpdateChildToken(r.Context(), id, name, enabled, expiresAt, maxUses, note, collectionIDs)
		if err != nil {
			writeDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, token)
	case http.MethodDelete:
		if err := a.db.DeleteToken(r.Context(), "child", id); err != nil {
			writeDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		methodNotAllowed(w)
	}
}

func (a *App) handleLogs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		logs, err := a.db.ListLogs(r.Context(), limit)
		if err != nil {
			writeDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, logs)
	case http.MethodDelete:
		if err := a.db.ClearLogs(r.Context()); err != nil {
			writeDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		methodNotAllowed(w)
	}
}

func (a *App) handleBackup(w http.ResponseWriter, r *http.Request) {
	backup, err := a.db.Backup(r.Context())
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, backup)
}

func (a *App) handleRestore(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "读取备份失败: " + err.Error()})
		return
	}
	var backup BackupData
	if err := json.Unmarshal(data, &backup); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "备份 JSON 无效: " + err.Error()})
		return
	}
	if err := a.db.Restore(r.Context(), &backup); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
