// ─────────────────────────────────────────────────────────────
// agent-runner — Ephemeral agent container lifecycle manager
// Part of the Frao Technologies LLM Studio
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

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/prhymera/llm-studio/code/agent-runner/internal/docker"
	"github.com/prhymera/llm-studio/code/agent-runner/internal/session"
)

const (
	defaultPort = "3200"
	version     = "0.1.0"
)

// ── Configuration ────────────────────────────────────────────

type Config struct {
	Port           string
	LLMGatewayURL  string
	DataDir        string
	DefaultModel   string
	DefaultAgent   string
	AgentTimeout   int // seconds
	AgentCPULimit  int
	AgentMemoryLimit string
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
	log.Printf("⚡ agent-runner v%s starting", version)
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
	})
	if err != nil {
		log.Fatalf("Failed to initialize Docker manager: %v", err)
	}

	// ── Router ──────────────────────────────────────────

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RealIP)
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

// ── Route Handlers (stubs) ──────────────────────────────────

func createSession(store *session.Store, dm *docker.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			AgentType      string `json:"agent_type"`
			Model          string `json:"model"`
			WorkspaceLabel string `json:"workspace_label"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}

		sess, err := dm.CreateSession(r.Context(), req.AgentType, req.Model, req.WorkspaceLabel)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}

		if err := store.Save(sess); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save session"})
			return
		}

		writeJSON(w, http.StatusCreated, sess)
	}
}

func listSessions(store *session.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessions, err := store.List()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"sessions": sessions,
			"total":    len(sessions),
		})
	}
}

func getSession(store *session.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		sess, err := store.Get(id)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
			return
		}
		writeJSON(w, http.StatusOK, sess)
	}
}

func deleteSession(store *session.Store, dm *docker.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if err := dm.DestroySession(r.Context(), id); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if err := store.Delete(id); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "session_id": id})
	}
}

func reconnectSession(store *session.Store, dm *docker.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		sess, err := dm.ReconnectSession(r.Context(), id)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if err := store.UpdateStatus(id, "running"); err != nil {
			log.Printf("Warning: failed to update session status: %v", err)
		}
		writeJSON(w, http.StatusOK, sess)
	}
}

func terminalWS(store *session.Store, dm *docker.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		// Upgrade to WebSocket and attach to container PTY
		dm.AttachTerminal(w, r, id)
	}
}

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
		// Proxy to llm-gateway
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
		writeJSON(w, http.StatusOK, map[string]string{"status": "healthy"})
	}
}

func status(store *session.Store, dm *docker.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessions, _ := store.List()
		agents := dm.ListAgentImages()
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"version":      version,
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
