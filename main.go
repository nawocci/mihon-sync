// Command mihon-sync runs the sync server and its admin tools.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"mihon-sync/internal/server"
	"mihon-sync/internal/store"
)

const version = "0.1.0"

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		runServe(nil)
		return
	}
	switch args[0] {
	case "serve":
		runServe(args[1:])
	case "invite":
		runInvite(args[1:])
	case "version":
		fmt.Println(version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", args[0])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: mihon-sync <command>

commands:
  serve                 run the sync server (default when no command is given)
  invite create         create an invite code that can be redeemed on the web
  version               print the version

run "mihon-sync <command> -h" for the flags of a command.
`)
}

func runServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", ":8080", "listen address")
	dataDir := fs.String("data", "./data", "directory holding the database")
	bootstrap := fs.String(
		"bootstrap-network",
		"loopback",
		`who may generate keys from the web: "loopback", "private" or "any"`,
	)
	fs.Parse(args)

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	st, err := openStore(*dataDir)
	if err != nil {
		logger.Error("opening the database failed", "error", err)
		os.Exit(1)
	}
	defer st.Close()

	srv := &http.Server{
		Addr: *addr,
		Handler: server.New(st, server.Config{
			BootstrapNetwork: *bootstrap,
			Version:          version,
		}, logger).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	logger.Info("sync server listening", "addr", *addr, "data", *dataDir)

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		logger.Info("sync server stopped")
	}
}

func runInvite(args []string) {
	if len(args) == 0 || args[0] != "create" {
		fmt.Fprintln(os.Stderr, "usage: mihon-sync invite create [--label X] [--uses N] [--ttl 168h] [--data DIR]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("invite create", flag.ExitOnError)
	dataDir := fs.String("data", "./data", "directory holding the database")
	label := fs.String("label", "", "label for the library it creates (default key-N)")
	uses := fs.Int("uses", 1, "how many times the invite may be redeemed")
	ttl := fs.Duration("ttl", 7*24*time.Hour, "validity, e.g. 48h (0 = never expires)")
	fs.Parse(args[1:])

	st, err := openStore(*dataDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	defer st.Close()

	code, err := st.CreateInvite(context.Background(), *label, *uses, *ttl)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Println(code)
	fmt.Fprintln(os.Stderr, "redeem it on the dashboard, or with POST /v1/register")
}

func openStore(dataDir string) (*store.Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	return store.Open(filepath.Join(dataDir, "sync.db"))
}
