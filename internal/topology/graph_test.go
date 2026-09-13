package topology

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/erdemkayatr/nabiz/internal/model"
)

type memSink struct {
	mu    sync.Mutex
	edges []EdgeSample
}

func (m *memSink) WriteEdges(_ context.Context, edges []EdgeSample) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.edges = append(m.edges, edges...)
	return nil
}

func (m *memSink) find(client, server string) (EdgeSample, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.edges {
		if e.Key.Client == client && e.Key.Server == server {
			return e, true
		}
	}
	return EdgeSample{}, false
}

func newTestBuilder(sink EdgeSink) *Builder {
	cfg := DefaultConfig()
	cfg.Shards = 4
	return New(cfg, sink, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func serverSpan(service, spanID, parentID string, durMS uint64) *model.Span {
	return &model.Span{
		SpanID:       spanID,
		ParentSpanID: parentID,
		ServiceName:  service,
		Kind:         model.KindServer,
		DurationNS:   durMS * 1e6,
	}
}

func clientSpan(service, spanID string, durMS uint64) *model.Span {
	return &model.Span{
		SpanID:      spanID,
		ServiceName: service,
		Kind:        model.KindClient,
		DurationNS:  durMS * 1e6,
	}
}

// A frontend -> backend call turning into an edge is topology's base case.
func TestPairsClientAndServerIntoEdge(t *testing.T) {
	sink := &memSink{}
	b := newTestBuilder(sink)

	// the frontend's entry span (no parent) -> entry point edge
	b.observe(serverSpan("frontend", "aaaa", "", 50))
	// frontend'in HttpClient span'i
	b.observe(clientSpan("frontend", "bbbb", 40))
	// the backend span that serves this call
	b.observe(serverSpan("backend", "cccc", "bbbb", 30))

	b.flush(context.Background())

	entry, ok := sink.find("user", "frontend")
	if !ok {
		t.Fatal("no entry point edge was produced")
	}
	if entry.Key.ConnType != ConnEntry {
		t.Errorf("the entry edge type is %s, expected entry", entry.Key.ConnType)
	}

	edge, ok := sink.find("frontend", "backend")
	if !ok {
		t.Fatal("no frontend -> backend edge was produced")
	}
	if edge.Calls != 1 {
		t.Errorf("calls = %d, 1 bekleniyordu", edge.Calls)
	}
	if edge.Key.ConnType != ConnService {
		t.Errorf("the edge type is %s, expected service", edge.Key.ConnType)
	}
	// The duration must come from the server side: 30ms, not the 40ms the client saw.
	if edge.DurationSumNS != 30*1e6 {
		t.Errorf("the duration is %dns, expected the server side (30ms)", edge.DurationSumNS)
	}
	if b.stats.Paired.Load() != 1 {
		t.Errorf("paired = %d, 1 bekleniyordu", b.stats.Paired.Load())
	}
}

// Spans can arrive out of order: the pairing must still hold when the server
// span reaches us before the client one.
func TestPairsWhenServerArrivesFirst(t *testing.T) {
	sink := &memSink{}
	b := newTestBuilder(sink)

	b.observe(serverSpan("backend", "cccc", "bbbb", 30))
	b.observe(clientSpan("frontend", "bbbb", 40))
	b.flush(context.Background())

	if _, ok := sink.find("frontend", "backend"); !ok {
		t.Fatal("spans arriving out of order did not pair")
	}
}

// A database call has no span on the other side; the target has to be derived
// from the span attributes.
func TestInfersDatabaseDependencyOnRotation(t *testing.T) {
	sink := &memSink{}
	b := newTestBuilder(sink)

	db := clientSpan("backend", "dddd", 12)
	db.DBSystem = "postgresql"
	db.DBName = "orders"
	b.observe(db)

	// Its pair never arrives; after two rotations it is written as an external dependency.
	b.rotate()
	b.rotate()
	b.flush(context.Background())

	edge, ok := sink.find("backend", "postgresql:orders")
	if !ok {
		t.Fatal("no database dependency edge was produced")
	}
	if edge.Key.ConnType != ConnDatabase {
		t.Errorf("the edge type is %s, expected database", edge.Key.ConnType)
	}
	if b.stats.InferredPeers.Load() != 1 {
		t.Errorf("inferred_peers = %d, 1 bekleniyordu", b.stats.InferredPeers.Load())
	}
}

// A call to an uninstrumented external service must turn into a node using the
// address attributes.
func TestInfersExternalPeerFromAddress(t *testing.T) {
	sink := &memSink{}
	b := newTestBuilder(sink)

	ext := clientSpan("backend", "eeee", 80)
	ext.PeerAddress = "api.stripe.com"
	ext.PeerPort = 443
	b.observe(ext)
	b.rotate()
	b.rotate()
	b.flush(context.Background())

	edge, ok := sink.find("backend", "api.stripe.com:443")
	if !ok {
		t.Fatal("no external dependency edge was produced")
	}
	if edge.Key.ConnType != ConnExternal {
		t.Errorf("the edge type is %s, expected external", edge.Key.ConnType)
	}
}

// Kubernetes dimensions must be carried into the edge key, so the topology can
// be drawn at both service and workload level.
func TestCarriesKubernetesDimensions(t *testing.T) {
	sink := &memSink{}
	b := newTestBuilder(sink)

	c := clientSpan("frontend", "bbbb", 40)
	c.K8sNamespace, c.K8sWorkload = "shop", "frontend-deploy"
	s := serverSpan("backend", "cccc", "bbbb", 30)
	s.K8sNamespace, s.K8sWorkload = "shop", "backend-deploy"

	b.observe(c)
	b.observe(s)
	b.flush(context.Background())

	edge, ok := sink.find("frontend", "backend")
	if !ok {
		t.Fatal("no edge was produced")
	}
	if edge.Key.ClientWorkload != "frontend-deploy" || edge.Key.ServerWorkload != "backend-deploy" {
		t.Errorf("the workload dimensions were not carried: %+v", edge.Key)
	}
	if edge.Key.ClientNamespace != "shop" || edge.Key.ServerNamespace != "shop" {
		t.Errorf("the namespace dimensions were not carried: %+v", edge.Key)
	}
}

// Repeats of the same edge must aggregate into one record; otherwise write
// volume grows with request volume.
func TestAggregatesRepeatedCalls(t *testing.T) {
	sink := &memSink{}
	b := newTestBuilder(sink)

	for i := 0; i < 100; i++ {
		id := string(rune('a'+i%26)) + string(rune('a'+i/26)) + "xx"
		c := clientSpan("frontend", id, 10)
		s := serverSpan("backend", "s"+id, id, 10)
		if i%10 == 0 {
			s.StatusCode = model.StatusError
		}
		b.observe(c)
		b.observe(s)
	}
	b.flush(context.Background())

	edge, ok := sink.find("frontend", "backend")
	if !ok {
		t.Fatal("no edge was produced")
	}
	if edge.Calls != 100 {
		t.Errorf("calls = %d, 100 bekleniyordu", edge.Calls)
	}
	if edge.Errors != 10 {
		t.Errorf("errors = %d, 10 bekleniyordu", edge.Errors)
	}
	if sinkLen(sink) != 1 {
		t.Errorf("%d edges were written, expected 1 (aggregation is not working)", sinkLen(sink))
	}
}

// An HTTP 5xx counts as an error even when the SDK does not turn the status into ERROR.
func TestTreatsServerErrorStatusAsError(t *testing.T) {
	sink := &memSink{}
	b := newTestBuilder(sink)

	c := clientSpan("frontend", "bbbb", 40)
	s := serverSpan("backend", "cccc", "bbbb", 30)
	s.HTTPStatusCode = 503

	b.observe(c)
	b.observe(s)
	b.flush(context.Background())

	edge, _ := sink.find("frontend", "backend")
	if edge.Errors != 1 {
		t.Errorf("errors = %d, expected 1 (5xx must count as an error)", edge.Errors)
	}
}

// The latency histogram must land in the right bucket; the quantile estimate in
// the API depends on it.
func TestLatencyBucketing(t *testing.T) {
	cases := []struct {
		durationMS uint64
		wantIndex  int
	}{
		{0, 0},   // <= 1ms
		{1, 0},   // <= 1ms
		{3, 2},   // <= 5ms
		{100, 6}, // <= 100ms
		{99999, BucketCount - 1},
	}
	for _, tc := range cases {
		got := bucketIndex(tc.durationMS * 1e6)
		if got != tc.wantIndex {
			t.Errorf("%dms -> kova %d, %d bekleniyordu", tc.durationMS, got, tc.wantIndex)
		}
	}
}

// No data race or loss under concurrent writes.
func TestConcurrentObserveIsRaceFree(t *testing.T) {
	sink := &memSink{}
	b := newTestBuilder(sink)

	var wg sync.WaitGroup
	const goroutines, perGoroutine = 8, 250
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				id := spanID(g, i)
				b.observe(clientSpan("frontend", id, 5))
				b.observe(serverSpan("backend", "s"+id, id, 5))
			}
		}(g)
	}
	wg.Wait()
	b.flush(context.Background())

	edge, ok := sink.find("frontend", "backend")
	if !ok {
		t.Fatal("no edge was produced")
	}
	if want := uint64(goroutines * perGoroutine); edge.Calls != want {
		t.Errorf("calls = %d, %d bekleniyordu", edge.Calls, want)
	}
}

