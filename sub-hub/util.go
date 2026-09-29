package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

var slugCleaner = regexp.MustCompile(`[^a-z0-9]+`)

func randomToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func tokenFingerprint(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func tokenPrefix(token string) string {
	token = strings.TrimSpace(token)
	if len(token) <= 10 {
		return token
	}
	return token[:10]
}

func makeSlug(name string, id int64) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = slugCleaner.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")
	if name == "" {
		name = fmt.Sprintf("collection-%d", id)
	}
	return name
}

func extractName(uri string) string {
	uri = strings.TrimSpace(uri)
	if uri == "" {
		return ""
	}
	if idx := strings.LastIndex(uri, "#"); idx >= 0 {
		name := uri[idx+1:]
		if decoded, err := url.PathUnescape(name); err == nil {
			name = decoded
		}
		if strings.TrimSpace(name) != "" {
			return strings.TrimSpace(name)
		}
	}
	if strings.HasPrefix(strings.ToLower(uri), "vmess://") {
		payload := uri[len("vmess://"):]
		for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
			if decoded, err := encoding.DecodeString(payload); err == nil {
				var obj map[string]any
				if json.Unmarshal(decoded, &obj) == nil {
					if name, _ := obj["ps"].(string); strings.TrimSpace(name) != "" {
						return strings.TrimSpace(name)
					}
				}
			}
		}
	}
	if parsed, err := url.Parse(uri); err == nil {
		if host := parsed.Hostname(); host != "" {
			return host
		}
	}
	return "node"
}

func parseTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func nullableTime(value *time.Time) any {
	if value == nil || value.IsZero() {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func parseNullableTime(value string) *time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	parsed := parseTime(value)
	if parsed.IsZero() {
		return nil
	}
	return &parsed
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func normalizeNodeSource(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if decoded, ok := decodeBase64Text(raw); ok && looksLikeNodeList(decoded) {
		return decoded
	}
	return strings.NewReplacer("\r\n", "\n", "\r", "\n", ";", "\n").Replace(raw)
}

func decodeBase64Text(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		decoded, err := encoding.DecodeString(value)
		if err == nil && utf8Printable(string(decoded)) {
			return string(decoded), true
		}
	}
	return "", false
}

func utf8Printable(value string) bool {
	for _, r := range value {
		if r == unicode.ReplacementChar {
			return false
		}
	}
	return true
}

func looksLikeNodeList(value string) bool {
	for _, line := range strings.Split(value, "\n") {
		if strings.Contains(line, "://") {
			return true
		}
	}
	return false
}

func splitNodeLines(value string) []string {
	value = normalizeNodeSource(value)
	parts := strings.Split(strings.NewReplacer(",", "\n").Replace(value), "\n")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || strings.HasPrefix(part, "#") {
			continue
		}
		out = append(out, part)
	}
	return uniqueStrings(out)
}

func parseInt64String(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("invalid id")
	}
	var id int64
	if _, err := fmt.Sscan(value, &id); err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid id")
	}
	return id, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func uniqueInt64s(values []int64) []int64 {
	seen := map[int64]struct{}{}
	out := make([]int64, 0, len(values))
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
