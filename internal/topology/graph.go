// Package topology derives the service and Kubernetes topology from the
// incoming requests (spans) themselves. There is no separate discovery
// mechanism, sidecar or network scan: the topology comes out of the traces.
//
// The method: the parent of a SERVER span is a CLIENT span produced in the
// calling service. Pair the two on span_id and you have both ends of the edge.
// CLIENT spans that never find a pair are external dependencies (a database, a
// queue, an uninstrumented service); those are derived from the peer
// attributes.
package topology

import (
	"context"
	"hash/maphash"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/erdemkayatr/nabiz/internal/model"
)

// ConnType is the type of an edge.
type ConnType uint8

const (
	ConnService   ConnType = iota // service -> service
	ConnDatabase                  // service -> database
	ConnMessaging                 // service -> queue/topic
	ConnExternal                  // service -> uninstrumented external endpoint
	ConnEntry                     // outside world -> service (entry point)
)

func (c ConnType) String() string {
	switch c {
	case ConnDatabase:
		return "database"
	case ConnMessaging:
		return "messaging"
	case ConnExternal:
		return "external"
	case ConnEntry:
		return "entry"
	default:
		return "service"
	}
}

// LatencyBounds are the upper bounds of the edge histogram, in milliseconds.
// The last bucket (+Inf) exists in the array but is not listed here.
var LatencyBounds = [...]float64{1, 2, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000}

// BucketCount is the number of histogram buckets, including +Inf.
const BucketCount = len(LatencyBounds) + 1

// EdgeKey identifies an edge. It is used as a map key, so it has to be
// comparable — hence the plain string fields.
type EdgeKey struct {
	Client          string
	Server          string
	ClientNamespace string
	ClientWorkload  string
	ServerNamespace string
	ServerWorkload  string
	ClientNode      string
	ServerNode      string
	ConnType        ConnType
}

// EdgeSample is an edge's aggregate within one time bucket.
type EdgeSample struct {
	Bucket time.Time // truncated to the minute
	Key    EdgeKey
	Calls  uint64
	Errors uint64

	// Durations come from the server-side span, or the client side if absent.
	DurationSumNS uint64
	DurationMaxNS uint64
	Buckets       [BucketCount]uint64
}

// EdgeSink is the component that writes aggregated edges (ClickHouse).
type EdgeSink interface {
	WriteEdges(ctx context.Context, edges []EdgeSample) error
}

// Config holds the topology builder's settings.
type Config struct {
	// Shards is the lock striping of the pairing table. Must be a power of two.
	Shards int
	// PairTTL is how long a CLIENT span waits for its pair. Anything still
	// unpaired at the end of it is treated as an external dependency.
	PairTTL time.Duration
	// FlushInterval is how often aggregated edges are written to storage.
	FlushInterval time.Duration
	// MaxPendingPerShard caps the pending spans per shard. Beyond it nothing
	// new is recorded: memory comes before topology accuracy.
	MaxPendingPerShard int
	// IncludeNodeDimension adds the Kubernetes node to the edge key. Turn it on
	// to see node-to-node traffic; it multiplies cardinality by the node count.
	IncludeNodeDimension bool
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		Shards:               64,
		PairTTL:              30 * time.Second,
		FlushInterval:        15 * time.Second,
		MaxPendingPerShard:   50_000,
		IncludeNodeDimension: false,
	}
}

// Stats holds the topology counters.
type Stats struct {
	Paired         atomic.Uint64
	InferredPeers  atomic.Uint64
	EntryPoints    atomic.Uint64
	Evicted        atomic.Uint64
	EdgesWritten   atomic.Uint64
	PendingDropped atomic.Uint64
}

// node identifies the service at one end of an edge.
type node struct {
	service   string
	namespace string
	workload  string
	k8sNode   string
}

// pending is a span waiting to be paired.
type pending struct {
	side       uint8 // 1 = client, 2 = server
	n          node
	durationNS uint64
	isError    bool
	connType   ConnType
	peer       string // the derived target name, for the client side
}

// shard is a two-generation table of pending spans.
//
// Generations rotate every TTL/2: cur -> prev, and prev is discarded. Records
// therefore live between TTL/2 and TTL, eviction costs O(1), and the memory
// ceiling takes care of itself. Unpaired CLIENT spans in the discarded
// generation are turned into "external dependency" edges.
type shard struct {
	mu   sync.Mutex
	cur  map[string]pending
	prev map[string]pending
}

// Builder derives topology from the span stream. It satisfies pipeline.Processor.
type Builder struct {
	cfg    Config
	sink   EdgeSink
	log    *slog.Logger
	stats  Stats
	seed   maphash.Seed
	shards []*shard
	mask   uint64

	aggMu sync.Mutex
	agg   map[aggKey]*EdgeSample

	stopCh chan struct{}
	wg     sync.WaitGroup
}

type aggKey struct {
	bucket int64
	key    EdgeKey
}

