package main

import (
	"embed"
	"encoding/base64"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed web/*
var webFS embed.FS

type App struct {
	config Config
	db     *DB
	client *http.Client

	publicStateMu    sync.Mutex
	internalBaseURL  string
	publicChallenges map[string]publicChallengeEntry
	publicPayloads   map[string]publicPayloadEntry
}

func NewApp(config Config, db *DB) *App {
	return &App{
		config:           config,
		db:               db,
		client:           &http.Client{Timeout: 45 * time.Second},
		publicChallenges: make(map[string]publicChallengeEntry),
		publicPayloads:   make(map[string]publicPayloadEntry),
	}
}

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", a.handleHealth)
	mux.HandleFunc("/admin", a.handleAdmin)
	mux.HandleFunc("/api/public/challenge", a.handlePublicChallenge)
	mux.HandleFunc("/api/public/convert", a.handlePublicConvert)
	mux.HandleFunc("/assets/", a.handleAsset)
	mux.HandleFunc("/api/", a.handleAPI)
	mux.HandleFunc("/sub", a.handleSubscription)
	mux.HandleFunc("/sub/", a.handleSubscription)
	mux.HandleFunc("/s/", a.handlePathSubscription)
	mux.HandleFunc("/internal/nodes", a.handleInternalNodes)
	mux.HandleFunc("/internal/public/", a.handleInternalPublicInput)
	mux.HandleFunc("/", a.handleRoot)
	return securityHeaders(requestLogger(mux))
}

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/sub") || strings.HasPrefix(r.URL.Path, "/s/") {
			log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
		}
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (a *App) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := webFS.ReadFile("web/public.html")
	if err != nil {
		http.Error(w, "public page unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func (a *App) handleAdmin(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/admin" {
		http.NotFound(w, r)
		return
	}
	data, err := webFS.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "admin page unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func (a *App) handleAsset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/assets/")
	if name == "" || strings.Contains(name, "..") {
		http.NotFound(w, r)
		return
	}
	data, err := webFS.ReadFile("web/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch {
	case strings.HasSuffix(name, ".css"):
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case strings.HasSuffix(name, ".js"):
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	default:
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	_, _ = w.Write(data)
}

func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	overview, err := a.db.Overview(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":      "ok",
		"nodes":       overview.EnabledNodes,
		"collections": overview.CollectionCount,
		"children":    overview.ChildCount,
	})
}

func (a *App) handleInternalNodes(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	collectionIDs, err := parseIDList(r.URL.Query().Get("collections"))
	if err != nil {
		http.Error(w, "invalid collections", http.StatusBadRequest)
		return
	}
	uris, err := a.db.NodeURIsForCollections(r.Context(), collectionIDs)
	if err != nil || len(uris) == 0 {
		http.Error(w, "no nodes", http.StatusNotFound)
		return
	}
	payload := strings.Join(uris, "\n")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, base64.StdEncoding.EncodeToString([]byte(payload)))
}

func isLoopbackRequest(r *http.Request) bool {
	host := r.RemoteAddr
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func (a *App) handleInternalPublicInput(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/internal/public/")
	payload, ok := a.getPublicPayload(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, payload)
}

func parseIDList(value string) ([]int64, error) {
	ids := []int64{}
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := parseInt64String(part)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return uniqueInt64s(ids), nil
}

func (a *App) handleSubscription(w http.ResponseWriter, r *http.Request) {
	principal, err := a.authenticate(r)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	a.serveSubscription(w, r, principal, nil, "sub")
}

func (a *App) handlePathSubscription(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/s/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "Token 无效或缺失"})
		return
	}
	token := strings.TrimSpace(parts[0])
	collectionPath := ""
	if len(parts) > 1 {
		collectionPath = strings.TrimSpace(parts[1])
	}
	principal, err := a.db.Authenticate(r.Context(), token)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	a.serveSubscription(w, r, principal, splitList(collectionPath), "path")
}

func (a *App) buildInternalURL(collectionIDs []int64, token string) string {
	base := "http://127.0.0.1:" + a.config.Port + "/internal/nodes?collections=" + url.QueryEscape(joinIDs(collectionIDs))
	if token != "" {
		base += "&token=" + url.QueryEscape(token)
	}
	return base
}

func splitList(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func joinIDs(ids []int64) string {
	if len(ids) == 0 {
		return ""
	}
	values := make([]string, len(ids))
	for i, id := range ids {
		values[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(values, ",")
}
