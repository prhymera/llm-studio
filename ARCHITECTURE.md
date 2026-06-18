# LLM Studio — Architecture Analysis & Design

> **Context:** Frao Technologies enterprise portal (SvelteKit + Nginx + Docker Compose)  
> **Server:** 12-core EPYC, 23GB RAM, CPU-only, Arch Linux  
> **Existing infra:** `llama-lab` (llama.cpp model manager), aetherflow (Go), picoclaw, pi.dev, `docker-compose.yml`  
> **Date:** 2026-06-18

---

## Table of Contents

1. [Problem Statement](#1-problem-statement)
2. [Requirements Analysis](#2-requirements-analysis)
3. [Architecture Alternatives](#3-architecture-alternatives)
4. [Comparison Matrix](#4-comparison-matrix)
5. [System Architecture (Selected)](#5-system-architecture-selected)
   - [5.1 Component Overview](#51-component-overview)
   - [5.2 llm-gateway (Go)](#52-llm-gateway-go)
   - [5.3 agent-runner Service](#53-agent-runner-service)
   - [5.4 auth-bridge (SSO Proxy)](#54-auth-bridge-sso-proxy)
6. [Agent Runner Design](#6-agent-runner-design)
   - [6.1 Agent Contract](#61-agent-contract)
   - [6.2 Agent Images & Lifecycle](#62-agent-images--lifecycle)
   - [6.3 Terminal Integration](#63-terminal-integration)
   - [6.4 Session Model](#64-session-model)
7. [Storage Layer Architecture](#7-storage-layer-architecture)
   - [7.1 Directory Layout](#71-directory-layout)
   - [7.2 Volume Mount Strategy](#72-volume-mount-strategy)
   - [7.3 Backup & Retention](#73-backup--retention)
8. [Configuration & Endpoint Management](#8-configuration--endpoint-management)
   - [8.1 Central Configuration Store](#81-central-configuration-store)
   - [8.2 Auto-Provisioning Pipeline](#82-auto-provisioning-pipeline)
9. [SSO & Auth Integration](#9-sso--auth-integration)
   - [9.1 Auth Flow](#91-auth-flow)
   - [9.2 User Provisioning](#92-user-provisioning)
10. [Go vs Rust Decision](#10-go-vs-rust-decision)
11. [Resource Budget](#11-resource-budget)
12. [Integration with Portal](#12-integration-with-portal)
13. [Implementation Roadmap](#13-implementation-roadmap)

---

## 1. Problem Statement

The Frao Technologies enterprise portal needs an **LLM Studio** — a unified interface for:

- **Chatting** with both remote enterprise LLMs (DeepSeek, Gemini, ChatGPT) and **local LLMs** (Qwen, Gemma, GLM via llama.cpp)
- **Managing** local model lifecycle — download, start, stop, switch models
- **Running agents** — picoclaw, pi.dev, opencode — inside ephemeral Docker containers with persistent workspaces, accessible via a terminal-in-browser interface
- **Notes & Sessions** — persistent, searchable chat history with markdown notes
- **Advanced features** — RAG, prompt libraries, multi-user access, RBAC integration
- **SSO** — portal users automatically authenticated in Open WebUI and agent runner
- **Deep integration** with the existing portal sidebar, auth, and Docker Compose stack

The core design questions:
1. **Build or buy** the chat UI? → **Open WebUI** wins decisively (see §3-4)
2. **Go or Rust** for the gateway service? → **Go** wins (see §10)
3. **How to run agents securely** with persistence? → **Ephemeral Docker containers + host volumes** (see §6-7)
4. **How to SSO portal → Open WebUI?** → **Auth proxy with JWT validation** (see §9)

---

## 2. Requirements Analysis

### Functional Requirements

| Priority | Requirement | Detail |
|----------|------------|--------|
| **P0** | Multi-provider chat | Remote: DeepSeek, Gemini, ChatGPT. Local: llama.cpp server |
| **P0** | Chat history | Persistent, searchable, per-user |
| **P0** | Model management | Start/stop/switch local models, show status |
| **P0** | Agent sessions | Launch picoclaw/pi.dev/opencode in ephemeral containers, interact via terminal |
| **P0** | Persistence | Agent workspaces survive container death, service restart, host reboot |
| **P1** | Notes | Markdown notebook alongside chats |
| **P1** | RBAC | Portal auth integration (roles: user/operator/admin) |
| **P1** | SSO | Portal login → automatic Open WebUI + agent runner authentication |
| **P1** | Endpoint config | Single source of truth for all LLM endpoints → auto-provisioned to all subsystems |
| **P2** | RAG | Upload documents, search within them |
| **P2** | Prompt library | Saved/categorized prompt templates |
| **P2** | Multi-modal | Image upload for vision models |
| **P2** | Agent image mgmt | Manage which agent versions are available, update images |
| **P3** | Tool calling | Function calling / plugin system |
| **P3** | Model switching at runtime | Switch between providers mid-conversation |

### Non-Functional Requirements

| Requirement | Target |
|-------------|--------|
| **Resource usage** | < 4GB RAM for the studio components at idle |
| **Latency** | < 500ms overhead on top of LLM inference time |
| **Security** | API keys stored securely, never leaked to frontend; agents run isolated |
| **Maintainability** | Minimal custom code, leverage upstream |
| **Integration** | Must fit into existing Docker Compose + Nginx routing |
| **Persistence** | Zero data loss on container/service/host restart |

---

## 3. Architecture Alternatives

*(See previous analysis — Architecture A: Open WebUI + llm-gateway remains the winner; the alternatives analysis is unchanged.)*

**Selected: Architecture A — Open WebUI + llm-gateway**, extended with:
- **agent-runner** — new component for ephemeral agent containers
- **auth-bridge** — new component for portal → Open WebUI SSO
- **Storage layer** — persistent volume design for agent workspaces

---

## 4. Comparison Matrix

*(unchanged from previous analysis)*

---

## 5. System Architecture (Selected)

### 5.1 Component Overview

```
┌─────────────────────────────────────────────────────────────────────────────────────┐
│  Frao Portal (SvelteKit SPA)                                                         │
│  ┌──────────────────────────────────────────────────────────────────────────────┐   │
│  │  nginx (reverse proxy)                                                        │   │
│  │  /api/llm/*           → llm-gateway:3100                                      │   │
│  │  /api/agents/*         → agent-runner:3200                                    │   │
│  │  /api/auth/*           → auth-bridge:3300                                     │   │
│  │  /llm-studio/*         → auth-bridge:3300 → open-webui:8080                   │   │
│  │  /agents/term/*        → agent-runner:3200 (WebSocket upgrade)                │   │
│  └──────────────────────────────────────────────────────────────────────────────┘   │
│  Portal UI directly calls:                                                           │
│  ┌──────────────────────────────────────────────────────────────────────────────┐   │
│  │  /app/llm-studio/    → SvelteKit page w/ embedded iframe to /llm-studio/    │   │
│  │  /app/agents/        → SvelteKit page w/ xterm.js terminal + session list   │   │
│  └──────────────────────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────────────────────┘
                                    │
           ┌────────────────────────┼────────────────────────────┐
           │                        │                            │
           ▼                        ▼                            ▼
  ┌──────────────────┐   ┌────────────────────┐   ┌────────────────────────┐
  │  Open WebUI      │   │  llm-gateway (Go)  │   │  agent-runner (Go)     │
  │  · Chat UI       │   │  · Model lifecycle │   │  · Docker API mgmt     │
  │  · RAG           │   │  · llama.cpp mgmt  │   │  · Agent session CRUD  │
  │  · History       │   │  · Remote proxy    │   │  · Terminal WebSocket   │
  │  · Prompt lib    │   │  · Endpoint status │   │  · Workspace mounts    │
  │  · Tools/Multi   │   └────────┬───────────┘   └───────────┬────────────┘
  └──────┬───────────┘            │                           │
         │                  ┌─────┴──────┐                    │
         │                  │  llama.cpp  │                    │
         │                  │  server     │                    │
         │                  │  (per model)│                    │
         │                  └────────────┘                    │
         │                                                    │
         │           ┌────────────────────────────────────────┘
         │           │          ┌──────────────────────┐
         │           │          │  Auth Bridge (Go)    │
         │           │          │  · JWT validation    │
         │           │          │  · Session injection  │
         │           │          │  · User provisioning  │
         │           │          └──────────────────────┘
         ▼           ▼
  ┌──────────────────────────────────────────────────┐
  │  Remote LLMs                                      │
  │  DeepSeek · Gemini · ChatGPT · OpenRouter · etc   │
  └──────────────────────────────────────────────────┘

  ┌──────────────────────────────────────────────────────────────────────────┐
  │  Ephemeral Agent Containers (spawned per-session by agent-runner)        │
  │                                                                          │
  │  ┌──────────┐  ┌──────────┐  ┌──────────┐                               │
  │  │ picoclaw  │  │  pi.dev  │  │ opencode │  ...                          │
  │  │ container │  │  container│  │ container│                               │
  │  │ :3001/tty │  │ :3002/tty│  │ :3003/tty│                               │
  │  │ vol: ws/  │  │ vol: ws/ │  │ vol: ws/ │                               │
  │  └──────────┘  └──────────┘  └──────────┘                               │
  │                                                                          │
  │  Each container mounts:                                                  │
  │  /data/llm-studio/workspaces/{user}/{session}/ → /workspace              │
  │  /data/llm-studio/config/endpoints.yml → /etc/llm/endpoints.yml (ro)     │
  │  /data/llm-studio/config/secrets.yml → /etc/llm/secrets.yml (ro)         │
  └──────────────────────────────────────────────────────────────────────────┘
```

### 5.2 llm-gateway (Go)

**Purpose:** Minimal model lifecycle manager and API proxy. Inspired by LiteLLM's architecture but kept deliberately thin — it should do _only_ what Open WebUI cannot.

**API Surface:**

```
# Model Management
GET    /v1/models                      → List models (local installed + remote configured)
POST   /v1/models/:name/load           → Start llama.cpp server with model
POST   /v1/models/:name/unload         → Stop llama.cpp server
GET    /v1/models/:name/status         → Model status (unloaded/loading/ready/error)
POST   /v1/models/:name/download       → Download model GGUF from HuggingFace
DELETE /v1/models/:name                → Delete model files

# Chat (passthrough to currently loaded local model, or route to remote)
POST   /v1/chat/completions            → OpenAI-compatible chat (routes to active backend)
POST   /v1/embeddings                  → OpenAI-compatible embeddings

# Health & Status
GET    /health                         → Liveness (always 200)
GET    /status                         → Full system status (loaded model, RAM, uptime)

# Open WebUI Integration
GET    /v1/endpoints                   → All configured endpoints for Open WebUI auto-config
```

**Design principles:**
- No database dependency — state is derived from filesystem + running processes
- All remote API keys come from environment variables (managed by Docker Compose / .env)
- llama.cpp is run as a managed subprocess, not in a separate container (avoids Docker-in-Docker complexity)
- Logs are structured JSON to stdout, collected by Docker logging driver

### 5.3 agent-runner Service

**Purpose:** Manage the full lifecycle of agent sessions — create, launch, monitor, terminate, reconnect.

**API Surface:**

```
# Session Management
POST   /v1/sessions                    → Create new agent session
  Body: { agent_type, model, workspace_label, config_overrides }
GET    /v1/sessions                    → List user's sessions
GET    /v1/sessions/:id                → Get session details
DELETE /v1/sessions/:id                → Terminate & cleanup session
POST   /v1/sessions/:id/reconnect      → Reconnect to existing session (restart container if stopped)

# Terminal
GET    /v1/sessions/:id/tty            → WebSocket upgrade → xterm.js protocol
POST   /v1/sessions/:id/input          → Send terminal input (alternative to WS)
GET    /v1/sessions/:id/output         → Stream terminal output (SSE, alternative to WS)

# Agent Images
GET    /v1/agents                      → List available agent types & versions
POST   /v1/agents/:type/pull            → Pull/update agent Docker image

# Configuration (read from central config)
GET    /v1/endpoints                   → All configured endpoints (models, API keys masked)
```

**Design principles:**
- Each session = 1 Docker container running the agent binary
- Container is ephemeral (no restart policy — agent-runner recreates on reconnect)
- Terminal I/O via WebSocket + xterm.js protocol (PTY multiplexing)
- Workspace lives on host volume (survives container death)
- Agent-runner owns the Docker socket, not the gateway

### 5.4 auth-bridge (SSO Proxy)

**Purpose:** Sit in front of Open WebUI, validate portal session cookies/JWTs, inject auth headers, auto-provision users.

**API Surface (internal, used by nginx):**

```
GET    /*                             → Proxy to Open WebUI after auth check
  Checks: Authorization header or session cookie
  On success: injects X-User-Id, X-User-Role, X-User-Email headers
  On failure: redirects to portal login
```

**Design:**
- Stateless Go reverse proxy
- Validates JWT signed by portal backend
- If valid: passes request to Open WebUI with enriched headers
- Open WebUI is configured to trust these headers for auth
- First-time users auto-provisioned via Open WebUI API (user create on first access)

---

## 6. Agent Runner Design

### 6.1 Agent Contract

Every agent container must satisfy this minimal contract to work with the runner:

```yaml
# Agent Image Contract
# Each agent Docker image MUST:
# 1. Accept these environment variables:
#    - LLM_ENDPOINT:   Base URL of the LLM API (e.g. http://llm-gateway:3100/v1)
#    - LLM_MODEL:      Model name (e.g. "qwen25-coder-7b")
#    - LLM_API_KEY:    API key (for remote endpoints, or "local" for llama.cpp)
#    - WORKSPACE:      Path to workspace directory (/workspace)
#    - USER_ID:        Portal user ID
#    - SESSION_ID:     Agent session ID
#    - AGENT_CONFIG:   Path to JSON config file (/etc/agent/config.json)
#
# 2. Provide a terminal interface on a known port (e.g., 3001/tcp) or
#    inherit stdin/stdout from the container (docker exec -it)
#
# 3. Store all work inside /workspace (persistent volume mount)
#
# 4. Exit cleanly on SIGTERM/SIGINT
```

**Agent Images Registry:**

| Agent | Base Image | Binary Source | Terminal Port |
|-------|-----------|---------------|---------------|
| picoclaw | `debian:bookworm-slim` | Pre-built binary or build from source | stdin/stdout (docker exec) |
| pi.dev | `node:20-alpine` | `npm install -g @anthropic-ai/pi` | stdin/stdout |
| opencode | `python:3.12-slim` | `pip install opencode` | stdin/stdout |
| custom | User-defined | Any | stdin/stdout |

**Agent Image Dockerfile examples:**

```dockerfile
# picoclaw-agent/Dockerfile
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y \
    ca-certificates libssl3 libolm3 curl git \
    && rm -rf /var/lib/apt/lists/*

COPY picoclaw-linux-amd64 /usr/local/bin/picoclaw
COPY entrypoint.sh /entrypoint.sh

RUN chmod +x /usr/local/bin/picoclaw /entrypoint.sh

WORKDIR /workspace
ENTRYPOINT ["/entrypoint.sh"]
```

```dockerfile
# pi-agent/Dockerfile
FROM node:20-alpine

RUN npm install -g @anthropic-ai/pi

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

WORKDIR /workspace
ENTRYPOINT ["/entrypoint.sh"]
```

```dockerfile
# opencode-agent/Dockerfile
FROM python:3.12-slim

RUN pip install opencode

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

WORKDIR /workspace
ENTRYPOINT ["/entrypoint.sh"]
```

**Entrypoint script (shared pattern):**

```bash
#!/bin/bash
set -euo pipefail

# Write agent config from environment
mkdir -p /etc/agent
cat > /etc/agent/config.json <<EOF
{
  "llm_endpoint": "${LLM_ENDPOINT}",
  "llm_model": "${LLM_MODEL}",
  "llm_api_key": "${LLM_API_KEY}",
  "user_id": "${USER_ID}",
  "session_id": "${SESSION_ID}",
  "workspace": "${WORKSPACE:-/workspace}"
}
EOF

# Source any workspace-local config
if [ -f /workspace/.agentrc ]; then
  source /workspace/.agentrc
fi

echo "Agent session ${SESSION_ID} started at $(date)"
echo "Model: ${LLM_MODEL} via ${LLM_ENDPOINT}"

# Launch the agent - it blocks until done
exec picoclaw "$@"
```

### 6.2 Agent Images & Lifecycle

#### Image Build & Storage

Agent images are built as part of the LLM Studio deployment and stored in:
- **Locally:** Docker image registry on the host (images built from `code/agent-images/*`)
- **Future:** GitHub Container Registry (ghcr.io) for distribution

#### Container Lifecycle

```
stateDiagram-v2
    [*] --> Creating : POST /v1/sessions
    Creating --> Running : container started
    Creating --> Failed : Docker error
    Running --> Active : user connects via terminal
    Running --> Idle : no terminal connection
    Active --> Idle : user disconnects
    Idle --> Active : user reconnects
    Running --> Stopped : DELETE /v1/sessions/:id
    Running --> Stopped : SIGTERM (timeout)
    Stopped --> Running : POST /v1/sessions/:id/reconnect
    Running --> Failed : agent crash
    Failed --> Running : POST /v1/sessions/:id/reconnect
    Stopped --> [*] : retention policy expired
```

#### Container Configuration on Launch

```json
{
  "container_name": "agent-{user_id}-{session_id}",
  "image": "llm-studio-agent-picoclaw:latest",
  "env": [
    "LLM_ENDPOINT=http://llm-gateway:3100/v1",
    "LLM_MODEL=qwen25-coder-7b",
    "LLM_API_KEY=local",
    "WORKSPACE=/workspace",
    "USER_ID=user-abc123",
    "SESSION_ID=sess-def456"
  ],
  "volumes": [
    {
      "host": "/data/llm-studio/workspaces/user-abc123/sess-def456",
      "container": "/workspace",
      "mode": "rw"
    },
    {
      "host": "/data/llm-studio/config/endpoints.yml",
      "container": "/etc/llm/endpoints.yml",
      "mode": "ro"
    }
  ],
  "resources": {
    "cpu_limit": 4,
    "memory_limit": "4G",
    "cpu_reservation": 1,
    "memory_reservation": "1G"
  },
  "security": {
    "read_only_rootfs": true,
    "cap_drop": ["ALL"],
    "cap_add": [],
    "seccomp": "default",
    "no_new_privileges": true,
    "network": "trading-network"
  },
  "timeout": 86400,
  "labels": {
    "llm-studio.component": "agent",
    "llm-studio.user": "user-abc123",
    "llm-studio.session": "sess-def456",
    "llm-studio.agent-type": "picoclaw"
  }
}
```

### 6.3 Terminal Integration

The terminal UI is a native SvelteKit page (not iframe) in the portal:

```
/agents → SvelteKit page
  ├── Session list (left sidebar)
  │   ├── Active sessions (green dot, click to reconnect)
  │   ├── Past sessions (grey, click to reopen)
  │   └── "New Session" button
  ├── Terminal pane (main area)
  │   ├── xterm.js instance
  │   └── WebSocket connection to agent-runner
  └── Session toolbar (top)
      ├── Agent type selector (picoclaw/pi/opencode)
      ├── Model selector (populated from gateway)
      ├── Workspace label (editable)
      └── Actions: Stop, Restart, Delete
```

**WebSocket Protocol:**

```
Client → Server (agent-runner):
  { type: "input", data: "ls -la\r\n" }
  { type: "resize", cols: 120, rows: 40 }
  { type: "ping" }

Server → Client:
  { type: "output", data: "\x1b[32mfile.txt\x1b[0m\r\n" }
  { type: "exit", code: 0 }
  { type: "error", message: "container crashed" }
  { type: "pong" }
```

**Implementation:**
- Frontend: [`xterm.js`](https://xtermjs.org/) + [`xterm-addon-fit`](https://www.npmjs.com/package/xterm-addon-fit)
- Backend (agent-runner): [`gorilla/websocket`](https://github.com/gorilla/websocket) + [`creack/pty`](https://github.com/creack/pty) (PTY multiplexing) or Docker exec with terminal attach
- Alternative: Use [`docker attach`](https://docs.docker.com/engine/api/v1.47/#tag/Container/operation/ContainerAttach) WebSocket endpoint directly via Docker API

**PTY architecture for agent-runner:**

```go
// agent-runner internal/terminal/pty.go
type PTYManager struct {
    // Maps session ID → PTY session
    sessions map[string]*PTYSession
}

type PTYSession struct {
    ContainerID string
    PTY         *os.File      // PTY master
    Stdin       io.Writer
    Stdout      io.Reader
    WS          *websocket.Conn  // Connected frontend
}
```

The runner:
1. Creates a new session → starts Docker container
2. Uses `docker exec -it` with a PTY attached
3. Multiplexes PTY output → WebSocket to the browser
4. On disconnect: keeps container running (detaches from PTY)
5. On reconnect: `docker attach` to the same container

### 6.4 Session Model

```go
// agent-runner internal/session/types.go
type Session struct {
    ID            string    `json:"id" db:"id"`
    UserID        string    `json:"user_id" db:"user_id"`
    AgentType     string    `json:"agent_type" db:"agent_type"`       // picoclaw, pi, opencode
    Model         string    `json:"model" db:"model"`                 // qwen25-coder-7b, deepseek-v4, etc.
    Endpoint      string    `json:"endpoint" db:"endpoint"`           // local / remote endpoint name
    Status        string    `json:"status" db:"status"`               // creating, running, stopped, failed
    ContainerID   string    `json:"container_id" db:"container_id"`
    WorkspacePath string    `json:"workspace_path" db:"workspace_path"`
    WorkspaceLabel string   `json:"workspace_label" db:"workspace_label"`
    ConfigJSON    string    `json:"config_json" db:"config_json"`     // session-specific config overrides
    CreatedAt     time.Time `json:"created_at" db:"created_at"`
    LastActiveAt  time.Time `json:"last_active_at" db:"last_active_at"`
    StoppedAt     *time.Time `json:"stopped_at" db:"stopped_at"`
}
```

---

## 7. Storage Layer Architecture

### 7.1 Directory Layout

```
/data/llm-studio/                         ← Host volume root
├── config/                               ← Shared configuration (read-only to containers)
│   ├── endpoints.yml                     ← All LLM endpoints (auto-generated)
│   ├── secrets.yml                       ← API keys (auto-generated, masked in logs)
│   └── agent-images/                     ← Agent Docker image build contexts
│       ├── picoclaw/
│       │   ├── Dockerfile
│       │   ├── entrypoint.sh
│       │   └── picoclaw-linux-amd64
│       ├── pi/
│       │   ├── Dockerfile
│       │   └── entrypoint.sh
│       └── opencode/
│           ├── Dockerfile
│           └── entrypoint.sh
├── workspaces/                           ← Per-user, per-session workspaces
│   └── {user_id}/
│       └── {session_id}/
│           ├── .agentrc                  ← Session-local agent config (restored on reconnect)
│           ├── .agent_history            ← Command history
│           └── ... (user's files)
├── sessions.db                           ← SQLite database (session metadata)
├── chat/                                 ← Open WebUI data (persistent volume)
│   └── (Open WebUI's own data directory)
└── logs/                                 ← Centralized logs
    ├── llm-gateway/
    ├── agent-runner/
    └── agents/
        └── {session_id}.log
```

### 7.2 Volume Mount Strategy

```yaml
# docker-compose.llm-studio.yml — volumes section
volumes:
  llm-studio-data:
    driver: local
    driver_opts:
      type: none
      device: /data/llm-studio
      o: bind
  llm-studio-workspaces:
    driver: local
    driver_opts:
      type: none
      device: /data/llm-studio/workspaces
      o: bind
  open-webui-data:    # Open WebUI's internal data
```

**Mount mapping to services:**

| Service | Mount | Path in Container | Mode |
|---------|-------|-------------------|------|
| llm-gateway | `/data/llm-studio/config` | `/config` | ro |
| llm-gateway | `~/.local/share/llama-lab/models` | `/models` | ro |
| agent-runner | `/data/llm-studio/workspaces` | `/workspaces` | rw |
| agent-runner | `/data/llm-studio/config` | `/config` | ro |
| agent-runner | `/var/run/docker.sock` | `/var/run/docker.sock` | rw |
| agent-runner | `/data/llm-studio/sessions.db` | `/data/sessions.db` | rw |
| agent-runner | `/data/llm-studio/logs` | `/logs` | rw |
| open-webui | `/data/llm-studio/chat` | `/app/backend/data` | rw |
| Agent containers | `/data/llm-studio/workspaces/{user}/{session}` | `/workspace` | rw |
| Agent containers | `/data/llm-studio/config/endpoints.yml` | `/etc/llm/endpoints.yml` | ro |

### 7.3 Backup & Retention

**Backup strategy:**
- Workspaces are on a bind-mounted host directory → can be backed up with standard tools (rsync, borg, restic)
- SQLite database (`sessions.db`) — daily `sqlite3 .backup` + WAL archiving
- Open WebUI data — included in `llm-studio-data` volume

**Retention policy:**
- Active sessions: kept indefinitely (user decides when to delete)
- Stopped sessions: 90 days since last activity, then auto-cleanup
- Config files: versioned (previous configs kept for 30 days as `.yml.bak`)

**Recovery:**
- Session reconnect: agent-runner checks if workspace exists, re-creates container, mounts same workspace
- Full host restore: restore `/data/llm-studio` from backup, restart stack

---

## 8. Configuration & Endpoint Management

### 8.1 Central Configuration Store

The single source of truth for all LLM configuration lives in the portal's `.env` file and `docker-compose.yml`. On startup, the llm-gateway generates the derived config files.

**Source of truth (`hedgefund/.env`):**

```bash
# ── Remote Endpoints ──
DEXTER_DEEPSEEK_API_KEY=sk-...
DEXTER_GOOGLE_API_KEY=AIza...
DEXTER_OPENAI_API_KEY=sk-...
DEXTER_ANTHROPIC_API_KEY=sk-ant-...
DEXTER_XAI_API_KEY=...
DEXTER_OPENROUTER_API_KEY=...

# ── Local Models ──
LLAMA_MODELS_DIR=~/.local/share/llama-lab/models
LLAMA_DEFAULT_MODEL=qwen25-coder-7b

# ── Agent Configuration ──
LLM_STUDIO_AGENT_DEFAULT_MODEL=qwen25-coder-7b
LLM_STUDIO_AGENT_DEFAULT_TYPE=picoclaw
LLM_STUDIO_AGENT_TIMEOUT=86400
LLM_STUDIO_AGENT_CPU_LIMIT=4
LLM_STUDIO_AGENT_MEMORY_LIMIT=4G
```

**Auto-generated configuration (`endpoints.yml`):**

```yaml
# Generated by llm-gateway on startup
# Path: /data/llm-studio/config/endpoints.yml
endpoints:
  - name: "llama-local"
    type: local
    base_url: "http://llm-gateway:3100/v1"
    api_key: "local"
    models:
      - "qwen25-coder-7b"
      - "qwen3-coder-14b"
      - "qwen3-coder-30b-a3b"
      - "codegemma-12b"
      - "glm4-9b"
    default: true

  - name: "deepseek"
    type: remote
    base_url: "https://api.deepseek.com/v1"
    api_key_env: "DEEPSEEK_API_KEY"
    models:
      - "deepseek/deepseek-v4-pro"
      - "deepseek/deepseek-v4-flash[1m]"
      - "deepseek/deepseek-reasoner"

  - name: "gemini"
    type: remote
    base_url: "https://generativelanguage.googleapis.com/v1beta/openai/"
    api_key_env: "GEMINI_API_KEY"
    models:
      - "gemini/gemini-2.5-flash"
      - "gemini/gemini-2.5-pro"

  - name: "openai"
    type: remote
    base_url: "https://api.openai.com/v1"
    api_key_env: "OPENAI_API_KEY"
    models:
      - "gpt-4o"
      - "gpt-4-turbo"

  - name: "openrouter"
    type: remote
    base_url: "https://openrouter.ai/api/v1"
    api_key_env: "OPENROUTER_API_KEY"
    models:
      - "openrouter/anthropic/claude-sonnet-4"
      - "openrouter/qwen/qwen-2.5-coder-32b-instruct"
```

### 8.2 Auto-Provisioning Pipeline

```
                    ┌──────────────────────┐
                    │  docker-compose up    │
                    └──────────┬───────────┘
                               │
                               ▼
              ┌──────────────────────────────┐
              │  llm-gateway startup         │
              │  1. Read env vars            │
              │  2. Scan /models directory    │
              │  3. Generate endpoints.yml   │
              │  4. Write to /data/llm-studio │
              └──────────┬───────────────────┘
                         │
                         ▼
              ┌──────────────────────────────┐
              │  agent-runner startup        │
              │  1. Read endpoints.yml       │
              │  2. Register local agents    │
              │  3. Build agent images if    │
              │     not present              │
              └──────────┬───────────────────┘
                         │
                         ▼
              ┌──────────────────────────────┐
              │  Open WebUI startup          │
              │  1. Read endpoints.yml via   │
              │     auth-bridge              │
              │  2. Auto-configure endpoints │
              │     via Open WebUI API       │
              │  3. Set default model        │
              └──────────────────────────────┘
```

**Open WebUI auto-configuration (via API):**

```bash
# After Open WebUI starts, llm-gateway provisions endpoints via its API
curl -X POST http://open-webui:8080/api/v1/endpoints \
  -H "Authorization: Bearer ${ADMIN_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Local LLMs",
    "base_url": "http://llm-gateway:3100/v1",
    "api_key": "local",
    "models": ["qwen25-coder-7b", "qwen3-coder-14b", ...]
  }'

curl -X POST http://open-webui:8080/api/v1/endpoints \
  -H "Authorization: Bearer ${ADMIN_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "DeepSeek",
    "base_url": "https://api.deepseek.com/v1",
    "api_key": "${DEEPSEEK_API_KEY}",
    "models": ["deepseek/deepseek-v4-pro", ...]
  }'
```

**Agent config provisioning:**

When a user launches an agent session, the agent-runner:
1. Reads `endpoints.yml` for the available models
2. Presents the model list to the user in the portal UI
3. On selection, generates the container config with:
   - `LLM_ENDPOINT` → the endpoint's base_url
   - `LLM_MODEL` → the selected model name
   - `LLM_API_KEY` → resolved from secrets (or "local" for llama.cpp)
   - `WORKSPACE` → `/workspace` (mounted volume)
4. Launches the container with these env vars

---

## 9. SSO & Auth Integration

### 9.1 Auth Flow

```
User Browser              Portal Nginx          Portal Backend        Auth Bridge         Open WebUI
    │                        │                      │                    │                   │
    │  POST /api/login       │                      │                    │                   │
    │  (email, password) ────│─────→               │                    │                   │
    │                        │                      │                    │                   │
    │                        │                      │  Validate creds    │                   │
    │                        │                      │  Create JWT        │                   │
    │                        │                      │  Set cookie        │                   │
    │                        │←────────────────────│                    │                   │
    │  ← Set-Cookie:         │                      │                    │                   │
    │     session_jwt=...    │                      │                    │                   │
    │                        │                      │                    │                   │
    │  GET /llm-studio/      │                      │                    │                   │
    │  (cookie attached) ────│───────────────────────────────────────────│                   │
    │                        │                      │                    │                   │
    │                        │                      │  Validate JWT     │                   │
    │                        │                      │  Extract user:    │                   │
    │                        │                      │  {id, role, email}│                   │
    │                        │                      │                    │                   │
    │                        │                      │  Check user exists │                   │
    │                        │                      │  in Open WebUI DB? │                   │
    │                        │                      │  If no: create via │                   │
    │                        │                      │  POST /api/v1/users│                   │
    │                        │                      │                    │                   │
    │                        │                      │  Inject headers:   │                   │
    │                        │                      │  X-User-Id         │                   │
    │                        │                      │  X-User-Role       │                   │
    │                        │                      │  X-User-Email      │                   │
    │                        │                      │                    │    Proxy request  │
    │                        │                      │───────────────────────────────────────│
    │                        │                      │                    │                   │
    │                        │                      │                    │  Trust headers    │
    │                        │                      │                    │  Authenticate     │
    │                        │                      │                    │  Serve page       │
    │                        │                      │←──────────────────────────────────────│
    │  ← Open WebUI page ───│───────────────────────────────────────────│                   │
    │                        │                      │                    │                   │
```

**Key design decisions:**
1. **Shared JWT secret** between portal backend and auth-bridge (set via `JWT_SECRET` env var)
2. **Auth-bridge is stateless** — no database, no sessions, just JWT validation + API calls
3. **Open WebUI configured with `WEBUI_AUTH_TRUST_HEADERS=true`** — trusts `X-User-*` headers set by auth-bridge
4. **User auto-provisioning** — first time a user hits Open WebUI, auth-bridge creates the user via Open WebUI admin API
5. **Role mapping:** portal role → Open WebUI role:
   - `public` → no access
   - `user` → `user`
   - `operator` → `user` (with admin flag for certain operations)
   - `admin` → `admin`
   - `superadmin` → `admin`

### 9.2 User Provisioning

```go
// auth-bridge internal/provision/user.go
func ProvisionUser(ctx context.Context, user UserInfo, owuiAdminKey string) error {
    // 1. Check if user exists in Open WebUI
    existing, err := GetUser(ctx, user.Email, owuiAdminKey)
    if err == nil && existing != nil {
        // 2. Update role if changed
        return UpdateUserRole(ctx, existing.ID, mapRole(user.Role), owuiAdminKey)
    }

    // 3. Create new user
    return CreateUser(ctx, CreateUserRequest{
        Email:    user.Email,
        Password: generateSecurePassword(), // random, user logs in via SSO only
        Name:     user.Name,
        Role:     mapRole(user.Role),
    }, owuiAdminKey)
}
```

---

## 10. Go vs Rust Decision

**Recommendation: Go** for all custom services (llm-gateway, agent-runner, auth-bridge)

### Detailed Comparison

| Criterion | Go | Rust |
|-----------|:---:|:----:|
| **Docker SDK** | ✅ First-class: `docker/docker/client` is the official Go SDK | ⚠️ `bollard` crate is community-maintained, fewer examples |
| **HTTP/WebSocket** | ✅ `net/http` + `gorilla/websocket` — battle-tested, simple | ✅ `axum` + `tokio-tungstenite` — excellent but more complex |
| **PTY management** | ✅ `creack/pty` — direct, widely used | ⚠️ `portable-pty` via `ssh2` crate, more indirection |
| **Subprocess management** | ✅ `os/exec` — simple, reliable | ✅ `std::process::Command` — similar, but async patterns add complexity |
| **JSON/YAML config** | ✅ `encoding/json` + `gopkg.in/yaml.v3` — zero-config | ✅ `serde` — excellent but requires derive macros |
| **Concurrency model** | ✅ Goroutines — simple, implicit | ✅ Tokio async — explicit, powerful but steeper |
| **Compile time** | ✅ ~1 second | ⚠️ ~30-60 seconds for full builds |
| **Binary size** | ~10-15MB | ~5-8MB (smaller) |
| **Memory usage** | ~10-15MB RSS | ~5-8MB RSS (smaller) |
| **Ecosystem fit** | ✅ Matches aetherflow's Go codebase | ❌ Aetherflow-svc is Rust, but rest of stack (trading platform, other services) is Go |
| **Maintainer familiarity** | ✅ Team already works with Go | ❌ Go is more accessible for ops/devops |

### Why Go wins for this specific workload

The llm-gateway, agent-runner, and auth-bridge are **I/O-bound glue services**:

```
Workload profile:
  CPU usage:    5-10% (mostly marshaling JSON and proxying bytes)
  Memory:       10-20MB RSS
  Bottleneck:   I/O (Docker API calls, HTTP proxying, WebSocket multiplexing)
```

Rust's performance advantages (zero-cost abstractions, no GC pauses) matter for:
- High-frequency trading
- Game engines
- Embedded systems
- Latency-sensitive packet processing

They do **not matter** for an HTTP proxy that spends 99% of its time waiting on the Docker API or downstream LLM endpoints.

**The pragmatic argument:** Go's Docker SDK (`docker/docker/client`) is the official, first-party SDK maintained by Docker Inc. The Rust alternative (`bollard`) is community-maintained. For a service whose primary job is managing Docker containers, using the official SDK eliminates an entire class of integration risk.

---

## 11. Resource Budget

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

### LLM Studio Components

| Component | Idle RAM | Active RAM (no inference) | Active RAM (inference) | CPU |
|-----------|:--------:|:-------------------------:|:----------------------:|:---:|
| Open WebUI | ~200MB | ~300MB | ~300MB | 0.2 cores |
| llm-gateway | ~15MB | ~20MB | ~25MB | <0.1 cores |
| agent-runner | ~20MB | ~30MB | ~40MB | 0.1 cores |
| auth-bridge | ~10MB | ~15MB | ~15MB | <0.1 cores |
| llama.cpp (loaded) | ~2GB (mmap) | ~2GB | ~6GB (14B model) | 11 cores |
| **Subtotal** | **~245MB** | **~365MB** | **~6.4GB** | **11.4 cores** |

### Agent Session Overhead (per active session)

| Component | RAM | CPU | Disk |
|-----------|:---:|:---:|:----:|
| Agent container (idle) | ~50MB | 0 | ~500MB (image) |
| Agent container (inference) | ~500MB | 2-4 cores | — |
| Workspace data | — | — | Variable |

**Example: 3 concurrent agent sessions**
- Total idle: ~750MB + 245MB + (3 × 50MB) = **~1.1GB**
- Total active: ~750MB + 6.4GB + (3 × 500MB) = **~8.7GB** (fits in 23GB with ~14GB to spare)

### Model Fit Analysis

| Model | Inference RAM | Fits with baseline + studio? |
|-------|:----------:|:--------------------------:|
| qwen25-coder-7b | ~5.5GB | ✅ Comfortably |
| qwen3-coder-14b | ~9GB | ✅ Comfortably |
| codegemma-12b | ~8.5GB | ✅ Comfortably |
| qwen3-coder-30b-a3b | ~13GB | ✅ Yes (~10GB spare) |
| glm4-9b | ~6.5GB | ✅ Comfortably |
| qwen25-coder-32b | ~20GB | ⚠️ Tight |
| 3 agent sessions | ~6GB | ✅ Possible alongside 7B model |

---

## 12. Integration with Portal

### Docker Compose

```yaml
services:
  # ── existing services unchanged ──

  # ── Local LLM Gateway ──
  llm-gateway:
    build: ./code/llm-gateway
    ports:
      - "${BIND_IP:-0.0.0.0}:3100:3100"
    environment: &llm_env
      - GATEWAY_PORT=3100
      - MODELS_DIR=/models
      - LLAMACPP_BIN=llama-server
      - LLAMA_DEFAULT_MODEL=${LLAMA_DEFAULT_MODEL:-qwen25-coder-7b}
      - DATA_DIR=/data
      - DEEPSEEK_API_KEY=${DEXTER_DEEPSEEK_API_KEY:-}
      - GEMINI_API_KEY=${DEXTER_GOOGLE_API_KEY:-}
      - OPENAI_API_KEY=${DEXTER_OPENAI_API_KEY:-}
      - ANTHROPIC_API_KEY=${DEXTER_ANTHROPIC_API_KEY:-}
      - OPENROUTER_API_KEY=${DEXTER_OPENROUTER_API_KEY:-}
    volumes:
      - ${LLAMA_MODELS_DIR:-~/.local/share/llama-lab/models}:/models:ro
      - llm-studio-config:/data/config
      - llm-studio-logs:/data/logs
    networks:
      - trading-network
    restart: unless-stopped

  # ── Agent Runner ──
  agent-runner:
    build: ./code/agent-runner
    ports:
      - "${BIND_IP:-0.0.0.0}:3200:3200"
    environment:
      - RUNNER_PORT=3200
      - LLM_GATEWAY_URL=http://llm-gateway:3100
      - DATA_DIR=/data
      - DEFAULT_MODEL=${LLM_STUDIO_AGENT_DEFAULT_MODEL:-qwen25-coder-7b}
      - DEFAULT_AGENT_TYPE=${LLM_STUDIO_AGENT_DEFAULT_TYPE:-picoclaw}
      - AGENT_TIMEOUT=${LLM_STUDIO_AGENT_TIMEOUT:-86400}
      - AGENT_CPU_LIMIT=${LLM_STUDIO_AGENT_CPU_LIMIT:-4}
      - AGENT_MEMORY_LIMIT=${LLM_STUDIO_AGENT_MEMORY_LIMIT:-4G}
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:rw
      - llm-studio-workspaces:/data/workspaces
      - llm-studio-config:/data/config:ro
      - llm-studio-sessions:/data/sessions
      - llm-studio-logs:/data/logs
    networks:
      - trading-network
    restart: unless-stopped
    depends_on:
      - llm-gateway

  # ── Auth Bridge (SSO Proxy for Open WebUI) ──
  auth-bridge:
    build: ./code/auth-bridge
    ports:
      - "${BIND_IP:-0.0.0.0}:3300:3300"
    environment:
      - BRIDGE_PORT=3300
      - JWT_SECRET=${JWT_SECRET:-change-me-in-production}
      - OPEN_WEBUI_URL=http://open-webui:8080
      - OPEN_WEBUI_ADMIN_KEY=${WEBUI_ADMIN_KEY:-}
    networks:
      - trading-network
    restart: unless-stopped
    depends_on:
      - open-webui

  # ── Open WebUI (LLM Studio Frontend) ──
  open-webui:
    image: ghcr.io/open-webui/open-webui:main
    expose:
      - "8080"      # Internal only — access via auth-bridge
    environment:
      - OPENAI_API_BASE_URL=http://llm-gateway:3100/v1
      - WEBUI_SECRET_KEY=${WEBUI_SECRET_KEY:-change-me-in-production}
      - WEBUI_AUTH_TRUST_HEADERS=true
      - WEBUI_NAME=Frao LLM Studio
    volumes:
      - llm-studio-chat:/app/backend/data
    networks:
      - trading-network
    restart: unless-stopped

volumes:
  llm-studio-config:
    driver: local
    driver_opts:
      type: none
      device: /data/llm-studio/config
      o: bind
  llm-studio-workspaces:
    driver: local
    driver_opts:
      type: none
      device: /data/llm-studio/workspaces
      o: bind
  llm-studio-sessions:
  llm-studio-chat:
  llm-studio-logs:
    driver: local
    driver_opts:
      type: none
      device: /data/llm-studio/logs
      o: bind
```

### Nginx Routes

```nginx
# In company-portal/nginx.conf:

# LLM Gateway API
location /api/llm/ {
    proxy_pass http://llm-gateway:3100/;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_buffering off;
    proxy_cache off;
    proxy_read_timeout 86400s;
}

# Agent Runner API
location /api/agents/ {
    proxy_pass http://agent-runner:3200/;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_buffering off;
    proxy_cache off;
    proxy_read_timeout 86400s;
}

# Agent Terminal WebSocket (must match /api/agents/ for WS upgrade)
location /api/agents/ {
    proxy_pass http://agent-runner:3200/;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header Host $host;
    proxy_read_timeout 86400s;
}

# LLM Studio (Open WebUI via auth-bridge)
location /llm-studio/ {
    proxy_pass http://auth-bridge:3300/;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_buffering off;
    proxy_cache off;
    proxy_read_timeout 86400s;
}
```

### Portal UI Integration

Add to `state.ts`:

```typescript
export const TOOLS: Tool[] = [
  // ... existing tools ...

  { id: 'llm-studio', name: 'LLM Studio',
    description: 'Unified LLM interface — chat with DeepSeek, Gemini, ChatGPT, and local models.',
    icon: '🧠', href: '/llm-studio', minRole: 'user', category: 'platform', status: 'active' },

  { id: 'agents', name: 'Agent Terminal',
    description: 'Launch & interact with AI agents (picoclaw, pi.dev, opencode) in ephemeral containers.',
    icon: '⚡', href: '/agents', minRole: 'user', category: 'platform', status: 'beta' },
];
```

---

## 13. Implementation Roadmap

### Phase 1: Foundation (Week 1-2)

| Task | Component | Effort |
|------|-----------|--------|
| llm-gateway Go scaffolding | llm-gateway | 2 days |
| Model lifecycle (start/stop/health llama.cpp) | llm-gateway | 2 days |
| Endpoint config generator (env → endpoints.yml) | llm-gateway | 1 day |
| Docker Compose integration | DevOps | 1 day |
| Nginx routes | DevOps | 0.5 day |
| Portal tool entry | Frontend | 0.5 day |
| **Total** | | **~7 days** |

### Phase 2: Open WebUI & SSO (Week 2-3)

| Task | Component | Effort |
|------|-----------|--------|
| Deploy Open WebUI | DevOps | 1 day |
| Auth-bridge Go service (JWT validation + proxy) | auth-bridge | 2 days |
| User auto-provisioning (Open WebUI API) | auth-bridge | 1 day |
| Open WebUI endpoint auto-config | llm-gateway | 1 day |
| Theming & branding | DevOps | 0.5 day |
| **Total** | | **~5.5 days** |

### Phase 3: Agent Runner (Week 3-4)

| Task | Component | Effort |
|------|-----------|--------|
| Agent-runner Go scaffolding | agent-runner | 1 day |
| Docker container lifecycle (create/start/stop/delete) | agent-runner | 1 day |
| Agent images (picoclaw, pi, opencode) | agent-runner | 2 days |
| Terminal WebSocket protocol | agent-runner | 1 day |
| Session persistence (SQLite) | agent-runner | 1 day |
| Workspace volume management | agent-runner | 0.5 day |
| **Total** | | **~6.5 days** |

### Phase 4: Portal UI (Week 4-5)

| Task | Component | Effort |
|------|-----------|--------|
| LLM Studio iframe page (Open WebUI embed) | Frontend | 0.5 day |
| Agent session list + new session form | Frontend | 1 day |
| xterm.js terminal component | Frontend | 1.5 days |
| Model/agent selectors (populated from API) | Frontend | 0.5 day |
| Session toolbar (stop, restart, delete) | Frontend | 0.5 day |
| **Total** | | **~4 days** |

### Phase 5: Polish (Week 5-6)

| Task | Component | Effort |
|------|-----------|--------|
| Error handling & recovery paths | All | 1 day |
| Resource monitoring dashboard | Frontend | 1 day |
| Backup/retention automation | DevOps | 1 day |
| Documentation | Docs | 1 day |
| Load testing | QA | 1 day |
| Security audit | Security | 1 day |
| **Total** | | **~6 days** |

### Total Timeline

| Phase | Duration | Cumulative |
|-------|----------|------------|
| Phase 1: Foundation | 7 days | Week 1-2 |
| Phase 2: Open WebUI & SSO | 5.5 days | Week 2-3 |
| Phase 3: Agent Runner | 6.5 days | Week 3-4 |
| Phase 4: Portal UI | 4 days | Week 4-5 |
| Phase 5: Polish | 6 days | Week 5-6 |
| **Total** | **~29 days** | **~6 weeks** |

---

## Appendix A: Open WebUI Alternatives Considered

*(unchanged from previous analysis)*

## Appendix B: Existing Models Available

Currently downloaded (via llama-lab):
- `qwen25-coder-7b` (4.5GB Q4_K_M)
- `qwen3-coder-30b-a3b` (8GB Q4_K_M)

Recommended additional downloads for the studio:
- `qwen3-coder-14b` — Excellent all-purpose chat + coding
- `glm4-9b` — Strong bilingual model
- `codegemma-12b` — Gemma 3 12B, strong all-around

## Appendix C: Agent Image Build Contexts

```
code/agent-images/
├── picoclaw/
│   ├── Dockerfile
│   ├── entrypoint.sh
│   └── README.md
├── pi/
│   ├── Dockerfile
│   ├── entrypoint.sh
│   └── README.md
└── opencode/
    ├── Dockerfile
    ├── entrypoint.sh
    └── README.md
```

Each agent image is built with a shared pattern:
1. Minimal base image (debian-slim, alpine, python-slim)
2. Install agent binary (pre-built or package manager)
3. Copy entrypoint.sh (config injection + agent launch)
4. Set WORKDIR to /workspace (volume mount)
5. ENTRYPOINT to entrypoint.sh

Images are built by agent-runner on first launch or explicitly via `POST /v1/agents/:type/build`.

## Appendix D: Go Dependencies

```
# llm-gateway
github.com/go-chi/chi/v5          # HTTP router
github.com/go-chi/cors             # CORS middleware
gopkg.in/yaml.v3                   # YAML config generation

# agent-runner
github.com/go-chi/chi/v5          # HTTP router
github.com/gorilla/websocket       # WebSocket for terminal
github.com/creack/pty              # PTY multiplexing (alternative: docker exec)
github.com/docker/docker/client    # Official Docker SDK
github.com/docker/go-connections   # Docker connection helpers
modernc.org/sqlite                 # SQLite driver (CGo-free)

# auth-bridge
github.com/go-chi/chi/v5          # HTTP router
github.com/golang-jwt/jwt/v5       # JWT validation
```
