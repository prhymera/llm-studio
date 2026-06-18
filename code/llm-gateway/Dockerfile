# ── Build Stage ──
FROM golang:1.26 AS builder

RUN apt-get update && apt-get install -y ca-certificates && rm -rf /var/lib/apt/lists/*

WORKDIR /src

# Cache Go dependencies
COPY go.mod go.sum ./
RUN go mod download

# Build llm-gateway
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X main.version=0.1.0" \
    -o /llm-gateway ./cmd/

# ── llama.cpp Build Stage ──
# Build llama.cpp from source so it links against the container's GLIBC
FROM ubuntu:24.04 AS llama-builder

RUN apt-get update && apt-get install -y \
    cmake \
    build-essential \
    curl \
    ca-certificates \
    git \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /build
RUN git clone --depth=1 https://github.com/ggml-org/llama.cpp.git .
RUN cmake -B build \
    -DCMAKE_BUILD_TYPE=Release \
    -DBUILD_SHARED_LIBS=ON \
    -DGGML_NATIVE=OFF \
    -DGGML_CPU_ALL_VARIANTS=OFF \
    -DGGML_BLAS=OFF \
    -DGGML_CUDA=OFF \
    -DGGML_METAL=OFF \
    -DGGML_VULKAN=OFF \
    -DGGML_SYCL=OFF
RUN cmake --build build --target llama-server -- -j$(nproc)

# ── Runtime Stage ──
FROM ubuntu:24.04

RUN apt-get update && apt-get install -y \
    ca-certificates \
    tzdata \
    libstdc++6 \
    libgomp1 \
    libssl3t64 \
    zlib1g \
    curl \
    && rm -rf /var/lib/apt/lists/*

# Create required directories
RUN mkdir -p /data/config /data/logs /models

# Copy Go binary
COPY --from=builder /llm-gateway /usr/local/bin/llm-gateway

# Copy llama.cpp build output (binary + .so libs)
COPY --from=llama-builder /build/build/bin/llama-server /llama/bin/llama-server
COPY --from=llama-builder /build/build/bin/libllama*.so* /llama/bin/
COPY --from=llama-builder /build/build/bin/libggml*.so* /llama/bin/
COPY --from=llama-builder /build/build/bin/libmtmd*.so* /llama/bin/

# Create symlinks for versioned .so files (llama-server expects .so.0)
RUN cd /llama/bin && for f in *.so; do ln -sf "$f" "${f}.0" 2>/dev/null || true; done

ENV LD_LIBRARY_PATH=/llama/bin

EXPOSE 3100

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD curl -sf http://localhost:3100/health || exit 1

ENTRYPOINT ["llm-gateway"]
