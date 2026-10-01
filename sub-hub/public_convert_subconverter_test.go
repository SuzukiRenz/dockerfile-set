package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublicConvertUsesInternalInputForSubconverter(t *testing.T) {
	app, _ := newTestApp(t)
	appServer := httptest.NewServer(app.Handler())
	app.internalBaseURL = appServer.URL
	defer appServer.Close()
	var requestedPath string
	var requestedTarget string
	var requestedInput string
	converter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		requestedTarget = r.URL.Query().Get("target")
		inputURL := r.URL.Query().Get("url")
		response, err := http.Get(inputURL)
		if err != nil {
			t.Errorf("fetch internal input: %v", err)
			http.Error(w, "input unavailable", http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		payload, err := io.ReadAll(response.Body)
		if err != nil {
			t.Errorf("read internal input: %v", err)
			http.Error(w, "input unreadable", http.StatusBadGateway)
			return
		}
		requestedInput = string(payload)
		w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
		_, _ = fmt.Fprint(w, "proxies: []\n")
	}))
	defer converter.Close()
	app.config.SubconverterURL = converter.URL

	challengeToken, question, _ := mustPublicChallenge(t, app)
	answer := solvePublicQuestion(t, question)
	body, _ := json.Marshal(map[string]string{
		"token":  challengeToken,
		"answer": answer,
		"input":  "vless://uuid@example.com:443#Visitor",
		"target": "clash",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/public/convert", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("convert status=%d body=%s", rec.Code, rec.Body.String())
	}
	if requestedPath != "/sub" || requestedTarget != "clash" {
		t.Fatalf("converter got path=%q target=%q", requestedPath, requestedTarget)
	}
	if !strings.Contains(requestedInput, "vless://uuid@example.com:443#Visitor") {
		t.Fatalf("converter input=%q", requestedInput)
	}
	var result publicConversion
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.Text != "proxies: []" {
		t.Fatalf("converted text=%q", result.Text)
	}
}

func mustPublicChallenge(t *testing.T, app *App) (string, string, int64) {
	t.Helper()
	token, question, expiresAt, err := app.newPublicChallenge()
	if err != nil {
		t.Fatalf("newPublicChallenge: %v", err)
	}
	return token, question, expiresAt
}
