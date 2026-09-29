package main

import (
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Port             string
	DBPath           string
	BootstrapToken   string
	NodesFile        string
	SubconverterURL  string
	LoggerMaxRecords int
}

func loadConfig() Config {
	return Config{
		Port:             envOr("PORT", "8787"),
		DBPath:           envOr("DB_PATH", "/data/sub-hub.db"),
		BootstrapToken:   envOr("MASTER_TOKEN", envOr("TOKEN", "change-me-please")),
		NodesFile:        envOr("NODES_FILE", "/data/nodes.txt"),
		SubconverterURL:  strings.TrimRight(envOr("SUBCONVERTER_URL", "http://127.0.0.1:25500"), "/"),
		LoggerMaxRecords: envInt("LOG_MAX_RECORDS", 1000),
	}
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		return fallback
	}
	return parsed
}
