# edge-gateway-controller

Controller module for the gateway's namespaced `Route` custom resource.

It lists all Routes, selects the oldest owner for each normalized prefix,
renders one deterministic `gateway.yaml`, and writes it to a ConfigMap. Route
status reports `Ready` and `Conflict`; conflicting Routes also receive a
Kubernetes warning event.

The repository-level Helm chart installs the CRD and controller. For local
controller development:

```bash
make test       # generation, vet, and envtest integration tests
make docker-build IMG=edge-gateway-controller:dev
```

See the [Week 3 decision note](../docs/decisions/003-controller.md) and the root
[README](../README.md) for the end-to-end kind demo.
