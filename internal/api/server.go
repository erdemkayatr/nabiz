// Package api serves the query endpoint on top of ClickHouse.
//
// Every query goes to a pre-aggregated table (operation_stats, service_edges,
// trace_index); the raw spans table is only touched when a single trace's
// detail is requested, and then with a narrowed time range.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/erdemkayatr/nabiz/internal/identity"
	"github.com/erdemkayatr/nabiz/internal/topology"
)

// Server is the HTTP query endpoint.
type Server struct {
	conn     driver.Conn
	db       string
	identity *identity.Store
	logins   *loginLimiter
	// dumpDir is where dump files fetched from applications are kept.
	dumpDir   string
	retention DumpRetention
	// inflight holds the cancel functions of running dump jobs.
	inflight   map[string]context.CancelFunc
	inflightMu sync.Mutex
	log        *slog.Logger
}

// SetDumpRetention replaces the retention rules.
func (s *Server) SetDumpRetention(r DumpRetention) { s.retention = r }

// New builds the query server.
func New(conn driver.Conn, db string, ident *identity.Store, dumpDir string, log *slog.Logger) *Server {
	return &Server{
		conn:      conn,
		db:        db,
		identity:  ident,
		dumpDir:   dumpDir,
		retention: DefaultDumpRetention(),
		inflight:  map[string]context.CancelFunc{},
		// Ten failed attempts per five minutes: generous for a human, useless
		// for a dictionary attack.
		logins: newLoginLimiter(10, 5*time.Minute),
		log:    log,
	}
}

// Handler wires up the routes.
//
// Every endpoint except /healthz requires a session. Telemetry endpoints also
// go through a permission check and only return data for applications assigned
// to the user's projects.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	// --- identity ---
	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", s.handleLogout)
	mux.HandleFunc("GET /api/v1/auth/me", s.handleMe)
	mux.HandleFunc("POST /api/v1/auth/password", requireAuth(s.handleChangeOwnPassword))

	// --- telemetry ---
	mux.HandleFunc("GET /api/v1/services", requirePermission(permServices, s.handleServices))
	mux.HandleFunc("GET /api/v1/operations", requirePermission(permServices, s.handleOperations))
	mux.HandleFunc("GET /api/v1/topology", requirePermission(permTopology, s.handleTopology))
	mux.HandleFunc("GET /api/v1/traces", requirePermission(permTraces, s.handleTraceSearch))
	mux.HandleFunc("GET /api/v1/traces/{traceID}", requirePermission(permTraces, s.handleTraceDetail))

	// --- control plane ---
	s.registerAdmin(mux)
	s.registerDiagnostics(mux)

	// The UI is served from the same binary and the same origin as the API.
	if err := registerUI(mux); err != nil {
		s.log.Error("could not mount the UI, serving the API only", "err", err)
	}
	return s.withSession(mux)
}

// --- services ---

// ServiceSummary is a service's RED summary.
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

	// Service-level rate and errors are computed from entry spans only (server
	// / consumer); otherwise every internal call would be counted twice.
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

// --- operations ---

// OperationSummary is the RED summary of a single operation within a service.
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
		writeError(w, http.StatusBadRequest, errors.New("the service parameter is required"))
		return
	}
	// Returning 403 for an out-of-scope service reveals that the service
	// exists. An empty result is both safe and sufficient.
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

// --- topology ---

// Node is a node in the topology graph.
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

// Edge is the call relationship between two nodes.
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

	// level=service   -> group by service name (default)
	// level=workload  -> group by k8s deployment/statefulset
	// level=namespace -> group by k8s namespace
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

// groupExprs returns the grouping expressions for a topology level.
// Endpoints with no Kubernetes counterpart — databases, queues — keep their own
// name at every level; otherwise they would drop out of the graph.
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

// --- trace search ---

// TraceSummary is one row in the trace list.
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
		// filterTraces returns " AND ..."; the prefix is stripped because it
		// is appended to a list here.
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
			writeError(w, http.StatusBadRequest, errors.New("minDurationMs must be a number"))
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

// --- trace detail ---

// SpanEvent is an event on a span, usually an exception.
type SpanEvent struct {
	Timestamp  time.Time         `json:"timestamp"`
	Name       string            `json:"name"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// CodeLocation is the source location a span came from.
type CodeLocation struct {
	Function   string `json:"function,omitempty"`
	File       string `json:"file,omitempty"`
	Line       int    `json:"line,omitempty"`
	Namespace  string `json:"namespace,omitempty"`
	StackTrace string `json:"stackTrace,omitempty"`
}

// SpanView is a single span in the trace detail.
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
	// SelfMS is the time spent in this span but not in its children. Where a
	// request is slow is answered by this number, not by the total.
	SelfMS     float64           `json:"selfMs"`
	Status     string            `json:"status"`
	StatusMsg  string            `json:"statusMessage,omitempty"`
	Code       *CodeLocation     `json:"code,omitempty"`
	Events     []SpanEvent       `json:"events,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// TimeSlice is one slice of the trace's duration breakdown.
type TimeSlice struct {
	Key   string  `json:"key"`
	MS    float64 `json:"ms"`
	Share float64 `json:"share"`
	Count int     `json:"count"`
}

// Hotspot is one of the most expensive operations in a trace by total self time.
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
		writeError(w, http.StatusBadRequest, errors.New("traceID must be 32 hex characters"))
		return
	}

	sc := scopeFor(r)
	if sc.empty() {
		writeErrorCode(w, http.StatusNotFound, "trace not found", "not_found")
		return
	}

	// Get the time range from the index first, so the spans table can be
	// partition-pruned. Without the index the query would scan every day.
	//
	// The same query also performs the access check: if none of the trace's
	// services is in scope, the trace counts as not found at all.
	filter, filterArgs := sc.filterTraces()
	var start, end time.Time
	idxQ := fmt.Sprintf(
		`SELECT min(start), max(end) FROM %s.trace_index WHERE trace_id = ?%s GROUP BY trace_id`,
		s.db, filter)
	if err := s.conn.QueryRow(r.Context(), idxQ,
		append([]any{traceID}, filterArgs...)...).Scan(&start, &end); err != nil {
		writeErrorCode(w, http.StatusNotFound, "trace not found", "not_found")
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

	// Self time is computed here rather than on the client: every client
	// re-implementing the same arithmetic is both waste and a source of
	// inconsistency.
	for i := range out {
		self := durations[out[i].SpanID]
		if children := childSum[out[i].SpanID]; children < self {
			self -= children
		} else {
			// Parallel children can add up to more than the parent; a negative
			// self time would be meaningless, so it is clamped to zero.
			self = 0
		}
		out[i].SelfMS = nsToMS(float64(self))
	}

	byCategory, byService := buildBreakdown(out)
	writeJSON(w, map[string]any{
		"traceId":  traceID,
		"spans":    out,
		"hotspots": buildHotspots(out),
		// Where the time went: own code, the database, or a call out to
		// another service.
		"breakdown": byCategory,
		"byService": byService,
	})
}

