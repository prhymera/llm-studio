// Package docker manages ephemeral Docker containers for agent sessions.
package docker

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/gorilla/websocket"
	"github.com/prhymera/llm-studio/code/agent-runner/internal/session"
)

// Config configures the Docker manager.
type Config struct {
	DataDir        string
	GatewayURL     string
	DefaultModel   string
	DefaultAgent   string
	AgentTimeout   int
	CPULimit       int
	MemoryLimit    string
	DockerNetwork  string
}

// Manager handles Docker container lifecycle for agent sessions.
type Manager struct {
	cli      *client.Client
	cfg      Config
	upgrader websocket.Upgrader
}

// NewManager creates a Docker manager with the Docker client.
func NewManager(cfg Config) (*Manager, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}

	return &Manager{
		cli: cli,
		cfg: cfg,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}, nil
}

// AgentImageInfo describes an available agent image.
type AgentImageInfo struct {
	Type        string `json:"type"`
	ImageName   string `json:"image_name"`
	Version     string `json:"version"`
	Built       bool   `json:"built"`
	Description string `json:"description"`
}

// ListAgentImages returns the list of known agent images and their build status.
func (m *Manager) ListAgentImages() []AgentImageInfo {
	agents := []AgentImageInfo{
		{Type: "picoclaw", ImageName: "llm-studio-agent-picoclaw", Description: "Autonomous coding agent (picoclaw)", Built: false},
		{Type: "pi", ImageName: "llm-studio-agent-pi", Description: "AI coding assistant (pi.dev)", Built: false},
		{Type: "opencode", ImageName: "llm-studio-agent-opencode", Description: "Open-source coding agent", Built: false},
	}

	for i, a := range agents {
		_, _, err := m.cli.ImageInspectWithRaw(context.Background(), a.ImageName)
		agents[i].Built = (err == nil)
	}

	return agents
}

// BuildAgentImage builds the Docker image for the given agent type.
func (m *Manager) BuildAgentImage(ctx context.Context, agentType string) error {
	buildDir := filepath.Join(m.cfg.DataDir, "config", "agent-images", agentType)
	if _, err := os.Stat(buildDir); os.IsNotExist(err) {
		return fmt.Errorf("agent image %s not found at %s", agentType, buildDir)
	}

	log.Printf("Building agent image: %s from %s", agentType, buildDir)
	// TODO: Implement Docker build via Docker SDK's ImageBuild()
	return nil
}

// CreateSession starts a new agent container and returns the session.
func (m *Manager) CreateSession(ctx context.Context, agentType, model, label string) (*session.Session, error) {
	sessID := fmt.Sprintf("sess-%d", time.Now().UnixNano())
	userID := extractUserID(ctx)

	workspacePath := filepath.Join(m.cfg.DataDir, "workspaces", userID, sessID)
	if err := os.MkdirAll(workspacePath, 0755); err != nil {
		return nil, fmt.Errorf("create workspace: %w", err)
	}

	imageName := fmt.Sprintf("llm-studio-agent-%s:latest", agentType)
	containerName := fmt.Sprintf("agent-%s-%s", userID[:8], sessID[:12])

	// Remove any existing container with the same name (from previous failed sessions)
	m.cli.ContainerRemove(ctx, containerName, container.RemoveOptions{Force: true})

	env := []string{
		fmt.Sprintf("LLM_ENDPOINT=%s/v1", m.cfg.GatewayURL),
		fmt.Sprintf("LLM_MODEL=%s", model),
		"LLM_API_KEY=local",
		"WORKSPACE=/workspace",
		fmt.Sprintf("USER_ID=%s", userID),
		fmt.Sprintf("SESSION_ID=%s", sessID),
	}

	resp, err := m.cli.ContainerCreate(ctx, &container.Config{
		Image:        imageName,
		Env:          env,
		Cmd:          []string{},
		OpenStdin:    true,
		Tty:          true,
		Labels: map[string]string{
			"llm-studio.component":  "agent",
			"llm-studio.user":       userID,
			"llm-studio.session":    sessID,
			"llm-studio.agent-type": agentType,
		},
	}, &container.HostConfig{
		Binds: []string{
			fmt.Sprintf("%s:/workspace:rw", workspacePath),
		},
		Resources: container.Resources{
			Memory:   parseMemoryBytes(m.cfg.MemoryLimit),
			CPUCount: int64(m.cfg.CPULimit),
		},
		ReadonlyRootfs: true,
		Tmpfs:          map[string]string{"/tmp": "rw,noexec,nosuid,size=64m"},
		NetworkMode:    container.NetworkMode(m.effectiveNetwork()),
	}, nil, nil, containerName)
	if err != nil {
		os.RemoveAll(workspacePath)
		return nil, fmt.Errorf("create container: %w", err)
	}

	if err := m.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		m.cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		os.RemoveAll(workspacePath)
		return nil, fmt.Errorf("start container: %w", err)
	}

	log.Printf("Started agent container %s (session %s)", containerName, sessID)

	return &session.Session{
		ID:             sessID,
		UserID:         userID,
		AgentType:      agentType,
		Model:          model,
		Status:         "running",
		ContainerID:    resp.ID,
		WorkspacePath:  workspacePath,
		WorkspaceLabel: label,
		CreatedAt:      time.Now(),
		LastActiveAt:   time.Now(),
	}, nil
}

