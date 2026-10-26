package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/cravingPixels/dimuda/internal/app"
	"github.com/cravingPixels/dimuda/internal/changelog"
	"github.com/cravingPixels/dimuda/internal/config"
	"github.com/cravingPixels/dimuda/internal/docker"
	"github.com/cravingPixels/dimuda/internal/gitsource"
	"github.com/cravingPixels/dimuda/internal/notify"
	"github.com/cravingPixels/dimuda/internal/overrides"
	"github.com/cravingPixels/dimuda/internal/privdrop"
	"github.com/cravingPixels/dimuda/internal/registry"
	"github.com/cravingPixels/dimuda/internal/scanner"
	"github.com/cravingPixels/dimuda/internal/web"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	dropped, err := privdrop.Drop(privdrop.OS(), privdrop.Identity{UID: cfg.PUID, GID: cfg.PGID, Groups: cfg.ExtraGIDs}, cfg.DataDir)
	if err != nil {
		return err
	}
	log.Info("running as", "uid", os.Geteuid(), "gid", os.Getegid(), "dropped_from_root", dropped)

	ov, err := overrides.LoadLayered(cfg.OverridesFile, cfg.UserOverrides)
	if err != nil {
		return err
	}

	httpClient := &http.Client{Timeout: 30 * time.Second}
	s := &scanner.Scanner{
		Dirs:      cfg.ComposeDirs,
		GitRepos:  cfg.GitRepos,
		Git:       gitsource.NewSyncer(filepath.Join(cfg.DataDir, "git"), cfg.GitToken),
		Registry:  registry.New(&http.Client{Timeout: 90 * time.Second}),
		Releases:  changelog.New(httpClient, cfg.GitHubToken, cfg.ScanInterval),
		Overrides: ov,
		Logger:    log,
	}
	if cfg.DockerHost != "" {
		dc, err := docker.New(cfg.DockerHost)
		if err != nil {
			return err
		}
		s.Docker = dc
	}

	var notifier app.Notifier
	if cfg.NtfyEnabled() {
		notifier = notify.NewNtfy(httpClient, cfg.NtfyURL, cfg.NtfyTopic, cfg.NtfyToken)
	}

	a, err := app.New(s, notifier, cfg.DataDir, cfg.PublicURL, log)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv, err := web.New(ctx, a, log)
	if err != nil {
		return err
	}
	httpServer := &http.Server{Addr: cfg.ListenAddr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}

	go a.Run(ctx, cfg.ScanInterval)
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		httpServer.Shutdown(shutdown)
	}()

	log.Info("listening", "addr", cfg.ListenAddr, "compose_dirs", len(cfg.ComposeDirs), "git_repos", len(cfg.GitRepos),
		"docker", cfg.DockerHost != "", "ntfy", cfg.NtfyEnabled(), "interval", cfg.ScanInterval)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
