package config

import (
	"os"
	"testing"
)

func TestLoad_Defaults(t *testing.T) {
	// Clear env vars that might interfere
	clearEnvVars()
	defer clearEnvVars()

	cfg := Load()

	if cfg.Port != 3100 {
		t.Errorf("expected default port 3100, got %d", cfg.Port)
	}
	if cfg.LlamaBin != "llama-server" {
		t.Errorf("expected default llama-server, got %s", cfg.LlamaBin)
	}
	if cfg.ModelContextSize != 8192 {
		t.Errorf("expected default context size 8192, got %d", cfg.ModelContextSize)
	}
	if cfg.ModelGPULayers != 0 {
		t.Errorf("expected default GPU layers 0, got %d", cfg.ModelGPULayers)
	}
	if cfg.BindIP != "0.0.0.0" {
		t.Errorf("expected default bind 0.0.0.0, got %s", cfg.BindIP)
	}
}

func TestLoad_Overrides(t *testing.T) {
	clearEnvVars()

	os.Setenv("GATEWAY_PORT", "4200")
	os.Setenv("LLAMACPP_BIN", "/custom/llama-server")
	os.Setenv("DEFAULT_MODEL", "qwen25-coder-7b")
	os.Setenv("MODEL_CONTEXT_SIZE", "16384")
	os.Setenv("MODEL_THREADS", "8")
	os.Setenv("BIND_IP", "127.0.0.1")
	defer clearEnvVars()

	cfg := Load()

	if cfg.Port != 4200 {
		t.Errorf("expected port 4200, got %d", cfg.Port)
	}
	if cfg.LlamaBin != "/custom/llama-server" {
		t.Errorf("expected custom llama bin, got %s", cfg.LlamaBin)
	}
	if cfg.DefaultModel != "qwen25-coder-7b" {
		t.Errorf("expected default model qwen25-coder-7b, got %s", cfg.DefaultModel)
	}
	if cfg.ModelContextSize != 16384 {
		t.Errorf("expected context size 16384, got %d", cfg.ModelContextSize)
	}
	if cfg.ModelThreads != 8 {
		t.Errorf("expected threads 8, got %d", cfg.ModelThreads)
	}
	if cfg.BindIP != "127.0.0.1" {
		t.Errorf("expected bind 127.0.0.1, got %s", cfg.BindIP)
	}
}

func TestLoad_APIKeys(t *testing.T) {
	clearEnvVars()
	os.Setenv("DEEPSEEK_API_KEY", "sk-deepseek-test")
	os.Setenv("GEMINI_API_KEY", "ai-gemini-test")
	defer clearEnvVars()

	cfg := Load()

	if cfg.RemoteAPIKeys["deepseek"] != "sk-deepseek-test" {
		t.Errorf("expected deepseek key, got %s", cfg.RemoteAPIKeys["deepseek"])
	}
	if cfg.RemoteAPIKeys["gemini"] != "ai-gemini-test" {
		t.Errorf("expected gemini key, got %s", cfg.RemoteAPIKeys["gemini"])
	}
	if _, ok := cfg.RemoteAPIKeys["openai"]; ok {
		t.Errorf("expected no openai key when not set")
	}
}

func TestAddr(t *testing.T) {
	cfg := Config{Port: 3100, BindIP: "0.0.0.0"}
	if cfg.Addr() != "0.0.0.0:3100" {
		t.Errorf("expected 0.0.0.0:3100, got %s", cfg.Addr())
	}

	cfg.BindIP = "127.0.0.1"
	if cfg.Addr() != "127.0.0.1:3100" {
		t.Errorf("expected 127.0.0.1:3100, got %s", cfg.Addr())
	}
}

func TestKnownProviders(t *testing.T) {
	providers := KnownProviders()
	expected := []string{"deepseek", "gemini", "openai", "anthropic", "openrouter", "xai"}

	if len(providers) != len(expected) {
		t.Errorf("expected %d providers, got %d", len(expected), len(providers))
	}

	for i, p := range expected {
		if providers[i] != p {
			t.Errorf("provider[%d] = %s, expected %s", i, providers[i], p)
		}
	}
}

func clearEnvVars() {
	for _, key := range []string{
		"GATEWAY_PORT", "LLAMACPP_BIN", "DEFAULT_MODEL",
		"MODEL_CONTEXT_SIZE", "MODEL_THREADS", "MODEL_GPU_LAYERS",
		"BIND_IP", "DATA_DIR", "MODELS_DIR",
		"DEEPSEEK_API_KEY", "GEMINI_API_KEY", "OPENAI_API_KEY",
		"ANTHROPIC_API_KEY", "OPENROUTER_API_KEY", "XAI_API_KEY",
	} {
		os.Unsetenv(key)
	}
}
