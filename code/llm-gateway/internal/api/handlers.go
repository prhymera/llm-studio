// Package api provides HTTP handlers for the llm-gateway.
// It implements the OpenAI-compatible API surface plus model lifecycle endpoints.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/prhymera/llm-studio/code/llm-gateway/internal/config"
	"github.com/prhymera/llm-studio/code/llm-gateway/internal/llama"
	"github.com/prhymera/llm-studio/code/llm-gateway/internal/models"
)

const version = "0.1.0"

// Server holds the dependencies for HTTP handlers.
type Server struct {
	cfg     config.Config
	reg     *models.Registry
	llama   *llama.Process
	llamaMu chan struct{} // semaphore (size 1) for llama process transitions
}

// NewServer creates a Server with all dependencies.
func NewServer(cfg config.Config, reg *models.Registry) *Server {
	return &Server{
		cfg:     cfg,
		reg:     reg,
		llamaMu: make(chan struct{}, 1),
	}
}

// Router builds the HTTP handler with all routes.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()

	// Middleware
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(300 * time.Second))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"*"},
		AllowCredentials: true,
	}))

	// Health
	r.Get("/health", s.handleHealth)
	r.Get("/status", s.handleStatus)

	// OpenAI-compatible API
	r.Route("/v1", func(r chi.Router) {
		r.Get("/models", s.handleListModels)
		r.Get("/endpoints", s.handleEndpoints)
		r.Post("/chat/completions", s.handleChatCompletion)

		// Model-specific routes (single handler dispatches by method + path)
		r.Get("/models/*", s.handleModelGetWildcard)
		r.Post("/models/*", s.handleModelPostWildcard)
		r.Delete("/models/*", s.handleModelDeleteWildcard)
	})

	return r
}

// ── Handlers ─────────────────────────────────────────────────

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "healthy"})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	activeModel := s.reg.GetActiveModel()
	llamaState := "stopped"
	if s.llama != nil {
		llamaState = string(s.llama.State())
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"version":      version,
		"active_model": activeModel,
		"llama_state":  llamaState,
		"models":       len(s.reg.List()),
		"healthy":      true,
	})
}

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	allModels := s.reg.List()

	// Transform to OpenAI-compatible format
	data := make([]map[string]interface{}, 0, len(allModels))
	for _, m := range allModels {
		entry := map[string]interface{}{
			"id":       m.ID,
			"object":   "model",
			"created":  time.Now().Unix(),
			"owned_by": string(m.Provider),
		}
		if m.Provider == models.ProviderLocal {
			entry["status"] = string(m.Status)
		} else {
			entry["status"] = "ready"
		}
		data = append(data, entry)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"object": "list",
		"data":   data,
	})
}

// parseModelPath extracts the model name and optional action from a wildcard path.
// The chi wildcard `*` returns the path without a leading slash (e.g., "qwen25-coder-7b/status").
func parseModelPath(path string) (name, action string) {
	if path == "" {
		return "", ""
	}

	// Remove leading slash if present
	if len(path) > 0 && path[0] == '/' {
		path = path[1:]
	}

	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		possibleAction := path[idx+1:]
		switch possibleAction {
		case "load", "unload", "status", "download":
			return path[:idx], possibleAction
		}
	}
	return path, ""
}

func (s *Server) handleModelGetWildcard(w http.ResponseWriter, r *http.Request) {
	path := chi.URLParam(r, "*")
	name, action := parseModelPath(path)
	if name == "" {
		return
	}

	switch action {
	case "status":
		s.handleModelStatus(w, r, name)
	default:
		s.handleGetModel(w, r, name)
	}
}

func (s *Server) handleModelPostWildcard(w http.ResponseWriter, r *http.Request) {
	path := chi.URLParam(r, "*")
	name, action := parseModelPath(path)
	if name == "" {
		return
	}

	switch action {
	case "load":
		s.handleLoadModel(w, r, name)
	case "unload":
		s.handleUnloadModel(w, r, name)
	case "download":
		s.handleDownloadModel(w, r, name)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("model '%s' not found", name)})
	}
}

