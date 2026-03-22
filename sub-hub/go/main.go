package main

import (
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

//go:embed web
var webFS embed.FS

// ── App ───────────────────────────────────────────────────────────────────────

type App struct {
	db    *DB
	token string
	guest string
	port  string
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// ── Auth ──────────────────────────────────────────────────────────────────────

func extractToken(r *http.Request) string {
	if t := r.URL.Query().Get("token"); t != "" {
		return t
	}
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

func (a *App) adminOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if extractToken(r) != a.token {
			w.Header().Set("Content-Type", "application/json")
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (a *App) subAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t := extractToken(r)
		if t == a.token || (a.guest != "" && t == a.guest) {
			next(w, r)
			return
		}
		http.Error(w, "401 unauthorized", http.StatusUnauthorized)
	}
}

func loopbackOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil || (h != "127.0.0.1" && h != "::1") {
			http.Error(w, "403 forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// ── Subconverter probe ────────────────────────────────────────────────────────

func waitForSubconverter(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:25500", time.Second)
		if err == nil {
			conn.Close()
			log.Println("subconverter ready")
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	log.Println("warning: subconverter not ready – will retry per request")
}

// ── Handlers ──────────────────────────────────────────────────────────────────

func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	nodes, _ := getEnabledNodes(a.db)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"status": "ok", "nodes": len(nodes)})
}

func (a *App) handleInternalNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := getEnabledNodes(a.db)
	if err != nil || len(nodes) == 0 {
		http.Error(w, "no nodes", http.StatusNotFound)
		return
	}
	uris := make([]string, 0, len(nodes))
	for _, n := range nodes {
		uris = append(uris, n.URI)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, base64.StdEncoding.EncodeToString([]byte(strings.Join(uris, "\n"))))
}

func (a *App) handleSub(w http.ResponseWriter, r *http.Request) {
	nodes, err := getEnabledNodes(a.db)
	if err != nil || len(nodes) == 0 {
		http.Error(w, "no nodes configured", http.StatusNotFound)
		return
	}

	subName, _ := getConfig(a.db, "sub_name")
	if subName == "" {
		subName = "subscription"
	}
	subconfig, _ := getConfig(a.db, "subconfig_url")
	target := r.URL.Query().Get("target")

	// Raw base64
	if target == "" {
		uris := make([]string, 0, len(nodes))
		for _, n := range nodes {
			uris = append(uris, n.URI)
		}
		encoded := base64.StdEncoding.EncodeToString([]byte(strings.Join(uris, "\n")))
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Profile-Update-Interval", "24")
		w.Header().Set("Subscription-Userinfo",
			"upload=0; download=0; total=107374182400; expire=4102329600")
		w.Header().Set("Content-Disposition",
			`attachment; filename="`+url.PathEscape(subName)+`"`)
		fmt.Fprint(w, encoded)
		return
	}

	// Forward to subconverter
	nodesURL := fmt.Sprintf("http://127.0.0.1:%s/internal/nodes", a.port)
	q := url.Values{}
	q.Set("url", nodesURL)
	q.Set("insert", "false")
	if subconfig != "" {
		q.Set("config", subconfig)
	}
	for _, p := range []string{
		"target", "udp", "tfo", "scv", "fdn", "expand",
		"classic", "list", "sort", "new_name", "filename",
		"interval", "rename", "exclude", "include",
		"groups", "ruleset", "config", "ver",
	} {
		if v := r.URL.Query().Get(p); v != "" {
			q.Set(p, v)
		}
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get("http://127.0.0.1:25500/sub?" + q.Encode())
	if err != nil {
		log.Printf("subconverter error: %v", err)
		http.Error(w, "subscription converter unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	if w.Header().Get("Content-Disposition") == "" {
		w.Header().Set("Content-Disposition",
			`attachment; filename="`+url.PathEscape(subName)+`"`)
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func (a *App) handleAdmin(w http.ResponseWriter, r *http.Request) {
	data, err := webFS.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

// ── REST API ──────────────────────────────────────────────────────────────────

func (a *App) apiNodes(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	sub := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/nodes"), "/")

	switch {
	case sub == "":
		switch r.Method {
		case http.MethodGet:
			nodes, err := getAllNodes(a.db)
			if err != nil {
				jsonErr(w, err.Error(), 500)
				return
			}
			json.NewEncoder(w).Encode(nodes)
		case http.MethodPost:
			var n Node
			if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
				jsonErr(w, "invalid json", 400)
				return
			}
			n.URI = strings.TrimSpace(n.URI)
			if n.URI == "" {
				jsonErr(w, "uri is required", 400)
				return
			}
			if err := createNode(a.db, &n); err != nil {
				jsonErr(w, err.Error(), 500)
				return
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(n)
		default:
			http.Error(w, "method not allowed", 405)
		}

	case sub == "/batch" && r.Method == http.MethodPost:
		var req struct {
			URIs string `json:"uris"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonErr(w, "invalid json", 400)
			return
		}
		count, err := batchCreateNodes(a.db, req.URIs)
		if err != nil {
			jsonErr(w, err.Error(), 500)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"imported": count})

	case sub == "/reorder" && r.Method == http.MethodPost:
		var ids []int64
		if err := json.NewDecoder(r.Body).Decode(&ids); err != nil {
			jsonErr(w, "invalid json", 400)
			return
		}
		if err := reorderNodes(a.db, ids); err != nil {
			jsonErr(w, err.Error(), 500)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true})

	default:
		idStr := strings.TrimPrefix(sub, "/")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			jsonErr(w, "invalid node id", 400)
			return
		}
		switch r.Method {
		case http.MethodPut:
			var n Node
			if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
				jsonErr(w, "invalid json", 400)
				return
			}
			n.ID = id
			n.URI = strings.TrimSpace(n.URI)
			if n.URI == "" {
				jsonErr(w, "uri is required", 400)
				return
			}
			if err := updateNode(a.db, &n); err != nil {
				jsonErr(w, err.Error(), 500)
				return
			}
			json.NewEncoder(w).Encode(n)
		case http.MethodDelete:
			if err := deleteNode(a.db, id); err != nil {
				jsonErr(w, err.Error(), 500)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.Error(w, "method not allowed", 405)
		}
	}
}

func (a *App) apiConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		cfg, err := getAllConfig(a.db)
		if err != nil {
			jsonErr(w, err.Error(), 500)
			return
		}
		json.NewEncoder(w).Encode(cfg)
	case http.MethodPut:
		var updates map[string]string
		if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
			jsonErr(w, "invalid json", 400)
			return
		}
		allowed := map[string]bool{"sub_name": true, "subconfig_url": true}
		for k, v := range updates {
			if allowed[k] {
				setConfig(a.db, k, v)
			}
		}
		cfg, _ := getAllConfig(a.db)
		json.NewEncoder(w).Encode(cfg)
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func jsonErr(w http.ResponseWriter, msg string, code int) {
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[sub-server] ")

	app := &App{
		token: envOr("TOKEN", "change-me"),
		guest: os.Getenv("GUEST"),
		port:  envOr("PORT", "8787"),
	}
	if app.token == "change-me" {
		log.Println("WARNING: TOKEN is default – please change it")
	}

	db, err := initDB(envOr("DB_PATH", "/data/db.json"))
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	app.db = db

	log.Println("waiting for subconverter…")
	waitForSubconverter(60 * time.Second)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", app.handleHealth)
	mux.HandleFunc("/internal/nodes", loopbackOnly(app.handleInternalNodes))
	mux.HandleFunc("/sub", app.subAuth(app.handleSub))
	mux.HandleFunc("/", app.subAuth(app.handleSub))
	mux.HandleFunc("/admin", app.handleAdmin)
	mux.HandleFunc("/api/nodes", app.adminOnly(app.apiNodes))
	mux.HandleFunc("/api/nodes/", app.adminOnly(app.apiNodes))
	mux.HandleFunc("/api/config", app.adminOnly(app.apiConfig))

	srv := &http.Server{
		Addr:         ":" + app.port,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		log.Printf("listening on :%s", app.port)
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("shutting down…")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
	log.Println("bye")
}
