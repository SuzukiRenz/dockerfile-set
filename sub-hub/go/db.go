package main

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"
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

type Config struct {
	SubName      string `json:"sub_name"`
	SubConfigURL string `json:"subconfig_url"`
}

type store struct {
	Nodes  []Node `json:"nodes"`
	Config Config `json:"config"`
	NextID int64  `json:"next_id"`
}

// ── DB ────────────────────────────────────────────────────────────────────────

type DB struct {
	mu   sync.RWMutex
	path string
	data store
}

func initDB(path string) (*DB, error) {
	db := &DB{
		path: path,
		data: store{
			NextID: 1,
			Config: Config{SubName: "My Subscription"},
		},
	}

	b, err := os.ReadFile(path)
	if err == nil {
		json.Unmarshal(b, &db.data)
	}
	// ensure defaults
	if db.data.NextID == 0 {
		db.data.NextID = 1
	}
	return db, nil
}

func (db *DB) save() error {
	b, err := json.MarshalIndent(db.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(db.path, b, 0644)
}

// ── Node CRUD ─────────────────────────────────────────────────────────────────

func getAllNodes(db *DB) ([]Node, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	out := make([]Node, len(db.data.Nodes))
	copy(out, db.data.Nodes)
	return out, nil
}

func getEnabledNodes(db *DB) ([]Node, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	var out []Node
	for _, n := range db.data.Nodes {
		if n.Enabled {
			out = append(out, n)
		}
	}
	return out, nil
}

func createNode(db *DB, n *Node) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	n.ID = db.data.NextID
	db.data.NextID++
	n.Enabled = true
	n.SortOrder = len(db.data.Nodes)
	n.CreatedAt = time.Now()
	if n.Name == "" {
		n.Name = extractName(n.URI)
	}
	db.data.Nodes = append(db.data.Nodes, *n)
	return db.save()
}

func batchCreateNodes(db *DB, raw string) (int, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	count := 0
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		n := Node{
			ID:        db.data.NextID,
			URI:       line,
			Name:      extractName(line),
			Enabled:   true,
			SortOrder: len(db.data.Nodes),
			CreatedAt: time.Now(),
		}
		db.data.NextID++
		db.data.Nodes = append(db.data.Nodes, n)
		count++
	}
	if count > 0 {
		db.save()
	}
	return count, nil
}

func updateNode(db *DB, n *Node) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	for i, existing := range db.data.Nodes {
		if existing.ID == n.ID {
			n.CreatedAt = existing.CreatedAt
			if n.Name == "" {
				n.Name = extractName(n.URI)
			}
			db.data.Nodes[i] = *n
			return db.save()
		}
	}
	return nil
}

func deleteNode(db *DB, id int64) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	nodes := db.data.Nodes[:0]
	for _, n := range db.data.Nodes {
		if n.ID != id {
			nodes = append(nodes, n)
		}
	}
	db.data.Nodes = nodes
	return db.save()
}

func reorderNodes(db *DB, ids []int64) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	idx := make(map[int64]int, len(ids))
	for i, id := range ids {
		idx[id] = i
	}
	nodes := make([]Node, len(db.data.Nodes))
	copy(nodes, db.data.Nodes)
	for i := range nodes {
		if order, ok := idx[nodes[i].ID]; ok {
			nodes[i].SortOrder = order
		}
	}
	// stable sort by SortOrder
	for i := 1; i < len(nodes); i++ {
		for j := i; j > 0 && nodes[j].SortOrder < nodes[j-1].SortOrder; j-- {
			nodes[j], nodes[j-1] = nodes[j-1], nodes[j]
		}
	}
	db.data.Nodes = nodes
	return db.save()
}

// ── Config ────────────────────────────────────────────────────────────────────

func getConfig(db *DB, key string) (string, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	switch key {
	case "sub_name":
		return db.data.Config.SubName, nil
	case "subconfig_url":
		return db.data.Config.SubConfigURL, nil
	}
	return "", nil
}

func setConfig(db *DB, key, value string) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	switch key {
	case "sub_name":
		db.data.Config.SubName = value
	case "subconfig_url":
		db.data.Config.SubConfigURL = value
	}
	return db.save()
}

func getAllConfig(db *DB) (map[string]string, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return map[string]string{
		"sub_name":      db.data.Config.SubName,
		"subconfig_url": db.data.Config.SubConfigURL,
	}, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func extractName(uri string) string {
	if idx := strings.LastIndex(uri, "#"); idx != -1 {
		return uri[idx+1:]
	}
	return ""
}
