# 002: Health checks, active versus passive

Status: accepted

## Context

The gateway must stop sending traffic to a broken upstream and resume when it
recovers, without an operator touching it. Two signals are available: probe the
upstream on a timer (active), or watch the outcome of real requests (passive).

## Decision

Use **active** checks for v0.2: every `health.interval` the gateway issues
`GET <upstream><health.path>` to each upstream. A 2xx response puts it in
rotation; a non-2xx response, a timeout or a connection error takes it out. A
single probe decides, so recovery is as fast as the interval.

The gateway also runs one probe round **before it starts listening**, so
`/readyz` reflects real upstream state from the first request instead of an
optimistic default.

`/readyz` is `200` only while at least one upstream is healthy and the process
is not draining. `/healthz` only says the process is alive. They are separate
because the cost of getting them wrong differs:

- A failing liveness probe restarts the pod. Tying it to upstream health would
  restart every gateway replica because a backend is down, which fixes nothing
  and removes capacity.
- A failing readiness probe only removes the pod from Service endpoints, which
  is the right response when it cannot serve.

## Alternatives considered

- **Passive only** (eject after N consecutive request failures). Needs no extra
  traffic and reacts to real failures, but the first requests to a dead
  upstream are the ones that fail, and an ejected upstream needs some other
  mechanism to be re-admitted. It also cannot tell "down" from "this request
  was bad".
- **Active plus passive.** What production proxies do. Passive ejection catches
  failures between probes. It is deferred because it needs ejection timing and
  re-admission rules that deserve their own design, and the circuit breaker
  planned for v0.4 covers the same ground.
- **Thresholds (N failures to eject, M successes to recover).** Avoids flapping
  on a single dropped probe. Not needed yet with a 2 to 5 second interval, and
  cheap to add later inside the probe loop.

## Consequences

- Worst-case time to notice a dead upstream is `interval + timeout`. In the
  kind demo (`2s` and `1s`) that is about 3 seconds of failed requests.
- Probe traffic is `upstreams x replicas / interval`. Negligible here, but it
  grows with fleet size.
- Upstreams must expose a cheap health endpoint (`/health` by default).
- One lost probe flips an upstream out for a full interval. If that proves
  noisy, add the thresholds above.
