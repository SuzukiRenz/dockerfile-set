package main

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (a *App) serveSubscription(w http.ResponseWriter, r *http.Request, principal *Principal, explicitCollections []string, route string) {
	requested := explicitCollections
	if len(requested) == 0 {
		requested = splitList(r.URL.Query().Get("collections"))
	}
	if len(requested) == 0 {
		requested = splitList(r.URL.Query().Get("c"))
	}

	collectionIDs, collectionSlug, err := a.db.ResolveCollectionIDs(r.Context(), principal, requested)
	if err != nil {
		a.logSubscription(r, principal, collectionSlug, 0, http.StatusForbidden)
		writeDBError(w, err)
		return
	}
	uris, err := a.db.NodeURIsForCollections(r.Context(), collectionIDs)
	if err != nil {
		a.logSubscription(r, principal, collectionSlug, 0, http.StatusInternalServerError)
		writeDBError(w, err)
		return
	}
	if len(uris) == 0 && principal.IsMaster {
		if legacy, legacyErr := a.db.LegacyNodeURIs(r.Context(), a.config.NodesFile); legacyErr == nil {
			uris = legacy
		}
	}
	if len(uris) == 0 {
		a.logSubscription(r, principal, collectionSlug, 0, http.StatusNotFound)
		http.Error(w, "no nodes configured", http.StatusNotFound)
		return
	}

	if !principal.IsMaster {
		if err := a.db.ConsumeChildUse(r.Context(), principal.ID); err != nil {
			a.logSubscription(r, principal, collectionSlug, len(uris), http.StatusForbidden)
			writeDBError(w, err)
			return
		}
	}

	subName, _ := a.db.Setting(r.Context(), "sub_name")
	if strings.TrimSpace(subName) == "" {
		subName = "Sub Hub"
	}
	target := strings.TrimSpace(r.URL.Query().Get("target"))
	status := http.StatusOK
	if target == "" {
		a.writeRawSubscription(w, r, uris, subName)
	} else {
		status = a.writeConvertedSubscription(w, r, collectionIDs, target, subName)
	}
	a.logSubscription(r, principal, collectionSlug, len(uris), status)
}

func (a *App) writeRawSubscription(w http.ResponseWriter, r *http.Request, uris []string, subName string) {
	payload := strings.Join(uris, "\n")
	encoded := encodeBase64(payload)
	interval, _ := a.db.Setting(r.Context(), "update_interval")
	if strings.TrimSpace(interval) == "" {
		interval = "24"
	}
	userinfo, _ := a.db.Setting(r.Context(), "userinfo_header")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Profile-Update-Interval", interval)
	w.Header().Set("Subscription-Userinfo", userinfo)
	w.Header().Set("Content-Disposition", `attachment; filename="`+url.PathEscape(subName)+`"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, encoded)
}

func (a *App) writeConvertedSubscription(w http.ResponseWriter, r *http.Request, collectionIDs []int64, target, subName string) int {
	nodesURL := a.buildInternalURL(collectionIDs, "")
	query := url.Values{}
	query.Set("url", nodesURL)
	query.Set("insert", "false")
	if configURL, _ := a.db.Setting(r.Context(), "subconfig_url"); strings.TrimSpace(configURL) != "" {
		query.Set("config", configURL)
	}
	for _, key := range []string{
		"target", "udp", "tfo", "scv", "fdn", "expand", "classic", "list", "sort",
		"new_name", "filename", "interval", "rename", "exclude", "include", "groups",
		"ruleset", "config", "ver",
	} {
		if value := strings.TrimSpace(r.URL.Query().Get(key)); value != "" {
			query.Set(key, value)
		}
	}
	query.Set("filename", subName)
	query.Set("target", target)

	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, a.config.SubconverterURL+"/sub?"+query.Encode(), nil)
	if err != nil {
		http.Error(w, "subscription converter unavailable", http.StatusBadGateway)
		return http.StatusBadGateway
	}
	response, err := a.client.Do(request)
	if err != nil {
		http.Error(w, "subscription converter unavailable", http.StatusBadGateway)
		return http.StatusBadGateway
	}
	defer response.Body.Close()

	for key, values := range response.Header {
		if strings.EqualFold(key, "Content-Length") {
			continue
		}
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	if w.Header().Get("Content-Disposition") == "" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+url.PathEscape(subName)+`"`)
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
	return response.StatusCode
}

func (a *App) logSubscription(r *http.Request, principal *Principal, collectionSlug string, nodeCount, status int) {
	if principal == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	entry := AccessLog{
		TokenKind:      principal.Kind,
		TokenID:        principal.ID,
		TokenName:      principal.Name,
		TokenPrefix:    principal.TokenPrefix,
		CollectionSlug: collectionSlug,
		Target:         strings.TrimSpace(r.URL.Query().Get("target")),
		IP:             clientIP(r),
		UserAgent:      r.UserAgent(),
		Status:         status,
		NodeCount:      nodeCount,
		CreatedAt:      time.Now(),
	}
	_ = a.db.LogAccess(ctx, entry, a.config.LoggerMaxRecords)
}

func encodeBase64(value string) string {
	return base64.StdEncoding.EncodeToString([]byte(value))
}
