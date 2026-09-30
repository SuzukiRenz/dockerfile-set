package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

type nodeImportResult struct {
	URIs    []string
	Format  string
	Ignored int
}

type NodeBatchResult struct {
	Format   string `json:"format,omitempty"`
	Label    string `json:"format_label,omitempty"`
	Imported int    `json:"imported"`
	Skipped  int    `json:"skipped"`
	Ignored  int    `json:"ignored"`
}

var nodeURIRegex = regexp.MustCompile(`(?i)(?:vmess|vless|trojan|ssr|ss|hysteria2|hy2|hysteria|tuic|anytls|socks5?|http|https)://[^\s<>"']+`)

func parseNodeInput(raw string) nodeImportResult {
	return parseNodeInputDepth(raw, 0)
}

func parseNodeInputDepth(raw string, depth int) nodeImportResult {
	if depth > 4 {
		return nodeImportResult{Format: "unknown"}
	}
	raw = cleanNodeInput(raw)
	if raw == "" {
		return nodeImportResult{Format: "empty"}
	}

	if strings.HasPrefix(raw, "{") || strings.HasPrefix(raw, "[") {
		if uris := parseNodeJSON(raw); len(uris) > 0 {
			return nodeImportResult{URIs: uniqueStrings(uris), Format: "shadowrocket-json"}
		}
		return nodeImportResult{Format: "shadowrocket-json", Ignored: 1}
	}

	if strings.HasPrefix(strings.ToLower(raw), "vmess://") && strings.Contains(raw, "?") {
		if uri, ok := parseShadowrocketVMessVariant(raw); ok {
			return nodeImportResult{URIs: []string{uri}, Format: "shadowrocket-vmess"}
		}
	}

	if isShadowrocketLink(raw) {
		if uris := parseShadowrocketLink(raw, depth+1); len(uris) > 0 {
			return nodeImportResult{URIs: uniqueStrings(uris), Format: "shadowrocket-link"}
		}
		return nodeImportResult{Format: "shadowrocket-link", Ignored: 1}
	}

	if strings.HasPrefix(strings.ToLower(raw), "sub://") {
		if uris := parseInlineSubscription(strings.TrimSpace(raw[len("sub://"):]), depth+1); len(uris) > 0 {
			return nodeImportResult{URIs: uniqueStrings(uris), Format: "base64-subscription"}
		}
	}

	if decoded, ok := decodeBase64Text(raw); ok && looksLikeNodeInput(decoded) {
		result := parseNodeInputDepth(decoded, depth+1)
		if len(result.URIs) > 0 {
			result.Format = "base64-" + result.Format
			return result
		}
	}

	uris := extractNodeURIs(raw)
	if len(uris) == 0 {
		return nodeImportResult{Format: "unknown", Ignored: 1}
	}
	return nodeImportResult{URIs: uniqueStrings(uris), Format: "uri"}
}

func parseShadowrocketVMessVariant(raw string) (string, bool) {
	payload := strings.TrimSpace(raw[len("vmess://"):])
	queryIndex := strings.Index(payload, "?")
	if queryIndex <= 0 {
		return "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(payload[:queryIndex])
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(payload[:queryIndex])
	}
	if err != nil {
		decoded, err = base64.RawURLEncoding.DecodeString(payload[:queryIndex])
	}
	if err != nil {
		return "", false
	}
	var node map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(decoded)))
	decoder.UseNumber()
	if err := decoder.Decode(&node); err != nil {
		return "", false
	}
	query, err := url.ParseQuery(payload[queryIndex+1:])
	if err != nil {
		return "", false
	}
	if firstString(node, "ps", "remarks", "name") == "" {
		for _, key := range []string{"remarks", "remark", "name", "ps"} {
			if value := strings.TrimSpace(query.Get(key)); value != "" {
				node["ps"] = value
				break
			}
		}
	}
	if truthy(query.Get("tls")) {
		node["tls"] = "tls"
	}
	for _, key := range []string{"sni", "path", "host"} {
		if value := strings.TrimSpace(query.Get(key)); value != "" {
			node[key] = value
		}
	}
	return jsonMapToNodeURI(node, "vmess")
}

