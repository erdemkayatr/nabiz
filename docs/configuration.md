# Configuration reference

🇹🇷 [Türkçe](yapilandirma.md) · [Installation](installation.md) · [Usage](usage.md)

Every server setting is an environment variable prefixed with `NABIZ_`. There is
no configuration file for the server components — in Kubernetes the same image
goes to different environments, so what the deployment says has to win.

The .NET agent is the exception: it reads a `nabiz.json` generated at build time,
and environment variables override that file.

---

## nabiz-collector

Receives OTLP, writes to ClickHouse, derives topology.

### Storage

| Variable | Default | Description |
|---|---|---|
| `NABIZ_CLICKHOUSE_ADDRS` | `localhost:9000` | Comma-separated list of `host:port` |
| `NABIZ_CLICKHOUSE_DATABASE` | `nabiz` | Database name; created if missing |
| `NABIZ_CLICKHOUSE_USERNAME` | `default` | |
| `NABIZ_CLICKHOUSE_PASSWORD` | *(empty)* | |
| `NABIZ_CLICKHOUSE_MAX_CONNS` | `8` | Maximum open connections |
| `NABIZ_RETENTION_DAYS` | `7` | Raw span retention, applied as a ClickHouse TTL |

`NABIZ_RETENTION_DAYS` changes the table TTL on every startup, so lowering it
takes effect on the next ClickHouse merge — it is not an instant delete.

### Ingest pipeline

| Variable | Default | Description |
|---|---|---|
| `NABIZ_OTLP_GRPC_ADDR` | `:4317` | OTLP/gRPC listen address |
| `NABIZ_OTLP_HTTP_ADDR` | `:4318` | OTLP/HTTP listen address |
| `NABIZ_QUEUE_SIZE` | `200000` | Maximum spans held in the queue |
| `NABIZ_WORKERS` | `4` | Parallel batch writers |
| `NABIZ_BATCH_SIZE` | `5000` | Spans per ClickHouse insert |
| `NABIZ_FLUSH_INTERVAL` | `2s` | Flush a partial batch after this long |
| `NABIZ_DEBUG_ADDR` | `:8888` | Serves `/stats` and `/healthz` |
| `NABIZ_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

**The queue is bounded on purpose.** When it is full, spans are dropped and a
counter goes up; the receiver never blocks the sender. A slow ClickHouse must not
become a slow application. If `spans_dropped` in `/stats` is climbing, that is
the signal to raise `NABIZ_QUEUE_SIZE` and `NABIZ_WORKERS` or give ClickHouse
more resources — not to make the queue unbounded.

### Topology

| Variable | Default | Description |
|---|---|---|
| `NABIZ_TOPOLOGY_PAIR_TTL` | `30s` | How long a `CLIENT` span waits for its `SERVER` pair |
| `NABIZ_TOPOLOGY_FLUSH_INTERVAL` | `15s` | How often edges are written |
| `NABIZ_TOPOLOGY_MAX_PENDING_PER_SHARD` | `50000` | Unpaired-span ceiling per shard |
| `NABIZ_TOPOLOGY_INCLUDE_NODE` | `false` | Adds the Kubernetes node to the edge key |

`NABIZ_TOPOLOGY_PAIR_TTL` is the one worth tuning. A `CLIENT` span and its
`SERVER` span arrive from different processes, so they arrive at different times.
The TTL is how long the collector holds an unmatched `CLIENT` span waiting for
its partner. Too short and edges go missing across slow links; too long and the
pending map grows. The map is two-generation and sharded — it rotates every
`TTL/2` — so memory is bounded regardless.

Turning on `NABIZ_TOPOLOGY_INCLUDE_NODE` multiplies edge cardinality by the
number of nodes. Useful when chasing a node-specific problem, expensive as a
permanent setting.

---

## nabiz-api

Query endpoint plus the embedded UI.

| Variable | Default | Description |
|---|---|---|
| `NABIZ_API_ADDR` | `:8080` | Listen address |
| `NABIZ_CLICKHOUSE_*` | *(as above)* | Same storage settings as the collector |
| `NABIZ_POSTGRES_DSN` | `postgres://nabiz:nabiz@localhost:5432/nabiz?sslmode=disable` | Control plane |
| `NABIZ_ADMIN_EMAIL` | `admin@nabiz.local` | First administrator's email |
| `NABIZ_ADMIN_PASSWORD` | *(randomly generated)* | If unset, generated and logged **once** |
| `NABIZ_SECRET_KEY` | — | Encrypts diagnostics tokens; without it they are not stored |
| `NABIZ_DUMP_DIR` | *(temp dir)* | Where collected dumps are written |
| `NABIZ_DUMP_QUOTA_GB` | `10` | Total disk quota for dumps |
| `NABIZ_DUMP_RETENTION_DAYS` | `7` | Dumps older than this are deleted |
| `NABIZ_LOG_LEVEL` | `info` | |

