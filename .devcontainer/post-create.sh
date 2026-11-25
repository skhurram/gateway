#!/usr/bin/env bash
# Installs the project tooling that is not covered by dev container features.
set -euo pipefail

GOBIN="$(go env GOPATH)/bin"
ARCH="$(dpkg --print-architecture)"

echo "==> Installing Go tools"
# kind is pinned to the version used when the cluster demo was verified; it
# picks the Kubernetes node image (the chart needs 1.30 or newer).
go install sigs.k8s.io/kind@v0.33.0
go install github.com/rakyll/hey@latest
go install github.com/tsenart/vegeta/v12@latest
go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest
go install sigs.k8s.io/controller-tools/cmd/controller-gen@latest

echo "==> Installing golangci-lint (same version as CI)"
curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh \
  | sh -s -- -b "${GOBIN}" v2.15.0

echo "==> Installing kubebuilder"
sudo curl -sSfL -o /usr/local/bin/kubebuilder \
  "https://go.kubebuilder.io/dl/latest/linux/${ARCH}"
sudo chmod +x /usr/local/bin/kubebuilder

echo "==> Installing system packages"
sudo apt-get update
sudo apt-get install -y --no-install-recommends redis-tools jq python3
sudo rm -rf /var/lib/apt/lists/*

echo "==> Downloading Go modules"
if [ -f go.mod ]; then
  go mod download
fi

echo "==> Tool versions"
go version
docker --version
kind version
kubectl version --client
helm version --short
terraform version | head -n 1
golangci-lint --version
kubebuilder version || true

cat <<'EOF'

==> Ready. Next steps:
    make test                 # unit tests with -race (no cluster needed)
    make up                   # kind cluster + full demo (about 4 minutes cold)
    make rollout-test         # zero-downtime rollout proof (needs make up)
    plan/sessions/2026-11-25-codespaces-handoff.md   # where the previous session stopped
EOF
