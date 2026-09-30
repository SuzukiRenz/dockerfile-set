package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestParseNodeInputShadowrocketJSON(t *testing.T) {
	raw := `[
		{
			"type": "vmess",
			"remarks": "HK",
			"server": "hk.example.com",
			"server_port": 443,
			"uuid": "11111111-1111-1111-1111-111111111111",
			"alterId": 0,
			"network": "ws",
			"path": "/ws",
			"requestHost": "hk.example.com",
			"tls": true
		},
		{
			"type": "vless",
			"remarks": "JP",
			"server": "jp.example.com",
			"server_port": 8443,
			"uuid": "22222222-2222-2222-2222-222222222222",
			"network": "grpc",
			"path": "edge",
			"tls": true,
			"sni": "jp.example.com"
		},
		{
			"type": "trojan",
			"remarks": "SG",
			"server": "sg.example.com",
			"server_port": 443,
			"password": "secret",
			"network": "ws",
			"path": "/tr",
			"requestHost": "sg.example.com",
			"tls": true
		}
	]`

	result := parseNodeInput(raw)
	if result.Format != "shadowrocket-json" {
		t.Fatalf("format = %q, want shadowrocket-json", result.Format)
	}
	if len(result.URIs) != 3 {
		t.Fatalf("uris = %d, want 3: %#v", len(result.URIs), result.URIs)
	}

	var vmess map[string]string
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(result.URIs[0], "vmess://"))
	if err != nil {
		t.Fatalf("decode vmess: %v", err)
	}
	if err := json.Unmarshal(decoded, &vmess); err != nil {
		t.Fatalf("unmarshal vmess: %v", err)
	}
	if vmess["ps"] != "HK" || vmess["add"] != "hk.example.com" || vmess["net"] != "ws" || vmess["tls"] != "tls" {
		t.Fatalf("unexpected vmess payload: %#v", vmess)
	}
	if !strings.HasPrefix(result.URIs[1], "vless://") || !strings.Contains(result.URIs[1], "security=tls") {
		t.Fatalf("unexpected vless URI: %s", result.URIs[1])
	}
	if !strings.HasPrefix(result.URIs[2], "trojan://") {
		t.Fatalf("unexpected trojan URI: %s", result.URIs[2])
	}
}

func TestParseNodeInputShadowrocketGroupedJSON(t *testing.T) {
	raw := `{
		"vmess": [{
			"name": "grouped",
			"address": "group.example.com",
			"port": 443,
			"id": "33333333-3333-3333-3333-333333333333"
		}],
		"shadowsocks": [{
			"name": "ss",
			"server": "ss.example.com",
			"server_port": 8388,
			"method": "aes-128-gcm",
			"password": "ss-password"
		}]
	}`

	result := parseNodeInput(raw)
	if result.Format != "shadowrocket-json" || len(result.URIs) != 2 {
		t.Fatalf("unexpected grouped JSON result: %#v", result)
	}
	if !strings.HasPrefix(result.URIs[0], "vmess://") || !strings.HasPrefix(result.URIs[1], "ss://") {
		t.Fatalf("unexpected grouped URIs: %#v", result.URIs)
	}
}

func TestParseNodeInputShadowrocketVMessVariant(t *testing.T) {
	payload, err := json.Marshal(map[string]string{
		"v":    "2",
		"ps":   "SR VMess",
		"add":  "sr.example.com",
		"port": "443",
		"id":   "44444444-4444-4444-4444-444444444444",
		"aid":  "0",
		"net":  "ws",
		"path": "/ws",
		"host": "sr.example.com",
		"tls":  "tls",
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	token := base64.StdEncoding.EncodeToString(payload)
	raw := "vmess://" + token + "?remarks=SR%20VMess&tls=1"

	result := parseNodeInput(raw)
	if len(result.URIs) != 1 {
		t.Fatalf("variant result = %#v", result)
	}
	if !strings.HasPrefix(result.URIs[0], "vmess://") {
		t.Fatalf("variant output is not vmess: %s", result.URIs[0])
	}
	if strings.Contains(strings.TrimPrefix(result.URIs[0], "vmess://"), "?") {
		t.Fatalf("variant output was not canonicalized: %s", result.URIs[0])
	}
}

func TestParseNodeInputShadowrocketLinkAndSubscription(t *testing.T) {
	inner := "trojan://pass@link.example.com:443#Link"
	link := "shadowrocket://add/" + url.QueryEscape(inner)
	result := parseNodeInput(link)
	if result.Format != "shadowrocket-link" || len(result.URIs) != 1 || result.URIs[0] != inner {
		t.Fatalf("unexpected shadowrocket link result: %#v", result)
	}

	multiple := "vless://uuid@a.example.com:443#A\nss://YWVzLTEyOC1nY206cGFzcw@b.example.com:8388#B"
	encoded := base64.StdEncoding.EncodeToString([]byte(multiple))
	for _, raw := range []string{encoded, "sub://" + encoded} {
		result := parseNodeInput(raw)
		if len(result.URIs) != 2 {
			t.Fatalf("subscription %q result = %#v", raw, result)
		}
	}
}

func TestBatchCreateNodesAcceptsShadowrocketJSON(t *testing.T) {
	_, db := newTestApp(t)
	raw := `[
		{"type":"vmess","remarks":"batch","server":"batch.example.com","server_port":443,"uuid":"55555555-5555-5555-5555-555555555555"},
		{"type":"vless","remarks":"batch-vless","server":"vless.example.com","server_port":443,"uuid":"66666666-6666-6666-6666-666666666666","tls":true}
	]`

	result, err := db.BatchCreateNodes(context.Background(), raw)
	if err != nil {
		t.Fatalf("BatchCreateNodes: %v", err)
	}
	if result.Imported != 2 || result.Skipped != 0 || result.Format != "shadowrocket-json" {
		t.Fatalf("unexpected batch result: %#v", result)
	}
	nodes, err := db.ListNodes(context.Background())
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("nodes = %d, want 2", len(nodes))
	}
	for _, node := range nodes {
		if !strings.HasPrefix(node.URI, "vmess://") && !strings.HasPrefix(node.URI, "vless://") {
			t.Fatalf("node was not stored as a standard URI: %s", node.URI)
		}
	}
}

func TestMasterSubscriptionIncludesNodesOutsideCollections(t *testing.T) {
	app, db := newTestApp(t)
	createTestNode(t, db, "ungrouped", "vless://uuid@ungrouped.example.com:443#Ungrouped")

	req := httptest.NewRequest(http.MethodGet, "/sub?token=master-test-token", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	decoded, err := base64.StdEncoding.DecodeString(rec.Body.String())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(string(decoded), "ungrouped.example.com") {
		t.Fatalf("master subscription omitted ungrouped node: %q", decoded)
	}
}
