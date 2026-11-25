# 003: Aggregate Routes in a ConfigMap

## Context

A `Route` exposes a path prefix, one or more upstreams, and optional per-client
rate limiting. The controller has to turn any set of Routes into one gateway
configuration while keeping the gateway available during updates.

## Options

### Controller pushes an admin endpoint

The controller could POST a rendered configuration directly to each gateway
pod or to a gateway service.

- Fast propagation: the controller observes a Route and pushes immediately.
- The gateway needs an authenticated administration endpoint and the
  controller needs a way to target every replica.
- Temporary disconnects require retries and delivery tracking. A successful
  Kubernetes reconcile would not by itself prove that every pod received the
  update.

### Controller writes an aggregated ConfigMap

The controller lists all Routes in a namespace, renders one deterministic
`gateway.yaml`, and writes it to a ConfigMap mounted by the gateway pods.

- Kubernetes stores the desired state, so restarts and missed controller events
  recover from the same aggregate.
- Gateway reload is independent of the controller once the ConfigMap is
  visible.
- Mounted ConfigMaps are eventually consistent. Kubernetes can take up to
  about a minute to project a ConfigMap update into a pod, which affects the
  demo and is too slow for a control plane with strict propagation deadlines.

## Decision

Use one aggregated ConfigMap per namespace and have the gateway watch the
mounted config directory, validate each new document, and atomically swap the
router and health checker. The durable, inspectable configuration and absence
of a privileged network endpoint fit this learning project better than faster
push delivery.

The renderer sorts routes by normalized prefix and name. For duplicate
prefixes, the oldest Route wins and the other Routes receive a `Conflict`
condition plus a warning event. The winner is determined before rendering, so
the same Kubernetes input always produces byte-identical YAML.

Reconciliation always rebuilds the whole ConfigMap. Deleting a Route therefore
requires no finalizer: the next reconciliation renders the remaining Routes
and removes the deleted entry.

Gateway reloads keep the last known-good runtime if validation fails. A reload
replaces the router, health-check generation, and readiness source together.
In-flight requests retain their original router until completion.

## Consequences

- ConfigMap projection can add up to about a minute of latency to the live
  demo.
- Reloading resets each route's in-memory rate-limit buckets. This is an
  acceptable tradeoff for this version; limits are intentionally local to one
  gateway process.
- Changing the listen address cannot be applied to the existing HTTP server.
  The gateway rejects such a reload and continues with its current listener.
- A Route aggregation containing no routes is written, but the gateway's
  config validation rejects it and retains its last valid route set.
