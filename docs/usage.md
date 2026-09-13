# Usage

🇹🇷 [Türkçe](kullanim.md) · [Installation](installation.md) · [Configuration](configuration.md)

- [The five screens](#the-five-screens)
- [Topology](#topology)
- [Services](#services)
- [Traces](#traces)
- [Code-level timing](#code-level-timing)
- [On-demand dumps](#on-demand-dumps)
- [Administration and access control](#administration-and-access-control)
- [The HTTP API](#the-http-api)
- [Troubleshooting](#troubleshooting)

---

## The five screens

| Screen | Permission | What it is for |
|---|---|---|
| Topology | `topology.read` | Which service talks to which, and how healthily |
| Services | `services.read` | Rate, errors and latency per service |
| Traces | `traces.read` | One request end to end |
| Diagnostics | `diagnostics.manage` | CPU profiles and memory dumps |
| Administration | `admin.manage` | Users, role groups, projects |

The language selector in the top bar switches between Turkish and English. The
initial choice follows the browser, and your choice is remembered. Number and
time formats follow the language too.

The time range selector (15 minutes / 1 hour / 6 hours / 24 hours) applies to
every screen. Topology refreshes every 10 seconds.

---

## Topology

The graph is derived from the traces themselves. Nothing is configured, and
nothing is discovered by a separate mechanism — when a service starts calling a
new dependency, the edge appears on the next refresh.

**Reading it:**

- **Node size** is traffic volume.
- **Node colour** is type: service, database, message queue, external dependency.
- **Red** marks an error rate above 1%.
- **Dots flowing along an edge** move at the call rate — a busy edge visibly
  streams, an idle one does not.

**Interacting:**

- **Drag** a node to pin it where you want it.
- **Scroll or use the zoom bar** to zoom; "fit" frames everything.
- **Click a node or an edge** to open the metrics panel. Unrelated parts of the
  graph fade so you can follow one path.
- **Level selector** switches between service, Kubernetes workload and
  Kubernetes namespace views.

The layout does not jump on refresh. If the set of nodes has not changed, only
the numbers update and the positions stay exactly where you left them — a graph
that reshuffles every ten seconds is unusable for watching an incident.

The same data from the command line:

```bash
make topology
curl "localhost:8080/api/v1/topology?from=1h&level=workload"
```

### Where the database nodes come from

Nothing is installed in your database. A node like `postgresql:orders` is derived
from the query spans your application emitted — the agent records the database
system and name on each query span, and the topology builder turns those into a
node and an edge. The same applies to outbound HTTP calls to services that do not
run an agent: they show up as external dependencies.

---

## Services

Rate, error rate and p50/p95/p99 per service, computed from entry spans. Click a
service for per-operation breakdown.

The percentiles come from the spans in the selected range, so a wider range
smooths spikes. When comparing a deploy against "before", narrow the range to
either side of it rather than widening it across both.

---

## Traces

Filter by service, minimum duration and errors-only, then click a row for the
waterfall.

The waterfall shows, for each span:

- **Total duration** and **self time** side by side. The bar is two-layered: the
  pale part is total, the solid part is self time.
- **Code location** (`Program.cs:26`) when the span came from code-level timing.
- **The SQL statement** on database spans and the **target URL** on HTTP spans.
- **Exceptions** with type, message and full stack trace.

A span taking 200 ms does not mean it is slow. Self time is what it spent
outside its children:

```
GET /calculate      total  68.12 ms   self   0.33 ms
  validate cart     total  13.02 ms   self  13.02 ms   Program.cs:20
  calculate price   total  45.76 ms   self   0.02 ms   Program.cs:26
    apply campaign  total  45.74 ms   self  45.74 ms   Program.cs:28
  reserve stock     total   9.02 ms   self   9.02 ms   Program.cs:34
```

`calculate price` looks like the problem at 45.76 ms; it is actually
`apply campaign` underneath it.

### Time breakdown

Below the waterfall, where the time went — by category and by service:

```
database              52.92 ms  57.9%
own code              37.92 ms  41.5%
outbound service       0.62 ms   0.7%

By service: cart-service 90.97 ms (99.5%) · sample-backend 0.49 ms (0.5%)
```

Because these are self times, the slices add up to the trace duration. Knowing a
request is slow is not enough — whether it is slow in your own code or in
something it waits on puts the work on different teams.

### Hotspots

A summary ordered by self time, with spans of the same name grouped. This is how
N+1 query patterns become visible: eighty queries of 2 ms each are invisible one
by one, and obvious as a single 160 ms row at the top.

### Sharing

A trace's address (`#traces/<id>`) is shareable — paste it into a ticket and the
recipient lands on the same waterfall.

---

## Code-level timing

Automatic instrumentation sees requests, HTTP calls and database queries. It does
not see your own code in between, which is usually where the time goes.

### Automatic: all methods of DI-registered services

```csharp
builder.Services.AddScoped<ICartService, CartService>();
builder.Services.AddScoped<IStockService, StockService>();

builder.Services.AddNabizCodeLevel();   // AFTER the registrations
```

Every method on those services now gets its own span, without their code being
touched. `AddNabizCodeLevel` wraps what is **already registered**, so it has to
come after the registrations — anything registered afterwards is not wrapped.

**This only covers services registered through an interface.** Registrations of a
concrete class cannot be proxied, and the skipped ones are listed at startup so
you are not left guessing why a service is missing.

> Doing this with zero lines was attempted and does not work: `IHostingStartup`
> runs *before* the application's own service registrations, so there is nothing
> to wrap yet at that point. One line is the honest minimum.

Tune the noise with attributes:

```csharp
public class CartService : ICartService
{
    [NabizTrace(Name = "validate cart")]     // readable span name
    public Task<bool> ValidateAsync(Cart cart) { ... }

    [NabizIgnore]                            // called in a loop, not interesting
    public decimal LineTotal(CartLine line) { ... }
}
```

### Selective: mark a block

When you want one block measured rather than a whole service. File and line come
free from the compiler:

```csharp
var price = NabizTracer.Measure("calculate price", () => Calculate(cart));
await NabizTracer.MeasureAsync("reserve stock", () => ReserveAsync(cart));

using var span = NabizTracer.Start("apply campaign");
ApplyCampaigns(cart);

using var auto = NabizTracer.Start();   // named after the calling method
```

`Measure` / `MeasureAsync` wrap an expression; `Start` returns a disposable scope
for when the block does not fit in a lambda. Both fill in file and line from the
compiler.

### What a span carries

Duration, self time, code location (file and line), allocated memory, thread id,
and whether the async continuation moved to a different thread.

**Parameter values are never recorded** — only their types. Values can carry
personal data, passwords or tokens, so the agent does not send them at all.

### What it does not cover

Wrapping happens at the **service boundary**. A private helper you call inside a
method is invisible:

```csharp
public async Task<decimal> CalculateAsync(Cart cart)   // ← span
{
    var base_ = SumLines(cart);                        // ← no span (private)
    var discount = await _campaigns.ApplyAsync(cart);  // ← span (DI service)
    return base_ - discount;
}
```

Seeing inside a method needs build-time IL weaving. It is designed but
deliberately not shipped here: emitting broken IL means a monitoring tool that
crashes the application it monitors. It is being developed on a separate branch
behind an experimental flag.

---

## On-demand dumps

> **Read this first.** A memory dump writes the **entire memory of the process**
> to disk: connection strings, session tokens, passwords, customer data.
> Anyone who can download that file has all of it.
>
> In this architecture nabiz stores every application's diagnostics token and
> proxies the files. **Whoever takes over nabiz can take the process memory of
> every application it monitors.** That trade-off buys one-click dumps from the
> UI; if it is not acceptable in your environment, leave `diagnostics.enabled`
> off and take dumps with `dotnet-dump` and `kubectl cp` instead.

### Setting it up

Application side:

```bash
dotnet add package Nabiz.Agent.Diagnostics
```

```csharp
builder.Services.AddNabizDiagnostics();
```

```json
"apiUrl": "http://nabiz-api:8080",
"diagnostics": {
  "enabled": true,
  "token": "at-least-16-random-characters",
  "allowDownload": true
}
```

nabiz side: **Administration → Projects → Edit → Diagnostics token**, enter the
same token. It is stored encrypted under `NABIZ_SECRET_KEY` and never shown
again; without that key it is not stored at all.

### How it connects

```
application ──register (60s)──> nabiz ──dump request──> application ──file──> nabiz
```

The agent registers itself and includes the token. nabiz compares it against the
project's token. **If they do not match the instance is listed but dumps cannot
be triggered** — otherwise a forged registration could talk nabiz into sending
the token to an attacker's address. For the same reason the request goes to the
IP the registration came from, not to an address the agent claims;
`advertisedHost` is honoured only for verified registrations, for cases where the
source IP is not routable back (NAT, a proxy, Docker Desktop).

### Taking one

**Diagnostics → CPU profile** or **memory dump** on a running instance. A
progress dialog opens.

No time estimate is shown. How long a memory dump takes depends on the process's
memory, and an invented percentage bar only misleads the person waiting — elapsed
time is shown instead, because that is real information.

**Closing the dialog does not stop the job.** It keeps running in the background
and the finished file appears in the list. Stopping is a separate button, and it
behaves differently per type:

| Type | Stop |
|---|---|
| CPU profile | **Interrupted.** The samples collected so far are kept as a valid profile and downloaded normally. Measured: a 20-second profile stopped at 6 seconds produced a readable 5.4 MB `.nettrace`. |
| Memory dump | **Cannot be interrupted.** `WriteDump` goes to the runtime, which suspends the process and writes the file; there is no way back once it starts. The dialog says so and the job runs to completion. |

If the application managed to stop the job, nabiz **keeps waiting** and fetches
the partial file — abandoning it would throw away the data you collected. nabiz
gives up waiting only when it cannot reach the application at all, and the record
is then marked *stopped*, not *failed*.

### Cost

A memory dump **suspends the process**. Measured locally: a 540 MB dump took
3.8 seconds, and the application served no requests during it. Take dumps on an
instance that has been taken out of rotation.

Only one operation runs at a time. Two concurrent memory dumps would suspend the
process twice, and an application already in trouble would stop entirely.

A CPU profile costs measurable overhead while sampling. Use short windows;
`maxCpuSeconds` enforces an upper bound.

### Reading the output

The CPU profile is raw **nettrace**, deliberately not decoded. PerfView, Visual
Studio and `dotnet-trace convert` already read the format; writing our own
decoder would mean owning its bugs too.

```bash
dotnet-trace convert cpu-20260913-123931.nettrace --format speedscope
```

For memory dumps, `dotnet-dump analyze` or Visual Studio.

Dump types: `heap` (default), `full`, `mini`, `triage`. `full` writes the entire
address space and can be many gigabytes — a process whose `heap` dump is 540 MB
produced a 6.3 GB `full` dump in testing.

### Storage

The **Diagnostics** screen shows a usage bar: files, bytes used against the
quota, and the retention period. A janitor deletes by age and, when the quota is
exceeded, oldest-first. See
[configuration.md](configuration.md#dump-retention).

---

## Administration and access control

The chain:

```
user ──member of──> role group ──bound to──> project ──contains──> application
```

A user sees an application's telemetry only if a role group they belong to is
bound to that application's project. Permissions are never granted directly to a
user, so "why can this person see this data?" always has an answer you can read
off the chain.

| Concept | What it is |
|---|---|
| **Application** | A service sending telemetry (`service.name`). Never typed by hand — picked from the list the collector has actually seen. |
| **Project** | The unit that groups applications and access. |
| **Role group** | A group of users carrying a set of permissions. This is what binds to a project. |
| **System administrator** | Manages the whole control plane and sees all data. |

Permissions: `topology.read`, `services.read`, `traces.read`, `project.manage`,
`diagnostics.manage`, `admin.manage`.

### Setting up a team

1. **Administration → Projects → New.** Give it a name and key.
2. In the project editor, pick its applications from the discovered list. You
   choose from what is actually sending telemetry, so a typo cannot produce a
   project that silently shows nothing.
3. **Administration → Role groups → New.** Tick the permissions.
4. Bind the role group to the project.
5. **Administration → Users → New.** Add users to the role group.

### It is enforced server-side

Filtering happens at query level, not in the UI. Hiding a tab is not a security
measure: a user who types `#admin/users` in the address bar and a script calling
the API directly both get 403.

For resources a user cannot reach, an empty result is returned rather than 403 —
otherwise the endpoint becomes a tool for enumerating which services exist.

### Identity notes

- Passwords are stored with bcrypt; only a SHA-256 digest of the session token is
  in the database.
- The session cookie is `HttpOnly` and `SameSite=Lax`, and `Secure` behind TLS.
- Changing a password closes all of that user's sessions.
- The last administrator in the system cannot be demoted, deactivated or deleted.

---

## The HTTP API

Everything the UI does is an HTTP call you can make yourself. Authentication is
the session cookie from `/api/v1/auth/login`.

```bash
curl -c jar -X POST localhost:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"admin@nabiz.local","password":"nabiz1234"}'

curl -b jar "localhost:8080/api/v1/services?from=1h"
```

| Endpoint | Description |
|---|---|
| `GET /` | The UI (embedded in the binary) |
| `POST /api/v1/auth/login` | Sign in; sets the session cookie |
| `POST /api/v1/auth/logout` | Sign out |
| `GET /api/v1/auth/me` | Who you are, what you can do, which projects you reach |
| `POST /api/v1/auth/password` | Change your own password |
| `GET/POST/PATCH/DELETE /api/v1/admin/users` | User management |
| `GET/POST/PATCH/DELETE /api/v1/admin/role-groups` | Role group management |
| `GET/POST/PATCH/DELETE /api/v1/admin/projects` | Projects and application assignment |
| `GET /api/v1/admin/discovered-applications` | Services that are sending telemetry |
| `PUT /api/v1/admin/projects/{id}/diagnostics-token` | Set a project's diagnostics token |
| `GET /api/v1/services` | Rate, error rate, p50/p95/p99 per service |
| `GET /api/v1/operations?service=` | RED metrics per operation |
| `GET /api/v1/topology?level=` | Nodes and edges |
| `GET /api/v1/traces` | Trace search (`service`, `minDurationMs`, `onlyErrors`) |
| `GET /api/v1/traces/{traceID}` | One trace: spans with self time, hotspots, exceptions |
| `GET /api/v1/diagnostics/instances` | Registered agent instances |
| `POST /api/v1/diagnostics/instances/{id}/capture` | Start a dump; returns `202` with an artifact id |
| `GET /api/v1/diagnostics/artifacts` | Collected files plus storage usage |
| `GET /api/v1/diagnostics/artifacts/{id}` | One artifact's status |
| `POST /api/v1/diagnostics/artifacts/{id}/cancel` | Stop a running job |
| `GET /api/v1/diagnostics/artifacts/{id}/download` | Download the file |
| `DELETE /api/v1/diagnostics/artifacts/{id}` | Delete it |

Time range: `?from=15m` (relative) or `?from=<RFC3339>&to=<RFC3339>`. Any query
can be narrowed to a single project with `?project=<key>`.

Capture returns `202 Accepted` rather than blocking, because a memory dump can
take minutes and the browser would time out waiting. Poll
`/api/v1/diagnostics/artifacts/{id}` for the status.

---

## Troubleshooting

**The topology is empty.**
Check the collector is receiving: `curl -s localhost:8888/stats`. If
`spans_received` is not climbing, the problem is between your application and the
collector — check `endpoint` in `nabiz.json`, or the injection annotation in
Kubernetes.

**Spans are being dropped.**
`spans_dropped` climbing means the queue is full and ClickHouse is slower than
ingest. Raise `NABIZ_QUEUE_SIZE` and `NABIZ_WORKERS`, or give ClickHouse more
resources. This is by design: dropping spans is better than slowing down the
application.

**Edges are missing between two services.**
A `CLIENT` span waits `NABIZ_TOPOLOGY_PAIR_TTL` (30s) for its `SERVER` pair. Over
a slow link, raise it.

**A service is not in the discovered-applications list.**
The list comes from telemetry actually received. If the service has not sent a
span in the retention window, it is not there.

**A code-level service produces no spans.**
Either it is registered as a concrete class rather than through an interface
(check the startup log for the skipped list), or `AddNabizCodeLevel()` is called
before the registration rather than after.

**A dump fails with "connection refused".**
nabiz could not reach the application's diagnostics endpoint. The application may
be down, the port may not be reachable from nabiz, or a NAT/proxy may be in the
way — in the last case set `advertisedHost`. The UI turns the raw network error
into a specific message; read it rather than retrying.

**Diagnostics tokens will not save.**
`NABIZ_SECRET_KEY` is unset. Without it tokens are deliberately not stored at
all, rather than stored in plain text.

**The instance list shows something as stale.**
Agents re-register every 60 seconds. Stale means nabiz has not heard from it,
usually because the process is gone.
