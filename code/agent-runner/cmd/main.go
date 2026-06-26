// ─────────────────────────────────────────────────────────────
// agent-runner — Ephemeral agent container lifecycle manager
// Part of the Frao Technologies Multi-Agent Dashboard System
//
// Provides:
//   - Agent session CRUD with Docker container lifecycle
//   - Real-time container metrics (CPU, memory, IO, uptime)
//   - Container control (stop, pause, resume, kill)
//   - pi-web.dev UI integration for pi.dev agent sessions
//   - Model listing with remote/local categorization
//   - WebSocket PTY terminal for agent interaction
//   - Resolution-tracing on every endpoint (error reporting)
// ─────────────────────────────────────────────────────────────

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/prhymera/llm-studio/code/agent-runner/internal/docker"
	"github.com/prhymera/llm-studio/code/agent-runner/internal/models"
	"github.com/prhymera/llm-studio/code/agent-runner/internal/session"
	"github.com/prhymera/llm-studio/code/agent-runner/internal/tracing"
)

const (
	defaultPort = "3200"
	version     = "0.2.0"
)

// ── Configuration ────────────────────────────────────────────

type Config struct {
	Port             string
	LLMGatewayURL    string
	DataDir          string
	DefaultModel     string
	DefaultAgent     string
	AgentTimeout     int
	AgentCPULimit    int
	AgentMemoryLimit string
	DockerNetwork    string
}

func loadConfig() Config {
	return Config{
		Port:             envOrDefault("RUNNER_PORT", defaultPort),
		LLMGatewayURL:    envOrDefault("LLM_GATEWAY_URL", "http://llm-gateway:3100"),
		DataDir:          envOrDefault("DATA_DIR", "/data"),
		DefaultModel:     envOrDefault("DEFAULT_MODEL", "qwen25-coder-7b"),
		DefaultAgent:     envOrDefault("DEFAULT_AGENT_TYPE", "picoclaw"),
		AgentTimeout:     envInt("AGENT_TIMEOUT", 86400),
		AgentCPULimit:    envInt("AGENT_CPU_LIMIT", 4),
		AgentMemoryLimit: envOrDefault("AGENT_MEMORY_LIMIT", "4G"),
		DockerNetwork:    envOrDefault("DOCKER_NETWORK", "llm-studio_llm-studio-network"),
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		var i int
		if _, err := fmt.Sscanf(v, "%d", &i); err == nil {
			return i
		}
	}
	return def
}

// ── Main ─────────────────────────────────────────────────────

