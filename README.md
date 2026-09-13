# nabiz

**Kubernetes-native APM that derives the service map from the requests themselves — not from a separate discovery mechanism.**

[![CI](https://github.com/erdemkayatr/nabiz/actions/workflows/ci.yml/badge.svg)](https://github.com/erdemkayatr/nabiz/actions/workflows/ci.yml)
[![NuGet](https://img.shields.io/nuget/v/Nabiz.Agent.svg)](https://www.nuget.org/packages/Nabiz.Agent/)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

🇹🇷 [Türkçe belgeler](README.tr.md)

The first release covers **.NET** applications on **Docker** and **Kubernetes**.

---

## Why another APM

In most APMs the service map is the product of separate work: you either define
it by hand, or a sidecar sniffs network traffic, or you depend on a service
mesh's telemetry. All three mean setup cost and drift from reality.

In nabiz the service map is a by-product of the traces it already collects. The
parent of a `SERVER` span is a `CLIENT` span produced in the calling service;
pair the two on `span_id` and you have both ends of the edge. No extra data is
collected, no extra component is installed, and the map always shows real
traffic.

The first run, with nothing configured:

```
user                 --  entry   --> shop/sample-frontend   calls=569  err=3.7%  avg=6.8ms
shop/sample-frontend -- service  --> shop/sample-backend    calls=569  err=9.0%  avg=6.0ms
shop/sample-backend  -- database --> postgresql:orders      calls=292  err=0.0%  avg=8.9ms
```

Note `postgresql:orders`: nothing is installed in Postgres. That edge was
derived from the query spans the backend emitted.

## Quick start

```bash
git clone https://github.com/erdemkayatr/nabiz.git
cd nabiz
make up
```

About a minute later, open **http://localhost:8080** and sign in with
`admin@nabiz.local` / `nabiz1234`.

> That fixed login exists only in the local development compose file. In any
> other deployment, if `NABIZ_ADMIN_PASSWORD` is not supplied a random password
> is generated and written to the log **once**. A management panel that opens
> with a default password is worse than none.

Full instructions, including Kubernetes and building from source:
**[docs/installation.md](docs/installation.md)**.

## What you get

| Screen | What it shows |
|---|---|
| **Topology** | A live service graph derived from requests. Circular nodes, dots flowing along edges at call rate. Drag nodes, zoom the canvas, click a node or edge for a metrics panel. Switch between service / Kubernetes workload / Kubernetes namespace levels. |
| **Services** | Rate, error rate and p50/p95/p99 per service. |
| **Traces** | Search by service, duration and error; click a row for the waterfall — including the SQL statement and the HTTP target. |
| **Diagnostics** | Running instances, one-click CPU profile or memory dump, with a progress dialog that can stop a running CPU profile. Collected files are listed and downloadable. Requires the `diagnostics.manage` permission. |
| **Administration** | Users, role groups and projects. The project editor lists services that are actually sending telemetry, so you pick an application from a list instead of typing its name and silently getting no data. |

The UI ships **in English and Turkish**. Language follows the browser, can be
switched from the top bar, and the choice is remembered. Number and time formats
follow the language too (`6.292` / `6,292`, `20:27` / `8:27 PM`).

The UI is embedded in the `nabiz-api` binary: no separate static server, no
ConfigMap, no "API address" setting. It has no dependencies either — including
the force-directed layout, everything is hand-written, so it works in a cluster
with no CDN access.

## Performance posture

"It must not affect the monitored application's performance" is met in three
places:

| Where | What is done |
|---|---|
| In the application | Instrumentation is attached at runtime through the CLR Profiler API. No trace of it in your code, your NuGet graph or your build output. |
| Leaving the application | The sampling decision is made in the agent — a dropped span is never serialized and never reaches the network. Export is batched and off the request path. |
| Collector | The receiver never blocks the sender. If the queue fills, spans are dropped and a counter goes up; a slow ClickHouse cannot cascade into a slow application. |

The topology hot path, measured on an Apple M4 Pro:

```
BenchmarkObserve-14    38510395    93.42 ns/op    0 B/op    0 allocs/op
```

93 ns per span and zero allocations — a ceiling of roughly 10M spans/second on a
single core. In practice the bottleneck is ClickHouse write throughput, not the
topology calculation.

## Architecture

```
.NET application                   nabiz-collector              ClickHouse
┌────────────────┐                ┌──────────────────┐        ┌──────────────┐
│ your code      │                │ OTLP receiver    │        │ spans        │
│   (untouched)  │   OTLP/gRPC    │  (gRPC + HTTP)   │        │ service_edges│
│ ┌────────────┐ │ ─────────────► │        ↓         │ ─────► │ operation_.. │
│ │CLR Profiler│ │                │ bounded queue    │        │ trace_index  │
│ └────────────┘ │                │        ↓         │        └──────────────┘
└────────────────┘                │ ┌─────┬────────┐ │               ↑
        ▲                         │ │store│topology│ │               │
        │ injection               │ └─────┴────────┘ │          nabiz-api
┌───────┴────────┐                └──────────────────┘       (query endpoint)
│ nabiz-operator │
│ (admission wh) │
└────────────────┘
```

| Component | Language | Job |
|---|---|---|
| `nabiz-collector` | Go | Receives OTLP, writes to ClickHouse, derives topology |
| `nabiz-api` | Go | Query endpoint + UI (single binary) |
| `nabiz-operator` | Go | Webhook that injects .NET instrumentation into pods |
| Storage | ClickHouse | Telemetry; roughly 10x compression on spans |
| Control plane | PostgreSQL | Users, role groups, projects, sessions |
| Agent (k8s) | OpenTelemetry .NET auto-instrumentation | Injected by the operator, no code change |
| Agent (NuGet) | [`Nabiz.Agent`](https://www.nuget.org/packages/Nabiz.Agent/) | Referencing the package is enough; config is generated at build time |
| Dumps (NuGet) | [`Nabiz.Agent.Diagnostics`](https://www.nuget.org/packages/Nabiz.Agent.Diagnostics/) | On-demand CPU profile and memory dump |

Go was chosen because it is the native language of the Kubernetes ecosystem
(client-go, admission webhooks) and of OTLP. Rust would give a higher ceiling,
but the bottleneck is not in this layer — it is in storage.

## The .NET agent

Outside Kubernetes — a developer machine, a VM, a Windows service — there is no
operator. Use the agent package instead:

```bash
dotnet add package Nabiz.Agent
dotnet build
```

After the build a `nabiz.json` appears in the project folder; put your collector
address in `endpoint`. The file is never overwritten afterwards.

```json
{ "endpoint": "http://nabiz-collector:4317", "serviceName": "cart-service" }
```

Not a line of application code changes: at build time the package injects a
`[ModuleInitializer]` into the project, and the agent starts itself when the
application starts.

Environment variables win over the file (`NABIZ_ENDPOINT`, `NABIZ_SERVICE_NAME`,
…). In Kubernetes the same image goes to different environments, so editing the
file inside the image is not an option; what the deployment says wins.

## Code-level timing

Automatic instrumentation sees requests, HTTP calls and database queries — it
does not see your own code in between. Knowing that a request took 200 ms does
not tell you where those 200 ms went.

**Automatic:** put your DI-registered services under measurement with one line,
and every method on them gets its own span without their code being touched.

```csharp
builder.Services.AddScoped<ICartService, CartService>();
builder.Services.AddNabizCodeLevel();   // AFTER the registrations
```

**Selective:** mark the block you want to measure; file and line come free from
the compiler.

```csharp
var price = NabizTracer.Measure("calculate price", () => Calculate(cart));
await NabizTracer.MeasureAsync("reserve stock", () => Reserve(cart));
```

The trace detail then shows self time, a breakdown of where the time went (own
code / database / outbound service / queue), hotspots grouped by span name, and
exceptions with type, message and stack trace.

```
GET /calculate      total  68.12 ms   self   0.33 ms
  validate cart     total  13.02 ms   self  13.02 ms   Program.cs:20
  calculate price   total  45.76 ms   self   0.02 ms   Program.cs:26
    apply campaign  total  45.74 ms   self  45.74 ms   Program.cs:28
  reserve stock     total   9.02 ms   self   9.02 ms   Program.cs:34
```

Details: **[docs/usage.md](docs/usage.md#code-level-timing)**.

## On-demand dumps

From the **Diagnostics** screen you see running instances and take a CPU profile
or memory dump with one click. The application side needs a separate package:

```bash
dotnet add package Nabiz.Agent.Diagnostics
```

```csharp
builder.Services.AddNabizDiagnostics();
```

> **A memory dump writes the entire memory of the process to disk** — connection
> strings, session tokens, passwords, customer data. It is **off by default** on
> the application side and does not turn on without a **token of at least 16
> characters**. In this architecture nabiz stores every application's token and
> proxies the files: whoever takes over nabiz can take the process memory of
> every application it monitors.

Read [docs/usage.md](docs/usage.md#on-demand-dumps) before enabling this in
production.

## Access model

Identity and authorization read as a single chain:

```
user ──member of──> role group ──bound to──> project ──contains──> application
```

A user can see an application's telemetry only if a role group they belong to is
bound to that application's project. Permissions are never granted directly to a
user, so "why can this person see this data?" is always answerable by reading
the chain.

Filtering happens server-side, at query level. Hiding a tab in the UI is not a
security measure: a user who types `#admin/users` in the address bar and a
script that calls the API directly both get 403.

## Documentation

| | English | Türkçe |
|---|---|---|
| Installation | [docs/installation.md](docs/installation.md) | [docs/kurulum.md](docs/kurulum.md) |
| Usage | [docs/usage.md](docs/usage.md) | [docs/kullanim.md](docs/kullanim.md) |
| Configuration reference | [docs/configuration.md](docs/configuration.md) | [docs/yapilandirma.md](docs/yapilandirma.md) |
| Topology internals | [docs/topology.md](docs/topology.md) | [docs/topoloji.md](docs/topoloji.md) |
| Contributing | [CONTRIBUTING.md](CONTRIBUTING.md) | — |
| Security policy | [SECURITY.md](SECURITY.md) | — |

## Out of scope (for now)

- **Metrics and logs.** Only traces are collected. RED metrics are derived from
  spans; there is no separate metric pipeline.
- **Tail-based sampling.** Sampling happens in the agent, decided per trace up
  front. "Collect everything, keep the slow and failed ones" does not exist yet.
- **Continuous CPU profiling.** The dump package gives on-demand profiles, but
  there is no background profiler correlated with traces.
- **Object storage for dumps.** Files live on the nabiz-api disk under age and
  size limits; moving them to S3 or equivalent is not implemented.
- **Code inside a method.** Wrapping happens at the service boundary: a private
  helper you call inside a method is invisible. That needs build-time IL
  weaving — designed, but deferred to a separate branch behind an experimental
  flag.
- **Services registered as concrete classes.** Registrations without an
  interface cannot be proxied; the skipped ones are listed at startup.
- **CPU time vs wait time.** Spans measure wall-clock; there is no "was it on
  CPU or blocked on a lock" distinction.
- **SSO / LDAP.** Identity is email + password only.
- **Audit log.** Administrative actions go to the server log but are not kept in
  a queryable table.
- **Partially hiding a trace.** If one of a trace's services is in scope, all of
  its spans are shown. Hiding half of a distributed trace would make it
  unreadable, and this matches common APM behaviour.
- **Languages other than .NET.** The collector already speaks OTLP, so any
  OpenTelemetry SDK can send data today; only automatic injection is .NET-only.

## License

[Apache License 2.0](LICENSE).
