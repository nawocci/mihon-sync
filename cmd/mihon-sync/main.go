// mihon-sync is a self-hosted sync server for Mihon-based apps and forks.
//
// Usage:
//
//	mihon-sync serve                     run the HTTP server
//	mihon-sync genkey [-label NAME]      create an API key (shown once)
//	mihon-sync revokekey KEY             delete an account and all its data
//	mihon-sync listkeys [-json]          list API key hashes and labels
//	mihon-sync invite [-label NAME] [-ttl 15m]   mint a one-time invite code
//	mihon-sync listinvites [-json]       list invite codes with their status
//	mihon-sync revokeinvite ID           delete an unused invite code
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nawocci/mihon-sync/internal/auth"
	"github.com/nawocci/mihon-sync/internal/config"
	"github.com/nawocci/mihon-sync/internal/store"
	"github.com/nawocci/mihon-sync/internal/syncapi"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "serve":
		err = cmdServe(os.Args[2:])
	case "genkey":
		err = cmdGenkey(os.Args[2:])
	case "revokekey":
		err = cmdRevokekey(os.Args[2:])
	case "listkeys":
		err = cmdListkeys(os.Args[2:])
	case "invite":
		err = cmdInvite(os.Args[2:])
	case "listinvites":
		err = cmdListInvites(os.Args[2:])
	case "revokeinvite":
		err = cmdRevokeInvite(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		slog.Error("command failed", "error", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `usage: mihon-sync <command>

commands:
  serve                    run the HTTP server
  genkey [-label NAME]     create an API key (shown once)
  revokekey KEY            delete an account and all its synced data
  listkeys [-json]         list API key hashes and labels
  invite [-label NAME] [-ttl 15m]   mint a one-time invite code (shown once)
  listinvites [-json]      list invite codes with their status
  revokeinvite ID          delete an unused invite code by ID

environment:
  MIHON_SYNC_ADDR                listen address (default ":8080")
  MIHON_SYNC_DB                  SQLite database path (default "./mihon-sync.db")
  MIHON_SYNC_RETENTION_DAYS      tombstone retention in days (default 30)
  MIHON_SYNC_API_KEY             bootstrap API key, account created on serve start
  MIHON_SYNC_REGISTRATION        key generation mode: open (default), invite, closed
  MIHON_SYNC_MAX_ENTITIES        per-account entity cap (default 500000, 0 disables)
  MIHON_SYNC_METRICS             expose GET /metrics (default false)
  MIHON_SYNC_LOG_LEVEL           debug, info (default), warn, error
`)
}

// setupLogging configures the process-wide slog logger from the configured level.
func setupLogging(level string) {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l})))
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg := config.FromEnv()
	setupLogging(cfg.LogLevel)

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	ctx := context.Background()
	if cfg.BootstrapKey != "" {
		if err := st.CreateAccount(ctx, auth.HashKey(cfg.BootstrapKey), "bootstrap"); err != nil {
			return fmt.Errorf("bootstrap key: %w", err)
		}
		slog.Info("bootstrap API key account ensured")
	}

	// Tombstone garbage collection, once a day.
	retention := time.Duration(cfg.RetentionDays) * 24 * time.Hour
	gcCtx, stopGC := context.WithCancel(ctx)
	defer stopGC()
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-gcCtx.Done():
				return
			case <-ticker.C:
				if err := st.GC(gcCtx, retention); err != nil {
					slog.Warn("tombstone gc failed", "error", err)
				}
			}
		}
	}()

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           syncapi.NewHandler(st, cfg),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening",
			"addr", cfg.Addr,
			"db", cfg.DBPath,
			"registration", cfg.Registration,
			"metrics", cfg.MetricsEnabled,
			"version", syncapi.Version,
		)
		errCh <- srv.ListenAndServe()
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		slog.Info("shutting down", "signal", sig)
		shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func cmdGenkey(args []string) error {
	fs := flag.NewFlagSet("genkey", flag.ContinueOnError)
	label := fs.String("label", "", "human-readable label for the key")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg := config.FromEnv()
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	key, err := auth.GenerateKey()
	if err != nil {
		return err
	}
	if err := st.CreateAccount(context.Background(), auth.HashKey(key), *label); err != nil {
		return err
	}

	if *asJSON {
		return printJSON(map[string]string{"api_key": key, "label": *label})
	}
	fmt.Println("API key created. Store it somewhere safe — it is shown only once:")
	fmt.Println()
	fmt.Println("  " + key)
	return nil
}

func cmdRevokekey(args []string) error {
	fs := flag.NewFlagSet("revokekey", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: mihon-sync revokekey KEY")
	}

	cfg := config.FromEnv()
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	if err := st.DeleteAccount(context.Background(), auth.HashKey(fs.Arg(0))); err != nil {
		return err
	}
	fmt.Println("key revoked; all synced data for that account was deleted")
	return nil
}

