// Package setup handles the initial filesystem preparation and configuration bootstrapping.
package setup

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Standardized directory and file paths used across the supervisor.
const (
	DirAGHWork = "/opt/adguardhome/work"
	DirAGHConf = "/opt/adguardhome/conf"
	DirUnbound = "/opt/unbound"
	DirLego    = "/opt/lego"

	UnboundConfFile    = "/opt/unbound/unbound.conf"
	UnboundDefaultConf = "/etc/unbound/unbound.conf.default"
)

// Directories establishes the required runtime filesystem skeleton.
// Permissions are explicitly set to 0700 to restrict other users in the container.
func Directories() error {
	dirs := []string{DirAGHWork, DirAGHConf, DirUnbound, DirLego}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}
	return nil
}

// UnboundConfig checks if the operational config exists.
// If missing, it securely copies the default configuration file.
func UnboundConfig() error {
	_, err := os.Stat(UnboundConfFile)
	if err == nil {
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("failed to stat unbound config: %w", err)
	}

	return copyFile(UnboundDefaultConf, UnboundConfFile, 0o640)
}

// TrustAnchor initializes the DNSSEC root key for unbound.
// Unbound fails to start if auto-trust-anchor-file is missing.
func TrustAnchor() error {
	keyPath := filepath.Join(DirUnbound, "root.key")
	if _, err := os.Stat(keyPath); err == nil {
		return nil
	}

	// Prevent indefinite blocking if the external anchor server is unreachable.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// We ignore the error and rely on the subsequent unbound process to validate it.
	// #nosec G204 -- keyPath is derived from internal constants, immune to command injection.
	_ = exec.CommandContext(ctx, "unbound-anchor", "-a", keyPath).Run() //nolint:errcheck // unbound-anchor intentionally returns non-zero on successful bootstrap.
	return nil
}

// copyFile handles the secure transfer of file contents and permissions.
func copyFile(src, dst string, perm os.FileMode) (err error) {
	// #nosec G304 -- Paths are provided by internal constants, not user input.
	sourceFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("cannot open source file %s: %w", src, err)
	}
	defer func() {
		if cerr := sourceFile.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("failed to close source file: %w", cerr)
		}
	}()

	// O_EXCL prevents race conditions if another process is creating the file
	// #nosec G304 -- Paths are provided by internal constants, not user input.
	destFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, perm)
	if err != nil {
		return fmt.Errorf("cannot create destination file %s: %w", dst, err)
	}
	defer func() {
		if cerr := destFile.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("failed to close destination file: %w", cerr)
		}
	}()

	if _, err = io.Copy(destFile, sourceFile); err != nil {
		return fmt.Errorf("failed to write data to %s: %w", dst, err)
	}

	if err := destFile.Sync(); err != nil {
		return fmt.Errorf("failed to sync destination file: %w", err)
	}
	return nil
}
