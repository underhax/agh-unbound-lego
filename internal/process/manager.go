package process

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"slices"
	"sync"
	"syscall"
	"time"
)

// Manager handles the lifecycle, monitoring, and structured logging of child processes.
type Manager struct {
	cmds       map[string]*exec.Cmd
	errChan    chan error
	order      []string // Tracks insertion order for reverse LIFO shutdown.
	mu         sync.Mutex
	wg         sync.WaitGroup
	isStopping bool
}

// NewManager initializes process tracking and telemetry channels.
func NewManager() *Manager {
	return &Manager{
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
	cmd := exec.CommandContext(context.Background(), bin, args...)

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
	go pipeLogger(name, "stdout", stdout)
	go pipeLogger(name, "stderr", stderr)

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
	cmd, exists := m.cmds[name]
	m.mu.Unlock()

	if !exists {
		return m.Start(name, bin, args...)
	}

	slog.Info("Restarting process", "name", name)
	_ = cmd.Process.Signal(syscall.SIGTERM) //nolint:errcheck // Signal delivery is best-effort.

	if m.waitForExit(name, 50, 100*time.Millisecond) {
		return m.Start(name, bin, args...)
	}

	slog.Warn("Process failed to stop gracefully during restart, forcing SIGKILL", "name", name)
	_ = cmd.Process.Kill() //nolint:errcheck // Kill delivery is best-effort.

	if m.waitForExit(name, 50, 100*time.Millisecond) {
		return m.Start(name, bin, args...)
	}

	return fmt.Errorf("failed to terminate process %s before restart", name)
}

// waitForExit polls the process map to confirm termination.
func (m *Manager) waitForExit(name string, attempts int, delay time.Duration) bool {
	for range attempts {
		time.Sleep(delay)
		m.mu.Lock()
		_, exists := m.cmds[name]
		m.mu.Unlock()
		if !exists {
			return true
		}
	}
	return false
}

// StopAll executes a sequential LIFO shutdown to respect service dependencies.
func (m *Manager) StopAll(timeout time.Duration) {
	m.mu.Lock()
	m.isStopping = true
	shutdownOrder := make([]string, len(m.order))
	copy(shutdownOrder, m.order)
	m.mu.Unlock()

	// Shutdown in reverse order: AGH should stop before its upstream (unbound).
	for i := len(shutdownOrder) - 1; i >= 0; i-- {
		deadline := time.Now().Add(timeout)
		name := shutdownOrder[i]

		m.mu.Lock()
		cmd, exists := m.cmds[name]
		m.mu.Unlock()

		if !exists {
			continue
		}

		slog.Info("Stopping process", "name", name, "pid", cmd.Process.Pid)
		_ = cmd.Process.Signal(syscall.SIGTERM) //nolint:errcheck // Signal delivery is best-effort.

		for time.Now().Before(deadline) {
			m.mu.Lock()
			_, stillRunning := m.cmds[name]
			m.mu.Unlock()

			if !stillRunning {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	// Escalate to SIGKILL for any process that failed to exit within the deadline.
	m.mu.Lock()
	for name, cmd := range m.cmds {
		slog.Warn("Forcing SIGKILL", "name", name)
		_ = cmd.Process.Kill() //nolint:errcheck // Kill delivery is best-effort.
	}
	m.mu.Unlock()

	m.wg.Wait()
}

// Errors provides a channel for critical failure notifications.
func (m *Manager) Errors() <-chan error {
	return m.errChan
}