func cleanNodeInput(raw string) string {
	raw = strings.TrimSpace(strings.TrimPrefix(raw, "\ufeff"))
	if raw == "" {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	cleaned := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			continue
		}
		cleaned = append(cleaned, line)
	}
	return strings.TrimSpace(strings.Join(cleaned, "\n"))
}

func isShadowrocketLink(raw string) bool {
	lower := strings.ToLower(strings.TrimSpace(raw))
	return strings.HasPrefix(lower, "shadowrocket://") || strings.HasPrefix(lower, "shadowrocket:")
}

func parseShadowrocketLink(raw string, depth int) []string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil
	}

	for _, key := range []string{"url", "sub", "subscription"} {
		value := strings.TrimSpace(parsed.Query().Get(key))
		if value == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(value), "sub://") {
			if uris := parseInlineSubscription(strings.TrimSpace(value[len("sub://"):]), depth+1); len(uris) > 0 {
				return uris
			}
		}
		if result := parseNodeInputDepth(value, depth+1); len(result.URIs) > 0 {
			return result.URIs
		}
	}

	payload := strings.Trim(parsed.Path, "/")
	if fragment := strings.TrimSpace(parsed.Fragment); fragment != "" {
		if payload == "" {
			payload = fragment
		} else {
			payload += "#" + fragment
		}
	}
	if payload == "" {
		payload = strings.TrimSpace(parsed.Host)
	}
	payload = strings.TrimPrefix(payload, "add/")
	payload = strings.TrimPrefix(payload, "install/")
	payload = strings.TrimPrefix(payload, "import/")
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return nil
	}

	for i := 0; i < 3; i++ {
		decoded, err := url.QueryUnescape(payload)
		if err != nil || decoded == payload {
			break
		}
		payload = decoded
	}
	if strings.HasPrefix(strings.ToLower(payload), "sub://") {
		return parseInlineSubscription(strings.TrimSpace(payload[len("sub://"):]), depth+1)
	}
	result := parseNodeInputDepth(payload, depth+1)
	return result.URIs
}

func parseInlineSubscription(raw string, depth int) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if result := parseNodeInputDepth(raw, depth+1); len(result.URIs) > 0 {
		return result.URIs
	}
	if decoded, ok := decodeBase64Text(raw); ok {
		result := parseNodeInputDepth(decoded, depth+1)
		return result.URIs
	}
	return nil
}

func parseNodeJSON(raw string) []string {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil
	}
	return uniqueStrings(collectJSONNodeURIs(value, "", 0))
}

func collectJSONNodeURIs(value any, defaultType string, depth int) []string {
	if depth > 12 {
		return nil
	}
	switch item := value.(type) {
	case []any:
		out := []string{}
		for _, child := range item {
			out = append(out, collectJSONNodeURIs(child, defaultType, depth+1)...)
		}
		return out
	case map[string]any:
		out := []string{}
		if uri, ok := jsonMapToNodeURI(item, defaultType); ok {
			out = append(out, uri)
		}
		for key, child := range item {
			childType := jsonKeyNodeType(key)
			out = append(out, collectJSONNodeURIs(child, childType, depth+1)...)
		}
		return out
	default:
		return nil
	}
}

func jsonKeyNodeType(key string) string {
	normalized := normalizeJSONKey(key)
	switch {
	case strings.Contains(normalized, "vmess"):
		return "vmess"
	case strings.Contains(normalized, "vless"):
		return "vless"
	case strings.Contains(normalized, "trojan"):
		return "trojan"
	case strings.Contains(normalized, "hysteria2") || strings.Contains(normalized, "hy2"):
		return "hysteria2"
	case strings.Contains(normalized, "hysteria"):
		return "hysteria"
	case strings.Contains(normalized, "tuic"):
		return "tuic"
	case strings.Contains(normalized, "anytls"):
		return "anytls"
	case strings.Contains(normalized, "shadowsocksr") || normalized == "ssr":
		return "ssr"
	case strings.Contains(normalized, "shadowsocks") || normalized == "ss":
		return "ss"
	case strings.Contains(normalized, "socks"):
		return "socks5"
	case normalized == "http" || normalized == "https":
		return normalized
	default:
		return ""
	}
}

