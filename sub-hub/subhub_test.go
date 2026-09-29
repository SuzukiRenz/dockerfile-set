package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func newTestApp(t *testing.T) (*App, *DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := OpenDB(path, "master-test-token")
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	config := Config{
		Port:             "8787",
		DBPath:           path,
		BootstrapToken:   "master-test-token",
		NodesFile:        filepath.Join(t.TempDir(), "nodes.txt"),
		SubconverterURL:  "http://127.0.0.1:25500",
		LoggerMaxRecords: 1000,
	}
	return NewApp(config, db), db
}

func createTestNode(t *testing.T, db *DB, name, uri string) *Node {
	t.Helper()
	node, err := db.CreateNode(context.Background(), name, uri, true)
	if err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	return node
}

func TestDatabaseTokenCollectionsAndUsageLimit(t *testing.T) {
	app, db := newTestApp(t)
	ctx := context.Background()
	nodeA := createTestNode(t, db, "Node A", "vless://uuid-a@example.com:443#A")
	nodeB := createTestNode(t, db, "Node B", "trojan://pass@example.org:443#B")
	collectionA, err := db.CreateCollection(ctx, "移动线路", "mobile", "phone", true, []int64{nodeA.ID})
	if err != nil {
		t.Fatalf("CreateCollection A: %v", err)
	}
	collectionB, err := db.CreateCollection(ctx, "备用线路", "backup", "backup", true, []int64{nodeB.ID})
	if err != nil {
		t.Fatalf("CreateCollection B: %v", err)
	}
	if collectionA.NodeCount != 1 || collectionB.NodeCount != 1 {
		t.Fatalf("unexpected node counts: %d %d", collectionA.NodeCount, collectionB.NodeCount)
	}

	expires := time.Now().Add(time.Hour)
	child, err := db.CreateChildToken(ctx, "Alice", true, &expires, 2, "测试", []int64{collectionA.ID})
	if err != nil {
		t.Fatalf("CreateChildToken: %v", err)
	}
	principal, err := db.Authenticate(ctx, child.Token)
	if err != nil {
		t.Fatalf("Authenticate child: %v", err)
	}
	if principal.IsMaster {
		t.Fatal("child token authenticated as master")
	}
	ids, slug, err := db.ResolveCollectionIDs(ctx, principal, nil)
	if err != nil {
		t.Fatalf("ResolveCollectionIDs: %v", err)
	}
	if len(ids) != 1 || ids[0] != collectionA.ID || slug != "mobile" {
		t.Fatalf("child resolved unexpected collections: %v %q", ids, slug)
	}
	uris, err := db.NodeURIsForCollections(ctx, ids)
	if err != nil {
		t.Fatalf("NodeURIsForCollections: %v", err)
	}
	if len(uris) != 1 || uris[0] != nodeA.URI {
		t.Fatalf("child got unexpected node URIs: %v", uris)
	}

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/sub?token="+child.Token, nil)
		req.RemoteAddr = "127.0.0.1:12345"
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d status = %d body=%s", i+1, rec.Code, rec.Body.String())
		}
		decoded, err := base64.StdEncoding.DecodeString(rec.Body.String())
		if err != nil {
			t.Fatalf("base64 decode: %v", err)
		}
		if strings.TrimSpace(string(decoded)) != nodeA.URI {
			t.Fatalf("unexpected subscription payload: %q", decoded)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/sub?token="+child.Token, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatal("usage-limited child token was accepted")
	}

	updated, err := db.GetChildToken(ctx, child.ID)
	if err != nil {
		t.Fatalf("GetChildToken: %v", err)
	}
	if updated.UseCount != 2 {
		t.Fatalf("use count = %d, want 2", updated.UseCount)
	}
}

