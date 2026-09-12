// Package clickhouse, span ve topoloji kenarlarını ClickHouse'a yazar.
//
// Yazma yolu native protokol + kolon bazlı batch üzerinden gider: satır satır
// INSERT yerine hazır bir batch'e kolon kolon append yapılır. Bu, aynı veri
// için 10-20 kat daha az CPU demek.
package clickhouse

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/erdemkayatr/nabiz/internal/model"
	"github.com/erdemkayatr/nabiz/internal/topology"
)

// Config, ClickHouse bağlantı ayarları.
type Config struct {
	Addrs    []string
	Database string
	Username string
	Password string

	// TTLDays, ham span'lerin saklanma süresi.
	TTLDays int
	// MaxOpenConns, eşzamanlı yazan worker sayısına göre seçilir.
	MaxOpenConns int
	DialTimeout  time.Duration
}

// DefaultConfig, yerel geliştirme için varsayılanlar.
func DefaultConfig() Config {
	return Config{
		Addrs:        []string{"localhost:9000"},
		Database:     "nabiz",
		Username:     "default",
		TTLDays:      7,
		MaxOpenConns: 8,
		DialTimeout:  10 * time.Second,
	}
}

// Stats, depolama sayaçları.
type Stats struct {
	SpansWritten atomic.Uint64
	EdgesWritten atomic.Uint64
	WriteErrors  atomic.Uint64
	WriteLatency atomic.Int64 // son batch, mikrosaniye
}

// Store, ClickHouse bağlantısını ve yazma yollarını tutar.
type Store struct {
	conn  driver.Conn
	cfg   Config
	log   *slog.Logger
	stats Stats
}

// Open, bağlantıyı açar ve canlılığını doğrular.
func Open(ctx context.Context, cfg Config, log *slog.Logger) (*Store, error) {
	if len(cfg.Addrs) == 0 {
		cfg = DefaultConfig()
	}
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: cfg.Addrs,
		Auth: clickhouse.Auth{
			Database: "default", // veritabanı henüz yoksa bağlanabilmek için
			Username: cfg.Username,
			Password: cfg.Password,
		},
		Compression:  &clickhouse.Compression{Method: clickhouse.CompressionLZ4},
		MaxOpenConns: cfg.MaxOpenConns,
		MaxIdleConns: cfg.MaxOpenConns,
		DialTimeout:  cfg.DialTimeout,
		Settings: clickhouse.Settings{
			// Yazma kuyruğu birikirse ClickHouse'un bize geri basınç
			// uygulaması normaldir; pipeline zaten sınırlı.
			"max_execution_time": 60,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("clickhouse bağlantısı açılamadı: %w", err)
	}
	if err := conn.Ping(ctx); err != nil {
		return nil, fmt.Errorf("clickhouse ping başarısız: %w", err)
	}
	return &Store{conn: conn, cfg: cfg, log: log}, nil
}

// Close, bağlantıyı kapatır.
func (s *Store) Close() error { return s.conn.Close() }

// Conn, sorgu tarafının (API) kullanması için bağlantıyı verir.
func (s *Store) Conn() driver.Conn { return s.conn }

// Database, aktif veritabanı adı.
func (s *Store) Database() string { return s.cfg.Database }

// Stats, sayaçlara erişim verir.
func (s *Store) Stats() *Stats { return &s.stats }

// Migrate, veritabanını ve tabloları idempotent olarak oluşturur.
func (s *Store) Migrate(ctx context.Context) error {
	if err := s.conn.Exec(ctx, "CREATE DATABASE IF NOT EXISTS "+s.cfg.Database); err != nil {
		return fmt.Errorf("veritabanı oluşturulamadı: %w", err)
	}
	ttl := strconv.Itoa(s.cfg.TTLDays)
	for i, stmt := range Schema {
		stmt = strings.ReplaceAll(stmt, "{{TTL_DAYS}}", ttl)
		// Tablolar hedef veritabanında oluşsun.
		stmt = qualify(stmt, s.cfg.Database)
		if err := s.conn.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("şema adımı %d başarısız: %w", i, err)
		}
	}
	s.log.Info("clickhouse şeması hazır", "database", s.cfg.Database, "ttl_days", s.cfg.TTLDays)
	return nil
}

// qualify, DDL içindeki tablo adlarını veritabanı adıyla niteler.
func qualify(stmt, db string) string {
	repl := strings.NewReplacer(
		"CREATE TABLE IF NOT EXISTS ", "CREATE TABLE IF NOT EXISTS "+db+".",
		"CREATE MATERIALIZED VIEW IF NOT EXISTS ", "CREATE MATERIALIZED VIEW IF NOT EXISTS "+db+".",
		" TO operation_stats ", " TO "+db+".operation_stats ",
		" TO trace_index ", " TO "+db+".trace_index ",
		"FROM spans", "FROM "+db+".spans",
	)
	return repl.Replace(stmt)
}

// --- yazma: span'ler ---

// Name, pipeline.Processor arayüzü için.
func (s *Store) Name() string { return "clickhouse-spans" }

