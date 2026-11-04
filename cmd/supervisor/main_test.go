package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/underhax/adguardhome-unbound-lego/internal/acme"
	"github.com/underhax/adguardhome-unbound-lego/internal/config"
	"github.com/underhax/adguardhome-unbound-lego/internal/health"
	"github.com/underhax/adguardhome-unbound-lego/internal/process"
	"github.com/underhax/adguardhome-unbound-lego/internal/setup"
)

type mockDNSConn struct {
	response []byte
}

func (m *mockDNSConn) Read(b []byte) (int, error) {
	return copy(b, m.response), nil
}

func (m *mockDNSConn) Write(b []byte) (int, error) {
	if len(b) >= 2 && len(m.response) >= 2 {
		m.response[0] = b[0]
		m.response[1] = b[1]
	}
	return len(b), nil
}

func (m *mockDNSConn) Close() error                       { return nil }
func (m *mockDNSConn) LocalAddr() net.Addr                { return &net.IPAddr{} }
func (m *mockDNSConn) RemoteAddr() net.Addr               { return &net.IPAddr{} }
func (m *mockDNSConn) SetDeadline(_ time.Time) error      { return nil }
func (m *mockDNSConn) SetReadDeadline(_ time.Time) error  { return nil }
func (m *mockDNSConn) SetWriteDeadline(_ time.Time) error { return nil }

func validDNSHeader() []byte {
	return []byte{0x00, 0x00, 0x80, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00}
}

func TestMainExecution(t *testing.T) {
	origExit := osExit
	origArgs := os.Args
	t.Cleanup(func() {
		osExit = origExit
		os.Args = origArgs
	})

	var exitCode int
	osExit = func(code int) {
		exitCode = code
	}

	os.Args = []string{origArgs[0], "health"}
	main()

	if exitCode != 0 && exitCode != 1 {
		t.Errorf("unexpected exit code: %d", exitCode)
	}
}

func TestSetLogger(_ *testing.T) {
	levels := []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError}
	for _, lvl := range levels {
		setLogger(lvl)
	}
}

func TestPerformHealthcheck(t *testing.T) {
	origConf := setup.DirAGHConf
	t.Cleanup(func() {
		setup.DirAGHConf = origConf
	})

	tempDir := t.TempDir()
	setup.DirAGHConf = tempDir

	t.Run("FailureWhenConfigMissing", func(t *testing.T) {
		code := performHealthcheck()
		if code != 1 {
			t.Errorf("expected code 1 on missing config/unreachable unbound, got %d", code)
		}
	})

	t.Run("SuccessWhenEndpointsHealthy", func(t *testing.T) {
		confPath := filepath.Join(tempDir, "AdGuardHome.yaml")
		if err := os.WriteFile(confPath, []byte("http:\n  address: 0.0.0.0:80\n"), 0o600); err != nil {
			t.Fatalf("failed to write conf: %v", err)
		}

		restore := health.SetHooksForTest(
			func(_ context.Context, _ string) (net.Conn, error) {
				return &mockDNSConn{response: validDNSHeader()}, nil
			},
			func(_ context.Context, _ int) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("OK")),
				}, nil
			},
		)
		defer restore()

		code := performHealthcheck()
		if code != 0 {
			t.Errorf("expected code 0 on healthy endpoints, got %d", code)
		}
	})
}

func TestRun_Dispatch(t *testing.T) {
	appCmd := os.Args[0]

	t.Run("HealthArg", func(t *testing.T) {
		code := run([]string{appCmd, "health"})
		if code != 0 && code != 1 {
			t.Errorf("unexpected code from health run: %d", code)
		}
	})

	t.Run("SupervisorConfigFailure", func(t *testing.T) {
		t.Setenv("LEGO_ENABLE", "true")
		code := run([]string{appCmd})
		if code != 1 {
			t.Errorf("expected exit code 1 on invalid config, got %d", code)
		}
	})
}

func setupSupervisorEnv(t *testing.T) {
	t.Helper()
	t.Setenv("LEGO_ENABLE", "false")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("QUIC_GO_DISABLE_ECN", "true")
}

