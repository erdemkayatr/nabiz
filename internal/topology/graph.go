// Package topology, servis/k8s topolojisini gelen isteklerden (span'lerden)
// çıkarır. Ayrı bir keşif mekanizması, sidecar ya da ağ taraması yoktur:
// topoloji, trace'lerin kendisinden türer.
//
// Yöntem: bir SERVER span'inin ebeveyni, çağıran serviste üretilmiş bir CLIENT
// span'idir. İkisini span_id üzerinden eşleştirince kenarın iki ucu da elde
// edilir. Eşleşmeyen CLIENT span'leri ise dış bağımlılıktır (veritabanı, kuyruk,
// enstrümante olmayan servis); onları peer bilgisinden türetiriz.
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

// ConnType, kenarın türü.
type ConnType uint8

const (
	ConnService   ConnType = iota // servis -> servis
	ConnDatabase                  // servis -> veritabanı
	ConnMessaging                 // servis -> kuyruk/topic
	ConnExternal                  // servis -> enstrümante olmayan dış uç
	ConnEntry                     // dış dünya -> servis (giriş noktası)
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

// LatencyBounds, kenar histogramının üst sınırları (milisaniye).
// Son kova (+Inf) dizide yer alır ama burada listelenmez.
var LatencyBounds = [...]float64{1, 2, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000}

// BucketCount, histogramdaki kova sayısı (+Inf dahil).
const BucketCount = len(LatencyBounds) + 1

// EdgeKey, bir kenarın kimliğidir. Map anahtarı olarak kullanıldığı için
// karşılaştırılabilir olmak zorunda; bu yüzden düz string alanlar.
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

// EdgeSample, bir kenarın bir zaman kovasındaki toplamıdır.
type EdgeSample struct {
	Bucket time.Time // dakikaya yuvarlanmış
	Key    EdgeKey
	Calls  uint64
	Errors uint64

	// Süreler sunucu tarafı span'den alınır; yoksa istemci tarafından.
	DurationSumNS uint64
	DurationMaxNS uint64
	Buckets       [BucketCount]uint64
}

// EdgeSink, toplanmış kenarları yazan bileşen (ClickHouse).
type EdgeSink interface {
	WriteEdges(ctx context.Context, edges []EdgeSample) error
}

// Config, topoloji oluşturucu ayarları.
type Config struct {
	// Shards, eşleştirme tablosundaki kilit parçalanması. 2'nin kuvveti olmalı.
	Shards int
	// PairTTL, bir CLIENT span'inin eşini bekleyeceği süre. Bu sürenin
	// sonunda eşleşmeyenler dış bağımlılık kabul edilir.
	PairTTL time.Duration
	// FlushInterval, toplanan kenarların depoya yazılma aralığı.
	FlushInterval time.Duration
	// MaxPendingPerShard, shard başına bekleyen span üst sınırı. Aşılırsa yeni
	// kayıt alınmaz: bellek, topoloji doğruluğundan önce gelir.
	MaxPendingPerShard int
	// IncludeNodeDimension, kenar anahtarına k8s node'unu da ekler. Node'lar
	// arası trafiği görmek için açılır; kardinaliteyi node sayısı kadar çarpar.
	IncludeNodeDimension bool
}

// DefaultConfig, makul varsayılanlar.
func DefaultConfig() Config {
	return Config{
		Shards:               64,
		PairTTL:              30 * time.Second,
		FlushInterval:        15 * time.Second,
		MaxPendingPerShard:   50_000,
		IncludeNodeDimension: false,
	}
}

// Stats, topoloji sayaçları.
type Stats struct {
	Paired         atomic.Uint64
	InferredPeers  atomic.Uint64
	EntryPoints    atomic.Uint64
	Evicted        atomic.Uint64
	EdgesWritten   atomic.Uint64
	PendingDropped atomic.Uint64
}

// node, kenarın bir ucundaki servisin kimliği.
type node struct {
	service   string
	namespace string
	workload  string
	k8sNode   string
}

// pending, eşleşme bekleyen bir span.
type pending struct {
	side       uint8 // 1 = client, 2 = server
	n          node
	durationNS uint64
	isError    bool
	connType   ConnType
	peer       string // istemci tarafı için türetilmiş hedef adı
}

// shard, iki nesilli (generational) bir bekleyen-span tablosudur.
//
// Nesil rotasyonu TTL/2'de bir yapılır: cur -> prev, prev atılır. Böylece
// kayıtlar TTL/2 ile TTL arasında yaşar, silme maliyeti O(1) olur ve bellek
// üst sınırı serbest kalır. Atılan nesildeki eşleşmemiş CLIENT span'leri
// "dış bağımlılık" kenarına dönüştürülür.
type shard struct {
	mu   sync.Mutex
	cur  map[string]pending
	prev map[string]pending
}

// Builder, span akışından topoloji üretir. pipeline.Processor'ı karşılar.
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

// New, topoloji oluşturucuyu kurar.
func New(cfg Config, sink EdgeSink, log *slog.Logger) *Builder {
	if cfg.Shards <= 0 {
		cfg = DefaultConfig()
	}
	// Shards'ı 2'nin kuvvetine yuvarla.
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

// Name, Processor arayüzü için.
func (b *Builder) Name() string { return "topology" }

// Stats, sayaçlara erişim verir.
func (b *Builder) Stats() *Stats { return &b.stats }

// Start, rotasyon ve flush döngülerini başlatır.
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

// Stop, döngüleri durdurur ve son flush'ı bekler.
func (b *Builder) Stop() {
	close(b.stopCh)
	b.wg.Wait()
}

// Process, bir batch span'i topolojiye işler.
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
		// INTERNAL span'ler topolojiye katkı vermez; RED metrikleri span
		// tablosundan ayrıca hesaplanır.
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
		// Eşleştirmeye yer yok: kenarı yine de kaybetmeyelim, türetilmiş
		// hedefle yaz.
		b.emitInferred(p)
		return
	}
	sh.cur[key] = p
	sh.mu.Unlock()
}

func (b *Builder) observeServer(s *model.Span) {
	if s.ParentSpanID == "" {
		// Ebeveynsiz SERVER span'i = sistemin giriş noktası.
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

// emitPair, eşleşmiş istemci/sunucu çiftinden kenar üretir. Gecikme sunucu
// tarafından alınır: istemcinin gördüğü süre ağ ve kuyruk beklemesini de
// içerir, servis grafiğinde asıl aranan sunucunun harcadığı süredir.
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

// emitInferred, eşleşmemiş bir CLIENT span'ini dış bağımlılık kenarına çevirir.
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

// rotate, nesil değişimi yapar ve atılan nesildeki eşleşmemiş istemci
// span'lerini dış bağımlılık kenarına çevirir.
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

// flush, toplanmış kenarları depoya yazar.
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
		b.log.Error("topoloji kenarları yazılamadı", "edges", len(out), "err", err)
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

// take, anahtarı iki nesilden birinde bulup siler.
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

// peerNameOf, eşleşmemiş bir istemci span'inin hedefine isim verir.
// Sıra önemli: en açıklayıcı isim kazanır.
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