// When the memory limit is hit, no pending record is taken but the edge must not be lost.
func TestRespectsPendingLimit(t *testing.T) {
	sink := &memSink{}
	cfg := DefaultConfig()
	cfg.Shards = 1
	cfg.MaxPendingPerShard = 10
	b := New(cfg, sink, slog.New(slog.NewTextHandler(io.Discard, nil)))

	for i := 0; i < 50; i++ {
		c := clientSpan("frontend", spanID(0, i), 5)
		c.PeerAddress = "downstream"
		b.observe(c)
	}
	b.flush(context.Background())

	if b.stats.PendingDropped.Load() == 0 {
		t.Error("the pending record limit never kicked in")
	}
	if _, ok := sink.find("frontend", "downstream"); !ok {
		t.Error("the edge was lost entirely once the limit was exceeded")
	}
}

func spanID(g, i int) string {
	const hexdigits = "0123456789abcdef"
	buf := make([]byte, 16)
	n := g*100000 + i
	for j := 15; j >= 0; j-- {
		buf[j] = hexdigits[n&0xf]
		n >>= 4
	}
	return string(buf)
}

func sinkLen(m *memSink) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.edges)
}

func BenchmarkObserve(b *testing.B) {
	sink := &memSink{}
	builder := newTestBuilder(sink)
	spans := make([]*model.Span, 0, 2048)
	for i := 0; i < 1024; i++ {
		id := spanID(0, i)
		spans = append(spans, clientSpan("frontend", id, 5))
		spans = append(spans, serverSpan("backend", "s"+id, id, 5))
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		builder.observe(spans[i%len(spans)])
	}
	_ = time.Now()
}
