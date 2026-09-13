// Package pipeline manages the buffer between the receiver and storage/topology.
//
// Flow: Accept() -> bounded queue -> N workers -> batch -> Processors.
// The queue is bounded and drops rather than blocking when full, so a slow
// ClickHouse cannot cascade backwards into a slow application.
package pipeline

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/erdemkayatr/nabiz/internal/model"
)

// Processor is a component that does work on a batch of spans.
type Processor interface {
	Name() string
	Process(ctx context.Context, spans []*model.Span) error
}

// Config holds the pipeline settings.
type Config struct {
	// QueueSize caps the spans waiting in the queue.
	QueueSize int
	// Workers is the number of goroutines processing batches in parallel.
	Workers int
	// BatchSize is how many spans go into one Processor call.
	BatchSize int
	// FlushInterval is how long to wait before flushing a partial batch.
	FlushInterval time.Duration
}

// DefaultConfig returns sensible defaults aimed at ~100k spans/sec on one node.
func DefaultConfig() Config {
	return Config{
		QueueSize:     200_000,
		Workers:       4,
		BatchSize:     5_000,
		FlushInterval: 2 * time.Second,
	}
}

// Stats holds the pipeline counters.
type Stats struct {
	Enqueued   atomic.Uint64
	Dropped    atomic.Uint64
	Processed  atomic.Uint64
	Failed     atomic.Uint64
	QueueDepth atomic.Int64
}

// Pipeline manages the span stream.
type Pipeline struct {
	cfg        Config
	processors []Processor
	log        *slog.Logger

	queue chan []*model.Span
	stats Stats
	wg    sync.WaitGroup
}

// New builds the pipeline.
func New(cfg Config, log *slog.Logger, processors ...Processor) *Pipeline {
	if cfg.QueueSize <= 0 {
		cfg = DefaultConfig()
	}
	// The queue carries chunks; assuming an average chunk size of BatchSize/4,
	// the channel capacity is picked accordingly.
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

// Accept puts spans from the receiver into the queue. It never blocks.
// It returns the number of spans that could not be accepted.
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

// Stats exposes the counters.
func (p *Pipeline) Stats() *Stats { return &p.stats }

// Start runs the workers.
func (p *Pipeline) Start(ctx context.Context) {
	for i := 0; i < p.cfg.Workers; i++ {
		p.wg.Add(1)
		go func(id int) {
			defer p.wg.Done()
			p.worker(ctx, id)
		}(i)
	}
}

// Stop closes the queue and waits for the workers to flush their batches.
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

// dispatch hands the batch to every Processor in turn. If one processor
// fails, the others still run: topology must not be lost because of a
// storage error.
func (p *Pipeline) dispatch(ctx context.Context, spans []*model.Span) {
	for _, proc := range p.processors {
		if err := proc.Process(ctx, spans); err != nil {
			p.stats.Failed.Add(uint64(len(spans)))
			p.log.Error("processor error", "processor", proc.Name(), "spans", len(spans), "err", err)
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
