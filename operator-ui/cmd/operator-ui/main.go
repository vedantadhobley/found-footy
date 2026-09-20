// Command operator-ui runs the standalone Found Footy evidence browser.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/vedantadhobley/found-footy-operator-ui/internal/config"
	"github.com/vedantadhobley/found-footy-operator-ui/internal/live"
	"github.com/vedantadhobley/found-footy-operator-ui/internal/media"
	"github.com/vedantadhobley/found-footy-operator-ui/internal/store"
	"github.com/vedantadhobley/found-footy-operator-ui/internal/web"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		if err := healthcheck(); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(log); err != nil {
		log.Error("operator UI stopped", "error", err)
		os.Exit(1)
	}
}

// run wires the independent read stores, change watcher, and HTTP server.
func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	repo, err := store.New(ctx, cfg.PGDSN, cfg.PGMaxConns)
	if err != nil {
		return err
	}
	defer repo.Close()
	objects, err := media.New(ctx, cfg)
	if err != nil {
		return err
	}
	hub := live.NewHub()
	watcher := live.Watcher{
		Source: repo, Hub: hub, PollInterval: cfg.PollInterval, ResyncInterval: cfg.ResyncInterval,
		OnError: func(err error) { log.Error("database change poll failed", "error", err) },
	}
	go watcher.Run(ctx)
	handler, err := web.New(repo, objects, hub, cfg.Environment, log)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr: cfg.HTTPAddr, Handler: handler, ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Info("operator UI ready", "addr", cfg.HTTPAddr, "environment", cfg.Environment)
		errCh <- server.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// healthcheck probes the in-container HTTP listener without external tooling.
func healthcheck() error {
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get("http://127.0.0.1:8080/healthz")
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("health status %d", response.StatusCode)
	}
	return nil
}
