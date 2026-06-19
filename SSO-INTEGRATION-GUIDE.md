# LLM Studio SSO Integration Guide

> **Last updated:** 2026-06-19
> **Applies to:** Frao Technologies Portal → Open WebUI via auth-bridge

## Architecture

```
User Browser
    │
    ├── Portal (10.64.0.5:80)  ← SvelteKit SPA, served by nginx
    │     │
    │     ├── /llm-studio/  ──→  nginx proxy ──→ 10.64.0.5:3300 (auth-bridge)
    │     │                                              │
    │     │            POST /api/v1/auths/signin (auto, with X-User-* headers)
    │     │                                              │
    │     │                                    open-webui:8080
    │     │                                      WEBUI_AUTH_TRUSTED_EMAIL_HEADER=X-User-Email
    │     │
    │     └── LLM Studio tool href ──→ http://10.64.0.5:3300 (direct to auth-bridge)
    │
    ├── auth-bridge:3300
    │     ├── validateSession()     → backend /api/v1/auth/me (forwards "id" cookie)
    │     ├── provisionUserDB()     → direct SQLite write to OWUI's webui.db
    │     ├── owuiSignin()          → POST /api/v1/auths/signin with X-User-* headers
    │     │                            Returns JWT token cookie
    │     └── cookieInjector        → Wraps every response with Set-Cookie: token=...
    │
    └── open-webui:3001/8080
          ├── WEBUI_AUTH_TRUSTED_EMAIL_HEADER=X-User-Email  ← CRITICAL
          ├── WEBUI_AUTH_TRUSTED_NAME_HEADER=X-User-Name
          └── Reads token cookie from browser → no login form shown
```

## Critical Configuration Points

### 1. Open WebUI Env Vars (docker-compose.yml)

```yaml
open-webui:
  environment:
    # ❌ WRONG — this boolean alone does nothing:
    - WEBUI_AUTH_TRUST_HEADERS=true

    # ✅ CORRECT — must specify the actual header names:
    - WEBUI_AUTH_TRUSTED_EMAIL_HEADER=X-User-Email
    - WEBUI_AUTH_TRUSTED_NAME_HEADER=X-User-Name
```

**Why:** Open WebUI only processes trusted headers on the **`POST /api/v1/auths/signin`** endpoint — not as middleware on every request. The `WEBUI_AUTH_TRUSTED_EMAIL_HEADER` env var tells the signin handler to look for a specific HTTP header containing the user's email. Without this, the login form always appears.

The boolean `WEBUI_AUTH_TRUST_HEADERS=true` is effectively useless by itself.

### 2. Auth Headers Must Match Exactly

| Auth-bridge sets | OWUI env var | Must match |
|---|---|---|
| `X-User-Email` | `WEBUI_AUTH_TRUSTED_EMAIL_HEADER=X-User-Email` | ✅ |
| `X-User-Name` | `WEBUI_AUTH_TRUSTED_NAME_HEADER=X-User-Name` | ✅ |
| `X-User-Id` | (used internally) | — |
| `X-User-Role` | (via `WEBUI_AUTH_TRUSTED_ROLE_HEADER` if set) | — |

### 3. Auto-Signin is Required

Even with correct trusted headers, the SPA shows the login form because no JWT token exists on first load. The auth-bridge must **automatically call the signin endpoint** with the trusted headers, capture the returned JWT cookie, and inject it into every response.

The flow is in `auth-bridge/cmd/main.go`:

```go
// After validating portal session and provisioning user:
tokenCookie := owuiSignin(user, cfg.OpenWebUIURL)
// ...
w = &cookieInjector{ResponseWriter: w, cookie: tokenCookie}
proxy.ServeHTTP(w, r)
```

The `cookieInjector` response wrapper adds `Set-Cookie: token=...` to every proxied response. This is done via a per-request wrapper rather than `proxy.ModifyResponse` to avoid **race conditions** with shared proxy state.

### 4. Database Provisioning

The auth-bridge writes users **directly to Open WebUI's SQLite database** via a shared Docker volume (`open-webui-data`):

```yaml
auth-bridge:
  volumes:
    - open-webui-data:/owui-data:rw
```

The `provisionUserDB` function:
1. Checks if user exists by email
2. If exists: updates role if needed
3. If not exists: inserts into `user` and `auth` tables
4. Uses WAL journal mode for concurrent access with Open WebUI

This replaces the broken `OPEN_WEBUI_ADMIN_KEY` API key approach (the `api_key` table entries don't work as Bearer tokens for Open WebUI's REST API).

### 5. IP Binding — MOST COMMON FAILURE

Every `docker compose up` command **must** explicitly pass `BIND_IP`:

```bash
# ✅ CORRECT — overrides any shell/env BIND_IP:
BIND_IP=10.64.0.5 docker compose up -d auth-bridge --force-recreate

# ❌ WRONG — picks up shell's BIND_IP which might differ:
docker compose up -d auth-bridge --force-recreate
```

**Why:** The `.env` file in `llm-studio/` has `BIND_IP=10.64.0.5`, but the shell or parent `.env` might have a different `BIND_IP` (e.g., `10.128.0.5`). Docker Compose merges env vars from multiple sources, and the shell takes precedence over `.env`.

