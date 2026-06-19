#!/bin/bash
# ─────────────────────────────────────────────────────────────
# picoclaw agent entrypoint
# Configures the environment, launches picoclaw agent in
# interactive mode, then falls back to bash if it exits.
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
echo "═══════════════════════════════════════════"
echo ""

# Source workspace config if present
if [ -f /workspace/.agentrc ]; then
  source /workspace/.agentrc
fi

# Launch picoclaw agent in interactive mode.
# If TASK env var is set, send it as the first message.
# If picoclaw exits, container stays alive via bash for debugging.
if [ -n "${TASK:-}" ]; then
  echo "> Task: ${TASK}"
  picoclaw agent --model "${LLM_MODEL:-qwen25-coder-7b}" --message "${TASK}" 2>&1
else
  picoclaw agent --model "${LLM_MODEL:-qwen25-coder-7b}" 2>&1
fi

echo ""
echo "picoclaw agent session ended. Container stays alive for debugging."
echo "Run 'picoclaw agent' to start a new session."
echo ""

# Keep container alive so user can reconnect / debug
exec /bin/bash