### NABIZ_ADMIN_PASSWORD

If you do not set it, nabiz generates a random password at first startup and
writes it to the log once:

```
level=WARN msg="ilk yönetici oluşturuldu — bu parola bir daha gösterilmeyecek"
  email=admin@nabiz.local parola=<generated>
```

A management panel that opens with a default password is worse than none, which
is why there is no fallback default. The password is only settable at bootstrap;
afterwards it is changed from the UI.

### NABIZ_SECRET_KEY

Diagnostics tokens (the per-project secrets that let nabiz trigger dumps) are
stored AES-GCM encrypted under this key. Without it:

- tokens are not stored at all,
- the UI says why rather than failing silently,
- everything else keeps working.

Generate one with `openssl rand -base64 32`. Put it in a Secret, not in the
manifest. **If the key changes, previously stored tokens cannot be decrypted**
and must be re-entered.

### Dump retention

A janitor runs hourly, and once at startup. It applies two rules and then
collects orphans:

1. **Age.** Files older than `NABIZ_DUMP_RETENTION_DAYS` are deleted.
2. **Quota.** If the total exceeds `NABIZ_DUMP_QUOTA_GB`, the oldest are deleted
   until it fits.
3. **Orphans.** Files on disk with no database record are removed — but only if
   they have not been touched in the last 30 minutes, so a dump still being
   written is never deleted underneath itself.

Keep the quota **below** the volume's real size. The quota is a cleanup target,
not a write barrier: if the disk fills before the quota is reached, writes fail
(or in Kubernetes, the pod is evicted) rather than being rejected cleanly.

---

## nabiz-operator

The admission webhook that injects .NET instrumentation.

| Variable | Default | Description |
|---|---|---|
| `NABIZ_WEBHOOK_ADDR` | `:9443` | TLS listen address |
| `NABIZ_STATS_ADDR` | `:8888` | Serves `/stats` and `/healthz` |
| `NABIZ_NAMESPACE` | `nabiz` | Namespace the operator runs in |
| `NABIZ_WEBHOOK_SERVICE` | `nabiz-operator` | Service name used in the certificate |
| `NABIZ_WEBHOOK_CONFIG` | `nabiz-dotnet-injector` | `MutatingWebhookConfiguration` to patch |
| `NABIZ_COLLECTOR_ENDPOINT` | `http://nabiz-collector.<ns>.svc:4317` | What injected pods send to |
| `NABIZ_INSTRUMENTATION_IMAGE` | `nabiz/dotnet-instrumentation:1.16.0` | Init container image |
| `NABIZ_DEFAULT_SAMPLE_RATIO` | `1.0` | Sampling ratio when the pod does not say |
| `NABIZ_CLUSTER_NAME` | *(empty)* | Added as a resource attribute when set |

The operator generates a self-signed CA at startup and patches the webhook
configuration's `caBundle` itself, so cert-manager is not a dependency.

### Per-pod annotations

| Annotation | Default | Description |
|---|---|---|
| `nabiz.io/inject-dotnet` | — | `"true"` enables injection |
| `nabiz.io/service-name` | pod label | Sets `OTEL_SERVICE_NAME` |
| `nabiz.io/container` | first container | Target in a multi-container pod |
| `nabiz.io/sample-ratio` | `NABIZ_DEFAULT_SAMPLE_RATIO` | Agent-side sampling ratio |
| `nabiz.io/libc` | `glibc` | Use `musl` for Alpine-based images |

---

## The .NET agent (nabiz.json)

Generated in the project folder on the first build after adding `Nabiz.Agent`,
and never overwritten afterwards.

```json
{
  "endpoint": "http://localhost:4317",
  "protocol": "grpc",

  "serviceName": "",
  "serviceNamespace": "",
  "environment": "",

  "sampleRatio": 1.0,
  "enabled": true,

  "additionalSources": ["Npgsql"],
  "headers": {},

  "captureDbStatement": true,
  "captureCodeLocation": true,
  "debug": false
}
```

