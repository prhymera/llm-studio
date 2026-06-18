# ── Build Stage ──
FROM golang:1.22-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /llm-gateway ./cmd/

# ── Runtime Stage ──
FROM alpine:3.19

RUN apk add --no-cache ca-certificates

COPY --from=builder /llm-gateway /usr/local/bin/llm-gateway

EXPOSE 3100

ENTRYPOINT ["llm-gateway"]
