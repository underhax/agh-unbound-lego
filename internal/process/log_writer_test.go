package process

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestPipeLogger_StructuredOutput(t *testing.T) {
	oldLogger := slog.Default()
	defer slog.SetDefault(oldLogger)
	var buf bytes.Buffer
	handler := slog.NewJSONHandler(&buf, nil)
	logger := slog.New(handler)
	slog.SetDefault(logger)

	pr, pw := io.Pipe()

	done := make(chan struct{})
	go func() {
		pipeLogger(context.Background(), "test-proc", "stdout", pr)
		close(done)
	}()

	testLine := "service started successfully"
	if _, err := pw.Write([]byte(testLine + "\n")); err != nil {
		t.Fatalf("Failed to write to pipe: %v", err)
	}
	if err := pw.Close(); err != nil {
		t.Fatalf("Failed to close pipe writer: %v", err)
	}
	<-done // Ensure pipeLogger has finished all slog calls

	var logEntry map[string]any

	if err := json.Unmarshal(buf.Bytes(), &logEntry); err != nil {
		t.Fatalf("Failed to parse log as JSON: %v", err)
	}

	if logEntry["msg"] != testLine {
		t.Errorf("Expected message %q, got %q", testLine, logEntry["msg"])
	}
	if logEntry["process"] != "test-proc" {
		t.Errorf("Expected process attribute 'test-proc', got %q", logEntry["process"])
	}
	if logEntry["stream"] != "stdout" {
		t.Errorf("Expected stream attribute 'stdout', got %q", logEntry["stream"])
	}
}

func TestPipeLogger_StderrLevels(t *testing.T) {
	oldLogger := slog.Default()
	defer slog.SetDefault(oldLogger)
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))

	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		pipeLogger(context.Background(), "test-proc", "stderr", pr)
		close(done)
	}()

	if _, err := pw.Write([]byte("some raw error\n")); err != nil {
		t.Fatalf("Failed to write raw error to pipe: %v", err)
	}
	if _, err := pw.Write([]byte("2026/03/28 [info] starting\n")); err != nil {
		t.Fatalf("Failed to write info log to pipe: %v", err)
	}
	if _, err := pw.Write([]byte("2026/03/28 [error] failed\n")); err != nil {
		t.Fatalf("Failed to write error log to pipe: %v", err)
	}
	if err := pw.Close(); err != nil {
		t.Fatalf("Failed to close pipe writer: %v", err)
	}
	<-done

	output := buf.String()
	if !strings.Contains(output, `"level":"WARN"`) || !strings.Contains(output, "some raw error") {
		t.Errorf("Raw stderr should default to WARN")
	}
	if !strings.Contains(output, `"level":"INFO"`) || !strings.Contains(output, "[info] starting") {
		t.Errorf("Stderr with [info] should map to INFO")
	}
	if !strings.Contains(output, `"level":"ERROR"`) || !strings.Contains(output, "[error] failed") {
		t.Errorf("Stderr with [error] should map to ERROR")
	}
}
