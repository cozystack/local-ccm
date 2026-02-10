# Build stage
FROM --platform=$BUILDPLATFORM golang:1.23 AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

# Copy go.mod and go.sum for dependency caching
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY cmd/ cmd/
COPY pkg/ pkg/

# Build both binaries
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build \
    -ldflags="-w -s" \
    -o local-ccm \
    ./cmd/local-ccm

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build \
    -ldflags="-w -s" \
    -o node-lifecycle-controller \
    ./cmd/node-lifecycle-controller

# Runtime stage
FROM alpine:3.19

RUN apk add --no-cache ca-certificates

COPY --from=builder /workspace/local-ccm /usr/local/bin/local-ccm
COPY --from=builder /workspace/node-lifecycle-controller /usr/local/bin/node-lifecycle-controller

USER 0

ENTRYPOINT ["/usr/local/bin/local-ccm"]
