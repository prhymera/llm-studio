#!/bin/bash
# ─────────────────────────────────────────────────────────────
# picoclaw agent entrypoint
# Injects configuration from environment variables
# and launches the agent.
# ─────────────────────────────────────────────────────────────
set -euo pipefail

# Write agent config
mkdir -p /etc/agent
cat > /etc/agent/config.json <<CONFIGEOF
{
  "llm_endpoint": "${LLM_ENDPOINT:-http://llm-gateway:3100/v1}",
  "llm_model": "${LLM_MODEL:-qwen25-coder-7b}",
  "llm_api_key": "${LLM_API_KEY:-local}",
  "user_id": "${USER_ID:-unknown}",
  "session_id": "${SESSION_ID:-unknown}",
  "workspace": "${WORKSPACE:-/workspace}"
}
CONFIGEOF

echo "=== picoclaw agent session ${SESSION_ID} ==="
echo "Model: ${LLM_MODEL}"
echo "Endpoint: ${LLM_ENDPOINT}"
echo "Workspace: ${WORKSPACE}"
echo ""

# Restore .agentrc if it exists
if [ -f /workspace/.agentrc ]; then
  source /workspace/.agentrc
  echo "Restored workspace configuration."
fi

# Launch picoclaw with project context
exec picoclaw --model "${LLM_MODEL}" --provider "$(echo ${LLM_ENDPOINT} | sed 's|http://||;s|:.*||')" \
  --api-key "${LLM_API_KEY}" \
  --project-root "${WORKSPACE}" \
  "$@"
