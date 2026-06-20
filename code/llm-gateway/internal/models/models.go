// Package models provides the model registry and state management.
// It handles discovery of local GGUF models, configuration of remote endpoints,
// and tracks the currently loaded model state.
package models

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Status represents the lifecycle state of a local model.
type Status string

const (
	StatusUnloaded Status = "unloaded"
	StatusLoading  Status = "loading"
	StatusReady    Status = "ready"
	StatusError    Status = "error"
)

// ProviderType distinguishes local from remote model endpoints.
type ProviderType string

const (
	ProviderLocal  ProviderType = "local"
	ProviderRemote ProviderType = "remote"
)

// Model describes a single model (local or remote).
type Model struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Provider     ProviderType `json:"provider"`
	ProviderName string       `json:"provider_name,omitempty"`
	Status       Status       `json:"status"`
	SizeBytes    int64        `json:"size_bytes,omitempty"`
	SizeHuman    string       `json:"size_human,omitempty"`
	FilePath     string       `json:"file_path,omitempty"`
	Description  string       `json:"description,omitempty"`
	ContextLen   int          `json:"context_length,omitempty"`
	Params       string       `json:"params,omitempty"`
}

// Endpoint describes an LLM API endpoint configuration.
// This is used to generate the endpoints.yml for agent containers and
// to auto-configure Open WebUI.
type Endpoint struct {
	Name       string   `json:"name" yaml:"name"`
	Type       string   `json:"type" yaml:"type"`
	BaseURL    string   `json:"base_url" yaml:"base_url"`
	APIKeyEnv  string   `json:"api_key_env,omitempty" yaml:"api_key_env,omitempty"`
	Models     []string `json:"models" yaml:"models"`
	Default    bool     `json:"default" yaml:"default"`
}

// EndpointsConfig is the full configuration written to endpoints.yml.
type EndpointsConfig struct {
	Endpoints []Endpoint `json:"endpoints" yaml:"endpoints"`
}

// Registry maintains the set of known models and their states.
// It is safe for concurrent access.
type Registry struct {
	mu       sync.RWMutex
	models   map[string]*Model
	endpoints []Endpoint

	// ActiveModel is the currently loaded local model, if any.
	ActiveModel string `json:"active_model"`

	// ModelsDir is the path scanned for GGUF files.
	ModelsDir string
}

// NewRegistry creates a registry and scans the models directory.
func NewRegistry(modelsDir string) (*Registry, error) {
	r := &Registry{
		models:    make(map[string]*Model),
		ModelsDir: modelsDir,
	}
	if err := r.scanLocalModels(); err != nil {
		return r, fmt.Errorf("scan models: %w", err)
	}
	return r, nil
}

// Scan refreshes the local model list by scanning the models directory.
func (r *Registry) Scan() error {
	return r.scanLocalModels()
}

func (r *Registry) scanLocalModels() error {
	entries, err := os.ReadDir(r.ModelsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No models directory yet, that's OK
		}
		return fmt.Errorf("read models dir: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		modelDir := filepath.Join(r.ModelsDir, entry.Name())
		ggufFiles, err := filepath.Glob(filepath.Join(modelDir, "*.gguf"))
		if err != nil || len(ggufFiles) == 0 {
			// Also check for directly-named GGUF files
			ggufFiles, _ = filepath.Glob(modelDir + "*.gguf")
		}

		if len(ggufFiles) > 0 {
			fi, err := os.Stat(ggufFiles[0])
			if err != nil {
				continue
			}
			model := &Model{
				ID:        entry.Name(),
				Name:      entry.Name(),
				Provider:  ProviderLocal,
				Status:    StatusUnloaded,
				SizeBytes: fi.Size(),
				SizeHuman: formatBytes(fi.Size()),
				FilePath:  ggufFiles[0],
			}
			r.models[entry.Name()] = model
		}
	}
	return nil
}

// AddRemoteModels registers remote endpoint models in the registry.
func (r *Registry) AddRemoteModels(providerName string, apiKey string, baseURL string, modelIDs []string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, mid := range modelIDs {
		id := fmt.Sprintf("%s/%s", providerName, mid)
		r.models[id] = &Model{
			ID:           id,
			Name:         mid,
			Provider:     ProviderRemote,
			ProviderName: providerName,
			Status:       StatusReady, // Remote models are always "ready"
		}
	}

	// Add to endpoints list
	r.endpoints = append(r.endpoints, Endpoint{
		Name:      providerName,
		Type:      "remote",
		BaseURL:   baseURL,
		APIKeyEnv: fmt.Sprintf("%s_API_KEY", strings.ToUpper(providerName)),
		Models:    modelIDs,
	})
}

// List returns all known models, sorted by provider then name.
func (r *Registry) List() []*Model {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*Model, 0, len(r.models))
	for _, m := range r.models {
		result = append(result, m)
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].Provider != result[j].Provider {
			return result[i].Provider < result[j].Provider
		}
		return result[i].Name < result[j].Name
	})

	return result
}

// LocalModels returns only local models, sorted by name.
func (r *Registry) LocalModels() []*Model {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []*Model
	for _, m := range r.models {
		if m.Provider == ProviderLocal {
			result = append(result, m)
		}
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})

	return result
}

// Get returns a model by ID.
func (r *Registry) Get(id string) (*Model, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	// Try exact match first
	if m, ok := r.models[id]; ok {
		return m, ok
	}
	// Try matching suffix (for remote models stored as 'provider/model')
	// e.g., 'deepseek-v4-flash' should match 'deepseek/deepseek-v4-flash'
	for key, m := range r.models {
		if strings.HasSuffix(key, "/"+id) || strings.HasSuffix(key, id) {
			return m, true
		}
	}
	return nil, false
}

// SetStatus updates the status of a local model.
func (r *Registry) SetStatus(id string, status Status) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	m, ok := r.models[id]
	if !ok {
		return fmt.Errorf("model %s not found", id)
	}
	m.Status = status
	if status == StatusReady {
		r.ActiveModel = id
	} else if status == StatusUnloaded || status == StatusError {
		if r.ActiveModel == id {
			r.ActiveModel = ""
		}
	}
	return nil
}

// GetActiveModel returns the currently active local model ID.
func (r *Registry) GetActiveModel() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.ActiveModel
}

// GetEndpointsConfig builds the endpoints.yml configuration.
func (r *Registry) GetEndpointsConfig() EndpointsConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Build local endpoint from local models
	localModels := make([]string, 0)
	for _, m := range r.models {
		if m.Provider == ProviderLocal {
			localModels = append(localModels, m.ID)
		}
	}

	endpoints := []Endpoint{
		{
			Name:    "llama-local",
			Type:    "local",
			BaseURL: "http://llm-gateway:3100/v1",
			Models:  localModels,
			Default: true,
		},
	}

	endpoints = append(endpoints, r.endpoints...)

	return EndpointsConfig{Endpoints: endpoints}
}

// ToJSON marshals the endpoints config to pretty JSON.
func (e EndpointsConfig) ToJSON() (string, error) {
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
