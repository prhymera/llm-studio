#!/bin/sh
# ─────────────────────────────────────────────────────────────
# pi.dev agent entrypoint
# Configures the environment and drops into an interactive shell.
# ─────────────────────────────────────────────────────────────
set -eu

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

if [ -f /workspace/.agentrc ]; then
  . /workspace/.agentrc
fi

# Keep container alive with interactive shell
exec /bin/sh
