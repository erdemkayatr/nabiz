// Package pipeline, alıcı ile depolama/topoloji arasındaki tamponu yönetir.
//
// Akış: Accept() -> sınırlı kuyruk -> N worker -> batch -> Processor'lar.
// Kuyruk sınırlıdır ve dolduğunda bloke etmek yerine düşürür; böylece yavaş
// bir ClickHouse, sırtından zincirleme olarak uygulamayı yavaşlatamaz.
package pipeline

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/erdemkayatr/nabiz/internal/model"
)

// Processor, bir batch span üzerinde iş yapan bileşendir.
type Processor interface {
	Name() string
	Process(ctx context.Context, spans []*model.Span) error
}

// Config, pipeline ayarları.
type Config struct {
	// QueueSize, kuyruktaki bekleyen span üst sınırı.
	QueueSize int
	// Workers, paralel batch işleyen goroutine sayısı.
	Workers int
	// BatchSize, bir Processor çağrısına giden span sayısı.
	BatchSize int
	// FlushInterval, batch dolmasa bile boşaltma aralığı.
	FlushInterval time.Duration
}

// DefaultConfig, tek node'da ~100k span/sn'yi hedefleyen makul varsayılanlar.
func DefaultConfig() Config {
	return Config{
		QueueSize:     200_000,
		Workers:       4,
		BatchSize:     5_000,
		FlushInterval: 2 * time.Second,
	}
}

// Stats, pipeline sayaçları.
type Stats struct {
	Enqueued   atomic.Uint64
	Dropped    atomic.Uint64
	Processed  atomic.Uint64
	Failed     atomic.Uint64
	QueueDepth atomic.Int64
}

// Pipeline, span akışını yönetir.
type Pipeline struct {
	cfg        Config
	processors []Processor
	log        *slog.Logger

	queue chan []*model.Span
	stats Stats
	wg    sync.WaitGroup
}

// New, pipeline'ı kurar.
func New(cfg Config, log *slog.Logger, processors ...Processor) *Pipeline {
	if cfg.QueueSize <= 0 {
		cfg = DefaultConfig()
	}
	// Kuyruk chunk taşır; chunk başına ortalama boyutu BatchSize/4 varsayıp
	// kanal kapasitesini buna göre seçiyoruz.
	chunkCap := cfg.QueueSize / max(cfg.BatchSize/4, 1)
	if chunkCap < 64 {
		chunkCap = 64
	}
	return &Pipeline{
		cfg:        cfg,
		processors: processors,
		log:        log,
		queue:      make(chan []*model.Span, chunkCap),
	}
}

// Accept, alıcıdan gelen span'leri kuyruğa koyar. Asla bloke etmez.
// Kabul edilemeyen span sayısını döndürür.
func (p *Pipeline) Accept(spans []*model.Span) int {
	if len(spans) == 0 {
		return 0
	}
	select {
	case p.queue <- spans:
		p.stats.Enqueued.Add(uint64(len(spans)))
		p.stats.QueueDepth.Add(int64(len(spans)))
		return 0
	default:
		p.stats.Dropped.Add(uint64(len(spans)))
		return len(spans)
	}
}

// Stats, sayaçlara erişim verir.
func (p *Pipeline) Stats() *Stats { return &p.stats }

// Start, worker'ları çalıştırır.
func (p *Pipeline) Start(ctx context.Context) {
	for i := 0; i < p.cfg.Workers; i++ {
		p.wg.Add(1)
		go func(id int) {
			defer p.wg.Done()
			p.worker(ctx, id)
		}(i)
	}
}

// Stop, kuyruğu kapatır ve worker'ların batch'leri boşaltmasını bekler.
func (p *Pipeline) Stop() {
	close(p.queue)
	p.wg.Wait()
}

func (p *Pipeline) worker(ctx context.Context, id int) {
	batch := make([]*model.Span, 0, p.cfg.BatchSize)
	ticker := time.NewTicker(p.cfg.FlushInterval)
	defer ticker.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		p.dispatch(ctx, batch)
		batch = batch[:0]
	}

	for {
		select {
		case chunk, ok := <-p.queue:
			if !ok {
				flush()
				return
			}
			p.stats.QueueDepth.Add(-int64(len(chunk)))
			batch = append(batch, chunk...)
			if len(batch) >= p.cfg.BatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-ctx.Done():
			flush()
			return
		}
	}
}

// dispatch, batch'i tüm Processor'lara sırayla verir. Bir processor hata
// verirse diğerleri yine de çalışır: topoloji, depolama hatası yüzünden
// kaybolmamalı.
func (p *Pipeline) dispatch(ctx context.Context, spans []*model.Span) {
	for _, proc := range p.processors {
		if err := proc.Process(ctx, spans); err != nil {
			p.stats.Failed.Add(uint64(len(spans)))
			p.log.Error("processor hatası", "processor", proc.Name(), "spans", len(spans), "err", err)
			continue
		}
	}
	p.stats.Processed.Add(uint64(len(spans)))
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
