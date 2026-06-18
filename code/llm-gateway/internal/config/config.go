// Package config provides configuration loading for the llm-gateway.
// Configuration is loaded from environment variables with sensible defaults.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds all configuration for the llm-gateway service.
type Config struct {
	// Port for the HTTP server
	Port int

	// ModelsDir is the path to the directory containing GGUF model files.
	ModelsDir string

	// LlamaBin is the path to the llama-server binary.
	LlamaBin string

	// DataDir is the path for generated config files and logs.
	DataDir string

	// DefaultModel is the model to load on startup.
	DefaultModel string

	// RemoteAPIKeys maps provider names to their API keys.
	RemoteAPIKeys map[string]string

	// ModelContextSize is the default context size for local models.
	ModelContextSize int

	// ModelThreads is the number of CPU threads for local inference.
	ModelThreads int

	// ModelGPULayers is the number of layers to offload to GPU (0 for CPU-only).
	ModelGPULayers int

	// BindIP is the IP address to bind the HTTP server to.
	BindIP string
}

// Load reads configuration from environment variables.
func Load() Config {
	cfg := Config{
		Port:            envInt("GATEWAY_PORT", 3100),
		ModelsDir:       envStr("MODELS_DIR", defaultModelsDir()),
		LlamaBin:        envStr("LLAMACPP_BIN", "llama-server"),
		DataDir:         envStr("DATA_DIR", "/data"),
		DefaultModel:    envStr("DEFAULT_MODEL", ""),
		RemoteAPIKeys:   loadAPIKeys(),
		ModelContextSize: envInt("MODEL_CONTEXT_SIZE", 8192),
		ModelThreads:    envInt("MODEL_THREADS", defaultThreadCount()),
		ModelGPULayers:  envInt("MODEL_GPU_LAYERS", 0),
		BindIP:          envStr("BIND_IP", "0.0.0.0"),
	}
	return cfg
}

// Addr returns the address string for the HTTP server.
func (c Config) Addr() string {
	return fmt.Sprintf("%s:%d", c.BindIP, c.Port)
}

// KnownProviders returns the list of supported remote LLM providers.
func KnownProviders() []string {
	return []string{
		"deepseek",
		"gemini",
		"openai",
		"anthropic",
		"openrouter",
		"xai",
	}
}

func defaultModelsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/models"
	}
	return fmt.Sprintf("%s/.local/share/llama-lab/models", home)
}

func defaultThreadCount() int {
	// Try to detect CPU count, fall back to 4
	contents, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return 4
	}
	count := strings.Count(string(contents), "processor\t:")
	if count < 1 {
		return 4
	}
	return count - 1 // leave one for the OS
}

func loadAPIKeys() map[string]string {
	keys := make(map[string]string)
	providers := KnownProviders()
	for _, p := range providers {
		envVar := fmt.Sprintf("%s_API_KEY", strings.ToUpper(p))
		if val := os.Getenv(envVar); val != "" {
			keys[p] = val
		}
	}
	return keys
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
