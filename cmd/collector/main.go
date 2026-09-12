// nabiz-collector, OTLP trafiğini alır, ClickHouse'a yazar ve aynı akıştan
// servis/k8s topolojisini çıkarır.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/erdemkayatr/nabiz/internal/config"
	"github.com/erdemkayatr/nabiz/internal/otlp"
	"github.com/erdemkayatr/nabiz/internal/pipeline"
	chstore "github.com/erdemkayatr/nabiz/internal/storage/clickhouse"
	"github.com/erdemkayatr/nabiz/internal/topology"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLevel(config.String("LOG_LEVEL", "info")),
	}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// --- depolama ---
	chCfg := chstore.DefaultConfig()
	chCfg.Addrs = config.StringSlice("CLICKHOUSE_ADDRS", chCfg.Addrs)
	chCfg.Database = config.String("CLICKHOUSE_DATABASE", chCfg.Database)
	chCfg.Username = config.String("CLICKHOUSE_USERNAME", chCfg.Username)
	chCfg.Password = config.String("CLICKHOUSE_PASSWORD", chCfg.Password)
	chCfg.TTLDays = config.Int("RETENTION_DAYS", chCfg.TTLDays)
	chCfg.MaxOpenConns = config.Int("CLICKHOUSE_MAX_CONNS", chCfg.MaxOpenConns)

	store, err := openStoreWithRetry(ctx, chCfg, log)
	if err != nil {
		log.Error("clickhouse'a bağlanılamadı", "err", err)
		os.Exit(1)
	}
	defer store.Close()

	if err := store.Migrate(ctx); err != nil {
		log.Error("şema oluşturulamadı", "err", err)
		os.Exit(1)
	}

	// --- topoloji ---
	topoCfg := topology.DefaultConfig()
	topoCfg.PairTTL = config.Duration("TOPOLOGY_PAIR_TTL", topoCfg.PairTTL)
	topoCfg.FlushInterval = config.Duration("TOPOLOGY_FLUSH_INTERVAL", topoCfg.FlushInterval)
	topoCfg.MaxPendingPerShard = config.Int("TOPOLOGY_MAX_PENDING_PER_SHARD", topoCfg.MaxPendingPerShard)
	topoCfg.IncludeNodeDimension = config.Bool("TOPOLOGY_INCLUDE_NODE", topoCfg.IncludeNodeDimension)

	topo := topology.New(topoCfg, store, log)
	topo.Start(ctx)
	defer topo.Stop()

	// --- pipeline ---
	pipeCfg := pipeline.DefaultConfig()
	pipeCfg.QueueSize = config.Int("QUEUE_SIZE", pipeCfg.QueueSize)
	pipeCfg.Workers = config.Int("WORKERS", pipeCfg.Workers)
	pipeCfg.BatchSize = config.Int("BATCH_SIZE", pipeCfg.BatchSize)
	pipeCfg.FlushInterval = config.Duration("FLUSH_INTERVAL", pipeCfg.FlushInterval)

	pipe := pipeline.New(pipeCfg, log, store, topo)
	pipe.Start(ctx)

	// --- alıcı ---
	recvCfg := otlp.Config{
		GRPCAddr: config.String("OTLP_GRPC_ADDR", ":4317"),
		HTTPAddr: config.String("OTLP_HTTP_ADDR", ":4318"),
	}
	recv := otlp.New(recvCfg, pipe, log)

	// --- iç gözlem ucu ---
	go serveDebug(config.String("DEBUG_ADDR", ":8888"), recv, pipe, topo, store, log)

	log.Info("nabiz-collector çalışıyor",
		"otlp_grpc", recvCfg.GRPCAddr,
		"otlp_http", recvCfg.HTTPAddr,
		"clickhouse", chCfg.Addrs,
		"workers", pipeCfg.Workers,
		"queue_size", pipeCfg.QueueSize,
	)

	if err := recv.Serve(ctx, recvCfg); err != nil {
		log.Error("alıcı hata verdi", "err", err)
	}

	// Kapanış: önce kuyruğu boşalt, sonra topolojiyi yaz.
	log.Info("kapanıyor, kuyruk boşaltılıyor")
	pipe.Stop()
}

// openStoreWithRetry, ClickHouse'un container'dan önce ayağa kalkmamış olma
// ihtimaline karşı bekler. compose ve k8s'te sık karşılaşılan durum.
func openStoreWithRetry(ctx context.Context, cfg chstore.Config, log *slog.Logger) (*chstore.Store, error) {
	var lastErr error
	for attempt := 1; attempt <= 30; attempt++ {
		store, err := chstore.Open(ctx, cfg, log)
		if err == nil {
			return store, nil
		}
		lastErr = err
		log.Warn("clickhouse hazır değil, yeniden denenecek", "deneme", attempt, "err", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return nil, lastErr
}

func serveDebug(addr string, recv *otlp.Receiver, pipe *pipeline.Pipeline, topo *topology.Builder, store *chstore.Store, log *slog.Logger) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, _ *http.Request) {
		rs, ps, ts, ss := recv.Stats(), pipe.Stats(), topo.Stats(), store.Stats()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"receiver": map[string]uint64{
				"spans_received": rs.SpansReceived.Load(),
				"spans_dropped":  rs.SpansDropped.Load(),
				"requests":       rs.Requests.Load(),
				"errors":         rs.Errors.Load(),
			},
			"pipeline": map[string]any{
				"enqueued":    ps.Enqueued.Load(),
				"dropped":     ps.Dropped.Load(),
				"processed":   ps.Processed.Load(),
				"failed":      ps.Failed.Load(),
				"queue_depth": ps.QueueDepth.Load(),
			},
			"topology": map[string]uint64{
				"paired":          ts.Paired.Load(),
				"inferred_peers":  ts.InferredPeers.Load(),
				"entry_points":    ts.EntryPoints.Load(),
				"evicted":         ts.Evicted.Load(),
				"edges_written":   ts.EdgesWritten.Load(),
				"pending_dropped": ts.PendingDropped.Load(),
			},
			"storage": map[string]any{
				"spans_written":    ss.SpansWritten.Load(),
				"edges_written":    ss.EdgesWritten.Load(),
				"write_errors":     ss.WriteErrors.Load(),
				"write_latency_us": ss.WriteLatency.Load(),
			},
		})
	})
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("debug ucu kapandı", "err", err)
	}
}

func parseLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
