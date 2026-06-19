#!/bin/bash
# ─────────────────────────────────────────────────────────────
# picoclaw agent entrypoint
# Configures the environment and drops into an interactive shell.
# The container stays alive so the user can interact via the
# WebSocket terminal at any time.
# ─────────────────────────────────────────────────────────────
set -euo pipefail

# Write agent config
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
echo "  picoclaw agent session ${SESSION_ID}"
echo "═══════════════════════════════════════════"
echo "  Model:    ${LLM_MODEL}"
echo "  Endpoint: ${LLM_ENDPOINT}"
echo "  Workspace: ${WORKSPACE}"
echo ""
echo "  picoclaw is available in PATH."
echo "  Run it manually with your task description."
echo ""
echo "  Example:"
echo "    picoclaw --model ${LLM_MODEL} \\"
echo "      --project-root ${WORKSPACE} \\"
echo "      'Write a Python function to merge two sorted lists'"
echo ""
echo "═══════════════════════════════════════════"
echo ""

# Source workspace config if present
if [ -f /workspace/.agentrc ]; then
  source /workspace/.agentrc
fi

# Keep the container alive with an interactive bash shell
exec /bin/bash
