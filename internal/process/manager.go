package process

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/webstudiobond/agh-unbound-lego/internal/util"
)

// Manager handles the lifecycle, monitoring, and structured logging of child processes.
type Manager struct {
	ctx        context.Context
	cmds       map[string]*exec.Cmd
	errChan    chan error
	order      []string // Tracks insertion order for reverse LIFO shutdown.
	mu         sync.Mutex
	wg         sync.WaitGroup
	isStopping bool
}

// NewManager creates a process table to track child lifecycles and route crash signals.
func NewManager(ctx context.Context) *Manager {
	return &Manager{
		ctx:     ctx,
		cmds:    make(map[string]*exec.Cmd),
		errChan: make(chan error, 2),
	}
}

// Start launches a process and redirects its output to the structured logging multiplexer.
func (m *Manager) Start(name, bin string, args ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.cmds[name]; exists {
		return fmt.Errorf("process %s is already running", name)
	}

	// #nosec G204 - Arguments are derived from validated, internally controlled configuration.
	cmd := exec.CommandContext(m.ctx, bin, args...)

	// Explicit ENV whitelist mirrors the isolation applied to the lego subprocess.
	// Prevents supervisor secrets from leaking into AGH or unbound if the parent
	// process ever receives them via environment rather than Docker secrets.
	cmd.Env = []string{}
	for _, key := range []string{"PATH", "HOME"} {
		if val := os.Getenv(key); val != "" {
			cmd.Env = append(cmd.Env, key+"="+val)
		}
	}

	// Create pipes to intercept child process output.
	// Directing these to os.Stdout would break JSON log parsing.
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe for %s: %w", name, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to create stderr pipe for %s: %w", name, err)
	}

	// Group assignment ensures signal isolation from the supervisor's TTY/Docker shell.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start %s: %w", name, err)
	}

	// Consume stdout/stderr in separate goroutines to prevent pipe buffer saturation (blocking).
	go pipeLogger(m.ctx, name, "stdout", stdout)
	go pipeLogger(m.ctx, name, "stderr", stderr)

	m.cmds[name] = cmd

	// Track order to support sequential LIFO shutdown logic.
	if !slices.Contains(m.order, name) {
		m.order = append(m.order, name)
	}

	m.wg.Add(1)
	slog.Info("Process started", "name", name, "pid", cmd.Process.Pid)

	go m.monitor(name, cmd)

	return nil
}

// monitor waits for process exit and distinguishes between intentional and accidental termination.
func (m *Manager) monitor(name string, cmd *exec.Cmd) {
	defer m.wg.Done()

	err := cmd.Wait()

	m.mu.Lock()
	stopping := m.isStopping
	delete(m.cmds, name)
	m.mu.Unlock()

	if stopping {
		slog.Debug("Process terminated intentionally", "name", name)
		return
	}

	if err != nil {
		slog.Error("Process crashed", "name", name, "error", err)
		m.errChan <- fmt.Errorf("process %s failed: %w", name, err)
		return
	}

	slog.Error("Process exited unexpectedly", "name", name)
	m.errChan <- fmt.Errorf("process %s exited unexpectedly", name)
}

// Restart performs a SIGTERM followed by a re-invocation of the process.
func (m *Manager) Restart(name, bin string, args ...string) error {
	m.mu.Lock()
	_, exists := m.cmds[name]
	m.mu.Unlock()

	if !exists {
		return m.Start(name, bin, args...)
	}

	slog.Info("Restarting process", "name", name)
	if err := m.stopOne(name, 5*time.Second); err != nil {
		return fmt.Errorf("failed to terminate process %s before restart: %w", name, err)
	}
	return m.Start(name, bin, args...)
}

// stopOne encapsulates the full SIGTERM -> wait -> SIGKILL escalation for a single process.
// The signal is sent while holding the mutex to prevent a PID reuse race between
// process exit detection in monitor() and signal delivery.
func (m *Manager) stopOne(name string, timeout time.Duration) error {
	m.mu.Lock()
	cmd, exists := m.cmds[name]
	if !exists {
		m.mu.Unlock()
		return nil
	}
	slog.Info("Stopping process", "name", name, "pid", cmd.Process.Pid)
	_ = cmd.Process.Signal(syscall.SIGTERM) //nolint:errcheck // Signal delivery is best-effort.
	m.mu.Unlock()

	// PollAfterDelayWithBackoff enforces the timeout via deadline, eliminating the fragile
	// integer-division attempt calculation that silently truncates non-round timeout values.
	if util.PollAfterDelayWithBackoff(timeout, 50*time.Millisecond, 500*time.Millisecond, func() bool {
		m.mu.Lock()
		_, exists := m.cmds[name]
		m.mu.Unlock()
		return !exists
	}) {
		return nil
	}

	slog.Warn("Process failed to stop gracefully, forcing SIGKILL", "name", name)
	m.mu.Lock()
	if cmd, exists := m.cmds[name]; exists {
		_ = cmd.Process.Kill() //nolint:errcheck // Kill delivery is best-effort.
	}
	m.mu.Unlock()

	// Allow one final scheduling quantum for the kernel to clean up after SIGKILL.
	if util.PollAfterDelayWithBackoff(1*time.Second, 100*time.Millisecond, 200*time.Millisecond, func() bool {
		m.mu.Lock()
		_, exists := m.cmds[name]
		m.mu.Unlock()
		return !exists
	}) {
		return nil
	}

	return fmt.Errorf("process %s refused to die after SIGKILL", name)
}

// StopAll executes a sequential LIFO shutdown to respect service dependencies.
func (m *Manager) StopAll(timeout time.Duration) {
	m.mu.Lock()
	m.isStopping = true
	shutdownOrder := make([]string, len(m.order))
	copy(shutdownOrder, m.order)
	m.mu.Unlock()

	// Shutdown in reverse order: AGH must stop before its upstream resolver (unbound).
	for i := len(shutdownOrder) - 1; i >= 0; i-- {
		name := shutdownOrder[i]
		if err := m.stopOne(name, timeout); err != nil {
			slog.Error("Failed to stop process cleanly", "name", name, "error", err)
		}
	}

	m.wg.Wait()
}

// Errors provides a channel for critical failure notifications.
func (m *Manager) Errors() <-chan error {
	return m.errChan
}
