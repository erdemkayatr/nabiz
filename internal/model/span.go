// Package model holds nabiz'in dahili veri modelini tutar. Model, kolon bazlı
// depolamaya (ClickHouse) doğrudan yazılabilecek şekilde düz tutulur: sıcak
// yolda map/interface dolaşmak yerine sık sorgulanan alanlar kolon olarak
// yükseltilmiştir.
package model

import "time"

// SpanKind, OTLP span kind değerleriyle birebir aynıdır.
type SpanKind uint8

const (
	KindUnspecified SpanKind = iota
	KindInternal
	KindServer
	KindClient
	KindProducer
	KindConsumer
)

func (k SpanKind) String() string {
	switch k {
	case KindInternal:
		return "internal"
	case KindServer:
		return "server"
	case KindClient:
		return "client"
	case KindProducer:
		return "producer"
	case KindConsumer:
		return "consumer"
	default:
		return "unspecified"
	}
}

// StatusCode, OTLP status code değerleriyle birebir aynıdır.
type StatusCode uint8

const (
	StatusUnset StatusCode = iota
	StatusOK
	StatusError
)

func (s StatusCode) String() string {
	switch s {
	case StatusOK:
		return "ok"
	case StatusError:
		return "error"
	default:
		return "unset"
	}
}

// Event, span üzerindeki bir olaydır (exception dahil).
type Event struct {
	Timestamp time.Time
	Name      string
	Attrs     map[string]string
}

// Link, başka bir trace/span'e referanstır.
type Link struct {
	TraceID string
	SpanID  string
	Attrs   map[string]string
}

// Span, tek bir iş biriminin kaydıdır.
//
// Alan sırası ClickHouse'daki kolon sırasıyla aynıdır; batch insert bu sırayı
// varsayar (bkz. internal/storage/clickhouse).
type Span struct {
	Timestamp    time.Time
	TraceID      string // 32 hex karakter
	SpanID       string // 16 hex karakter
	ParentSpanID string // 16 hex karakter veya boş
	TraceState   string
	Flags        uint32

	Name          string
	Kind          SpanKind
	DurationNS    uint64
	StatusCode    StatusCode
	StatusMessage string

	// Servis kimliği (resource attributes'tan)
	ServiceName      string
	ServiceNamespace string
	ServiceVersion   string
	ServiceInstance  string
	SDKLanguage      string

	// Kubernetes boyutları. Operator bunları downward API ile pod'a enjekte
	// eder; collector sıcak yolda k8s API'sine hiç gitmez.
	K8sCluster   string
	K8sNamespace string
	K8sPod       string
	K8sWorkload  string // Deployment/StatefulSet/DaemonSet adı
	K8sNode      string
	K8sContainer string

	// Sık sorgulanan semantic convention alanları kolon olarak yükseltilir.
	HTTPMethod      string
	HTTPRoute       string
	HTTPStatusCode  uint16
	HTTPURL         string
	DBSystem        string
	DBName          string
	DBStatement     string
	RPCSystem       string
	RPCService      string
	RPCMethod       string
	MessagingSystem string
	MessagingDest   string
	PeerService     string
	PeerAddress     string
	PeerPort        uint16

	ResourceAttrs map[string]string
	SpanAttrs     map[string]string

	Events []Event
	Links  []Link
}

// ServiceKey, servisin topolojideki kimliğidir.
func (s *Span) ServiceKey() string {
	if s.ServiceNamespace != "" {
		return s.ServiceNamespace + "/" + s.ServiceName
	}
	return s.ServiceName
}

// IsError, span'in hata sayılıp sayılmayacağını söyler. Status ERROR değilse
// HTTP 5xx de hata kabul edilir: bazı SDK'lar 5xx'i ERROR'a çevirmez.
func (s *Span) IsError() bool {
	return s.StatusCode == StatusError || s.HTTPStatusCode >= 500
}

// Batch, pipeline boyunca taşınan span kümesidir.
type Batch struct {
	Spans    []*Span
	Received time.Time
}
