package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestRootServesPublicConverterWithoutRedirect(t *testing.T) {
	app, _ := newTestApp(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("root status=%d body=%s", rec.Code, rec.Body.String())
	}
	if location := rec.Header().Get("Location"); location != "" {
		t.Fatalf("root unexpectedly redirects to %q", location)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "admin-entry") || !strings.Contains(body, "converter-form") {
		t.Fatalf("root did not serve the public converter: %s", body)
	}
}

func TestPublicChallengeIsSingleUse(t *testing.T) {
	app, _ := newTestApp(t)
	req := httptest.NewRequest(http.MethodPost, "/api/public/challenge", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	var challenge struct {
		Token    string `json:"token"`
		Question string `json:"question"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &challenge); err != nil {
		t.Fatalf("decode challenge: %v", err)
	}
	answer := solvePublicQuestion(t, challenge.Question)
	convert := func() *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{
			"token":  challenge.Token,
			"answer": answer,
			"input":  "vless://uuid@example.com:443#Visitor",
		})
		req := httptest.NewRequest(http.MethodPost, "/api/public/convert", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		out := httptest.NewRecorder()
		app.Handler().ServeHTTP(out, req)
		return out
	}
	if first := convert(); first.Code != http.StatusOK {
		t.Fatalf("first conversion status=%d body=%s", first.Code, first.Body.String())
	}
	if second := convert(); second.Code != http.StatusBadRequest {
		t.Fatalf("reused challenge status=%d body=%s", second.Code, second.Body.String())
	}
}

func TestPublicConvertChallengeAndBase64(t *testing.T) {
	app, _ := newTestApp(t)
	req := httptest.NewRequest(http.MethodPost, "/api/public/challenge", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("challenge status=%d body=%s", rec.Code, rec.Body.String())
	}
	var challenge struct {
		Token    string `json:"token"`
		Question string `json:"question"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &challenge); err != nil {
		t.Fatalf("decode challenge: %v", err)
	}
	if challenge.Token == "" || !strings.Contains(challenge.Question, "=") {
		t.Fatalf("unexpected challenge: %#v", challenge)
	}

	answer := solvePublicQuestion(t, challenge.Question)
	body, _ := json.Marshal(map[string]string{
		"token":  challenge.Token,
		"answer": answer,
		"input":  "vless://uuid@example.com:443#Visitor",
	})
	req = httptest.NewRequest(http.MethodPost, "/api/public/convert", strings.NewReader(string(body)))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("convert status=%d body=%s", rec.Code, rec.Body.String())
	}
	var result publicConversion
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode conversion: %v", err)
	}
	if result.Count != 1 || len(result.Nodes) != 1 {
		t.Fatalf("unexpected conversion: %#v", result)
	}
	decoded, err := base64.StdEncoding.DecodeString(result.Base64)
	if err != nil {
		t.Fatalf("decode result base64: %v", err)
	}
	if string(decoded) != result.Nodes[0] {
		t.Fatalf("base64=%q nodes=%q", decoded, result.Nodes)
	}
}

func TestPublicConvertRejectsWrongAnswerWithoutTouchingNodes(t *testing.T) {
	app, db := newTestApp(t)
	req := httptest.NewRequest(http.MethodPost, "/api/public/challenge", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	var challenge struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &challenge); err != nil {
		t.Fatalf("decode challenge: %v", err)
	}
	body, _ := json.Marshal(map[string]string{
		"token":  challenge.Token,
		"answer": "999999",
		"input":  "vless://uuid@example.com:443#Visitor",
	})
	req = httptest.NewRequest(http.MethodPost, "/api/public/convert", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("wrong answer status=%d body=%s", rec.Code, rec.Body.String())
	}
	nodes, err := db.ListNodes(context.Background())
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("public conversion wrote %d nodes into the database", len(nodes))
	}
}

func solvePublicQuestion(t *testing.T, question string) string {
	t.Helper()
	parts := strings.Fields(question)
	if len(parts) != 5 || parts[3] != "=" || parts[4] != "?" {
		t.Fatalf("unsupported question %q", question)
	}
	var left, right int
	if _, err := fmt.Sscanf(parts[0], "%d", &left); err != nil {
		t.Fatalf("parse left: %v", err)
	}
	if _, err := fmt.Sscanf(parts[2], "%d", &right); err != nil {
		t.Fatalf("parse right: %v", err)
	}
	switch parts[1] {
	case "+":
		return strconv.Itoa(left + right)
	case "-":
		return strconv.Itoa(left - right)
	default:
		t.Fatalf("unsupported operator %q", parts[1])
		return ""
	}
}
