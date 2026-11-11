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
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc"

	"github.com/hakanngul/GoLoadTestingApp/internal/allowlist"
	"github.com/hakanngul/GoLoadTestingApp/internal/api"
	"github.com/hakanngul/GoLoadTestingApp/internal/cluster"
	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/notify"
	"github.com/hakanngul/GoLoadTestingApp/internal/provision"
	"github.com/hakanngul/GoLoadTestingApp/internal/runner"
	"github.com/hakanngul/GoLoadTestingApp/internal/scheduler"
	"github.com/hakanngul/GoLoadTestingApp/internal/store"
	"github.com/hakanngul/GoLoadTestingApp/internal/suite"
	"github.com/hakanngul/GoLoadTestingApp/web"
)

// cmdServe runs the master: REST API, live stream, embedded UI and the
// run manager. Runs execute in-process until workers are connected.
func cmdServe(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	addr := flags.String("addr", envOr("GOLOAD_ADDR", ":8080"), "HTTP listen address")
	db := flags.String("db", envOr("GOLOAD_DB", defaultDB),
		`store: "sqlite:<path>" (or a path ending in .db), "postgres://..." / "postgresql://..." DSN, or "memory" for a throwaway in-memory store`)
	token := flags.String("token", os.Getenv("GOLOAD_TOKEN"), "require this bearer token on /api (empty = no auth)")
	devCORS := flags.Bool("dev-cors", false, "allow the Vite dev server (localhost:5173) to call the API")
	allowTargets := flags.String("allow-targets", os.Getenv("GOLOAD_ALLOWED_TARGETS"),
		`target hosts runs may hit: "api.example.com,*.staging.example.com,10.0.0.0/8"; "*" allows all (development only)`)
	grpcAddr := flags.String("grpc-addr", envOr("GOLOAD_GRPC_ADDR", ":9090"), `gRPC listen address for workers ("" disables distributed mode)`)
	workerToken := flags.String("worker-token", os.Getenv("GOLOAD_WORKER_TOKEN"), "token workers must present (required when -grpc-addr is set)")
	retentionFlag := flags.String("retention", os.Getenv("GOLOAD_RETENTION"),
		`delete finished runs older than this, checked at start and hourly: e.g. 90d, 720h (min 1h; 0 or empty = keep forever). A test's baseline and latest run are always kept`)
	publicURL := flags.String("public-url", os.Getenv("GOLOAD_PUBLIC_URL"),
		`externally reachable base URL of this server (e.g. https://goload.example.com), used for links in notifications; empty = no links`)
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	retention, err := parseRetention(*retentionFlag)
	if err != nil {
		fmt.Fprintf(stderr, "serve: -retention: %v\n", err)
		return exitUsage
	}

	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	spec, err := parseStoreSpec(*db)
	if err != nil {
		fmt.Fprintf(stderr, "serve: -db: %v\n", err)
		return exitUsage
	}
	st, err := openStore(ctx, spec)
	if err != nil {
		fmt.Fprintf(stderr, "store: %v\n", err)
		return exitUsage
	}
	defer st.Close()
	log.Info("store opened", "kind", spec.kind, "location", spec.describe())
	if n, err := st.MarkInterrupted(ctx, "master restarted while the run was active"); err != nil {
		log.Warn("mark interrupted runs", "error", err)
	} else if n > 0 {
		log.Info("marked interrupted runs", "count", n)
	}
	if retention > 0 {
		rctx, cancel := context.WithCancel(ctx)
		wait, err := startRetention(rctx, st, retention, log)
		if err != nil {
			cancel()
			fmt.Fprintf(stderr, "serve: -retention: %v\n", err)
			return exitUsage
		}
		// Runs before st.Close (deferred earlier): stop pruning first.
		defer func() { cancel(); wait() }()
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
	// Notifications (Slack / webhook) when runs and suite runs finish. Every
	// built-in store keeps the channels.
	var notifier *notify.Notifier
	var onSuiteFinish func(model.SuiteRun)
	if ns, ok := st.(store.NotificationStore); ok {
		notifier = notify.New(notify.Options{Store: ns, PublicURL: *publicURL, Logger: log})
		ropts.OnFinish = notifier.RunFinished
		onSuiteFinish = notifier.SuiteFinished
	}
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
		// Not GracefulStop: worker streams never end on their own, so it
		// would wait forever and leave a half-dead master that workers stay
		// attached to. Active runs are already stopped by mgr.Shutdown
		// (deferred calls run after it); Stop drops the streams so workers
		// reconnect to the next master.
		defer gs.Stop()
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

	// Test suites run their tests through mgr too. Suite runs left active
	// by a previous process are recorded as error (like runs above).
	var suites *suite.Service
	if ss, ok := st.(suite.Store); ok {
		suites = suite.New(suite.Options{Store: ss, Runner: mgr, Logger: log, OnFinish: onSuiteFinish})
		if n, err := suites.Recover(ctx); err != nil {
			log.Warn("recover suite runs", "error", err)
		} else if n > 0 {
			log.Info("marked interrupted suite runs as error", "count", n)
		}
	}

	// The scheduler starts planned runs through mgr (same allowlist and
	// validation as the API) and start suite runs through suites for
	// suite schedules. Every built-in store supports schedules.
	var sched *scheduler.Scheduler
	schedDone := make(chan struct{})
	schedCtx, stopSched := context.WithCancel(context.Background())
	defer stopSched()
	if ss, ok := st.(scheduler.Store); ok {
		sched = scheduler.New(scheduler.Options{Store: ss, Runner: mgr, Suites: suiteStarterOrNil(suites), Logger: log})
		go func() { sched.Run(schedCtx); close(schedDone) }()
	} else {
		close(schedDone)
	}

	ui, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		fmt.Fprintf(stderr, "ui: %v\n", err)
		return exitRun
	}
	srv := &http.Server{
		Addr: *addr,
		Handler: api.NewServer(api.Config{
			Store: st, Runner: mgr, UI: ui, Version: version,
			Logger: log, AuthToken: *token, DevCORS: *devCORS, Retention: retention,
			Scheduler: schedulerOrNil(sched),
			Suites:    suitesOrNil(suites),
			Notifier:  notifierOrNil(notifier),
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	fmt.Fprintf(stdout, "goload %s listening on %s (store: %s)\n", version, *addr, spec.String())

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(stderr, "http: %v\n", err)
			return exitRun
		}
	case <-ctx.Done():
	}
	// Restore default signal handling: a second Ctrl-C / SIGTERM kills the
	// process if graceful shutdown hangs.
	stop()

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Stop scheduling first so no new run starts while runs are stopped.
	stopSched()
	select {
	case <-schedDone:
	case <-shutdownCtx.Done():
	}
	if suites != nil {
		if err := suites.Shutdown(shutdownCtx); err != nil {
			log.Warn("stop active suite runs", "error", err)
		}
	}
	if err := mgr.Shutdown(shutdownCtx); err != nil {
		log.Warn("stop active runs", "error", err)
	}
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Warn("http shutdown", "error", err)
	}
	// Last: runs stopped above may still have notifications queued.
	if notifier != nil {
		notifier.Close(shutdownCtx)
	}
	return exitOK
}

