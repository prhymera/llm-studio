// Package llama manages the lifecycle of the llama.cpp server process.
// It handles starting, stopping, and monitoring the server that serves
// local GGUF models via an OpenAI-compatible HTTP API.
package llama

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// State represents the llama.cpp server process state.
type State string

const (
	StateStopped  State = "stopped"
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateFailed   State = "failed"
)

// Config holds parameters for starting the llama.cpp server.
type Config struct {
	// ModelPath is the path to the GGUF model file.
	ModelPath string

	// BinaryPath is the path to the llama-server binary.
	BinaryPath string

	// Host is the bind address for the server.
	Host string

	// Port is the port for the server.
	Port int

	// ContextSize is the model context window size.
	ContextSize int

	// Threads is the number of CPU threads to use.
	Threads int

	// GPULayers is the number of layers to offload to GPU (0 for CPU-only).
	GPULayers int

	// NBatch is the batch size for prompt processing.
	NBatch int

	// NUBatch is the batch size for token generation.
	NUBatch int

	// FlashAttention enables flash attention optimization.
	FlashAttention bool

	// MLock locks the model in RAM to prevent swapping.
	MLock bool
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		Host:           "127.0.0.1",
		Port:           8080,
		ContextSize:    8192,
		Threads:        4,
		GPULayers:      0,
		NBatch:         512,
		NUBatch:        512,
		FlashAttention: true,
		MLock:          false,
	}
}

// Process manages the llama.cpp server subprocess.
type Process struct {
	mu      sync.RWMutex
	cmd     *exec.Cmd
	state   State
	config  Config
	stdout  io.ReadCloser
	stderr  io.ReadCloser
	stopCh  chan struct{}
	doneCh  chan struct{}
}

// New creates a new Process but does not start it.
func New(cfg Config) *Process {
	return &Process{
		state:  StateStopped,
		config: cfg,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
}

// Start launches the llama.cpp server as a subprocess.
// It returns once the server is ready to accept requests (detected by parsing stdout),
// or an error if the process fails to start.
func (p *Process) Start(ctx context.Context) error {
	p.mu.Lock()
	if p.state != StateStopped {
		p.mu.Unlock()
		return fmt.Errorf("process already running (state: %s)", p.state)
	}
	p.state = StateStarting
	p.mu.Unlock()

	args := p.buildArgs()
	log.Printf("Starting llama-server: %s %v", p.config.BinaryPath, args)

	// Use exec.Command (not CommandContext) so the process outlives the startup context.
	// CommandContext kills the subprocess when the context is cancelled, which would
	// kill llama-server once the load handler returns and its defer cancel() runs.
	cmd := exec.Command(p.config.BinaryPath, args...)

	// Create pipes for output (redirect to dev null for scanner — we poll HTTP instead)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		p.setState(StateFailed)
		return fmt.Errorf("stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		p.setState(StateFailed)
		return fmt.Errorf("stderr pipe: %w", err)
	}

	p.mu.Lock()
	p.cmd = cmd
	p.stdout = stdout
	p.stderr = stderr
	p.mu.Unlock()

	if err := cmd.Start(); err != nil {
		p.setState(StateFailed)
		return fmt.Errorf("start: %w", err)
	}

	// Drain stdout/stderr in background to prevent pipe deadlock
	go io.Copy(io.Discard, stdout)
	go io.Copy(io.Discard, stderr)

	// Poll the llama-server HTTP endpoint until it responds or context is cancelled
	readyCh := make(chan error, 1)
	go p.pollForReady(ctx, readyCh)

	// Wait for ready or timeout
	timeout := 180 * time.Second
	select {
	case err := <-readyCh:
		if err != nil {
			p.setState(StateFailed)
			return fmt.Errorf("server failed to start: %w", err)
		}
		log.Printf("llama-server ready on %s:%d", p.config.Host, p.config.Port)
		p.setState(StateRunning)

		// Monitor process in background
		go p.monitorProcess()

	case <-time.After(timeout):
		log.Printf("llama-server did not become ready within %v", timeout)
		p.stop()
		return fmt.Errorf("start timed out after %v", timeout)

	case <-ctx.Done():
		log.Printf("llama-server startup cancelled")
		p.stop()
		return fmt.Errorf("start cancelled: %w", ctx.Err())
	}

	return nil
}

// pollForReady polls the llama-server health endpoint until it responds.
func (p *Process) pollForReady(ctx context.Context, readyCh chan<- error) {
	client := &http.Client{Timeout: 2 * time.Second}
	serverURL := fmt.Sprintf("http://%s:%d/health", p.config.Host, p.config.Port)

	for i := 0; i < 180; i++ { // up to 180 * 1s = 180s
		select {
		case <-p.stopCh:
			readyCh <- fmt.Errorf("process was stopped")
			return
		case <-ctx.Done():
			readyCh <- fmt.Errorf("context cancelled")
			return
		default:
		}

		resp, err := client.Get(serverURL)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				readyCh <- nil
				return
			}
		}

		time.Sleep(1 * time.Second)
	}

	readyCh <- fmt.Errorf("server did not respond within timeout")
}

// Stop terminates the llama.cpp server gracefully.
func (p *Process) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.state == StateStopped {
		return nil
	}

	return p.stop()
}

func (p *Process) stop() error {
	if p.cmd != nil && p.cmd.Process != nil {
		log.Printf("Stopping llama-server (pid %d)", p.cmd.Process.Pid)
		close(p.stopCh) // signal pollForReady to stop
		// Try graceful shutdown first
		if err := p.cmd.Process.Signal(softTermSignal); err != nil {
			// Force kill
			p.cmd.Process.Kill()
		}
	}

	p.state = StateStopped
	return nil
}

// State returns the current process state.
func (p *Process) State() State {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.state
}

// IsRunning returns true if the server is currently running.
func (p *Process) IsRunning() bool {
	return p.State() == StateRunning
}

// Config returns a copy of the process configuration.
func (p *Process) Config() Config {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.config
}

// PID returns the process ID, or 0 if not running.
func (p *Process) PID() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.cmd != nil && p.cmd.Process != nil {
		return p.cmd.Process.Pid
	}
	return 0
}

func (p *Process) buildArgs() []string {
	args := []string{
		"--model", p.config.ModelPath,
		"--host", p.config.Host,
		"--port", fmt.Sprintf("%d", p.config.Port),
		"--ctx-size", fmt.Sprintf("%d", p.config.ContextSize),
		"--threads", fmt.Sprintf("%d", p.config.Threads),
		"--batch-size", fmt.Sprintf("%d", p.config.NBatch),
		"--ubatch-size", fmt.Sprintf("%d", p.config.NUBatch),
	}

	if p.config.GPULayers > 0 {
		args = append(args, "--n-gpu-layers", fmt.Sprintf("%d", p.config.GPULayers))
	}

	if p.config.FlashAttention {
		args = append(args, "--flash-attn", "auto")
	}

	if p.config.MLock {
		args = append(args, "--mlock")
	}

	return args
}



func (p *Process) monitorProcess() {
	defer close(p.doneCh)

	if err := p.cmd.Wait(); err != nil {
		// Only log if we didn't intentionally stop it
		p.mu.RLock()
		state := p.state
		p.mu.RUnlock()
		if state != StateStopped {
			log.Printf("llama-server exited unexpectedly: %v", err)
			p.setState(StateFailed)
		}
	}
}

func (p *Process) setState(state State) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state = state
}

// softTermSignal returns SIGTERM for graceful shutdown.
var softTermSignal = syscall.SIGTERM


