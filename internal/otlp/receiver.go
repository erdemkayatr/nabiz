// Package otlp, OTLP alıcılarını (gRPC ve HTTP) barındırır.
//
// Tasarım kuralı: alıcı asla göndereni bloke etmez. Kuyruk dolduğunda span
// düşürülür ve sayaç artar; uygulamanın içindeki agent bizim yüzümüzden
// yavaşlamaz. "Performansa etkisi olmasın" gereksiniminin sunucu tarafındaki
// karşılığı budur.
package otlp

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/erdemkayatr/nabiz/internal/model"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	// gRPC tarafında gzip'li export'ları çözebilmek için codec'i kaydeder.
	_ "google.golang.org/grpc/encoding/gzip"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Consumer, alıcıdan çıkan span'leri tüketen bileşendir (pipeline).
// Accept bloke etmemeli; kabul edilmeyen span sayısını döndürür.
type Consumer interface {
	Accept(spans []*model.Span) (dropped int)
}

// Stats, alıcının sayaçlarıdır; /metrics ve loglarda kullanılır.
type Stats struct {
	SpansReceived atomic.Uint64
	SpansDropped  atomic.Uint64
	Requests      atomic.Uint64
	Errors        atomic.Uint64
}

// Receiver, gRPC ve HTTP OTLP uçlarını birlikte yönetir.
type Receiver struct {
	consumer Consumer
	log      *slog.Logger
	stats    Stats

	grpcServer *grpc.Server
	httpServer *http.Server
}

// Config, alıcı ayarlarıdır.
type Config struct {
	GRPCAddr       string
	HTTPAddr       string
	MaxRecvMsgSize int
}

// New, alıcıyı kurar ama dinlemeye başlamaz.
func New(cfg Config, consumer Consumer, log *slog.Logger) *Receiver {
	if cfg.MaxRecvMsgSize <= 0 {
		cfg.MaxRecvMsgSize = 16 << 20 // 16 MiB
	}
	r := &Receiver{consumer: consumer, log: log}

	r.grpcServer = grpc.NewServer(
		grpc.MaxRecvMsgSize(cfg.MaxRecvMsgSize),
		grpc.NumStreamWorkers(uint32(defaultStreamWorkers())),
		grpc.KeepaliveEnforcementPolicy(keepalivePolicy()),
	)
	coltracepb.RegisterTraceServiceServer(r.grpcServer, &traceService{r: r})

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/traces", r.handleHTTPTraces)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	r.httpServer = &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return r
}

// Stats, sayaçlara erişim verir.
func (r *Receiver) Stats() *Stats { return &r.stats }

// Serve, iki dinleyiciyi de açar ve ctx iptal edilene kadar çalışır.
func (r *Receiver) Serve(ctx context.Context, cfg Config) error {
	lis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return err
	}

	errCh := make(chan error, 2)
	go func() {
		r.log.Info("OTLP/gRPC dinleniyor", "addr", cfg.GRPCAddr)
		errCh <- r.grpcServer.Serve(lis)
	}()
	go func() {
		r.log.Info("OTLP/HTTP dinleniyor", "addr", cfg.HTTPAddr)
		if err := r.httpServer.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		r.shutdown()
		return nil
	case err := <-errCh:
		r.shutdown()
		return err
	}
}

func (r *Receiver) shutdown() {
	r.grpcServer.GracefulStop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = r.httpServer.Shutdown(ctx)
}

// consume, ortak giriş noktasıdır: çevir, tüket, sayaçları güncelle.
func (r *Receiver) consume(req *coltracepb.ExportTraceServiceRequest) (received, dropped int) {
	spans := ConvertTraces(req.ResourceSpans)
	if len(spans) == 0 {
		return 0, 0
	}
	dropped = r.consumer.Accept(spans)
	r.stats.SpansReceived.Add(uint64(len(spans)))
	if dropped > 0 {
		r.stats.SpansDropped.Add(uint64(dropped))
	}
	return len(spans), dropped
}

// defaultStreamWorkers, gRPC stream'lerini sabit bir goroutine havuzuna
// bağlar. Havuz olmadan her stream yeni goroutine açar; yüzlerce pod'lu bir
// kümede bu, stack büyümesi ve scheduler baskısı demek.
func defaultStreamWorkers() int {
	n := runtime.GOMAXPROCS(0) * 2
	if n < 4 {
		n = 4
	}
	if n > 64 {
		n = 64
	}
	return n
}

// keepalivePolicy, agresif ping atan agent'ların bağlantısını koparmamak için
// gevşek tutulur; agent tarafında yeniden bağlanma maliyeti bizde değil onda.
func keepalivePolicy() keepalive.EnforcementPolicy {
	return keepalive.EnforcementPolicy{
		MinTime:             10 * time.Second,
		PermitWithoutStream: true,
	}
}

// --- gRPC ---

type traceService struct {
	coltracepb.UnimplementedTraceServiceServer
	r *Receiver
}

func (t *traceService) Export(_ context.Context, req *coltracepb.ExportTraceServiceRequest) (*coltracepb.ExportTraceServiceResponse, error) {
	t.r.stats.Requests.Add(1)
	_, dropped := t.r.consume(req)
	resp := &coltracepb.ExportTraceServiceResponse{}
	if dropped > 0 {
		resp.PartialSuccess = &coltracepb.ExportTracePartialSuccess{
			RejectedSpans: int64(dropped),
			ErrorMessage:  "nabiz: kuyruk dolu, span düşürüldü",
		}
	}
	return resp, nil
}

// --- HTTP ---

const maxHTTPBody = 16 << 20

func (r *Receiver) handleHTTPTraces(w http.ResponseWriter, req *http.Request) {
	r.stats.Requests.Add(1)

	var body io.Reader = http.MaxBytesReader(w, req.Body, maxHTTPBody)
	if req.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(body)
		if err != nil {
			r.httpError(w, http.StatusBadRequest, "gzip çözülemedi")
			return
		}
		defer gz.Close()
		body = gz
	}

	raw, err := io.ReadAll(body)
	if err != nil {
		r.httpError(w, http.StatusBadRequest, "gövde okunamadı")
		return
	}

	contentType := req.Header.Get("Content-Type")
	isJSON := contentType == "application/json"

	var otlpReq coltracepb.ExportTraceServiceRequest
	if isJSON {
		err = protojson.Unmarshal(raw, &otlpReq)
	} else {
		err = proto.Unmarshal(raw, &otlpReq)
	}
	if err != nil {
		r.httpError(w, http.StatusBadRequest, "OTLP gövdesi çözümlenemedi")
		return
	}

	_, dropped := r.consume(&otlpReq)

	resp := &coltracepb.ExportTraceServiceResponse{}
	if dropped > 0 {
		resp.PartialSuccess = &coltracepb.ExportTracePartialSuccess{
			RejectedSpans: int64(dropped),
			ErrorMessage:  "nabiz: kuyruk dolu, span düşürüldü",
		}
	}

	var out []byte
	if isJSON {
		out, _ = protojson.Marshal(resp)
		w.Header().Set("Content-Type", "application/json")
	} else {
		out, _ = proto.Marshal(resp)
		w.Header().Set("Content-Type", "application/x-protobuf")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}

func (r *Receiver) httpError(w http.ResponseWriter, code int, msg string) {
	r.stats.Errors.Add(1)
	http.Error(w, msg, code)
}
