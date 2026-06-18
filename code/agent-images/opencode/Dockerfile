# ─────────────────────────────────────────────────────────────
# opencode Agent Image
# Ephemeral container for open-source coding agent sessions
# ─────────────────────────────────────────────────────────────

FROM python:3.12-slim

RUN pip install opencode 2>/dev/null || \
    echo "Warning: opencode not available, install manually"

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

WORKDIR /workspace
ENTRYPOINT ["/entrypoint.sh"]
