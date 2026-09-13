# Security policy

## Reporting a vulnerability

Please report security issues privately, through
[GitHub's private vulnerability reporting](https://github.com/erdemkayatr/nabiz/security/advisories/new)
on this repository. Do not open a public issue for a vulnerability.

Include what you can: what you found, how to reproduce it, and what an attacker
gets out of it. You will get an acknowledgement, and a fix or an explanation of
why it is not one.

## What nabiz holds

Two things in nabiz are worth an attacker's time. Both are documented here
because knowing what you are deploying is part of deploying it safely.

### Diagnostics tokens and process memory

nabiz can trigger a memory dump on any monitored application and stores the
resulting file on its own disk. **A memory dump contains the entire memory of
the process:** connection strings, session tokens, passwords, customer data.

This means the architecture has a real concentration of risk: nabiz stores every
monitored application's diagnostics token and proxies the files, so **whoever
takes over nabiz can take the process memory of every application it monitors.**

The mitigations in place:

- Diagnostics are **off by default** on the application side, and do not turn on
  without a token of at least 16 characters.
- Tokens are stored AES-GCM encrypted under `NABIZ_SECRET_KEY`. Without that
  key they are not stored at all rather than stored in plain text, and a token
  is never returned to the UI once saved.
- Taking a dump is a **separate permission** (`diagnostics.manage`), not implied
  by being able to read traces.
- An agent registration is verified against the project's token. An unverified
  instance is listed but cannot be triggered — otherwise a forged registration
  could talk nabiz into sending the token to an attacker's address. For the same
  reason the request goes to the IP the registration came from, and
  `advertisedHost` is honoured only for verified registrations.
- `allowDownload` is a separate switch on the application side. While it is off,
  nabiz cannot fetch the file over HTTP at all.

If that concentration is not acceptable in your environment, leave
`diagnostics.enabled` off and take dumps with `dotnet-dump` and `kubectl cp`
instead. Everything else in nabiz keeps working.

### Telemetry access

Traces can contain SQL statements and HTTP targets. Access is filtered
server-side at query level through a single code path, and hiding a tab in the
UI is not treated as a security measure — a user who types the admin URL and a
script calling the API directly both get 403.

Set `captureDbStatement: false` in `nabiz.json` if your codebase inlines
literals into SQL rather than parameterising, since the statement text then
carries data.

Method **parameter values are never recorded** anywhere in the agent, only their
types.

## Deployment notes

- Set `NABIZ_SECRET_KEY` from a Secret, never inline in a manifest. If it
  changes, stored diagnostics tokens can no longer be decrypted.
- Do not set `NABIZ_ADMIN_PASSWORD` in production. Without it nabiz generates a
  random password at first startup and writes it to the log once. The fixed
  credentials in `deploy/docker/docker-compose.yml` are for local development
  and nothing else.
- Put nabiz behind TLS. The session cookie is `HttpOnly` and `SameSite=Lax`, and
  is marked `Secure` when the request arrives over HTTPS (including via
  `X-Forwarded-Proto`).
- The admission webhook ships with `failurePolicy: Ignore` so a broken
  monitoring system cannot stop your deployments. If you would rather fail
  closed, change it — and accept that an operator outage then blocks pod
  creation.

## Supported versions

nabiz is pre-1.0. Security fixes go onto `main` and into the next release; there
are no maintained release branches yet.
