package process

import (
	"context"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestManager_Lifecycle(t *testing.T) {
	m := NewManager(t.Context())

	err := m.Start("sleeper", "sleep", "10")
	if err != nil {
		t.Fatalf("Expected no error on start, got: %v", err)
	}

	err = m.Start("sleeper", "sleep", "10")
	if err == nil {
		t.Fatal("Expected error when starting an already tracked process")
	}

	startTime := time.Now()
	m.StopAll(2 * time.Second)

	if time.Since(startTime) >= 2*time.Second {
		t.Error("StopAll took too long")
	}

	select {
	case err := <-m.Errors():
		t.Fatalf("Received unexpected error from intentional shutdown: %v", err)
	default:
	}
}

func TestManager_StartFailure(t *testing.T) {
	m := NewManager(t.Context())

	err := m.Start("invalid", "nonexistent_proc_binary_xyz")
	if err == nil {
		t.Error("expected error when starting nonexistent binary")
	}
}

func TestManager_UnexpectedExit(t *testing.T) {
	m := NewManager(t.Context())

	if err := m.Start("quick", "true"); err != nil {
		t.Fatalf("failed to start quick: %v", err)
	}

	select {
	case err := <-m.Errors():
		if !strings.Contains(err.Error(), "exited unexpectedly") {
			t.Errorf("unexpected error message: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for exit notification")
	}
}

func TestManager_CrashExit(t *testing.T) {
	m := NewManager(t.Context())

	if err := m.Start("crasher", "false"); err != nil {
		t.Fatalf("failed to start crasher: %v", err)
	}

	select {
	case err := <-m.Errors():
		if !strings.Contains(err.Error(), "failed:") {
			t.Errorf("unexpected error message: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for crash notification")
	}
}

func TestManager_RestartUnstartedProcess(t *testing.T) {
	m := NewManager(t.Context())

	if err := m.Restart("unstarted", "sleep", "1"); err != nil {
		t.Fatalf("expected restart of unstarted process to succeed, got %v", err)
	}

	m.StopAll(1 * time.Second)
}

func TestManager_RestartSuppressesCrashError(t *testing.T) {
	m := NewManager(t.Context())

	err := m.Start("sleeper", "sleep", "10")
	if err != nil {
		t.Fatalf("Expected no error on start, got: %v", err)
	}

	err = m.Restart("sleeper", "sleep", "10")
	if err != nil {
		t.Fatalf("Expected graceful restart to succeed, got: %v", err)
	}

	timer := time.NewTimer(300 * time.Millisecond)
	defer timer.Stop()

	select {
	case err := <-m.Errors():
		t.Fatalf("recorded fatal crash during restart: %v", err)
	case <-timer.C:
	}

	m.StopAll(2 * time.Second)
}

func TestManager_ConcurrentRestartMultipleProcesses(t *testing.T) {
	m := NewManager(t.Context())

	if err := m.Start("proc1", "sleep", "10"); err != nil {
		t.Fatalf("Failed to start proc1: %v", err)
	}
	if err := m.Start("proc2", "sleep", "10"); err != nil {
		t.Fatalf("Failed to start proc2: %v", err)
	}

	errCh := make(chan error, 2)
	go func() {
		errCh <- m.Restart("proc1", "sleep", "10")
	}()
	go func() {
		errCh <- m.Restart("proc2", "sleep", "10")
	}()

	for range 2 {
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("Concurrent restart failed: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Concurrent restart timed out")
		}
	}

	timer := time.NewTimer(300 * time.Millisecond)
	defer timer.Stop()

	select {
	case err := <-m.Errors():
		t.Fatalf("recorded false crash detection: %v", err)
	case <-timer.C:
	}

	m.StopAll(2 * time.Second)
}

func TestManager_ProcessGroupIsolation(t *testing.T) {
	m := NewManager(t.Context())

	if err := m.Start("bash", "bash", "-c", "trap '' TERM; sleep 10 & wait"); err != nil {
		t.Fatalf("Failed to start bash: %v", err)
	}

	m.StopAll(2 * time.Second)
}

func TestManager_StopOne_EscalationToSigkill(t *testing.T) {
	m := NewManager(t.Context())

	if err := m.Start("stubborn", "sleep", "10"); err != nil {
		t.Fatalf("failed to start stubborn: %v", err)
	}

	if err := m.stopOne("stubborn", 1*time.Nanosecond); err != nil {
		t.Fatalf("stopOne failed: %v", err)
	}
}

func TestManager_StopOne_NonExistent(t *testing.T) {
	m := NewManager(t.Context())

	if err := m.stopOne("ghost", 1*time.Second); err != nil {
		t.Fatalf("expected nil for nonexistent process, got: %v", err)
	}
}

func TestManager_StartPipeErrors(t *testing.T) {
	pipeTests := []struct {
		setup func(cmd *exec.Cmd)
		name  string
	}{
		{
			setup: func(cmd *exec.Cmd) {
				cmd.Stdout = io.Discard
			},
			name: "StdoutPipeError",
		},
		{
			setup: func(cmd *exec.Cmd) {
				cmd.Stderr = io.Discard
			},
			name: "StderrPipeError",
		},
	}
	for _, tt := range pipeTests {
		t.Run(tt.name, func(t *testing.T) {
			origExec := execCommand
			execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
				cmd := defaultCommandContext(ctx, name, args...)
				tt.setup(cmd)
				return cmd
			}
			t.Cleanup(func() { execCommand = origExec })

			m := NewManager(t.Context())
			if err := m.Start("pipetest", "echo"); err == nil {
				t.Error("expected pipe error on start")
			}
		})
	}
}

func TestManager_StopOne_SignalsAndRefusal(t *testing.T) {
	origTimeout := killTimeout
	killTimeout = 1 * time.Millisecond
	t.Cleanup(func() { killTimeout = origTimeout })

	m := NewManager(t.Context())
	cmd := exec.CommandContext(t.Context(), "true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to run helper command: %v", err)
	}

	m.mu.Lock()
	m.cmds["undying"] = cmd
	m.mu.Unlock()

	err := m.stopOne("undying", 1*time.Millisecond)
	if err == nil {
		t.Fatal("expected error when process is not removed from cmds map")
	}
	if !strings.Contains(err.Error(), "refused to die after SIGKILL") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestManager_Restart_StopError(t *testing.T) {
	origRestartTimeout := restartStopTimeout
	restartStopTimeout = 1 * time.Millisecond
	t.Cleanup(func() { restartStopTimeout = origRestartTimeout })

	origTimeout := killTimeout
	killTimeout = 1 * time.Millisecond
	t.Cleanup(func() { killTimeout = origTimeout })

	m := NewManager(t.Context())
	cmd := exec.CommandContext(t.Context(), "true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to run helper command: %v", err)
	}

	m.mu.Lock()
	m.cmds["undying"] = cmd
	m.mu.Unlock()

	err := m.Restart("undying", "true")
	if err == nil {
		t.Fatal("expected restart error when stopOne fails")
	}
	if !strings.Contains(err.Error(), "failed to terminate process") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestManager_StopAll_ErrorsAndTimeout(t *testing.T) {
	t.Run("StopOneErrorInStopAll", func(t *testing.T) {
		origTimeout := killTimeout
		killTimeout = 1 * time.Millisecond
		t.Cleanup(func() { killTimeout = origTimeout })

		m := NewManager(t.Context())
		cmd := exec.CommandContext(t.Context(), "true")
		if err := cmd.Run(); err != nil {
			t.Fatalf("failed to run helper command: %v", err)
		}

		m.mu.Lock()
		m.cmds["undying"] = cmd
		m.order = append(m.order, "undying")
		m.mu.Unlock()

		m.StopAll(1 * time.Millisecond)
	})

	t.Run("StopAllTimeoutBranch", func(t *testing.T) {
		m := NewManager(t.Context())
		m.wg.Add(1)
		defer m.wg.Done()
		m.StopAll(1 * time.Millisecond)
	})
}
