// Package docker manages ephemeral Docker containers for agent sessions.
package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
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
	FraoSkillsPath string // path on host containing frao-skills (mounted ro into agents)
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

	// ── Per-user persistent home (shared across all user's sessions) ──
	userHomeDir := filepath.Join(m.cfg.DataDir, "workspaces", userID, "home")
	if err := os.MkdirAll(userHomeDir, 0755); err != nil {
		return nil, fmt.Errorf("create user home: %w", err)
	}
	os.MkdirAll(filepath.Join(userHomeDir, ".npm"), 0755)
	os.MkdirAll(filepath.Join(userHomeDir, ".npm-global"), 0755)
	os.MkdirAll(filepath.Join(userHomeDir, ".pi", "skills"), 0755)
	os.MkdirAll(filepath.Join(userHomeDir, ".config"), 0755)

	// ── Soft 15GB quota per user ──
	userRoot := filepath.Join(m.cfg.DataDir, "workspaces", userID)
	if err := enforceQuota(userRoot, 15*1024*1024*1024); err != nil {
		return nil, err
	}

	// ── Session workspace (isolated, not shared) ──
	workspacePath := filepath.Join(m.cfg.DataDir, "workspaces", userID, "sessions", sessID)
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
		"TERM=xterm-256color",
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
			fmt.Sprintf("%s:/home/pi-agent:rw", userHomeDir),
			fmt.Sprintf("%s:/frao-skills:ro", m.cfg.FraoSkillsPath),
		},
		Resources: container.Resources{
			Memory:   parseMemoryBytes(m.cfg.MemoryLimit),
			CPUCount: int64(m.cfg.CPULimit),
		},
		ReadonlyRootfs: true,
		Tmpfs: map[string]string{
			"/tmp": "rw,noexec,nosuid,size=64m",
		},
		NetworkMode:  container.NetworkMode(m.effectiveNetwork()),
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
		ws.WriteMessage(websocket.TextMessage, []byte(`{"type":"error","message":"session not found"}`))
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
		// --provider gateway routes ALL models through the LLM gateway
		// (custom provider defined in /etc/pi-gateway-extension.js)
		agentCmd = []string{"pi", "--provider", "gateway", "--model", model, "--api-key", "local",
			"--extension", "/root/.pi/extensions/gateway.js"}
		// Preload frao-skills if available ("/frao-skills" mounted from host)
		for _, skill := range m.discoverFraoSkills() {
			agentCmd = append(agentCmd, "--skill", filepath.Join("/frao-skills", skill))
		}
	case "opencode":
		agentCmd = []string{"opencode", "--model", model}
	default:
		agentCmd = []string{"/bin/bash"}
	}

	log.Printf("Attaching terminal for %s (type=%s, model=%s): %v", c.ID[:12], agentType, model, agentCmd)

	// Try commands in order until one works.
	// ContainerExecCreate may succeed even if the binary doesn't exist
	// (Docker records intent). OCI errors come through the attach stream,
	// not as attach errors. Read the first chunk to detect this.
	cmdsToTry := [][]string{
		agentCmd,
		{"/bin/bash"},
		{"/bin/sh"},
	}

	var execAttach types.HijackedResponse
	var execResp container.ExecCreateResponse
	var lastErr error
	attachSucceeded := false

