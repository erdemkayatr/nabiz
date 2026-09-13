package clickhouse

// Schema holds the DDL statements run idempotently at startup.
//
// Design notes:
//   - spans is ordered by (service_name, name, timestamp); almost every APM
//     query starts with "this operation in that service".
//   - pulling one trace by trace_id does not fit that ordering, so there is a
//     separate bloom_filter skip index; it turns a full scan into granule
//     skipping.
//   - service_edges is the topology itself and arrives pre-aggregated; the
//     SummingMergeTree only has to add up parts landing in the same minute.
//   - RED metrics are derived from spans by a materialized view: no extra work
//     on the write path, no full scan on the query path.
var Schema = []string{
	`CREATE TABLE IF NOT EXISTS spans (
		timestamp            DateTime64(9, 'UTC') CODEC(Delta(8), ZSTD(1)),
		trace_id             String CODEC(ZSTD(1)),
		span_id              String CODEC(ZSTD(1)),
		parent_span_id       String CODEC(ZSTD(1)),
		trace_state          String CODEC(ZSTD(1)),
		flags                UInt32 CODEC(ZSTD(1)),

		name                 LowCardinality(String) CODEC(ZSTD(1)),
		kind                 Enum8('unspecified'=0,'internal'=1,'server'=2,'client'=3,'producer'=4,'consumer'=5),
		duration_ns          UInt64 CODEC(T64, ZSTD(1)),
		status_code          Enum8('unset'=0,'ok'=1,'error'=2),
		status_message       String CODEC(ZSTD(1)),

		service_name         LowCardinality(String) CODEC(ZSTD(1)),
		service_namespace    LowCardinality(String) CODEC(ZSTD(1)),
		service_version      LowCardinality(String) CODEC(ZSTD(1)),
		service_instance     String CODEC(ZSTD(1)),
		sdk_language         LowCardinality(String) CODEC(ZSTD(1)),

		k8s_cluster          LowCardinality(String) CODEC(ZSTD(1)),
		k8s_namespace        LowCardinality(String) CODEC(ZSTD(1)),
		k8s_pod              String CODEC(ZSTD(1)),
		k8s_workload         LowCardinality(String) CODEC(ZSTD(1)),
		k8s_node             LowCardinality(String) CODEC(ZSTD(1)),
		k8s_container        LowCardinality(String) CODEC(ZSTD(1)),

		http_method          LowCardinality(String) CODEC(ZSTD(1)),
		http_route           LowCardinality(String) CODEC(ZSTD(1)),
		http_status_code     UInt16 CODEC(ZSTD(1)),
		http_url             String CODEC(ZSTD(1)),
		db_system            LowCardinality(String) CODEC(ZSTD(1)),
		db_name              LowCardinality(String) CODEC(ZSTD(1)),
		db_statement         String CODEC(ZSTD(1)),
		rpc_system           LowCardinality(String) CODEC(ZSTD(1)),
		rpc_service          LowCardinality(String) CODEC(ZSTD(1)),
		rpc_method           LowCardinality(String) CODEC(ZSTD(1)),
		messaging_system     LowCardinality(String) CODEC(ZSTD(1)),
		messaging_dest       LowCardinality(String) CODEC(ZSTD(1)),
		peer_service         LowCardinality(String) CODEC(ZSTD(1)),
		peer_address         String CODEC(ZSTD(1)),
		peer_port            UInt16 CODEC(ZSTD(1)),

		resource_attributes  Map(LowCardinality(String), String) CODEC(ZSTD(1)),
		span_attributes      Map(LowCardinality(String), String) CODEC(ZSTD(1)),

		events_timestamp     Array(DateTime64(9, 'UTC')) CODEC(ZSTD(1)),
		events_name          Array(LowCardinality(String)) CODEC(ZSTD(1)),
		events_attributes    Array(Map(LowCardinality(String), String)) CODEC(ZSTD(1)),

		links_trace_id       Array(String) CODEC(ZSTD(1)),
		links_span_id        Array(String) CODEC(ZSTD(1)),

		INDEX idx_trace_id   trace_id TYPE bloom_filter(0.01) GRANULARITY 1,
		INDEX idx_duration   duration_ns TYPE minmax GRANULARITY 1,
		INDEX idx_http_status http_status_code TYPE set(64) GRANULARITY 1,
		INDEX idx_k8s_pod    k8s_pod TYPE bloom_filter(0.01) GRANULARITY 4,
		INDEX idx_span_attr_keys mapKeys(span_attributes) TYPE bloom_filter(0.01) GRANULARITY 1
	) ENGINE = MergeTree
	PARTITION BY toDate(timestamp)
	ORDER BY (service_name, name, toUnixTimestamp(timestamp))
	TTL toDateTime(timestamp) + INTERVAL {{TTL_DAYS}} DAY DELETE
	SETTINGS index_granularity = 8192, ttl_only_drop_parts = 1`,

	`CREATE TABLE IF NOT EXISTS service_edges (
		bucket               DateTime('UTC') CODEC(Delta(4), ZSTD(1)),
		client               LowCardinality(String),
		server               LowCardinality(String),
		client_namespace     LowCardinality(String),
		client_workload      LowCardinality(String),
		server_namespace     LowCardinality(String),
		server_workload      LowCardinality(String),
		client_node          LowCardinality(String),
		server_node          LowCardinality(String),
		conn_type            Enum8('service'=0,'database'=1,'messaging'=2,'external'=3,'entry'=4),

		calls                SimpleAggregateFunction(sum, UInt64),
		errors               SimpleAggregateFunction(sum, UInt64),
		duration_sum_ns      SimpleAggregateFunction(sum, UInt64),
		duration_max_ns      SimpleAggregateFunction(max, UInt64),

		le_1ms               SimpleAggregateFunction(sum, UInt64),
		le_2ms               SimpleAggregateFunction(sum, UInt64),
		le_5ms               SimpleAggregateFunction(sum, UInt64),
		le_10ms              SimpleAggregateFunction(sum, UInt64),
		le_25ms              SimpleAggregateFunction(sum, UInt64),
		le_50ms              SimpleAggregateFunction(sum, UInt64),
		le_100ms             SimpleAggregateFunction(sum, UInt64),
		le_250ms             SimpleAggregateFunction(sum, UInt64),
		le_500ms             SimpleAggregateFunction(sum, UInt64),
		le_1000ms            SimpleAggregateFunction(sum, UInt64),
		le_2500ms            SimpleAggregateFunction(sum, UInt64),
		le_5000ms            SimpleAggregateFunction(sum, UInt64),
		le_10000ms           SimpleAggregateFunction(sum, UInt64),
		le_inf               SimpleAggregateFunction(sum, UInt64)
	) ENGINE = AggregatingMergeTree
	PARTITION BY toDate(bucket)
	ORDER BY (bucket, conn_type, client, server, client_namespace, client_workload, server_namespace, server_workload, client_node, server_node)
	TTL bucket + INTERVAL {{TTL_DAYS}} DAY DELETE
	SETTINGS ttl_only_drop_parts = 1`,

	`CREATE TABLE IF NOT EXISTS operation_stats (
		bucket               DateTime('UTC') CODEC(Delta(4), ZSTD(1)),
		service_name         LowCardinality(String),
		operation            LowCardinality(String),
		kind                 Enum8('unspecified'=0,'internal'=1,'server'=2,'client'=3,'producer'=4,'consumer'=5),
		k8s_namespace        LowCardinality(String),
		k8s_workload         LowCardinality(String),

		calls                SimpleAggregateFunction(sum, UInt64),
		errors               SimpleAggregateFunction(sum, UInt64),
		duration_sum_ns      SimpleAggregateFunction(sum, UInt64),
		duration_max_ns      SimpleAggregateFunction(max, UInt64),
		duration_quantiles   AggregateFunction(quantiles(0.5, 0.9, 0.95, 0.99), UInt64)
	) ENGINE = AggregatingMergeTree
	PARTITION BY toDate(bucket)
	ORDER BY (service_name, operation, kind, k8s_namespace, k8s_workload, bucket)
	TTL bucket + INTERVAL {{TTL_DAYS}} DAY DELETE
	SETTINGS ttl_only_drop_parts = 1`,

	// Derive the RED metrics at write time: read a per-minute summary instead of
	// scanning billions of spans at query time.
	`CREATE MATERIALIZED VIEW IF NOT EXISTS operation_stats_mv TO operation_stats AS
	SELECT
		toStartOfMinute(timestamp)                              AS bucket,
		service_name,
		name                                                    AS operation,
		kind,
		k8s_namespace,
		k8s_workload,
		count()                                                 AS calls,
		countIf(status_code = 'error' OR http_status_code >= 500) AS errors,
		sum(duration_ns)                                        AS duration_sum_ns,
		max(duration_ns)                                        AS duration_max_ns,
		quantilesState(0.5, 0.9, 0.95, 0.99)(duration_ns)       AS duration_quantiles
	FROM spans
	GROUP BY bucket, service_name, operation, kind, k8s_namespace, k8s_workload`,

	// trace_id -> time range index. It narrows which partition of the spans
	// table to look at when pulling a single trace.
	`CREATE TABLE IF NOT EXISTS trace_index (
		trace_id     String,
		start        SimpleAggregateFunction(min, DateTime64(9, 'UTC')),
		end          SimpleAggregateFunction(max, DateTime64(9, 'UTC')),
		    -- max, NOT any: when an AggregatingMergeTree merges, any can pick the
		    -- empty value from a part that does not contain the root span. max picks
		-- korur.
		root_service SimpleAggregateFunction(max, LowCardinality(String)),
		root_name    SimpleAggregateFunction(max, LowCardinality(String)),
		duration_ns  SimpleAggregateFunction(max, UInt64),
		spans        SimpleAggregateFunction(sum, UInt64),
		errors       SimpleAggregateFunction(sum, UInt64),
		services     SimpleAggregateFunction(groupUniqArrayArray, Array(String))
	) ENGINE = AggregatingMergeTree
	PARTITION BY toDate(start)
	ORDER BY (trace_id)
	TTL toDateTime(start) + INTERVAL {{TTL_DAYS}} DAY DELETE
	SETTINGS ttl_only_drop_parts = 1`,

	`CREATE MATERIALIZED VIEW IF NOT EXISTS trace_index_mv TO trace_index AS
	SELECT
		trace_id,
		min(timestamp)                                            AS start,
		max(timestamp)                                            AS end,
		anyIf(service_name, parent_span_id = '')                  AS root_service,
		anyIf(name, parent_span_id = '')                          AS root_name,
		maxIf(duration_ns, parent_span_id = '')                   AS duration_ns,
		count()                                                   AS spans,
		countIf(status_code = 'error' OR http_status_code >= 500)  AS errors,
		groupUniqArray(toString(service_name))                    AS services
	FROM spans
	GROUP BY trace_id`,
}