// DestroySession stops and removes the container for a session.
func (m *Manager) DestroySession(ctx context.Context, sessionID string) error {
	containers, err := m.cli.ContainerList(ctx, container.ListOptions{
		All: true,
		Filters: filters.NewArgs(
			filters.Arg("label", fmt.Sprintf("llm-studio.session=%s", sessionID)),
		),
	})
	if err != nil {
		return err
	}

	for _, c := range containers {
		if err := m.cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true}); err != nil {
			log.Printf("Warning: failed to remove container %s: %v", c.ID[:12], err)
		}
	}

	return nil
}

// ReconnectSession restarts a stopped container for an existing session.
func (m *Manager) ReconnectSession(ctx context.Context, sessionID string) (*session.Session, error) {
	containers, err := m.cli.ContainerList(ctx, container.ListOptions{
		All: true,
		Filters: filters.NewArgs(
			filters.Arg("label", fmt.Sprintf("llm-studio.session=%s", sessionID)),
		),
	})
	if err != nil {
		return nil, err
	}

	if len(containers) == 0 {
		return nil, fmt.Errorf("no container found for session %s", sessionID)
	}

	c := containers[0]
	if c.State == "running" {
		return mapContainerToSession(c), nil
	}

	if err := m.cli.ContainerStart(ctx, c.ID, container.StartOptions{}); err != nil {
		return nil, fmt.Errorf("restart container: %w", err)
	}

	return mapContainerToSession(c), nil
}

