# Nabiz.Agent.Diagnostics

On-demand **CPU profiles** and **memory dumps** for the nabiz agent. You send
the application a request, and the file is produced.

🇹🇷 [Türkçe](README.tr.md)

> **A memory dump writes the entire memory of the process to disk:** connection
> strings, session tokens, passwords, customer data. That is why the feature is
> **off by default** and **does not turn on without a token**.

A separate package, so the diagnostics IPC stack only enters applications that
need it.

```bash
dotnet add package Nabiz.Agent.Diagnostics
```

```csharp
builder.Services.AddNabizDiagnostics();
```

`nabiz.json`:

```json
"diagnostics": {
  "enabled": true,
  "path": "/nabiz/diag",
  "token": "at-least-16-random-characters",
  "outputDirectory": "",
  "maxFiles": 5,
  "maxCpuSeconds": 120,
  "allowDownload": false
}
```

## From the nabiz UI

If `apiUrl` is set in `nabiz.json`, the agent registers itself with nabiz and
appears under the **Diagnostics** menu; you can take a dump from there with one
click.

```json
"apiUrl": "http://nabiz-api:8080",
"diagnostics": {
  "enabled": true,
  "token": "...",
  "allowDownload": true,
  "advertisedHost": ""
}
```

nabiz sends the dump request to **the IP the registration came from**, not to
the address the agent claims. When the source IP is not routable back (NAT, a
proxy, Docker Desktop), fill in `advertisedHost` — it is honoured only for
token-verified registrations.

With `allowDownload` off, nabiz cannot fetch the file; it stays on the
application's disk and the UI shows "download disabled".

## Endpoints

All of them require the `X-Nabiz-Token` header.

| Endpoint | What it does |
|---|---|
| `POST /nabiz/diag/cpu?seconds=20` | CPU sampling, produces a `.nettrace` |
| `POST /nabiz/diag/memory?type=heap` | Memory dump, produces a `.dmp` |
| `POST /nabiz/diag/cancel` | Stops the running job |
| `GET /nabiz/diag` | Lists the produced files |
| `GET /nabiz/diag/{file}` | Downloads one (requires `allowDownload`) |
| `DELETE /nabiz/diag/{file}` | Deletes one |

```bash
curl -X POST -H "X-Nabiz-Token: $TOKEN" \
  "http://localhost:5000/nabiz/diag/cpu?seconds=20"
```

`type` values: `heap` (default), `full`, `mini`, `triage`.

`cancel` returns `{"cancelled": bool, "reason": string}`. A CPU profile can be
interrupted: the session is closed and the samples collected so far make a valid
file. A memory dump cannot — `WriteDump` goes to the runtime, which suspends the
process and writes the file; trying to cut it short risks leaving a suspended
process behind. In that case `cancelled: false` comes back with the reason.

## Reading the output

The CPU profile is raw **nettrace**, deliberately not decoded. PerfView, Visual
Studio and `dotnet-trace convert` already read the format; writing our own
decoder would mean owning its bugs too.

```bash
dotnet-trace convert cpu-20260912-221500.nettrace --format speedscope
```

For memory dumps, `dotnet-dump analyze` or Visual Studio.

## Security

- Off by default; it needs `enabled: true` **and** a token of at least 16
  characters. A short token leaves the endpoints closed, and the reason is
  logged.
- The token is compared in constant time.
- `allowDownload` is a separate switch. Turning it on means anyone who obtains
  the token can download the process's memory over HTTP. While it is off the
  files simply stay on disk and are collected with `kubectl cp`.
- Every dump request is logged with the client address.
- Only one operation runs at a time: two concurrent memory dumps would suspend
  the process twice and bring an application that is already in trouble to a
  complete stop.

## Cost

- **CPU profile:** measurable overhead for the duration of the sampling. Use
  short windows; `maxCpuSeconds` enforces an upper bound.
- **Memory dump:** the process is **suspended** while the dump is written. In a
  local measurement a 444 MB dump took 3.8 seconds, and the application served
  no requests during it. Take it on an instance that has been taken out of
  rotation.
- In Kubernetes a writable directory is required; for pods running with
  `readOnlyRootFilesystem`, mount an `emptyDir` and point `outputDirectory` at
  it.

## License

[Apache License 2.0](https://github.com/erdemkayatr/nabiz/blob/main/LICENSE)
