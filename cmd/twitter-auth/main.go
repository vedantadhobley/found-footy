// twitter-auth runs the raw-Firefox profile capture and status service.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/twitterauth"
)

var (
	gitSHA  = "unknown"
	builtAt = "unknown"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.LoadFor(config.BinaryTwitterAuth)
	if err != nil {
		log.Error("configuration rejected", "action", "config_error", "error", err)
		os.Exit(1)
	}
	authCfg := cfg.TwitterAuth
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	capturer := twitterauth.NewCapturer(
		twitterauth.SQLiteSource{ProfileDir: authCfg.ProfileDir},
		authCfg.CookieFile,
		authCfg.PollInterval,
		twitterauth.BuildInfo{GitSHA: gitSHA, BuiltAt: builtAt, ImageTag: os.Getenv("IMAGE_TAG")},
	)
	go capturer.Run(ctx)

	server := &http.Server{
		Addr:              authCfg.ListenAddr,
		Handler:           capturer.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.ListenAndServe() }()
	log.Info("raw Firefox auth capture service starting",
		"action", "service_starting",
		"listen", authCfg.ListenAddr,
		"profile_dir", authCfg.ProfileDir,
		"cookie_file", authCfg.CookieFile,
		"git_sha", gitSHA,
		"built_at", builtAt,
		"image_tag", os.Getenv("IMAGE_TAG"),
	)

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Error("HTTP shutdown failed", "action", "shutdown_error", "error", err)
		}
	case err := <-serverDone:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Error("HTTP server failed", "action", "server_error", "error", err)
			os.Exit(1)
		}
	}
}
