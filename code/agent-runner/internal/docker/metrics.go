// Package docker — Container metrics and control operations.
package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
)

// ── Metrics ────────────────────────────────────────────────

// ContainerMetrics holds resource usage statistics for a container.
type ContainerMetrics struct {
	ContainerID   string  `json:"container_id"`
	CPUPercent    float64 `json:"cpu_percent"`
	MemoryUsage   int64   `json:"memory_usage_bytes"`
	MemoryLimit   int64   `json:"memory_limit_bytes"`
	MemoryPercent float64 `json:"memory_percent"`
	IOReadBytes   int64   `json:"io_read_bytes"`
	IOWriteBytes  int64   `json:"io_write_bytes"`
	NetRxBytes    int64   `json:"net_rx_bytes"`
	NetTxBytes    int64   `json:"net_tx_bytes"`
	PIDCount      int64   `json:"pid_count"`
	UptimeSeconds int64   `json:"uptime_seconds"`
	Status        string  `json:"status"`
	Timestamp     string  `json:"timestamp"`
}

// GetMetrics retrieves real-time container resource metrics from Docker.
func (m *Manager) GetMetrics(ctx context.Context, sessionID string) (*ContainerMetrics, error) {
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

	// Get detailed stats (one-shot, no streaming)
	statsResp, err := m.cli.ContainerStatsOneShot(ctx, c.ID)
	if err != nil {
		return nil, fmt.Errorf("container stats: %w", err)
	}
	defer statsResp.Body.Close()

	statsData, err := io.ReadAll(statsResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read stats: %w", err)
	}

	var dockerStats container.StatsResponse
	if err := json.Unmarshal(statsData, &dockerStats); err != nil {
		return nil, fmt.Errorf("parse stats: %w", err)
	}

	metrics := &ContainerMetrics{
		ContainerID: c.ID,
		Status:      c.Status,
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
	}

	// CPU calculation
	cpuDelta := float64(dockerStats.CPUStats.CPUUsage.TotalUsage - dockerStats.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(dockerStats.CPUStats.SystemUsage - dockerStats.PreCPUStats.SystemUsage)
	numCPUs := float64(dockerStats.CPUStats.OnlineCPUs)
	if systemDelta > 0 && cpuDelta > 0 && numCPUs > 0 {
		metrics.CPUPercent = (cpuDelta / systemDelta) * numCPUs * 100.0
	}

	// Memory
	metrics.MemoryUsage = int64(dockerStats.MemoryStats.Usage)
	metrics.MemoryLimit = int64(dockerStats.MemoryStats.Limit)
	if metrics.MemoryLimit > 0 {
		metrics.MemoryPercent = float64(metrics.MemoryUsage) / float64(metrics.MemoryLimit) * 100.0
	}

	// IO (blkio)
	for _, bio := range dockerStats.BlkioStats.IoServiceBytesRecursive {
		if bio.Op == "Read" {
			metrics.IOReadBytes += int64(bio.Value)
		}
		if bio.Op == "Write" {
			metrics.IOWriteBytes += int64(bio.Value)
		}
	}

	// Network
	for _, netw := range dockerStats.Networks {
		metrics.NetRxBytes += int64(netw.RxBytes)
		metrics.NetTxBytes += int64(netw.TxBytes)
	}

	// PID count
	metrics.PIDCount = int64(dockerStats.PidsStats.Current)

	// Uptime — inspect container for StartTime
	insp, err := m.cli.ContainerInspect(ctx, c.ID)
	if err == nil && insp.State != nil {
		startedAt, parseErr := time.Parse(time.RFC3339Nano, insp.State.StartedAt)
		if parseErr == nil && !startedAt.IsZero() {
			metrics.UptimeSeconds = int64(time.Since(startedAt).Seconds())
		}
	}

	return metrics, nil
}

// ── Container Control ──────────────────────────────────────

// StopContainer gracefully stops a container (SIGTERM, then SIGKILL after timeout).
func (m *Manager) StopContainer(ctx context.Context, sessionID string) error {
	return m.controlContainer(ctx, sessionID, "stop", func(cID string) error {
		timeout := 10
		return m.cli.ContainerStop(ctx, cID, container.StopOptions{
			Timeout: &timeout,
		})
	})
}

// PauseContainer suspends all processes in a container (SIGSTOP equivalent).
func (m *Manager) PauseContainer(ctx context.Context, sessionID string) error {
	return m.controlContainer(ctx, sessionID, "pause", func(cID string) error {
		return m.cli.ContainerPause(ctx, cID)
	})
}

// ResumeContainer resumes a paused container (SIGCONT equivalent).
func (m *Manager) ResumeContainer(ctx context.Context, sessionID string) error {
	return m.controlContainer(ctx, sessionID, "resume", func(cID string) error {
		return m.cli.ContainerUnpause(ctx, cID)
	})
}

// KillContainer forcefully kills a container (SIGKILL).
func (m *Manager) KillContainer(ctx context.Context, sessionID string) error {
	return m.controlContainer(ctx, sessionID, "kill", func(cID string) error {
		return m.cli.ContainerKill(ctx, cID, "SIGKILL")
	})
}

// controlContainer finds the container for a session and applies the action.
func (m *Manager) controlContainer(ctx context.Context, sessionID, action string, fn func(string) error) error {
	containers, err := m.cli.ContainerList(ctx, container.ListOptions{
		All: true,
		Filters: filters.NewArgs(
			filters.Arg("label", fmt.Sprintf("llm-studio.session=%s", sessionID)),
		),
	})
	if err != nil {
		return fmt.Errorf("list containers: %w", err)
	}
	if len(containers) == 0 {
		return fmt.Errorf("session %s: no container found", sessionID)
	}

	c := containers[0]
	log.Printf("Container control: %s %s (%s)", action, c.ID[:12], sessionID)

	if err := fn(c.ID); err != nil {
		return fmt.Errorf("%s container: %w", action, err)
	}

	return nil
}
