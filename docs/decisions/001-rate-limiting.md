# 001: Why a token bucket for rate limiting

Status: accepted

## Context

The gateway needs to protect upstreams from a single noisy client while
tolerating short, legitimate bursts (a page load firing a dozen API calls at
once). The limiter runs on every request, so it must be cheap and must not
need a background goroutine per client.

## Decision

Use a token bucket per client IP, configured per route with
`requests_per_second` (refill rate) and `burst` (bucket size).

- State per client is two numbers: tokens and the last refill time. Refill is
  computed lazily on each request, so there is no timer per client.
- Rejected requests get `429` and a `Retry-After` header computed from the
  exact time until the next token (rounded up to whole seconds, minimum 1).
- The clock is injected, so tests advance time instead of sleeping.
- Clients are keyed by the connection's remote IP. `X-Forwarded-For` is
  ignored because clients can set it freely and would otherwise pick their
  own bucket.
- Buckets that have been idle long enough to be full again are swept once a
  minute, since a fresh bucket is equivalent. Memory is bounded by recently
  active clients.
- Buckets are split across 64 shards by a hash of the client key, each with
  its own lock, and each shard sweeps itself. Unrelated clients rarely
  contend, and a sweep pauses only one shard's clients. With 200,000 clients
  on 8 CPUs, the worst pause dropped from about 25 ms to under 1 ms (issue
  #2, `BenchmarkSweepPause`).

## Alternatives considered

- **Fixed window counter.** Simplest, but allows up to twice the limit across
  a window boundary and gives no natural `Retry-After`.
- **Sliding window log.** Exact, but stores a timestamp per request, so memory
  grows with the limit. Sliding window counters are cheaper but approximate.
- **Leaky bucket.** Smooths traffic into a constant rate, which delays
  requests instead of rejecting them. Not what an API gateway usually wants.

## Consequences

- Bursts up to `burst` are allowed after idle periods, then traffic settles at
  `requests_per_second`.
- Limits are per gateway instance. Running N replicas allows roughly N times
  the configured rate. A shared store (for example Redis) would fix that at the
  cost of a network hop per request; it is out of scope for v0.1.
- Behind a load balancer or proxy that hides client addresses, every client
  shares one bucket until trusted-proxy handling is added.