// New builds the topology builder.
func New(cfg Config, sink EdgeSink, log *slog.Logger) *Builder {
	if cfg.Shards <= 0 {
		cfg = DefaultConfig()
	}
	// Round Shards up to a power of two.
	n := 1
	for n < cfg.Shards {
		n <<= 1
	}
	cfg.Shards = n

	b := &Builder{
		cfg:    cfg,
		sink:   sink,
		log:    log,
		seed:   maphash.MakeSeed(),
		shards: make([]*shard, n),
		mask:   uint64(n - 1),
		agg:    make(map[aggKey]*EdgeSample, 1024),
		stopCh: make(chan struct{}),
	}
	for i := range b.shards {
		b.shards[i] = &shard{
			cur:  make(map[string]pending, 1024),
			prev: make(map[string]pending, 1024),
		}
	}
	return b
}

// Name satisfies the Processor interface.
func (b *Builder) Name() string { return "topology" }

// Stats exposes the counters.
func (b *Builder) Stats() *Stats { return &b.stats }

// Start launches the rotation and flush loops.
func (b *Builder) Start(ctx context.Context) {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		rotate := time.NewTicker(b.cfg.PairTTL / 2)
		flush := time.NewTicker(b.cfg.FlushInterval)
		defer rotate.Stop()
		defer flush.Stop()
		for {
			select {
			case <-rotate.C:
				b.rotate()
			case <-flush.C:
				b.flush(ctx)
			case <-b.stopCh:
				b.rotate()
				b.flush(context.WithoutCancel(ctx))
				return
			case <-ctx.Done():
				b.rotate()
				b.flush(context.WithoutCancel(ctx))
				return
			}
		}
	}()
}

// Stop halts the loops and waits for the final flush.
func (b *Builder) Stop() {
	close(b.stopCh)
	b.wg.Wait()
}

// Process folds a batch of spans into the topology.
func (b *Builder) Process(_ context.Context, spans []*model.Span) error {
	for _, s := range spans {
		b.observe(s)
	}
	return nil
}

func (b *Builder) observe(s *model.Span) {
	switch s.Kind {
	case model.KindClient, model.KindProducer:
		b.observeClient(s)
	case model.KindServer, model.KindConsumer:
		b.observeServer(s)
	default:
		// INTERNAL spans contribute nothing to the topology; RED metrics are
		// computed separately from the span table.
	}
}

func (b *Builder) observeClient(s *model.Span) {
	key := s.SpanID
	if key == "" {
		return
	}
	p := pending{
		side:       1,
		n:          nodeOf(s),
		durationNS: s.DurationNS,
		isError:    s.IsError(),
		connType:   connTypeOf(s),
		peer:       peerNameOf(s),
	}

	sh := b.shardFor(key)
	sh.mu.Lock()
	if other, ok := sh.take(key); ok && other.side == 2 {
		sh.mu.Unlock()
		b.emitPair(p, other)
		return
	}
	if len(sh.cur) >= b.cfg.MaxPendingPerShard {
		sh.mu.Unlock()
		b.stats.PendingDropped.Add(1)
		// No room to pair it: rather than lose the edge, write it with the
		// derived target.
		b.emitInferred(p)
		return
	}
	sh.cur[key] = p
	sh.mu.Unlock()
}

func (b *Builder) observeServer(s *model.Span) {
	if s.ParentSpanID == "" {
		// A SERVER span with no parent is an entry point into the system.
		b.stats.EntryPoints.Add(1)
		b.record(EdgeKey{
			Client:          "user",
			Server:          s.ServiceKey(),
			ServerNamespace: s.K8sNamespace,
			ServerWorkload:  s.K8sWorkload,
			ServerNode:      b.nodeDim(s.K8sNode),
			ConnType:        ConnEntry,
		}, s.DurationNS, s.IsError())
		return
	}

	p := pending{
		side:       2,
		n:          nodeOf(s),
		durationNS: s.DurationNS,
		isError:    s.IsError(),
		connType:   ConnService,
	}

	sh := b.shardFor(s.ParentSpanID)
	sh.mu.Lock()
	if other, ok := sh.take(s.ParentSpanID); ok && other.side == 1 {
		sh.mu.Unlock()
		b.emitPair(other, p)
		return
	}
	if len(sh.cur) >= b.cfg.MaxPendingPerShard {
		sh.mu.Unlock()
		b.stats.PendingDropped.Add(1)
		return
	}
	sh.cur[s.ParentSpanID] = p
	sh.mu.Unlock()
}

// emitPair produces an edge from a matched client/server pair. The latency is
// taken from the server side: what the client sees includes network and queue
// wait, whereas what a service graph is actually asking about is the time the
// server spent.
func (b *Builder) emitPair(client, server pending) {
	b.stats.Paired.Add(1)
	dur := server.durationNS
	if dur == 0 {
		dur = client.durationNS
	}
	b.record(EdgeKey{
		Client:          client.n.service,
		Server:          server.n.service,
		ClientNamespace: client.n.namespace,
		ClientWorkload:  client.n.workload,
		ServerNamespace: server.n.namespace,
		ServerWorkload:  server.n.workload,
		ClientNode:      b.nodeDim(client.n.k8sNode),
		ServerNode:      b.nodeDim(server.n.k8sNode),
		ConnType:        ConnService,
	}, dur, client.isError || server.isError)
}

