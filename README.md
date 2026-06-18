# 🧠 LLM Studio

**Unified LLM interface for the Frao Technologies enterprise portal** — chat with DeepSeek, Gemini, ChatGPT, and local models (Qwen, Gemma, GLM) through a single, integrated studio.

## Architecture

Two-component system:

1. **Open WebUI** — Feature-rich chat frontend (RAG, prompt libraries, multi-modal, history)
2. **llm-gateway** — Thin Go service managing local model lifecycle (start/stop/health llama.cpp servers)

See [ARCHITECTURE.md](./ARCHITECTURE.md) for the full analysis and ranking of alternatives.

### Quick Overview

```
                    ┌──────────────────────┐
                    │  Frao Portal         │
                    │  (SvelteKit + Nginx)  │
                    └──────┬───────────────┘
                           │
              ┌────────────┴────────────┐
              │                         │
              ▼                         ▼
    ┌──────────────────┐    ┌──────────────────────┐
    │  Open WebUI      │    │  llm-gateway (Go)    │
    │  · Chat UI       │    │  · Model lifecycle   │
    │  · RAG           │    │  · llama.cpp wrapper │
    │  · History       │    │  · /v1 API proxy     │
    │  · Prompt lib    │    └──────────┬───────────┘
    └──────┬───────────┘               │
           │                     ┌─────┴──────┐
           │                     │  llama.cpp  │
           │                     │  server     │
           │                     └────────────┘
           │
    ┌──────┴───────────┐
    │  Remote LLMs     │
    │  DeepSeek,       │
    │  Gemini, ChatGPT │
    └──────────────────┘
```

## Status

📄 **Architecture analysis complete** — see [ARCHITECTURE.md](./ARCHITECTURE.md) for the full comparison of 4 alternatives.

🔄 **Implementation pending** — Phase 1 (llm-gateway, Docker integration) is the next step.

## Why Open WebUI + llm-gateway?

| Criterion | Score |
|-----------|-------|
| Feature completeness | ⭐⭐⭐⭐⭐ |
| Development effort | ~1 week |
| Custom code | ~500 lines |
| Resource usage (idle) | ~250MB |
| RAG / Tools / Multi-modal | Built-in |
| Model lifecycle control | Full (via gateway) |

## Model Support

- **Remote:** DeepSeek, Gemini, ChatGPT (any OpenAI-compatible)
- **Local:** Qwen3-Coder, Qwen2.5-Coder, Gemma 3, GLM-4, DeepSeek-Coder (via llama.cpp)
- **Server spec:** 12-core EPYC, 23GB RAM — comfortably runs 7B-14B models, with MoE models like qwen3-coder-30b-a3b fitting efficiently