// codeLocationOf extracts the code location from a span's attributes.
// Both the current (code.function.name) and the legacy (code.function) names
// are read.
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

// buildEvents turns ClickHouse's parallel arrays into a list of events.
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

// categoryOf says which breakdown slice a span's time belongs to.
//
// It works on self time, which is why the slices add up to the trace's total
// duration. Computing on total duration would count nested spans more than
// once.
func categoryOf(s *SpanView) string {
	a := s.Attributes
	switch {
	case a["db.system"] != "" || a["db.system.name"] != "":
		return "database"
	case a["messaging.system"] != "":
		return "messaging"
	case s.Kind == "client" || s.Kind == "producer":
		// An outbound call: even when the target service is instrumented, the
		// time spent here is network and wait time.
		return "outbound"
	case s.Kind == "internal":
		return "code"
	default:
		return "code"
	}
}

// buildBreakdown splits the duration by category and by service.
func buildBreakdown(spans []SpanView) ([]TimeSlice, []TimeSlice) {
	cat := map[string]*TimeSlice{}
	svc := map[string]*TimeSlice{}
	var total float64

	for i := range spans {
		s := &spans[i]
		total += s.SelfMS

		key := categoryOf(s)
		if c, ok := cat[key]; ok {
			c.MS += s.SelfMS
			c.Count++
		} else {
			cat[key] = &TimeSlice{Key: key, MS: s.SelfMS, Count: 1}
		}

		if v, ok := svc[s.Service]; ok {
			v.MS += s.SelfMS
			v.Count++
		} else {
			svc[s.Service] = &TimeSlice{Key: s.Service, MS: s.SelfMS, Count: 1}
		}
	}

	flatten := func(m map[string]*TimeSlice) []TimeSlice {
		out := make([]TimeSlice, 0, len(m))
		for _, v := range m {
			if total > 0 {
				v.Share = v.MS / total
			}
			out = append(out, *v)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].MS > out[j].MS })
		return out
	}
	return flatten(cat), flatten(svc)
}

// buildHotspots summarises the most expensive operations by self time.
//
// A trace can hold hundreds of spans, and answering "which span is slowest"
// by eye means reading the waterfall row by row. Grouping spans that share a
// name also makes patterns like N+1 queries visible: eighty queries of 2 ms
// each show up as a single 160 ms row at the top.
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

// --- helpers ---

func bucketColumns() []string {
	cols := make([]string, 0, topology.BucketCount)
	for _, b := range topology.LatencyBounds {
		cols = append(cols, "le_"+strconv.FormatFloat(b, 'f', -1, 64)+"ms")
	}
	return append(cols, "le_inf")
}

// histogramQuantile estimates a quantile from bucket counts by linear
// interpolation. The result is in milliseconds.
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
			// The +Inf bucket: there is nothing to return but the lower bound.
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
			return time.Time{}, time.Time{}, errors.New("the to parameter must be RFC3339")
		}
		to = t.UTC()
	}

	from := to.Add(-time.Hour)
	if v := qp.Get("from"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			// A relative value such as "15m" or "2h": backwards from to.
			from = to.Add(-d)
		} else if t, err := time.Parse(time.RFC3339, v); err == nil {
			from = t.UTC()
		} else {
			return time.Time{}, time.Time{}, errors.New("the from parameter must be RFC3339 or a duration (15m, 2h)")
		}
	}
	if !from.Before(to) {
		return time.Time{}, time.Time{}, errors.New("from must be before to")
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

// writeJSONStatus writes JSON along with a status code.
func writeJSONStatus(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// decodeJSON decodes the request body. Unknown fields are an error: getting
// told immediately beats writing "isSuperadmin" and then hunting for why the
// permission was never granted.
func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("could not decode the request body: %w", err)
	}
	return nil
}

// writeErrorCode also emits a machine-readable code so the client can branch
// on it, rather than the UI having to decide based on the message text.
func writeErrorCode(w http.ResponseWriter, status int, msg, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg, "code": code})
}

// jsonUnmarshal decodes an external service's response.
func jsonUnmarshal(data []byte, out any) error {
	return json.Unmarshal(data, out)
}

func writeError(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
