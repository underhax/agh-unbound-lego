package process

import (
	"testing"
	"time"
)

func TestManager_Lifecycle(t *testing.T) {
	t.Parallel()

	// t.Context() is cancelled automatically when the test ends, preventing subprocess orphaning
	// if the test panics or is killed before StopAll completes.
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
		t.Error("StopAll took too long, likely fell back to SIGKILL timeout instead of exiting immediately on SIGTERM")
	}

	select {
	case err := <-m.Errors():
		t.Fatalf("Received unexpected error from intentional shutdown: %v", err)
	default:
		// Success
	}
}

func TestManager_RestartSuppressesCrashError(t *testing.T) {
	t.Parallel()

	m := NewManager(t.Context())

	err := m.Start("sleeper", "sleep", "10")
	if err != nil {
		t.Fatalf("Expected no error on start, got: %v", err)
	}

	// Validates that intentional SIGTERM via Restart bypasses crash telemetry.
	err = m.Restart("sleeper", "sleep", "10")
	if err != nil {
		t.Fatalf("Expected graceful restart to succeed, got: %v", err)
	}

	// Delay accommodates concurrent monitor processing of the dispatched SIGTERM.
	timer := time.NewTimer(500 * time.Millisecond)
	defer timer.Stop()

	select {
	case err := <-m.Errors():
		t.Fatalf("BUG REGRESSION: Process Manager recorded a fatal crash during a graceful restart: %v", err)
	case <-timer.C:
	}

	m.StopAll(2 * time.Second)
}

func TestManager_ConcurrentRestartMultipleProcesses(t *testing.T) {
	t.Parallel()

	m := NewManager(t.Context())

	if err := m.Start("proc1", "sleep", "10"); err != nil {
		t.Fatalf("Failed to start proc1: %v", err)
	}
	if err := m.Start("proc2", "sleep", "10"); err != nil {
		t.Fatalf("Failed to start proc2: %v", err)
	}

	// Concurrent restart validates fix_1 works under parallel conditions.
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

	// Verify false crash detection is suppressed during parallel restart.
	timer := time.NewTimer(500 * time.Millisecond)
	defer timer.Stop()

	select {
	case err := <-m.Errors():
		t.Fatalf("BUG REGRESSION: Concurrent restart caused false crash detection: %v", err)
	case <-timer.C:
	}

	m.StopAll(2 * time.Second)
}

func TestManager_ProcessGroupIsolation(t *testing.T) {
	t.Parallel()

	m := NewManager(t.Context())

	// Process group isolation ensures SIGTERM propagates to children.
	if err := m.Start("bash", "bash", "-c", "trap '' TERM; sleep 10 & wait"); err != nil {
		t.Fatalf("Failed to start bash: %v", err)
	}

	m.StopAll(2 * time.Second)
}

func TestManager_StopAllTimeoutBehavior(t *testing.T) {
	t.Parallel()

	// Short timeout exposes unbounded wait regression (fix_4).
	m := NewManager(t.Context())

	if err := m.Start("ignoresig", "bash", "-c", "trap '' TERM; sleep 30"); err != nil {
		t.Fatalf("Failed to start ignoresig: %v", err)
	}

	startTime := time.Now()
	m.StopAll(500 * time.Millisecond)

	elapsed := time.Since(startTime)
	// Validates bounded wait prevents indefinite blocking.
	if elapsed >= 3*time.Second {
		t.Error("StopAll appeared to block indefinitely instead of timing out")
	}

	// SIGKILL fallback may report exit error — this is expected behavior.
	select {
	case err := <-m.Errors():
		t.Logf("Process exited with: %v", err)
	default:
	}
}
