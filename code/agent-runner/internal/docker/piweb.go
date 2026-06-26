// Package docker — pi-web.dev integration for pi.dev agent sessions.
package docker

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
)

const (
	piWebDefaultPort = 8504
	piWebMaxRetries  = 30
	piWebRetryDelay  = 500 * time.Millisecond
)

// piWebProxy tracks an active TCP proxy forwarding external traffic
// to an agent container's internal pi-web.dev port.
type piWebProxy struct {
	listener net.Listener
	sessionID string
	port     int
}

// piWebProxyRegistry holds active pi-web.dev TCP proxies keyed by session ID.
var (
	piWebProxyRegistry   = map[string]*piWebProxy{}
	piWebProxyRegistryMu sync.Mutex
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

	// Check if there's an active proxy for this session
	piWebProxyRegistryMu.Lock()
	proxy, hasProxy := piWebProxyRegistry[sessionID]
	if hasProxy && proxy != nil {
		status.Enabled = true
		status.Port = proxy.port
	}
	piWebProxyRegistryMu.Unlock()

	if !status.Enabled {
		status.Message = "pi-web.dev is not enabled for this session"
		return status, nil
	}

	// Check if pi-web.dev process is running inside container
	if c.State == "running" {
		healthPort := piWebDefaultPort
		if status.Port != 0 {
			healthPort = status.Port
		}
		execResp, execErr := m.cli.ContainerExecCreate(ctx, c.ID, container.ExecOptions{
			Cmd:          []string{"sh", "-c", fmt.Sprintf("wget -qO- http://127.0.0.1:%d/api/health 2>/dev/null || echo FAIL", healthPort)},
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
	}

	if status.Running {
		status.URL = fmt.Sprintf("http://%s:%d", detectBindIP(), status.Port)
		status.Message = "pi-web.dev is running"
	} else {
		status.Message = "pi-web.dev is not running — click Start to launch"
	}

	return status, nil
}

// StartPiWeb launches pi-web.dev inside the container and creates a TCP proxy
// on the agent-runner's VPN IP to forward traffic to the container.
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

	// Get container internal IP for proxying
	insp, inspErr := m.cli.ContainerInspect(ctx, c.ID)
	if inspErr != nil {
		return nil, fmt.Errorf("inspect container: %w", inspErr)
	}
	containerIP := insp.NetworkSettings.IPAddress
	if containerIP == "" {
		// Try networks map
		for _, net := range insp.NetworkSettings.Networks {
			if net.IPAddress != "" {
				containerIP = net.IPAddress
				break
			}
		}
	}
	if containerIP == "" {
		return nil, fmt.Errorf("cannot determine container IP")
	}

	// Check if proxy already exists
	piWebProxyRegistryMu.Lock()
	if existing, ok := piWebProxyRegistry[sessionID]; ok {
		piWebProxyRegistryMu.Unlock()
		// Proxy exists — verify pi-web is running
		if err := checkPiWebHealth(ctx, m, c.ID); err == nil {
			return &PiWebStatus{
				Enabled: true,
				Running: true,
				URL:     fmt.Sprintf("http://%s:%d", detectBindIP(), existing.port),
				Port:    existing.port,
				BindIP:  detectBindIP(),
				Message: "pi-web.dev is already running",
			}, nil
		}
		// pi-web died but proxy exists — stop proxy, restart below
		existing.listener.Close()
		delete(piWebProxyRegistry, sessionID)
	}
	piWebProxyRegistryMu.Unlock()

	// Find an available port on the BIND_IP
	bindIP := detectBindIP()
	hostPort, err := findAvailablePortOnIP(bindIP)
	if err != nil {
		return nil, fmt.Errorf("find available port on %s: %w", bindIP, err)
	}

	// Launch pi-web.dev inside the container on the default port
	execResp, execErr := m.cli.ContainerExecCreate(ctx, c.ID, container.ExecOptions{
		Cmd:          []string{"sh", "-c", fmt.Sprintf("nohup pi-web-server --port %d > /tmp/piweb.log 2>&1 & echo \"pi-web started on port %d\"", piWebDefaultPort, piWebDefaultPort)},
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
	log.Printf("pi-web.dev launch output: %s", strings.TrimSpace(string(buf[:n])))

	// Wait for pi-web.dev to be ready inside the container
	ready := false
	for i := 0; i < piWebMaxRetries; i++ {
		time.Sleep(piWebRetryDelay)
		if checkPiWebHealth(ctx, m, c.ID) == nil {
			ready = true
			break
		}
	}

	if !ready {
		return &PiWebStatus{
			Enabled: true,
			Running: false,
			Port:    hostPort,
			BindIP:  bindIP,
			Message: "pi-web.dev was launched but not yet ready — try again in a moment",
		}, nil
	}

	// Start TCP proxy: bind_ip:hostPort -> container_ip:piWebDefaultPort
	targetAddr := fmt.Sprintf("%s:%d", containerIP, piWebDefaultPort)
	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", bindIP, hostPort))
	if err != nil {
		return nil, fmt.Errorf("start proxy listener on %s:%d: %w", bindIP, hostPort, err)
	}

	proxy := &piWebProxy{
		listener:  listener,
		sessionID: sessionID,
		port:     hostPort,
	}

	go runPiWebProxy(proxy, targetAddr)

	piWebProxyRegistryMu.Lock()
	piWebProxyRegistry[proxy.sessionID] = proxy
	piWebProxyRegistryMu.Unlock()

	log.Printf("pi-web.dev proxy started: %s:%d → %s", bindIP, hostPort, targetAddr)

	return &PiWebStatus{
		Enabled: true,
		Running: true,
		URL:     fmt.Sprintf("http://%s:%d", bindIP, hostPort),
		Port:    hostPort,
		BindIP:  bindIP,
		Message: "pi-web.dev is running",
	}, nil
}

// StopPiWeb stops the pi-web.dev proxy for a session without removing the container.
func StopPiWeb(sessionID string) {
	piWebProxyRegistryMu.Lock()
	defer piWebProxyRegistryMu.Unlock()

	if proxy, ok := piWebProxyRegistry[sessionID]; ok {
		proxy.listener.Close()
		delete(piWebProxyRegistry, sessionID)
		log.Printf("pi-web.dev proxy stopped for session %s", sessionID)
	}
}

// StopAllPiWebProxies stops all active pi-web.dev proxies.
func StopAllPiWebProxies() {
	piWebProxyRegistryMu.Lock()
	defer piWebProxyRegistryMu.Unlock()

	for id, proxy := range piWebProxyRegistry {
		proxy.listener.Close()
		delete(piWebProxyRegistry, id)
		log.Printf("pi-web.dev proxy stopped for session %s", id)
	}
}

// runPiWebProxy accepts connections on the listener and forwards them to targetAddr.
func runPiWebProxy(proxy *piWebProxy, targetAddr string) {
	for {
		conn, err := proxy.listener.Accept()
		if err != nil {
			if !isClosedNetworkErr(err) {
				log.Printf("pi-web proxy accept error (session %s): %v", proxy.sessionID, err)
			}
			return
		}
		go func(client net.Conn) {
			defer client.Close()
			backend, err := net.DialTimeout("tcp", targetAddr, 5*time.Second)
			if err != nil {
				log.Printf("pi-web proxy dial %s: %v", targetAddr, err)
				return
			}
			defer backend.Close()

			// Bidirectional copy
			done := make(chan struct{}, 2)
			go func() { io.Copy(backend, client); done <- struct{}{} }()
			go func() { io.Copy(client, backend); done <- struct{}{} }()
			<-done
		}(conn)
	}
}

// checkPiWebHealth verifies pi-web.dev is responding inside the container.
func checkPiWebHealth(ctx context.Context, m *Manager, containerID string) error {
	execResp, err := m.cli.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          []string{"sh", "-c", fmt.Sprintf("wget -qO- http://127.0.0.1:%d/api/health 2>/dev/null || echo FAIL", piWebDefaultPort)},
		AttachStdout: true,
	})
	if err != nil {
		return err
	}
	execAttach, err := m.cli.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return err
	}
	defer execAttach.Close()
	buf := make([]byte, 512)
	n, _ := execAttach.Reader.Read(buf)
	if strings.Contains(string(buf[:n]), "FAIL") {
		return fmt.Errorf("health check failed")
	}
	return nil
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

// findAvailablePortOnIP finds an available TCP port on a specific IP in the pi-web.dev range.
func findAvailablePortOnIP(bindIP string) (int, error) {
	for port := 44000; port <= 44999; port++ {
		listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", bindIP, port))
		if err == nil {
			listener.Close()
			return port, nil
		}
	}
	return 0, fmt.Errorf("no available ports in range 44000-44999 on %s", bindIP)
}

// isClosedNetworkErr returns true if the error indicates the listener was closed.
func isClosedNetworkErr(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "use of closed network connection")
}
