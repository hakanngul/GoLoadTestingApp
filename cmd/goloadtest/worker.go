package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/hakanngul/GoLoadTestingApp/internal/cluster"
)

// cmdWorker connects to a master and generates load on its behalf until
// interrupted. It reconnects automatically when the master restarts.
func cmdWorker(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("worker", flag.ContinueOnError)
	flags.SetOutput(stderr)
	master := flags.String("master", os.Getenv("GOLOAD_MASTER"), "master gRPC address, e.g. 10.0.1.5:9090")
	token := flags.String("token", os.Getenv("GOLOAD_WORKER_TOKEN"), "worker token (prefer GOLOAD_WORKER_TOKEN; flags show up in ps)")
	insecure := flags.Bool("insecure", true, "plaintext gRPC (inside a private network); false requires TLS")
	id := flags.String("id", os.Getenv("GOLOAD_WORKER_ID"), "worker id (default: hostname + random suffix)")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	if *master == "" || *token == "" {
		fmt.Fprintln(stderr, "worker: -master and a token (GOLOAD_WORKER_TOKEN) are required")
		return exitUsage
	}

	log := slog.New(slog.NewTextHandler(stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Fprintf(stdout, "goload worker %s connecting to %s\n", version, *master)
	err := cluster.RunWorker(ctx, cluster.WorkerOptions{
		MasterAddr: *master,
		Token:      *token,
		Insecure:   *insecure,
		ID:         *id,
		Version:    version,
		Logger:     log,
	})
	if err != nil {
		fmt.Fprintf(stderr, "worker: %v\n", err)
		return exitRun
	}
	return exitOK
}
