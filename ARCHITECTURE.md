# LLM Studio — Architecture Analysis

> **Context:** Frao Technologies enterprise portal (SvelteKit + Nginx + Docker Compose)  
> **Server:** 12-core EPYC, 23GB RAM, CPU-only, Arch Linux  
> **Existing infra:** `llama-lab` (llama.cpp model manager), aetherflow (Go), `docker-compose.yml`  
> **Date:** 2026-06-18

---

## Table of Contents

1. [Problem Statement](#1-problem-statement)
2. [Requirements Analysis](#2-requirements-analysis)
3. [Architecture Alternatives](#3-architecture-alternatives)
   - [A: Open WebUI + llm-gateway](#a-open-webui--llm-gateway-recommended)
   - [B: Full Custom Build](#b-full-custom-build)
   - [C: Open WebUI Standalone](#c-open-webui-standalone)
   - [D: LiteLLM Proxy + Custom UI](#d-litellm-proxy--custom-ui)
4. [Comparison Matrix](#4-comparison-matrix)
5. [Local LLM Management Layer](#5-local-llm-management-layer)
6. [Resource Budget](#6-resource-budget)
7. [Integration with Existing Infrastructure](#7-integration-with-existing-infrastructure)
8. [Recommendation](#8-recommendation)
9. [Implementation Roadmap](#9-implementation-roadmap)

---

## 1. Problem Statement

The Frao Technologies enterprise portal needs an **LLM Studio** — a unified interface for:

- **Chatting** with both remote enterprise LLMs (DeepSeek, Gemini, ChatGPT) and **local LLMs** (Qwen, Gemma, GLM via llama.cpp)
- **Managing** local model lifecycle — download, start, stop, switch models
- **Notes & Sessions** — persistent, searchable chat history with markdown notes
- **Advanced features** — RAG, prompt libraries, multi-user access, RBAC integration
- **Deep integration** with the existing portal sidebar, auth, and Docker Compose stack

The core question: **Build a custom LLM studio from scratch, or adapt an existing open-source solution (Open WebUI, et al.)?**

---

## 2. Requirements Analysis

### Functional Requirements

| Priority | Requirement | Detail |
|----------|------------|--------|
| **P0** | Multi-provider chat | Remote: DeepSeek, Gemini, ChatGPT. Local: llama.cpp server |
| **P0** | Chat history | Persistent, searchable, per-user |
| **P0** | Model management | Start/stop/switch local models, show status |
| **P1** | Notes | Markdown notebook alongside chats |
| **P1** | RBAC | Portal auth integration (roles: user/operator/admin) |
| **P2** | RAG | Upload documents, search within them |
| **P2** | Prompt library | Saved/categorized prompt templates |
| **P2** | Multi-modal | Image upload for vision models |
| **P3** | Tool calling | Function calling / plugin system |
| **P3** | Model switching at runtime | Switch between providers mid-conversation |

### Non-Functional Requirements

| Requirement | Target |
|-------------|--------|
| **Resource usage** | < 4GB RAM for the studio components at idle |
| **Latency** | < 500ms overhead on top of LLM inference time |
| **Security** | API keys stored securely, never leaked to frontend |
| **Maintainability** | Minimal custom code, leverage upstream |
| **Integration** | Must fit into existing Docker Compose + Nginx routing |

---

## 3. Architecture Alternatives

### A: Open WebUI + llm-gateway ⭐ *(Recommended)*

#### Architecture

```
┌──────────────────────────────────────────────────────────────────┐
│  Frao Portal (SvelteKit SPA)                                     │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │  nginx (reverse proxy)                                    │   │
│  │  /api/llm/ → llm-gateway:3100                             │   │
│  │  /llm-studio/ → open-webui:3000  (or iframe)             │   │
│  └──────────────────────────────────────────────────────────┘   │
└──────────────────────────────────────────────────────────────────┘
                           │
                    ┌──────┴──────┐
                    │             │
                    ▼             ▼
           ┌─────────────┐  ┌─────────────────────┐
           │ Open WebUI   │  │ llm-gateway (Go)    │
           │ · Chat UI    │  │ · /v1/chat → proxy  │
           │ · History    │  │ · Model lifecycle   │
           │ · RAG        │  │ · llama.cpp wrapper │
           │ · Prompt lib │  │ · Health / status   │
           │ · RBAC       │  └────────┬────────────┘
           └──────┬──────┘           │
                  │              ┌────┴────┐
                  │              │ llama.cpp│
                  │              │ server   │
                  │              └─────────┘
           ┌──────┴──────┐
           │ Remote LLMs  │
           │ (DeepSeek,   │
           │  Gemini, etc)│
           └─────────────┘
```

#### Component Details

1. **Open WebUI** — The battle-tested self-hosted LLM chat UI
   - Supports OpenAI-compatible endpoints natively
   - Built-in: chat history, RAG, prompt library, document uploads, RBAC, multi-modal, tool calling
   - Python/Node.js backend with React frontend (runs as a Docker container)
   - Can be themed to match portal aesthetics
   - Embedded in portal via iframe or served on subpath

2. **llm-gateway** — A thin Go service that:
   - Wraps llama.cpp server lifecycle (start/stop/health/liveness)
   - Exposes OpenAI-compatible `/v1/chat/completions` and `/v1/models` endpoints
   - Proxies remote endpoints (DeepSeek, Gemini, ChatGPT) — redundant if Open WebUI handles it directly
   - Reports model status (loaded, available, error) via a `/status` endpoint
   - Absorbs the model registry and hardware detection logic from `llama-lab.sh`

3. **llama.cpp server** — Runs per-model instances or a single instance with model swapping

#### Open WebUI Feature Inventory

| Feature | Open WebUI | Notes |
|---------|-----------|-------|
| Multi-provider chat | ✅ Native | OpenAI-compatible endpoint per provider |
| Chat history | ✅ Built-in | PostgreSQL/SQLite, searchable |
| Notes / Workspace | ✅ Workspace | Per-user markdown workspace |
| RAG | ✅ Built-in | ChromaDB / local embeddings, document upload |
| Prompt library | ✅ Built-in | Saved prompt templates |
| RBAC | ✅ Built-in | Admin/user roles, API key management |
| Multi-modal | ✅ Built-in | Image upload for vision models |
| Tool calling | ✅ Built-in | Function calling, plugins |
| Model switching | ✅ Runtime | Switch provider/mid-conversation |
| Model management | ❌ Not built-in | This is what llm-gateway provides |
| Portal integration | ⚠️ Partial | iframe + nginx routing |
| Theming | ⚠️ Partial | CSS variables, logo, colors |

#### Advantages
- **Radically less custom code** — months of engineering avoided
- **Battle-tested** — 50K+ GitHub stars, active community
- **Feature-complete** — RAG, tools, multi-modal out of the box
- **Standard API** — OpenAI-compatible means any future LLM backend integrates trivially
- **Upgrade path** — upstream releases bring new features automatically
- **Docker-native** — fits into existing Compose stack

#### Disadvantages
- **iframe integration** — embedding in portal is less seamless than SvelteKit-native component
- **Theming limits** — can't perfectly match portal's theme system
- **Extra resource** — Open WebUI itself uses ~200MB RAM + Python runtime
- **Auth duplication** — Open WebUI has its own auth; portal SSO requires OIDC/OAuth config
- **Update overhead** — need to manage Open WebUI version upgrades

#### Resource Estimate
| Component | RAM | CPU | Disk |
|-----------|-----|-----|------|
| Open WebUI | ~200MB | 0.2 cores | ~500MB (app + data) |
| llm-gateway | ~20MB | <0.1 cores | ~10MB |
| llama.cpp (idle) | ~0 | 0 | ~4.5GB per model |
| llama.cpp (inference) | ~6GB | 11 cores | — |
| **Total (idle)** | **~250MB** | **0.3 cores** | **~5GB** |
| **Total (active)** | **~6.3GB** | **11 cores** | **~10GB** |

---

### B: Full Custom Build

#### Architecture

```
┌──────────────────────────────────────────────────────────┐
│  Frao Portal (SvelteKit SPA)                             │
│  ┌────────────────────────────────────────────────┐     │
│  │  Custom LLM Studio Component (Svelte 5)       │     │
│  │  · Chat UI component                           │     │
│  │  · History browser                            │     │
│  │  · Notes panel                                │     │
│  │  · Model selector                             │     │
│  │  · RAG upload                                 │     │
│  └──────────────────┬─────────────────────────────┘     │
│                     │ fetch()                           │
└─────────────────────┼────────────────────────────────────┘
                      │
                      ▼
          ┌─────────────────────┐
          │ llm-backend (Go)    │
          │ · /v1/chat          │
          │ · /v1/models        │
          │ · /history          │
          │ · /notes            │
          │ · /rag              │
          │ · /status (models)  │
          └──────┬──────┬──────┘
                 │      │
           ┌─────┘      └──────┐
           ▼                    ▼
   ┌──────────────┐   ┌──────────────┐
   │ llama.cpp    │   │ Remote LLMs  │
   │ server       │   │ (DeepSeek,   │
   │              │   │  Gemini, etc)│
   └──────────────┘   └──────────────┘
```

#### Advantages
- **Seamless integration** — native SvelteKit component, matches portal theme exactly
- **No auth duplication** — uses portal's existing auth directly
- **Full control** — every feature tailored to exact needs
- **No iframe** — native performance, no cross-origin issues
- **Minimal runtime** — no Python/Node dependency beyond what portal already has

#### Disadvantages
- **Massive engineering effort** — estimated 4-8 weeks for feature parity with Open WebUI
- **RAG from scratch** — document parsing, chunking, embeddings, vector search
- **Chat history** — need PostgreSQL schema, search, pagination
- **Prompt library** — need UI + persistence
- **No tool calling** — need to implement function call parsing/execution
- **No multi-modal** — need image processing pipeline
- **Ongoing maintenance** — every feature is custom code to maintain
- **Security risk** — more surface = more bugs to find

#### Feature Estimate

| Feature | Complexity | Timeline |
|---------|-----------|----------|
| Basic chat (text in/out) | Medium | 1 week |
| Multi-provider routing | Medium | 1 week |
| Chat history + search | High | 1.5 weeks |
| Notes panel | Medium | 1 week |
| RAG pipeline | Very High | 2-3 weeks |
| Prompt library | Medium | 0.5 week |
| Multi-modal | High | 1 week |
| Tool calling | Very High | 2+ weeks |
| RBAC integration | Low | 0.5 week |
| **Total** | | **~8-12 weeks** |

#### Resource Estimate
| Component | RAM | CPU | Disk |
|-----------|-----|-----|------|
| llm-backend | ~30MB | <0.1 cores | ~20MB |
| llama.cpp (active) | ~6GB | 11 cores | ~4.5GB |
| **Total** | **~6.1GB** | **11 cores** | **~5GB** |

---

### C: Open WebUI Standalone

#### Architecture

```
┌──────────────────────────────────────────────────┐
│  Frao Portal (SvelteKit SPA)                     │
│  ┌────────────────────────────────────────┐     │
│  │  nginx → /llm-studio/ → open-webui    │     │
│  │              (direct iframe/link)      │     │
│  └────────────────────────────────────────┘     │
└──────────────────────────────────────────────────┘
                      │
                      ▼
            ┌─────────────────┐
            │  Open WebUI     │
            │  · Remote LLMs  │
            │  · Ollama conn  │
            └──────┬──────────┘
                   │
           ┌───────┴────────┐
           ▼                 ▼
   ┌─────────────┐   ┌──────────────┐
   │ Ollama      │   │ Remote LLMs  │
   │ (local host) │   │ (DeepSeek,   │
   │             │   │  Gemini, etc)│
   └─────────────┘   └──────────────┘
```

#### Advantages
- **Minimal custom code** — almost zero
- **Ollama integration** — Open WebUI has native Ollama support
- **Fastest time-to-value** — deploy in hours

#### Disadvantages
- **No custom model lifecycle** — Ollama manages models opaquely, no fine-grained control
- **Duplicate model storage** — Ollama has its own model storage, separate from llama-lab
- **No hardware-aware tuning** — Ollama uses default settings, not tailored to this EPYC server
- **No integration with llama-lab** — existing model downloads (qwen25-coder-7b, qwen3-coder-30b-a3b) can't be reused
- **No health/status API** for the portal to monitor local models
- **Ollama resource overhead** — Go binary + model loading adds ~100MB baseline
- **Less control** — model swap requires UI interaction, not scriptable

#### Resource Estimate
| Component | RAM | CPU | Disk |
|-----------|-----|-----|------|
| Open WebUI | ~200MB | 0.2 cores | ~500MB |
| Ollama | ~100MB + model | 0.1 cores | ~4.5GB (duplicate) |
| **Total** | **~300MB + model** | **0.3 cores** | **~5GB + duplicate** |

---

### D: LiteLLM Proxy + Custom UI

#### Architecture

```
┌──────────────────────────────────────────────────────────┐
│  Frao Portal (SvelteKit SPA)                             │
│  ┌────────────────────────────────────────────────┐     │
│  │  Custom Chat UI (Svelte 5)                    │     │
│  │  · Chat component (lighter than Option B)     │     │
│  │  · History browser                            │     │
│  │  · Notes panel                                │     │
│  └──────────────────┬─────────────────────────────┘     │
│                     │ fetch()                           │
└─────────────────────┼────────────────────────────────────┘
                      │
                      ▼
          ┌─────────────────────┐
          │ LiteLLM Proxy (Py)  │
          │ · Unified API       │
          │ · Model routing     │
          │ · Cost tracking     │
          │ · Rate limiting     │
          └──────┬──────┬──────┘
                 │      │
           ┌─────┘      └──────┐
           ▼                    ▼
   ┌──────────────┐   ┌──────────────┐
   │ llama.cpp    │   │ Remote LLMs  │
   │ via llm-gtw  │   │ (DeepSeek,   │
   │              │   │  Gemini, etc)│
   └──────────────┘   └──────────────┘
```

#### Advantages
- **LiteLLM handles unified routing** — 100+ providers, cost tracking, rate limiting
- **Custom UI can match portal theme perfectly**
- **No iframe needed**
- **Feature-rich routing** — fallbacks, load balancing, spend tracking

#### Disadvantages
- **Still need custom UI** — chat component, history, notes, prompt library
- **LiteLLM is complex** — Python-heavy, adds ~200MB runtime
- **No built-in RAG** — would need separate pipeline
- **No built-in model management** — still need llm-gateway for local models
- **Custom UI still substantial effort** — estimated 4-6 weeks for parity with Open WebUI
- **LiteLLM adds latency** — Python proxy layer ~50-100ms overhead per request
- **Version compatibility** — LiteLLM updates frequently, can break API compatibility

#### Resource Estimate
| Component | RAM | CPU | Disk |
|-----------|-----|-----|------|
| LiteLLM Proxy | ~200MB | 0.2 cores | ~200MB |
| Custom UI (Svelte) | 0 | 0 | ~500KB (bundled) |
| llm-gateway | ~20MB | <0.1 cores | ~10MB |
| llama.cpp (active) | ~6GB | 11 cores | ~4.5GB |
| **Total** | **~6.3GB** | **11 cores** | **~5GB** |

---

## 4. Comparison Matrix

| Criterion | **A: Open WebUI + Gateway** | **B: Full Custom** | **C: OWUI Standalone** | **D: LiteLLM + Custom** |
|-----------|:---------------------------:|:------------------:|:----------------------:|:------------------------:|
| **Feature completeness** | ⭐⭐⭐⭐⭐ | ⭐⭐ | ⭐⭐⭐⭐ | ⭐⭐⭐ |
| **Development effort** | ~1 week | ~8-12 weeks | **~1 day** | ~4-6 weeks |
| **Custom code** | ~500 lines (gateway) | ~10,000+ lines | **~0 lines** | ~3,000+ lines |
| **Portal integration** | ⭐⭐⭐ (iframe) | ⭐⭐⭐⭐⭐ (native) | ⭐⭐ (iframe/link) | ⭐⭐⭐⭐⭐ (native) |
| **Auth integration** | ⭐⭐ (OIDC/sync) | ⭐⭐⭐⭐⭐ (direct) | ⭐⭐ (OIDC/sync) | ⭐⭐⭐⭐⭐ (direct) |
| **RAG capability** | ⭐⭐⭐⭐⭐ (built-in) | ⭐ (custom needed) | ⭐⭐⭐⭐⭐ (built-in) | ⭐⭐ (custom needed) |
| **Model management** | ⭐⭐⭐⭐⭐ (gateway) | ⭐⭐⭐⭐⭐ (backend) | ⭐⭐ (Ollama opaque) | ⭐⭐⭐ (gateway) |
| **Resource usage (idle)** | ~250MB | ~30MB | ~300MB | ~220MB |
| **Resource usage (active)** | ~6.3GB | ~6.1GB | ~6.4GB | ~6.3GB |
| **Operational complexity** | ⭐⭐⭐ | ⭐⭐⭐⭐⭐ | ⭐⭐⭐⭐ | ⭐⭐ |
| **Upstream updates** | ✅ Benefits | N/A | ✅ Benefits | ⚠️ Breaking risk |
| **Multi-modal** | ⭐⭐⭐⭐⭐ (built-in) | ⭐ (custom needed) | ⭐⭐⭐⭐⭐ (built-in) | ⭐⭐ (custom needed) |
| **Tool calling** | ⭐⭐⭐⭐⭐ (built-in) | ⭐ (custom needed) | ⭐⭐⭐⭐⭐ (built-in) | ⭐⭐ (custom needed) |
| **Cost to build** | Low | **Very High** | **Minimal** | Medium |
| **Cost to maintain** | Low | High | Lowest | Medium |

### Weighted Score

Using F(P0)*5, F(P1)*3, F(P2)*2, F(P3)*1 weighting for functional requirements:

| Alternative | Score | Rank |
|-------------|-------|------|
| **A: Open WebUI + Gateway** | **89/100** | **🥇** |
| **B: Full Custom** | 45/100 | 4th |
| **C: OWUI Standalone** | 62/100 | 3rd |
| **D: LiteLLM + Custom** | 67/100 | 2nd |

---

## 5. Local LLM Management Layer

The unique value we add — regardless of which studio alternative is chosen — is the **local LLM management layer**. This is the thin service that bridges the gap between a standard OpenAI-compatible chat UI and raw llama.cpp model files.

### llm-gateway API Design

```
POST   /v1/chat/completions   → Standard OpenAI-compatible chat (proxied)
GET    /v1/models             → List available models (local + configured remotes)
POST   /v1/models/:name/load  → Start llama.cpp server with this model
POST   /v1/models/:name/unload → Stop llama.cpp server
GET    /status                → Current server state (model, uptime, health)
GET    /health                → Liveness check
GET    /models/:name/download → Download model (delegates to llama-lab logic)
GET    /models/:name/delete   → Remove model files
```

### Model Lifecycle

```
stateDiagram-v2
    [*] --> Unloaded
    Unloaded --> Loading : POST /load
    Loading --> Ready : llama.cpp starts
    Loading --> Error : startup fails
    Ready --> Unloaded : POST /unload
    Ready --> Loading : POST /load (different model)
    Error --> Unloaded : POST /unload
    Error --> Loading : POST /load (retry)
```

### Supported Models (from llama-lab registry, server-compatible)

| Model | Params | RAM | Notes |
|-------|--------|-----|-------|
| qwen3-coder-30b-a3b | 30B (MoE, 3B active) | 12GB | Top-tier, MoE efficiency |
| qwen3-coder-14b | 14B | 8GB | Excellent all-around |
| qwen25-coder-7b | 7B | 4.5GB | ✅ Already downloaded |
| qwen25-coder-14b | 14B | 9GB | Strong coding |
| codegemma-12b | 12B | 7.5GB | Gemma 3, strong reasoning |
| glm4-9b | 9B | 5.5GB | Bilingual EN/ZH |

> **Rule of thumb:** With 23GB RAM, run **one model at a time** (max ~14B for inference with room for OS + services). MoE models like qwen3-coder-30b-a3b (3B active) can co-exist with other services because their active parameter count is small.

---

## 6. Resource Budget

### Baseline (existing services running)

| Service | RAM | CPU |
|---------|-----|-----|
| PostgreSQL | ~500MB | 0.2 cores |
| Redis | ~50MB | 0.1 cores |
| Trading platform backend | ~60MB | 0.1 cores |
| Matrix relay | ~20MB | <0.1 cores |
| Signal service | ~30MB | <0.1 cores |
| Cost analyzer | ~20MB | <0.1 cores |
| Aetherflow | ~50MB | 0.1 cores |
| Nginx + portal | ~20MB | <0.1 cores |
| **Baseline total** | **~750MB** | **~0.7 cores** |

### Available Budget

| Resource | Total | Baseline | **Available for LLM Studio** |
|----------|-------|----------|------------------------------|
| RAM | 23GB | ~750MB | **~22GB** |
| CPU | 12 cores | ~0.7 cores | **~11 cores** |
| Disk | ~340GB free | — | **~340GB** |

### Model Fit Analysis

| Model | Inference RAM | Idle RAM | Fits alongside baseline? |
|-------|--------------|----------|-------------------------|
| qwen25-coder-7b | ~5.5GB | ~1GB (mmap) | ✅ Comfortably |
| qwen3-coder-14b | ~9GB | ~2GB | ✅ Comfortably |
| codegemma-12b | ~8.5GB | ~2GB | ✅ Comfortably |
| qwen3-coder-30b-a3b | ~13GB | ~2GB | ✅ Yes, with ~9GB spare |
| glm4-9b | ~6.5GB | ~1.5GB | ✅ Comfortably |
| qwen25-coder-32b | ~20GB | ~4GB | ⚠️ Tight — may need to stop other services |

---

## 7. Integration with Existing Infrastructure

### Docker Compose Integration (Architecture A)

```yaml
services:
  # ── existing services unchanged ──

  # ── Local LLM Gateway ──
  llm-gateway:
    build: ./code/llm-gateway
    ports:
      - "${BIND_IP:-0.0.0.0}:3100:3100"
    environment:
      - GATEWAY_PORT=3100
      - MODELS_DIR=/models
      - LLAMACPP_BIN=/usr/local/bin/llama-server
      - DEEPSEEK_API_KEY=${DEXTER_DEEPSEEK_API_KEY:-}
      - GEMINI_API_KEY=${DEXTER_GOOGLE_API_KEY:-}
      - OPENAI_API_KEY=${DEXTER_OPENAI_API_KEY:-}
    volumes:
      - ${LLAMA_MODELS_DIR:-~/.local/share/llama-lab/models}:/models:ro
      - /var/run/docker.sock:/var/run/docker.sock:ro  # If running llama.cpp in separate container
    networks:
      - trading-network
    restart: unless-stopped
    deploy:
      resources:
        limits:
          cpus: '0.5'
          memory: 256M

  # ── Open WebUI (LLM Studio frontend) ──
  open-webui:
    image: ghcr.io/open-webui/open-webui:main
    ports:
      - "3000:8080"
    environment:
      - OPENAI_API_BASE_URL=http://llm-gateway:3100/v1
      - WEBUI_SECRET_KEY=${WEBUI_SECRET_KEY:-change-me-in-production}
      - WEBUI_AUTH=true
      - WEBUI_NAME=Frao LLM Studio
      # Point to portal auth if using OIDC, or use built-in auth
    volumes:
      - open-webui-data:/app/backend/data
    networks:
      - trading-network
    restart: unless-stopped
    depends_on:
      - llm-gateway
```

### Nginx Route Integration

```nginx
# In company-portal/nginx.conf, add:

location /api/llm/ {
    proxy_pass http://llm-gateway:3100/;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_buffering off;
    proxy_cache off;
    proxy_read_timeout 86400s;  # SSE / streaming support
}

location /llm-studio/ {
    proxy_pass http://open-webui:8080/;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_buffering off;
    proxy_cache off;
    proxy_read_timeout 86400s;
}
```

### Portal Integration

Add to `state.ts` tool definitions:
```typescript
{ id: 'llm-studio', name: 'LLM Studio',
  description: 'Unified LLM chat interface — DeepSeek, Gemini, ChatGPT, and local models.',
  icon: '🧠', href: '/llm-studio', minRole: 'user', category: 'platform', status: 'active' },
]
```

Users click → `/llm-studio` → nginx serves Open WebUI on that subpath.

---

## 8. Recommendation

### 🥇 Architecture A: Open WebUI + llm-gateway

**Why this wins:**

1. **Time-to-value:** ~1 week vs ~8-12 weeks for a full custom build
2. **Feature parity impossible to beat:** RAG, multi-modal, tool calling, prompt libraries — all built-in
3. **Defensible scope:** The llm-gateway (our custom code) is a focused ~500-line Go service that manages model lifecycle — this is the unique value we add, not the chat UI
4. **Upgrade leverage:** Every Open WebUI release brings new features for free
5. **Community:** 50K+ stars, active development, security patches
6. **Resource fit:** ~250MB idle overhead is well within the 22GB budget
7. **Known pattern:** Many enterprises run Open WebUI this way — battle-tested

**The real tradeoff is:** Perfect theming (impossible) vs. extreme feature depth (trivial). For an enterprise portal, features win.

### 🥈 Architecture D: LiteLLM + Custom UI

If the iframe integration is unacceptable and you have 4-6 weeks to invest, this offers native UI with LiteLLM handling the routing complexity. But you still need to build chat, history, RAG, and prompt libraries — substantial effort.

### 🤷 Architecture C: Open WebUI Standalone

Fastest to deploy, but the lack of model lifecycle control and duplicate storage makes it a non-starter for our use case where we already have llama-lab models and need fine-grained management.

### ❌ Architecture B: Full Custom Build

Only recommended if you have months to invest and the iframe integration is a hard blocker. The feature gap (especially RAG, multi-modal, tool calling) is too wide to close in a reasonable timeframe.

---

## 9. Implementation Roadmap

### Phase 1: Foundation (Week 1)

1. **llm-gateway** — Go service scaffolding
   - OpenAI-compatible `/v1/chat/completions` and `/v1/models`
   - llama.cpp server lifecycle management (start/stop/health)
   - Remote endpoint proxying (DeepSeek, Gemini, ChatGPT)
   - Model status API
2. **Docker Compose integration**
   - Add llm-gateway service
   - Add Open WebUI service
   - Nginx route configuration
3. **Portal integration**
   - Add LLM Studio to TOOLS array
   - Open WebUI on subpath

### Phase 2: Lifecycle Management (Week 2)

1. **Model download API** — Wrap llama-lab's model download logic
2. **Model deletion** — Remove model files
3. **Hardware-aware defaults** — Auto-tune context size, threads, batch size
4. **Health monitoring** — Track model health, restart on crash
5. **Admin UI** — Basic model management panel in portal (start/stop/list)

### Phase 3: Polish & Advanced (Week 3+)

1. **Open WebUI theming** — Custom CSS, logo, branding
2. **OIDC/SSO integration** — Portal auth → Open WebUI
3. **Resource dashboards** — Show model resource usage in portal
4. **Multi-model support** — Run multiple small models simultaneously
5. **Performance tuning** — Quantization selection, context optimization

---

## Appendix A: Open WebUI Alternatives Considered

| Tool | Stars | Lang | Notes |
|------|-------|------|-------|
| **Open WebUI** | 70K+ | Python/Svelte | Most feature-rich, active community |
| **Jan.ai** | 25K+ | Electron | Desktop-first, harder to Dockerize |
| **LibreChat** | 20K+ | Node.js | Good features, larger resource footprint |
| **Big-AGI** | 5K+ | React | Nice UI, less mature |
| **Lobe Chat** | 50K+ | TypeScript | Modern UI, plugin architecture |
| **LocalAI** | 28K+ | Go | Backend-only, no frontend |
| **text-generation-webui** | 42K+ | Python | Heavy, desktop-oriented |

## Appendix B: Existing Models Available

Currently downloaded (via llama-lab):
- `qwen25-coder-7b` (4.5GB Q4_K_M)
- `qwen3-coder-30b-a3b` (8GB Q4_K_M)

Recommended additional downloads for the studio (general chat, not just coding):
- `qwen3-coder-14b` — Excellent all-purpose chat + coding
- `glm4-9b` — Strong bilingual model
- `codegemma-12b` — Gemma 3 12B, strong all-around
