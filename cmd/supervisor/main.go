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

	"github.com/webstudiobond/agh-unbound-lego/internal/acme"
	"github.com/webstudiobond/agh-unbound-lego/internal/config"
	"github.com/webstudiobond/agh-unbound-lego/internal/health"
	"github.com/webstudiobond/agh-unbound-lego/internal/process"
	"github.com/webstudiobond/agh-unbound-lego/internal/setup"
	"github.com/webstudiobond/agh-unbound-lego/internal/util"
)

func setLogger(level slog.Level) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	}))
	slog.SetDefault(logger)
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "health" {
		performHealthcheck()
		return
	}

	code, err := run()
	if err != nil {
		slog.Error("Supervisor failed", "error", err)
	}
	os.Exit(code)
}

// performHealthcheck bypasses the supervisor lifecycle to execute isolated diagnostics.
func performHealthcheck() {
	checker := &health.Checker{
		UnboundPort: "127.0.0.1:5053",
		AGHConfPath: filepath.Join(setup.DirAGHConf, "AdGuardHome.yaml"),
	}
	if err := checker.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Healthcheck failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Healthcheck passed")
}

// run encapsulates the supervisor's lifecycle to guarantee deferred cleanups prior to os.Exit.
func run() (int, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	setLogger(slog.LevelInfo)

	cfg, err := config.Load(config.DefaultSecretsDir)
	if err != nil {
		return 1, fmt.Errorf("configuration failure: %w", err)
	}

	setLogger(cfg.LogLevel)

	if cfg.DisableECN {
		if err := os.Setenv("QUIC_GO_DISABLE_ECN", "true"); err != nil {
			return 1, fmt.Errorf("failed to disable QUIC ECN: %w", err)
		}
	}

	slog.Info("Supervisor starting", "domain", cfg.ACMEDomain, "log_level", cfg.LogLevel.String())

	if err := initInfrastructure(); err != nil {
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
		if err := initLego(ctx, cfg, pm, aghArgs); err != nil {
			return 1, err
		}
	}

	if err := startServices(ctx, pm, aghArgs); err != nil {
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

// initInfrastructure prepares the filesystem and service configurations.
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

// initLego handles initial certificate acquisition and renewal scheduling.
// onRenew restarts AGH directly without routing through OS signals,
// removing the shell-execution surface of the --renew-hook mechanism.
func initLego(ctx context.Context, cfg *config.Config, pm *process.Manager, aghArgs []string) error {
	onRenew := func() {
		if err := pm.Restart("adguardhome", "/opt/adguardhome/AdGuardHome", aghArgs...); err != nil {
			slog.Error("Failed to restart AdGuard Home after certificate renewal", "error", err)
		}
	}
	acmeManager := acme.NewManager(cfg, onRenew)
	if err := acmeManager.EnsureCert(ctx); err != nil {
		return fmt.Errorf("failed to ensure TLS certificate: %w", err)
	}
	acmeManager.StartRenewTicker(ctx)
	return nil
}

// startServices launches unbound and AdGuard Home in the correct order.
func startServices(ctx context.Context, pm *process.Manager, aghArgs []string) error {
	if err := pm.Start("unbound", "unbound", "-d", "-c", setup.UnboundConfFile); err != nil {
		return fmt.Errorf("failed to start unbound: %w", err)
	}

	// Backoff avoids redundant DNS probes during Unbound's initialization window
	// while still catching fast starts without unnecessary delay.
	unboundReady := util.PollImmediateWithBackoff(10*time.Second, 10*time.Millisecond, 500*time.Millisecond, func() bool {
		return health.CheckDNS(ctx, "127.0.0.1:5053") == nil
	})

	if !unboundReady {
		return errors.New("unbound failed to become ready in time")
	}

	if err := pm.Start("adguardhome", "/opt/adguardhome/AdGuardHome", aghArgs...); err != nil {
		return fmt.Errorf("failed to start AdGuard Home: %w", err)
	}

	slog.Info("All critical processes running.")
	return nil
}
