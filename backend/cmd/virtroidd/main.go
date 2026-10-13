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
	"virtroid/backend/internal/siaindexd"
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

	sia, err := httpapi.NewSiaRegistry(cfg)
	if err != nil {
		log.Fatalf("sia storage: %v", err)
	}
	// Accounts only become paid through deposits, so without wallets nothing
	// is charged.
	if cfg.TronDepositXPub != "" {
		pg.SetPricing(store.Pricing{
			RuntimeHourMicro:     cfg.RuntimePriceHourMicro,
			StorageGiBMonthMicro: cfg.StoragePriceGiBMonthMicro,
		})
	}

	go sessionReaperLoop(ctx, pg, cfg)
	go appCatalogSyncLoop(ctx, pg, cfg)
	go provisioningLoop(ctx, pg, cfg)
	go siaReleaseLoop(ctx, pg, sia)
	go usageLoop(ctx, pg)

	server := &http.Server{
		Addr:              cfg.BindAddr,
		Handler:           httpapi.New(cfg, pg),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
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
		runBackgroundIteration("app catalog sync", func() {
			syncCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			count, err := appcatalog.SyncFDroid(syncCtx, pg, cfg.AppCatalogSyncURL, cfg.AppCatalogSyncSHA256, cfg.AppCatalogSyncMaxApps)
			if err != nil {
				log.Printf("app catalog sync failed: %v", err)
				return
			}
			log.Printf("app catalog sync: upserted=%d source=fdroid", count)
		})
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
		runBackgroundIteration("session reaper", func() {
			result, err := pg.ReapStaleSessions(ctx, cfg.ActiveSessionTimeout, cfg.RuntimeIdleTimeout)
			if err != nil {
				log.Printf("session reaper failed: %v", err)
				return
			}
			prunedRuntimeLogs, err := pg.PruneRuntimeLogs(ctx, cfg.RuntimeLogRetention)
			if err != nil {
				log.Printf("runtime log retention sweep failed: %v", err)
				return
			}
			if result.ExpiredPendingSessions > 0 ||
				result.StaleActiveSessions > 0 ||
				result.RevokedRuntimeCapabilities > 0 ||
				result.PrunedRuntimeCapabilityNonces > 0 ||
				result.PrunedBlobKeyHandoffs > 0 ||
				len(result.StoppedRuntimeIDs) > 0 ||
				prunedRuntimeLogs > 0 {
				log.Printf(
					"session reaper: expired_pending=%d stale_active=%d revoked_capabilities=%d pruned_capability_nonces=%d pruned_blob_key_handoffs=%d pruned_runtime_logs=%d runtimes_queued_to_stop=%d",
					result.ExpiredPendingSessions,
					result.StaleActiveSessions,
					result.RevokedRuntimeCapabilities,
					result.PrunedRuntimeCapabilityNonces,
					result.PrunedBlobKeyHandoffs,
					prunedRuntimeLogs,
					len(result.StoppedRuntimeIDs),
				)
			}
		})
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

// usageLoop meters runtime use every minute: trial time, and charges to paid
// accounts. It stops runtimes whose trial hour or balance is used up.
func usageLoop(ctx context.Context, pg *store.Store) {
	meter := func() {
		runBackgroundIteration("usage metering", func() {
			result, err := pg.MeterUsage(ctx)
			if err != nil {
				log.Printf("usage metering failed: %v", err)
				return
			}
			if result.StorageCharges > 0 || len(result.StoppedRuntimeIDs) > 0 {
				log.Printf("usage: runtime_charges=%d storage_charges=%d trial_seconds=%d runtimes_stopped=%d",
					result.RuntimeCharges, result.StorageCharges, result.TrialSeconds, len(result.StoppedRuntimeIDs))
			}
		})
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			meter()
		}
	}
}

// provisioningLoop applies the runtime VM policy: it gives up on VMs that
// never came up, retires unused ones, and keeps warm spares topped up. The
// provisioner service does the actual creating and deleting.
func provisioningLoop(ctx context.Context, pg *store.Store, cfg config.ServerConfig) {
	if cfg.ProvisionerMaxVMs <= 0 || len(cfg.ProvisionedRegions) == 0 {
		log.Printf("runtime VM provisioning disabled")
		return
	}
	policy := store.ProvisioningPolicy{
		Regions:      cfg.ProvisionedRegions,
		MaxVMs:       cfg.ProvisionerMaxVMs,
		WarmSpares:   cfg.ProvisionerWarmSpares,
		SetupTimeout: cfg.ProvisionerSetupTimeout,
		RetireAfter:  cfg.ProvisionerRetireAfter,
	}
	log.Printf("runtime VM provisioning: regions=%v max_vms=%d warm_spares=%d", policy.Regions, policy.MaxVMs, policy.WarmSpares)
	reconcile := func() {
		runBackgroundIteration("provisioning", func() {
			summary, err := pg.ReconcileProvisionedVMs(ctx, policy)
			if err != nil {
				log.Printf("provisioning reconcile failed: %v", err)
				return
			}
			if summary != (store.ProvisioningSummary{}) {
				log.Printf("provisioning: timed_out=%d accounts_gone=%d retired=%d spares_created=%d runtimes_reset=%d",
					summary.TimedOut, summary.AccountsGone, summary.Retired, summary.SparesCreated, summary.RuntimesReset)
			}
		})
	}
	reconcile()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcile()
		}
	}
}

// siaReleaseLoop removes deleted accounts from the Sia indexer, which drops
// their snapshots there, then finishes the deletion of runtimes that only
// waited for those snapshots.
func siaReleaseLoop(ctx context.Context, pg *store.Store, sia *siaindexd.Registry) {
	if sia == nil {
		log.Printf("sia storage disabled")
		return
	}
	release := func() {
		runBackgroundIteration("sia release", func() {
			releaseCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			accountIDs, err := pg.DeletedAccountsOnSia(releaseCtx, 50)
			if err != nil {
				log.Printf("sia release: list deleted accounts: %v", err)
				return
			}
			for _, accountID := range accountIDs {
				if err := sia.DeleteAccount(releaseCtx, accountID); err != nil {
					log.Printf("sia release: account=%s: %v", accountID, err)
					continue
				}
				finished, err := pg.ReleaseAccountSia(releaseCtx, accountID, siaindexd.StoreKind)
				if err != nil {
					log.Printf("sia release: account=%s: record: %v", accountID, err)
					continue
				}
				log.Printf("sia: removed deleted account=%s runtimes_finished=%d", accountID, finished)
			}
		})
	}
	release()
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			release()
		}
	}
}

func runBackgroundIteration(name string, action func()) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("%s panic recovered: %v", name, recovered)
		}
	}()
	action()
}