func TestRunSupervisor_Failures(t *testing.T) {
	origInitInfra := initInfrastructureFunc
	origInitLego := initLegoFunc
	origStartServices := startServicesFunc
	origSetenv := osSetenv
	t.Cleanup(func() {
		initInfrastructureFunc = origInitInfra
		initLegoFunc = origInitLego
		startServicesFunc = origStartServices
		osSetenv = origSetenv
	})

	t.Run("DisableECNFailure", func(t *testing.T) {
		setupSupervisorEnv(t)
		origSetenv := osSetenv
		t.Cleanup(func() { osSetenv = origSetenv })
		osSetenv = func(_, _ string) error {
			return errors.New("mock setenv error")
		}

		code, err := runSupervisor()
		if code != 1 || err == nil {
			t.Errorf("expected failure code 1 on setenv error, got code %d, err %v", code, err)
		}
	})

	t.Run("InfrastructureFailure", func(t *testing.T) {
		setupSupervisorEnv(t)
		initInfrastructureFunc = func() error {
			return errors.New("infrastructure error")
		}

		code, err := runSupervisor()
		if code != 1 || err == nil {
			t.Errorf("expected failure code 1, got code %d, err %v", code, err)
		}
	})

	t.Run("LegoFailure", func(t *testing.T) {
		setupSupervisorEnv(t)
		t.Setenv("LEGO_ENABLE", "true")
		tempSecrets := t.TempDir()
		if err := os.WriteFile(filepath.Join(tempSecrets, "acme_domain"), []byte("dns.example.com"), 0o600); err != nil {
			t.Fatalf("failed to write secret: %v", err)
		}
		if err := os.WriteFile(filepath.Join(tempSecrets, "acme_email"), []byte("admin@example.com"), 0o600); err != nil {
			t.Fatalf("failed to write secret: %v", err)
		}
		if err := os.WriteFile(filepath.Join(tempSecrets, "cf_dns_api_token"), []byte("token"), 0o600); err != nil {
			t.Fatalf("failed to write secret: %v", err)
		}

		initInfrastructureFunc = func() error { return nil }
		initLegoFunc = func(_ context.Context, _ *config.Config, _ *process.Manager, _ []string) error {
			return errors.New("lego error")
		}

		origSecrets := secretsDir
		secretsDir = tempSecrets
		t.Cleanup(func() { secretsDir = origSecrets })

		code, err := runSupervisor()
		if code != 1 || err == nil {
			t.Errorf("expected failure code 1 on lego error, got code %d, err %v", code, err)
		}
	})

	t.Run("StartServicesFailure", func(t *testing.T) {
		setupSupervisorEnv(t)
		initInfrastructureFunc = func() error { return nil }
		startServicesFunc = func(_ context.Context, _ *process.Manager, _ []string) error {
			return errors.New("services start error")
		}

		code, err := runSupervisor()
		if code != 1 || err == nil {
			t.Errorf("expected failure code 1 on start services error, got code %d, err %v", code, err)
		}
	})
}

func TestRunSupervisor_Termination(t *testing.T) {
	origInitInfra := initInfrastructureFunc
	origStartServices := startServicesFunc
	t.Cleanup(func() {
		initInfrastructureFunc = origInitInfra
		startServicesFunc = origStartServices
	})

	t.Run("TerminationSignal_SIGINT", func(t *testing.T) {
		setupSupervisorEnv(t)
		initInfrastructureFunc = func() error { return nil }
		startServicesFunc = func(_ context.Context, _ *process.Manager, _ []string) error {
			go func() {
				time.Sleep(50 * time.Millisecond)
				if err := syscall.Kill(syscall.Getpid(), syscall.SIGINT); err != nil {
					t.Logf("signal send failed: %v", err)
				}
			}()
			return nil
		}

		code, err := runSupervisor()
		if code != 130 || err != nil {
			t.Errorf("expected code 130 on SIGINT, got %d, err %v", code, err)
		}
	})

	t.Run("TerminationSignal_SIGTERM", func(t *testing.T) {
		setupSupervisorEnv(t)
		initInfrastructureFunc = func() error { return nil }
		startServicesFunc = func(_ context.Context, _ *process.Manager, _ []string) error {
			go func() {
				time.Sleep(50 * time.Millisecond)
				if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
					t.Logf("signal send failed: %v", err)
				}
			}()
			return nil
		}

		code, err := runSupervisor()
		if code != 143 || err != nil {
			t.Errorf("expected code 143 on SIGTERM, got %d, err %v", code, err)
		}
	})

	t.Run("ProcessCrashFailure", func(t *testing.T) {
		setupSupervisorEnv(t)
		initInfrastructureFunc = func() error { return nil }
		startServicesFunc = func(_ context.Context, pm *process.Manager, _ []string) error {
			return pm.Start("short_lived", "false")
		}

		code, err := runSupervisor()
		if code != 1 || err == nil {
			t.Errorf("expected code 1 on process crash, got %d, err %v", code, err)
		}
	})
}

