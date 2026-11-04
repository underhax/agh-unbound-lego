// Package main provides a process supervisor for AdGuard Home, Unbound, and Lego (ACME).
// It coordinates service startup, health checking, certificate management, and graceful shutdown.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/underhax/adguardhome-unbound-lego/internal/acme"
	"github.com/underhax/adguardhome-unbound-lego/internal/config"
	"github.com/underhax/adguardhome-unbound-lego/internal/health"
	"github.com/underhax/adguardhome-unbound-lego/internal/process"
	"github.com/underhax/adguardhome-unbound-lego/internal/setup"
	"github.com/underhax/adguardhome-unbound-lego/internal/util"
)

func defaultUnboundArgs() []string {
	return []string{"-d", "-c", setup.UnboundConfFile}
}

var (
	osExit                 = os.Exit
	osSetenv               = os.Setenv
	unboundBin             = "unbound"
	unboundArgsFunc        = defaultUnboundArgs
	aghBin                 = "/opt/adguardhome/AdGuardHome"
	secretsDir             = config.DefaultSecretsDir
	initInfrastructureFunc = initInfrastructure
	initLegoFunc           = initLego
	startServicesFunc      = startServices
	checkDNSFunc           = health.CheckDNS
	unboundWaitTimeout     = 10 * time.Second
)

func setLogger(level slog.Level) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	}))
	slog.SetDefault(logger)
}

func main() {
	osExit(run(os.Args))
}

// performHealthcheck bypasses the supervisor lifecycle to execute isolated diagnostics.
func performHealthcheck() int {
	checker := &health.Checker{
		UnboundPort: "127.0.0.1:5053",
		AGHConfPath: filepath.Join(setup.DirAGHConf, "AdGuardHome.yaml"),
	}
	if err := checker.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Healthcheck failed: %v\n", err)
		return 1
	}
	fmt.Println("Healthcheck passed")
	return 0
}

// run encapsulates the supervisor's lifecycle to guarantee deferred cleanups prior to exit.
func run(args []string) int {
	if len(args) > 1 && args[1] == "health" {
		return performHealthcheck()
	}

	code, err := runSupervisor()
	if err != nil {
		slog.Error("Supervisor failed", "error", err)
	}
	return code
}

// runSupervisor executes the core supervisor lifecycle.
func runSupervisor() (int, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	setLogger(slog.LevelInfo)

	cfg, err := config.Load(secretsDir)
	if err != nil {
		return 1, fmt.Errorf("configuration failure: %w", err)
	}

	setLogger(cfg.LogLevel)

	if cfg.DisableECN {
		if err := osSetenv("QUIC_GO_DISABLE_ECN", "true"); err != nil {
			return 1, fmt.Errorf("failed to disable QUIC ECN: %w", err)
		}
	}

	slog.Info("Supervisor starting", "domain", cfg.ACMEDomain, "log_level", cfg.LogLevel.String())

	if err := initInfrastructureFunc(); err != nil {
		return 1, err
	}

	pm := process.NewManager(ctx)

	aghArgs := []string{
		"-c", filepath.Join(setup.DirAGHConf, "AdGuardHome.yaml"),
		"-w", setup.DirAGHWork,
		"--no-check-update",
		"--no-permcheck",
	}

	if cfg.LegoEnable {
		if err := initLegoFunc(ctx, cfg, pm, aghArgs); err != nil {
			return 1, err
		}
	}

	if err := startServicesFunc(ctx, pm, aghArgs); err != nil {
		pm.StopAll(5 * time.Second)
		return 1, err
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		slog.Info("Received termination signal. Initiating graceful shutdown.", "signal", sig.String())
		pm.StopAll(10 * time.Second)
		slog.Info("Supervisor terminated.")
		if sig == syscall.SIGINT {
			return 130, nil
		}
		return 143, nil
	case err := <-pm.Errors():
		slog.Error("Critical process failure detected", "error", err)
		pm.StopAll(10 * time.Second)
		return 1, err
	}
}

// initInfrastructure ensures the container environment satisfies the strict execution assumptions of Unbound and AGH.
func initInfrastructure() error {
	if err := setup.Directories(); err != nil {
		return fmt.Errorf("failed to initialize directories: %w", err)
	}
	if err := setup.UnboundConfig(); err != nil {
		return fmt.Errorf("failed to initialize unbound configuration: %w", err)
	}
	if err := setup.TrustAnchor(); err != nil {
		slog.Error("Failed to initialize unbound trust anchor (non-fatal)", "error", err)
	}
	return nil
}

// newOnRenew creates a restart handler invoked when Lego successfully renews the certificate.
func newOnRenew(pm *process.Manager, aghArgs []string) func() {
	return func() {
		if err := pm.Restart("adguardhome", aghBin, aghArgs...); err != nil {
			slog.Error("Failed to restart AdGuard Home after certificate renewal", "error", err)
		}
	}
}

// initLego integrates ACME provisions into the supervisor lifecycle prior to network component activation.
func initLego(ctx context.Context, cfg *config.Config, pm *process.Manager, aghArgs []string) error {
	acmeManager := acme.NewManager(cfg, newOnRenew(pm, aghArgs))
	if err := acmeManager.EnsureCert(ctx); err != nil {
		return fmt.Errorf("failed to ensure TLS certificate: %w", err)
	}
	acmeManager.StartRenewTicker(ctx)
	return nil
}

// startServices orchestrates the specific startup dependency chain where AGH relies on Unbound's availability.
func startServices(ctx context.Context, pm *process.Manager, aghArgs []string) error {
	if err := pm.Start("unbound", unboundBin, unboundArgsFunc()...); err != nil {
		return fmt.Errorf("failed to start unbound: %w", err)
	}

	unboundReady := util.PollImmediateWithBackoff(unboundWaitTimeout, 10*time.Millisecond, 500*time.Millisecond, func() bool {
		return checkDNSFunc(ctx, "127.0.0.1:5053") == nil
	})

	if !unboundReady {
		return errors.New("unbound failed to become ready in time")
	}

	if err := pm.Start("adguardhome", aghBin, aghArgs...); err != nil {
		return fmt.Errorf("failed to start AdGuard Home: %w", err)
	}

	slog.Info("All critical processes running.")
	return nil
}
