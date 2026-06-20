#!/bin/bash
# ─────────────────────────────────────────────────────────────
# picoclaw agent entrypoint
# Configures the environment, launches picoclaw agent in
# interactive mode, then falls back to bash if it exits.
# ─────────────────────────────────────────────────────────────
set +eu

# Resolve LLM endpoint and model
LLM_ENDPOINT="${LLM_ENDPOINT:-http://llm-gateway:3100/v1}"
LLM_MODEL="${LLM_MODEL:-qwen25-coder-7b}"
LLM_API_KEY="${LLM_API_KEY:-local}"

# Determine provider type from endpoint
if echo "$LLM_ENDPOINT" | grep -qi "deepseek"; then
  PROVIDER_TYPE="deepseek"
elif echo "$LLM_ENDPOINT" | grep -qi "openai\|gateway\|localhost\|10\."; then
  PROVIDER_TYPE="openai"
elif echo "$LLM_ENDPOINT" | grep -qi "anthropic"; then
  PROVIDER_TYPE="anthropic"
else
  PROVIDER_TYPE="openai"
fi

# Write workspace agent config
mkdir -p /workspace/.agent
cat > /workspace/.agent/config.json <<CONFIGEOF
{
  "llm_endpoint": "${LLM_ENDPOINT}",
  "llm_model": "${LLM_MODEL}",
  "llm_api_key": "${LLM_API_KEY}",
  "user_id": "${USER_ID:-unknown}",
  "session_id": "${SESSION_ID:-unknown}",
  "workspace": "${WORKSPACE:-/workspace}"
}
CONFIGEOF

# Write picoclaw config in the PROPER format (matching Aetherflow schema)
# with agents.defaults.model_name set so picoclaw knows which model to use
mkdir -p /root/.picoclaw
cat > /root/.picoclaw/config.json <<PICOCONFIG
{
  "version": 3,
  "agents": {
    "defaults": {
      "provider": "${PROVIDER_TYPE}",
      "model_name": "${LLM_MODEL}",
      "workspace": "/workspace",
      "restrict_to_workspace": true,
      "allow_read_outside_workspace": false,
      "max_tokens": 32768,
      "context_window": 131072,
      "temperature": 0.7,
      "max_tool_iterations": 200,
      "summarize_message_threshold": 20,
      "summarize_token_percent": 75,
      "steering_mode": "one-at-a-time",
      "max_llm_retries": 3,
      "llm_retry_backoff_secs": 2,
      "subturn": {
        "max_depth": 0,
        "max_concurrent": 0,
        "default_timeout_minutes": 0,
        "default_token_budget": 0,
        "concurrency_timeout_sec": 0
      },
      "tool_feedback": {
        "enabled": false,
        "max_args_length": 300,
        "separate_messages": false
      },
      "split_on_marker": false
    }
  },
  "model_list": [
    {
      "provider": "${PROVIDER_TYPE}",
      "model_name": "${LLM_MODEL}",
      "model": "${LLM_MODEL}",
      "api_key": "${LLM_API_KEY}",
      "api_base": "${LLM_ENDPOINT}",
      "enabled": true
    }
  ],
  "session": {
    "dimensions": ["chat"]
  },
  "channel_list": {},
  "tools": {
    "exec": {
      "enabled": true,
      "enable_deny_patterns": true,
      "allow_remote": true,
      "timeout_seconds": 60
    },
    "write_file": {"enabled": true},
    "read_file": {"enabled": true, "mode": "bytes", "max_read_file_size": 65536},
    "list_dir": {"enabled": true},
    "web_fetch": {"enabled": true},
    "skills": {
      "enabled": true,
      "registries": {
        "clawhub": {"base_url": "https://clawhub.ai", "enabled": true},
        "github": {"base_url": "https://github.com", "enabled": true}
      }
    }
  },
  "events": {
    "logging": {
      "enabled": true,
      "include": ["agent.*"],
      "min_severity": "info"
    }
  },
  "heartbeat": {
    "enabled": true,
    "interval": 30
  }
}
PICOCONFIG

echo "═══════════════════════════════════════════"
echo "  picoclaw agent session ${SESSION_ID:-unknown}"
echo "═══════════════════════════════════════════"
echo "  Model:    ${LLM_MODEL}"
echo "  Provider: ${PROVIDER_TYPE}"
echo "  Endpoint: ${LLM_ENDPOINT}"
echo "  Workspace: ${WORKSPACE:-/workspace}"
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
  picoclaw agent --model "${LLM_MODEL}" --message "${TASK}" 2>&1
else
  picoclaw agent --model "${LLM_MODEL}" 2>&1
fi

echo ""
echo "picoclaw agent session ended. Container stays alive for debugging."
echo ""

exec /bin/bash
