// Package models provides model listing and categorization for agent sessions.
package models

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

// ModelInfo represents an available AI model.
type ModelInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"` // "remote" or "local"
	Provider string `json:"provider"`
	Context  int    `json:"context_window"`
	Ready    bool   `json:"ready"`
}

// ModelListResponse is the response for GET /v1/models.
type ModelListResponse struct {
	Remote []ModelInfo `json:"remote"`
	Local  []ModelInfo `json:"local"`
	Total  int         `json:"total"`
}

// Default remote models (always available via API key).
var RemoteModels = []ModelInfo{
	{
		ID:       "deepseek-v4-pro",
		Name:     "DeepSeek V4 Pro",
		Category: "remote",
		Provider: "deepseek",
		Context:  131072,
		Ready:    true,
	},
	{
		ID:       "deepseek-v4-flash[1m]",
		Name:     "DeepSeek V4 Flash (1M context)",
		Category: "remote",
		Provider: "deepseek",
		Context:  1048576,
		Ready:    true,
	},
}

// FetchModels retrieves the merged model list from the LLM gateway and adds remote defaults.
func FetchModels(gatewayURL string) (*ModelListResponse, error) {
	response := &ModelListResponse{
		Remote: make([]ModelInfo, len(RemoteModels)),
		Local:  []ModelInfo{},
	}

	copy(response.Remote, RemoteModels)

	// Try to get local models from the LLM gateway
	if gatewayURL != "" {
		localModels, err := fetchLocalModels(gatewayURL)
		if err != nil {
			log.Printf("[models] failed to fetch local models from gateway: %v", err)
			// Non-fatal: return remote models only
		} else {
			response.Local = localModels
		}
	}

	response.Total = len(response.Remote) + len(response.Local)
	return response, nil
}

// fetchLocalModels queries the LLM gateway for locally available models.
func fetchLocalModels(gatewayURL string) ([]ModelInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/v1/models", gatewayURL), nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gateway request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gateway returned %d", resp.StatusCode)
	}

	// Parse the gateway response — format depends on the gateway
	// Standard format: {"data": [{"id": "model-name", ...}]}
	var gatewayResp struct {
		Data []struct {
			ID      string `json:"id"`
			Name    string `json:"name,omitempty"`
			OwnedBy string `json:"owned_by,omitempty"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&gatewayResp); err != nil {
		return nil, fmt.Errorf("decode gateway response: %w", err)
	}

	localModels := make([]ModelInfo, 0, len(gatewayResp.Data))
	for _, m := range gatewayResp.Data {
		// Skip models already listed in RemoteModels (avoid duplicates)
		if isInRemoteModels(m.ID) {
			continue
		}
		name := m.Name
		if name == "" {
			name = m.ID
		}
		category := "local"
		if m.OwnedBy == "remote" {
			category = "remote"
		}
		localModels = append(localModels, ModelInfo{
			ID:       m.ID,
			Name:     name,
			Category: category,
			Provider: m.OwnedBy,
			Context:  32768,
			Ready:    true,
		})
	}

	return localModels, nil
}

// isInRemoteModels checks if a model ID is in the hardcoded RemoteModels list.
func isInRemoteModels(id string) bool {
	for _, m := range RemoteModels {
		if m.ID == id {
			return true
		}
	}
	return false
}
