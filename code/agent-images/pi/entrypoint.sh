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

# Write pi gateway extension (pi extensions are JS modules, not JSON configs).
# Routes ALL models through the LLM gateway — handles local + remote transparently.
cat > /root/pi-gateway-extension.js <<PIEXT
export default function(pi) {
  pi.registerProvider("gateway", {
    name: "LLM Gateway",
    baseUrl: "${LLM_ENDPOINT:-http://llm-gateway:3100/v1}",
    apiKey: "${LLM_API_KEY:-local}",
    api: "openai-completions",
    models: [
      { id: "qwen25-coder-7b", name: "Qwen 2.5 Coder 7B", reasoning: false, input: ["text"], cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, contextWindow: 32768, maxTokens: 4096, compat: { supportsDeveloperRole: false } },
      { id: "qwen3-coder-30b-a3b", name: "Qwen 3 Coder 30B", reasoning: false, input: ["text"], cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, contextWindow: 32768, maxTokens: 4096, compat: { supportsDeveloperRole: false } },
      { id: "deepseek-v4-pro", name: "DeepSeek V4 Pro", reasoning: true, input: ["text"], cost: { input: 2.0, output: 8.0, cacheRead: 0.5, cacheWrite: 2.0 }, contextWindow: 131072, maxTokens: 8192, compat: { supportsDeveloperRole: false } },
      { id: "deepseek-v4-flash", name: "DeepSeek V4 Flash", reasoning: false, input: ["text"], cost: { input: 0.27, output: 1.10, cacheRead: 0.07, cacheWrite: 0.27 }, contextWindow: 1048576, maxTokens: 8192, compat: { supportsDeveloperRole: false } },
      { id: "deepseek-reasoner", name: "DeepSeek Reasoner", reasoning: true, input: ["text"], cost: { input: 0.55, output: 2.19, cacheRead: 0.14, cacheWrite: 0.55 }, contextWindow: 131072, maxTokens: 8192, compat: { supportsDeveloperRole: false } }
    ]
  });
}
PIEXT

echo "═══════════════════════════════════════════"
echo "  pi.dev agent session ${SESSION_ID}"
echo "═══════════════════════════════════════════"
echo "  Model:    ${LLM_MODEL}"
echo "  Endpoint: ${LLM_ENDPOINT}"
echo "  Workspace: ${WORKSPACE}"
echo ""
echo "  pi is available in PATH."
echo "  Run it manually:"
echo "    pi --provider gateway --model ${LLM_MODEL} --api-key ${LLM_API_KEY} --extension /root/pi-gateway-extension.js"
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