func jsonMapToNodeURI(node map[string]any, defaultType string) (string, bool) {
	server := firstString(node, "server", "address", "add", "server_host", "hostname")
	if server == "" {
		server = firstString(node, "host")
	}
	port := firstInt(node, "port", "server_port", "serverport", "remote_port")
	if server == "" || port <= 0 {
		return "", false
	}

	nodeType := strings.ToLower(strings.TrimSpace(firstString(node, "type", "proxy_type", "proxytype")))
	if nodeType == "" {
		nodeType = strings.ToLower(strings.TrimSpace(defaultType))
	}
	if nodeType == "" {
		configType := firstInt(node, "config_type", "configtype")
		switch configType {
		case 1:
			nodeType = "vmess"
		case 3:
			nodeType = "ss"
		case 4:
			nodeType = "socks5"
		}
	}
	if nodeType == "" {
		identifier := firstString(node, "uuid", "id", "user_id", "userid")
		if identifier != "" {
			if firstInt(node, "alter_id", "alterid", "aid") >= 0 {
				nodeType = "vmess"
			} else {
				nodeType = "vless"
			}
		} else if firstString(node, "method", "cipher") != "" && firstString(node, "password", "pass") != "" {
			nodeType = "ss"
		} else if firstString(node, "password", "pass") != "" && firstBool(node, "tls", "stream_security", "streamsecurity") {
			nodeType = "trojan"
		} else if firstString(node, "auth", "auth_str", "authstr") != "" {
			nodeType = "hysteria2"
		}
	}

	name := firstString(node, "name", "remarks", "remark", "ps", "title", "tag", "label")
	network := normalizeNetwork(firstString(node, "network", "net", "transport"))
	path := firstString(node, "path", "ws_path", "wspath", "obfs_uri", "service_name")
	host := firstString(node, "ws_host", "wshost", "request_host", "requesthost", "obfs_host", "obfshost")
	headers := firstMap(node, "headers", "ws_headers", "wsheaders", "request_headers", "requestheaders")
	if host == "" {
		host = firstString(headers, "host", "Host")
	} else if strings.EqualFold(host, "Host") {
		host = firstString(headers, host)
	}
	tlsValue := firstValue(node, "tls", "stream_security", "streamsecurity", "over_tls", "overtls", "tls_enabled", "tlsenabled")
	tlsEnabled := truthy(tlsValue)
	sni := firstString(node, "sni", "server_name", "servername", "peer", "tls_host", "tlshost", "peername")
	if tlsConfig := firstMap(node, "tls"); len(tlsConfig) > 0 {
		if !hasValue(tlsValue) {
			tlsEnabled = truthy(firstValue(tlsConfig, "enabled", "value", "secure"))
		}
		if sni == "" {
			sni = firstString(tlsConfig, "server_name", "servername", "sni", "peer")
		}
	}
	if transport := firstMap(node, "transport"); len(transport) > 0 {
		if network == "" {
			network = normalizeNetwork(firstString(transport, "type", "network", "protocol"))
		}
		if path == "" {
			path = firstString(transport, "path", "service_name", "servicename")
		}
		if host == "" {
			headers := firstMap(transport, "headers")
			host = firstString(headers, "host", "Host")
		}
	}
	if wsConfig := firstMap(node, "ws", "websocket"); len(wsConfig) > 0 {
		if network == "" {
			network = "ws"
		}
		if path == "" {
			path = firstString(wsConfig, "path")
		}
		if host == "" {
			headers := firstMap(wsConfig, "headers")
			host = firstString(headers, "host", "Host")
		}
	}
	if network == "" && truthy(firstValue(node, "ws", "websocket")) {
		network = "ws"
	}
	if network == "" {
		network = "tcp"
	}
	if path == "" && network == "ws" {
		path = "/"
	}

	switch nodeType {
	case "vmess":
		return buildVMessURI(node, name, server, port, network, path, host, tlsEnabled, sni)
	case "vless":
		return buildVLESSURI(node, name, server, port, network, path, host, tlsEnabled, sni)
	case "trojan":
		return buildTrojanURI(node, name, server, port, network, path, host, tlsEnabled, sni)
	case "ss":
		return buildSSURI(node, name, server, port)
	case "ssr":
		return buildSSRURI(node, name, server, port)
	case "hysteria2", "hy2":
		return buildHysteria2URI(node, name, server, port)
	case "hysteria":
		return buildHysteriaURI(node, name, server, port)
	case "tuic":
		return buildTUICURI(node, name, server, port)
	case "anytls":
		return buildAnyTLSURI(node, name, server, port, sni)
	case "socks", "socks5":
		return buildSocksURI(node, name, "socks5", server, port)
	case "http", "https":
		return buildSocksURI(node, name, nodeType, server, port)
	default:
		return "", false
	}
}

