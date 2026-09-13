# Installation

🇹🇷 [Türkçe](kurulum.md) · [Usage](usage.md) · [Configuration](configuration.md)

Three ways to run nabiz, from quickest to most production-like:

1. [Docker Compose](#1-docker-compose) — the whole stack on one machine, with sample traffic
2. [Kubernetes](#2-kubernetes) — manifests plus an injecting admission webhook
3. [From source](#3-from-source) — build the binaries yourself

Then: [instrumenting your own application](#instrumenting-your-own-application).

---

## Requirements

| | Version | Needed for |
|---|---|---|
| Docker | 24+ with Compose v2 | Option 1 |
| Kubernetes | 1.25+ | Option 2 |
| Go | 1.26+ | Option 3, development |
| .NET SDK | 8.0+ | Building the agent packages and samples |
| ClickHouse | 24+ | Telemetry storage (Compose and the manifests bring their own) |
| PostgreSQL | 14+ | Control plane (same) |

nabiz itself is three small Go binaries. Everything heavy is ClickHouse.

**Sizing.** A starting point for a cluster doing a few thousand spans/second:
ClickHouse 4 vCPU / 8 GB RAM and disk sized for your retention (spans compress
roughly 10x; measure your own traffic before committing). The collector and api
are happy with 500m CPU / 512Mi each. PostgreSQL holds only users, role groups,
projects and sessions — it stays tiny.

---

## 1. Docker Compose

The fastest way to see nabiz working, including two sample .NET services and a
load generator that keeps traffic flowing.

```bash
git clone https://github.com/erdemkayatr/nabiz.git
cd nabiz
make up
```

`make up` is `docker compose -f deploy/docker/docker-compose.yml up -d --build`.
It starts:

| Service | Published port | What it is |
|---|---|---|
| `clickhouse` | 8123, 9000 | Telemetry storage |
| `nabiz-postgres` | — | Control plane database (internal only) |
| `collector` | 4317 (OTLP/gRPC), 4318 (OTLP/HTTP), 8888 (`/stats`, `/healthz`) | OTLP receiver |
| `api` | 8080 | Query endpoint + UI |
| `dump-init` | — | Runs once to chown the dump volume, then exits |
| `backend` | 8081 | Sample .NET service (calls Postgres) |
| `frontend` | 8082 | Sample .NET service (calls `backend`) |
| `postgres` | 5432 | The sample services' own database |
| `loadgen` | — | Generates continuous traffic |

`dump-init` showing as `Exited` is expected: the dump volume is created owned by
root and the api runs as uid 10001, so one container fixes the ownership and
stops. In Kubernetes `fsGroup` handles the same thing.

Give it about a minute — ClickHouse has to become healthy and the schema has to
be created — then open:

**http://localhost:8080** → `admin@nabiz.local` / `nabiz1234`

### Verifying it works

```bash
make topology    # the service graph derived from requests
make services    # RED metrics per service
make stats       # the collector's internal counters
make logs        # follow the collector
```

If the topology is empty, work down this list:

```bash
# 1. Is the collector receiving anything? received should be climbing.
curl -s localhost:8888/stats | python3 -m json.tool

# 2. Is ClickHouse healthy?
docker compose -f deploy/docker/docker-compose.yml ps

# 3. Are the sample services actually sending?
docker compose -f deploy/docker/docker-compose.yml logs backend | tail -20
```

A `spans_dropped` or `dropped` counter climbing in `/stats` means the queue is
full: ClickHouse is slower than ingest. Raise `NABIZ_QUEUE_SIZE` and `NABIZ_WORKERS`, or give
ClickHouse more resources. nabiz drops spans rather than applying backpressure
on purpose — a slow store must never become a slow application.

### Shutting down

```bash
make down        # removes containers and volumes (-v)
```

---

## 2. Kubernetes

```bash
kubectl apply -f deploy/k8s/
```

This creates the `nabiz` namespace, ClickHouse, PostgreSQL, the collector, the
api and the operator. The manifests are deliberately plain YAML — no Helm chart,
no operator framework — so you can read exactly what lands in your cluster.

Wait for everything to be ready:

```bash
kubectl -n nabiz get pods -w
```

Then reach the UI:

```bash
kubectl -n nabiz port-forward svc/nabiz-api 8080:8080
```

The manifests deliberately do not set `NABIZ_ADMIN_PASSWORD`. nabiz generates
one at first startup and writes it to the log **once**:

```bash
kubectl -n nabiz logs deploy/nabiz-api | grep "ilk yönetici"
```

```
level=WARN msg="ilk yönetici oluşturuldu — bu parola bir daha gösterilmeyecek"
  email=admin@nabiz.local parola=<the generated password>
```

Save it immediately; it is not recoverable from the database, only resettable.
To choose the password yourself instead, add it to the deployment before the
first apply — from a Secret, not inline:

```bash
kubectl -n nabiz create secret generic nabiz-admin \
  --from-literal=password='choose-something-long'
```

```yaml
- name: NABIZ_ADMIN_PASSWORD
  valueFrom:
    secretKeyRef: { name: nabiz-admin, key: password }
```

### The encryption key

Diagnostics tokens are stored encrypted with `NABIZ_SECRET_KEY`. The manifests
read it from an optional Secret that **you** create:

```bash
kubectl -n nabiz create secret generic nabiz-secret-key \
  --from-literal=key="$(openssl rand -base64 32)"
```

Without it, diagnostics tokens are not stored at all and the UI says why. If the
key later changes, previously stored tokens can no longer be decrypted and have
to be re-entered.

### Instrumenting a .NET pod

One annotation on the pod template:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: cart-service
spec:
  template:
    metadata:
      annotations:
        nabiz.io/inject-dotnet: "true"
    spec:
      containers:
        - name: app
          image: registry.example.com/cart-service:1.4.2
```

At admission the operator adds an init container and environment variables to
the pod. The application image, its Dockerfile and its code do not change;
undoing it is deleting the annotation.

#### Annotation reference

| Annotation | Default | Description |
|---|---|---|
| `nabiz.io/inject-dotnet` | — | `"true"` enables injection |
| `nabiz.io/service-name` | pod label | Sets `OTEL_SERVICE_NAME` |
| `nabiz.io/container` | first container | Target in a multi-container pod |
| `nabiz.io/sample-ratio` | `1.0` | Agent-side sampling ratio |
| `nabiz.io/libc` | `glibc` | Use `musl` for Alpine-based images |

#### Webhook certificates

The operator generates a self-signed CA at startup and patches its own
`MutatingWebhookConfiguration` with the bundle. cert-manager is not required.
The certificate is regenerated if the secret is missing, so deleting the secret
and restarting the operator is a valid way to rotate it.

If pods stop being admitted after an operator problem, the webhook's
`failurePolicy` decides what happens. It ships as `Ignore` so a broken monitoring
system cannot stop your deployments — the trade-off being that pods created
during an outage come up without instrumentation and need a restart afterwards.

### Sizing the dump volume

The api keeps collected dumps on a volume. A single memory dump can be hundreds
of megabytes — a `Full` type dump of a large process can exceed several GB.

```yaml
- { name: NABIZ_DUMP_QUOTA_GB,      value: "15" }
- { name: NABIZ_DUMP_RETENTION_DAYS, value: "7" }
```

Keep `NABIZ_DUMP_QUOTA_GB` **below** the volume's actual size. The quota is
enforced by a janitor that deletes oldest-first; if the disk fills before the
quota is reached, the pod gets evicted instead.

---

## 3. From source

```bash
git clone https://github.com/erdemkayatr/nabiz.git
cd nabiz
make build
```

Binaries land in the module's build output; `cmd/collector`, `cmd/api` and
`cmd/operator` each build independently:

```bash
go build -trimpath -o bin/nabiz-collector ./cmd/collector
go build -trimpath -o bin/nabiz-api       ./cmd/api
go build -trimpath -o bin/nabiz-operator  ./cmd/operator
```

You supply ClickHouse and PostgreSQL yourself:

```bash
export NABIZ_CLICKHOUSE_ADDRS=localhost:9000
export NABIZ_POSTGRES_DSN='postgres://nabiz:nabiz@localhost:5432/nabiz?sslmode=disable'
export NABIZ_SECRET_KEY='a-long-random-value'

./bin/nabiz-collector &
./bin/nabiz-api
```

Both create their own schemas at startup; there is no separate migration step.

The UI is embedded in the api binary through `go:embed`, so `go build` is the
only build step — there is no JavaScript toolchain, bundler or `node_modules`.

### Development commands

```bash
make build   # compile
make test    # tests
make race    # tests with the race detector
make bench   # hot-path benchmark
make fmt     # gofmt + go vet
```

---

## Instrumenting your own application

### With the NuGet package (anywhere)

On a developer machine, a VM, a Windows service or a container that the operator
does not reach:

```bash
dotnet add package Nabiz.Agent
dotnet build
```

After the build, `nabiz.json` appears next to your project file:

```json
{
  "endpoint": "http://nabiz-collector:4317",
  "serviceName": "cart-service",
  "sampleRatio": 1.0
}
```

Set `endpoint` to your collector's address. The file is generated once and never
overwritten, so your edits survive rebuilds. Commit it or don't — environment
variables override it either way:

```bash
NABIZ_ENDPOINT=http://nabiz-collector:4317 NABIZ_SERVICE_NAME=cart-service dotnet run
```

No code change is needed. The package injects a `[ModuleInitializer]` at build
time and the agent starts with the application.

> The config file and the module initializer are only generated for executable
> projects (`OutputType` of `Exe` or `WinExe`). A class library that references
> the package gets neither — a library is not what reads configuration or starts
> the agent.

### Code-level timing (optional)

To see your own methods between the HTTP and database spans, add one line after
your DI registrations:

```csharp
builder.Services.AddScoped<ICartService, CartService>();
builder.Services.AddNabizCodeLevel();   // AFTER the registrations
```

Order matters: `AddNabizCodeLevel` wraps what is already registered. See
[usage.md](usage.md#code-level-timing).

### Dumps (optional)

```bash
dotnet add package Nabiz.Agent.Diagnostics
```

```csharp
builder.Services.AddNabizDiagnostics();
```

Then in `nabiz.json`:

```json
"apiUrl": "http://nabiz-api:8080",
"diagnostics": {
  "enabled": true,
  "token": "at-least-16-random-characters",
  "allowDownload": true
}
```

And in the UI, **Administration → Projects → Edit → Diagnostics token**, enter
the same token. Read [usage.md](usage.md#on-demand-dumps) first — a memory dump
contains everything in the process.

---

## Upgrading

nabiz creates and migrates its own schemas at startup, in both ClickHouse and
PostgreSQL. Upgrading is replacing the images and restarting:

```bash
# Compose
git pull && make up

# Kubernetes
kubectl -n nabiz rollout restart deploy/nabiz-collector deploy/nabiz-api deploy/nabiz-operator
```

The agent packages version independently. `Nabiz.Agent.Diagnostics` depends on a
matching `Nabiz.Agent`, so upgrade them together.

---

## Uninstalling

```bash
# Compose — removes volumes too
make down

# Kubernetes
kubectl delete -f deploy/k8s/
```

To stop instrumenting an application, remove the `nabiz.io/inject-dotnet`
annotation and restart the pod, or remove the `Nabiz.Agent` package reference
and rebuild. Neither leaves anything behind in your application: the agent never
modified your code, only the running process.
