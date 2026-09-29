package main

import "time"

type Node struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	URI       string    `json:"uri"`
	Enabled   bool      `json:"enabled"`
	SortOrder int       `json:"sort_order"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Collection struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Slug        string    `json:"slug"`
	Description string    `json:"description"`
	Enabled     bool      `json:"enabled"`
	SortOrder   int       `json:"sort_order"`
	NodeIDs     []int64   `json:"node_ids"`
	NodeCount   int       `json:"node_count"`
	TokenCount  int       `json:"token_count"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type MasterToken struct {
	ID            int64      `json:"id"`
	Name          string     `json:"name"`
	Token         string     `json:"token"`
	TokenPrefix   string     `json:"token_prefix"`
	Enabled       bool       `json:"enabled"`
	CollectionIDs []int64    `json:"collection_ids"`
	CreatedAt     time.Time  `json:"created_at"`
	LastUsedAt    *time.Time `json:"last_used_at"`
}

type ChildToken struct {
	ID            int64      `json:"id"`
	Name          string     `json:"name"`
	Token         string     `json:"token"`
	TokenPrefix   string     `json:"token_prefix"`
	Enabled       bool       `json:"enabled"`
	ExpiresAt     *time.Time `json:"expires_at"`
	MaxUses       int64      `json:"max_uses"`
	UseCount      int64      `json:"use_count"`
	Note          string     `json:"note"`
	CollectionIDs []int64    `json:"collection_ids"`
	CreatedAt     time.Time  `json:"created_at"`
	LastUsedAt    *time.Time `json:"last_used_at"`
}

type AccessLog struct {
	ID             int64     `json:"id"`
	TokenKind      string    `json:"token_kind"`
	TokenID        int64     `json:"token_id"`
	TokenName      string    `json:"token_name"`
	TokenPrefix    string    `json:"token_prefix"`
	CollectionSlug string    `json:"collection_slug"`
	Target         string    `json:"target"`
	IP             string    `json:"ip"`
	UserAgent      string    `json:"user_agent"`
	Status         int       `json:"status"`
	NodeCount      int       `json:"node_count"`
	CreatedAt      time.Time `json:"created_at"`
}

type Principal struct {
	Kind          string     `json:"kind"`
	ID            int64      `json:"id"`
	Name          string     `json:"name"`
	TokenPrefix   string     `json:"token_prefix"`
	IsMaster      bool       `json:"is_master"`
	ExpiresAt     *time.Time `json:"expires_at"`
	MaxUses       int64      `json:"max_uses"`
	UseCount      int64      `json:"use_count"`
	CollectionIDs []int64    `json:"collection_ids"`
}

type Overview struct {
	NodeCount       int   `json:"node_count"`
	EnabledNodes    int   `json:"enabled_nodes"`
	CollectionCount int   `json:"collection_count"`
	ChildCount      int   `json:"child_count"`
	ActiveChildren  int   `json:"active_children"`
	MasterCount     int   `json:"master_count"`
	RequestCount    int64 `json:"request_count"`
	TodayRequests   int64 `json:"today_requests"`
}

type BackupData struct {
	Version     int                 `json:"version"`
	ExportedAt  time.Time           `json:"exported_at"`
	NextID      int64               `json:"next_id,omitempty"` // Legacy db.json compatibility only.
	Config      *BackupLegacyConfig `json:"config,omitempty"`  // Legacy db.json compatibility only.
	Settings    map[string]string   `json:"settings"`
	Nodes       []BackupNode        `json:"nodes"`
	Collections []BackupCollection  `json:"collections"`
	Masters     []BackupMaster      `json:"masters"`
	Children    []BackupChild       `json:"children"`
}

type BackupLegacyConfig struct {
	SubName      string `json:"sub_name"`
	SubConfigURL string `json:"subconfig_url"`
}

type BackupNode struct {
	Name      string     `json:"name"`
	URI       string     `json:"uri"`
	Enabled   bool       `json:"enabled"`
	SortOrder int        `json:"sort_order"`
	CreatedAt *time.Time `json:"created_at,omitempty"`
}

type BackupCollection struct {
	Name        string   `json:"name"`
	Slug        string   `json:"slug"`
	Description string   `json:"description"`
	Enabled     bool     `json:"enabled"`
	SortOrder   int      `json:"sort_order"`
	NodeURIs    []string `json:"node_uris"`
}

type BackupMaster struct {
	Name            string   `json:"name"`
	Token           string   `json:"token"`
	Enabled         bool     `json:"enabled"`
	CollectionSlugs []string `json:"collection_slugs"`
}

type BackupChild struct {
	Name            string     `json:"name"`
	Token           string     `json:"token"`
	Enabled         bool       `json:"enabled"`
	ExpiresAt       *time.Time `json:"expires_at"`
	MaxUses         int64      `json:"max_uses"`
	UseCount        int64      `json:"use_count"`
	Note            string     `json:"note"`
	CollectionSlugs []string   `json:"collection_slugs"`
}
