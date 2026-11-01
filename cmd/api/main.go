// Command api is the Little World Library HTTP server.
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

	"littleworld/internal/auth"
	"littleworld/internal/books"
	"littleworld/internal/bundles"
	"littleworld/internal/catalog"
	"littleworld/internal/config"
	"littleworld/internal/db"
	"littleworld/internal/httpapi"
	"littleworld/internal/library"
	"littleworld/internal/orders"
	"littleworld/internal/reading"
	"littleworld/internal/storage"
	"littleworld/internal/vnpay"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "api:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := config.Load()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	if err := cfg.Validate(); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	store, err := openStorage(cfg, logger)
	if err != nil {
		return err
	}

	// A nil *storage.Client in a books.Storage interface is not nil, so the service
	// would panic on upload. Keep the interface nil when storage is absent.
	var (
		bookStore   books.Storage
		bundleStore bundles.Storage
		presigner   catalog.Presigner
	)
	if store != nil {
		bookStore = store
		bundleStore = store
		presigner = store
	}

	catalogService := catalog.New(pool, presigner)

	srv := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: httpapi.New(httpapi.Deps{
			DB:             pool,
			Auth:           auth.New(pool),
			Log:            logger,
			Storage:        store,
			Books:          books.New(pool, bookStore),
			Bundles:        bundles.New(pool, bundleStore),
			Catalog:        catalogService,
			Reading:        reading.New(pool, presigner),
			Library:        library.New(pool, catalogService),
			Orders:         orders.New(pool, vnpay.New(cfg.Vnpay)),
			SecureCookies:  cfg.AppEnv != "dev",
			VnpayReturnURL: cfg.VnpayReturnURL,
		}).Router(),
		// Uploads stream through the API, so read/write budgets are generous;
		// the header and idle budgets stay tight.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       10 * time.Minute,
		WriteTimeout:      10 * time.Minute,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	listenErr := make(chan error, 1)
	go func() {
		logger.Info("api listening", "addr", cfg.HTTPAddr, "env", cfg.AppEnv)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			listenErr <- err
			return
		}
		listenErr <- nil
	}()

	select {
	case err := <-listenErr:
		return err
	case <-ctx.Done():
	}
	logger.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown failed", "err", err)
	}
	return nil
}

// openStorage returns nil when S3 settings are absent, so auth and catalog can run
// without object storage; upload endpoints reject with ErrNotConfigured.
func openStorage(cfg config.Config, logger *slog.Logger) (*storage.Client, error) {
	if !cfg.S3.Configured() {
		logger.Warn("object storage is not configured; uploads will be unavailable")
		return nil, nil
	}
	store, err := storage.New(cfg.S3)
	if err != nil {
		return nil, err
	}
	logger.Info("object storage configured", "endpoint", cfg.S3.Endpoint, "bucket", cfg.S3.Bucket)
	return store, nil
}
