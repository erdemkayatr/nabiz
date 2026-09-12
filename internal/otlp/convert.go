package otlp

import (
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/erdemkayatr/nabiz/internal/model"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// resourceInfo, tek bir ResourceSpans için bir kez çözülen servis/k8s
// boyutlarıdır. Aynı resource altındaki yüzlerce span bunu paylaşır, bu yüzden
// attribute taraması span başına değil resource başına yapılır.
type resourceInfo struct {
	serviceName      string
	serviceNamespace string
	serviceVersion   string
	serviceInstance  string
	sdkLanguage      string

	k8sCluster   string
	k8sNamespace string
	k8sPod       string
	k8sWorkload  string
	k8sNode      string
	k8sContainer string

	attrs map[string]string
}

func parseResource(r *resourcepb.Resource) resourceInfo {
	info := resourceInfo{serviceName: "unknown_service"}
	if r == nil {
		return info
	}
	info.attrs = make(map[string]string, len(r.Attributes))

	var replicaSet string
	for _, kv := range r.Attributes {
		v := anyValueToString(kv.Value)
		info.attrs[kv.Key] = v
		switch kv.Key {
		case "service.name":
			if v != "" {
				info.serviceName = v
			}
		case "service.namespace":
			info.serviceNamespace = v
		case "service.version":
			info.serviceVersion = v
		case "service.instance.id":
			info.serviceInstance = v
		case "telemetry.sdk.language":
			info.sdkLanguage = v
		case "k8s.cluster.name":
			info.k8sCluster = v
		case "k8s.namespace.name":
			info.k8sNamespace = v
		case "k8s.pod.name":
			info.k8sPod = v
		case "k8s.node.name":
			info.k8sNode = v
		case "k8s.container.name":
			info.k8sContainer = v
		case "k8s.deployment.name", "k8s.statefulset.name", "k8s.daemonset.name", "k8s.job.name", "k8s.cronjob.name":
			info.k8sWorkload = v
		case "k8s.replicaset.name":
			replicaSet = v
		}
	}

	// Deployment adı yoksa ReplicaSet'ten türet: "api-7d9f8b6c5d" -> "api".
	if info.k8sWorkload == "" && replicaSet != "" {
		info.k8sWorkload = trimPodHash(replicaSet)
	}
	// O da yoksa pod adından türet: "api-7d9f8b6c5d-x2k9p" -> "api".
	if info.k8sWorkload == "" && info.k8sPod != "" {
		info.k8sWorkload = trimPodHash(trimPodHash(info.k8sPod))
	}
	return info
}

// trimPodHash, son "-<hash>" ekini atar. Hash olmayan bir sonek varsa dokunmaz.
func trimPodHash(name string) string {
	i := strings.LastIndexByte(name, '-')
	if i <= 0 || i == len(name)-1 {
		return name
	}
	suffix := name[i+1:]
	if len(suffix) < 3 || len(suffix) > 10 {
		return name
	}
	for j := 0; j < len(suffix); j++ {
		c := suffix[j]
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'z') {
			return name
		}
	}
	return name[:i]
}

// ConvertTraces, OTLP ResourceSpans listesini dahili Span listesine çevirir.
// Tek geçişte çalışır ve çıktı dilimini tek seferde ayırır.
func ConvertTraces(resourceSpans []*tracepb.ResourceSpans) []*model.Span {
	total := 0
	for _, rs := range resourceSpans {
		for _, ss := range rs.ScopeSpans {
			total += len(ss.Spans)
		}
	}
	if total == 0 {
		return nil
	}

	out := make([]*model.Span, 0, total)
	backing := make([]model.Span, total)
	idx := 0

	for _, rs := range resourceSpans {
		info := parseResource(rs.Resource)
		for _, ss := range rs.ScopeSpans {
			scopeName, scopeVersion := "", ""
			if ss.Scope != nil {
				scopeName, scopeVersion = ss.Scope.Name, ss.Scope.Version
			}
			for _, sp := range ss.Spans {
				s := &backing[idx]
				idx++
				convertSpan(sp, &info, scopeName, scopeVersion, s)
				out = append(out, s)
			}
		}
	}
	return out
}

