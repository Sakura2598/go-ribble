# Stage 1: Build geth from source using Go
FROM golang:1.23-alpine AS builder

# Install required build dependencies
RUN apk add --no-cache gcc musl-dev linux-headers git

WORKDIR /go-ethereum

# Download Go module dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy full source code
COPY . .

# Build geth with static linking
RUN go run build/ci.go install -static ./cmd/geth

# Stage 2: Minimal runtime image
FROM alpine:latest

# Install runtime dependencies
RUN apk add --no-cache ca-certificates bash

# Copy geth binary
COPY --from=builder /go-ethereum/build/bin/geth /usr/local/bin/geth

# Copy entrypoint script
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /usr/local/bin/geth /entrypoint.sh

# Expose commonly used ports
EXPOSE 8545 8546 30303 30303/udp

# Default entrypoint
ENTRYPOINT ["/entrypoint.sh"]

# Optional metadata
ARG COMMIT=""
ARG VERSION=""
ARG BUILDNUM=""
LABEL commit="$COMMIT" version="$VERSION" buildnum="$BUILDNUM"
