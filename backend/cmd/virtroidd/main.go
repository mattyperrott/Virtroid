package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"virtroid/backend/internal/appcatalog"
	"virtroid/backend/internal/config"
	"virtroid/backend/internal/httpapi"
	"virtroid/backend/internal/store"
)

func main() {
	cfg := config.LoadServer()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pg, err := store.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer pg.Close()

	if err := pg.EnsureSchema(ctx); err != nil {
		log.Fatalf("ensure schema: %v", err)
	}

	go sessionReaperLoop(ctx, pg, cfg)
	go appCatalogSyncLoop(ctx, pg, cfg)

	server := &http.Server{
		Addr:              cfg.BindAddr,
		Handler:           httpapi.New(cfg, pg),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("virtroidd listening on %s", cfg.BindAddr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}

func appCatalogSyncLoop(ctx context.Context, pg *store.Store, cfg config.ServerConfig) {
	if !cfg.AppCatalogSyncEnabled {
		log.Printf("app catalog sync disabled")
		return
	}
	interval := cfg.AppCatalogSyncInterval
	if interval <= 0 {
		interval = 12 * time.Hour
	}

	runSync := func() {
		syncCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		count, err := appcatalog.SyncFDroid(syncCtx, pg, cfg.AppCatalogSyncURL, cfg.AppCatalogSyncSHA256, cfg.AppCatalogSyncMaxApps)
		if err != nil {
			log.Printf("app catalog sync failed: %v", err)
			return
		}
		log.Printf("app catalog sync: upserted=%d source=fdroid", count)
	}

	runSync()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runSync()
		}
	}
}

func sessionReaperLoop(ctx context.Context, pg *store.Store, cfg config.ServerConfig) {
	interval := cfg.SessionReaperInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}

	runReaper := func() {
		result, err := pg.ReapStaleSessions(ctx, cfg.ActiveSessionTimeout, cfg.RuntimeIdleTimeout)
		if err != nil {
			log.Printf("session reaper failed: %v", err)
			return
		}
		if result.ExpiredPendingSessions > 0 ||
			result.StaleActiveSessions > 0 ||
			result.RevokedRuntimeCapabilities > 0 ||
			result.PrunedRuntimeCapabilityNonces > 0 ||
			len(result.StoppedRuntimeIDs) > 0 {
			log.Printf(
				"session reaper: expired_pending=%d stale_active=%d revoked_capabilities=%d pruned_capability_nonces=%d runtimes_queued_to_stop=%d",
				result.ExpiredPendingSessions,
				result.StaleActiveSessions,
				result.RevokedRuntimeCapabilities,
				result.PrunedRuntimeCapabilityNonces,
				len(result.StoppedRuntimeIDs),
			)
		}
	}

	runReaper()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runReaper()
		}
	}
}
