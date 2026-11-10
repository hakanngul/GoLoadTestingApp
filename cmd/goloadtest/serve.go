package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc"

	"github.com/hakanngul/GoLoadTestingApp/internal/allowlist"
	"github.com/hakanngul/GoLoadTestingApp/internal/api"
	"github.com/hakanngul/GoLoadTestingApp/internal/cluster"
	"github.com/hakanngul/GoLoadTestingApp/internal/provision"
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
	allowTargets := flags.String("allow-targets", os.Getenv("GOLOAD_ALLOWED_TARGETS"),
		`target hosts runs may hit: "api.example.com,*.staging.example.com,10.0.0.0/8"; "*" allows all (development only)`)
	grpcAddr := flags.String("grpc-addr", envOr("GOLOAD_GRPC_ADDR", ":9090"), `gRPC listen address for workers ("" disables distributed mode)`)
	workerToken := flags.String("worker-token", os.Getenv("GOLOAD_WORKER_TOKEN"), "token workers must present (required when -grpc-addr is set)")
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

	allow, err := allowlist.Parse(*allowTargets)
	if err != nil {
		fmt.Fprintf(stderr, "serve: %v\n", err)
		return exitUsage
	}
	switch {
	case allow.Empty():
		fmt.Fprintln(stderr, "serve: -allow-targets (or GOLOAD_ALLOWED_TARGETS) is required: list the hosts you own or have written permission to test; use \"*\" only for local development")
		return exitUsage
	case allow.AllowsAll():
		log.Warn("target allowlist is \"*\": runs may send load to any host")
	default:
		log.Info("target allowlist", "entries", allow.String())
	}

	ropts := runner.Options{Store: st, Version: version, Logger: log, Allow: allow.Check}
	if *grpcAddr != "" {
		if *workerToken == "" {
			fmt.Fprintln(stderr, "serve: -worker-token (or GOLOAD_WORKER_TOKEN) is required with -grpc-addr; use -grpc-addr \"\" for single-node mode")
			return exitUsage
		}
		master := cluster.NewMaster(cluster.MasterOptions{Token: *workerToken, Logger: log})
		gs := grpc.NewServer()
		master.Register(gs)
		lis, err := net.Listen("tcp", *grpcAddr)
		if err != nil {
			fmt.Fprintf(stderr, "grpc listen: %v\n", err)
			return exitUsage
		}
		go func() {
			if err := gs.Serve(lis); err != nil {
				log.Error("grpc serve", "error", err)
			}
		}()
		defer gs.GracefulStop()
		ropts.Dispatcher = master
		ropts.WorkerToken = *workerToken
		ropts.MasterAddr = os.Getenv("GOLOAD_MASTER_ADVERTISE_ADDR")
		prov, err := provisionerFromEnv(ctx, log)
		if err != nil {
			fmt.Fprintf(stderr, "provisioner: %v\n", err)
			return exitUsage
		}
		if prov != nil {
			ropts.Provisioner = prov
			go reapStaleWorkers(ctx, prov, log)
		}
		fmt.Fprintf(stdout, "worker coordinator listening on %s\n", *grpcAddr)
	}
	mgr := runner.NewManager(ropts)

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

// provisionerFromEnv builds the worker provisioner configured through
// GOLOAD_PROVISIONER (none | aws) and the GOLOAD_AWS_* variables.
func provisionerFromEnv(ctx context.Context, log *slog.Logger) (*provision.AWS, error) {
	switch v := envOr("GOLOAD_PROVISIONER", "none"); v {
	case "none":
		return nil, nil
	case "aws":
		if os.Getenv("GOLOAD_MASTER_ADVERTISE_ADDR") == "" {
			return nil, fmt.Errorf("GOLOAD_MASTER_ADVERTISE_ADDR is required with GOLOAD_PROVISIONER=aws")
		}
		return provision.NewAWS(ctx, provision.AWSConfig{
			Region:           os.Getenv("GOLOAD_AWS_REGION"),
			LaunchTemplateID: os.Getenv("GOLOAD_AWS_LAUNCH_TEMPLATE_ID"),
			SubnetIDs:        splitList(os.Getenv("GOLOAD_AWS_SUBNET_IDS")),
			InstanceTypes:    splitList(os.Getenv("GOLOAD_AWS_INSTANCE_TYPES")),
			MaxPrice:         os.Getenv("GOLOAD_AWS_MAX_PRICE"),
			SpotOnly:         os.Getenv("GOLOAD_AWS_SPOT_ONLY") == "true",
			WorkerImage:      os.Getenv("GOLOAD_WORKER_IMAGE"),
			Logger:           log,
		})
	default:
		return nil, fmt.Errorf("unknown GOLOAD_PROVISIONER %q (want none or aws)", v)
	}
}

// reapStaleWorkers terminates orphaned workers (e.g. left behind by a
// master crash) every 10 minutes. Workers older than the default 6h
// lifetime cap are considered stale.
func reapStaleWorkers(ctx context.Context, p *provision.AWS, log *slog.Logger) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			ids, err := p.TerminateStale(ctx, 6*time.Hour)
			if err != nil {
				log.Warn("terminate stale workers", "error", err)
			} else if len(ids) > 0 {
				log.Info("terminated stale workers", "count", len(ids))
			}
		}
	}
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
