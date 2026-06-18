package models

import (
	"os"
	"path/filepath"
	"testing"
)

func setupTestModelsDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// Create simulated model directories with GGUF files
	models := []string{"qwen25-coder-7b", "qwen3-coder-14b", "codegemma-12b"}
	for _, m := range models {
		modelDir := filepath.Join(dir, m)
		if err := os.MkdirAll(modelDir, 0755); err != nil {
			t.Fatal(err)
		}
		// Create a dummy GGUF file
		ggufPath := filepath.Join(modelDir, m+"-q4_k_m.gguf")
		if err := os.WriteFile(ggufPath, []byte("dummy"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	return dir
}

func TestNewRegistry_ScansLocalModels(t *testing.T) {
	modelsDir := setupTestModelsDir(t)
	reg, err := NewRegistry(modelsDir)
	if err != nil {
		t.Fatalf("NewRegistry failed: %v", err)
	}

	models := reg.List()
	if len(models) < 3 {
		t.Fatalf("expected at least 3 local models, got %d", len(models))
	}

	// Check specific models found
	found := make(map[string]bool)
	for _, m := range models {
		if m.Provider == ProviderLocal {
			found[m.Name] = true
			if m.Status != StatusUnloaded {
				t.Errorf("model %s should be unloaded initially, got %s", m.Name, m.Status)
			}
			if m.SizeBytes <= 0 {
				t.Errorf("model %s should have size > 0", m.Name)
			}
			if m.SizeHuman == "" {
				t.Errorf("model %s should have human-readable size", m.Name)
			}
		}
	}

	for _, name := range []string{"qwen25-coder-7b", "qwen3-coder-14b", "codegemma-12b"} {
		if !found[name] {
			t.Errorf("expected to find model %s", name)
		}
	}
}

func TestNewRegistry_NoModelsDir(t *testing.T) {
	reg, err := NewRegistry("/nonexistent/path")
	if err != nil {
		t.Fatalf("NewRegistry should not fail on missing dir: %v", err)
	}
	if len(reg.List()) != 0 {
		t.Errorf("expected 0 models, got %d", len(reg.List()))
	}
}

func TestAddRemoteModels(t *testing.T) {
	reg, err := NewRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	reg.AddRemoteModels("deepseek", "sk-test", "https://api.deepseek.com/v1",
		[]string{"deepseek-v4-pro", "deepseek-v4-flash"})

	models := reg.List()
	remoteCount := 0
	for _, m := range models {
		if m.Provider == ProviderRemote {
			remoteCount++
			if m.Status != StatusReady {
				t.Errorf("remote model %s should be ready, got %s", m.ID, m.Status)
			}
		}
	}
	if remoteCount != 2 {
		t.Errorf("expected 2 remote models, got %d", remoteCount)
	}
}

func TestSetStatus(t *testing.T) {
	modelsDir := setupTestModelsDir(t)
	reg, err := NewRegistry(modelsDir)
	if err != nil {
		t.Fatal(err)
	}

	// Transition: unloaded → loading → ready
	if err := reg.SetStatus("qwen25-coder-7b", StatusLoading); err != nil {
		t.Fatalf("SetStatus loading failed: %v", err)
	}
	if err := reg.SetStatus("qwen25-coder-7b", StatusReady); err != nil {
		t.Fatalf("SetStatus ready failed: %v", err)
	}

	m, ok := reg.Get("qwen25-coder-7b")
	if !ok {
		t.Fatal("model not found")
	}
	if m.Status != StatusReady {
		t.Errorf("expected status ready, got %s", m.Status)
	}

	if reg.GetActiveModel() != "qwen25-coder-7b" {
		t.Errorf("expected active model qwen25-coder-7b, got %s", reg.GetActiveModel())
	}

	// Unload
	if err := reg.SetStatus("qwen25-coder-7b", StatusUnloaded); err != nil {
		t.Fatal(err)
	}
	if reg.GetActiveModel() != "" {
		t.Errorf("expected no active model after unload, got %s", reg.GetActiveModel())
	}
}

func TestSetStatus_UnknownModel(t *testing.T) {
	reg, err := NewRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	err = reg.SetStatus("nonexistent", StatusReady)
	if err == nil {
		t.Fatal("expected error for unknown model")
	}
}

func TestList_Ordering(t *testing.T) {
	reg, err := NewRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	reg.AddRemoteModels("openai", "sk-test", "https://api.openai.com/v1",
		[]string{"gpt-4o"})
	reg.AddRemoteModels("deepseek", "sk-test", "https://api.deepseek.com/v1",
		[]string{"deepseek-v4"})

	models := reg.List()
	// Order should be: deepseek/deepseek-v4, openai/gpt-4o
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].ID != "deepseek/deepseek-v4" {
		t.Errorf("expected first model deepseek/deepseek-v4, got %s", models[0].ID)
	}
	if models[1].ID != "openai/gpt-4o" {
		t.Errorf("expected second model openai/gpt-4o, got %s", models[1].ID)
	}
}

func TestLocalModels(t *testing.T) {
	modelsDir := setupTestModelsDir(t)
	reg, err := NewRegistry(modelsDir)
	if err != nil {
		t.Fatal(err)
	}

	reg.AddRemoteModels("openai", "sk-test", "https://api.openai.com/v1",
		[]string{"gpt-4o"})

	locals := reg.LocalModels()
	for _, m := range locals {
		if m.Provider != ProviderLocal {
			t.Errorf("expected local model, got %s/%s", m.Provider, m.ID)
		}
	}
	if len(locals) < 3 {
		t.Errorf("expected at least 3 local models, got %d", len(locals))
	}
}

func TestGetEndpointsConfig(t *testing.T) {
	modelsDir := setupTestModelsDir(t)
	reg, err := NewRegistry(modelsDir)
	if err != nil {
		t.Fatal(err)
	}

	reg.AddRemoteModels("deepseek", "sk-test", "https://api.deepseek.com/v1",
		[]string{"deepseek-v4-pro"})
	reg.AddRemoteModels("openai", "sk-test", "https://api.openai.com/v1",
		[]string{"gpt-4o"})

	ec := reg.GetEndpointsConfig()
	if len(ec.Endpoints) != 3 {
		t.Fatalf("expected 3 endpoints (1 local + 2 remote), got %d", len(ec.Endpoints))
	}

	// First should be local
	if ec.Endpoints[0].Type != "local" {
		t.Errorf("expected first endpoint to be local, got %s", ec.Endpoints[0].Type)
	}
	if !ec.Endpoints[0].Default {
		t.Errorf("expected local endpoint to be default")
	}

	// Check remote endpoints
	foundDeepseek := false
	foundOpenAI := false
	for _, e := range ec.Endpoints {
		if e.Name == "deepseek" {
			foundDeepseek = true
			if e.APIKeyEnv != "DEEPSEEK_API_KEY" {
				t.Errorf("expected DEEPSEEK_API_KEY env, got %s", e.APIKeyEnv)
			}
		}
		if e.Name == "openai" {
			foundOpenAI = true
		}
	}
	if !foundDeepseek {
		t.Error("expected deepseek endpoint")
	}
	if !foundOpenAI {
		t.Error("expected openai endpoint")
	}
}

func TestEndpointsConfig_ToJSON(t *testing.T) {
	ec := EndpointsConfig{
		Endpoints: []Endpoint{
			{Name: "test", Type: "local", BaseURL: "http://test:3100/v1", Models: []string{"test-model"}, Default: true},
		},
	}

	json, err := ec.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON failed: %v", err)
	}
	if !contains(json, "test-model") {
		t.Errorf("expected JSON to contain model name")
	}
}

func TestFormatBytes(t *testing.T) {
	cases := []struct {
		bytes    int64
		expected string
	}{
		{500, "500 B"},
		{1024, "1.0 KB"},
		{5 * 1024 * 1024, "5.0 MB"},
		{2 * 1024 * 1024 * 1024, "2.0 GB"},
		{10 * 1024 * 1024 * 1024, "10.0 GB"},
	}

	for _, c := range cases {
		result := formatBytes(c.bytes)
		if result != c.expected {
			t.Errorf("formatBytes(%d) = %s, expected %s", c.bytes, result, c.expected)
		}
	}
}

func TestGet_NotFound(t *testing.T) {
	reg, err := NewRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	_, ok := reg.Get("nonexistent")
	if ok {
		t.Error("expected false for nonexistent model")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && containsStr(s, substr)
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
