# Nabiz.Agent

The nabiz APM agent. Add the package, build, put in your server address. No
change to application code is required.

🇹🇷 [Türkçe](README.tr.md)

```bash
dotnet add package Nabiz.Agent
dotnet build
```

After the build a `nabiz.json` appears in the project folder:

```json
{
  "endpoint": "http://localhost:4317",
  "protocol": "grpc",
  "serviceName": "",
  "sampleRatio": 1.0,
  "enabled": true
}
```

Put your nabiz collector address in `endpoint`. The file is never overwritten;
later builds keep your change.

## Configuration

| Field | Default | Description |
|---|---|---|
| `endpoint` | `http://localhost:4317` | The nabiz collector address |
| `protocol` | `grpc` | `grpc` (4317) or `http` (4318) |
| `serviceName` | *(assembly name)* | The name shown in the topology |
| `serviceNamespace` | — | Prevents name clashes (`shop/api`) |
| `environment` | — | `prod`, `staging`, `dev` |
| `sampleRatio` | `1.0` | Sampling between 0.0 and 1.0 |
| `enabled` | `true` | When `false`, the agent never starts |
| `additionalSources` | *(common libraries)* | Extra ActivitySource names |
| `codeLevel.enabled` | `true` | Automatic method measurement |
| `codeLevel.includeNamespaces` | *(entry assembly root)* | Namespaces to measure |
| `codeLevel.excludeNamespaces` | `[]` | Namespaces to exclude |
| `codeLevel.captureAllocations` | `true` | Memory allocated per span |
| `codeLevel.captureThread` | `true` | Thread id and async switch |
| `codeLevel.captureParameterTypes` | `true` | Parameter types (never values) |
| `headers` | `{}` | OTLP headers (ingress authentication) |
| `captureDbStatement` | `true` | When `false`, the SQL text is stripped from the span |
| `captureCodeLocation` | `true` | Adds file:line to failing spans |
| `debug` | `false` | The agent's own diagnostic output |

Every field can be overridden with an environment variable: `NABIZ_ENDPOINT`,
`NABIZ_SERVICE_NAME`, `NABIZ_SAMPLE_RATIO`, `NABIZ_ENABLED` …

The precedence is **environment variable > nabiz.json > default**. In Kubernetes
the same image goes to different environments, so editing the file inside the
image is not an option; what the deployment says wins.

The standard `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_SERVICE_NAME` and
`OTEL_RESOURCE_ATTRIBUTES` are read as well — pods injected by the nabiz
operator need no extra configuration.

## MSBuild settings

```xml
<PropertyGroup>
  <NabizConfigFile>nabiz.json</NabizConfigFile>
  <NabizGenerateConfig>true</NabizGenerateConfig>
  <NabizAutoStart>true</NabizAutoStart>
</PropertyGroup>
```

With `NabizAutoStart` off, you start the agent yourself:

```csharp
Nabiz.Agent.NabizAgent.Start();
```

> The config file and the automatic startup hook are only generated for
> executable projects (`OutputType` of `Exe` or `WinExe`). A class library that
> references the package gets neither.

## Automatic method-level measurement

Automatic instrumentation sees requests, HTTP calls and database queries — not
your own code in between. You can put your services under measurement with one
line:

```csharp
builder.Services.AddScoped<ICartService, CartService>();
builder.Services.AddScoped<IPricingService, PricingService>();

builder.Services.AddNabizCodeLevel();   // AFTER the registrations
```

From that point on, **every method** of those services gets its own span with no
change to their code: the method name, its duration, its self time, allocated
memory, thread id and async thread switch.

Only the **types** of parameters are recorded. Values are never sent: a
parameter can carry personal data, a password or a token.

### Why one line is needed

Tools like Dynatrace rewrite IL at runtime through the CLR Profiler API and see
every method with no markup at all. This package does not touch IL: emitting
broken IL means an observability tool that crashes the application it observes.

Removing even this line with `IHostingStartup` was tried and does not work:
hosting startup runs **before** the application's own service registrations and
cannot yet see the services to wrap.

### Silencing the noise

Once every service is measured, some methods produce nothing but noise: tiny
checks called in tight loops, accessors that run hundreds of times per request.
Mark those:

```csharp
[NabizIgnore]
public bool IsValidQuantity(int quantity) => quantity > 0 && quantity < 1000;

[NabizTrace(Name = "read unit price")]
public decimal UnitPrice(string productCode) { ... }
```

`[NabizIgnore]` can go on a method or a type. `[NabizTrace]` gives the span a
readable name. When both are present the exclusion wins: a decision to silence
always beats a decision to measure.

### Limits

- Only services registered **through an interface** are wrapped. Services
  registered as concrete classes would need their methods to be `virtual`, and
  that measures incompletely without saying so. Class registrations in scope are
  skipped and listed at startup — "why can't I see this service" has to be
  answerable from the log.
- **It cannot see your own code inside a method.** Wrapping happens at the
  service boundary; a private helper you call inside a method is invisible. That
  needs build-time IL weaving (see below).
- For methods returning `ValueTask`, only the synchronous part is measured.
  Calling `AsTask()` would take the result out of the caller's hands; the span
  marks this case explicitly.
- By default only the entry assembly's root namespace is scanned. For a
  different scope: `AddNabizCodeLevel(o => o.IncludeNamespaces.Add("Shop."))`

## Roadmap: IL weaving

Seeing private calls inside a method requires touching IL at build time. The
design is settled, the code is not written yet:

- The scope is chosen in `Program.cs`: **the whole application assembly**, or
  **only what is marked `[NabizTrace]`**.
- In whole-assembly mode you exclude methods you do not want with
  `[NabizIgnore]` — that attribute works today and will carry the same meaning
  when weaving arrives.
- It will be developed on a separate branch behind an experimental flag; the
  default will not change until it is mature.

## What is collected

ASP.NET Core requests, `HttpClient` calls, SQL Server queries, and the common
libraries that publish their own `ActivitySource`: PostgreSQL (Npgsql), MySQL,
Kafka, RabbitMQ, MassTransit, Elasticsearch, MongoDB, Quartz, YARP, the Azure
SDK. Listening to these sources is free — if the library is absent, the source
never emits. Trace context travels between services in the `traceparent` header,
and nabiz derives the service topology from those relationships.

## Effect on the application

- The sampling decision is made here: a dropped span is never serialized and
  never reaches the network.
- Export is batched and in the background; there is no network call on the
  request path.
- If the queue fills, spans are dropped. Losing telemetry is preferred over
  slowing the application down.
- If the agent fails to start it throws nothing, and the application carries on
  normally.

## License

[Apache License 2.0](https://github.com/erdemkayatr/nabiz/blob/main/LICENSE)
