# edge-gateway-controller

Controller module for the gateway's namespaced `Route` custom resource.

It lists all Routes, selects the oldest owner for each normalized prefix,
renders one deterministic `gateway.yaml`, and writes it to a ConfigMap. Route
status reports `Ready` and `Conflict`; conflicting Routes also receive a
Kubernetes warning event.

Before writing, the controller checks the config with the gateway's own rules
by importing `gateway/pkg/config` from the parent module (`replace gateway =>
../`). The Route CRD schema is looser than those rules: for example, it
accepts an `ftp://` upstream. A Route the gateway would reject is left out of
the config and gets `Ready=False` (reason `InvalidConfig`) plus a
`RouteInvalid` event, and the other Routes are still written. If the gateway
rejects the base config itself, nothing is written and every Route reports
`Ready=False` (reason `InvalidBaseConfig`). Because of the parent-module
import, the image builds from the repository root:
`docker build -f controller/Dockerfile .` (`make docker-build` does this).

With `--base-config-map-name` (the Helm chart sets it), the controller reads
that ConfigMap but never writes it. It keeps every base setting and route,
appends the Route routes, and writes the result to `--config-map-name`, with
an owner reference to the base. A base route owns its prefix, so a Route that
claims the same prefix is a conflict. The controller also watches both
ConfigMaps, so a `helm upgrade` that re-renders the base, or an edit to the
output, is reconciled right away.

The repository-level Helm chart installs the CRD and controller. For local
controller development:

```bash
make test       # generation, vet, and envtest integration tests
make docker-build IMG=edge-gateway-controller:dev
```

See the [Week 3 decision note](../docs/decisions/003-controller.md) and the root
[README](../README.md) for the end-to-end kind demo.
