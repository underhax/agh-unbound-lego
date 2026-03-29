// Package acme provides a wrapper around the lego CLI to manage TLS certificates
// through the ACME protocol, specifically optimized for Cloudflare DNS.
package acme

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/webstudiobond/agh-unbound-lego/internal/config"
	"github.com/webstudiobond/agh-unbound-lego/internal/setup"
	"github.com/webstudiobond/agh-unbound-lego/internal/util"
)

// Manager handles ACME certificate issuance and renewal via the lego CLI.
type Manager struct {
	cfg     *config.Config
	onRenew func()
}

// NewManager creates a new ACME manager. onRenew is called after each successful
// certificate renewal to allow the caller to reload dependent services.
// onRenew may be nil if no post-renewal action is required.
func NewManager(cfg *config.Config, onRenew func()) *Manager {
	return &Manager{cfg: cfg, onRenew: onRenew}
}

// certExists checks if the certificate file is already present on the filesystem.
func (m *Manager) certExists() bool {
	certPath := filepath.Join(setup.DirLego, "certificates", m.cfg.ACMEDomain+".crt")
	_, err := os.Stat(certPath)
	return err == nil
}

// getCertModTime returns the modification time of the certificate file, or zero time if absent.
func (m *Manager) getCertModTime() time.Time {
	certPath := filepath.Join(setup.DirLego, "certificates", m.cfg.ACMEDomain+".crt")
	info, err := os.Stat(certPath)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// buildCmd constructs the exec.Cmd for lego with required arguments and secure ENV injection.
func (m *Manager) buildCmd(ctx context.Context, action string) *exec.Cmd {
	args := []string{
		"--accept-tos",
		"--path", setup.DirLego,
		"--email", m.cfg.ACMEEmail,
		"--dns", "cloudflare",
		"--domains", m.cfg.ACMEDomain,
		"--domains", "*." + m.cfg.ACMEDomain,
		action,
	}

	// #nosec G204 - Arguments are derived from validated, internally controlled configuration.
	cmd := exec.CommandContext(ctx, "lego", args...)

	// Inject the secret token exclusively into the lego process environment.
	// We only preserve critical variables like PATH to prevent leaking other supervisor secrets.
	cmd.Env = []string{"CF_DNS_API_TOKEN=" + m.cfg.CFDNSToken}
	if path := os.Getenv("PATH"); path != "" {
		cmd.Env = append(cmd.Env, "PATH="+path)
	}
	if home := os.Getenv("HOME"); home != "" {
		cmd.Env = append(cmd.Env, "HOME="+home)
	}

	return cmd
}

// EnsureCert verifies certificate existence on startup and obtains one if missing.
func (m *Manager) EnsureCert(ctx context.Context) error {
	if m.certExists() {
		slog.Info("TLS certificate already exists", "domain", m.cfg.ACMEDomain)
		return nil
	}

	slog.Info("Obtaining initial TLS certificate", "domain", m.cfg.ACMEDomain)
	cmd := m.buildCmd(ctx, "run")

	if err := executeAndLog(ctx, cmd, "lego-run"); err != nil {
		return fmt.Errorf("lego run failed: %w", err)
	}

	slog.Info("Successfully obtained initial TLS certificate")
	return nil
}

// StartRenewTicker runs a background routine that checks for renewal daily.
func (m *Manager) StartRenewTicker(ctx context.Context) {
	go func() {
		// Once-daily polling matches lego's built-in 30-day renewal threshold.
		// More frequent checks waste ACME API quota without shortening the renewal window.
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				slog.Debug("Executing scheduled TLS certificate renewal check")
				beforeMtime := m.getCertModTime()
				cmd := m.buildCmd(ctx, "renew")

				if err := executeAndLog(ctx, cmd, "lego-renew"); err != nil {
					slog.Error("Lego renewal check encountered an error", "error", err)
					continue
				}

				// mtime comparison is reliable here because lego performs atomic certificate
				// replacement via rename(2), guaranteeing mtime advances only on actual file change.
				if m.getCertModTime().After(beforeMtime) {
					slog.Info("TLS certificate renewed, triggering dependent service reload")
					if m.onRenew != nil {
						m.onRenew()
					}
				}
			}
		}
	}()
}

// executeAndLog runs the command and streams its output directly into the structured logger.
func executeAndLog(ctx context.Context, cmd *exec.Cmd, processName string) error {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to attach stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to attach stderr pipe: %w", err)
	}

	if err = cmd.Start(); err != nil {
		return fmt.Errorf("failed to start process: %w", err)
	}

	// Guarantee all streams are fully consumed before returning,
	// preventing log truncation if the process exits faster than we read.
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		util.StreamToLog(ctx, processName, "stdout", stdout, slog.LevelInfo, nil)
	}()
	go func() {
		defer wg.Done()
		util.StreamToLog(ctx, processName, "stderr", stderr, slog.LevelWarn, nil)
	}()

	err = cmd.Wait()
	wg.Wait()

	if err != nil {
		return fmt.Errorf("process execution failed: %w", err)
	}
	return nil
}