func buildVMessURI(node map[string]any, name, server string, port int, network, path, host string, tlsEnabled bool, sni string) (string, bool) {
	identifier := preferredProxyIdentifier(node)
	if identifier == "" {
		return "", false
	}
	aid := firstInt(node, "alter_id", "alterid", "aid")
	cipher := firstString(node, "security", "cipher", "method", "encryption")
	if cipher == "" {
		cipher = "auto"
	}
	payload := map[string]string{
		"v":    "2",
		"ps":   name,
		"add":  server,
		"port": strconv.Itoa(port),
		"id":   identifier,
		"aid":  strconv.Itoa(maxInt(aid, 0)),
		"scy":  cipher,
		"net":  network,
		"type": firstString(node, "header_type", "headertype", "fake_type", "faketype"),
		"host": host,
		"path": path,
	}
	if payload["type"] == "" {
		payload["type"] = "none"
	}
	if tlsEnabled {
		payload["tls"] = "tls"
	}
	if sni != "" {
		payload["sni"] = sni
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", false
	}
	return "vmess://" + base64.StdEncoding.EncodeToString(encoded), true
}

func buildVLESSURI(node map[string]any, name, server string, port int, network, path, host string, tlsEnabled bool, sni string) (string, bool) {
	identifier := preferredProxyIdentifier(node)
	if identifier == "" {
		return "", false
	}
	query := url.Values{}
	query.Set("encryption", firstStringDefault(node, "encryption", "none"))
	query.Set("type", network)
	setQuery(query, "security", "none")
	if tlsEnabled {
		query.Set("security", "tls")
	}
	if isReality(node) {
		query.Set("security", "reality")
		setQuery(query, "pbk", firstString(node, "public_key", "publickey", "pbk"))
		setQuery(query, "sid", firstString(node, "short_id", "shortid", "sid"))
		setQuery(query, "spx", firstString(node, "spider_x", "spiderx", "spx"))
	}
	setQuery(query, "flow", vlessFlow(node))
	setQuery(query, "host", host)
	setQuery(query, "path", path)
	setQuery(query, "sni", sni)
	setQuery(query, "fp", firstString(node, "fingerprint", "fp", "tls_profile", "tlsprofile"))
	setQuery(query, "alpn", normalizeListValue(firstValue(node, "alpn")))
	setQuery(query, "headerType", firstString(node, "header_type", "headertype"))
	if firstBool(node, "allow_insecure", "allowinsecure", "skip_cert_verify", "skipcertverify", "scv") {
		query.Set("allowInsecure", "1")
	}
	return makeUserURI("vless", url.User(identifier).String(), server, port, query, name), true
}

func buildTrojanURI(node map[string]any, name, server string, port int, network, path, host string, tlsEnabled bool, sni string) (string, bool) {
	password := firstString(node, "password", "pass", "auth", "auth_str", "authstr")
	if password == "" {
		return "", false
	}
	query := url.Values{}
	if tlsEnabled || !hasValue(firstValue(node, "tls")) {
		query.Set("security", "tls")
	} else {
		query.Set("security", "none")
	}
	query.Set("type", network)
	setQuery(query, "host", host)
	setQuery(query, "path", path)
	setQuery(query, "sni", sni)
	setQuery(query, "fp", firstString(node, "fingerprint", "fp", "tls_profile", "tlsprofile"))
	setQuery(query, "alpn", normalizeListValue(firstValue(node, "alpn")))
	if firstBool(node, "allow_insecure", "allowinsecure", "skip_cert_verify", "skipcertverify", "scv") {
		query.Set("allowInsecure", "1")
	}
	return makeUserURI("trojan", url.User(password).String(), server, port, query, name), true
}

