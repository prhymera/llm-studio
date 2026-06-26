// Package docker — pi-web.dev integration for pi.dev agent sessions.
package docker

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
)

const (
	piWebDefaultPort = 8504
	piWebMaxRetries  = 30
	piWebRetryDelay  = 500 * time.Millisecond
)

// PiWebStatus holds the status of the pi-web.dev UI for a session.
type PiWebStatus struct {
	Enabled bool   `json:"enabled"`
	Running bool   `json:"running"`
	URL     string `json:"url,omitempty"`
	Port    int    `json:"port,omitempty"`
	BindIP  string `json:"bind_ip"`
	Message string `json:"message,omitempty"`
}

// GetPiWebStatus checks if pi-web.dev is running in the container.
func (m *Manager) GetPiWebStatus(ctx context.Context, sessionID string) (*PiWebStatus, error) {
	containers, err := m.cli.ContainerList(ctx, container.ListOptions{
		All: true,
		Filters: filters.NewArgs(
			filters.Arg("label", fmt.Sprintf("llm-studio.session=%s", sessionID)),
		),
	})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	if len(containers) == 0 {
		return nil, fmt.Errorf("session %s: no container found", sessionID)
	}

	c := containers[0]

	status := &PiWebStatus{
		Enabled: false,
		Running: false,
		BindIP:  detectBindIP(),
	}

	// Check if pi-web.dev is configured for this session
	insp, inspErr := m.cli.ContainerInspect(ctx, c.ID)
	if inspErr == nil && insp.Config != nil {
		for _, env := range insp.Config.Env {
			if strings.HasPrefix(env, "PI_WEB_ENABLED=") {
				val := strings.TrimPrefix(env, "PI_WEB_ENABLED=")
				status.Enabled = (val == "true" || val == "1")
			}
			if strings.HasPrefix(env, "PI_WEB_PORT=") {
				fmt.Sscanf(strings.TrimPrefix(env, "PI_WEB_PORT="), "%d", &status.Port)
			}
		}
	}

	if !status.Enabled {
		status.Message = "pi-web.dev is not enabled for this session"
		return status, nil
	}

	if status.Port == 0 {
		status.Port = piWebDefaultPort
	}

	// Check if pi-web.dev process is running inside container
	execResp, execErr := m.cli.ContainerExecCreate(ctx, c.ID, container.ExecOptions{
		Cmd:          []string{"sh", "-c", fmt.Sprintf("wget -qO- http://127.0.0.1:%d/api/health 2>/dev/null || echo FAIL", status.Port)},
		AttachStdout: true,
		AttachStderr: true,
	})

	if execErr == nil {
		execAttach, attachErr := m.cli.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
		if attachErr == nil {
			defer execAttach.Close()
			buf := make([]byte, 1024)
			n, _ := execAttach.Reader.Read(buf)
			output := string(buf[:n])
			if !strings.Contains(output, "FAIL") && strings.Contains(strings.ToLower(output), "ok") {
				status.Running = true
			}
		}
	}

	hostIP := detectBindIP()
	if status.Running {
		status.URL = fmt.Sprintf("http://%s:%d", hostIP, status.Port)
		status.Message = "pi-web.dev is running"
	} else {
		status.Message = "pi-web.dev is not running — click Start to launch"
	}

	return status, nil
}

// StartPiWeb launches pi-web.dev inside the container for the given session.
func (m *Manager) StartPiWeb(ctx context.Context, sessionID string) (*PiWebStatus, error) {
	containers, err := m.cli.ContainerList(ctx, container.ListOptions{
		All: true,
		Filters: filters.NewArgs(
			filters.Arg("label", fmt.Sprintf("llm-studio.session=%s", sessionID)),
		),
	})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	if len(containers) == 0 {
		return nil, fmt.Errorf("session %s: no container found", sessionID)
	}

	c := containers[0]

	if c.State != "running" {
		return nil, fmt.Errorf("container is not running (state: %s)", c.State)
	}

	// Assign a dynamic port
	port, err := findAvailablePort()
	if err != nil {
		return nil, fmt.Errorf("find available port: %w", err)
	}

	// Launch pi-web.dev inside the container
	execResp, execErr := m.cli.ContainerExecCreate(ctx, c.ID, container.ExecOptions{
		Cmd:          []string{"sh", "-c", fmt.Sprintf("PI_WEB_PORT=%d nohup pi-web-server > /tmp/piweb.log 2>&1 & echo \"pi-web started on port %d\"", port, port)},
		AttachStdout: true,
		AttachStderr: true,
	})

	if execErr != nil {
		return nil, fmt.Errorf("exec create: %w", execErr)
	}

	execAttach, attachErr := m.cli.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if attachErr != nil {
		return nil, fmt.Errorf("exec attach: %w", attachErr)
	}
	defer execAttach.Close()

	buf := make([]byte, 256)
	n, _ := execAttach.Reader.Read(buf)
	output := string(buf[:n])
	log.Printf("pi-web.dev launch output: %s", strings.TrimSpace(output))

	// Wait for pi-web.dev to be ready
	for i := 0; i < piWebMaxRetries; i++ {
		time.Sleep(piWebRetryDelay)
		healthResp, healthErr := m.cli.ContainerExecCreate(ctx, c.ID, container.ExecOptions{
			Cmd:          []string{"sh", "-c", fmt.Sprintf("wget -qO- http://127.0.0.1:%d/api/health 2>/dev/null || echo FAIL", port)},
			AttachStdout: true,
		})
		if healthErr == nil {
			healthAttach, hErr := m.cli.ContainerExecAttach(ctx, healthResp.ID, container.ExecAttachOptions{})
			if hErr == nil {
				hb := make([]byte, 512)
				hn, _ := healthAttach.Reader.Read(hb)
				healthAttach.Close()
				if !strings.Contains(string(hb[:hn]), "FAIL") {
					hostIP := detectBindIP()
					log.Printf("pi-web.dev is ready on %s:%d", hostIP, port)
					return &PiWebStatus{
						Enabled: true,
						Running: true,
						URL:     fmt.Sprintf("http://%s:%d", hostIP, port),
						Port:    port,
						BindIP:  hostIP,
						Message: "pi-web.dev is running",
					}, nil
				}
			}
		}
	}

	hostIP := detectBindIP()
	return &PiWebStatus{
		Enabled: true,
		Running: false,
		URL:     fmt.Sprintf("http://%s:%d", hostIP, port),
		Port:    port,
		BindIP:  hostIP,
		Message: "pi-web.dev was launched but not yet ready — try again in a moment",
	}, nil
}

// detectBindIP returns the configured or detected non-loopback IPv4 address.
func detectBindIP() string {
	if ip := os.Getenv("BIND_IP"); ip != "" {
		return ip
	}
	if ip := os.Getenv("HOST_IP"); ip != "" {
		return ip
	}
	addrs, err := net.InterfaceAddrs()
	if err == nil {
		for _, addr := range addrs {
			if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() && ipNet.IP.To4() != nil {
				return ipNet.IP.String()
			}
		}
	}
	return "127.0.0.1"
}

// findAvailablePort finds an available TCP port for dynamic assignment.
func findAvailablePort() (int, error) {
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}
