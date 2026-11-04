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
	"strings"
	"sync"
	"time"

	"github.com/underhax/adguardhome-unbound-lego/internal/config"
	"github.com/underhax/adguardhome-unbound-lego/internal/setup"
	"github.com/underhax/adguardhome-unbound-lego/internal/util"
)

func defaultCommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name)
	cmd.Args = append(cmd.Args, args...)
	return cmd
}

var (
	legoBin            = "lego"
	renewCheckInterval = 24 * time.Hour
	executeAndLog      = defaultExecuteAndLog
	execCommand        = defaultCommandContext
)

// SetExecuteAndLogForTest configures lego command execution mock for testing.
func SetExecuteAndLogForTest(fn func(context.Context, *exec.Cmd, string) error) func() {
	orig := executeAndLog
	executeAndLog = fn
	return func() {
		executeAndLog = orig
	}
}

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

func (m *Manager) statCert() (os.FileInfo, error) {
	info, err := os.Stat(filepath.Join(setup.DirLego, "certificates", m.cfg.ACMEDomain+".crt"))
	if err != nil {
		return nil, fmt.Errorf("failed to stat certificate file: %w", err)
	}
	return info, nil
}

func (m *Manager) certExists() bool {
	_, err := m.statCert()
	return err == nil
}

func (m *Manager) getCertModTime() time.Time {
	info, err := m.statCert()
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

func (m *Manager) buildCmd(ctx context.Context, action string) *exec.Cmd {
	args := []string{
		action,
		"--accept-tos",
		"--path", setup.DirLego,
		"--email", string(m.cfg.ACMEEmail),
		"--dns", "cloudflare",
		"--domains", m.cfg.ACMEDomain,
		"--domains", "*." + m.cfg.ACMEDomain,
		"--no-random-sleep",
		"--dns.resolvers", "1.1.1.1:53",
	}

	cmd := execCommand(ctx, legoBin)
	cmd.Args = append(cmd.Args, args...)
	cmd.Env = util.BuildCleanEnv("CF_DNS_API_TOKEN=" + string(m.cfg.CFDNSToken))

	return cmd
}

func (m *Manager) needsMigration() bool {
	pattern := filepath.Join(setup.DirLego, "accounts", "*", "*", "keys")
	matches, err := filepath.Glob(pattern)
	return err == nil && len(matches) > 0
}

// EnsureCert verifies certificate existence on startup and obtains or renews it if necessary.
func (m *Manager) EnsureCert(ctx context.Context) error {
	slog.Info("Ensuring valid TLS certificate", "domain", m.cfg.ACMEDomain)

	if m.needsMigration() {
		slog.Info("Detected v4 lego storage format, starting migration...")
		migrateCmd := execCommand(ctx, legoBin, "migrate", "--path", setup.DirLego)
		migrateCmd.Stdin = strings.NewReader("Y\n")
		if out, err := migrateCmd.CombinedOutput(); err != nil {
			slog.Debug("Lego migration skipped or failed", "error", err, "output", string(out))
		} else {
			slog.Info("Successfully migrated lego storage to v5 format", "output", string(out))
		}
	} else {
		slog.Debug("Lego storage is already v5 or fresh install, skipping migration")
	}

	cmd := m.buildCmd(ctx, "run")

	if err := executeAndLog(ctx, cmd, "lego-run"); err != nil {
		return fmt.Errorf("lego run failed: %w", err)
	}

	slog.Info("TLS certificate is ready")
	return nil
}

// StartRenewTicker aligns polling thresholds with Cloudflare API allowances and Let's Encrypt throttling.
func (m *Manager) StartRenewTicker(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(renewCheckInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				slog.Debug("Executing scheduled TLS certificate renewal check")
				beforeMtime := m.getCertModTime()
				cmd := m.buildCmd(ctx, "run")

				if err := executeAndLog(ctx, cmd, "lego-renew"); err != nil {
					slog.Error("Lego renewal check encountered an error", "error", err)
					continue
				}

				if m.getCertModTime().After(beforeMtime) {
					slog.Info("TLS certificate renewed, triggering dependent service reload")
					if m.onRenew != nil {
						m.onRenew()
					}
				}
			}
		}
	}()
	return done
}

func defaultExecuteAndLog(ctx context.Context, cmd *exec.Cmd, processName string) error {
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

	wg.Wait()
	err = cmd.Wait()

	if err != nil {
		return fmt.Errorf("process execution failed: %w", err)
	}
	return nil
}
