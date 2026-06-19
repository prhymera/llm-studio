# Agent Runner WebSocket Terminal Protocol

Based on the Dexter terminal integration in the trading platform backend.

## WebSocket Endpoint

```
GET /v1/sessions/{session_id}/tty → WebSocket upgrade
```

## Protocol (binary/text)

### Frontend → Backend (WebSocket → Container)

| Type | Format | Description |
|------|--------|-------------|
| stdin | raw binary/text | Forwarded directly to container stdin |
| resize | `{"type":"resize","cols":80,"rows":24}` | Sent as JSON text; ignored if raw, triggers PTY resize |

### Backend → Frontend (Container → WebSocket)

| Type | Format | Description |
|------|--------|-------------|
| stdout | raw text/blob | Container stdout/stderr forwarded as-is via WebSocket text frames |
| error | `{"type":"error","message":"..."}` | JSON error message when session/container not found |

## Session Lifecycle

1. Frontend opens WebSocket to `/v1/sessions/{id}/tty`
2. Backend finds the running container for the session
3. Backend attaches to container's main process PTY (via `docker attach` SDK)
4. Data flows bidirectionally until either side closes
5. On close: container stays running (can reconnect)
6. On reconnect: new WebSocket → new PTY attach

## Reference: Dexter Implementation

The Dexter terminal uses an `actix_ws` WebSocket handler with:
- `stdin_tx`: Tokio channel sender for container stdin
- `stdout_rx`: Tokio channel receiver for container stdout
- `resize_tx`: Tokio channel sender for PTY resize events
- Docker exec with `AttachStdin`, `AttachStdout`, `Tty: true`

## Agent Containers

Agent containers (picoclaw, pi.dev) run the agent binary as their main process
via `/entrypoint.sh`. The entrypoint:
1. Writes agent config to `/workspace/.agent/config.json`
2. Displays session banner
3. Launches the agent binary (picoclaw) or drops to shell (pi.dev)
4. Falls back to `exec /bin/bash` (picoclaw) or `exec /bin/sh` (pi.dev) when agent exits

`ContainerAttach` connects to this main process, giving the user direct agent access.
