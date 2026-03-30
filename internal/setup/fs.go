// Package setup handles the initial filesystem preparation and configuration bootstrapping.
package setup

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
// Then normalizes port and interface to the expected values.
func UnboundConfig() error {
	if _, err := os.Stat(UnboundConfFile); os.IsNotExist(err) {
		if err := copyFile(UnboundDefaultConf, UnboundConfFile, 0o640); err != nil {
			return err
		}
	}

	return normalizeUnboundConfig()
}

// normalizeUnboundConfig enforces the expected port and interface values.
// This prevents container failure if a user-provided config has non-standard values,
// which could happen when copying config from another project.
func normalizeUnboundConfig() error {
	data, err := os.ReadFile(UnboundConfFile)
	if err != nil {
		return fmt.Errorf("failed to read unbound config: %w", err)
	}

	var result []string
	for _, line := range strings.Split(string(data), "\n") { //nolint:modernize // strings.SplitSeq requires Go 1.24+
		trimmed := strings.TrimSpace(line)

		// Port and interface must be 5053 and 127.0.0.1 for the supervisor healthcheck to work.
		switch {
		case strings.HasPrefix(trimmed, "port:"):
			line = "    port: 5053"
		case strings.HasPrefix(trimmed, "interface:"):
			line = "    interface: 127.0.0.1"
		}
		result = append(result, line)
	}

	// Atomic write: temp file + rename prevents data loss on crash between truncate and write.
	// UnboundConfFile is a constant, safe from path traversal.
	tmpPath := UnboundConfFile + ".tmp"
	//nolint:gosec // G306: temp file 0600 is stricter than final 0640, G703: path is constant
	if err := os.WriteFile(tmpPath, []byte(strings.Join(result, "\n")), 0o600); err != nil {
		return fmt.Errorf("failed to write temp unbound config: %w", err)
	}

	if err := os.Rename(tmpPath, UnboundConfFile); err != nil {
		return fmt.Errorf("failed to rename unbound config: %w", err)
	}

	return nil
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

	// unbound-anchor returns non-zero on first-run bootstrap even on success, so the exit
	// code is not a reliable signal. File presence and size are checked immediately after.
	// #nosec G204 -- keyPath is derived from internal constants, immune to command injection.
	_ = exec.CommandContext(ctx, "unbound-anchor", "-a", keyPath).Run() //nolint:errcheck // exit code is unreliable; post-run file validation below is the actual check.

	// Validate that the anchor file was actually created and is not empty.
	// If the network is down during first boot, unbound-anchor fails silently.
	if info, err := os.Stat(keyPath); err != nil {
		return fmt.Errorf("trust anchor file missing after bootstrap attempt: %w", err)
	} else if info.Size() == 0 {
		return fmt.Errorf("trust anchor file %s was created but is empty", keyPath)
	}

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

	// O_EXCL guards against concurrent supervisor instances silently overwriting a user-customised config on shared volume mounts during parallel container startup.
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
