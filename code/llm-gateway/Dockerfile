# ── Build Stage ──
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /src

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Build with full source (including internal packages)
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X main.version=0.1.0" \
    -o /llm-gateway ./cmd/

# ── Runtime Stage ──
FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata

# Create required directories
RUN mkdir -p /data/config /data/logs /models

COPY --from=builder /llm-gateway /usr/local/bin/llm-gateway

EXPOSE 3100

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD wget -qO- http://localhost:3100/health || exit 1

ENTRYPOINT ["llm-gateway"]