| Key | Default | Description |
|---|---|---|
| `endpoint` | `http://localhost:4317` | Collector address |
| `protocol` | `grpc` | `grpc` or `http/protobuf` |
| `serviceName` | *(assembly name)* | Appears as the service in nabiz |
| `serviceNamespace` | *(empty)* | Groups services, e.g. `shop` |
| `environment` | *(empty)* | `production`, `staging`, … |
| `sampleRatio` | `1.0` | `0.1` keeps one trace in ten |
| `enabled` | `true` | `false` disables the agent entirely |
| `additionalSources` | `["Npgsql"]` | Extra `ActivitySource` names to listen to |
| `headers` | `{}` | Headers added to OTLP requests (e.g. auth) |
| `captureDbStatement` | `true` | Records the SQL text on database spans |
| `captureCodeLocation` | `true` | Records file and line for code-level spans |
| `debug` | `false` | Logs agent activity to the console |

> `captureDbStatement` records the **statement**, not the parameter values. If
> your codebase inlines literals into SQL instead of parameterising, that text
> reaches nabiz — turn this off in that case.

Method **parameter values are never recorded**, only their types. Values can
carry personal data, passwords or tokens, so the agent does not send them at all.

### Environment variable overrides

Every key can be overridden with a `NABIZ_`-prefixed, upper-snake-case
environment variable:

| Variable | Overrides | Standard fallback |
|---|---|---|
| `NABIZ_ENDPOINT` | `endpoint` | `OTEL_EXPORTER_OTLP_ENDPOINT` |
| `NABIZ_PROTOCOL` | `protocol` | `OTEL_EXPORTER_OTLP_PROTOCOL` |
| `NABIZ_SERVICE_NAME` | `serviceName` | `OTEL_SERVICE_NAME` |
| `NABIZ_SERVICE_NAMESPACE` | `serviceNamespace` | — |
| `NABIZ_ENVIRONMENT` | `environment` | — |
| `NABIZ_SAMPLE_RATIO` | `sampleRatio` | `OTEL_TRACES_SAMPLER_ARG` |
| `NABIZ_ENABLED` | `enabled` | — |
| `NABIZ_DEBUG` | `debug` | — |
| `NABIZ_API_URL` | `apiUrl` | — |
| `NABIZ_CAPTURE_DB_STATEMENT` | `captureDbStatement` | — |
| `NABIZ_CAPTURE_CODE_LOCATION` | `captureCodeLocation` | — |
| `NABIZ_CONFIG` | *(path to the config file itself)* | — |

The standard `OTEL_*` variables are honoured as a fallback, so an application
already configured for OpenTelemetry keeps working. `NABIZ_*` wins when both are
set.

### Diagnostics block

Only read when `Nabiz.Agent.Diagnostics` is referenced.

```json
"apiUrl": "http://nabiz-api:8080",
"diagnostics": {
  "enabled": false,
  "path": "/nabiz/diag",
  "token": "",
  "outputDirectory": "",
  "maxFiles": 5,
  "maxCpuSeconds": 120,
  "allowDownload": false,
  "advertisedHost": ""
}
```

| Key | Default | Description |
|---|---|---|
| `apiUrl` | *(empty)* | If set, the agent registers itself with nabiz |
| `enabled` | `false` | Off by default |
| `path` | `/nabiz/diag` | Route prefix for the diagnostics endpoints |
| `token` | *(empty)* | **At least 16 characters**, or the endpoints never open |
| `outputDirectory` | *(temp dir)* | Where dumps are written in the application |
| `maxFiles` | `5` | Older files are trimmed beyond this |
| `maxCpuSeconds` | `120` | Upper bound on a profile's duration |
| `allowDownload` | `false` | Required for nabiz to fetch the file over HTTP |
| `advertisedHost` | *(empty)* | Address to report when the source IP is not routable |

`advertisedHost` is honoured **only for token-verified registrations**. An
unverified registration cannot talk nabiz into sending a token to an
attacker-chosen address.

See [usage.md](usage.md#on-demand-dumps) for what these actually expose.

---

## Query parameters

The read endpoints take a time range and an optional project scope:

| Parameter | Example | Description |
|---|---|---|
| `from` | `15m`, `1h`, `24h` | Relative range |
| `from` / `to` | RFC3339 timestamps | Absolute range |
| `project` | `cart` | Narrows every query to one project |
| `level` | `service`, `workload`, `namespace` | Topology aggregation level |

```bash
curl "localhost:8080/api/v1/topology?from=1h&level=workload"
curl "localhost:8080/api/v1/traces?service=cart-service&minDurationMs=500&onlyErrors=true"
```
