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
    -o /auth-bridge ./cmd/

# ── Runtime Stage ──
FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /auth-bridge /usr/local/bin/auth-bridge

EXPOSE 3300

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD wget -qO- http://localhost:3300/health || exit 1

ENTRYPOINT ["auth-bridge"]
