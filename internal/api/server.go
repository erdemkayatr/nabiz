// Package api, ClickHouse üstündeki sorgu ucunu sunar.
//
// Sorguların tamamı önceden toplanmış tablolara (operation_stats,
// service_edges, trace_index) gider; ham spans tablosuna yalnızca tek bir
// trace'in detayı istendiğinde ve daraltılmış zaman aralığıyla inilir.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/erdemkayatr/nabiz/internal/identity"
	"github.com/erdemkayatr/nabiz/internal/topology"
)

// Server, HTTP sorgu ucudur.
type Server struct {
	conn     driver.Conn
	db       string
	identity *identity.Store
	logins   *loginLimiter
	log      *slog.Logger
}

// New, sorgu sunucusunu kurar.
func New(conn driver.Conn, db string, ident *identity.Store, log *slog.Logger) *Server {
	return &Server{
		conn:     conn,
		db:       db,
		identity: ident,
		// Beş dakikada on başarısız deneme: insan için bol, sözlük saldırısı
		// için işe yaramaz.
		logins: newLoginLimiter(10, 5*time.Minute),
		log:    log,
	}
}

// Handler, rotaları kurar.
//
// /healthz dışındaki her uç oturum ister. Telemetri uçları ayrıca yetki
// kontrolünden geçer ve yalnızca kullanıcının projelerine atanmış
// uygulamaların verisini döndürür.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	// --- kimlik ---
	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", s.handleLogout)
	mux.HandleFunc("GET /api/v1/auth/me", s.handleMe)
	mux.HandleFunc("POST /api/v1/auth/password", requireAuth(s.handleChangeOwnPassword))

	// --- telemetri ---
	mux.HandleFunc("GET /api/v1/services", requirePermission(permServices, s.handleServices))
	mux.HandleFunc("GET /api/v1/operations", requirePermission(permServices, s.handleOperations))
	mux.HandleFunc("GET /api/v1/topology", requirePermission(permTopology, s.handleTopology))
	mux.HandleFunc("GET /api/v1/traces", requirePermission(permTraces, s.handleTraceSearch))
	mux.HandleFunc("GET /api/v1/traces/{traceID}", requirePermission(permTraces, s.handleTraceDetail))

	// --- denetim düzlemi ---
	s.registerAdmin(mux)

	// Arayüz API ile aynı binary'den ve aynı kaynaktan sunulur.
	if err := registerUI(mux); err != nil {
		s.log.Error("arayüz bağlanamadı, yalnızca API sunuluyor", "err", err)
	}
	return s.withSession(mux)
}

// --- servisler ---

// ServiceSummary, bir servisin RED özeti.
type ServiceSummary struct {
	Service    string  `json:"service"`
	Namespace  string  `json:"namespace,omitempty"`
	Workload   string  `json:"workload,omitempty"`
	Calls      uint64  `json:"calls"`
	Errors     uint64  `json:"errors"`
	ErrorRate  float64 `json:"errorRate"`
	RatePerSec float64 `json:"ratePerSec"`
	AvgMS      float64 `json:"avgMs"`
	P50MS      float64 `json:"p50Ms"`
	P95MS      float64 `json:"p95Ms"`
	P99MS      float64 `json:"p99Ms"`
	MaxMS      float64 `json:"maxMs"`
}

