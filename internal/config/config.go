// Package config loads server configuration from environment variables.
package config

import (
	"os"
	"strconv"
)

const (
	defaultAddr          = ":8080"
	defaultDBPath        = "./mihon-sync.db"
	defaultRetentionDays = 30
)

type Config struct {
	// Addr is the listen address, e.g. ":8080".
	Addr string
	// DBPath is the path to the SQLite database file.
	DBPath string
	// RetentionDays is how long tombstones (deleted entities) are kept
	// before garbage collection.
	RetentionDays int
	// BootstrapKey, when set, ensures an account exists for this API key
	// on server start. Convenient for first-run/Docker setups.
	BootstrapKey string
}

func FromEnv() Config {
	return Config{
		Addr:          envStr("MIHON_SYNC_ADDR", defaultAddr),
		DBPath:        envStr("MIHON_SYNC_DB", defaultDBPath),
		RetentionDays: envInt("MIHON_SYNC_RETENTION_DAYS", defaultRetentionDays),
		BootstrapKey:  os.Getenv("MIHON_SYNC_API_KEY"),
	}
}

func envStr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}
