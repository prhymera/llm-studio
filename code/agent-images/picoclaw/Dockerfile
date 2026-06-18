# ─────────────────────────────────────────────────────────────
# picoclaw Agent Image
# Ephemeral container for autonomous coding agent sessions
# ─────────────────────────────────────────────────────────────

FROM debian:bookworm-slim

# Install dependencies (picoclaw needs libolm3 + libssl3)
RUN apt-get update && apt-get install -y \
    ca-certificates \
    libssl3 \
    libolm3 \
    curl \
    git \
    bash \
    && rm -rf /var/lib/apt/lists/*

# Copy picoclaw binary
COPY picoclaw-linux-amd64 /usr/local/bin/picoclaw
RUN chmod +x /usr/local/bin/picoclaw

# Copy entrypoint
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

WORKDIR /workspace
ENTRYPOINT ["/entrypoint.sh"]