readLoop:
	for _, cmd := range cmdsToTry {
		var execErr error
		execResp, execErr = m.cli.ContainerExecCreate(r.Context(), c.ID, container.ExecOptions{
			User:         "1100", // run as pi-agent (not root)
			Cmd:          cmd,
			Env:          []string{"HOME=/home/pi-agent"},
			AttachStdin:  true,
			AttachStdout: true,
			AttachStderr: true,
			Tty:          true,
		})
		if execErr != nil {
			lastErr = execErr
			log.Printf("Exec create failed for %v: %v, trying next", cmd, execErr)
			continue
		}

		var attachErr error
		execAttach, attachErr = m.cli.ContainerExecAttach(r.Context(), execResp.ID, container.ExecAttachOptions{Tty: true})
		if attachErr != nil {
			lastErr = attachErr
			log.Printf("Exec attach failed for %v: %v, trying next", cmd, attachErr)
			continue
		}

		// Peek at the first bytes to detect OCI runtime errors
		// (binary not found errors come through stream, not attach error)
		execAttach.Conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		peek := make([]byte, 512)
		n, peekErr := execAttach.Reader.Read(peek)
		// Reset deadline (clear for normal operation)
		execAttach.Conn.SetReadDeadline(time.Time{})

		ociError := false
		if peekErr == nil && n > 0 {
			chunk := strings.ToLower(string(peek[:n]))
			if strings.Contains(chunk, "oci runtime") ||
				strings.Contains(chunk, "exec: \"") ||
				strings.Contains(chunk, "no such file or directory") ||
				strings.Contains(chunk, "command not found") {
				lastErr = fmt.Errorf("OCI exec error: %s", strings.TrimSpace(chunk))
				ociError = true
				log.Printf("OCI error for %v: %v, trying next", cmd, string(peek[:n]))
			}
		}

		if ociError {
			execAttach.Close()
			continue readLoop
		}

		// If we got valid output, re-push the peeked bytes back
		// (they contain real agent output like the picoclaw logo)
		if peekErr == nil && n > 0 {
			execAttach.Reader = bufio.NewReader(io.MultiReader(bytes.NewReader(peek[:n]), execAttach.Reader))
		}

		attachSucceeded = true
		log.Printf("Terminal attached via: %v", cmd)
		break
	}

	if !attachSucceeded {
		ws.WriteMessage(websocket.TextMessage,
			[]byte(fmt.Sprintf("\r\n\x1b[31m⚠ Failed to attach terminal: %v\r\n\x1b[0m", lastErr)))
		return
	}

	containerReader := execAttach.Reader
	containerWriter := execAttach.Conn
	defer execAttach.Close()

	// ── Bi-directional copy: WebSocket ↔ Container PTY ──
	errCh := make(chan error, 2)

	// Container stdout → WebSocket (BinaryMessage for terminal bytes — may contain
	// non-UTF8 control sequences that would corrupt TextMessage frames)
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
			if err := ws.WriteMessage(websocket.BinaryMessage, data); err != nil {
				errCh <- err
				return
			}
		}
	}()

	// WebSocket → Container stdin (binary frames) or control (JSON text frames)
	go func() {
		for {
			msgType, msg, err := ws.ReadMessage()
			if err != nil {
				errCh <- err
				return
			}
			switch msgType {
			case websocket.BinaryMessage:
				// Raw stdin bytes → PTY
				containerWriter.Write(msg)
			case websocket.TextMessage:
				// JSON control frame — parse and dispatch
				handleWSControl(r.Context(), m, execResp.ID, msg)
			}
		}
	}()

	<-errCh
}

// handleWSControl dispatches JSON WebSocket control frames from the frontend.
// Supported types: resize (PTY cols/rows).
func handleWSControl(ctx context.Context, m *Manager, execID string, msg []byte) {
	var ctrl struct {
		Type string `json:"type"`
		Cols uint   `json:"cols"`
		Rows uint   `json:"rows"`
	}
	if err := json.Unmarshal(msg, &ctrl); err != nil {
		log.Printf("WS control: bad JSON: %v", err)
		return
	}
	switch ctrl.Type {
	case "resize":
		if ctrl.Cols > 0 && ctrl.Rows > 0 {
			err := m.cli.ContainerExecResize(ctx, execID, container.ResizeOptions{
				Height: ctrl.Rows,
				Width:  ctrl.Cols,
			})
			if err != nil {
				log.Printf("WS resize %dx%d failed: %v", ctrl.Cols, ctrl.Rows, err)
			}
		}
	default:
		log.Printf("WS control: unknown type %q", ctrl.Type)
	}
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

// enforceQuota checks that a directory does not exceed the byte limit.
// Uses du -s (block-level summary, fast) for soft enforcement.
func enforceQuota(path string, limitBytes int64) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil // path doesn't exist yet, quota check passes
	}
	// du -s reports in 1024-byte blocks on Linux
	cmd := exec.Command("du", "-s", "--block-size=1", path)
	out, err := cmd.Output()
	if err != nil {
		// If du fails (e.g., path not available), allow creation
		log.Printf("quota check: du failed for %s: %v — allowing", path, err)
		return nil
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return nil
	}
	used, convErr := strconv.ParseInt(fields[0], 10, 64)
	if convErr != nil {
		log.Printf("quota check: cannot parse du output for %s: %v", path, convErr)
		return nil
	}
	if used > limitBytes {
		usedGB := float64(used) / (1024 * 1024 * 1024)
		limitGB := float64(limitBytes) / (1024 * 1024 * 1024)
		return fmt.Errorf("storage quota exceeded: %.1fGB used of %.0fGB limit — free up space", usedGB, limitGB)
	}
	return nil
}

// discoverFraoSkills returns the directory names of frao-skills available on the host.
// These are mounted into agent containers at /frao-skills/<name>.
func (m *Manager) discoverFraoSkills() []string {
	if m.cfg.FraoSkillsPath == "" {
		return nil
	}
	entries, err := os.ReadDir(m.cfg.FraoSkillsPath)
	if err != nil {
		return nil
	}
	var skills []string
	for _, e := range entries {
		if e.IsDir() {
			skills = append(skills, e.Name())
		}
	}
	return skills
}
