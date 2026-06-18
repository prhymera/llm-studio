# ── Build Stage ──
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /src

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Build
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w" \
    -o /agent-runner ./cmd/

# ── Runtime Stage ──
FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata docker-cli

# Create required directories
RUN mkdir -p /data/sessions /data/workspaces /data/logs

COPY --from=builder /agent-runner /usr/local/bin/agent-runner

EXPOSE 3200

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD wget -qO- http://localhost:3200/health || exit 1

ENTRYPOINT ["agent-runner"]