func buildSSURI(node map[string]any, name, server string, port int) (string, bool) {
	method := firstString(node, "method", "cipher", "encryption")
	password := firstString(node, "password", "pass")
	if method == "" || password == "" {
		return "", false
	}
	userInfo := base64.RawURLEncoding.EncodeToString([]byte(method + ":" + password))
	query := url.Values{}
	plugin := firstString(node, "plugin")
	pluginOptions := firstString(node, "plugin_opts", "pluginopts", "plugin_options", "pluginoptions")
	if plugin == "" {
		obfs := firstString(node, "obfs")
		if obfs != "" && obfs != "plain" {
			plugin = "obfs-local"
			pluginOptions = "obfs=" + obfs
			if host := firstString(node, "obfs_host", "obfshost"); host != "" {
				pluginOptions += ";obfs-host=" + host
			}
		}
	}
	if plugin != "" {
		if pluginOptions != "" {
			plugin += ";" + pluginOptions
		}
		query.Set("plugin", plugin)
	}
	return makeUserURI("ss", userInfo, server, port, query, name), true
}

func buildSSRURI(node map[string]any, name, server string, port int) (string, bool) {
	method := firstString(node, "method", "cipher", "encryption")
	password := firstString(node, "password", "pass")
	if method == "" || password == "" {
		return "", false
	}
	protocol := firstStringDefault(node, "protocol", "origin")
	obfs := firstStringDefault(node, "obfs", "plain")
	hostPort := net.JoinHostPort(strings.Trim(server, "[]"), strconv.Itoa(port))
	raw := strings.Join([]string{
		hostPort,
		protocol,
		method,
		obfs,
		base64.RawURLEncoding.EncodeToString([]byte(password)),
	}, ":") + "/?"
	query := url.Values{}
	if value := firstString(node, "obfs_param", "obfsparam"); value != "" {
		query.Set("obfsparam", base64.RawURLEncoding.EncodeToString([]byte(value)))
	}
	if value := firstString(node, "protocol_param", "protocolparam", "protoparam"); value != "" {
		query.Set("protoparam", base64.RawURLEncoding.EncodeToString([]byte(value)))
	}
	if name != "" {
		query.Set("remarks", base64.RawURLEncoding.EncodeToString([]byte(name)))
	}
	return "ssr://" + base64.RawURLEncoding.EncodeToString([]byte(raw+query.Encode())), true
}

func buildHysteria2URI(node map[string]any, name, server string, port int) (string, bool) {
	password := firstString(node, "password", "pass", "auth", "auth_str", "authstr")
	if password == "" {
		return "", false
	}
	query := url.Values{}
	setQuery(query, "sni", firstString(node, "sni", "server_name", "servername", "peer"))
	setQuery(query, "obfs", firstString(node, "obfs"))
	setQuery(query, "obfs-password", firstString(node, "obfs_password", "obfspassword"))
	setQuery(query, "pinSHA256", firstString(node, "pin_sha256", "pinsha256", "fingerprint"))
	setQuery(query, "alpn", normalizeListValue(firstValue(node, "alpn")))
	if firstBool(node, "allow_insecure", "allowinsecure", "skip_cert_verify", "skipcertverify", "scv") {
		query.Set("insecure", "1")
	}
	return makeUserURI("hysteria2", url.User(password).String(), server, port, query, name), true
}

func buildHysteriaURI(node map[string]any, name, server string, port int) (string, bool) {
	auth := firstString(node, "auth", "auth_str", "authstr", "password", "pass")
	if auth == "" {
		return "", false
	}
	query := url.Values{}
	setQuery(query, "auth", auth)
	setQuery(query, "protocol", firstString(node, "protocol"))
	setQuery(query, "peer", firstString(node, "peer", "sni"))
	setQuery(query, "obfs", firstString(node, "obfs"))
	setQuery(query, "upmbps", firstString(node, "up", "up_mbps", "upmbps"))
	setQuery(query, "downmbps", firstString(node, "down", "down_mbps", "downmbps"))
	setQuery(query, "alpn", normalizeListValue(firstValue(node, "alpn")))
	if firstBool(node, "allow_insecure", "allowinsecure", "skip_cert_verify", "skipcertverify", "scv") {
		query.Set("insecure", "1")
	}
	return makeUserURI("hysteria", "", server, port, query, name), true
}

