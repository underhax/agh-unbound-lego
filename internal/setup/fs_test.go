package setup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyFile(t *testing.T) {
	tempDir := t.TempDir()
	src := filepath.Join(tempDir, "source.conf")
	dst := filepath.Join(tempDir, "dest.conf")

	err := os.WriteFile(src, []byte("server: verbosity: 1"), 0o600)
	if err != nil {
		t.Fatalf("Failed to setup source file: %v", err)
	}

	err = copyFile(src, dst, 0o640)
	if err != nil {
		t.Fatalf("Expected successful copy, got: %v", err)
	}

	// #nosec G304 -- Paths are generated internally by t.TempDir().
	data, err := os.ReadFile(dst)
	if err != nil || string(data) != "server: verbosity: 1" {
		t.Errorf("Copied data mismatch or read error")
	}
}
