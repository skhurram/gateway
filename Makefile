.PHONY: run build test controller-test controller-manifests lint vet up down cluster images deploy metrics-server rollout-test helm-check

CLUSTER      ?= edge-gateway
CTX          ?= kind-$(CLUSTER)
NS           ?= edge
GATEWAY_IMG  ?= edge-gateway:dev
ECHO_IMG     ?= edge-gateway-echo:dev
CONTROLLER_IMG ?= edge-gateway-controller:dev
KUBECTL      := kubectl --context $(CTX)
HELM         := helm --kube-context $(CTX)

run:
	go run ./cmd/gateway

build:
	go build -o bin/gateway ./cmd/gateway

vet:
	go vet ./...

test:
	go test -race ./...

controller-test:
	$(MAKE) -C controller test

controller-manifests:
	$(MAKE) -C controller manifests
	cp controller/config/crd/bases/gateway.gateway.local_routes.yaml \
		deploy/helm/gateway/crds/routes.gateway.gateway.local.yaml

lint:
	golangci-lint run ./...

helm-check: controller-manifests
	helm lint deploy/helm/gateway --kube-version 1.31.0
	helm template gateway deploy/helm/gateway --kube-version 1.31.0 -f deploy/kind/gateway-values.yaml > /dev/null

# Create the kind cluster, build and load the images, and deploy everything.
# Requires: docker, kind, kubectl, helm.
up: cluster images metrics-server deploy
	@echo
	@echo "Gateway:  http://localhost:8080/api/users/hello"
	@echo "Jaeger:   $(KUBECTL) -n $(NS) port-forward svc/jaeger 16686:16686  ->  http://localhost:16686"
	@echo "Metrics:  $(KUBECTL) -n $(NS) port-forward svc/otel-collector 8889:8889  ->  http://localhost:8889/metrics"

down:
	kind delete cluster --name $(CLUSTER)

cluster:
	@kind get clusters | grep -qx '$(CLUSTER)' || kind create cluster --config deploy/kind/kind-config.yaml --wait 120s

images:
	docker build -t $(GATEWAY_IMG) .
	docker build --build-arg CMD=echo -t $(ECHO_IMG) .
	docker build -t $(CONTROLLER_IMG) -f controller/Dockerfile .
	kind load docker-image $(GATEWAY_IMG) $(ECHO_IMG) $(CONTROLLER_IMG) --name $(CLUSTER)

# The HPA needs metrics-server. kind uses self-signed kubelet certificates.
# Non-fatal: the gateway works without it, only autoscaling does not.
metrics-server:
	-$(KUBECTL) apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
	-$(KUBECTL) -n kube-system patch deployment metrics-server --type=json \
		-p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'

deploy:
	$(KUBECTL) create namespace $(NS) --dry-run=client -o yaml | $(KUBECTL) apply -f -
	@existing=$$($(HELM) status gateway -n $(NS) >/dev/null 2>&1 && echo yes || echo no); \
	$(KUBECTL) -n $(NS) apply -f deploy/observability.yaml -f deploy/kind/echo.yaml && \
	$(HELM) upgrade --install gateway deploy/helm/gateway -n $(NS) \
		-f deploy/kind/gateway-values.yaml --wait --timeout 3m && \
	if [ "$$existing" = yes ]; then \
		$(KUBECTL) -n $(NS) rollout restart deployment/gateway deployment/gateway-controller deployment/echo-users deployment/echo-orders deployment/otel-collector; \
	fi && \
	$(KUBECTL) -n $(NS) rollout status deployment/gateway --timeout=180s && \
	$(KUBECTL) -n $(NS) rollout status deployment/gateway-controller --timeout=180s && \
	$(KUBECTL) -n $(NS) rollout status deployment/echo-users deployment/echo-orders --timeout=180s

# Load test through the gateway while it does a rolling update; fails on any
# non-200 response. Requires: hey (go install github.com/rakyll/hey@latest).
rollout-test:
	CTX=$(CTX) NS=$(NS) scripts/rollout-test.sh
