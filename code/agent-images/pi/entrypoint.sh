#!/bin/sh
# ─────────────────────────────────────────────────────────────
# pi.dev agent entrypoint
# Configures the environment and drops into an interactive shell.
# ─────────────────────────────────────────────────────────────
set +e
export HOME=/root

# ── Restore pi-web.dev assets from image layer ──
# /root is a workspace volume bind mount at runtime, so build-time files
# in /root/.pi-web are shadowed.  Copy them back from the image stash.
if [ -d /opt/pi-web-assets ] && [ ! -d /root/.pi-web ]; then
    cp -a /opt/pi-web-assets /root/.pi-web
fi

mkdir -p /workspace/.agent
cat > /workspace/.agent/config.json <<CONFIGEOF
{
  "llm_endpoint": "${LLM_ENDPOINT:-http://llm-gateway:3100/v1}",
  "llm_model": "${LLM_MODEL:-qwen25-coder-7b}",
  "llm_api_key": "${LLM_API_KEY:-local}",
  "user_id": "${USER_ID:-unknown}",
  "session_id": "${SESSION_ID:-unknown}",
  "workspace": "${WORKSPACE:-/workspace}"
}
CONFIGEOF

# Write pi config to workspace (rootfs may be read-only)
cat > /workspace/.agent/pi-config.json <<PICONFIG
{
  "models": {
    "default": "${LLM_MODEL:-qwen25-coder-7b}",
    "providers": {
      "openai": {
        "api_key": "${LLM_API_KEY:-local}",
        "base_url": "${LLM_ENDPOINT:-http://llm-gateway:3100/v1}"
      }
    }
  }
}
PICONFIG

echo "═══════════════════════════════════════════"
echo "  pi.dev agent session ${SESSION_ID}"
echo "═══════════════════════════════════════════"
echo "  Model:    ${LLM_MODEL}"
echo "  Endpoint: ${LLM_ENDPOINT}"
echo "  Workspace: ${WORKSPACE}"
echo ""
echo "  pi is available in PATH."
echo "  Run it manually:"
echo "    pi --model ${LLM_MODEL} --api-key ${LLM_API_KEY}"
echo ""
echo "═══════════════════════════════════════════"
echo ""

# ── Install pi packages (idempotent, runs on every container start) ──
# /root is on tmpfs, so we register packages each time the container starts
mkdir -p ~/.pi
pi install npm:pi-memory-stone 2>/dev/null
pi install npm:pi-observational-memory 2>/dev/null

echo "  Packages: pi-memory-stone + pi-observational-memory enabled"
echo ""

if [ -f /workspace/.agentrc ]; then
  . /workspace/.agentrc
fi

# Keep container alive with interactive shell
exec /bin/sh
