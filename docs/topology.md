# How the topology is derived

🇹🇷 [Türkçe](topoloji.md)

This document explains the reasoning behind the decisions in the
`internal/topology` package.

## The problem

Drawing a service map needs the fact that "service A calls service B". That fact
is written in no single span:

- The calling service has a `CLIENT` span, and it does not know the target's
  **name** — only an address like `http://backend:8080`.
- The called service has a `SERVER` span, and it does not know the caller's
  **name**.

The only thing joining the two is that the `SERVER` span's `parent_span_id`
equals the `CLIENT` span's `span_id`.

## The solution: a two-generation pairing table

Every incoming `CLIENT` and `SERVER` span is written into a table sharded by
`span_id`. If its pair is already there, an edge is produced and both records
are removed.

The pair may never arrive: the target is not instrumented, or the target is a
database. Those records must not pile up forever.

The classic answer is to stamp each record with an expiry and scan periodically,
which gets more expensive as the table grows. Instead, each shard keeps **two
generations** of map:

```
lookup  : check cur, then prev
rotation: discard prev, cur becomes prev, open a new cur   (every TTL/2)
```

Eviction costs O(1), the memory ceiling takes care of itself, and records live
between TTL/2 and TTL. Unpaired `CLIENT` spans in the discarded generation are
not lost: the target name is derived from the span attributes and turned into an
external-dependency edge.

## Deriving the target name

In order, from the most descriptive to the most generic:

| Priority | Source | Example result |
|---|---|---|
| 1 | `db.system` + `db.namespace` | `postgresql:orders` |
| 2 | `messaging.system` + destination | `rabbitmq:order-queue` |
| 3 | `peer.service` | `payment-service` |
| 4 | `rpc.service` | `Shop.Orders.V1` |
| 5 | `server.address` + port | `api.stripe.com:443` |

## Aggregation

Edges are not written raw. They are aggregated per edge identity into one-minute
buckets: call count, error count, duration sum, duration maximum, and a
14-bucket latency histogram.

In a local test, 2605 spans came down to 27 edge rows. Write volume grows with
the complexity of the topology, not with request volume — which is exactly what
an APM needs in order to scale.

## What is in the edge identity

```go
type EdgeKey struct {
	Client, Server                   string
	ClientNamespace, ClientWorkload  string
	ServerNamespace, ServerWorkload  string
	ClientNode, ServerNode           string
	ConnType                         ConnType
}
```

Because the Kubernetes dimensions ride on the edge, three different graphs can
be drawn from the same data: service, deployment and namespace level. The node
dimension is off by default; turning it on makes node-to-node traffic visible
but multiplies edge cardinality by the number of nodes.

## Why latency comes from the server side

A matched pair has two durations: what the client saw, and what the server
spent. The client's includes network latency and connection pool wait. What a
service graph is asking is "how slow is service B", so the server side is used;
the client side only steps in when there is no server span.

## The cost

Measured on an Apple M4 Pro:

```
BenchmarkObserve-14    38510395    93.42 ns/op    0 B/op    0 allocs/op
```

93 ns per span and zero allocations. In practice the bottleneck is ClickHouse
write throughput, not this calculation.
