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
	defaultAllowReg      = true
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
	// AllowRegistration controls whether web/API users can generate a new
	// account/key via POST /api/v1/auth/register.
	AllowRegistration bool
}

func FromEnv() Config {
	return Config{
		Addr:              envStr("MIHON_SYNC_ADDR", defaultAddr),
		DBPath:            envStr("MIHON_SYNC_DB", defaultDBPath),
		RetentionDays:     envInt("MIHON_SYNC_RETENTION_DAYS", defaultRetentionDays),
		BootstrapKey:      os.Getenv("MIHON_SYNC_API_KEY"),
		AllowRegistration: envBool("MIHON_SYNC_ALLOW_REGISTRATION", defaultAllowReg),
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

func envBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}
