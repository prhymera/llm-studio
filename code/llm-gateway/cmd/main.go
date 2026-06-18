// ─────────────────────────────────────────────────────────────
// llm-gateway — Local LLM lifecycle manager + API proxy
// Part of the Frao Technologies LLM Studio
// ─────────────────────────────────────────────────────────────

package main

import (
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
)

const (
	defaultPort = "3100"
	version     = "0.1.0"
)

// ── Configuration ────────────────────────────────────────────

type Config struct {
	Port       string `json:"port"`
	ModelsDir  string `json:"models_dir"`
	LlamaBin   string `json:"llama_bin"`
	DeepSeekAK string `json:"deepseek_api_key"`
	GeminiAK   string `json:"gemini_api_key"`
	OpenAIK    string `json:"openai_api_key"`
}

func loadConfig() Config {
	return Config{
		Port:       envOrDefault("GATEWAY_PORT", defaultPort),
		ModelsDir:  envOrDefault("MODELS_DIR", os.ExpandEnv("$HOME/.local/share/llama-lab/models")),
		LlamaBin:   envOrDefault("LLAMACPP_BIN", "llama-server"),
		DeepSeekAK: os.Getenv("DEEPSEEK_API_KEY"),
		GeminiAK:   os.Getenv("GEMINI_API_KEY"),
		OpenAIK:    os.Getenv("OPENAI_API_KEY"),
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ── Health / Status ──────────────────────────────────────────

type StatusResponse struct {
	Version string         `json:"version"`
	Models  []ModelSummary `json:"models"`
	Healthy bool           `json:"healthy"`
}

type ModelSummary struct {
	Name     string `json:"name"`
	Loaded   bool   `json:"loaded"`
	SizeGB   string `json:"size_gb,omitempty"`
	Status   string `json:"status"` // "unloaded" | "loading" | "ready" | "error"
}

// ── Main ─────────────────────────────────────────────────────

func main() {
	cfg := loadConfig()
	log.Printf("🧠 llm-gateway v%s starting", version)
	log.Printf("   Port:       %s", cfg.Port)
	log.Printf("   Models dir: %s", cfg.ModelsDir)
	log.Printf("   llama bin:  %s", cfg.LlamaBin)

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
		r.Get("/models", listModels(cfg))
		r.Get("/models/{name}", getModel(cfg))
		r.Post("/models/{name}/load", loadModel(cfg))
		r.Post("/models/{name}/unload", unloadModel(cfg))
		r.Post("/chat/completions", chatCompletion(cfg))
	})

	r.Get("/health", health(cfg))
	r.Get("/status", status(cfg))

	// ── Graceful Shutdown ───────────────────────────────

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Println("Shutting down...")
		// TODO: unload any running model
		os.Exit(0)
	}()

	addr := fmt.Sprintf(":%s", cfg.Port)
	log.Printf("Listening on %s", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

// ── Route Handlers (stubs) ──────────────────────────────────

func listModels(cfg Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"object": "list",
			"data":   []map[string]string{},
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func getModel(cfg Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		writeJSON(w, http.StatusOK, map[string]string{
			"id":     name,
			"object": "model",
			"status": "unloaded",
		})
	}
}

func loadModel(cfg Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		// TODO: spawn llama-server with this model
		writeJSON(w, http.StatusAccepted, map[string]string{
			"status":  "loading",
			"model":   name,
			"message": fmt.Sprintf("Starting model: %s", name),
		})
	}
}

func unloadModel(cfg Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		// TODO: kill llama-server
		writeJSON(w, http.StatusOK, map[string]string{
			"status":  "unloaded",
			"model":   name,
			"message": fmt.Sprintf("Stopped model: %s", name),
		})
	}
}

func chatCompletion(cfg Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// TODO: proxy to local llama-server or remote endpoint
		writeJSON(w, http.StatusNotImplemented, map[string]string{
			"error": "not implemented yet",
		})
	}
}

func health(cfg Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{
			"status": "healthy",
		})
	}
}

func status(cfg Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, StatusResponse{
			Version: version,
			Models:  []ModelSummary{},
			Healthy: true,
		})
	}
}

// ── Helpers ─────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
