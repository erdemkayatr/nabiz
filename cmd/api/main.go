// nabiz-api, toplanan veriyi sorgulanabilir hale getiren HTTP ucudur.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/erdemkayatr/nabiz/internal/api"
	"github.com/erdemkayatr/nabiz/internal/config"
	"github.com/erdemkayatr/nabiz/internal/identity"
	chstore "github.com/erdemkayatr/nabiz/internal/storage/clickhouse"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := chstore.DefaultConfig()
	cfg.Addrs = config.StringSlice("CLICKHOUSE_ADDRS", cfg.Addrs)
	cfg.Database = config.String("CLICKHOUSE_DATABASE", cfg.Database)
	cfg.Username = config.String("CLICKHOUSE_USERNAME", cfg.Username)
	cfg.Password = config.String("CLICKHOUSE_PASSWORD", cfg.Password)

	var store *chstore.Store
	var err error
	for attempt := 1; attempt <= 30; attempt++ {
		store, err = chstore.Open(ctx, cfg, log)
		if err == nil {
			break
		}
		log.Warn("clickhouse not ready", "attempt", attempt, "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
	if err != nil {
		log.Error("could not connect to clickhouse", "err", err)
		os.Exit(1)
	}
	defer store.Close()

	// --- control plane ---
	ident, err := openIdentityWithRetry(ctx, config.String("POSTGRES_DSN",
		"postgres://nabiz:nabiz@localhost:5432/nabiz?sslmode=disable"), log)
	if err != nil {
		log.Error("could not connect to the control plane database", "err", err)
		os.Exit(1)
	}
	defer ident.Close()

	if err := ident.Migrate(ctx); err != nil {
		log.Error("could not create the control plane schema", "err", err)
		os.Exit(1)
	}

	// Diagnostics tokens are stored encrypted; without a key, storing a token is
	// refused and the UI says so plainly.
	if sealer, err := identity.NewSealer(config.String("SECRET_KEY", "")); err == nil {
		ident.SetSealer(sealer)
	} else {
		log.Warn("NABIZ_SECRET_KEY is not set: diagnostics tokens will not be stored")
	}

	dumpDir := config.String("DUMP_DIR", filepath.Join(os.TempDir(), "nabiz-dumps"))
	if err := os.MkdirAll(dumpDir, 0o750); err != nil {
		log.Error("could not create the dump directory", "dir", dumpDir, "err", err)
		os.Exit(1)
	}

	created, generated, err := ident.Bootstrap(ctx,
		config.String("ADMIN_EMAIL", ""), config.String("ADMIN_PASSWORD", ""))
	if err != nil {
		log.Error("could not create the first administrator", "err", err)
		os.Exit(1)
	}
	if created {
		email := config.String("ADMIN_EMAIL", "admin@nabiz.local")
		if generated != "" {
			// The password is visible here and only here, once. A management panel that
			// opens with a default password is worse than none.
			log.Warn("first administrator created — this password will not be shown again",
				"email", email, "password", generated)
		} else {
			log.Info("first administrator created", "email", email)
		}
	}

	// Clear out expired sessions on a schedule.
	go purgeSessions(ctx, ident, log)

	apiServer := api.New(store.Conn(), cfg.Database, ident, dumpDir, log)

	retention := api.DefaultDumpRetention()
	retention.MaxAge = time.Duration(config.Int("DUMP_RETENTION_DAYS", 7)) * 24 * time.Hour
	retention.MaxBytes = int64(config.Int("DUMP_QUOTA_GB", 10)) << 30
	apiServer.SetDumpRetention(retention)
	apiServer.StartDumpJanitor(ctx)

	addr := config.String("API_ADDR", ":8080")
	srv := &http.Server{
		Addr:              addr,
		Handler:           apiServer.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Info("nabiz-api running", "addr", addr, "clickhouse", cfg.Addrs,
		"dump_dir", dumpDir, "dump_kota_gb", retention.MaxBytes>>30,
		"dump_saklama_gun", int(retention.MaxAge.Hours()/24))
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("the api stopped", "err", err)
		os.Exit(1)
	}
}

// openIdentityWithRetry waits when the Postgres container comes up after the
// API. A common situation under both compose and Kubernetes.
func openIdentityWithRetry(ctx context.Context, dsn string, log *slog.Logger) (*identity.Store, error) {
	var lastErr error
	for attempt := 1; attempt <= 30; attempt++ {
		store, err := identity.Open(ctx, dsn, log)
		if err == nil {
			return store, nil
		}
		lastErr = err
		log.Warn("postgres not ready, will retry", "attempt", attempt, "err", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return nil, lastErr
}

func purgeSessions(ctx context.Context, ident *identity.Store, log *slog.Logger) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := ident.PurgeExpiredSessions(ctx)
			if err != nil {
				log.Warn("session cleanup failed", "err", err)
				continue
			}
			if n > 0 {
				log.Info("expired sessions deleted", "count", n)
			}
		}
	}
}