func convertSpan(sp *tracepb.Span, info *resourceInfo, scopeName, scopeVersion string, s *model.Span) {
	s.Timestamp = time.Unix(0, int64(sp.StartTimeUnixNano)).UTC()
	s.TraceID = hex.EncodeToString(sp.TraceId)
	s.SpanID = hex.EncodeToString(sp.SpanId)
	if len(sp.ParentSpanId) > 0 {
		s.ParentSpanID = hex.EncodeToString(sp.ParentSpanId)
	}
	s.TraceState = sp.TraceState
	s.Flags = sp.Flags
	s.Name = sp.Name
	s.Kind = model.SpanKind(sp.Kind)

	if sp.EndTimeUnixNano > sp.StartTimeUnixNano {
		s.DurationNS = sp.EndTimeUnixNano - sp.StartTimeUnixNano
	}
	if sp.Status != nil {
		s.StatusCode = model.StatusCode(sp.Status.Code)
		s.StatusMessage = sp.Status.Message
	}

	s.ServiceName = info.serviceName
	s.ServiceNamespace = info.serviceNamespace
	s.ServiceVersion = info.serviceVersion
	s.ServiceInstance = info.serviceInstance
	s.SDKLanguage = info.sdkLanguage
	s.K8sCluster = info.k8sCluster
	s.K8sNamespace = info.k8sNamespace
	s.K8sPod = info.k8sPod
	s.K8sWorkload = info.k8sWorkload
	s.K8sNode = info.k8sNode
	s.K8sContainer = info.k8sContainer
	s.ResourceAttrs = info.attrs

	attrs := make(map[string]string, len(sp.Attributes)+2)
	if scopeName != "" {
		attrs["otel.scope.name"] = scopeName
	}
	if scopeVersion != "" {
		attrs["otel.scope.version"] = scopeVersion
	}
	for _, kv := range sp.Attributes {
		v := anyValueToString(kv.Value)
		attrs[kv.Key] = v
		promoteAttribute(s, kv.Key, v)
	}
	s.SpanAttrs = attrs

	if len(sp.Events) > 0 {
		s.Events = make([]model.Event, 0, len(sp.Events))
		for _, e := range sp.Events {
			s.Events = append(s.Events, model.Event{
				Timestamp: time.Unix(0, int64(e.TimeUnixNano)).UTC(),
				Name:      e.Name,
				Attrs:     kvToMap(e.Attributes),
			})
		}
	}
	if len(sp.Links) > 0 {
		s.Links = make([]model.Link, 0, len(sp.Links))
		for _, l := range sp.Links {
			s.Links = append(s.Links, model.Link{
				TraceID: hex.EncodeToString(l.TraceId),
				SpanID:  hex.EncodeToString(l.SpanId),
				Attrs:   kvToMap(l.Attributes),
			})
		}
	}
}

// promoteAttribute, sık sorgulanan semantic convention alanlarını kolona taşır.
// Hem güncel (http.request.method) hem eski (http.method) adlar desteklenir;
// .NET auto-instrumentation sürüme göre ikisinden birini gönderiyor.
func promoteAttribute(s *model.Span, key, val string) {
	switch key {
	case "http.request.method", "http.method":
		s.HTTPMethod = val
	case "http.route":
		s.HTTPRoute = val
	case "http.response.status_code", "http.status_code":
		if n, err := strconv.ParseUint(val, 10, 16); err == nil {
			s.HTTPStatusCode = uint16(n)
		}
	case "url.full", "http.url":
		s.HTTPURL = val
	case "db.system", "db.system.name":
		s.DBSystem = val
	case "db.namespace", "db.name":
		s.DBName = val
	case "db.query.text", "db.statement":
		s.DBStatement = val
	case "rpc.system":
		s.RPCSystem = val
	case "rpc.service":
		s.RPCService = val
	case "rpc.method":
		s.RPCMethod = val
	case "messaging.system":
		s.MessagingSystem = val
	case "messaging.destination.name", "messaging.destination":
		s.MessagingDest = val
	case "peer.service":
		s.PeerService = val
	case "server.address", "net.peer.name":
		if s.PeerAddress == "" || key == "server.address" {
			s.PeerAddress = val
		}
	case "server.port", "net.peer.port":
		if n, err := strconv.ParseUint(val, 10, 16); err == nil {
			s.PeerPort = uint16(n)
		}
	}
}

func kvToMap(kvs []*commonpb.KeyValue) map[string]string {
	if len(kvs) == 0 {
		return nil
	}
	m := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		m[kv.Key] = anyValueToString(kv.Value)
	}
	return m
}

func anyValueToString(v *commonpb.AnyValue) string {
	if v == nil {
		return ""
	}
	switch x := v.Value.(type) {
	case *commonpb.AnyValue_StringValue:
		return x.StringValue
	case *commonpb.AnyValue_BoolValue:
		if x.BoolValue {
			return "true"
		}
		return "false"
	case *commonpb.AnyValue_IntValue:
		return strconv.FormatInt(x.IntValue, 10)
	case *commonpb.AnyValue_DoubleValue:
		return strconv.FormatFloat(x.DoubleValue, 'f', -1, 64)
	case *commonpb.AnyValue_BytesValue:
		return hex.EncodeToString(x.BytesValue)
	case *commonpb.AnyValue_ArrayValue:
		var b strings.Builder
		b.WriteByte('[')
		for i, item := range x.ArrayValue.Values {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(anyValueToString(item))
		}
		b.WriteByte(']')
		return b.String()
	case *commonpb.AnyValue_KvlistValue:
		var b strings.Builder
		b.WriteByte('{')
		for i, item := range x.KvlistValue.Values {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(item.Key)
			b.WriteByte('=')
			b.WriteString(anyValueToString(item.Value))
		}
		b.WriteByte('}')
		return b.String()
	}
	return ""
}
