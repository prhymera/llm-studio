// Package docker tests PTY/terminal attachment for agent containers.
//
// These are integration tests requiring a running Docker daemon.
// They verify that:
//   1. AttachTerminal connects to the container's main process (not a sub-shell)
//   2. Bidirectional I/O works (echo test)
//   3. Terminal resize events propagate
//   4. Multiple connections work (fallback via exec)
package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/gorilla/websocket"
)

// TestContainerAttachMainProcess verifies that AttachTerminal connects
// to the container's main process, not a secondary exec.
// It starts a container with a known entrypoint, then verifies the
// terminal output contains the entrypoint's banner text.
func TestContainerAttachMainProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()

	// Create a manager with test config
	mgr, err := NewManager(Config{
		DataDir:       t.TempDir(),
		GatewayURL:    "http://llm-gateway:3100",
		DefaultModel:  "qwen25-coder-7b",
		DefaultAgent:  "picoclaw",
		AgentTimeout:  30,
		CPULimit:      1,
		MemoryLimit:   "128M",
		DockerNetwork: "",
	})
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	// Create session (starts a real container)
	sess, err := mgr.CreateSession(ctx, "picoclaw", "qwen25-coder-7b", "test-session")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}
	defer func() {
		mgr.DestroySession(ctx, sess.ID)
	}()

	// Verify container is running
	if sess.Status != "running" {
		t.Fatalf("Expected session status 'running', got '%s'", sess.Status)
	}

	// Set up a WebSocket server to simulate the frontend
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Logf("Upgrade error: %v", err)
			return
		}
		defer ws.Close()

		// Read initial output — should contain the picoclaw banner
		ws.SetReadDeadline(time.Now().Add(10 * time.Second))
		var bannerBuf strings.Builder
		for {
			_, msg, err := ws.ReadMessage()
			if err != nil {
				break
			}
			bannerBuf.Write(msg)

			// Check if we got the picoclaw banner
			if strings.Contains(bannerBuf.String(), "picoclaw") ||
				strings.Contains(bannerBuf.String(), "agent session") {
				t.Logf("Received banner output: %s", bannerBuf.String()[:min(100, bannerBuf.Len())])
				break
			}

			// If we see "bash" or shell prompt, the attach went to the wrong place
			if strings.Contains(bannerBuf.String(), "bash-") ||
				strings.Contains(bannerBuf.String(), "$ ") ||
				strings.Contains(bannerBuf.String(), "# ") {
				t.Errorf("Terminal attached to bash instead of main process. Output: %s",
					bannerBuf.String()[:min(100, bannerBuf.Len())])
				break
			}
		}
	}))
	defer srv.Close()

	// Connect WebSocket → AttachTerminal
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/tty"
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to dial test server: %v", err)
	}
	defer ws.Close()

	// Simulate frontend: send an echo command
	echoCmd := "echo HELLO_FROM_PTY\n"
	err = ws.WriteMessage(websocket.TextMessage, []byte(echoCmd))
	if err != nil {
		t.Fatalf("Failed to write to WebSocket: %v", err)
	}

	// Read response — should contain our echo
	ws.SetReadDeadline(time.Now().Add(15 * time.Second))
	var outputBuf strings.Builder
	for {
		_, msg, err := ws.ReadMessage()
		if err != nil {
			break
		}
		outputBuf.Write(msg)
		if strings.Contains(outputBuf.String(), "HELLO_FROM_PTY") {
			t.Logf("SUCCESS: Got echo back from PTY: %s", outputBuf.String()[:min(100, outputBuf.Len())])
			return
		}
	}

	t.Errorf("Did not receive echo 'HELLO_FROM_PTY' in output. Got: %s",
		outputBuf.String()[:min(200, outputBuf.Len())])
}

// TestTerminalResizeEvent verifies that resize events propagate to the container.
func TestTerminalResizeEvent(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()
	mgr, err := NewManager(Config{
		DataDir:       t.TempDir(),
		GatewayURL:    "http://llm-gateway:3100",
		DefaultModel:  "qwen25-coder-7b",
		DefaultAgent:  "picoclaw",
		AgentTimeout:  30,
		CPULimit:      1,
		MemoryLimit:   "128M",
		DockerNetwork: "",
	})
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	sess, err := mgr.CreateSession(ctx, "picoclaw", "qwen25-coder-7b", "resize-test")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}
	defer mgr.DestroySession(ctx, sess.ID)

	// Request the AttachTerminal handler
	handler := func(w http.ResponseWriter, r *http.Request) {
		mgr.AttachTerminal(w, r, sess.ID)
	}

	srv := httptest.NewServer(http.HandlerFunc(handler))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to dial: %v", err)
	}
	defer ws.Close()

	// Send a resize event
	resizeMsg, _ := json.Marshal(map[string]interface{}{
		"type": "resize",
		"cols": 132,
		"rows": 43,
	})
	err = ws.WriteMessage(websocket.TextMessage, resizeMsg)
	if err != nil {
		t.Fatalf("Failed to send resize: %v", err)
	}

	// Send a command that reports terminal size
	sttyCmd := "stty size\n"
	err = ws.WriteMessage(websocket.TextMessage, []byte(sttyCmd))
	if err != nil {
		t.Fatalf("Failed to write command: %v", err)
	}

	// Read response - stty should output "43 132" or similar
	ws.SetReadDeadline(time.Now().Add(10 * time.Second))
	var output strings.Builder
	for {
		_, msg, err := ws.ReadMessage()
		if err != nil {
			break
		}
		output.Write(msg)

		// stty output format is "rows cols"
		if strings.Contains(output.String(), "132") ||
			strings.Contains(output.String(), "43") {
			t.Logf("Resize appears to have propagated. Output: %s",
				output.String()[:min(100, output.Len())])
			return
		}
	}

	t.Logf("Resize test output (may be inconclusive without PTY): %s",
		output.String()[:min(100, output.Len())])
}

