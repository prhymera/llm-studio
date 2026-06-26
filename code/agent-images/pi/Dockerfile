# ─────────────────────────────────────────────────────────────
# pi.dev Agent Image
# Ephemeral container for AI coding assistant sessions
# ─────────────────────────────────────────────────────────────

FROM node:20-alpine

# Install pi.dev CLI
RUN npm install -g --ignore-scripts @mariozechner/pi-coding-agent

# Install pi packages (memory-stone, observational-memory)
# These install into /usr/local/lib/node_modules/ (shared, not in tmpfs)
RUN npm install -g pi-memory-stone pi-observational-memory

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

WORKDIR /workspace
ENTRYPOINT ["/entrypoint.sh"]
