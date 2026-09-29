package main

import (
	"net/http"
	"strings"
	"time"
)

func extractToken(r *http.Request) string {
	if token := strings.TrimSpace(r.URL.Query().Get("token")); token != "" {
		return token
	}
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(header) > 7 && strings.EqualFold(header[:7], "bearer ") {
		return strings.TrimSpace(header[7:])
	}
	return ""
}

func (a *App) authenticate(r *http.Request) (*Principal, error) {
	token := extractToken(r)
	if token == "" {
		return nil, ErrUnauthorized
	}
	return a.db.Authenticate(r.Context(), token)
}

func (p *Principal) collectionAllowed(collectionID int64) bool {
	if p.IsMaster && len(p.CollectionIDs) == 0 {
		return true
	}
	for _, id := range p.CollectionIDs {
		if id == collectionID {
			return true
		}
	}
	return false
}

func (p *Principal) validateSubscriptionUse(now time.Time) error {
	if p.IsMaster {
		return nil
	}
	if p.ExpiresAt != nil && !p.ExpiresAt.After(now) {
		return ErrExpired
	}
	if p.MaxUses > 0 && p.UseCount >= p.MaxUses {
		return ErrUsageLimit
	}
	return nil
}
