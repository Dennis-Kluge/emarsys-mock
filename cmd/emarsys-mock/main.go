// Command emarsys-mock serves an API-compatible mock of the SAP Emarsys Suite
// API together with a dashboard for inspecting and editing the mocked data.
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

	// Embeds the IANA database so Europe/Vienna resolves in a scratch or
	// distroless image, which carries no /usr/share/zoneinfo.
	_ "time/tzdata"

	"github.com/dennis-kluge/emarsys-mock/internal/config"
	"github.com/dennis-kluge/emarsys-mock/internal/server"
	"github.com/dennis-kluge/emarsys-mock/internal/store"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if err := run(logger); err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	db, err := store.Open(cfg.DSN)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := db.Migrate(); err != nil {
		return err
	}

	app := server.New(cfg, db, logger)
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening",
			"addr", cfg.Addr,
			"db", cfg.DSN,
			"read_only", cfg.ReadOnly,
			"export_timezone", cfg.ExportLocation.String())
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := srv.Shutdown(shutdownCtx)
		// Wait for webhook deliveries so a trigger accepted just before the
		// signal is not dropped on the floor.
		app.Shutdown()
		return err
	}
}
