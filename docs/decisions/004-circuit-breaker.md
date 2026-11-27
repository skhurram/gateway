# 004: Passive circuit breaking plus active health checks

## Context

Active health checks remove an upstream after a failed probe, but an upstream
can fail between probes. Repeated requests to it during that interval waste
capacity and increase client latency.

## Decision

Each upstream gets a circuit breaker. Connection errors, timeouts, and 5xx
responses count as failures; 4xx responses do not. (Decision 005 narrows the
5xx part to 500, 502, 503 and 504.) After the configured number
of consecutive failures, the balancer stops selecting that upstream. After the
open timeout, a limited number of trial requests test recovery. Any failed
trial reopens the breaker; all successful trials close it.

Health checks remain active and independent. They determine whether an
upstream's declared health endpoint passes and continue probing an open circuit.
Traffic requires both an active health pass and an admitting breaker. The
breaker reacts immediately to real request failures, while the health checker
can restore service health and continues to expose probe-based failures.

Breaker state is per runtime and therefore resets on config reload or pod
restart. This is acceptable because it avoids retaining stale failure history
for an upstream whose configuration changed.

## Alternatives

Using only active health checks was retained as the simpler baseline, but it
cannot react until the next probe. Retrying each failed request on another
upstream could mask failures, but would duplicate unsafe non-idempotent
requests and is intentionally not enabled.
