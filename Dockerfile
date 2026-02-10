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

# Build local-ccm
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build \
    -ldflags="-w -s" \
    -o local-ccm \
    ./cmd/local-ccm

# Build node-lifecycle-controller
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build \
    -ldflags="-w -s" \
    -o node-lifecycle-controller \
    ./cmd/node-lifecycle-controller

# local-ccm image
FROM alpine:3.19 AS local-ccm

RUN apk add --no-cache ca-certificates

COPY --from=builder /workspace/local-ccm /usr/local/bin/local-ccm

# local-ccm needs to run as root to access netlink
# which requires NET_ADMIN capability
USER root

ENTRYPOINT ["/usr/local/bin/local-ccm"]

# node-lifecycle-controller image
FROM scratch AS node-lifecycle-controller

# Copy CA certificates for HTTPS connections to Kubernetes API
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

COPY --from=builder /workspace/node-lifecycle-controller /node-lifecycle-controller

# Run as root for ICMP ping (requires CAP_NET_RAW)
USER 0

ENTRYPOINT ["/node-lifecycle-controller"]
