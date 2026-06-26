package models

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ── Unit Tests: Data Structures ────────────────────────────

func TestModelInfo_JSONSerialization(t *testing.T) {
	m := ModelInfo{
		ID:       "test-model",
		Name:     "Test Model",
		Category: "remote",
		Provider: "test-provider",
		Context:  131072,
		Ready:    true,
	}

	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var decoded ModelInfo
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if decoded.ID != m.ID {
		t.Errorf("ID mismatch: %q != %q", decoded.ID, m.ID)
	}
	if decoded.Category != "remote" {
		t.Errorf("Category mismatch: %q", decoded.Category)
	}
	if decoded.Context != 131072 {
		t.Errorf("Context mismatch: %d", decoded.Context)
	}
}

func TestModelListResponse_JSONSerialization(t *testing.T) {
	resp := ModelListResponse{
		Remote: []ModelInfo{{ID: "r1", Category: "remote"}},
		Local:  []ModelInfo{{ID: "l1", Category: "local"}},
		Total:  2,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var decoded ModelListResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if len(decoded.Remote) != 1 || decoded.Remote[0].ID != "r1" {
		t.Error("remote model mismatch")
	}
	if len(decoded.Local) != 1 || decoded.Local[0].ID != "l1" {
		t.Error("local model mismatch")
	}
	if decoded.Total != 2 {
		t.Errorf("Total mismatch: %d", decoded.Total)
	}
}

// ── Unit Tests: Default Remote Models ──────────────────────

func TestRemoteModels_Defaults(t *testing.T) {
	if len(RemoteModels) == 0 {
		t.Fatal("RemoteModels should have default entries")
	}

	foundDeepSeekV4 := false
	foundDeepSeekFlash := false
	for _, m := range RemoteModels {
		if m.ID == "deepseek-v4-pro" {
			foundDeepSeekV4 = true
			if m.Category != "remote" {
				t.Error("deepseek-v4-pro should be category=remote")
			}
			if m.Context != 131072 {
				t.Errorf("deepseek-v4-pro context window, expected 131072, got %d", m.Context)
			}
		}
		if m.ID == "deepseek-v4-flash[1m]" {
			foundDeepSeekFlash = true
			if m.Context != 1048576 {
				t.Errorf("deepseek-v4-flash context window, expected 1048576, got %d", m.Context)
			}
		}
	}

	if !foundDeepSeekV4 {
		t.Error("deepseek-v4-pro not in RemoteModels defaults")
	}
	if !foundDeepSeekFlash {
		t.Error("deepseek-v4-flash[1m] not in RemoteModels defaults")
	}
}

func TestRemoteModels_AllRemote(t *testing.T) {
	for _, m := range RemoteModels {
		if m.Category != "remote" {
			t.Errorf("model %q category should be 'remote', got %q", m.ID, m.Category)
		}
		if !m.Ready {
			t.Errorf("model %q should be ready", m.ID)
		}
	}
}

// ── Integration Tests: FetchModels ─────────────────────────

func TestFetchModels_NoGateway_ReturnsRemoteOnly(t *testing.T) {
	resp, err := FetchModels("")
	if err != nil {
		t.Fatalf("FetchModels() failed: %v", err)
	}

	if len(resp.Remote) == 0 {
		t.Fatal("expected remote models")
	}
	if resp.Total != len(resp.Remote)+len(resp.Local) {
		t.Errorf("Total mismatch: %d != %d", resp.Total, len(resp.Remote)+len(resp.Local))
	}
	// With empty gateway, locals should be empty or the remote count should match
	if len(resp.Local) != 0 {
		t.Log("local models returned without gateway (unexpected but not fatal)")
	}
}

func TestFetchModels_WithGateway(t *testing.T) {
	// Mock LLM gateway
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		resp := map[string]interface{}{
			"data": []map[string]interface{}{
				{"id": "local-model-1", "owned_by": "llama.cpp"},
				{"id": "local-model-2", "name": "Custom Model", "owned_by": "ollama"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	resp, err := FetchModels(server.URL)
	if err != nil {
		t.Fatalf("FetchModels() failed: %v", err)
	}

	if len(resp.Local) != 2 {
		t.Fatalf("expected 2 local models from gateway, got %d", len(resp.Local))
	}

	if resp.Local[0].Category != "local" {
		t.Errorf("local model 0 should be category=local, got %q", resp.Local[0].Category)
	}

	// Check total
	expectedTotal := len(resp.Remote) + 2
	if resp.Total != expectedTotal {
		t.Errorf("Total mismatch: %d != %d (remote=%d + local=2)", resp.Total, expectedTotal, len(resp.Remote))
	}
}

func TestFetchModels_GatewayDown(t *testing.T) {
	// Gateway not running
	resp, err := FetchModels("http://127.0.0.1:19999")
	if err != nil {
		t.Fatalf("FetchModels() should succeed even if gateway is down: %v", err)
	}

	// Should return remote models only
	if len(resp.Remote) == 0 {
		t.Fatal("remote models should be returned")
	}
}

func TestFetchModels_GatewayErrorStatusCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	resp, err := FetchModels(server.URL)
	if err != nil {
		t.Fatalf("FetchModels() should not fail on gateway error: %v", err)
	}

	if len(resp.Remote) == 0 {
		t.Fatal("remote models should be returned even when gateway errors")
	}
}

func TestFetchModels_InvalidGatewayURL(t *testing.T) {
	resp, err := FetchModels("://invalid")
	if err != nil {
		t.Fatalf("FetchModels() should not fail on invalid URL: %v", err)
	}

	// Remote models should still be returned
	if len(resp.Remote) == 0 {
		t.Fatal("remote models should be returned")
	}
}

func TestFetchModels_GatewayEmptyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	resp, err := FetchModels(server.URL)
	if err != nil {
		t.Fatalf("FetchModels() failed: %v", err)
	}

	if len(resp.Local) != 0 {
		t.Errorf("expected 0 local models for empty gateway, got %d", len(resp.Local))
	}
}

func TestFetchModels_LocalModelNameFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Model without name field — should use ID as fallback
		resp := map[string]interface{}{
			"data": []map[string]interface{}{
				{"id": "no-name-model"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	resp, err := FetchModels(server.URL)
	if err != nil {
		t.Fatalf("FetchModels() failed: %v", err)
	}

	if len(resp.Local) != 1 {
		t.Fatalf("expected 1 local model, got %d", len(resp.Local))
	}
	if resp.Local[0].Name != "no-name-model" {
		t.Errorf("expected name fallback to 'no-name-model', got %q", resp.Local[0].Name)
	}
}

func TestFetchModels_RemoteModelsImmutable(t *testing.T) {
	resp1, _ := FetchModels("")
	n1 := len(resp1.Remote)

	// Modify the first remote model's ID
	if len(resp1.Remote) > 0 {
		resp1.Remote[0].ID = "mutated"
	}

	resp2, _ := FetchModels("")
	if len(resp2.Remote) != n1 {
		t.Error("remote model count changed after mutation — copy not working")
	}
	if resp2.Remote[0].ID == "mutated" {
		t.Error("remote models were mutated — copy is shallow")
	}
}

func TestFetchModels_TotalMatches(t *testing.T) {
	resp, err := FetchModels("")
	if err != nil {
		t.Fatalf("FetchModels() failed: %v", err)
	}

	if resp.Total != len(resp.Remote)+len(resp.Local) {
		t.Errorf("Total property doesn't match actual count: %d != %d+%d",
			resp.Total, len(resp.Remote), len(resp.Local))
	}
}