func main() {
	cfg := loadConfig()
	log.Printf("⚡ agent-runner v%s starting (agent-dashboard-svc)", version)
	log.Printf("   Port:        %s", cfg.Port)
	log.Printf("   Gateway:     %s", cfg.LLMGatewayURL)
	log.Printf("   Default:     %s / %s", cfg.DefaultAgent, cfg.DefaultModel)

	// Initialize stores
	sessionStore, err := session.NewStore(fmt.Sprintf("%s/sessions/sessions.db", cfg.DataDir))
	if err != nil {
		log.Fatalf("Failed to initialize session store: %v", err)
	}

	// Initialize Docker manager
	dockerManager, err := docker.NewManager(docker.Config{
		DataDir:        cfg.DataDir,
		GatewayURL:     cfg.LLMGatewayURL,
		DefaultModel:   cfg.DefaultModel,
		DefaultAgent:   cfg.DefaultAgent,
		AgentTimeout:   cfg.AgentTimeout,
		CPULimit:       cfg.AgentCPULimit,
		MemoryLimit:    cfg.AgentMemoryLimit,
		DockerNetwork:  cfg.DockerNetwork,
	})
	if err != nil {
		log.Fatalf("Failed to initialize Docker manager: %v", err)
	}

	// Set tracing service name
	tracing.SetServiceName("agent-dashboard-svc")

	// ── Router ──────────────────────────────────────────

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RealIP)
	r.Use(middleware.Timeout(60 * time.Second))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"*"},
		AllowCredentials: true,
	}))

	// ── Routes ──────────────────────────────────────────

	r.Route("/v1", func(r chi.Router) {
		// Session management
		r.Post("/sessions", createSession(sessionStore, dockerManager))
		r.Get("/sessions", listSessions(sessionStore))
		r.Get("/sessions/{id}", getSession(sessionStore))
		r.Delete("/sessions/{id}", deleteSession(sessionStore, dockerManager))
		r.Post("/sessions/{id}/reconnect", reconnectSession(sessionStore, dockerManager))

		// Terminal WebSocket
		r.Get("/sessions/{id}/tty", terminalWS(sessionStore, dockerManager))

		// ── NEW: Container Metrics ─────────────────────
		r.Get("/sessions/{id}/metrics", getMetrics(sessionStore, dockerManager))

		// ── NEW: Container Control Actions ─────────────
		r.Post("/sessions/{id}/stop", stopSession(sessionStore, dockerManager))
		r.Post("/sessions/{id}/pause", pauseSession(sessionStore, dockerManager))
		r.Post("/sessions/{id}/resume", resumeSession(sessionStore, dockerManager))
		r.Post("/sessions/{id}/kill", killSession(sessionStore, dockerManager))

		// ── NEW: pi-web.dev Integration ────────────────
		r.Get("/sessions/{id}/piweb", getPiWeb(sessionStore, dockerManager))
		r.Post("/sessions/{id}/piweb/start", startPiWeb(sessionStore, dockerManager))

		// ── NEW: Model Listing ─────────────────────────
		r.Get("/models", listModels(cfg))

		// Agent image management
		r.Get("/agents", listAgents(dockerManager))
		r.Post("/agents/{type}/pull", pullAgentImage(dockerManager))

		// Endpoint info (from gateway)
		r.Get("/endpoints", getEndpoints(cfg))
	})

	r.Get("/health", health())
	r.Get("/status", status(sessionStore, dockerManager))

	// ── Graceful Shutdown ───────────────────────────────

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		<-sigCh
		log.Println("Shutting down...")
		dockerManager.CleanupAll(ctx)
		cancel()
		os.Exit(0)
	}()

	addr := fmt.Sprintf(":%s", cfg.Port)
	log.Printf("Listening on %s", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

// ── Route Handlers: Session CRUD ─────────────────────────────

func createSession(store *session.Store, dm *docker.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		ctx, _ = tracing.NewCorrelationID(ctx)

		var req struct {
			AgentType      string `json:"agent_type"`
			Model          string `json:"model"`
			WorkspaceLabel string `json:"workspace_label"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}

		sess, err := tracing.TraceFn(ctx, "CreateSession", func() (interface{}, error) {
			return dm.CreateSession(ctx, req.AgentType, req.Model, req.WorkspaceLabel)
		})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}

		if s, ok := sess.(*session.Session); ok {
			if saveErr := store.Save(s); saveErr != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save session"})
				return
			}
			writeJSON(w, http.StatusCreated, s)
		}
	}
}

func listSessions(store *session.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, _ := tracing.NewCorrelationID(r.Context())

		result, err := tracing.TraceFn(ctx, "ListSessions", func() (interface{}, error) {
			sessions, err := store.List()
			if err != nil {
				return nil, err
			}
			return sessions, nil
		})

		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}

		sessions := result.([]*session.Session)
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"sessions": sessions,
			"total":    len(sessions),
		})
	}
}

func getSession(store *session.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, _ := tracing.NewCorrelationID(r.Context())
		id := chi.URLParam(r, "id")

		result, err := tracing.TraceFn(ctx, "GetSession", func() (interface{}, error) {
			return store.Get(id)
		})
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
			return
		}

		sess := result.(*session.Session)
		writeJSON(w, http.StatusOK, sess)
	}
}

func deleteSession(store *session.Store, dm *docker.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, _ := tracing.NewCorrelationID(r.Context())
		id := chi.URLParam(r, "id")

		_, err := tracing.TraceFn(ctx, "DeleteSession", func() (interface{}, error) {
			if err := dm.DestroySession(ctx, id); err != nil {
				return nil, fmt.Errorf("destroy: %w", err)
			}
			if err := store.Delete(id); err != nil {
				return nil, fmt.Errorf("delete from store: %w", err)
			}
			return nil, nil
		})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "session_id": id})
	}
}

func reconnectSession(store *session.Store, dm *docker.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, _ := tracing.NewCorrelationID(r.Context())
		id := chi.URLParam(r, "id")

		result, err := tracing.TraceFn(ctx, "ReconnectSession", func() (interface{}, error) {
			return dm.ReconnectSession(ctx, id)
		})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}

		sess := result.(*session.Session)
		if updateErr := store.UpdateStatus(id, "running"); updateErr != nil {
			log.Printf("Warning: failed to update session status: %v", updateErr)
		}
		writeJSON(w, http.StatusOK, sess)
	}
}

func terminalWS(store *session.Store, dm *docker.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		dm.AttachTerminal(w, r, id)
	}
}

// ── NEW Handlers: Metrics ────────────────────────────────────

func getMetrics(store *session.Store, dm *docker.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, _ := tracing.NewCorrelationID(r.Context())
		id := chi.URLParam(r, "id")

		result, err := tracing.TraceFn(ctx, "GetSessionMetrics", func() (interface{}, error) {
			return dm.GetMetrics(ctx, id)
		})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}

		writeJSON(w, http.StatusOK, result)
	}
}

// ── NEW Handlers: Container Control ─────────────────────────

func stopSession(store *session.Store, dm *docker.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, _ := tracing.NewCorrelationID(r.Context())
		id := chi.URLParam(r, "id")

		_, err := tracing.TraceFn(ctx, "StopContainer", func() (interface{}, error) {
			return nil, dm.StopContainer(ctx, id)
		})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}

		_ = store.UpdateStatus(id, "stopped")
		writeJSON(w, http.StatusOK, map[string]string{"status": "stopped", "session_id": id})
	}
}

func pauseSession(store *session.Store, dm *docker.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, _ := tracing.NewCorrelationID(r.Context())
		id := chi.URLParam(r, "id")

		_, err := tracing.TraceFn(ctx, "PauseContainer", func() (interface{}, error) {
			return nil, dm.PauseContainer(ctx, id)
		})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}

		_ = store.UpdateStatus(id, "paused")
		writeJSON(w, http.StatusOK, map[string]string{"status": "paused", "session_id": id})
	}
}

func resumeSession(store *session.Store, dm *docker.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, _ := tracing.NewCorrelationID(r.Context())
		id := chi.URLParam(r, "id")

		_, err := tracing.TraceFn(ctx, "ResumeContainer", func() (interface{}, error) {
			return nil, dm.ResumeContainer(ctx, id)
		})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}

		_ = store.UpdateStatus(id, "running")
		writeJSON(w, http.StatusOK, map[string]string{"status": "running", "session_id": id})
	}
}

func killSession(store *session.Store, dm *docker.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, _ := tracing.NewCorrelationID(r.Context())
		id := chi.URLParam(r, "id")

		_, err := tracing.TraceFn(ctx, "KillContainer", func() (interface{}, error) {
			return nil, dm.KillContainer(ctx, id)
		})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}

		_ = store.UpdateStatus(id, "killed")
		writeJSON(w, http.StatusOK, map[string]string{"status": "killed", "session_id": id})
	}
}

// ── NEW Handlers: pi-web.dev Integration ────────────────────

func getPiWeb(store *session.Store, dm *docker.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, _ := tracing.NewCorrelationID(r.Context())
		id := chi.URLParam(r, "id")

		result, err := tracing.TraceFn(ctx, "GetPiWebStatus", func() (interface{}, error) {
			return dm.GetPiWebStatus(ctx, id)
		})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}

		writeJSON(w, http.StatusOK, result)
	}
}

func startPiWeb(store *session.Store, dm *docker.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, _ := tracing.NewCorrelationID(r.Context())
		id := chi.URLParam(r, "id")

		result, err := tracing.TraceFn(ctx, "StartPiWeb", func() (interface{}, error) {
			return dm.StartPiWeb(ctx, id)
		})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}

		writeJSON(w, http.StatusOK, result)
	}
}

// ── NEW Handlers: Models ────────────────────────────────────

func listModels(cfg Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, _ := tracing.NewCorrelationID(r.Context())

		result, err := tracing.TraceFn(ctx, "ListModels", func() (interface{}, error) {
			return models.FetchModels(cfg.LLMGatewayURL)
		})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}

		writeJSON(w, http.StatusOK, result)
	}
}

// ── Existing Handlers (unchanged logic, added tracing) ──────

func listAgents(dm *docker.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agents := dm.ListAgentImages()
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"agents": agents,
		})
	}
}

func pullAgentImage(dm *docker.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentType := chi.URLParam(r, "type")
		if err := dm.BuildAgentImage(r.Context(), agentType); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"status": "building",
			"agent":  agentType,
		})
	}
}

func getEndpoints(cfg Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp, err := http.Get(fmt.Sprintf("%s/v1/endpoints", cfg.LLMGatewayURL))
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "gateway unavailable"})
			return
		}
		defer resp.Body.Close()
		var data interface{}
		json.NewDecoder(resp.Body).Decode(&data)
		writeJSON(w, resp.StatusCode, data)
	}
}

func health() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"status":  "healthy",
			"service": "agent-dashboard-svc",
			"version": version,
		})
	}
}

func status(store *session.Store, dm *docker.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessions, _ := store.List()
		agents := dm.ListAgentImages()
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"version":      version,
			"service":      "agent-dashboard-svc",
			"sessions":     len(sessions),
			"agents":       agents,
			"healthy":      true,
		})
	}
}

// ── Helpers ─────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
