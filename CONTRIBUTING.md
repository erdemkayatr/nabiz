# Contributing to nabiz

Thanks for taking an interest. This document covers how to get the project
running, what the code expects of you, and how a change gets merged.

## Getting set up

```bash
git clone https://github.com/erdemkayatr/nabiz.git
cd nabiz
make up        # the whole stack plus sample traffic
```

Then <http://localhost:8080> — `admin@nabiz.local` / `nabiz1234`.

You need Go 1.26+, Docker with Compose v2, and the .NET SDK 8.0+ if you are
touching the agent. Details: [docs/installation.md](docs/installation.md).

## Before you open a pull request

```bash
make fmt     # gofmt + go vet
make race    # tests with the race detector
make bench   # the topology hot path
```

CI runs all three, plus a `gofmt -l` check that fails on unformatted files. The
race detector is not optional here: the topology builder's sharded pairing table
is concurrent, and a plain `go test` runs green straight past its bugs.

## Language

**Code, comments, commit messages, logs and error strings are in English.** The
UI is bilingual (English and Turkish) and the documentation exists in both, but
everything a contributor reads in the source is English.

When you add a user-facing string to the UI, add it to **both** dictionaries in
`web/app.js`. A missing key renders as the key itself, which is a bug, not a
fallback.

## What the code expects

A few rules that are load-bearing rather than stylistic:

- **The collector never blocks the sender.** If a queue fills, drop and count.
  A slow store must never become a slow application. Do not "fix" a drop by
  making a queue unbounded.
- **Authorization filtering happens in one place.** Every query endpoint goes
  through `scope` in `internal/api/authz.go`. If you add an endpoint that reads
  telemetry, it uses the same filter — a per-endpoint filter is how the "we
  forgot that one" bug happens.
- **An unreachable resource returns an empty result, not 403.** Otherwise the
  endpoint becomes a tool for enumerating what exists.
- **Parameter values are never recorded.** Only types. Values can carry personal
  data, passwords or tokens.
- **The agent must never take the application down.** Anything in the startup
  path catches its own exceptions. An observability tool that crashes what it
  observes is worse than no telemetry.

## Comments

Comments in this codebase explain **why**, not what. If a line needs a comment
saying what it does, the line usually wants rewriting instead. The comments
worth adding are the ones that stop someone "simplifying" a deliberate decision
six months from now — which is why several of them read like arguments.

## Commit messages

A subject line in the imperative, then a body explaining why the change is the
right one. When a decision has a trade-off, name it. When you measured
something, put the number in.

## Releasing the NuGet packages

Publishing runs from a tag, not from a merge to main — a package version cannot
be taken back, so it stays an explicit decision.

1. Bump `<Version>` in **both** `agent/dotnet/Nabiz.Agent/Nabiz.Agent.csproj`
   and `agent/dotnet/Nabiz.Agent.Diagnostics/Nabiz.Agent.Diagnostics.csproj`,
   and the `Nabiz.Agent` `PackageReference` version inside the Diagnostics
   csproj. They release together because Diagnostics depends on a matching
   Agent.
2. Commit, then tag with the same version:

   ```bash
   git tag v0.8.0
   git push origin v0.8.0
   ```

The workflow refuses to publish if the tag and the csproj versions disagree.

To check what would be published without publishing it, run the **Publish to
NuGet** workflow manually with `dry_run` left on: it packs, uploads the
`.nupkg` files as build artifacts, and pushes nothing.

## Reporting a security issue

Please do not open a public issue. See [SECURITY.md](SECURITY.md).
