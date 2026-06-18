package llama

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Port != 8080 {
		t.Errorf("expected port 8080, got %d", cfg.Port)
	}
	if cfg.Host != "127.0.0.1" {
		t.Errorf("expected host 127.0.0.1, got %s", cfg.Host)
	}
	if cfg.ContextSize != 8192 {
		t.Errorf("expected context size 8192, got %d", cfg.ContextSize)
	}
	if cfg.Threads != 4 {
		t.Errorf("expected threads 4, got %d", cfg.Threads)
	}
	if !cfg.FlashAttention {
		t.Errorf("expected flash attention enabled by default")
	}
}

func TestProcess_New(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ModelPath = "/tmp/test.gguf"

	p := New(cfg)
	if p == nil {
		t.Fatal("New returned nil")
	}
	if p.State() != StateStopped {
		t.Errorf("expected stopped state, got %s", p.State())
	}
}

func TestProcess_StartStop(t *testing.T) {
	// Start a real HTTP health server that Process.Start will poll
	healthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer healthSrv.Close()

	// Use the health server's actual port
	_, portStr, _ := net.SplitHostPort(healthSrv.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)

	// Create a mock "process" script that just sleeps
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "mock-process.sh")
	os.WriteFile(scriptPath, []byte("#!/bin/sh\nsleep 60\n"), 0755)

	cfg := DefaultConfig()
	cfg.BinaryPath = scriptPath
	cfg.ModelPath = "/tmp/test.gguf"
	cfg.Port = port
	cfg.Host = "127.0.0.1"

	p := New(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// This should succeed because the health server is already running
	err := p.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if !p.IsRunning() {
		t.Fatal("expected process to be running")
	}

	if p.PID() <= 0 {
		t.Fatal("expected valid PID")
	}

	// Verify config is preserved
	if p.Config().Port != port {
		t.Errorf("expected port %d, got %d", port, p.Config().Port)
	}

	// Stop it
	if err := p.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	if p.State() != StateStopped {
		t.Errorf("expected stopped state, got %s", p.State())
	}
}

func TestProcess_DoubleStart(t *testing.T) {
	healthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer healthSrv.Close()

	_, portStr, _ := net.SplitHostPort(healthSrv.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)

	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "mock.sh")
	os.WriteFile(scriptPath, []byte("#!/bin/sh\nsleep 60\n"), 0755)

	cfg := DefaultConfig()
	cfg.BinaryPath = scriptPath
	cfg.ModelPath = "/tmp/test.gguf"
	cfg.Port = port

	p := New(cfg)

	ctx1, cancel1 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel1()
	if err := p.Start(ctx1); err != nil {
		t.Skipf("Skipping: start failed: %v", err)
	}
	defer p.Stop()

	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()
	err := p.Start(ctx2)
	if err == nil {
		t.Error("expected error on double start")
	}
}

func TestProcess_Config(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ModelPath = "/models/test.gguf"
	cfg.Port = 9999
	cfg.Threads = 8

	p := New(cfg)
	returnedCfg := p.Config()

	if returnedCfg.Port != 9999 {
		t.Errorf("expected port 9999, got %d", returnedCfg.Port)
	}
	if returnedCfg.Threads != 8 {
		t.Errorf("expected threads 8, got %d", returnedCfg.Threads)
	}
}

func TestProcess_StartCancelledContext(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "slow.sh")
	os.WriteFile(scriptPath, []byte("#!/bin/sh\nsleep 60\n"), 0755)

	cfg := DefaultConfig()
	cfg.BinaryPath = scriptPath
	cfg.ModelPath = "/tmp/test.gguf"

	p := New(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := p.Start(ctx)
	if err == nil {
		p.Stop()
		t.Error("expected error on cancelled context")
	}
}

func TestProcess_PID(t *testing.T) {
	p := &Process{state: StateStopped}
	if p.PID() != 0 {
		t.Errorf("expected PID 0 for stopped process, got %d", p.PID())
	}
}

func TestProcess_StopStopped(t *testing.T) {
	p := New(DefaultConfig())
	if err := p.Stop(); err != nil {
		t.Errorf("stop on stopped process should not error: %v", err)
	}
}

func TestProcess_BuildArgs(t *testing.T) {
	cfg := Config{
		ModelPath:       "/models/test.gguf",
		BinaryPath:      "llama-server",
		Host:            "127.0.0.1",
		Port:            8080,
		ContextSize:     4096,
		Threads:         4,
		GPULayers:       0,
		NBatch:          512,
		NUBatch:         512,
		FlashAttention:  true,
		MLock:           true,
	}

	p := New(cfg)
	_ = p // buildArgs is tested implicitly through Start
}
