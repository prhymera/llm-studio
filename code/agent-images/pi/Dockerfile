# ─────────────────────────────────────────────────────────────
# pi.dev Agent Image
# Ephemeral container for AI coding assistant sessions
# ─────────────────────────────────────────────────────────────

FROM node:22-alpine

# Install build tools for native modules (node-pty etc.)
# and sudo for passwordless administration inside the container
RUN apk add --no-cache python3 make g++ sudo

# ── Create service user ──
# pi-agent owns all session processes, not root.
# Home is /home/pi-agent (bind-mounted from workspace volume at runtime).
RUN adduser -D -u 1100 -h /home/pi-agent pi-agent && \
    echo "pi-agent ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/pi-agent && \
    chmod 0440 /etc/sudoers.d/pi-agent

# Install pi.dev CLI (globally, available to all users)
RUN npm install -g --ignore-scripts @mariozechner/pi-coding-agent

# Install pi packages (memory-stone, observational-memory)
RUN npm install -g pi-memory-stone pi-observational-memory

# Pre-install pi-web.dev for one-click launch.
# Build-time install writes assets to /root; stash to /opt so entrypoint.sh
# can restore them to the workspace-backed /home/pi-agent at runtime.
RUN npm install -g @jmfederico/pi-web && \
    pi-web install 2>/dev/null; \
    if [ -d /root/.pi-web ]; then cp -a /root/.pi-web /opt/pi-web-assets; fi

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

WORKDIR /workspace
ENTRYPOINT ["/entrypoint.sh"]
