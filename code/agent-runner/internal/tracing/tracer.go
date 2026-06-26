// ─────────────────────────────────────────────────────────────
// resolution-tracing — Function Tracing & Error Reporting for Go
// Frao Technologies LLC — Agent Dashboard Service
// ─────────────────────────────────────────────────────────────
//
// Provides:
//   - TraceFn() — wrap any function call with automatic tracing
//   - ErrorReport — structured error payload for resolution-collector
//   - ReportError() — async HTTP submission to collector
//   - Correlation ID propagation via context.Context
//   - Thread-local trace buffer for batching
//
// Usage:
//   import "github.com/prhymera/llm-studio/code/agent-runner/internal/tracing"
//
//   result, err := tracing.TraceFn(ctx, "compute", func() (interface{}, error) {
//       return doWork(), nil
//   })
// ─────────────────────────────────────────────────────────────

package tracing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	defaultCollectorURL = "http://resolution-collector:9090"
	maxTraceBuffer      = 500
	reportTimeout       = 5 * time.Second
)

// ── Configuration ──────────────────────────────────────────

var (
	collectorURL = envOrDefault("RESOLUTION_COLLECTOR_URL", defaultCollectorURL)
	serviceName  = envOrDefault("RESOLUTION_SERVICE_NAME", "agent-dashboard-svc")
	version      = envOrDefault("APP_VERSION", "0.2.0")
)

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ── Context Keys ───────────────────────────────────────────

type contextKey string

const (
	correlationIDKey contextKey = "correlation_id"
	traceIDKey       contextKey = "trace_id"
)

// ── Trace Event ────────────────────────────────────────────

// TraceEvent captures a single function invocation trace.
type TraceEvent struct {
	Function       string  `json:"function"`
	ArgsSig        string  `json:"args_sig,omitempty"`
	ResultSig      string  `json:"result_sig,omitempty"`
	Error          string  `json:"error,omitempty"`
	DurationMs     float64 `json:"duration_ms"`
	Timestamp      string  `json:"timestamp"`
	CorrelationID  string  `json:"correlation_id"`
	TraceID        string  `json:"trace_id"`
	ParentTraceID  string  `json:"parent_trace_id,omitempty"`
}

// ErrorReport is the payload sent to the resolution collector.
type ErrorReport struct {
	Service       string       `json:"service"`
	Component     string       `json:"component"`
	Level         string       `json:"level"`
	Message       string       `json:"message"`
	StackTrace    string       `json:"stack_trace,omitempty"`
	Metadata      interface{}  `json:"metadata,omitempty"`
	CorrelationID string       `json:"correlation_id,omitempty"`
	Traces        []TraceEvent `json:"traces,omitempty"`
	Version       string       `json:"version,omitempty"`
}

// ── Trace Buffer ───────────────────────────────────────────

// traceBuffer is a simple in-memory buffer for the current goroutine's traces.
// Thread-safe via mutex.
type traceBuffer struct {
	mu     sync.Mutex
	events []TraceEvent
}

var globalBuffer = &traceBuffer{}

func (b *traceBuffer) push(event TraceEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, event)
	if len(b.events) > maxTraceBuffer {
		excess := len(b.events) - maxTraceBuffer
		b.events = b.events[excess:]
	}
}

func (b *traceBuffer) drain() []TraceEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	events := b.events
	b.events = nil
	return events
}

// ── TraceFn ────────────────────────────────────────────────

// TraceFn wraps a function call with automatic tracing: captures duration,
// result, and errors. On error, it sends a report to the resolution collector.
//
// Returns the function's result and error, preserving the original behavior.
func TraceFn(ctx context.Context, name string, fn func() (interface{}, error)) (interface{}, error) {
	start := time.Now()
	cid := GetCorrelationID(ctx)
	tid := uuid.New().String()

	log.Printf("[trace →] %s (cid=%s)", name, cid[:8])

	result, err := fn()
	duration := time.Since(start).Seconds() * 1000 // milliseconds

	var errStr string
	if err != nil {
		errStr = err.Error()
		log.Printf("[trace ✗] %s (%.2fms) — %s", name, duration, errStr)
	} else {
		log.Printf("[trace ←] %s (%.2fms)", name, duration)
	}

	event := TraceEvent{
		Function:      name,
		DurationMs:    duration,
		Timestamp:     time.Now().UTC().Format(time.RFC3339),
		CorrelationID: cid,
		TraceID:       tid,
	}

	if errStr != "" {
		event.Error = errStr
	}

	globalBuffer.push(event)

	// On error, fire a report to the resolution collector
	if err != nil {
		report := &ErrorReport{
			Service:       serviceName,
			Component:     name,
			Level:         "error",
			Message:       errStr,
			CorrelationID: cid,
			Traces:        globalBuffer.drain(),
			Version:       version,
			Metadata: map[string]interface{}{
				"os":   runtime.GOOS,
				"arch": runtime.GOARCH,
			},
		}
		// Fire-and-forget: don't block the caller on error reporting
		go func() {
			if rErr := ReportError(report); rErr != nil {
				log.Printf("[tracing] failed to report error: %v", rErr)
			}
		}()
	}

	return result, err
}

// TraceFnSimple is a convenience wrapper for functions that return only error.
func TraceFnSimple(ctx context.Context, name string, fn func() error) error {
	_, err := TraceFn(ctx, name, func() (interface{}, error) {
		return nil, fn()
	})
	return err
}

// ── Error Reporting ────────────────────────────────────────

// ReportError sends an error report to the resolution collector.
func ReportError(report *ErrorReport) error {
	if report == nil {
		return fmt.Errorf("nil error report")
	}
	if report.Level == "" {
		report.Level = "error"
	}
	if report.Service == "" {
		report.Service = serviceName
	}

	payload, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("marshal report: %w", err)
	}

	client := &http.Client{
		Timeout: reportTimeout,
	}

	url := fmt.Sprintf("%s/api/v1/reports", collectorURL)
	resp, err := client.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("post report: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("collector returned %d", resp.StatusCode)
	}

	log.Printf("[tracing] error report sent to collector: %s", report.Message[:min(80, len(report.Message))])
	return nil
}

// ReportErrorSync sends an error report synchronously (blocks).
func ReportErrorSync(report *ErrorReport) error {
	return ReportError(report)
}

// ── Correlation ID Propagation ─────────────────────────────

// NewCorrelationID generates a new correlation ID and stores it in context.
func NewCorrelationID(ctx context.Context) (context.Context, string) {
	id := uuid.New().String()
	return context.WithValue(ctx, correlationIDKey, id), id
}

// GetCorrelationID extracts the correlation ID from context.
// Returns a new ID if none is set (should not happen in production).
func GetCorrelationID(ctx context.Context) string {
	if id, ok := ctx.Value(correlationIDKey).(string); ok && id != "" {
		return id
	}
	id := uuid.New().String()
	log.Printf("[tracing] WARNING: no correlation ID in context, generated new: %s", id[:8])
	return id
}

// WithTraceID adds a trace ID to the context for child calls.
func WithTraceID(ctx context.Context) (context.Context, string) {
	id := uuid.New().String()
	return context.WithValue(ctx, traceIDKey, id), id
}

// ── Helpers ────────────────────────────────────────────────

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// DrainTraces returns and clears all buffered trace events.
func DrainTraces() []TraceEvent {
	return globalBuffer.drain()
}

// SetServiceName allows overriding the service name at runtime.
func SetServiceName(name string) {
	serviceName = name
	version = envOrDefault("APP_VERSION", "0.2.0")
}