func TestHTTPAdminNodeCollectionAndRestore(t *testing.T) {
	app, db := newTestApp(t)
	ctx := context.Background()

	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("Authorization", "Bearer master-test-token")
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		return rec
	}

	rec := request(http.MethodPost, "/api/nodes", `{"name":"香港节点","uri":"vless://uuid@example.com:443#HK"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create node status=%d body=%s", rec.Code, rec.Body.String())
	}
	var node Node
	if err := json.Unmarshal(rec.Body.Bytes(), &node); err != nil {
		t.Fatalf("decode node: %v", err)
	}

	rec = request(http.MethodPost, "/api/collections", `{"name":"香港集合","slug":"hk","description":"香港","node_ids":[`+int64String(node.ID)+`]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create collection status=%d body=%s", rec.Code, rec.Body.String())
	}
	var collection Collection
	if err := json.Unmarshal(rec.Body.Bytes(), &collection); err != nil {
		t.Fatalf("decode collection: %v", err)
	}

	rec = request(http.MethodPost, "/api/tokens/child", `{"name":"香港用户","collection_ids":[`+int64String(collection.ID)+`],"max_uses":0}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create child status=%d body=%s", rec.Code, rec.Body.String())
	}
	var child ChildToken
	if err := json.Unmarshal(rec.Body.Bytes(), &child); err != nil {
		t.Fatalf("decode child: %v", err)
	}
	if child.Token == "" || child.CollectionIDs[0] != collection.ID {
		t.Fatalf("unexpected child token: %+v", child)
	}

	rec = request(http.MethodGet, "/api/backup", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("backup status=%d body=%s", rec.Code, rec.Body.String())
	}
	var backup BackupData
	if err := json.Unmarshal(rec.Body.Bytes(), &backup); err != nil {
		t.Fatalf("decode backup: %v", err)
	}
	if len(backup.Nodes) != 1 || len(backup.Collections) != 1 || len(backup.Children) != 1 {
		t.Fatalf("unexpected backup: %+v", backup)
	}

	if err := db.ClearLogs(ctx); err != nil {
		t.Fatalf("ClearLogs: %v", err)
	}
	restoreBody, err := json.Marshal(backup)
	if err != nil {
		t.Fatalf("marshal backup: %v", err)
	}
	rec = request(http.MethodPost, "/api/restore", string(restoreBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("restore status=%d body=%s", rec.Code, rec.Body.String())
	}
	collections, err := db.ListCollections(ctx)
	if err != nil {
		t.Fatalf("ListCollections after restore: %v", err)
	}
	if len(collections) != 1 || collections[0].NodeCount != 1 {
		t.Fatalf("unexpected restored collections: %+v", collections)
	}
}

func TestChildCannotUseUnauthorizedCollection(t *testing.T) {
	_, db := newTestApp(t)
	ctx := context.Background()
	nodeA := createTestNode(t, db, "A", "vless://a@example.com:443#A")
	nodeB := createTestNode(t, db, "B", "vless://b@example.com:443#B")
	collectionA, _ := db.CreateCollection(ctx, "A", "a", "", true, []int64{nodeA.ID})
	collectionB, _ := db.CreateCollection(ctx, "B", "b", "", true, []int64{nodeB.ID})
	child, err := db.CreateChildToken(ctx, "child", true, nil, 0, "", []int64{collectionA.ID})
	if err != nil {
		t.Fatalf("CreateChildToken: %v", err)
	}
	principal, err := db.Authenticate(ctx, child.Token)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if _, _, err := db.ResolveCollectionIDs(ctx, principal, []string{collectionB.Slug}); err == nil {
		t.Fatal("child was allowed to resolve an unauthorized collection")
	}
}

func TestRawSubscriptionDecodesOriginalNodes(t *testing.T) {
	app, db := newTestApp(t)
	node := createTestNode(t, db, "raw", "trojan://password@example.com:443#Raw")
	collection, err := db.CreateCollection(context.Background(), "raw", "raw", "", true, []int64{node.ID})
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/sub?token=master-test-token&collections="+collection.Slug, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	data, err := io.ReadAll(rec.Result().Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(string(data))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if strings.TrimSpace(string(decoded)) != node.URI {
		t.Fatalf("payload=%q want=%q", decoded, node.URI)
	}
}

func int64String(value int64) string {
	return strconv.FormatInt(value, 10)
}
func TestHTTPChildTokenUpdateCanClearOptionalFields(t *testing.T) {
	app, db := newTestApp(t)
	ctx := context.Background()
	node := createTestNode(t, db, "node", "vless://uuid@example.com:443#Node")
	collection, err := db.CreateCollection(ctx, "collection", "collection", "", true, []int64{node.ID})
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	expires := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	child, err := db.CreateChildToken(ctx, "child", true, &expires, 5, "keep me", []int64{collection.ID})
	if err != nil {
		t.Fatalf("CreateChildToken: %v", err)
	}

	body := `{"name":"child","enabled":true,"collection_ids":[` + int64String(collection.ID) + `],"expires_at":null,"note":""}`
	req := httptest.NewRequest(http.MethodPatch, "/api/tokens/child/"+int64String(child.ID), strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Authorization", "Bearer master-test-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", rec.Code, rec.Body.String())
	}

	updated, err := db.GetChildToken(ctx, child.ID)
	if err != nil {
		t.Fatalf("GetChildToken: %v", err)
	}
	if updated.ExpiresAt != nil {
		t.Fatalf("expires_at = %v, want nil", updated.ExpiresAt)
	}
	if updated.Note != "" {
		t.Fatalf("note = %q, want empty", updated.Note)
	}
	if updated.MaxUses != 5 {
		t.Fatalf("max_uses = %d, want preserved value 5", updated.MaxUses)
	}
}

func TestRestoreLegacyDBJSON(t *testing.T) {
	app, db := newTestApp(t)
	ctx := context.Background()
	legacy := `{
		"nodes": [{"id":9,"name":"Legacy HK","uri":"vless://legacy@example.com:443#Legacy-HK","enabled":true,"sort_order":3,"created_at":"2026-01-02T03:04:05Z"}],
		"config": {"sub_name":"Legacy Sub","subconfig_url":"https://example.com/config.ini"},
		"next_id": 10
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/restore", strings.NewReader(legacy))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Authorization", "Bearer master-test-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("legacy restore status=%d body=%s", rec.Code, rec.Body.String())
	}
	nodes, err := db.ListNodes(ctx)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 1 || nodes[0].URI != "vless://legacy@example.com:443#Legacy-HK" {
		t.Fatalf("legacy nodes = %+v", nodes)
	}
	settings, err := db.Settings(ctx)
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	if settings["sub_name"] != "Legacy Sub" || settings["subconfig_url"] != "https://example.com/config.ini" {
		t.Fatalf("legacy settings = %+v", settings)
	}
	if _, err := db.GetMasterToken(ctx, 1); err != nil {
		t.Fatalf("existing master token was not preserved: %v", err)
	}
}