// notifierOrNil keeps a nil *Notifier from becoming a non-nil
// api.NotificationSender interface value.
func notifierOrNil(n *notify.Notifier) api.NotificationSender {
	if n == nil {
		return nil
	}
	return n
}

// schedulerOrNil keeps a nil *Scheduler from becoming a non-nil
// api.Scheduler interface value.
func schedulerOrNil(s *scheduler.Scheduler) api.Scheduler {
	if s == nil {
		return nil
	}
	return s
}

// defaultDB is the -db default: a SQLite file in the working directory,
// so a plain `serve` keeps its data without any database server.
const defaultDB = "sqlite:./goload.db"

// storeSpec is a parsed -db value.
type storeSpec struct {
	kind string // "memory", "sqlite" or "postgres"
	loc  string // sqlite: file path; postgres: DSN
}

// parseStoreSpec interprets -db: "memory", "sqlite:<path>", a bare path
// ending in .db / .sqlite / .sqlite3, or a PostgreSQL DSN (postgres:// or
// postgresql:// URL, or libpq "host=... dbname=..." form).
func parseStoreSpec(v string) (storeSpec, error) {
	v = strings.TrimSpace(v)
	lower := strings.ToLower(v)
	switch {
	case v == "":
		return storeSpec{}, errors.New(`empty; use "sqlite:<path>", "postgres://..." or "memory"`)
	case lower == "memory":
		return storeSpec{kind: "memory"}, nil
	case strings.HasPrefix(lower, "sqlite://"):
		return sqliteSpec(v[len("sqlite://"):])
	case strings.HasPrefix(lower, "sqlite:"):
		return sqliteSpec(v[len("sqlite:"):])
	case strings.HasPrefix(lower, "postgres://"), strings.HasPrefix(lower, "postgresql://"):
		return storeSpec{kind: "postgres", loc: v}, nil
	case strings.Contains(v, "=") && (strings.Contains(lower, "host=") || strings.Contains(lower, "dbname=")):
		// libpq keyword/value DSN ("host=db dbname=goload ...").
		return storeSpec{kind: "postgres", loc: v}, nil
	case strings.HasSuffix(lower, ".db"), strings.HasSuffix(lower, ".sqlite"), strings.HasSuffix(lower, ".sqlite3"):
		return sqliteSpec(v)
	default:
		return storeSpec{}, fmt.Errorf(`unrecognised store %q; use "sqlite:<path>", a path ending in .db, "postgres://..." or "memory"`, v)
	}
}