func (s *Server) handleServices(w http.ResponseWriter, r *http.Request) {
	from, to, err := timeRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	// Servis seviyesindeki hız/hata, yalnızca giriş span'lerinden (server /
	// consumer) hesaplanır; aksi halde her iç çağrı ikinci kez sayılır.
	sc := scopeFor(r)
	if sc.empty() {
		writeJSON(w, map[string]any{"from": from, "to": to, "services": []ServiceSummary{}})
		return
	}
	filter, filterArgs := sc.filterServiceName("service_name")

	q := fmt.Sprintf(`
		SELECT
			service_name,
			any(k8s_namespace),
			any(k8s_workload),
			sum(calls),
			sum(errors),
			sum(duration_sum_ns),
			max(duration_max_ns),
			quantilesMerge(0.5, 0.95, 0.99)(duration_quantiles)
		FROM %s.operation_stats
		WHERE bucket >= ? AND bucket < ? AND kind IN ('server', 'consumer')%s
		GROUP BY service_name
		ORDER BY sum(calls) DESC`, s.db, filter)

	rows, err := s.conn.Query(r.Context(), q, append([]any{from, to}, filterArgs...)...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()

	window := to.Sub(from).Seconds()
	out := []ServiceSummary{}
	for rows.Next() {
		var (
			svc, ns, wl    string
			calls, errs    uint64
			durSum, durMax uint64
			q              []float64
		)
		if err := rows.Scan(&svc, &ns, &wl, &calls, &errs, &durSum, &durMax, &q); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		out = append(out, ServiceSummary{
			Service:    svc,
			Namespace:  ns,
			Workload:   wl,
			Calls:      calls,
			Errors:     errs,
			ErrorRate:  ratio(errs, calls),
			RatePerSec: float64(calls) / window,
			AvgMS:      nsToMS(avg(durSum, calls)),
			P50MS:      nsToMS(quantileAt(q, 0)),
			P95MS:      nsToMS(quantileAt(q, 1)),
			P99MS:      nsToMS(quantileAt(q, 2)),
			MaxMS:      nsToMS(float64(durMax)),
		})
	}
	writeJSON(w, map[string]any{"from": from, "to": to, "services": out})
}

// --- işlemler ---

// OperationSummary, servis içindeki tek bir işlemin RED özeti.
type OperationSummary struct {
	Service   string  `json:"service"`
	Operation string  `json:"operation"`
	Kind      string  `json:"kind"`
	Calls     uint64  `json:"calls"`
	Errors    uint64  `json:"errors"`
	ErrorRate float64 `json:"errorRate"`
	AvgMS     float64 `json:"avgMs"`
	P95MS     float64 `json:"p95Ms"`
	P99MS     float64 `json:"p99Ms"`
}

func (s *Server) handleOperations(w http.ResponseWriter, r *http.Request) {
	from, to, err := timeRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	service := r.URL.Query().Get("service")
	if service == "" {
		writeError(w, http.StatusBadRequest, errors.New("service parametresi zorunlu"))
		return
	}
	// Kapsam dışı bir servis için 403 dönmek, o servisin var olduğunu ele
	// verir. Boş sonuç döndürmek hem güvenli hem yeterli.
	if sc := scopeFor(r); !sc.allows(service) {
		writeJSON(w, map[string]any{"from": from, "to": to, "operations": []OperationSummary{}})
		return
	}

	q := fmt.Sprintf(`
		SELECT
			operation,
			kind,
			sum(calls),
			sum(errors),
			sum(duration_sum_ns),
			quantilesMerge(0.5, 0.95, 0.99)(duration_quantiles)
		FROM %s.operation_stats
		WHERE bucket >= ? AND bucket < ? AND service_name = ?
		GROUP BY operation, kind
		ORDER BY sum(calls) DESC
		LIMIT 500`, s.db)

	rows, err := s.conn.Query(r.Context(), q, from, to, service)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()

	out := []OperationSummary{}
	for rows.Next() {
		var (
			op, kind    string
			calls, errs uint64
			durSum      uint64
			qs          []float64
		)
		if err := rows.Scan(&op, &kind, &calls, &errs, &durSum, &qs); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		out = append(out, OperationSummary{
			Service:   service,
			Operation: op,
			Kind:      kind,
			Calls:     calls,
			Errors:    errs,
			ErrorRate: ratio(errs, calls),
			AvgMS:     nsToMS(avg(durSum, calls)),
			P95MS:     nsToMS(quantileAt(qs, 1)),
			P99MS:     nsToMS(quantileAt(qs, 2)),
		})
	}
	writeJSON(w, map[string]any{"from": from, "to": to, "operations": out})
}

// --- topoloji ---

// Node, topoloji grafiğindeki bir düğüm.
type Node struct {
	ID        string  `json:"id"`
	Label     string  `json:"label"`
	Type      string  `json:"type"` // service | database | messaging | external | user
	Namespace string  `json:"namespace,omitempty"`
	Workload  string  `json:"workload,omitempty"`
	Calls     uint64  `json:"calls"`
	Errors    uint64  `json:"errors"`
	ErrorRate float64 `json:"errorRate"`
}

// Edge, iki düğüm arasındaki çağrı ilişkisi.
type Edge struct {
	Source    string  `json:"source"`
	Target    string  `json:"target"`
	Type      string  `json:"type"`
	Calls     uint64  `json:"calls"`
	Errors    uint64  `json:"errors"`
	ErrorRate float64 `json:"errorRate"`
	AvgMS     float64 `json:"avgMs"`
	P95MS     float64 `json:"p95Ms"`
	P99MS     float64 `json:"p99Ms"`
	MaxMS     float64 `json:"maxMs"`
}

func (s *Server) handleTopology(w http.ResponseWriter, r *http.Request) {
	from, to, err := timeRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	// level=service  -> servis adına göre (varsayılan)
	// level=workload -> k8s deployment/statefulset'e göre
	// level=namespace-> k8s namespace'ine göre
	level := r.URL.Query().Get("level")
	clientExpr, serverExpr := groupExprs(level)

	sumBuckets := make([]string, 0, topology.BucketCount)
	for _, c := range bucketColumns() {
		sumBuckets = append(sumBuckets, "sum("+c+")")
	}

	sc := scopeFor(r)
	if sc.empty() {
		writeJSON(w, map[string]any{
			"from": from, "to": to, "level": levelName(level),
			"nodes": []Node{}, "edges": []Edge{},
		})
		return
	}
	filter, filterArgs := sc.filterTopology()

	q := fmt.Sprintf(`
		SELECT
			%s AS src,
			%s AS dst,
			conn_type,
			any(client_namespace), any(client_workload),
			any(server_namespace), any(server_workload),
			sum(calls), sum(errors), sum(duration_sum_ns), max(duration_max_ns),
			%s
		FROM %s.service_edges
		WHERE bucket >= ? AND bucket < ?%s
		GROUP BY src, dst, conn_type
		HAVING sum(calls) > 0
		ORDER BY sum(calls) DESC
		LIMIT 2000`,
		clientExpr, serverExpr, strings.Join(sumBuckets, ", "), s.db, filter)

	rows, err := s.conn.Query(r.Context(), q, append([]any{from, to}, filterArgs...)...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()

	nodes := map[string]*Node{}
	edges := []Edge{}

	for rows.Next() {
		var (
			src, dst, connType          string
			clientNS, clientWL          string
			serverNS, serverWL          string
			calls, errs, durSum, durMax uint64
		)
		bucketVals := make([]uint64, topology.BucketCount)
		scanArgs := []any{
			&src, &dst, &connType,
			&clientNS, &clientWL, &serverNS, &serverWL,
			&calls, &errs, &durSum, &durMax,
		}
		for i := range bucketVals {
			scanArgs = append(scanArgs, &bucketVals[i])
		}
		if err := rows.Scan(scanArgs...); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		srcType := "service"
		if connType == "entry" {
			srcType = "user"
		}
		dstType := "service"
		switch connType {
		case "database", "messaging", "external":
			dstType = connType
		}

		upsertNode(nodes, src, srcType, clientNS, clientWL)
		dn := upsertNode(nodes, dst, dstType, serverNS, serverWL)
		dn.Calls += calls
		dn.Errors += errs
		dn.ErrorRate = ratio(dn.Errors, dn.Calls)

		edges = append(edges, Edge{
			Source:    src,
			Target:    dst,
			Type:      connType,
			Calls:     calls,
			Errors:    errs,
			ErrorRate: ratio(errs, calls),
			AvgMS:     nsToMS(avg(durSum, calls)),
			P95MS:     histogramQuantile(bucketVals, 0.95),
			P99MS:     histogramQuantile(bucketVals, 0.99),
			MaxMS:     nsToMS(float64(durMax)),
		})
	}

	nodeList := make([]Node, 0, len(nodes))
	for _, n := range nodes {
		nodeList = append(nodeList, *n)
	}
	writeJSON(w, map[string]any{
		"from":  from,
		"to":    to,
		"level": levelName(level),
		"nodes": nodeList,
		"edges": edges,
	})
}

// groupExprs, topoloji seviyesine göre gruplama ifadelerini verir.
// Veritabanı/kuyruk gibi k8s'te karşılığı olmayan uçlar her seviyede kendi
// adıyla kalır; aksi halde grafikten düşerlerdi.
func groupExprs(level string) (client, server string) {
	switch level {
	case "workload":
		return "if(client_workload != '', concat(client_namespace, '/', client_workload), client)",
			"if(server_workload != '', concat(server_namespace, '/', server_workload), server)"
	case "namespace":
		return "if(client_namespace != '', client_namespace, client)",
			"if(server_namespace != '', server_namespace, server)"
	default:
		return "client", "server"
	}
}

func levelName(level string) string {
	switch level {
	case "workload", "namespace":
		return level
	default:
		return "service"
	}
}

func upsertNode(nodes map[string]*Node, id, typ, ns, wl string) *Node {
	if n, ok := nodes[id]; ok {
		if n.Namespace == "" {
			n.Namespace = ns
		}
		if n.Workload == "" {
			n.Workload = wl
		}
		return n
	}
	n := &Node{ID: id, Label: id, Type: typ, Namespace: ns, Workload: wl}
	nodes[id] = n
	return n
}

// --- trace arama ---

// TraceSummary, trace listesindeki bir satır.
type TraceSummary struct {
	TraceID     string    `json:"traceId"`
	RootService string    `json:"rootService"`
	RootName    string    `json:"rootName"`
	Start       time.Time `json:"start"`
	DurationMS  float64   `json:"durationMs"`
	Spans       uint64    `json:"spans"`
	Errors      uint64    `json:"errors"`
	Services    []string  `json:"services"`
}

func (s *Server) handleTraceSearch(w http.ResponseWriter, r *http.Request) {
	from, to, err := timeRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	qp := r.URL.Query()

	sc := scopeFor(r)
	if sc.empty() {
		writeJSON(w, map[string]any{"from": from, "to": to, "traces": []TraceSummary{}})
		return
	}

	where := []string{"start >= ? AND start < ?"}
	args := []any{from, to}
	if filter, filterArgs := sc.filterTraces(); filter != "" {
		// filterTraces " AND ..." döndürüyor; burada listeye eklendiği için
		// baştaki ek kaldırılır.
		where = append(where, strings.TrimPrefix(filter, " AND "))
		args = append(args, filterArgs...)
	}

	if svc := qp.Get("service"); svc != "" {
		where = append(where, "has(services, ?)")
		args = append(args, svc)
	}
	if v := qp.Get("minDurationMs"); v != "" {
		ms, convErr := strconv.ParseFloat(v, 64)
		if convErr != nil {
			writeError(w, http.StatusBadRequest, errors.New("minDurationMs sayı olmalı"))
			return
		}
		where = append(where, "duration_ns >= ?")
		args = append(args, uint64(ms*1e6))
	}
	if qp.Get("onlyErrors") == "true" {
		where = append(where, "errors > 0")
	}

	limit := 100
	if v := qp.Get("limit"); v != "" {
		if n, convErr := strconv.Atoi(v); convErr == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}

	q := fmt.Sprintf(`
		SELECT
			trace_id,
			max(root_service),
			max(root_name),
			min(start),
			max(duration_ns),
			sum(spans),
			sum(errors),
			groupUniqArrayArray(services)
		FROM %s.trace_index
		WHERE %s
		GROUP BY trace_id
		ORDER BY min(start) DESC
		LIMIT %d`, s.db, strings.Join(where, " AND "), limit)

	rows, err := s.conn.Query(r.Context(), q, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()

	out := []TraceSummary{}
	for rows.Next() {
		var (
			id, rootSvc, rootName string
			start                 time.Time
			durNS, spans, errs    uint64
			services              []string
		)
		if err := rows.Scan(&id, &rootSvc, &rootName, &start, &durNS, &spans, &errs, &services); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		out = append(out, TraceSummary{
			TraceID:     id,
			RootService: rootSvc,
			RootName:    rootName,
			Start:       start,
			DurationMS:  nsToMS(float64(durNS)),
			Spans:       spans,
			Errors:      errs,
			Services:    services,
		})
	}
	writeJSON(w, map[string]any{"from": from, "to": to, "traces": out})
}

// --- trace detayı ---

// SpanEvent, span üzerindeki bir olay (çoğunlukla istisna).
type SpanEvent struct {
	Timestamp  time.Time         `json:"timestamp"`
	Name       string            `json:"name"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// CodeLocation, span'in geldiği kaynak konumu.
type CodeLocation struct {
	Function   string `json:"function,omitempty"`
	File       string `json:"file,omitempty"`
	Line       int    `json:"line,omitempty"`
	Namespace  string `json:"namespace,omitempty"`
	StackTrace string `json:"stackTrace,omitempty"`
}

// SpanView, trace detayındaki tek bir span.
type SpanView struct {
	SpanID       string    `json:"spanId"`
	ParentSpanID string    `json:"parentSpanId,omitempty"`
	Name         string    `json:"name"`
	Kind         string    `json:"kind"`
	Service      string    `json:"service"`
	Namespace    string    `json:"namespace,omitempty"`
	Pod          string    `json:"pod,omitempty"`
	Workload     string    `json:"workload,omitempty"`
	Node         string    `json:"node,omitempty"`
	Start        time.Time `json:"start"`
	DurationMS   float64   `json:"durationMs"`
	// SelfMS, bu span'de geçen ama çocuklarında geçmeyen süre. Bir isteğin
	// nerede yavaşladığını toplam süre değil bu sayı söyler.
	SelfMS     float64           `json:"selfMs"`
	Status     string            `json:"status"`
	StatusMsg  string            `json:"statusMessage,omitempty"`
	Code       *CodeLocation     `json:"code,omitempty"`
	Events     []SpanEvent       `json:"events,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// Hotspot, trace içindeki toplam self time'a göre en pahalı işlemler.
type Hotspot struct {
	Name    string        `json:"name"`
	Service string        `json:"service"`
	Kind    string        `json:"kind"`
	SelfMS  float64       `json:"selfMs"`
	Share   float64       `json:"share"`
	Count   int           `json:"count"`
	Code    *CodeLocation `json:"code,omitempty"`
}

func (s *Server) handleTraceDetail(w http.ResponseWriter, r *http.Request) {
	traceID := r.PathValue("traceID")
	if len(traceID) != 32 {
		writeError(w, http.StatusBadRequest, errors.New("traceID 32 hex karakter olmalı"))
		return
	}

	sc := scopeFor(r)
	if sc.empty() {
		writeErrorCode(w, http.StatusNotFound, "trace bulunamadı", "not_found")
		return
	}

	// Önce indeksten zaman aralığını al: spans tablosunda partition budaması
	// yapabilmek için. İndekssiz sorgu tüm günleri tarardı.
	//
	// Aynı sorgu erişim kontrolünü de yapar: trace'in servislerinden en az
	// biri kapsamda değilse trace hiç bulunamamış sayılır.
	filter, filterArgs := sc.filterTraces()
	var start, end time.Time
	idxQ := fmt.Sprintf(
		`SELECT min(start), max(end) FROM %s.trace_index WHERE trace_id = ?%s GROUP BY trace_id`,
		s.db, filter)
	if err := s.conn.QueryRow(r.Context(), idxQ,
		append([]any{traceID}, filterArgs...)...).Scan(&start, &end); err != nil {
		writeErrorCode(w, http.StatusNotFound, "trace bulunamadı", "not_found")
		return
	}

	q := fmt.Sprintf(`
		SELECT
			span_id, parent_span_id, name, kind,
			service_name, k8s_namespace, k8s_pod, k8s_workload, k8s_node,
			timestamp, duration_ns, status_code, status_message, span_attributes,
			events_timestamp, events_name, events_attributes
		FROM %s.spans
		WHERE trace_id = ? AND timestamp >= ? AND timestamp <= ?
		ORDER BY timestamp ASC
		LIMIT 10000`, s.db)

	rows, err := s.conn.Query(r.Context(), q, traceID,
		start.Add(-time.Minute), end.Add(time.Minute))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()

	out := []SpanView{}
	durations := map[string]uint64{}
	childSum := map[string]uint64{}

	for rows.Next() {
		var (
			spanID, parentID, name, kind string
			svc, ns, pod, workload, node string
			ts                           time.Time
			durNS                        uint64
			status, statusMsg            string
			attrs                        map[string]string
			evTimes                      []time.Time
			evNames                      []string
			evAttrs                      []map[string]string
		)
		if err := rows.Scan(&spanID, &parentID, &name, &kind,
			&svc, &ns, &pod, &workload, &node,
			&ts, &durNS, &status, &statusMsg, &attrs,
			&evTimes, &evNames, &evAttrs); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		durations[spanID] = durNS
		if parentID != "" {
			childSum[parentID] += durNS
		}

		out = append(out, SpanView{
			SpanID:       spanID,
			ParentSpanID: parentID,
			Name:         name,
			Kind:         kind,
			Service:      svc,
			Namespace:    ns,
			Pod:          pod,
			Workload:     workload,
			Node:         node,
			Start:        ts,
			DurationMS:   nsToMS(float64(durNS)),
			Status:       status,
			StatusMsg:    statusMsg,
			Code:         codeLocationOf(attrs),
			Events:       buildEvents(evTimes, evNames, evAttrs),
			Attributes:   attrs,
		})
	}

	// Self time'ı burada hesaplıyoruz, istemcide değil: her istemcinin aynı
	// aritmetiği tekrar yazması hem israf hem de tutarsızlık kaynağı.
	for i := range out {
		self := durations[out[i].SpanID]
		if children := childSum[out[i].SpanID]; children < self {
			self -= children
		} else {
			// Paralel çocuklar toplamda ebeveynden uzun sürebilir; negatif
			// self time anlamsız olacağı için sıfıra kırpılır.
			self = 0
		}
		out[i].SelfMS = nsToMS(float64(self))
	}

	writeJSON(w, map[string]any{
		"traceId":  traceID,
		"spans":    out,
		"hotspots": buildHotspots(out),
	})
}

// codeLocationOf, span attribute'larından kod konumunu çıkarır.
// Hem güncel (code.function.name) hem eski (code.function) adlar okunur.
func codeLocationOf(attrs map[string]string) *CodeLocation {
	if len(attrs) == 0 {
		return nil
	}
	pick := func(keys ...string) string {
		for _, k := range keys {
			if v := attrs[k]; v != "" {
				return v
			}
		}
		return ""
	}
	loc := CodeLocation{
		Function:   pick("code.function.name", "code.function"),
		File:       pick("code.file.path", "code.filepath"),
		Namespace:  pick("code.namespace"),
		StackTrace: pick("code.stacktrace"),
	}
	if n, err := strconv.Atoi(pick("code.line.number", "code.lineno")); err == nil {
		loc.Line = n
	}
	if loc.Function == "" && loc.File == "" && loc.StackTrace == "" {
		return nil
	}
	return &loc
}

// buildEvents, ClickHouse'un paralel dizilerini olay listesine çevirir.
func buildEvents(times []time.Time, names []string, attrs []map[string]string) []SpanEvent {
	if len(names) == 0 {
		return nil
	}
	events := make([]SpanEvent, 0, len(names))
	for i := range names {
		ev := SpanEvent{Name: names[i]}
		if i < len(times) {
			ev.Timestamp = times[i]
		}
		if i < len(attrs) {
			ev.Attributes = attrs[i]
		}
		events = append(events, ev)
	}
	return events
}

// buildHotspots, self time'a göre en pahalı işlemleri özetler.
//
// Bir trace'te yüzlerce span olabilir; "en yavaş span hangisi" sorusuna
// bakarak cevap vermek şelaleyi satır satır okumayı gerektirir. Aynı adı
// taşıyan span'leri toplamak, N+1 sorgu gibi desenleri de görünür kılar:
// tek tek 2 ms süren 80 sorgu, listede 160 ms olarak en üste çıkar.
func buildHotspots(spans []SpanView) []Hotspot {
	if len(spans) == 0 {
		return []Hotspot{}
	}
	type agg struct {
		Hotspot
		total float64
	}
	byKey := map[string]*agg{}
	var totalSelf float64

	for i := range spans {
		s := &spans[i]
		totalSelf += s.SelfMS
		key := s.Service + "\x00" + s.Name
		a, ok := byKey[key]
		if !ok {
			a = &agg{Hotspot: Hotspot{Name: s.Name, Service: s.Service, Kind: s.Kind, Code: s.Code}}
			byKey[key] = a
		}
		a.SelfMS += s.SelfMS
		a.Count++
		if a.Code == nil {
			a.Code = s.Code
		}
	}

	out := make([]Hotspot, 0, len(byKey))
	for _, a := range byKey {
		if totalSelf > 0 {
			a.Share = a.SelfMS / totalSelf
		}
		out = append(out, a.Hotspot)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SelfMS > out[j].SelfMS })
	if len(out) > 10 {
		out = out[:10]
	}
	return out
}

// --- yardımcılar ---

func bucketColumns() []string {
	cols := make([]string, 0, topology.BucketCount)
	for _, b := range topology.LatencyBounds {
		cols = append(cols, "le_"+strconv.FormatFloat(b, 'f', -1, 64)+"ms")
	}
	return append(cols, "le_inf")
}

// histogramQuantile, kova sayımlarından doğrusal interpolasyonla quantile
// tahmin eder. Sonuç milisaniyedir.
func histogramQuantile(counts []uint64, q float64) float64 {
	var total uint64
	for _, c := range counts {
		total += c
	}
	if total == 0 {
		return 0
	}
	target := q * float64(total)
	var cum float64
	for i, c := range counts {
		prev := cum
		cum += float64(c)
		if cum < target {
			continue
		}
		if i >= len(topology.LatencyBounds) {
			// +Inf kovası: alt sınırı döndürmekten başka bilgi yok.
			return topology.LatencyBounds[len(topology.LatencyBounds)-1]
		}
		lower := 0.0
		if i > 0 {
			lower = topology.LatencyBounds[i-1]
		}
		upper := topology.LatencyBounds[i]
		if c == 0 {
			return upper
		}
		return lower + (upper-lower)*((target-prev)/float64(c))
	}
	return topology.LatencyBounds[len(topology.LatencyBounds)-1]
}

func timeRange(r *http.Request) (time.Time, time.Time, error) {
	now := time.Now().UTC()
	qp := r.URL.Query()

	to := now
	if v := qp.Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return time.Time{}, time.Time{}, errors.New("to parametresi RFC3339 olmalı")
		}
		to = t.UTC()
	}

	from := to.Add(-time.Hour)
	if v := qp.Get("from"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			// "15m", "2h" gibi göreli değer: to'dan geriye.
			from = to.Add(-d)
		} else if t, err := time.Parse(time.RFC3339, v); err == nil {
			from = t.UTC()
		} else {
			return time.Time{}, time.Time{}, errors.New("from parametresi RFC3339 ya da süre (15m, 2h) olmalı")
		}
	}
	if !from.Before(to) {
		return time.Time{}, time.Time{}, errors.New("from, to'dan önce olmalı")
	}
	return from, to, nil
}

func ratio(part, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(part) / float64(total)
}

func avg(sum, count uint64) float64 {
	if count == 0 {
		return 0
	}
	return float64(sum) / float64(count)
}

func nsToMS(ns float64) float64 { return ns / 1e6 }

func quantileAt(q []float64, i int) float64 {
	if i < len(q) {
		return q[i]
	}
	return 0
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// writeJSONStatus, durum koduyla birlikte JSON yazar.
func writeJSONStatus(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// decodeJSON, istek gövdesini çözer. Bilinmeyen alanlar hata verir:
// "isSuperadmin" yazıp yetkinin neden verilmediğini aramak yerine anında
// hata almak yeğdir.
func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("istek gövdesi çözümlenemedi: %w", err)
	}
	return nil
}

// writeErrorCode, istemcinin dallanabilmesi için makine okunur bir kod da
// ekler; arayüz mesaj metnine göre karar vermek zorunda kalmasın.
func writeErrorCode(w http.ResponseWriter, status int, msg, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg, "code": code})
}

func writeError(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
