#!/usr/bin/env bash
# Installs the project tooling that is not covered by dev container features.
set -euo pipefail

GOBIN="$(go env GOPATH)/bin"
ARCH="$(dpkg --print-architecture)"

echo "==> Installing Go tools"
go install sigs.k8s.io/kind@latest
go install github.com/rakyll/hey@latest
go install github.com/tsenart/vegeta/v12@latest
go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest
go install sigs.k8s.io/controller-tools/cmd/controller-gen@latest

echo "==> Installing golangci-lint"
curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh \
  | sh -s -- -b "${GOBIN}"

echo "==> Installing kubebuilder"
sudo curl -sSfL -o /usr/local/bin/kubebuilder \
  "https://go.kubebuilder.io/dl/latest/linux/${ARCH}"
sudo chmod +x /usr/local/bin/kubebuilder

echo "==> Installing system packages"
sudo apt-get update
sudo apt-get install -y --no-install-recommends redis-tools jq
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
