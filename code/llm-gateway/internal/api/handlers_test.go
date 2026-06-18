package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prhymera/llm-studio/code/llm-gateway/internal/config"
	"github.com/prhymera/llm-studio/code/llm-gateway/internal/models"
)

func setupTestServer(t *testing.T) *Server {
	t.Helper()

	// Create a temp directory with mock model files
	modelsDir := t.TempDir()
	for _, m := range []string{"qwen25-coder-7b", "qwen3-coder-14b"} {
		modelDir := filepath.Join(modelsDir, m)
		if err := os.MkdirAll(modelDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(modelDir, m+"-q4_k_m.gguf"), []byte("mock"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	reg, err := models.NewRegistry(modelsDir)
	if err != nil {
		t.Fatal(err)
	}

	// Add some remote models
	reg.AddRemoteModels("deepseek", "sk-test", "https://api.deepseek.com/v1",
		[]string{"deepseek-v4-pro", "deepseek-v4-flash"})
	reg.AddRemoteModels("openai", "sk-test", "https://api.openai.com/v1",
		[]string{"gpt-4o"})

	cfg := config.Config{
		Port:          3100,
		ModelsDir:     modelsDir,
		LlamaBin:      "llama-server",
		DefaultModel:  "",
		RemoteAPIKeys: map[string]string{"deepseek": "sk-test", "openai": "sk-test2"},
		BindIP:        "0.0.0.0",
	}

	return NewServer(cfg, reg)
}

func TestHandleHealth(t *testing.T) {
	srv := setupTestServer(t)
	router := srv.Router()

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var resp map[string]string
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["status"] != "healthy" {
		t.Errorf("expected healthy, got %s", resp["status"])
	}
}

func TestHandleStatus(t *testing.T) {
	srv := setupTestServer(t)
	router := srv.Router()

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var resp map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&resp)

	if resp["version"] != "0.1.0" {
		t.Errorf("expected version 0.1.0, got %s", resp["version"])
	}
	if resp["llama_state"] != "stopped" {
		t.Errorf("expected llama_state stopped, got %s", resp["llama_state"])
	}
	if resp["healthy"] != true {
		t.Errorf("expected healthy true")
	}

	modelsCount := int(resp["models"].(float64))
	if modelsCount < 4 {
		t.Errorf("expected at least 4 models, got %d", modelsCount)
	}
}

func TestHandleListModels(t *testing.T) {
	srv := setupTestServer(t)
	router := srv.Router()

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var resp struct {
		Object string                   `json:"object"`
		Data   []map[string]interface{} `json:"data"`
	}
	json.NewDecoder(rec.Body).Decode(&resp)

	if resp.Object != "list" {
		t.Errorf("expected object 'list', got %s", resp.Object)
	}

	// Should have at least: 2 local + 3 remote = 5 models
	if len(resp.Data) < 4 {
		t.Errorf("expected at least 4 models, got %d", len(resp.Data))
	}

	// Check each model has required fields
	for _, m := range resp.Data {
		if _, ok := m["id"]; !ok {
			t.Error("model missing 'id' field")
		}
		if _, ok := m["object"]; !ok {
			t.Error("model missing 'object' field")
		}
		if _, ok := m["status"]; !ok {
			t.Error("model missing 'status' field")
		}
	}
}

func TestHandleGetModel_Found(t *testing.T) {
	srv := setupTestServer(t)
	router := srv.Router()

	req := httptest.NewRequest(http.MethodGet, "/v1/models/qwen25-coder-7b", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var resp map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&resp)

	if resp["id"] != "qwen25-coder-7b" {
		t.Errorf("expected id 'qwen25-coder-7b', got %s", resp["id"])
	}
	if resp["status"] != "unloaded" {
		t.Errorf("expected status 'unloaded', got %s", resp["status"])
	}
}

func TestHandleGetModel_NotFound(t *testing.T) {
	srv := setupTestServer(t)
	router := srv.Router()

	req := httptest.NewRequest(http.MethodGet, "/v1/models/nonexistent", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestHandleGetModel_Remote(t *testing.T) {
	srv := setupTestServer(t)
	router := srv.Router()

	req := httptest.NewRequest(http.MethodGet, "/v1/models/deepseek/deepseek-v4-pro", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var resp map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["status"] != "ready" {
		t.Errorf("expected remote model to be 'ready', got %s", resp["status"])
	}
}

func TestHandleLoadModel_NotFound(t *testing.T) {
	srv := setupTestServer(t)
	router := srv.Router()

	req := httptest.NewRequest(http.MethodPost, "/v1/models/nonexistent/load", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestHandleLoadModel_Remote(t *testing.T) {
	srv := setupTestServer(t)
	router := srv.Router()

	req := httptest.NewRequest(http.MethodPost, "/v1/models/deepseek/deepseek-v4-pro/load", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for remote model, got %d", rec.Code)
	}
}

func TestHandleModelStatus(t *testing.T) {
	srv := setupTestServer(t)
	router := srv.Router()

	req := httptest.NewRequest(http.MethodGet, "/v1/models/qwen25-coder-7b/status", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var resp map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&resp)

	if resp["id"] != "qwen25-coder-7b" {
		t.Errorf("expected id 'qwen25-coder-7b', got %s", resp["id"])
	}
	if resp["loaded"] != false {
		t.Errorf("expected loaded=false initially")
	}
}

func TestHandleEndpoints(t *testing.T) {
	srv := setupTestServer(t)
	router := srv.Router()

	req := httptest.NewRequest(http.MethodGet, "/v1/endpoints", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var resp struct {
		Endpoints []struct {
			Name   string   `json:"name"`
			Type   string   `json:"type"`
			Models []string `json:"models"`
		} `json:"endpoints"`
	}
	json.NewDecoder(rec.Body).Decode(&resp)

	if len(resp.Endpoints) < 3 {
		t.Errorf("expected at least 3 endpoints (1 local + 2 remote), got %d", len(resp.Endpoints))
	}

	foundLocal := false
	foundDeepseek := false
	for _, e := range resp.Endpoints {
		if e.Type == "local" {
			foundLocal = true
		}
		if e.Name == "deepseek" {
			foundDeepseek = true
		}
	}
	if !foundLocal {
		t.Error("expected local endpoint")
	}
	if !foundDeepseek {
		t.Error("expected deepseek endpoint")
	}
}

func TestHandleChatCompletion_NoModel(t *testing.T) {
	srv := setupTestServer(t)
	router := srv.Router()

	body := `{"model": "nonexistent", "messages": [{"role": "user", "content": "hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestHandleChatCompletion_LocalUnloaded(t *testing.T) {
	srv := setupTestServer(t)
	router := srv.Router()

	body := `{"model": "qwen25-coder-7b", "messages": [{"role": "user", "content": "hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestHandleDownloadModel(t *testing.T) {
	srv := setupTestServer(t)
	router := srv.Router()

	req := httptest.NewRequest(http.MethodPost, "/v1/models/test-model/download", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Errorf("expected 501, got %d", rec.Code)
	}
}

func TestUnloadModel_WhenNoneLoaded(t *testing.T) {
	srv := setupTestServer(t)
	router := srv.Router()

	req := httptest.NewRequest(http.MethodPost, "/v1/models/qwen25-coder-7b/unload", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	// Should still succeed (no-op)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for unload with no model loaded, got %d", rec.Code)
	}
}

func TestGetProviderBaseURL(t *testing.T) {
	urls := map[string]string{
		"deepseek":   "https://api.deepseek.com/v1",
		"gemini":     "https://generativelanguage.googleapis.com/v1beta/openai/",
		"openai":     "https://api.openai.com/v1",
		"anthropic":  "https://api.anthropic.com/v1",
		"openrouter": "https://openrouter.ai/api/v1",
		"xai":        "https://api.x.ai/v1",
	}

	for provider, expected := range urls {
		result := getProviderBaseURL(provider)
		if result != expected {
			t.Errorf("getProviderBaseURL(%q) = %q, expected %q", provider, result, expected)
		}
	}

	if unknown := getProviderBaseURL("unknown"); unknown != "" {
		t.Errorf("expected empty for unknown provider, got %q", unknown)
	}
}

func TestListModelsResponse_Format(t *testing.T) {
	srv := setupTestServer(t)
	router := srv.Router()

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	var resp map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&resp)

	// Should be OpenAI-compatible format
	if resp["object"] != "list" {
		t.Errorf("expected object=list, got %v", resp["object"])
	}

	data, ok := resp["data"].([]interface{})
	if !ok {
		t.Fatal("expected data array")
	}

	for _, item := range data {
		model, ok := item.(map[string]interface{})
		if !ok {
			t.Fatal("expected model object")
		}
		// Must have these fields per OpenAI spec
		for _, field := range []string{"id", "object", "created", "owned_by"} {
			if _, exists := model[field]; !exists {
				t.Errorf("model missing required field '%s'", field)
			}
		}
	}
}

func TestChatInvalidJSON(t *testing.T) {
	srv := setupTestServer(t)
	router := srv.Router()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{invalid json`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid JSON, got %d", rec.Code)
	}
}
