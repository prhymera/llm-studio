# ─────────────────────────────────────────────────────────────
# pi.dev Agent Image
# Ephemeral container for AI coding assistant sessions
# ─────────────────────────────────────────────────────────────

FROM node:22-alpine

# Install build tools for native modules (node-pty etc.)
RUN apk add --no-cache python3 make g++

# Install pi.dev CLI
RUN npm install -g --ignore-scripts @mariozechner/pi-coding-agent

# Install pi packages (memory-stone, observational-memory)
# These install into /usr/local/lib/node_modules/ (shared, not in tmpfs)
RUN npm install -g pi-memory-stone pi-observational-memory

# Pre-install pi-web.dev for one-click launch
# Assets install to /root/.pi-web during build; entrypoint.sh copies them to
# the workspace-backed /root at runtime (since /root is a bind mount that
# shadows the image layer).
# pi-web.dev: globally install the server binary + web UI assets.
# pi-web install writes config/assets to ~/.pi-web;
# stash them to /opt so entrypoint.sh can restore them to the
# workspace-backed /root mount at runtime.
RUN npm install -g @jmfederico/pi-web && \
    pi-web install 2>/dev/null; \
    if [ -d /root/.pi-web ]; then cp -a /root/.pi-web /opt/pi-web-assets; fi

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

WORKDIR /workspace
ENTRYPOINT ["/entrypoint.sh"]
