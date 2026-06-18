# ─────────────────────────────────────────────────────────────
# pi.dev Agent Image
# Ephemeral container for AI coding assistant sessions
# ─────────────────────────────────────────────────────────────

FROM node:20-alpine

RUN npm install -g @anthropic-ai/pi 2>/dev/null || \
    echo "Warning: pi package not available, install manually"

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

WORKDIR /workspace
ENTRYPOINT ["/entrypoint.sh"]
