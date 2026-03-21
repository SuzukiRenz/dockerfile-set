package main

import (
	"context"
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
	"strings"
	"syscall"
	"time"
)

// ─── Config ───────────────────────────────────────────────────────────────────

var cfg = struct {
	Token         string
	Port          string
	SubName       string
	SubConverterURL string
	NodesFile     string
}{
	Token:         envOr("TOKEN", "change-me-please"),
	Port:          envOr("PORT", "8080"),
	SubName:       envOr("SUB_NAME", "My Subscription"),
	SubConverterURL: envOr("SUBCONVERTER_URL", "http://127.0.0.1:25500"),
	NodesFile:     envOr("NODES_FILE", "/data/nodes.txt"),
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ─── Node Loading ─────────────────────────────────────────────────────────────

// getNodes returns valid proxy lines from env NODES or the nodes file.
// Lines beginning with '#' or empty lines are ignored.
func getNodes() []string {
	var raw string

	if v := os.Getenv("NODES"); v != "" {
		raw = v
	} else {
		b, err := os.ReadFile(cfg.NodesFile)
		if err == nil {
			raw = string(b)
		}
	}

	var nodes []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		nodes = append(nodes, line)
	}
	return nodes
}

// ─── Subconverter Helper ───────────────────────────────────────────────────────

// subconverterReady probes subconverter's version endpoint until it responds
// or the deadline is exceeded.
func waitForSubconverter(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(cfg.SubConverterURL + "/version")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 500 {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("subconverter did not become ready within %s", timeout)
}

// callSubconverter forwards the conversion request to the internal subconverter
// service and streams the response back to the caller.
func callSubconverter(w http.ResponseWriter, r *http.Request, nodesURL string) {
	// Allowed passthrough query params from the client
	passthrough := []string{
		"target", "udp", "tfo", "scv", "fdn", "expand",
		"classic", "list", "sort", "new_name", "filename",
		"interval", "rename", "exclude", "include",
		"groups", "ruleset", "config",
	}

	q := url.Values{}
	q.Set("url", nodesURL)
	q.Set("insert", "false")

	for _, p := range passthrough {
		if v := r.URL.Query().Get(p); v != "" {
			q.Set(p, v)
		}
	}

	convURL := cfg.SubConverterURL + "/sub?" + q.Encode()

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(convURL)
	if err != nil {
		log.Printf("[ERR] subconverter: %v", err)
		http.Error(w, "subscription converter unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Copy headers from subconverter (Content-Type, Content-Disposition, etc.)
	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	if w.Header().Get("Content-Disposition") == "" {
		w.Header().Set("Content-Disposition",
			`attachment; filename="`+url.PathEscape(cfg.SubName)+`"`)
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// ─── Middleware ───────────────────────────────────────────────────────────────

func withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("token")
		if token == "" {
			auth := r.Header.Get("Authorization")
			token = strings.TrimPrefix(auth, "Bearer ")
		}
		if token != cfg.Token {
			http.Error(w, "401 unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// localhostOnly rejects requests that do not originate from the loopback
// interface — used to protect internal endpoints from external access.
func localhostOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil || (host != "127.0.0.1" && host != "::1") {
			http.Error(w, "403 forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// ─── Handlers ────────────────────────────────────────────────────────────────

// internalNodesHandler serves a base64-encoded subscription for subconverter
// to fetch internally. Only reachable from loopback.
func internalNodesHandler(w http.ResponseWriter, r *http.Request) {
	nodes := getNodes()
	if len(nodes) == 0 {
		http.Error(w, "no nodes configured", http.StatusNotFound)
		return
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(strings.Join(nodes, "\n")))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, encoded)
}

// subHandler is the main subscription endpoint.
//
//   GET /?token=TOKEN                      → raw base64 (v2rayN / xray clients)
//   GET /?token=TOKEN&target=clash         → Clash YAML via subconverter
//   GET /?token=TOKEN&target=surge         → Surge conf via subconverter
//   GET /sub?token=TOKEN&target=singbox    → SingBox JSON via subconverter
//
// Additional subconverter parameters (udp, tfo, scv, exclude, include …)
// are forwarded transparently.
func subHandler(w http.ResponseWriter, r *http.Request) {
	nodes := getNodes()
	if len(nodes) == 0 {
		http.Error(w, "no nodes configured – add nodes to NODES env var or /data/nodes.txt", http.StatusNotFound)
		return
	}

	target := r.URL.Query().Get("target")

	// No target → raw base64 for V2RayN / Xray / Nekoray etc.
	if target == "" {
		encoded := base64.StdEncoding.EncodeToString([]byte(strings.Join(nodes, "\n")))
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Profile-Update-Interval", "24")
		w.Header().Set("Subscription-Userinfo",
			"upload=0; download=0; total=107374182400; expire=4102329600")
		w.Header().Set("Content-Disposition",
			`attachment; filename="`+url.PathEscape(cfg.SubName)+`"`)
		fmt.Fprint(w, encoded)
		return
	}

	// Ask subconverter to convert by having it fetch our internal endpoint.
	nodesURL := fmt.Sprintf("http://127.0.0.1:%s/internal/nodes", cfg.Port)
	callSubconverter(w, r, nodesURL)
}

// healthHandler exposes a minimal health check.
func healthHandler(w http.ResponseWriter, r *http.Request) {
	nodes := getNodes()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
		"nodes":  len(nodes),
	})
}

// ─── Main ─────────────────────────────────────────────────────────────────────

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[sub-server] ")

	log.Printf("waiting for subconverter to be ready …")
	if err := waitForSubconverter(60 * time.Second); err != nil {
		log.Printf("warning: %v (will retry per request)", err)
	} else {
		log.Printf("subconverter is ready")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler)
	mux.HandleFunc("/internal/nodes", localhostOnly(internalNodesHandler))
	mux.HandleFunc("/sub", withAuth(subHandler))
	mux.HandleFunc("/", withAuth(subHandler))

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		log.Printf("listening on :%s", cfg.Port)
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Printf("shutting down …")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
	log.Printf("bye")
}
