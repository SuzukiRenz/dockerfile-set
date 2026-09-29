package main

import "errors"

var (
	ErrUnauthorized  = errors.New("unauthorized")
	ErrExpired       = errors.New("token expired")
	ErrUsageLimit    = errors.New("token usage limit reached")
	ErrTokenDisabled = errors.New("token disabled")
	ErrNotFound      = errors.New("not found")
	ErrNoNodes       = errors.New("no nodes configured")
	ErrForbidden     = errors.New("forbidden")
)