func buildTUICURI(node map[string]any, name, server string, port int) (string, bool) {
	identifier := firstString(node, "uuid", "id", "user_id", "userid")
	password := firstString(node, "password", "pass")
	if identifier == "" || password == "" {
		return "", false
	}
	query := url.Values{}
	setQuery(query, "sni", firstString(node, "sni", "server_name", "servername", "peer"))
	setQuery(query, "alpn", normalizeListValue(firstValue(node, "alpn")))
	setQuery(query, "congestion_control", firstString(node, "congestion_control", "congestioncontrol"))
	setQuery(query, "udp_relay_mode", firstString(node, "udp_relay_mode", "udprelaymode"))
	if firstBool(node, "allow_insecure", "allowinsecure", "skip_cert_verify", "skipcertverify", "scv") {
		query.Set("allow_insecure", "1")
	}
	return makeUserURI("tuic", url.UserPassword(identifier, password).String(), server, port, query, name), true
}

func buildAnyTLSURI(node map[string]any, name, server string, port int, sni string) (string, bool) {
	password := firstString(node, "password", "pass")
	if password == "" {
		return "", false
	}
	query := url.Values{}
	query.Set("security", "tls")
	setQuery(query, "sni", sni)
	setQuery(query, "fp", firstString(node, "fingerprint", "fp", "tls_profile", "tlsprofile"))
	if firstBool(node, "allow_insecure", "allowinsecure", "skip_cert_verify", "skipcertverify", "scv") {
		query.Set("allowInsecure", "1")
	}
	return makeUserURI("anytls", url.User(password).String(), server, port, query, name), true
}

func buildSocksURI(node map[string]any, name, nodeType, server string, port int) (string, bool) {
	username := firstString(node, "username", "user")
	password := firstString(node, "password", "pass")
	userInfo := ""
	if username != "" {
		userInfo = url.User(username).String()
		if password != "" {
			userInfo = url.UserPassword(username, password).String()
		}
	}
	if nodeType == "https" {
		nodeType = "https"
	} else if nodeType == "http" {
		nodeType = "http"
	} else {
		nodeType = "socks5"
	}
	return makeUserURI(nodeType, userInfo, server, port, url.Values{}, name), true
}

func makeUserURI(scheme, userInfo, server string, port int, query url.Values, name string) string {
	hostPort := net.JoinHostPort(strings.Trim(server, "[]"), strconv.Itoa(port))
	base := scheme + "://"
	if userInfo != "" {
		base += userInfo + "@"
	}
	base += hostPort
	if encoded := query.Encode(); encoded != "" {
		base += "?" + encoded
	}
	if name != "" {
		base += "#" + url.PathEscape(name)
	}
	return base
}

func canonicalizeNodeURI(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimRight(raw, ",;，；。)]}")
	if strings.HasPrefix(strings.ToLower(raw), "hy2://") {
		return "hysteria2://" + raw[len("hy2://"):]
	}
	if uri, ok := expandShadowrocketAuthorityURI(raw); ok {
		return uri
	}
	return raw
}

func extractNodeURIs(raw string) []string {
	matches := nodeURIRegex.FindAllString(raw, -1)
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		uri := canonicalizeNodeURI(match)
		if importableNodeURI(uri) {
			out = append(out, uri)
		}
	}
	return uniqueStrings(out)
}

func importableNodeURI(raw string) bool {
	lower := strings.ToLower(strings.TrimSpace(raw))
	if lower == "" {
		return false
	}
	if strings.HasPrefix(lower, "vmess://") {
		return len(strings.TrimSpace(raw[len("vmess://"):])) > 0
	}
	if strings.HasPrefix(lower, "ssr://") {
		return len(strings.TrimSpace(raw[len("ssr://"):])) > 0
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return false
	}
	if parsed.Port() == "" {
		return false
	}
	switch {
	case strings.HasPrefix(lower, "http://"):
		return parsed.User != nil
	case strings.HasPrefix(lower, "https://"):
		return parsed.User != nil
	default:
		return true
	}
}