func TestInitInfrastructure(t *testing.T) {
	origWork := setup.DirAGHWork
	origConf := setup.DirAGHConf
	origUnbound := setup.DirUnbound
	origLego := setup.DirLego
	origConfFile := setup.UnboundConfFile
	origDefaultConf := setup.UnboundDefaultConf

	t.Cleanup(func() {
		setup.DirAGHWork = origWork
		setup.DirAGHConf = origConf
		setup.DirUnbound = origUnbound
		setup.DirLego = origLego
		setup.UnboundConfFile = origConfFile
		setup.UnboundDefaultConf = origDefaultConf
	})

	tempDir := t.TempDir()
	setup.DirAGHWork = filepath.Join(tempDir, "work")
	setup.DirAGHConf = filepath.Join(tempDir, "conf")
	setup.DirUnbound = filepath.Join(tempDir, "unbound")
	setup.DirLego = filepath.Join(tempDir, "lego")
	setup.UnboundConfFile = filepath.Join(setup.DirUnbound, "unbound.conf")
	setup.UnboundDefaultConf = filepath.Join(tempDir, "unbound.conf.default")

	defaultConfData := "server:\n  port: 5053\n  interface: 127.0.0.1\n"
	if err := os.WriteFile(setup.UnboundDefaultConf, []byte(defaultConfData), 0o600); err != nil {
		t.Fatalf("failed to write default conf: %v", err)
	}

	if err := os.MkdirAll(setup.DirUnbound, 0o700); err != nil {
		t.Fatalf("failed to create unbound dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(setup.DirUnbound, "root.key"), []byte("test key"), 0o600); err != nil {
		t.Fatalf("failed to write root key: %v", err)
	}

	t.Run("Success", func(t *testing.T) {
		if err := initInfrastructure(); err != nil {
			t.Errorf("expected success, got %v", err)
		}
	})

	t.Run("TrustAnchorErrorHandled", func(t *testing.T) {
		setup.DirAGHWork = filepath.Join(tempDir, "work_anchor")
		setup.DirUnbound = filepath.Join(tempDir, "unbound_no_anchor")
		if err := os.MkdirAll(setup.DirUnbound, 0o700); err != nil {
			t.Fatalf("failed to create unbound dir: %v", err)
		}
		setup.UnboundConfFile = filepath.Join(setup.DirUnbound, "unbound.conf")
		if err := os.WriteFile(setup.UnboundConfFile, []byte("server:\n"), 0o600); err != nil {
			t.Fatalf("failed to write conf: %v", err)
		}

		if err := initInfrastructure(); err != nil {
			t.Errorf("expected nil (trust anchor error is non-fatal), got %v", err)
		}
	})

	t.Run("DirectoriesError", func(t *testing.T) {
		filePath := filepath.Join(tempDir, "blocked_dir")
		if err := os.WriteFile(filePath, []byte("blocking"), 0o600); err != nil {
			t.Fatalf("failed to write block file: %v", err)
		}
		setup.DirAGHWork = filepath.Join(filePath, "sub")

		if err := initInfrastructure(); err == nil {
			t.Error("expected error when directory creation is blocked, got nil")
		}
	})

	t.Run("UnboundConfigError", func(t *testing.T) {
		setup.DirAGHWork = filepath.Join(tempDir, "work_retry")
		setup.UnboundDefaultConf = filepath.Join(tempDir, "missing_default.conf")
		if err := os.Remove(setup.UnboundConfFile); err != nil && !os.IsNotExist(err) {
			t.Logf("cleanup error: %v", err)
		}

		if err := initInfrastructure(); err == nil {
			t.Error("expected error when unbound config missing, got nil")
		}
	})
}

func TestInitLego(t *testing.T) {
	cfg := &config.Config{
		ACMEDomain: "dns.example.com",
		ACMEEmail:  config.Secret("admin@example.com"),
		CFDNSToken: config.Secret("secret"),
		LegoEnable: true,
	}

	pm := process.NewManager(t.Context())

	t.Run("EnsureCertFailure", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()

		err := initLego(ctx, cfg, pm, []string{"--test-fail"})
		if err == nil {
			t.Error("expected EnsureCert to fail in test environment, got nil")
		}
	})

	t.Run("EnsureCertSuccess", func(t *testing.T) {
		restore := acme.SetExecuteAndLogForTest(func(_ context.Context, _ *exec.Cmd, _ string) error {
			return nil
		})
		defer restore()

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		if err := initLego(ctx, cfg, pm, []string{"--test-ok"}); err != nil {
			t.Errorf("expected success from initLego, got %v", err)
		}
	})
}

