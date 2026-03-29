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
	"strings"
	"syscall"
	"time"

	"github.com/webstudiobond/agh-unbound-lego/internal/acme"
	"github.com/webstudiobond/agh-unbound-lego/internal/config"
	"github.com/webstudiobond/agh-unbound-lego/internal/health"
	"github.com/webstudiobond/agh-unbound-lego/internal/process"
	"github.com/webstudiobond/agh-unbound-lego/internal/setup"
	"github.com/webstudiobond/agh-unbound-lego/internal/util"
)

func setLogger(level string) {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "info":
		l = slog.LevelInfo
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		// Ensures programmatic misconfigurations trigger immediate crash rather than silent suppression.
		fmt.Fprintf(os.Stderr, "FATAL: unsupported log level requested: %s\n", level)
		os.Exit(1)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: l,
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
	setLogger("info")

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

	slog.Info("Supervisor starting", "domain", cfg.ACMEDomain, "log_level", cfg.LogLevel)

	if err := initInfrastructure(); err != nil {
		return 1, err
	}

	pm := process.NewManager()

	if cfg.LegoEnable {
		if err := initLego(cfg); err != nil {
			return 1, err
		}
	}

	aghArgs := []string{
		"-c", filepath.Join(setup.DirAGHConf, "AdGuardHome.yaml"),
		"-w", setup.DirAGHWork,
		"--no-check-update",
		"--no-permcheck",
	}

	if err := startServices(pm, aghArgs); err != nil {
		pm.StopAll(5 * time.Second)
		return 1, err
	}

	setupSignalHandlers(pm, aghArgs)

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
func initLego(cfg *config.Config) error {
	acmeManager := acme.NewManager(cfg)
	if err := acmeManager.EnsureCert(context.Background()); err != nil {
		return fmt.Errorf("failed to ensure TLS certificate: %w", err)
	}
	acmeManager.StartRenewTicker(context.Background())
	return nil
}

// startServices launches unbound and AdGuard Home in the correct order.
func startServices(pm *process.Manager, aghArgs []string) error {
	if err := pm.Start("unbound", "unbound", "-d", "-c", setup.UnboundConfFile); err != nil {
		return fmt.Errorf("failed to start unbound: %w", err)
	}

	// Wait for Unbound to be ready instead of using a fixed sleep.
	// We'll poll its DNS port for up to 5 seconds.
	unboundReady := util.PollImmediate(50, 100*time.Millisecond, func() bool {
		return health.CheckDNS("127.0.0.1:5053") == nil
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

// setupSignalHandlers configures observers for external triggers like certificate updates.
func setupSignalHandlers(pm *process.Manager, aghArgs []string) {
	usr1Ch := make(chan os.Signal, 1)
	signal.Notify(usr1Ch, syscall.SIGUSR1)

	go func() {
		for range usr1Ch {
			slog.Info("Received SIGUSR1. TLS certificate updated. Restarting AdGuard Home...")
			if err := pm.Restart("adguardhome", "/opt/adguardhome/AdGuardHome", aghArgs...); err != nil {
				slog.Error("Failed to restart AdGuard Home", "error", err)
			}
		}
	}()
}