func looksLikeNodeInput(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if strings.HasPrefix(value, "{") || strings.HasPrefix(value, "[") {
		return true
	}
	if isShadowrocketLink(value) || strings.HasPrefix(strings.ToLower(value), "sub://") {
		return true
	}
	return nodeURIRegex.MatchString(value)
}

func normalizeNetwork(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "none":
		return ""
	case "websocket":
		return "ws"
	case "grpc":
		return "grpc"
	case "h2", "http/2":
		return "h2"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func isReality(node map[string]any) bool {
	return truthy(firstValue(node, "reality", "is_reality", "isreality")) ||
		firstString(node, "public_key", "publickey", "pbk") != ""
}

func firstValue(values map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := lookupJSONKey(values, key); ok {
			return value
		}
	}
	return nil
}

func firstString(values map[string]any, keys ...string) string {
	value := firstValue(values, keys...)
	switch item := value.(type) {
	case string:
		return strings.TrimSpace(item)
	case json.Number:
		return item.String()
	case float64:
		return strconv.FormatFloat(item, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(item), 'f', -1, 32)
	case int:
		return strconv.Itoa(item)
	case int64:
		return strconv.FormatInt(item, 10)
	case bool:
		return strconv.FormatBool(item)
	case []any:
		parts := make([]string, 0, len(item))
		for _, child := range item {
			if text := firstString(map[string]any{"value": child}, "value"); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, ",")
	default:
		return ""
	}
}

func firstStringDefault(values map[string]any, key, fallback string) string {
	if value := firstString(values, key); value != "" {
		return value
	}
	return fallback
}

func firstInt(values map[string]any, keys ...string) int {
	for _, key := range keys {
		value, ok := lookupJSONKey(values, key)
		if !ok {
			continue
		}
		switch item := value.(type) {
		case json.Number:
			if parsed, err := strconv.Atoi(item.String()); err == nil {
				return parsed
			}
		case float64:
			return int(item)
		case int:
			return item
		case int64:
			return int(item)
		case string:
			if parsed, err := strconv.Atoi(strings.TrimSpace(item)); err == nil {
				return parsed
			}
		}
	}
	return 0
}

func firstBool(values map[string]any, keys ...string) bool {
	return truthy(firstValue(values, keys...))
}

func firstMap(values map[string]any, keys ...string) map[string]any {
	value := firstValue(values, keys...)
	switch item := value.(type) {
	case map[string]any:
		return item
	case string:
		var decoded map[string]any
		if json.Unmarshal([]byte(item), &decoded) == nil {
			return decoded
		}
	}
	return nil
}

func lookupJSONKey(values map[string]any, key string) (any, bool) {
	if value, ok := values[key]; ok {
		return value, true
	}
	normalized := normalizeJSONKey(key)
	for existing, value := range values {
		if normalizeJSONKey(existing) == normalized {
			return value, true
		}
	}
	return nil, false
}

