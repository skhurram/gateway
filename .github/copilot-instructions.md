# edge-gateway: instructions for coding agents

A Go reverse proxy and API gateway, deployed to Kubernetes with Helm and provisioned with Terraform. Read `README.md` and `docs/decisions/` before starting work.

## Status
`v0.4.0` is tagged. Open known issues are tracked in GitHub issues. Check the
README's limitations section before claiming a feature works in production.

## Commands
- `make test` runs `go test -race ./...`. `make vet`, `make lint` (golangci-lint v2.15.0, same as CI), `gofmt -l .`.
- `make helm-check` lints and renders the chart. Use `--kube-version 1.31.0` if you call helm directly.
- `make up` creates the kind cluster and deploys everything (about 4 minutes cold). `make down` deletes it. `make rollout-test` needs the cluster.
- Before finishing any change: `gofmt -l .` prints nothing, `go vet ./...`, `go test -race ./...`, `golangci-lint run ./...`.

## Layout
- `cmd/gateway` (main), `cmd/echo` (demo upstream).
- `pkg/config` (config parsing and validation; the controller module imports it).
- `internal/{proxy,balancer,health,ratelimit,telemetry,breaker}`.
- `deploy/helm/gateway` (chart), `deploy/kind`, `deploy/observability.yaml`, `scripts/`.
- `docs/decisions/` holds short decision notes. Add one when a design choice has real alternatives.
- `controller/` is a separate Go module for the `Route` controller.

## Conventions
- Standard library first. Add a dependency only with a reason. Current ones: `yaml.v3` and OpenTelemetry.
- Table-driven tests, `httptest` for upstreams, and `go test -race` must pass. Inject clocks instead of sleeping in tests. New behavior needs a test.
- Each package starts with a package doc comment. Errors are wrapped with `%w` and checked with `errors.Is`.
- Keep the README honest: only claim results you measured, and say where you measured them.
- Reserved paths `/healthz` and `/readyz` are served before routing.
- Config is rejected on unknown YAML fields. Route prefixes match on path-segment boundaries.

## Things that are easy to get wrong
- `OTEL_METRIC_EXPORT_INTERVAL` is an integer in milliseconds, not `10s`.
- The proxy forwards the full request path (no prefix stripping).
- Run golangci-lint with a Go at least as new as `go.mod` (`go 1.27.1`).
- When creating new `.go` files, check the first lines. Some editors insert a stray duplicate `package` line.
- Hot reload must swap the router, the health checker and the metrics gauge callback together. See `pkg/config` and `cmd/gateway/runtime.go`.

## Working agreements
- Do not push, tag, delete branches or clusters, or change repository visibility without asking.
- Be brief. No emojis. State what you ran and what it showed.