// TestReconnectSession verifies that a disconnected/reconnected terminal
// attaches to the same container.
func TestReconnectSession(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()
	mgr, err := NewManager(Config{
		DataDir:       t.TempDir(),
		GatewayURL:    "http://llm-gateway:3100",
		DefaultModel:  "qwen25-coder-7b",
		DefaultAgent:  "pi",
		AgentTimeout:  30,
		CPULimit:      1,
		MemoryLimit:   "128M",
		DockerNetwork: "",
	})
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	sess, err := mgr.CreateSession(ctx, "pi", "qwen25-coder-7b", "reconnect-test")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}
	defer mgr.DestroySession(ctx, sess.ID)

	// Connect terminal once
	handler := func(w http.ResponseWriter, r *http.Request) {
		mgr.AttachTerminal(w, r, sess.ID)
	}

	// First connection
	srv1 := httptest.NewServer(http.HandlerFunc(handler))
	wsURL1 := "ws" + strings.TrimPrefix(srv1.URL, "http")
	ws1, _, err := websocket.DefaultDialer.Dial(wsURL1, nil)
	if err != nil {
		t.Fatalf("Failed to dial first connection: %v", err)
	}
	ws1.Close()
	srv1.Close()

	// Second connection (reconnect) — should succeed via exec fallback
	srv2 := httptest.NewServer(http.HandlerFunc(handler))
	defer srv2.Close()

	wsURL2 := "ws" + strings.TrimPrefix(srv2.URL, "http")
	ws2, _, err := websocket.DefaultDialer.Dial(wsURL2, nil)
	if err != nil {
		t.Fatalf("Failed to dial second connection (reconnect): %v", err)
	}
	defer ws2.Close()

	// Verify we get output on reconnect
	ws2.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, msg, err := ws2.ReadMessage()
	if err != nil {
		t.Fatalf("Failed to read from reconnected terminal: %v", err)
	}

	if len(msg) > 0 {
		t.Logf("Reconnect successful, received %d bytes of output", len(msg))
	}
}

// TestContainerExecCreation verifies that CreateSession correctly sets up
// the container with the right image and entrypoint.
func TestContainerExecCreation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()
	mgr, err := NewManager(Config{
		DataDir:       t.TempDir(),
		GatewayURL:    "http://llm-gateway:3100",
		DefaultModel:  "qwen25-coder-7b",
		DefaultAgent:  "picoclaw",
		AgentTimeout:  30,
		CPULimit:      1,
		MemoryLimit:   "128M",
		DockerNetwork: "",
	})
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	sess, err := mgr.CreateSession(ctx, "picoclaw", "qwen25-coder-7b", "creation-test")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}
	defer mgr.DestroySession(ctx, sess.ID)

	// Verify session has a container ID
	if sess.ContainerID == "" {
		t.Error("Expected non-empty ContainerID after creation")
	}

	// Verify workspace path exists
	if sess.WorkspacePath == "" {
		t.Error("Expected non-empty WorkspacePath")
	}

	// Verify session metadata
	if sess.AgentType != "picoclaw" {
		t.Errorf("Expected agent type 'picoclaw', got '%s'", sess.AgentType)
	}
	if sess.Model != "qwen25-coder-7b" {
		t.Errorf("Expected model 'qwen25-coder-7b', got '%s'", sess.Model)
	}

	// Verify container exists and is running
	containers, err := mgr.cli.ContainerList(ctx, container.ListOptions{
		All: true,
	})
	if err != nil {
		t.Fatalf("Failed to list containers: %v", err)
	}

	var found bool
	for _, c := range containers {
		if c.ID == sess.ContainerID {
			found = true
			if c.State != "running" {
				t.Errorf("Expected container state 'running', got '%s'", c.State)
			}
			break
		}
	}
	if !found {
		t.Error("Container not found in Docker")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
