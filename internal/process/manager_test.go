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
