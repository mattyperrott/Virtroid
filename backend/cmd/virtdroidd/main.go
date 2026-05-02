package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"virtdroid/backend/internal/config"
	"virtdroid/backend/internal/httpapi"
	"virtdroid/backend/internal/store"
)

func main() {
	cfg := config.LoadServer()

	ctx := context.Background()

	pg, err := store.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer pg.Close()

	if err := pg.EnsureSchema(ctx); err != nil {
		log.Fatalf("ensure schema: %v", err)
	}

	server := &http.Server{
		Addr:              cfg.BindAddr,
		Handler:           httpapi.New(cfg, pg),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("virtdroidd listening on %s", cfg.BindAddr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}
