#!/bin/sh
# ─────────────────────────────────────────────────────────────
# pi.dev agent entrypoint
# Runs as root to configure environment, then drops to pi-agent.
# ─────────────────────────────────────────────────────────────
set +e

# ── Early: set home for the service user ──
PI_HOME=/home/pi-agent
export HOME="$PI_HOME"

# ── Restore pi-web.dev assets from image layer ──
# /home/pi-agent is a workspace volume bind mount at runtime, so build-time
# files in that path are shadowed. Copy them back from /opt.
mkdir -p "$PI_HOME"
if [ -d /opt/pi-web-assets ] && [ ! -d "$PI_HOME/.pi-web" ]; then
    cp -a /opt/pi-web-assets "$PI_HOME/.pi-web"
    chown -R pi-agent:pi-agent "$PI_HOME"
fi

# ── Workspace config ──
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

# ── pi gateway extension (auto-loaded from ~/.pi/extensions/) ──
# pi and pi-web sessiond both auto-load extensions from this directory.
# Routes ALL models through the LLM gateway — handles local + remote transparently.
mkdir -p "$PI_HOME/.pi/extensions"
cat > "$PI_HOME/.pi/extensions/gateway.js" <<PIEXT
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

# ── pi agent settings (global defaults for pi and pi-web sessiond) ──
mkdir -p "$PI_HOME/.pi/agent"
cat > "$PI_HOME/.pi/agent/settings.json" <<SETEOF
{
  "defaultProvider": "gateway",
  "defaultModel": "${LLM_MODEL:-deepseek-v4-pro}",
  "defaultThinkingLevel": "high"
}
SETEOF

# ── pi-web config (sessiond + server state) ──
mkdir -p "$PI_HOME/.config/pi-web"
cat > "$PI_HOME/.config/pi-web/config.json" <<PWEBCFG
{
  "host": "0.0.0.0",
  "port": 8504,
  "spawnSessions": true
}
PWEBCFG

# ── Ensure ownership ──
chown -R pi-agent:pi-agent "$PI_HOME"

echo "═══════════════════════════════════════════"
echo "  pi.dev agent session ${SESSION_ID}"
echo "═══════════════════════════════════════════"
echo "  Model:    ${LLM_MODEL}"
echo "  Endpoint: ${LLM_ENDPOINT}"
echo "  User:     pi-agent (sudo: passwordless)"
echo "  Workspace: ${WORKSPACE}"
echo ""
echo "  pi is available in PATH."
echo "  Gateway extension auto-loaded from:"
echo "    ~/.pi/extensions/gateway.js"
echo ""
echo "═══════════════════════════════════════════"
echo ""

# ── Install extension for pi-agent (for pi-web sessiond auto-discovery) ──
su - pi-agent -c "HOME=/home/pi-agent pi install /home/pi-agent/.pi/extensions/gateway.js 2>/dev/null" 2>/dev/null || true

# ── Install pi packages (idempotent) ──
su - pi-agent -c "pi install npm:pi-memory-stone 2>/dev/null" 2>/dev/null
su - pi-agent -c "pi install npm:pi-observational-memory 2>/dev/null" 2>/dev/null

echo "  Packages: pi-memory-stone + pi-observational-memory enabled"
echo ""

if [ -f /workspace/.agentrc ]; then
  . /workspace/.agentrc
fi

# ── Drop privileges: run as pi-agent ──
exec su - pi-agent -c /bin/sh
