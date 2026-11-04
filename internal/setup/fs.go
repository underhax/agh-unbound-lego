// Package setup handles the initial filesystem preparation and configuration bootstrapping.
package setup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Standardized directory and file paths used across the supervisor.
var (
	DirAGHWork = "/opt/adguardhome/work"
	DirAGHConf = "/opt/adguardhome/conf"
	DirUnbound = "/opt/unbound"
	DirLego    = "/opt/lego"

	UnboundConfFile    = "/opt/unbound/unbound.conf"
	UnboundDefaultConf = "/etc/unbound/unbound.conf.default"
)

func defaultCommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	// #nosec G204 - Command and arguments are internally controlled.
	return exec.CommandContext(ctx, name, args...)
}

func defaultSyncFile(f *os.File) error {
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync failed: %w", err)
	}
	return nil
}

func defaultCloseFile(f *os.File) error {
	if err := f.Close(); err != nil {
		return fmt.Errorf("close failed: %w", err)
	}
	return nil
}

func defaultWriteString(f *os.File, s string) (int, error) {
	n, err := f.WriteString(s)
	if err != nil {
		return n, fmt.Errorf("write failed: %w", err)
	}
	return n, nil
}

var (
	execCommand = defaultCommandContext
	syncFile    = defaultSyncFile
	closeFile   = defaultCloseFile
	writeString = defaultWriteString
)

func defaultRunAnchor(ctx context.Context) error {
	cmd := execCommand(ctx, "unbound-anchor", "-a", filepath.Join(DirUnbound, "root.key"))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to run unbound-anchor: %w", err)
	}
	return nil
}

var runAnchor = defaultRunAnchor

// Directories establishes the required runtime filesystem skeleton.
func Directories() error {
	dirs := []string{DirAGHWork, DirAGHConf, DirUnbound, DirLego}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}
	return nil
}

// UnboundConfig guarantees a deterministic baseline state against potential volume-mount tampering or missing configs.
func UnboundConfig() error {
	if _, err := os.Stat(UnboundConfFile); os.IsNotExist(err) {
		if err := copyFile(UnboundDefaultConf, UnboundConfFile); err != nil {
			return err
		}
	}

	return normalizeUnboundConfig()
}

func normalizeConfigLines(data []byte) string {
	var result []string
	for line := range strings.SplitSeq(string(data), "\n") {
		trimmed := strings.TrimSpace(line)

		switch {
		case strings.HasPrefix(trimmed, "port:"):
			line = "    port: 5053"
		case strings.HasPrefix(trimmed, "interface:"):
			line = "    interface: 127.0.0.1"
		}
		result = append(result, line)
	}
	return strings.Join(result, "\n")
}

func cleanupOldTempConfigs() {
	oldTmps, err := filepath.Glob(filepath.Join(filepath.Dir(UnboundConfFile), "unbound.conf.*.tmp"))
	if err != nil {
		slog.Debug("failed to glob old temp config files", "error", err)
	}
	for _, f := range oldTmps {
		if removeErr := os.Remove(f); removeErr != nil {
			slog.Debug("failed to remove old temp config file", "file", f, "error", removeErr)
		}
	}
}

func normalizeUnboundConfig() error {
	data, err := os.ReadFile(filepath.Clean(UnboundConfFile))
	if err != nil {
		return fmt.Errorf("failed to read unbound config: %w", err)
	}

	content := normalizeConfigLines(data)
	cleanupOldTempConfigs()

	tmpFile, err := os.CreateTemp(filepath.Dir(UnboundConfFile), "unbound.conf.*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temp unbound config: %w", err)
	}

	tmpPath := tmpFile.Name()
	defer func() {
		if err := os.Remove(tmpPath); err != nil && !os.IsNotExist(err) {
			slog.Debug("failed to cleanup temp unbound config", "error", err)
		}
	}()

	if _, err := writeString(tmpFile, content); err != nil {
		return fmt.Errorf("failed to write temp unbound config data: %w", errors.Join(err, closeFile(tmpFile)))
	}
	if err := syncFile(tmpFile); err != nil {
		return fmt.Errorf("failed to sync temp unbound config data: %w", errors.Join(err, closeFile(tmpFile)))
	}
	if err := closeFile(tmpFile); err != nil {
		return fmt.Errorf("failed to close temp unbound config: %w", err)
	}

	if err := os.Rename(tmpPath, UnboundConfFile); err != nil {
		return fmt.Errorf("failed to rename unbound config: %w", err)
	}

	return nil
}

// TrustAnchor initializes the DNSSEC root key for unbound.
func TrustAnchor() error {
	keyPath := filepath.Join(DirUnbound, "root.key")
	if _, err := os.Stat(keyPath); err == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := runAnchor(ctx); err != nil {
		slog.Debug("unbound-anchor bootstrap completed with non-zero exit code (expected behavior)", "error", err)
	}

	if info, err := os.Stat(keyPath); err != nil {
		return fmt.Errorf("trust anchor file missing after bootstrap attempt: %w", err)
	} else if info.Size() == 0 {
		return fmt.Errorf("trust anchor file %s was created but is empty", keyPath)
	}

	return nil
}

func copyFile(src, dst string) (err error) {
	sourceFile, err := os.Open(filepath.Clean(src))
	if err != nil {
		return fmt.Errorf("cannot open source file %s: %w", src, err)
	}
	defer func() {
		if cerr := closeFile(sourceFile); cerr != nil && err == nil {
			err = fmt.Errorf("failed to close source file: %w", cerr)
		}
	}()

	destFile, err := os.OpenFile(filepath.Clean(dst), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("cannot create destination file %s: %w", dst, err)
	}
	defer func() {
		if cerr := closeFile(destFile); cerr != nil && err == nil {
			err = fmt.Errorf("failed to close destination file: %w", cerr)
		}
	}()

	if _, err = io.Copy(destFile, sourceFile); err != nil {
		return fmt.Errorf("failed to write data to %s: %w", dst, err)
	}

	if err := syncFile(destFile); err != nil {
		return fmt.Errorf("failed to sync destination file: %w", err)
	}
	return nil
}