// Process, bir batch span'i tek seferde yazar.
func (s *Store) Process(ctx context.Context, spans []*model.Span) error {
	if len(spans) == 0 {
		return nil
	}
	start := time.Now()

	batch, err := s.conn.PrepareBatch(ctx, qualifyInsert(InsertSpansSQL, s.cfg.Database))
	if err != nil {
		s.stats.WriteErrors.Add(1)
		return fmt.Errorf("span batch hazırlanamadı: %w", err)
	}

	for _, sp := range spans {
		evTS, evName, evAttrs := flattenEvents(sp.Events)
		linkTrace, linkSpan := flattenLinks(sp.Links)

		err = batch.Append(
			sp.Timestamp, sp.TraceID, sp.SpanID, sp.ParentSpanID, sp.TraceState, sp.Flags,
			sp.Name, sp.Kind.String(), sp.DurationNS, sp.StatusCode.String(), sp.StatusMessage,
			sp.ServiceName, sp.ServiceNamespace, sp.ServiceVersion, sp.ServiceInstance, sp.SDKLanguage,
			sp.K8sCluster, sp.K8sNamespace, sp.K8sPod, sp.K8sWorkload, sp.K8sNode, sp.K8sContainer,
			sp.HTTPMethod, sp.HTTPRoute, sp.HTTPStatusCode, sp.HTTPURL,
			sp.DBSystem, sp.DBName, sp.DBStatement,
			sp.RPCSystem, sp.RPCService, sp.RPCMethod,
			sp.MessagingSystem, sp.MessagingDest,
			sp.PeerService, sp.PeerAddress, sp.PeerPort,
			nilSafeMap(sp.ResourceAttrs), nilSafeMap(sp.SpanAttrs),
			evTS, evName, evAttrs,
			linkTrace, linkSpan,
		)
		if err != nil {
			s.stats.WriteErrors.Add(1)
			return fmt.Errorf("span append başarısız: %w", err)
		}
	}

	if err := batch.Send(); err != nil {
		s.stats.WriteErrors.Add(1)
		return fmt.Errorf("span batch gönderilemedi: %w", err)
	}
	s.stats.SpansWritten.Add(uint64(len(spans)))
	s.stats.WriteLatency.Store(time.Since(start).Microseconds())
	return nil
}

// --- yazma: topoloji kenarları ---

// WriteEdges, topology.EdgeSink arayüzünü karşılar.
func (s *Store) WriteEdges(ctx context.Context, edges []topology.EdgeSample) error {
	if len(edges) == 0 {
		return nil
	}
	batch, err := s.conn.PrepareBatch(ctx, qualifyInsert(InsertEdgesSQL, s.cfg.Database))
	if err != nil {
		s.stats.WriteErrors.Add(1)
		return fmt.Errorf("edge batch hazırlanamadı: %w", err)
	}

	for i := range edges {
		e := &edges[i]
		args := make([]any, 0, 14+topology.BucketCount)
		args = append(args,
			e.Bucket, e.Key.Client, e.Key.Server,
			e.Key.ClientNamespace, e.Key.ClientWorkload,
			e.Key.ServerNamespace, e.Key.ServerWorkload,
			e.Key.ClientNode, e.Key.ServerNode, e.Key.ConnType.String(),
			e.Calls, e.Errors, e.DurationSumNS, e.DurationMaxNS,
		)
		for _, b := range e.Buckets {
			args = append(args, b)
		}
		if err := batch.Append(args...); err != nil {
			s.stats.WriteErrors.Add(1)
			return fmt.Errorf("edge append başarısız: %w", err)
		}
	}

	if err := batch.Send(); err != nil {
		s.stats.WriteErrors.Add(1)
		return fmt.Errorf("edge batch gönderilemedi: %w", err)
	}
	s.stats.EdgesWritten.Add(uint64(len(edges)))
	return nil
}

func qualifyInsert(sql, db string) string {
	return strings.Replace(sql, "INSERT INTO ", "INSERT INTO "+db+".", 1)
}

func flattenEvents(events []model.Event) ([]time.Time, []string, []map[string]string) {
	if len(events) == 0 {
		return []time.Time{}, []string{}, []map[string]string{}
	}
	ts := make([]time.Time, len(events))
	names := make([]string, len(events))
	attrs := make([]map[string]string, len(events))
	for i, e := range events {
		ts[i] = e.Timestamp
		names[i] = e.Name
		attrs[i] = nilSafeMap(e.Attrs)
	}
	return ts, names, attrs
}

func flattenLinks(links []model.Link) ([]string, []string) {
	if len(links) == 0 {
		return []string{}, []string{}
	}
	traces := make([]string, len(links))
	spans := make([]string, len(links))
	for i, l := range links {
		traces[i] = l.TraceID
		spans[i] = l.SpanID
	}
	return traces, spans
}

// nilSafeMap, nil map'i boş map'e çevirir: ClickHouse sürücüsü Map kolonuna
// nil kabul etmiyor.
func nilSafeMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}
