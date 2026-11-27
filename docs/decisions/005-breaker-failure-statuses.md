# 005: Which upstream responses count as breaker failures

## Context

Decision 004 counted every 5xx response as a circuit breaker failure. Some
5xx statuses do not mean the upstream is unhealthy. `501 Not Implemented` and
`505 HTTP Version Not Supported` say that this upstream cannot handle this
kind of request. A misrouted or misconfigured route can produce them on every
request while the upstream is fine. Counting them lets one bad route take a
healthy upstream out of rotation for every route that shares it (issue #8).

## Decision

A response counts as a failure only when its status says the upstream failed
or is unavailable:

- `500 Internal Server Error`
- `502 Bad Gateway`
- `503 Service Unavailable`
- `504 Gateway Timeout`

Connection errors and timeouts still count as failures, as in decision 004.
Every other status counts as success, including `501`, `505`, `506`, `507`,
`508`, `510`, `511` and non-standard 5xx codes. The upstream answered
coherently, which is the same reasoning that already applies to 4xx
responses. These responses are still passed to the client unchanged, and
they still appear in the 5xx metrics.

This is the same set that nginx offers for `proxy_next_upstream`
(`http_500`, `http_502`, `http_503`, `http_504`).

## Alternatives

- **All 5xx except 501 and 505.** This is closer to the old behavior, but
  every new or non-standard 5xx code would count by default, including
  codes some CDNs and frameworks use for request-specific errors.
- **Only 502, 503 and 504** (gateway failures, as in Envoy's
  `consecutive_gateway_failure`). This would ignore an upstream that crashes
  on every request with `500`, which is the most common sign of a broken
  deployment.
- **Making the set configurable per route.** This is more flexible, but it
  adds configuration surface before there is a demonstrated need. It can be
  added later without changing the default.

## Consequences

- A route that answers `501` for every request no longer opens the breaker
  for its upstream.
- An upstream that fails with `507 Insufficient Storage` is not taken out of
  rotation by the breaker. The active health check can still remove it if
  its health endpoint fails.