func TestNewOnRenew(t *testing.T) {
	pm := process.NewManager(t.Context())
	onRenew := newOnRenew(pm, []string{"--test-renew"})

	t.Run("ProcessNotRunningErrorsGracefully", func(_ *testing.T) {
		onRenew()
	})

	t.Run("ProcessRunningRestartsSuccessfully", func(t *testing.T) {
		origAGH := aghBin
		aghBin = "/bin/sleep"
		t.Cleanup(func() {
			aghBin = origAGH
		})

		if err := pm.Start("adguardhome", "/bin/sleep", "2"); err != nil {
			t.Fatalf("failed to start mock agh: %v", err)
		}
		onRenew()
		pm.StopAll(500 * time.Millisecond)
	})
}

func TestDefaultUnboundArgs(t *testing.T) {
	args := defaultUnboundArgs()
	if len(args) != 3 || args[0] != "-d" || args[1] != "-c" {
		t.Errorf("unexpected default args: %v", args)
	}
}

func TestStartServices(t *testing.T) {
	origUnbound := unboundBin
	origArgs := unboundArgsFunc
	origAGH := aghBin
	origCheckDNS := checkDNSFunc
	origConf := setup.UnboundConfFile
	origTimeout := unboundWaitTimeout

	t.Cleanup(func() {
		unboundBin = origUnbound
		unboundArgsFunc = origArgs
		aghBin = origAGH
		checkDNSFunc = origCheckDNS
		setup.UnboundConfFile = origConf
		unboundWaitTimeout = origTimeout
	})

	unboundWaitTimeout = 30 * time.Millisecond
	unboundArgsFunc = func() []string {
		return []string{"2"}
	}

	tempDir := t.TempDir()
	setup.UnboundConfFile = filepath.Join(tempDir, "unbound.conf")
	if err := os.WriteFile(setup.UnboundConfFile, []byte("server:\n"), 0o600); err != nil {
		t.Fatalf("failed to write dummy conf: %v", err)
	}

	tests := []struct {
		checkDNS   func(context.Context, string) error
		name       string
		unboundCmd string
		aghCmd     string
		aghArgs    []string
		wantErr    bool
	}{
		{
			name:       "UnboundStartError",
			unboundCmd: "nonexistent_unbound_command",
			wantErr:    true,
		},
		{
			name: "UnboundNotReadyTimeout",
			checkDNS: func(_ context.Context, _ string) error {
				return errors.New("dns check down")
			},
			wantErr: true,
		},
		{
			name:   "AGHStartError",
			aghCmd: "nonexistent_agh_command",
			checkDNS: func(_ context.Context, _ string) error {
				return nil
			},
			wantErr: true,
		},
		{
			name:    "Success",
			aghArgs: []string{"2"},
			checkDNS: func(_ context.Context, _ string) error {
				return nil
			},
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pm := process.NewManager(t.Context())
			unboundBin = "sleep"
			if tc.unboundCmd != "" {
				unboundBin = tc.unboundCmd
			}
			aghBin = "sleep"
			if tc.aghCmd != "" {
				aghBin = tc.aghCmd
			}
			if tc.checkDNS != nil {
				checkDNSFunc = tc.checkDNS
			}

			err := startServices(t.Context(), pm, tc.aghArgs)
			pm.StopAll(500 * time.Millisecond)

			if (err != nil) != tc.wantErr {
				t.Errorf("startServices() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