**Symptom of wrong bind IP:**
- `docker port llm-studio-auth-bridge-1` shows `3300/tcp -> 10.128.0.5:3300` instead of `10.64.0.5:3300`
- Browser shows `ERR_CONNECTION_REFUSED` on `10.64.0.5:3300`
- Nginx proxy returns `502 Bad Gateway` because nginx.conf points to `10.64.0.5` but service is on `10.128.0.5`

**Fix all services at once:**
```bash
cd /home/rprimera/prhyme/projects/llm-studio
BIND_IP=10.64.0.5 docker compose up -d --force-recreate
```

Then verify:
```bash
docker port llm-studio-auth-bridge-1   # Must show 10.64.0.5:3300
docker port llm-studio-agent-runner-1  # Must show 10.64.0.5:3200
docker port llm-studio-llm-gateway-1   # Must show 10.64.0.5:3100
```

### 6. Nginx Proxy IP Must Match

The `nginx.conf` in the company-portal has hardcoded upstream IPs. When llm-studio services move between IPs, the nginx config must be updated too:

```nginx
# In company-portal/nginx.conf:
location /api/agents/ {
    proxy_pass http://10.64.0.5:3200/;  # Must match docker port output
}
location /llm-studio/ {
    proxy_pass http://10.64.0.5:3300/;  # Must match docker port output
}
location /api/llm/ {
    proxy_pass http://10.64.0.5:3100/;  # Must match docker port output
}
location /static/ {
    proxy_pass http://10.64.0.5:3001;   # Must match docker port output
}
```

After changing nginx.conf, restart the company-portal:
```bash
docker rm -f hedgefund_dev-company-portal-1
cd /home/rprimera/prhyme/projects/hedgefund
COMPOSE_PROJECT_NAME=hedgefund_dev BIND_IP=10.64.0.5 docker compose \
  -f docker-compose.yml -f docker-compose.dev.yml \
  --env-file .env.dev up -d company-portal --force-recreate
```

### 7. Session Cookie Name

The portal backend (Rust/actix-web) sets a session cookie named `id` (not `actix-session`), via `IdentityMiddleware::default()`. The auth-bridge's `validateSession()` copies **all** cookies from the incoming request, which includes this `id` cookie.

```go
// auth-bridge — copies ALL cookies, including "id":
for _, c := range r.Cookies() {
    req.AddCookie(c)
}
```

### 8. Backend URL for Session Validation

The auth-bridge calls the portal backend at `BACKEND_URL=http://10.64.0.5:8080` to validate sessions. This must match the portal backend's published port.

```yaml
auth-bridge:
  environment:
    BACKEND_URL: "http://10.64.0.5:8080"
```

The backend publishes port 8080 to `10.64.0.5:8080` (from docker-compose: `ports: "${BIND_IP}:${BACKEND_PORT:-8080}:8080"`).

## Verification Checklist

After any deployment, run:

```bash
# 1. All services on correct IP
docker port llm-studio-auth-bridge-1   | grep "10.64.0.5"
docker port llm-studio-agent-runner-1  | grep "10.64.0.5"
docker port llm-studio-llm-gateway-1   | grep "10.64.0.5"
docker port llm-studio-open-webui-1    | grep "10.64.0.5"

# 2. Auth-bridge healthy from host
curl -s -o /dev/null -w "%{http_code}" http://10.64.0.5:3300/health

# 3. Nginx proxy routes working
curl -s -o /dev/null -w "%{http_code}" http://10.64.0.5:80/api/agents/health
curl -s -o /dev/null -w "%{http_code}" http://10.64.0.5:80/llm-studio/health
curl -s -o /dev/null -w "%{http_code}" http://10.64.0.5:80/api/llm/health

# 4. Open WebUI has trusted headers configured
docker inspect llm-studio-open-webui-1 --format '{{range .Config.Env}}{{println .}}{{end}}' | grep TRUSTED

# 5. Token cookie is injected
COOKIE=$(curl -s -v -X POST http://10.64.0.5:8080/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"email":"admin@platform.com","password":"ChangeMe123!"}' 2>&1 | \
  grep -oP 'id=[^;]+' | head -1)
curl -s -v -H "Cookie: $COOKIE" http://10.64.0.5:3300/ 2>&1 | grep "Set-Cookie.*token"

# 6. Users exist in Open WebUI DB
docker exec llm-studio-auth-bridge-1 sh -c \
  "sqlite3 /owui-data/webui.db 'SELECT email, role FROM \"user\"'"
```

## Common Failure Modes

| Symptom | Root Cause | Fix |
|---------|-----------|-----|
| `ERR_CONNECTION_REFUSED` on `10.64.0.5:3300` | BIND_IP mismatch — service bound to wrong IP | Recreate with `BIND_IP=10.64.0.5` |
| Nginx `502 Bad Gateway` on `/llm-studio/` | nginx.conf points to wrong upstream IP | Update nginx.conf to match actual service IP |
| Open WebUI shows login form | `WEBUI_AUTH_TRUSTED_EMAIL_HEADER` not set | Add env var to docker-compose |
| Open WebUI shows "Get Started" page | No users in OWUI database | auth-bridge provisionUserDB not running or DB path wrong |
| Token cookie not set in browser | `cookieInjector` not wrapping response | Check auth-bridge logs for signin errors |
| Session validation fails | Portal `id` cookie not forwarded | Check `validateSession` copies all cookies |
| Agent terminal "Connected" but blank | Container exited (picoclaw error + `set -e`) | Entrypoint must use `set +e` and `exec /bin/bash` |