func cmdListkeys(args []string) error {
	fs := flag.NewFlagSet("listkeys", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the result as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg := config.FromEnv()
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	accounts, err := st.ListAccounts(context.Background())
	if err != nil {
		return err
	}

	if *asJSON {
		type row struct {
			ID        int64  `json:"id"`
			KeyHash   string `json:"key_hash_prefix"`
			CreatedAt int64  `json:"created_at"`
			Label     string `json:"label"`
		}
		rows := make([]row, 0, len(accounts))
		for _, a := range accounts {
			rows = append(rows, row{
				ID: a.ID, KeyHash: truncate(a.KeyHash, 16), CreatedAt: a.CreatedAt, Label: a.Label,
			})
		}
		return printJSON(rows)
	}

	if len(accounts) == 0 {
		fmt.Println("no API keys; create one with: mihon-sync genkey")
		return nil
	}

	fmt.Printf("%-10s %-20s %-12s %s\n", "ID", "KEY HASH (PREFIX)", "CREATED", "LABEL")
	for _, a := range accounts {
		fmt.Printf("%-10d %-20s %-12s %s\n",
			a.ID, truncate(a.KeyHash, 16)+"...", time.Unix(a.CreatedAt, 0).Format("2006-01-02"), a.Label)
	}
	return nil
}

const defaultInviteTTL = 15 * time.Minute
const maxInviteTTL = 24 * time.Hour

func cmdInvite(args []string) error {
	fs := flag.NewFlagSet("invite", flag.ContinueOnError)
	label := fs.String("label", "", "human-readable label for the invite")
	ttl := fs.Duration("ttl", defaultInviteTTL, "invite code lifetime, e.g. 15m (max 24h)")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *ttl <= 0 || *ttl > maxInviteTTL {
		return errors.New("ttl must be between 1s and 24h")
	}

	cfg := config.FromEnv()
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	code, err := auth.GenerateInviteCode()
	if err != nil {
		return err
	}
	expiresAt := time.Now().Add(*ttl).Unix()
	if err := st.CreateInviteToken(context.Background(), auth.HashKey(code), *label, expiresAt); err != nil {
		return err
	}

	if *asJSON {
		return printJSON(map[string]any{
			"invite_code": code,
			"label":       *label,
			"expires_at":  expiresAt,
		})
	}
	fmt.Println("Invite code created. Share it within the window — it burns on first successful use:")
	fmt.Println()
	fmt.Println("  " + code)
	fmt.Printf("\nExpires in %s.\n", (*ttl).String())
	return nil
}

func cmdListInvites(args []string) error {
	fs := flag.NewFlagSet("listinvites", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the result as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg := config.FromEnv()
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	tokens, err := st.ListInviteTokens(context.Background())
	if err != nil {
		return err
	}

	now := time.Now().Unix()
	statusOf := func(t store.InviteToken) string {
		switch {
		case t.UsedAt.Valid:
			return "used"
		case t.ExpiresAt <= now:
			return "expired"
		default:
			return "pending"
		}
	}

	if *asJSON {
		type row struct {
			ID        int64  `json:"id"`
			Status    string `json:"status"`
			ExpiresAt int64  `json:"expires_at"`
			CreatedAt int64  `json:"created_at"`
			Label     string `json:"label"`
		}
		rows := make([]row, 0, len(tokens))
		for _, t := range tokens {
			rows = append(rows, row{
				ID: t.ID, Status: statusOf(t), ExpiresAt: t.ExpiresAt,
				CreatedAt: t.CreatedAt, Label: t.Label,
			})
		}
		return printJSON(rows)
	}

	if len(tokens) == 0 {
		fmt.Println("no invite codes; create one with: mihon-sync invite")
		return nil
	}
	fmt.Printf("%-6s %-10s %-16s %-10s %s\n", "ID", "STATUS", "EXPIRES", "CREATED", "LABEL")
	for _, t := range tokens {
		fmt.Printf("%-6d %-10s %-16s %-10s %s (hash %s...)\n",
			t.ID, statusOf(t), time.Unix(t.ExpiresAt, 0).Format("01-02 15:04"),
			time.Unix(t.CreatedAt, 0).Format("2006-01-02"), t.Label, truncate(t.TokenHash, 8))
	}
	return nil
}

func cmdRevokeInvite(args []string) error {
	fs := flag.NewFlagSet("revokeinvite", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: mihon-sync revokeinvite ID")
	}
	id, err := strconv.ParseInt(fs.Arg(0), 10, 64)
	if err != nil || id <= 0 {
		return errors.New("usage: mihon-sync revokeinvite ID")
	}

	cfg := config.FromEnv()
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	if err := st.DeleteInviteTokenByID(context.Background(), id); err != nil {
		return err
	}
	fmt.Println("invite code revoked")
	return nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