// emitInferred turns an unpaired CLIENT span into an external-dependency edge.
func (b *Builder) emitInferred(p pending) {
	if p.peer == "" {
		return
	}
	b.stats.InferredPeers.Add(1)
	b.record(EdgeKey{
		Client:          p.n.service,
		Server:          p.peer,
		ClientNamespace: p.n.namespace,
		ClientWorkload:  p.n.workload,
		ClientNode:      b.nodeDim(p.n.k8sNode),
		ConnType:        p.connType,
	}, p.durationNS, p.isError)
}

func (b *Builder) record(key EdgeKey, durationNS uint64, isError bool) {
	bucket := time.Now().UTC().Truncate(time.Minute).Unix()
	ak := aggKey{bucket: bucket, key: key}

	b.aggMu.Lock()
	e, ok := b.agg[ak]
	if !ok {
		e = &EdgeSample{Bucket: time.Unix(bucket, 0).UTC(), Key: key}
		b.agg[ak] = e
	}
	e.Calls++
	if isError {
		e.Errors++
	}
	e.DurationSumNS += durationNS
	if durationNS > e.DurationMaxNS {
		e.DurationMaxNS = durationNS
	}
	e.Buckets[bucketIndex(durationNS)]++
	b.aggMu.Unlock()
}

// rotate advances the generations and turns the unpaired client spans in the
// discarded generation into external-dependency edges.
func (b *Builder) rotate() {
	for _, sh := range b.shards {
		sh.mu.Lock()
		dropped := sh.prev
		sh.prev = sh.cur
		sh.cur = make(map[string]pending, len(sh.cur))
		sh.mu.Unlock()

		for _, p := range dropped {
			b.stats.Evicted.Add(1)
			if p.side == 1 {
				b.emitInferred(p)
			}
		}
	}
}

// flush writes the aggregated edges to storage.
func (b *Builder) flush(ctx context.Context) {
	b.aggMu.Lock()
	if len(b.agg) == 0 {
		b.aggMu.Unlock()
		return
	}
	out := make([]EdgeSample, 0, len(b.agg))
	for _, e := range b.agg {
		out = append(out, *e)
	}
	b.agg = make(map[aggKey]*EdgeSample, len(b.agg))
	b.aggMu.Unlock()

	if err := b.sink.WriteEdges(ctx, out); err != nil {
		b.log.Error("could not write topology edges", "edges", len(out), "err", err)
		return
	}
	b.stats.EdgesWritten.Add(uint64(len(out)))
}

func (b *Builder) shardFor(key string) *shard {
	h := maphash.String(b.seed, key)
	return b.shards[h&b.mask]
}

func (b *Builder) nodeDim(n string) string {
	if !b.cfg.IncludeNodeDimension {
		return ""
	}
	return n
}

// take finds the key in either generation and removes it.
func (s *shard) take(key string) (pending, bool) {
	if p, ok := s.cur[key]; ok {
		delete(s.cur, key)
		return p, true
	}
	if p, ok := s.prev[key]; ok {
		delete(s.prev, key)
		return p, true
	}
	return pending{}, false
}

func nodeOf(s *model.Span) node {
	return node{
		service:   s.ServiceKey(),
		namespace: s.K8sNamespace,
		workload:  s.K8sWorkload,
		k8sNode:   s.K8sNode,
	}
}

func connTypeOf(s *model.Span) ConnType {
	switch {
	case s.DBSystem != "":
		return ConnDatabase
	case s.MessagingSystem != "":
		return ConnMessaging
	default:
		return ConnExternal
	}
}

// peerNameOf names the target of an unpaired client span.
// Order matters: the most descriptive name wins.
func peerNameOf(s *model.Span) string {
	if s.DBSystem != "" {
		name := s.DBName
		if name == "" {
			name = s.PeerAddress
		}
		if name == "" {
			return s.DBSystem
		}
		return s.DBSystem + ":" + name
	}
	if s.MessagingSystem != "" {
		if s.MessagingDest != "" {
			return s.MessagingSystem + ":" + s.MessagingDest
		}
		return s.MessagingSystem
	}
	if s.PeerService != "" {
		return s.PeerService
	}
	if s.RPCService != "" {
		return s.RPCService
	}
	if s.PeerAddress != "" {
		if s.PeerPort != 0 {
			return s.PeerAddress + ":" + strconv.Itoa(int(s.PeerPort))
		}
		return s.PeerAddress
	}
	return ""
}

func bucketIndex(durationNS uint64) int {
	ms := float64(durationNS) / 1e6
	for i, b := range LatencyBounds {
		if ms <= b {
			return i
		}
	}
	return BucketCount - 1
}