func normalizeJSONKey(value string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func truthy(value any) bool {
	switch item := value.(type) {
	case bool:
		return item
	case string:
		switch strings.ToLower(strings.TrimSpace(item)) {
		case "1", "true", "yes", "on", "tls", "reality", "enabled", "enable":
			return true
		default:
			return false
		}
	case json.Number:
		number, _ := item.Float64()
		return number != 0
	case float64:
		return item != 0
	case int:
		return item != 0
	case int64:
		return item != 0
	default:
		return false
	}
}

func hasValue(value any) bool {
	return value != nil
}

func setQuery(query url.Values, key, value string) {
	if strings.TrimSpace(value) != "" {
		query.Set(key, strings.TrimSpace(value))
	}
}

func normalizeListValue(value any) string {
	switch item := value.(type) {
	case []any:
		parts := make([]string, 0, len(item))
		for _, child := range item {
			text := firstString(map[string]any{"value": child}, "value")
			if text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, ",")
	default:
		return firstString(map[string]any{"value": value}, "value")
	}
}

func maxInt(value, minimum int) int {
	if value < minimum {
		return minimum
	}
	return value
}

func formatNodeImportLabel(format string) string {
	switch format {
	case "shadowrocket-json":
		return "小火箭 JSON"
	case "shadowrocket-link":
		return "小火箭分享链接"
	case "base64-subscription", "base64-uri", "base64-base64-uri":
		return "Base64 订阅"
	case "uri":
		return "标准节点 URI"
	default:
		return format
	}
}

func nodeImportError(format string) error {
	switch format {
	case "shadowrocket-json":
		return fmt.Errorf("没有从 JSON 中识别到可转换的节点")
	case "shadowrocket-link":
		return fmt.Errorf("小火箭分享链接中没有可离线解析的节点")
	default:
		return fmt.Errorf("没有识别到有效节点")
	}
}
func expandShadowrocketAuthorityURI(raw string) (string, bool) {
	lower := strings.ToLower(raw)
	if !strings.HasPrefix(lower, "vless://") && !strings.HasPrefix(lower, "vmess://") {
		return "", false
	}
	schemeEnd := strings.Index(raw, "://")
	if schemeEnd < 0 {
		return "", false
	}
	scheme := strings.ToLower(raw[:schemeEnd])
	rest := raw[schemeEnd+3:]
	fragment := ""
	if hash := strings.Index(rest, "#"); hash >= 0 {
		fragment = rest[hash+1:]
		rest = rest[:hash]
	}
	queryText := ""
	if query := strings.Index(rest, "?"); query >= 0 {
		queryText = rest[query+1:]
		rest = rest[:query]
	}
	decoded, ok := decodeBase64Text(rest)
	if !ok {
		return "", false
	}
	decoded = strings.TrimSpace(decoded)
	if !strings.Contains(decoded, "@") {
		return "", false
	}
	parts := strings.SplitN(decoded, "@", 2)
	userInfo := strings.TrimPrefix(parts[0], ":")
	endpoint := parts[1]
	lastColon := strings.LastIndex(endpoint, ":")
	if lastColon <= 0 || lastColon == len(endpoint)-1 {
		return "", false
	}
	server := strings.Trim(endpoint[:lastColon], "[]")
	port, err := strconv.Atoi(endpoint[lastColon+1:])
	if err != nil || port <= 0 || server == "" || userInfo == "" {
		return "", false
	}
	query, err := url.ParseQuery(queryText)
	if err != nil {
		return "", false
	}
	name := ""
	if fragment != "" {
		name, _ = url.QueryUnescape(fragment)
	}
	node := map[string]any{
		"type":     scheme,
		"server":   server,
		"port":     port,
		"name":     firstNonEmpty(name, query.Get("remarks"), query.Get("remark")),
		"network":  firstNonEmpty(query.Get("type"), query.Get("net"), query.Get("obfs")),
		"path":     query.Get("path"),
		"host":     query.Get("host"),
		"sni":      firstNonEmpty(query.Get("sni"), query.Get("peer")),
		"tls":      firstNonEmpty(query.Get("security"), query.Get("tls")),
		"pbk":      query.Get("pbk"),
		"sid":      query.Get("sid"),
		"fp":       query.Get("fingerprint"),
		"xtls":     query.Get("xtls"),
		"security": query.Get("security"),
	}
	node["uuid"] = userInfo
	return jsonMapToNodeURI(node, scheme)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

var uuidRegex = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func preferredProxyIdentifier(node map[string]any) string {
	candidates := []string{
		firstString(node, "uuid", "id", "user_id", "userid"),
		firstString(node, "password", "pass"),
	}
	for _, candidate := range candidates {
		if uuidRegex.MatchString(candidate) {
			return candidate
		}
	}
	for _, candidate := range candidates {
		if candidate != "" {
			return candidate
		}
	}
	return ""
}

func vlessFlow(node map[string]any) string {
	if flow := firstString(node, "flow"); flow != "" {
		return flow
	}
	switch firstString(node, "xtls") {
	case "1":
		return "xtls-rprx-direct"
	case "2":
		return "xtls-rprx-vision"
	default:
		return ""
	}
}
