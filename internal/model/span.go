// Package model holds nabiz's internal data model. The model is kept flat so
// it can be written straight into column-based storage (ClickHouse): instead
// of walking maps and interfaces on the hot path, frequently queried fields
// are promoted to columns.
package model

import "time"

// SpanKind matches the OTLP span kind values exactly.
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

// StatusCode matches the OTLP status code values exactly.
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

// Event is an event on a span, exceptions included.
type Event struct {
	Timestamp time.Time
	Name      string
	Attrs     map[string]string
}

// Link is a reference to another trace or span.
type Link struct {
	TraceID string
	SpanID  string
	Attrs   map[string]string
}

// Span is the record of a single unit of work.
//
// The field order matches the column order in ClickHouse; the batch insert
// varsayar (bkz. internal/storage/clickhouse).
type Span struct {
	Timestamp    time.Time
	TraceID      string // 32 hex karakter
	SpanID       string // 16 hex karakter
	ParentSpanID string // 16 hex characters, or empty
	TraceState   string
	Flags        uint32

	Name          string
	Kind          SpanKind
	DurationNS    uint64
	StatusCode    StatusCode
	StatusMessage string

	// Service identity, from the resource attributes.
	ServiceName      string
	ServiceNamespace string
	ServiceVersion   string
	ServiceInstance  string
	SDKLanguage      string

	// Kubernetes dimensions. The operator injects these into the pod through the
	// downward API; the collector never touches the k8s API on the hot path.
	K8sCluster   string
	K8sNamespace string
	K8sPod       string
	K8sWorkload  string // Deployment/StatefulSet/DaemonSet name
	K8sNode      string
	K8sContainer string

	// Frequently queried semantic convention fields are promoted to columns.
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

// ServiceKey is the service's identity in the topology.
func (s *Span) ServiceKey() string {
	if s.ServiceNamespace != "" {
		return s.ServiceNamespace + "/" + s.ServiceName
	}
	return s.ServiceName
}

// IsError says whether the span counts as an error. When the status is not
// ERROR, an HTTP 5xx still counts: some SDKs do not turn 5xx into ERROR.
func (s *Span) IsError() bool {
	return s.StatusCode == StatusError || s.HTTPStatusCode >= 500
}

// Batch is the set of spans carried through the pipeline.
type Batch struct {
	Spans    []*Span
	Received time.Time
}