func (s *Server) handleModelDeleteWildcard(w http.ResponseWriter, r *http.Request) {
	path := chi.URLParam(r, "*")
	name, _ := parseModelPath(path)
	if name == "" {
		return
	}
	s.handleDeleteModel(w, r, name)
}

func (s *Server) handleGetModel(w http.ResponseWriter, r *http.Request, name string) {
	m, ok := s.reg.Get(name)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": fmt.Sprintf("model '%s' not found", name),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id":       m.ID,
		"object":   "model",
		"created":  time.Now().Unix(),
		"owned_by": string(m.Provider),
		"status":   string(m.Status),
		"size":     m.SizeBytes,
		"size_str": m.SizeHuman,
	})
}

func (s *Server) handleChatCompletion(w http.ResponseWriter, r *http.Request) {
	// Parse the request body
	var req struct {
		Model       string          `json:"model"`
		Messages    json.RawMessage `json:"messages"`
		Temperature float64         `json:"temperature"`
		MaxTokens   int             `json:"max_tokens"`
		Stream      bool            `json:"stream"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("invalid request: %v", err),
		})
		return
	}

	// Determine if the requested model is local or remote
	m, ok := s.reg.Get(req.Model)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": fmt.Sprintf("model '%s' not found", req.Model),
		})
		return
	}

	if m.Provider == models.ProviderLocal {
		// Route to local llama.cpp server
		if s.llama == nil || !s.llama.IsRunning() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error": "no local model is currently loaded",
			})
			return
		}
		s.proxyToLocal(w, r, &req)
	} else {
		// Route to remote endpoint
		s.proxyToRemote(w, r, &req, m)
	}
}

func (s *Server) proxyToLocal(w http.ResponseWriter, r *http.Request, req *struct {
	Model       string          `json:"model"`
	Messages    json.RawMessage `json:"messages"`
	Temperature float64         `json:"temperature"`
	MaxTokens   int             `json:"max_tokens"`
	Stream      bool            `json:"stream"`
}) {
	// Build the URL for the local llama.cpp server
	targetURL := fmt.Sprintf("http://%s:%d/v1/chat/completions",
		s.llama.Config().Host, s.llama.Config().Port)

	// Proxy the request (simplified — in production use httputil.ReverseProxy)
	proxyReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, r.Body)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "proxy error"})
		return
	}
	proxyReq.Header = r.Header.Clone()

	client := &http.Client{Timeout: 300 * time.Second}
	resp, err := client.Do(proxyReq)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": fmt.Sprintf("local model unavailable: %v", err),
		})
		return
	}
	defer resp.Body.Close()

	// Stream the response back
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)

	buf := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				break
			}
		}
		if err != nil {
			break
		}
	}
}

func (s *Server) proxyToRemote(w http.ResponseWriter, r *http.Request, req *struct {
	Model       string          `json:"model"`
	Messages    json.RawMessage `json:"messages"`
	Temperature float64         `json:"temperature"`
	MaxTokens   int             `json:"max_tokens"`
	Stream      bool            `json:"stream"`
}, m *models.Model) {
	apiKey, ok := s.cfg.RemoteAPIKeys[m.ProviderName]
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("API key not configured for provider '%s'", m.ProviderName),
		})
		return
	}

	baseURL := getProviderBaseURL(m.ProviderName)
	if baseURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("unknown provider '%s'", m.ProviderName),
		})
		return
	}

	targetURL := fmt.Sprintf("%s/chat/completions", baseURL)
	proxyReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, r.Body)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "proxy error"})
		return
	}

	proxyReq.Header = r.Header.Clone()
	proxyReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiKey))

	client := &http.Client{Timeout: 300 * time.Second}
	resp, err := client.Do(proxyReq)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": fmt.Sprintf("remote model unavailable: %v", err),
		})
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)

	buf := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				break
			}
		}
		if err != nil {
			break
		}
	}
}

func (s *Server) handleLoadModel(w http.ResponseWriter, r *http.Request, name string) {

	m, ok := s.reg.Get(name)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "model not found"})
		return
	}
	if m.Provider != models.ProviderLocal {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "can only load local models"})
		return
	}
	if m.Status == models.StatusReady {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "model already loaded"})
		return
	}

	// Acquire the semaphore (only one model can load at a time)
	select {
	case s.llamaMu <- struct{}{}:
		defer func() { <-s.llamaMu }()
	default:
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "another model is currently loading",
		})
		return
	}

	// Stop existing process if any
	if s.llama != nil && s.llama.IsRunning() {
		if err := s.llama.Stop(); err != nil {
			log.Printf("Error stopping previous model: %v", err)
		}
	}

	// Create new process
	llamaCfg := llama.DefaultConfig()
	llamaCfg.BinaryPath = s.cfg.LlamaBin
	llamaCfg.ModelPath = m.FilePath
	llamaCfg.Host = "127.0.0.1"
	llamaCfg.Port = 8080
	llamaCfg.ContextSize = s.cfg.ModelContextSize
	llamaCfg.Threads = s.cfg.ModelThreads
	llamaCfg.GPULayers = s.cfg.ModelGPULayers

	s.reg.SetStatus(name, models.StatusLoading)
	s.llama = llama.New(llamaCfg)

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	if err := s.llama.Start(ctx); err != nil {
		s.reg.SetStatus(name, models.StatusError)
		s.llama = nil
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to load model: %v", err),
		})
		return
	}

	s.reg.SetStatus(name, models.StatusReady)
	log.Printf("Model %s loaded successfully", name)

	writeJSON(w, http.StatusOK, map[string]string{
		"status": "loaded",
		"model":  name,
	})
}

func (s *Server) handleUnloadModel(w http.ResponseWriter, r *http.Request, name string) {

	if s.llama == nil || !s.llama.IsRunning() {
		writeJSON(w, http.StatusOK, map[string]string{"status": "no model loaded"})
		return
	}

	if err := s.llama.Stop(); err != nil {
		log.Printf("Error stopping model %s: %v", name, err)
	}

	s.reg.SetStatus(name, models.StatusUnloaded)
	s.llama = nil

	writeJSON(w, http.StatusOK, map[string]string{
		"status": "unloaded",
		"model":  name,
	})
}

func (s *Server) handleModelStatus(w http.ResponseWriter, r *http.Request, name string) {
	m, ok := s.reg.Get(name)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "model not found"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id":     m.ID,
		"status": m.Status,
		"loaded": m.Status == models.StatusReady,
	})
}

func (s *Server) handleDownloadModel(w http.ResponseWriter, r *http.Request, name string) {
	// TODO: Implement model download via llama-lab script
	writeJSON(w, http.StatusNotImplemented, map[string]string{
		"error":  "not implemented",
		"model":  name,
		"hint":   "use llama-lab.sh install <model>",
	})
}

func (s *Server) handleDeleteModel(w http.ResponseWriter, r *http.Request, name string) {

	// Unload if it's the active model
	if s.reg.GetActiveModel() == name {
		if s.llama != nil {
			s.llama.Stop()
			s.llama = nil
		}
	}

	// TODO: Remove model files from disk
	writeJSON(w, http.StatusNotImplemented, map[string]string{
		"error": "not implemented",
		"model": name,
	})
}

func (s *Server) handleEndpoints(w http.ResponseWriter, r *http.Request) {
	ec := s.reg.GetEndpointsConfig()
	writeJSON(w, http.StatusOK, ec)
}

// ── Helpers ─────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("Error encoding JSON response: %v", err)
	}
}

func getProviderBaseURL(providerName string) string {
	urls := map[string]string{
		"deepseek":   "https://api.deepseek.com/v1",
		"gemini":     "https://generativelanguage.googleapis.com/v1beta/openai/",
		"openai":     "https://api.openai.com/v1",
		"anthropic":  "https://api.anthropic.com/v1",
		"openrouter": "https://openrouter.ai/api/v1",
		"xai":        "https://api.x.ai/v1",
	}
	return urls[providerName]
}
