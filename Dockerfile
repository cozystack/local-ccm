# Multi-stage build for local-ccm

# Build stage
FROM golang:1.23 AS builder

WORKDIR /workspace

# Copy go.mod and go.sum for dependency caching
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY cmd/ cmd/
COPY pkg/ pkg/

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s" \
    -o local-ccm \
    ./cmd/local-ccm

# Runtime stage
FROM alpine:3.19

# Install required packages:
# - ca-certificates: for HTTPS connections to Kubernetes API
RUN apk add --no-cache ca-certificates

# Copy the binary from builder
COPY --from=builder /workspace/local-ccm /usr/local/bin/local-ccm

# local-ccm needs to run as root to access netlink
# which requires NET_ADMIN capability
USER root

ENTRYPOINT ["/usr/local/bin/local-ccm"]
