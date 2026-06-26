# ─────────────────────────────────────────────────────────────
# opencode Agent Image
# Ephemeral container for open-source coding agent sessions
# ─────────────────────────────────────────────────────────────

FROM node:20-alpine

RUN apk add --no-cache bash && \
    npm install -g opencode-ai

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

WORKDIR /workspace
ENTRYPOINT ["/entrypoint.sh"]