// InsertSpansSQL is the statement prepared for the batch insert. The column
// order has to match the Append call in store.go exactly.
const InsertSpansSQL = `INSERT INTO spans (
	timestamp, trace_id, span_id, parent_span_id, trace_state, flags,
	name, kind, duration_ns, status_code, status_message,
	service_name, service_namespace, service_version, service_instance, sdk_language,
	k8s_cluster, k8s_namespace, k8s_pod, k8s_workload, k8s_node, k8s_container,
	http_method, http_route, http_status_code, http_url,
	db_system, db_name, db_statement,
	rpc_system, rpc_service, rpc_method,
	messaging_system, messaging_dest,
	peer_service, peer_address, peer_port,
	resource_attributes, span_attributes,
	events_timestamp, events_name, events_attributes,
	links_trace_id, links_span_id
)`

// InsertEdgesSQL is the batch insert for topology edges.
const InsertEdgesSQL = `INSERT INTO service_edges (
	bucket, client, server,
	client_namespace, client_workload, server_namespace, server_workload,
	client_node, server_node, conn_type,
	calls, errors, duration_sum_ns, duration_max_ns,
	le_1ms, le_2ms, le_5ms, le_10ms, le_25ms, le_50ms, le_100ms, le_250ms, le_500ms, le_1000ms, le_2500ms, le_5000ms, le_10000ms, le_inf
)`

// EdgeBucketColumns is the order of the latency histogram columns. It has to
// match LatencyBounds in the topology package exactly.
var EdgeBucketColumns = []string{"le_1ms", "le_2ms", "le_5ms", "le_10ms", "le_25ms", "le_50ms", "le_100ms", "le_250ms", "le_500ms", "le_1000ms", "le_2500ms", "le_5000ms", "le_10000ms", "le_inf"}
