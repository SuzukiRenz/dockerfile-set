package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
)

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func decodeJSON(r *http.Request, target any, maxBytes int64) error {
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	decoder := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("请求 JSON 无效: %w", err)
	}
	return nil
}

func methodNotAllowed(w http.ResponseWriter) {
	writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "请求方法不支持"})
}

func writeDBError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "资源不存在"})
	case errors.Is(err, ErrForbidden):
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "无权访问该集合"})
	case errors.Is(err, ErrExpired):
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "Token 已过期"})
	case errors.Is(err, ErrUsageLimit):
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "Token 使用次数已达上限"})
	case errors.Is(err, ErrTokenDisabled):
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "Token 已禁用"})
	case errors.Is(err, ErrUnauthorized):
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "Token 无效或缺失"})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
	}
}

func writeAuthError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrUnauthorized):
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "Token 无效或缺失"})
	case errors.Is(err, ErrExpired):
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "Token 已过期"})
	case errors.Is(err, ErrUsageLimit):
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "Token 使用次数已达上限"})
	case errors.Is(err, ErrTokenDisabled):
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "Token 已禁用"})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
	}
}

func parseInt64(value string) (int64, error) {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || parsed <= 0 {
		return 0, errors.New("无效 ID")
	}
	return parsed, nil
}

func int64SliceFromJSON(value []any) []int64 {
	out := []int64{}
	for _, item := range value {
		switch number := item.(type) {
		case float64:
			if number > 0 {
				out = append(out, int64(number))
			}
		case string:
			if parsed, err := parseInt64(number); err == nil {
				out = append(out, parsed)
			}
		}
	}
	return uniqueInt64s(out)
}

func clientIP(r *http.Request) string {
	for _, header := range []string{"CF-Connecting-IP", "X-Real-IP", "X-Forwarded-For"} {
		value := strings.TrimSpace(r.Header.Get(header))
		if value == "" {
			continue
		}
		if header == "X-Forwarded-For" {
			value = strings.TrimSpace(strings.Split(value, ",")[0])
		}
		if net.ParseIP(value) != nil {
			return value
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return strings.Trim(host, "[]")
}
