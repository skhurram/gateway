.PHONY: run build test lint vet

run:
	go run ./cmd/gateway

build:
	go build -o bin/gateway ./cmd/gateway

vet:
	go vet ./...

test:
	go test -race ./...

lint:
	golangci-lint run ./...
