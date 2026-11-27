# Build the manager binary.
# The controller module imports the gateway module (`replace gateway => ../`)
# for the gateway's config rules, so build from the repository root:
#   docker build -f controller/Dockerfile .
# Controller/Dockerfile.dockerignore limits the context to what the build needs.
# Override BASE_IMAGE to build from another registry, e.g. docker.io/library/golang:1.27
ARG BASE_IMAGE=golang:1.27
FROM ${BASE_IMAGE} AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
# Copy the Go Modules manifests of both modules and cache dependencies before
# copying source, so source changes don't invalidate the downloaded layer.
COPY go.mod go.sum ./
COPY controller/go.mod controller/go.sum controller/
WORKDIR /workspace/controller
RUN go mod download

# Copy the Go source (relies on Dockerfile.dockerignore to filter)
WORKDIR /workspace
COPY . .

# Build
# the GOARCH has no default value to allow the binary to be built according to the host where the command
# was called. For example, if we call make docker-build in a local env which has the Apple Silicon M1 SO
# the docker BUILDPLATFORM arg will be linux/arm64 when for Apple x86 it will be linux/amd64. Therefore,
# by leaving it empty we can ensure that the container and binary shipped on it will have the same platform.
WORKDIR /workspace/controller
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o manager cmd/main.go

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/controller/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
