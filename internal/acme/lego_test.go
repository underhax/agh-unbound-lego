package acme

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/underhax/adguardhome-unbound-lego/internal/config"
	"github.com/underhax/adguardhome-unbound-lego/internal/setup"
)

func TestBuildCmd_WildcardAndEnv(t *testing.T) {
	cfg := &config.Config{
		ACMEDomain: "dns.example.com",
		ACMEEmail:  config.Secret("admin@example.com"),
		CFDNSToken: config.Secret("secret-token"),
		LegoEnable: true,
	}
	m := NewManager(cfg, nil)

	cmd := m.buildCmd(context.Background(), "run")
	args := strings.Join(cmd.Args, " ")

	if !strings.Contains(args, "--domains dns.example.com") {
		t.Error("Primary domain missing in lego arguments")
	}

	if !strings.Contains(args, "--domains *.dns.example.com") {
		t.Error("Wildcard domain (*.dns.example.com) missing")
	}

	if !slices.Contains(cmd.Env, "CF_DNS_API_TOKEN=secret-token") {
		t.Error("CF_DNS_API_TOKEN missing in environment")
	}
}

func TestCertMetadata(t *testing.T) {
	origLego := setup.DirLego
	t.Cleanup(func() {
		setup.DirLego = origLego
	})

	tempDir := t.TempDir()
	setup.DirLego = tempDir

	cfg := &config.Config{ACMEDomain: "cert.example.net"}
	m := NewManager(cfg, nil)

	t.Run("NonExistentCert", func(t *testing.T) {
		if m.certExists() {
			t.Error("certExists returned true for non-existent cert")
		}
		if !m.getCertModTime().IsZero() {
			t.Error("getCertModTime returned non-zero for non-existent cert")
		}
	})

	t.Run("ExistingCert", func(t *testing.T) {
		certDir := filepath.Join(tempDir, "certificates")
		if err := os.MkdirAll(certDir, 0o700); err != nil {
			t.Fatalf("failed to create cert dir: %v", err)
		}
		certFile := filepath.Join(certDir, "cert.example.net.crt")
		if err := os.WriteFile(certFile, []byte("cert-data"), 0o600); err != nil {
			t.Fatalf("failed to write cert file: %v", err)
		}

		if !m.certExists() {
			t.Error("certExists returned false for existing cert")
		}
		if m.getCertModTime().IsZero() {
			t.Error("getCertModTime returned zero for existing cert")
		}
	})
}

func TestNeedsMigration(t *testing.T) {
	origLego := setup.DirLego
	t.Cleanup(func() {
		setup.DirLego = origLego
	})

	tempDir := t.TempDir()
	setup.DirLego = tempDir

	cfg := &config.Config{ACMEDomain: "mig.example.org"}
	m := NewManager(cfg, nil)

	t.Run("FreshInstallNoMigration", func(t *testing.T) {
		if m.needsMigration() {
			t.Error("needsMigration returned true for fresh install")
		}
	})

	t.Run("LegacyV4KeysExist", func(t *testing.T) {
		keysDir := filepath.Join(tempDir, "accounts", "acme-v02.api.letsencrypt.org", "user@example.com", "keys")
		if err := os.MkdirAll(keysDir, 0o700); err != nil {
			t.Fatalf("failed to create keys dir: %v", err)
		}

		if !m.needsMigration() {
			t.Error("needsMigration returned false when legacy keys exist")
		}
	})
}

func TestEnsureCert(t *testing.T) {
	origExec := executeAndLog
	origLego := setup.DirLego
	t.Cleanup(func() {
		executeAndLog = origExec
		setup.DirLego = origLego
	})

	tempDir := t.TempDir()
	setup.DirLego = tempDir

	cfg := &config.Config{
		ACMEDomain: "ensure.example.com",
		ACMEEmail:  config.Secret("admin@example.com"),
		CFDNSToken: config.Secret("secret"),
		LegoEnable: true,
	}

	t.Run("SuccessWithoutMigration", func(t *testing.T) {
		setup.DirLego = t.TempDir()
		executeAndLog = func(_ context.Context, _ *exec.Cmd, _ string) error {
			return nil
		}
		m := NewManager(cfg, nil)
		if err := m.EnsureCert(t.Context()); err != nil {
			t.Errorf("expected success, got %v", err)
		}
	})

	t.Run("SuccessWithMigrationTriggered", func(t *testing.T) {
		setup.DirLego = t.TempDir()
		origLegoBin := legoBin
		legoBin = "true"
		t.Cleanup(func() {
			legoBin = origLegoBin
		})

		keysDir := filepath.Join(setup.DirLego, "accounts", "reg", "acc", "keys")
		if err := os.MkdirAll(keysDir, 0o700); err != nil {
			t.Fatalf("failed to create keys dir: %v", err)
		}

		executeAndLog = func(_ context.Context, _ *exec.Cmd, _ string) error {
			return nil
		}
		m := NewManager(cfg, nil)
		if err := m.EnsureCert(t.Context()); err != nil {
			t.Errorf("expected success, got %v", err)
		}
	})

	t.Run("MigrationFailureHandled", func(t *testing.T) {
		setup.DirLego = t.TempDir()
		origLegoBin := legoBin
		legoBin = "false"
		t.Cleanup(func() {
			legoBin = origLegoBin
		})

		keysDir := filepath.Join(setup.DirLego, "accounts", "reg", "acc", "keys")
		if err := os.MkdirAll(keysDir, 0o700); err != nil {
			t.Fatalf("failed to create keys dir: %v", err)
		}

		executeAndLog = func(_ context.Context, _ *exec.Cmd, _ string) error {
			return nil
		}
		m := NewManager(cfg, nil)
		if err := m.EnsureCert(t.Context()); err != nil {
			t.Errorf("expected success despite migration error, got %v", err)
		}
	})

	t.Run("ExecutionError", func(t *testing.T) {
		setup.DirLego = t.TempDir()
		executeAndLog = func(_ context.Context, _ *exec.Cmd, _ string) error {
			return errors.New("lego error")
		}
		m := NewManager(cfg, nil)
		if err := m.EnsureCert(t.Context()); err == nil {
			t.Error("expected error when lego command fails")
		}
	})
}

