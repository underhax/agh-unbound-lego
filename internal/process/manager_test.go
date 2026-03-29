package process

import (
	"testing"
	"time"
)

func TestManager_Lifecycle(t *testing.T) {
	// t.Context() is cancelled automatically when the test ends, preventing subprocess orphaning
	// if the test panics or is killed before StopAll completes.
	m := NewManager(t.Context())

	// 1. Start a background process
	err := m.Start("sleeper", "sleep", "10")
	if err != nil {
		t.Fatalf("Expected no error on start, got: %v", err)
	}

	// 2. Prevent duplicate starts
	err = m.Start("sleeper", "sleep", "10")
	if err == nil {
		t.Fatal("Expected error when starting an already tracked process")
	}

	// 3. Graceful shutdown
	startTime := time.Now()
	m.StopAll(2 * time.Second)

	if time.Since(startTime) >= 2*time.Second {
		t.Error("StopAll took too long, likely fell back to SIGKILL timeout instead of exiting immediately on SIGTERM")
	}

	// 4. Ensure no error was sent to the error channel during intentional shutdown
	select {
	case err := <-m.Errors():
		t.Fatalf("Received unexpected error from intentional shutdown: %v", err)
	default:
		// Success
	}
}
