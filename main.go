package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ThirdAgeStudio/rs3-plus-one-server/internal/api"
	"github.com/ThirdAgeStudio/rs3-plus-one-server/internal/board"
	"github.com/ThirdAgeStudio/rs3-plus-one-server/internal/names"
)

const (
	shutdownGrace     = 5 * time.Second
	nameLookupTimeout = 3 * time.Second
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	address := flag.String("addr", ":8787", "address to listen on")
	timeout := flag.Duration("timeout", board.DefaultTimeout, "how long a listing or application lasts without a poll")
	unlimited := flag.Bool("unlimited", false, "no per-address limits on requests or name lookups, for running the end-to-end tests from one machine")
	flag.Parse()
	if *timeout <= 0 {
		return errors.New("-timeout must be longer than 0")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serverID := rand.Text()
	state := board.New(time.Now, *timeout)
	go sweep(ctx, state)

	var checker api.NameChecker
	if !api.DevBuild {
		checker = names.New(&http.Client{Timeout: nameLookupTimeout}, !*unlimited)
	}

	server := &http.Server{
		Addr:              *address,
		Handler:           api.New(state, checker, !*unlimited, serverID, log),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       time.Minute,
		MaxHeaderBytes:    8 << 10,
	}
	log.Info("listening", "addr", *address, "server", serverID, "dev", api.DevBuild, "timeout", *timeout, "unlimited", *unlimited)

	served := make(chan error, 1)
	go func() { served <- server.ListenAndServe() }()
	select {
	case err := <-served:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}

func sweep(ctx context.Context, state *board.Board) {
	ticker := time.NewTicker(board.SweepEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			state.Sweep()
		}
	}
}
