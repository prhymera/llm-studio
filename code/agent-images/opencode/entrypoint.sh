#!/bin/sh
# ─────────────────────────────────────────────────────────────
# opencode agent entrypoint
# ─────────────────────────────────────────────────────────────
set -euo

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

echo "=== opencode agent session ${SESSION_ID} ==="
echo "Model: ${LLM_MODEL}"
echo "Endpoint: ${LLM_ENDPOINT}"
echo "Workspace: ${WORKSPACE}"
echo ""

if [ -f /workspace/.agentrc ]; then
  . /workspace/.agentrc
fi

if command -v opencode >/dev/null 2>&1; then
  exec opencode --model "${LLM_MODEL}" --api-base "${LLM_ENDPOINT}" "$@"
else
  echo "opencode is not installed. Starting shell."
  exec /bin/sh
fi
