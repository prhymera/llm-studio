#!/bin/sh
# ─────────────────────────────────────────────────────────────
# pi.dev agent entrypoint
# ─────────────────────────────────────────────────────────────
set -euo

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

echo "=== pi.dev agent session ${SESSION_ID} ==="
echo "Model: ${LLM_MODEL}"
echo "Endpoint: ${LLM_ENDPOINT}"
echo "Workspace: ${WORKSPACE}"
echo ""

# Restore workspace state
if [ -f /workspace/.agentrc ]; then
  . /workspace/.agentrc
fi

# If pi is installed, launch it; otherwise exec a shell
if command -v pi >/dev/null 2>&1; then
  exec pi --model "${LLM_MODEL}" --api-key "${LLM_API_KEY}" \
    --provider "$(echo ${LLM_ENDPOINT} | sed 's|http://||;s|:.*||')" \
    --system-prompt "You are a coding assistant in workspace ${WORKSPACE}." \
    "$@"
else
  echo "pi is not installed. Starting shell."
  exec /bin/sh
fi