func sqliteSpec(path string) (storeSpec, error) {
	if path == "" {
		return storeSpec{}, errors.New(`"sqlite:" needs a file path, e.g. sqlite:./goload.db`)
	}
	return storeSpec{kind: "sqlite", loc: path}, nil
}

// describe is a log-safe location: the SQLite path, or the Postgres host
// and database without credentials.
func (s storeSpec) describe() string {
	switch s.kind {
	case "sqlite":
		if abs, err := filepath.Abs(s.loc); err == nil && s.loc != ":memory:" {
			return abs
		}
		return s.loc
	case "postgres":
		if u, err := url.Parse(s.loc); err == nil && u.Host != "" {
			return u.Host + u.Path
		}
		return "(keyword/value DSN)"
	default:
		return "in-memory"
	}
}

// String is the store description printed on startup.
func (s storeSpec) String() string {
	switch s.kind {
	case "memory":
		return "memory (data is lost on exit)"
	case "sqlite":
		return "sqlite " + s.describe()
	default:
		return "postgres " + s.describe()
	}
}

func openStore(ctx context.Context, spec storeSpec) (store.Store, error) {
	switch spec.kind {
	case "memory":
		return store.NewMemory(), nil
	case "sqlite":
		if dir := filepath.Dir(spec.loc); spec.loc != ":memory:" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("sqlite: create directory %s: %w", dir, err)
			}
		}
		return store.NewSQLite(ctx, spec.loc)
	default:
		return store.NewPostgres(ctx, spec.loc)
	}
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

// suitesOrNil keeps a nil *suite.Service from becoming a non-nil
// api.Suites interface value.
func suitesOrNil(s *suite.Service) api.Suites {
	if s == nil {
		return nil
	}
	return s
}

// suiteStarterOrNil keeps a nil *suite.Service from becoming a non-nil
// scheduler.SuiteStarter interface value.
func suiteStarterOrNil(s *suite.Service) scheduler.SuiteStarter {
	if s == nil {
		return nil
	}
	return s
}
