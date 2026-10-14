// Package config loads server configuration from environment variables.
package config

import (
	"os"
	"strconv"
	"strings"
)

const (
	defaultAddr          = ":8080"
	defaultDBPath        = "./mihon-sync.db"
	defaultRetentionDays = 30
	// defaultMaxEntities bounds a single account so one runaway push cannot
	// exhaust memory or disk. Zero disables the check.
	defaultMaxEntities = 500_000
)

// Registration modes for POST /api/v1/auth/register.
const (
	// RegistrationOpen allows key generation with no invite code.
	RegistrationOpen = "open"
	// RegistrationInvite requires a valid single-use invite code.
	RegistrationInvite = "invite"
	// RegistrationClosed disables key generation.
	RegistrationClosed = "closed"
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
	// Registration controls key generation via POST /api/v1/auth/register:
	// "open" (default), "invite", or "closed".
	Registration string
	// MaxEntities caps the number of non-deleted entities a single account may
	// hold. A push that would exceed it is rejected.
	MaxEntities int
	// MetricsEnabled exposes GET /metrics in Prometheus text format.
	MetricsEnabled bool
	// LogLevel is one of debug, info, warn, error.
	LogLevel string
}

func FromEnv() Config {
	return Config{
		Addr:           envStr("MIHON_SYNC_ADDR", defaultAddr),
		DBPath:         envStr("MIHON_SYNC_DB", defaultDBPath),
		RetentionDays:  envInt("MIHON_SYNC_RETENTION_DAYS", defaultRetentionDays),
		BootstrapKey:   os.Getenv("MIHON_SYNC_API_KEY"),
		Registration:   normalizeRegistration(os.Getenv("MIHON_SYNC_REGISTRATION"), os.Getenv("MIHON_SYNC_ALLOW_REGISTRATION")),
		MaxEntities:    envIntAllowZero("MIHON_SYNC_MAX_ENTITIES", defaultMaxEntities),
		MetricsEnabled: envBool("MIHON_SYNC_METRICS", false),
		LogLevel:       envStr("MIHON_SYNC_LOG_LEVEL", "info"),
	}
}

// normalizeRegistration resolves the registration mode. The new
// MIHON_SYNC_REGISTRATION variable wins; the legacy boolean
// MIHON_SYNC_ALLOW_REGISTRATION maps false to "closed" for compatibility.
func normalizeRegistration(mode, legacyAllow string) string {
	switch mode {
	case RegistrationOpen, RegistrationInvite, RegistrationClosed:
		return mode
	}
	if legacyAllow != "" {
		if b, err := strconv.ParseBool(legacyAllow); err == nil && !b {
			return RegistrationClosed
		}
	}
	return RegistrationOpen
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

// envIntAllowZero is like envInt but honours an explicit 0 (used to disable a cap).
func envIntAllowZero(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
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

// ValidLogLevel reports whether level is a recognised slog level name.
func ValidLogLevel(level string) bool {
	switch strings.ToLower(level) {
	case "debug", "info", "warn", "error":
		return true
	}
	return false
}
