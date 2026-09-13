// Package clickhouse writes spans and topology edges to ClickHouse.
//
// The write path goes through the native protocol and column-wise batches:
// instead of row-by-row INSERTs, values are appended column by column to a
// prepared batch. That is 10-20x less CPU for the same data.
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

// Config holds the ClickHouse connection settings.
type Config struct {
	Addrs    []string
	Database string
	Username string
	Password string

	// TTLDays is how long raw spans are kept.
	TTLDays int
	// MaxOpenConns is chosen to match the number of concurrent writing workers.
	MaxOpenConns int
	DialTimeout  time.Duration
}

// DefaultConfig returns defaults suited to local development.
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

// Stats holds the storage counters.
type Stats struct {
	SpansWritten atomic.Uint64
	EdgesWritten atomic.Uint64
	WriteErrors  atomic.Uint64
	WriteLatency atomic.Int64 // the last batch, in microseconds
}

// Store holds the ClickHouse connection and the write paths.
type Store struct {
	conn  driver.Conn
	cfg   Config
	log   *slog.Logger
	stats Stats
}

// Open opens the connection and verifies it is alive.
func Open(ctx context.Context, cfg Config, log *slog.Logger) (*Store, error) {
	if len(cfg.Addrs) == 0 {
		cfg = DefaultConfig()
	}
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: cfg.Addrs,
		Auth: clickhouse.Auth{
			Database: "default", // so we can connect before the database exists
			Username: cfg.Username,
			Password: cfg.Password,
		},
		Compression:  &clickhouse.Compression{Method: clickhouse.CompressionLZ4},
		MaxOpenConns: cfg.MaxOpenConns,
		MaxIdleConns: cfg.MaxOpenConns,
		DialTimeout:  cfg.DialTimeout,
		Settings: clickhouse.Settings{
			// It is fine for ClickHouse to push back when the write queue
			// builds up; the pipeline is already bounded.
			"max_execution_time": 60,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("could not open the clickhouse connection: %w", err)
	}
	if err := conn.Ping(ctx); err != nil {
		return nil, fmt.Errorf("clickhouse ping failed: %w", err)
	}
	return &Store{conn: conn, cfg: cfg, log: log}, nil
}

// Close closes the connection.
func (s *Store) Close() error { return s.conn.Close() }

// Conn exposes the connection for the query side (the API) to use.
func (s *Store) Conn() driver.Conn { return s.conn }

// Database is the active database name.
func (s *Store) Database() string { return s.cfg.Database }

// Stats exposes the counters.
func (s *Store) Stats() *Stats { return &s.stats }

// Migrate creates the database and tables idempotently.
func (s *Store) Migrate(ctx context.Context) error {
	if err := s.conn.Exec(ctx, "CREATE DATABASE IF NOT EXISTS "+s.cfg.Database); err != nil {
		return fmt.Errorf("could not create the database: %w", err)
	}
	ttl := strconv.Itoa(s.cfg.TTLDays)
	for i, stmt := range Schema {
		stmt = strings.ReplaceAll(stmt, "{{TTL_DAYS}}", ttl)
		// Create the tables in the target database.
		stmt = qualify(stmt, s.cfg.Database)
		if err := s.conn.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("schema step %d failed: %w", i, err)
		}
	}
	s.log.Info("clickhouse schema ready", "database", s.cfg.Database, "ttl_days", s.cfg.TTLDays)
	return nil
}

// qualify prefixes the table names inside the DDL with the database name.
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

// --- writing: spans ---

// Name satisfies the pipeline.Processor interface.
func (s *Store) Name() string { return "clickhouse-spans" }

// Process writes a batch of spans in one go.
func (s *Store) Process(ctx context.Context, spans []*model.Span) error {
	if len(spans) == 0 {
		return nil
	}
	start := time.Now()

	batch, err := s.conn.PrepareBatch(ctx, qualifyInsert(InsertSpansSQL, s.cfg.Database))
	if err != nil {
		s.stats.WriteErrors.Add(1)
		return fmt.Errorf("could not prepare the span batch: %w", err)
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
			return fmt.Errorf("span append failed: %w", err)
		}
	}

	if err := batch.Send(); err != nil {
		s.stats.WriteErrors.Add(1)
		return fmt.Errorf("could not send the span batch: %w", err)
	}
	s.stats.SpansWritten.Add(uint64(len(spans)))
	s.stats.WriteLatency.Store(time.Since(start).Microseconds())
	return nil
}

// --- writing: topology edges ---

// WriteEdges satisfies the topology.EdgeSink interface.
func (s *Store) WriteEdges(ctx context.Context, edges []topology.EdgeSample) error {
	if len(edges) == 0 {
		return nil
	}
	batch, err := s.conn.PrepareBatch(ctx, qualifyInsert(InsertEdgesSQL, s.cfg.Database))
	if err != nil {
		s.stats.WriteErrors.Add(1)
		return fmt.Errorf("could not prepare the edge batch: %w", err)
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
			return fmt.Errorf("edge append failed: %w", err)
		}
	}

	if err := batch.Send(); err != nil {
		s.stats.WriteErrors.Add(1)
		return fmt.Errorf("could not send the edge batch: %w", err)
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

// nilSafeMap turns a nil map into an empty one: the ClickHouse driver does not
// accept nil for a Map column.
func nilSafeMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}
