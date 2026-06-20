// ─────────────────────────────────────────────────────────────
// llm-gateway — Local LLM lifecycle manager + API proxy
// Part of the Frao Technologies LLM Studio
// ─────────────────────────────────────────────────────────────

package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prhymera/llm-studio/code/llm-gateway/internal/api"
	"github.com/prhymera/llm-studio/code/llm-gateway/internal/config"
	"github.com/prhymera/llm-studio/code/llm-gateway/internal/models"
)

const version = "0.1.0"

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("🧠 llm-gateway v%s starting", version)

	// Load configuration
	cfg := config.Load()
	log.Printf("   Port:       %d", cfg.Port)
	log.Printf("   Models dir: %s", cfg.ModelsDir)
	log.Printf("   Data dir:   %s", cfg.DataDir)
	log.Printf("   llama bin:  %s", cfg.LlamaBin)
	log.Printf("   Threads:    %d", cfg.ModelThreads)
	log.Printf("   Context:    %d", cfg.ModelContextSize)

	if len(cfg.RemoteAPIKeys) > 0 {
		providers := make([]string, 0, len(cfg.RemoteAPIKeys))
		for p := range cfg.RemoteAPIKeys {
			providers = append(providers, p)
		}
		log.Printf("   Remote providers: %v", providers)
	}

	// Initialize model registry
	reg, err := models.NewRegistry(cfg.ModelsDir)
	if err != nil {
		log.Printf("Warning: model registry init: %v", err)
	}

	// Register remote models
	registerRemoteModels(reg, cfg)

	log.Printf("   Models found: %d local, %d remote",
		len(reg.LocalModels()), len(reg.List())-len(reg.LocalModels()))

	// Create HTTP server
	srv := api.NewServer(cfg, reg)
	router := srv.Router()

	httpServer := &http.Server{
		Addr:         cfg.Addr(),
		Handler:      router,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 300 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Auto-load default model if specified
	if cfg.DefaultModel != "" {
		if _, ok := reg.Get(cfg.DefaultModel); ok {
			log.Printf("Auto-loading default model: %s", cfg.DefaultModel)
			go func() {
				addr := fmt.Sprintf("http://%s/v1/models/%s/load", cfg.Addr(), cfg.DefaultModel)
				// Retry up to 30s (server might not be ready immediately)
				for i := 0; i < 30; i++ {
					time.Sleep(1 * time.Second)
					resp, err := http.Post(addr, "application/json", nil)
					if err != nil {
						continue
					}
					resp.Body.Close()
					if resp.StatusCode == 200 {
						log.Printf("Default model %s loaded successfully", cfg.DefaultModel)
						return
					}
					if resp.StatusCode != 409 { // 409 = already loading/loaded
						log.Printf("Load model returned %d, retrying...", resp.StatusCode)
					}
				}
				log.Printf("Failed to auto-load model %s after 30s", cfg.DefaultModel)
			}()
		}
	}

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		log.Printf("Received signal %v, shutting down...", sig)

		// Unload any active model
		if active := reg.GetActiveModel(); active != "" {
			log.Printf("Unloading active model: %s", active)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := httpServer.Shutdown(ctx); err != nil {
			log.Printf("HTTP server shutdown error: %v", err)
		}
	}()

	log.Printf("Listening on %s", cfg.Addr())
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Server error: %v", err)
	}

	log.Println("Server stopped")
}

func registerRemoteModels(reg *models.Registry, cfg config.Config) {
	type providerConfig struct {
		key     string
		baseURL string
		models  []string
	}

	providers := []providerConfig{
		{key: "deepseek", baseURL: "https://api.deepseek.com/v1",
			models: []string{"deepseek-v4-pro", "deepseek-v4-flash", "deepseek-reasoner"}},
		{key: "gemini", baseURL: "https://generativelanguage.googleapis.com/v1beta/openai/",
			models: []string{"gemini-2.5-flash", "gemini-2.5-pro"}},
		{key: "openai", baseURL: "https://api.openai.com/v1",
			models: []string{"gpt-4o", "gpt-4-turbo"}},
		{key: "anthropic", baseURL: "https://api.anthropic.com/v1",
			models: []string{"claude-sonnet-4", "claude-opus-4"}},
		{key: "openrouter", baseURL: "https://openrouter.ai/api/v1",
			models: []string{"openrouter/anthropic/claude-sonnet-4", "openrouter/qwen/qwen-2.5-coder-32b-instruct"}},
		{key: "xai", baseURL: "https://api.x.ai/v1",
			models: []string{"grok-3", "grok-3-mini"}},
	}

	for _, p := range providers {
		if _, ok := cfg.RemoteAPIKeys[p.key]; ok {
			reg.AddRemoteModels(p.key, cfg.RemoteAPIKeys[p.key], p.baseURL, p.models)
		}
	}
}

// ensure Config has DataDir in exported form
func init() {
	_ = fmt.Sprintf("") // ensure fmt import
}
