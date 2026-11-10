package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/api"
	"github.com/hakanngul/GoLoadTestingApp/internal/runner"
	"github.com/hakanngul/GoLoadTestingApp/internal/store"
	"github.com/hakanngul/GoLoadTestingApp/web"
)

// cmdServe runs the master: REST API, live stream, embedded UI and the
// run manager. Runs execute in-process until workers are connected.
func cmdServe(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	addr := flags.String("addr", envOr("GOLOAD_ADDR", ":8080"), "HTTP listen address")
	db := flags.String("db", envOr("GOLOAD_DB", "memory"), `Postgres DSN, or "memory" for a throwaway in-memory store`)
	token := flags.String("token", os.Getenv("GOLOAD_TOKEN"), "require this bearer token on /api (empty = no auth)")
	devCORS := flags.Bool("dev-cors", false, "allow the Vite dev server (localhost:5173) to call the API")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}

	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := openStore(ctx, *db)
	if err != nil {
		fmt.Fprintf(stderr, "store: %v\n", err)
		return exitUsage
	}
	defer st.Close()
	if n, err := st.MarkInterrupted(ctx, "master restarted while the run was active"); err != nil {
		log.Warn("mark interrupted runs", "error", err)
	} else if n > 0 {
		log.Info("marked interrupted runs", "count", n)
	}

	mgr := runner.NewManager(runner.Options{Store: st, Version: version, Logger: log})

	ui, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		fmt.Fprintf(stderr, "ui: %v\n", err)
		return exitRun
	}
	srv := &http.Server{
		Addr: *addr,
		Handler: api.NewServer(api.Config{
			Store: st, Runner: mgr, UI: ui, Version: version,
			Logger: log, AuthToken: *token, DevCORS: *devCORS,
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	fmt.Fprintf(stdout, "goload %s listening on %s (store: %s)\n", version, *addr, storeKind(*db))

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(stderr, "http: %v\n", err)
			return exitRun
		}
	case <-ctx.Done():
	}

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := mgr.Shutdown(shutdownCtx); err != nil {
		log.Warn("stop active runs", "error", err)
	}
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Warn("http shutdown", "error", err)
	}
	return exitOK
}

func openStore(ctx context.Context, dsn string) (store.Store, error) {
	if dsn == "memory" {
		return store.NewMemory(), nil
	}
	return store.NewPostgres(ctx, dsn)
}

func storeKind(dsn string) string {
	if dsn == "memory" {
		return "memory (data is lost on exit)"
	}
	return "postgres"
}


func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