// AttachTerminal upgrades an HTTP connection to a WebSocket and connects to the container's PTY.
//
// Strategy: exec the agent interpreter (picoclaw/pi/opencode) directly into the container
// so the user sees the actual agent prompt, not a bash shell. Falls back to bash/sh
// if the agent binary is unavailable.
func (m *Manager) AttachTerminal(w http.ResponseWriter, r *http.Request, sessionID string) {
	ws, err := m.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}
	defer ws.Close()

	containers, err := m.cli.ContainerList(r.Context(), container.ListOptions{
		All: true,
		Filters: filters.NewArgs(
			filters.Arg("label", fmt.Sprintf("llm-studio.session=%s", sessionID)),
		),
	})
	if err != nil || len(containers) == 0 {
		ws.WriteJSON(map[string]string{"type": "error", "message": "session not found"})
		return
	}

	c := containers[0]

	// If container exited, restart it first
	if c.State == "exited" || c.State == "created" {
		log.Printf("Container %s is %s, attempting restart...", c.ID[:12], c.State)
		if err := m.cli.ContainerStart(r.Context(), c.ID, container.StartOptions{}); err != nil {
			log.Printf("Container restart failed: %v", err)
		} else {
			log.Printf("Container %s restarted", c.ID[:12])
			time.Sleep(500 * time.Millisecond)
		}
	}

	// Extract agent type from container labels, model from env
	agentType := c.Labels["llm-studio.agent-type"]
	model := m.cfg.DefaultModel
	if agentType == "" {
		agentType = "picoclaw" // default
	}

	// Inspect container to get the actual model from env
	insp, inspErr := m.cli.ContainerInspect(r.Context(), c.ID)
	if inspErr == nil && insp.Config != nil {
		for _, env := range insp.Config.Env {
			if strings.HasPrefix(env, "LLM_MODEL=") {
				model = strings.TrimPrefix(env, "LLM_MODEL=")
				break
			}
		}
	}

	// Build the agent interpreter command based on agent type
	var agentCmd []string
	switch agentType {
	case "picoclaw":
		agentCmd = []string{"picoclaw", "agent", "--model", model}
	case "pi":
		agentCmd = []string{"pi", "--model", model, "--api-key", "local"}
	case "opencode":
		agentCmd = []string{"opencode", "--model", model}
	default:
		agentCmd = []string{"/bin/bash"}
	}

	log.Printf("Attaching terminal for %s (type=%s, model=%s): %v", c.ID[:12], agentType, model, agentCmd)

	// Try the agent command first. If it fails (binary not found), fall back to bash/sh.
	execResp, execErr := m.cli.ContainerExecCreate(r.Context(), c.ID, container.ExecOptions{
		Cmd:          agentCmd,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          true,
	})
	if execErr != nil {
		log.Printf("Agent exec failed (%v), falling back to bash/sh", execErr)
		execResp, execErr = m.cli.ContainerExecCreate(r.Context(), c.ID, container.ExecOptions{
			Cmd:          []string{"/bin/bash"},
			AttachStdin:  true,
			AttachStdout: true,
			AttachStderr: true,
			Tty:          true,
		})
	}
	if execErr != nil {
		execResp, execErr = m.cli.ContainerExecCreate(r.Context(), c.ID, container.ExecOptions{
			Cmd:          []string{"/bin/sh"},
			AttachStdin:  true,
			AttachStdout: true,
			AttachStderr: true,
			Tty:          true,
		})
	}
	if execErr != nil {
		ws.WriteMessage(websocket.TextMessage,
			[]byte(fmt.Sprintf("\r\n\x1b[31m⚠ Failed to create agent shell: %v\r\n\x1b[0m", execErr)))
		return
	}

	execAttach, execAttachErr := m.cli.ContainerExecAttach(r.Context(), execResp.ID, container.ExecAttachOptions{Tty: true})
	if execAttachErr != nil {
		ws.WriteMessage(websocket.TextMessage,
			[]byte(fmt.Sprintf("\r\n\x1b[31m⚠ Failed to attach: %v\r\n\x1b[0m", execAttachErr)))
		return
	}

	containerReader := execAttach.Reader
	containerWriter := execAttach.Conn
	defer execAttach.Close()

	// ── Bi-directional copy: WebSocket ↔ Container PTY ──
	errCh := make(chan error, 2)

	// Container stdout → WebSocket
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := containerReader.Read(buf)
			if err != nil {
				errCh <- err
				return
			}
			if n == 0 {
				continue
			}
			data := buf[:n]
			if err := ws.WriteMessage(websocket.TextMessage, data); err != nil {
				errCh <- err
				return
			}
		}
	}()

	// WebSocket → Container stdin
	go func() {
		for {
			_, msg, err := ws.ReadMessage()
			if err != nil {
				errCh <- err
				return
			}
			containerWriter.Write(msg)
		}
	}()

	<-errCh
}

// CleanupAll stops and removes all agent containers.
func (m *Manager) CleanupAll(ctx context.Context) {
	containers, err := m.cli.ContainerList(ctx, container.ListOptions{
		All: true,
		Filters: filters.NewArgs(
			filters.Arg("label", "llm-studio.component=agent"),
		),
	})
	if err != nil {
		log.Printf("Cleanup: failed to list containers: %v", err)
		return
	}

	for _, c := range containers {
		log.Printf("Cleanup: removing container %s", c.ID[:12])
		m.cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true})
	}
}

// ── Helpers ─────────────────────────────────────────────────

func extractUserID(ctx context.Context) string {
	// TODO: extract from JWT or request context
	return "user-default"
}

func parseMemoryBytes(s string) int64 {
	if len(s) < 2 {
		return 0
	}
	var val int64
	fmt.Sscanf(s[:len(s)-1], "%d", &val)
	switch s[len(s)-1] {
	case 'G', 'g':
		return val * 1024 * 1024 * 1024
	case 'M', 'm':
		return val * 1024 * 1024
	case 'K', 'k':
		return val * 1024
	default:
		return val
	}
}

func (m *Manager) effectiveNetwork() string {
	n := m.cfg.DockerNetwork
	if n == "" {
		return "llm-studio_llm-studio-network"
	}
	return n
}

func mapContainerToSession(c container.Summary) *session.Session {
	return &session.Session{
		ContainerID: c.ID,
		Status:      c.State,
	}
}