func TestStartRenewTicker(t *testing.T) {
	origInterval := renewCheckInterval
	origExec := executeAndLog
	origLego := setup.DirLego
	t.Cleanup(func() {
		renewCheckInterval = origInterval
		executeAndLog = origExec
		setup.DirLego = origLego
	})

	tempDir := t.TempDir()
	setup.DirLego = tempDir
	certDir := filepath.Join(tempDir, "certificates")
	if err := os.MkdirAll(certDir, 0o700); err != nil {
		t.Fatalf("failed to create cert dir: %v", err)
	}
	certFile := filepath.Join(certDir, "renew.example.net.crt")
	if err := os.WriteFile(certFile, []byte("cert"), 0o600); err != nil {
		t.Fatalf("failed to write cert: %v", err)
	}

	renewCheckInterval = 10 * time.Millisecond

	var renewInvoked atomic.Bool
	onRenew := func() {
		renewInvoked.Store(true)
	}

	var callCount atomic.Int32
	executeAndLog = func(_ context.Context, _ *exec.Cmd, _ string) error {
		if callCount.Add(1) == 1 {
			return errors.New("transient renew error")
		}
		futureTime := time.Now().Add(10 * time.Minute)
		if err := os.Chtimes(certFile, futureTime, futureTime); err != nil {
			return err
		}
		return nil
	}

	cfg := &config.Config{
		ACMEDomain: "renew.example.net",
		ACMEEmail:  config.Secret("admin@example.com"),
		CFDNSToken: config.Secret("secret"),
		LegoEnable: true,
	}

	ctx, cancel := context.WithCancel(t.Context())
	m := NewManager(cfg, onRenew)
	done := m.StartRenewTicker(ctx)

	for range 20 {
		if renewInvoked.Load() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	<-done

	if !renewInvoked.Load() {
		t.Error("expected onRenew to be called upon certificate modification time change")
	}
}

func TestDefaultExecuteAndLog(t *testing.T) {
	t.Run("CommandExecutionFailure", func(t *testing.T) {
		cmd := exec.CommandContext(t.Context(), "nonexistent_binary_xyz")
		if err := defaultExecuteAndLog(t.Context(), cmd, "proc"); err == nil {
			t.Error("expected error for non-existent binary")
		}
	})

	t.Run("CommandExecutionSuccess", func(t *testing.T) {
		cmd := exec.CommandContext(t.Context(), "true")
		if err := defaultExecuteAndLog(t.Context(), cmd, "proc"); err != nil {
			t.Errorf("expected success with true command, got %v", err)
		}
	})

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
			cmd := exec.CommandContext(t.Context(), "echo")
			tt.setup(cmd)
			if err := defaultExecuteAndLog(t.Context(), cmd, "proc"); err == nil {
				t.Error("expected error when pipe is already assigned")
			}
		})
	}

	t.Run("CommandWaitFailure", func(t *testing.T) {
		cmd := exec.CommandContext(t.Context(), "false")
		if err := defaultExecuteAndLog(t.Context(), cmd, "proc"); err == nil {
			t.Error("expected error when command exits with non-zero code")
		}
	})
}

func TestSetExecuteAndLogForTest(t *testing.T) {
	called := false
	restore := SetExecuteAndLogForTest(func(_ context.Context, _ *exec.Cmd, _ string) error {
		called = true
		return nil
	})
	if err := executeAndLog(t.Context(), nil, "test"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("expected custom mock to be called")
	}
	restore()
}
