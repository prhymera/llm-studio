#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────
# LLM Studio — Standalone Build & Deploy Script
# ─────────────────────────────────────────────────────────────
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

echo "🔨 LLM Studio — Build & Deploy"
echo "═══════════════════════════════"
echo ""

# ── Check prerequisites ──
echo "📋 Checking prerequisites..."
command -v docker >/dev/null 2>&1 || { echo "❌ docker is required"; exit 1; }
echo "   ✅ docker found: $(docker --version)"
echo ""

# ── Build images ──
echo "🏗️  Building service images..."

echo "   Building llm-gateway..."
docker build -t llm-studio-gateway:latest ./code/llm-gateway

echo "   Building agent-runner..."
docker build -t llm-studio-agent-runner:latest ./code/agent-runner

echo "   Building auth-bridge..."
docker build -t llm-studio-auth-bridge:latest ./code/auth-bridge

echo ""

# ── Verify images ──
echo "✅ Images built:"
docker images --format "table {{.Repository}}:{{.Tag}}\t{{.Size}}" | grep llm-studio
echo ""

# ── Setup .env ──
if [ ! -f .env ]; then
    echo "📝 Creating .env from .env.example..."
    cp .env.example .env
    echo "   ⚠️  Edit .env with your configuration before deploying"
fi

# ── Check models directory ──
MODELS_DIR="${LLAMA_MODELS_DIR:-$HOME/.local/share/llama-lab/models}"
if [ -d "$MODELS_DIR" ]; then
    MODEL_COUNT=$(find "$MODELS_DIR" -name "*.gguf" 2>/dev/null | wc -l)
    echo "📦 Models found: $MODEL_COUNT in $MODELS_DIR"
else
    echo "⚠️  Models directory not found: $MODELS_DIR"
    echo "   Run ./llama-lab/llama-lab.sh install <model> to download models"
fi

echo ""
echo "🚀 Deploy with: docker compose up -d"
echo "   View logs:  docker compose logs -f"
echo "   Stop:       docker compose down"
echo ""
echo "   Portal:     http://localhost:3000  (Open WebUI)"
echo "   API:        http://localhost:3100  (llm-gateway)"
echo "   Agents API: http://localhost:3200  (agent-runner)"
